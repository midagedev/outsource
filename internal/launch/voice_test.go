package launch

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/midagedev/outsource/internal/runs"
)

// The lead's voice and the stop lever, end to end through the real launcher
// with a fake `claude` on PATH. Field reports behind them (2026-10-06): a
// round refused its lead's corrections as a peer's (hfdraft) while another
// accepted the same kind (eqA1-r3); a lead had to find the `claude -p` child
// by hand to stop a round; and eqA1-r2 died rc=143 with nothing on disk to say
// who sent the signal.

// Fakes of the claude CLI. Each reads the prompt off stdin, as the real one
// does, and the long-lived ones write their own pid to $FAKE_PIDFILE once they
// are ready, so a test signals only a pid its own child wrote down.
const (
	// fakeDump records what the harness was given: the prompt and the env.
	fakeDump = `#!/bin/sh
cat > "$FAKE_PROMPT_OUT"
env > "$FAKE_ENV_OUT"
printf '%s\n' '{"session_id":"sess-voice","usage":{"input_tokens":1},"total_cost_usd":0,"modelUsage":{"glm-5.3":{"inputTokens":1}}}'
exit 0
`
	// fakeCatchesTerm behaves like `claude -p` on TERM: it catches it and
	// exits 143 itself (measured on CLI 2.1.291), taking its child with it.
	fakeCatchesTerm = `#!/bin/bash
trap 'kill "$c" 2>/dev/null; exit 143' TERM
cat > /dev/null
sleep 600 & c=$!
echo $$ > "$FAKE_PIDFILE"
wait "$c"
`
	// fakeDiesByTerm has no handler: TERM ends it by signal.
	fakeDiesByTerm = `#!/bin/sh
cat > /dev/null
echo $$ > "$FAKE_PIDFILE"
exec sleep 600
`
	// fakeIgnoresTerm survives TERM (an ignored signal stays ignored across
	// exec), so only KILL ends it.
	fakeIgnoresTerm = `#!/bin/sh
trap '' TERM
cat > /dev/null
echo $$ > "$FAKE_PIDFILE"
exec sleep 600
`
)

type voiceRig struct {
	dir, spec, log, cfg, pidfile, promptOut, envOut string
	specBody                                        string
	// running is true while OutsourceMain still waits on the child, i.e.
	// while the fake's pid is unreaped and cannot have been reused.
	running atomic.Bool
}

// newVoiceRig installs a fake claude, points every lead-identity variable at
// a known value (the developer's own session exports real ones), and writes a
// spec. The registry is the package's throwaway directory (TestMain).
func newVoiceRig(t *testing.T, fake, leadSocket string) *voiceRig {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("ZAI_API_KEY", "test-key-not-a-real-credential")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "voice-test-lead")
	t.Setenv("CLAUDE_PID", "")
	t.Setenv("CLAUDE_CODE_MESSAGING_SOCKET", leadSocket)
	t.Setenv("CLAUDE_CODE_MESSAGING_TOKEN", "lead-own-messaging-token")
	g := &voiceRig{
		dir:       dir,
		spec:      filepath.Join(dir, "spec.md"),
		log:       filepath.Join(dir, "run.log"),
		cfg:       filepath.Join(dir, "cfg"),
		pidfile:   filepath.Join(dir, "child.pid"),
		promptOut: filepath.Join(dir, "prompt.txt"),
		envOut:    filepath.Join(dir, "env.txt"),
		specBody:  "# Task\nDo the voice thing.\nDONE-voice-test\n",
	}
	t.Setenv("FAKE_PIDFILE", g.pidfile)
	t.Setenv("FAKE_PROMPT_OUT", g.promptOut)
	t.Setenv("FAKE_ENV_OUT", g.envOut)
	if err := os.WriteFile(g.spec, []byte(g.specBody), 0o644); err != nil {
		t.Fatal(err)
	}
	return g
}

func (g *voiceRig) args(label string) []string {
	return []string{"--cwd", g.dir, "--spec", g.spec, "--log", g.log, "--label", label,
		"--config-dir", g.cfg, "--provider", "zai", "--foreground"}
}

func (g *voiceRig) sentinel(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(g.log + ".rc")
	if err != nil {
		t.Fatalf("no sentinel: %v", err)
	}
	return string(b)
}

// start launches the round in the background and returns its exit channel.
func (g *voiceRig) start(label string) chan int {
	done := make(chan int, 1)
	g.running.Store(true)
	go func() {
		var out, errb bytes.Buffer
		rc := OutsourceMain(g.args(label), &out, &errb)
		g.running.Store(false)
		done <- rc
	}()
	return done
}

// childPid waits for the fake to write its pid, i.e. for its handler to be
// in place.
func (g *voiceRig) childPid(t *testing.T) int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(g.pidfile); err == nil {
			if n, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && n > 0 {
				// A failing test must not leave the fake behind (one that
				// ignores TERM outlived a failed run by ten minutes,
				// 2026-10-06). Only while the round still waits on it: an
				// unreaped child's pid cannot belong to anyone else, and the
				// launcher made it a group leader, so the group is its own.
				t.Cleanup(func() {
					if g.running.Load() {
						_ = syscall.Kill(-n, syscall.SIGKILL)
					}
				})
				return n
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the fake harness never wrote its pid")
	return 0
}

func waitRC(t *testing.T, done chan int) int {
	t.Helper()
	select {
	case rc := <-done:
		return rc
	case <-time.After(30 * time.Second):
		t.Fatal("the round did not finish within 30 s")
		return 0
	}
}

func recordByLabel(t *testing.T, label string) *runs.Record {
	t.Helper()
	recs, err := runs.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range recs {
		if r.Label == label {
			return r
		}
	}
	t.Fatalf("no record labelled %s", label)
	return nil
}

func lineOf(body, key string) string {
	for _, l := range strings.Split(body, "\n") {
		if strings.HasPrefix(l, key+"=") {
			return strings.TrimPrefix(l, key+"=")
		}
	}
	return "<absent>"
}

const pinnedToken = "00112233445566778899aabbccddeeff"

func pinToken(t *testing.T) {
	t.Helper()
	old := mintLeadToken
	mintLeadToken = func() string { return pinnedToken }
	t.Cleanup(func() { mintLeadToken = old })
}

// The notice reaches the harness ahead of the spec — and only the prompt: the
// spec file on disk is byte-identical afterwards, so spec-lint and the
// done-marker check still read the spec alone. With a lead socket the notice
// names it as `uds:<socket>`; without one, only the token clause remains. Two
// launches with the same token and socket give the same prompt byte for byte.
//
// FAIL-first: with claudecode.go's Stdin back to the bare spec file, the
// prompt reads "# Task\nDo the voice thing.\n…" and the HasPrefix check fails.
func TestLeadNoticeIsInThePromptAndNotInTheSpec(t *testing.T) {
	pinToken(t)
	for _, c := range []struct{ socket, label string }{
		{"/tmp/x.sock", "voice-notice-sock"},
		{"", "voice-notice-nosock"},
	} {
		var prompts []string
		for run := 0; run < 2; run++ {
			g := newVoiceRig(t, fakeDump, c.socket)
			var out, errb bytes.Buffer
			OutsourceMain(g.args(c.label), &out, &errb)
			b, err := os.ReadFile(g.promptOut)
			if err != nil {
				t.Fatalf("%s: the fake never got a prompt: %v; stderr: %s", c.label, err, errb.String())
			}
			prompt := string(b)
			notice := leadNotice(c.socket, pinnedToken)
			if !strings.HasPrefix(prompt, "## Launcher notice: your lead\n") || prompt != notice+g.specBody {
				t.Fatalf("%s: the prompt is not notice+spec:\n%s", c.label, prompt)
			}
			if spec, _ := os.ReadFile(g.spec); string(spec) != g.specBody {
				t.Fatalf("%s: the spec file changed:\n%s", c.label, spec)
			}
			if !strings.Contains(notice, "its first line is `lead-token: "+pinnedToken+"`") {
				t.Fatalf("%s: the token clause is missing:\n%s", c.label, notice)
			}
			hasUDS := strings.Contains(notice, "`uds:/tmp/x.sock`")
			if hasUDS != (c.socket != "") || (c.socket == "" && strings.Contains(notice, "uds:")) {
				t.Fatalf("%s: the uds clause is wrong for socket %q:\n%s", c.label, c.socket, notice)
			}
			want := c.socket
			if want == "" {
				want = "none"
			}
			if got := lineOf(g.sentinel(t), "lead_socket"); got != want {
				t.Fatalf("%s: sentinel lead_socket=%s, want %s", c.label, got, want)
			}
			prompts = append(prompts, prompt)
		}
		if prompts[0] != prompts[1] {
			t.Fatalf("%s: two launches gave different prompts:\n%s\n----\n%s", c.label, prompts[0], prompts[1])
		}
	}
}

// Each launch mints its own token, 16 random bytes in hex.
//
// FAIL-first: with mintLeadToken's rand.Read removed (a constant token), the
// second launch reads the same 32 zeros and the "seen" check fails.
func TestEachLaunchMintsItsOwnToken(t *testing.T) {
	seen := map[string]bool{}
	hex32 := regexp.MustCompile(`^[0-9a-f]{32}$`)
	for i := 0; i < 2; i++ {
		g := newVoiceRig(t, fakeDump, "/tmp/x.sock")
		label := "voice-mint-" + strconv.Itoa(i)
		var out, errb bytes.Buffer
		OutsourceMain(g.args(label), &out, &errb)
		tok := recordByLabel(t, label).LeadToken
		if !hex32.MatchString(tok) || seen[tok] {
			t.Fatalf("launch %d: token %q is not a fresh 32-hex value (seen %v)", i, tok, seen)
		}
		seen[tok] = true
	}
}

// The harness child must not inherit the lead's inbox as its own: its
// SessionStart hook records CLAUDE_CODE_MESSAGING_SOCKET as the ROUND's
// inbox, so an inherited one would route the panel's notes back to the lead.
//
// FAIL-first: with nestedEnv's filter disabled, the dump carries the lead's
// CLAUDE_CODE_MESSAGING_* lines (observed first:
// CLAUDE_CODE_MESSAGING_TOKEN=lead-own-messaging-token) and this fails.
func TestHarnessChildDoesNotInheritTheLeadInbox(t *testing.T) {
	g := newVoiceRig(t, fakeDump, "/tmp/x.sock")
	var out, errb bytes.Buffer
	OutsourceMain(g.args("voice-env"), &out, &errb)
	b, err := os.ReadFile(g.envOut)
	if err != nil {
		t.Fatalf("the fake never dumped its env: %v; stderr: %s", err, errb.String())
	}
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "CLAUDE_CODE_MESSAGING_SOCKET=") || strings.HasPrefix(l, "CLAUDE_CODE_MESSAGING_TOKEN=") {
			t.Fatalf("the harness child inherited the lead's inbox: %s", l)
		}
	}
	if !strings.Contains(string(b), "OUTSOURCE_ROUND=1") {
		t.Fatalf("the nesting marker went missing from the child env:\n%s", b)
	}
}

// The token lives in the registry record (owner-only) and in `runs json`,
// where the panel reads it — and never in the sentinel, which is 0644 and
// outlives the record.
//
// FAIL-first: without the SetLead call the record's leadToken is "" and the
// first check fails; with cmdStart back at 0644 AND SetLead's chmod removed
// (either one alone still closes the file off) the mode check fails.
func TestLeadTokenIsInTheRecordAndJSONButNotTheSentinel(t *testing.T) {
	pinToken(t)
	g := newVoiceRig(t, fakeDump, "/tmp/x.sock")
	var out, errb bytes.Buffer
	OutsourceMain(g.args("voice-token"), &out, &errb)
	rec := recordByLabel(t, "voice-token")
	if rec.LeadToken != pinnedToken || rec.LeadSocket != "/tmp/x.sock" {
		t.Fatalf("record lead fields: socket %q token %q", rec.LeadSocket, rec.LeadToken)
	}
	fi, err := os.Stat(filepath.Join(runs.Dir(), rec.ID+".run"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("the record holding the token is %v, want -rw-------", fi.Mode().Perm())
	}
	var js bytes.Buffer
	if rc := runs.Main([]string{"json"}, &js, &errb); rc != 0 {
		t.Fatalf("runs json rc=%d", rc)
	}
	var rows []map[string]any
	if err := json.Unmarshal(js.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range rows {
		if row["id"] == rec.ID {
			found = row["leadToken"] == pinnedToken && row["leadSocket"] == "/tmp/x.sock"
		}
	}
	if !found {
		t.Fatalf("runs json does not carry the lead fields for %s:\n%s", rec.ID, js.String())
	}
	if strings.Contains(g.sentinel(t), pinnedToken) {
		t.Fatalf("the sentinel carries the token:\n%s", g.sentinel(t))
	}
}

// `runs stop` on a live round: the child (a claude-like fake that catches TERM
// and exits 143) is signalled, the sentinel says who stopped it, and the
// one-line view renders ■.
//
// FAIL-first: without the stop-request lookup in noteEnding the sentinel has
// no stopped_by line.
func TestRunsStopStopsTheHarnessChild(t *testing.T) {
	g := newVoiceRig(t, fakeCatchesTerm, "/tmp/x.sock")
	done := g.start("voice-stop")
	child := g.childPid(t)
	rec := recordByLabel(t, "voice-stop")
	if rec.ChildPid != strconv.Itoa(child) {
		t.Fatalf("record childPid=%q, the fake says %d", rec.ChildPid, child)
	}
	var so, se bytes.Buffer
	if rc := runs.Main([]string{"stop", "voice-stop", "--reason", "wrong premise"}, &so, &se); rc != 0 {
		t.Fatalf("runs stop rc=%d\nstdout: %s\nstderr: %s", rc, so.String(), se.String())
	}
	if !strings.Contains(so.String(), "rc=143") {
		t.Fatalf("runs stop did not print the sentinel's rc line:\n%s", so.String())
	}
	if rc := waitRC(t, done); rc != 143 {
		t.Fatalf("round rc=%d, want 143 (the child's own exit, kept as it is)", rc)
	}
	body := g.sentinel(t)
	for k, want := range map[string]string{
		"rc": "143", "stopped_by": "lead", "stop_reason": "wrong premise",
		"harness_signal": "TERM", "signal_source": "lead-stop",
	} {
		if got := lineOf(body, k); got != want {
			t.Fatalf("sentinel %s=%s, want %s\n%s", k, got, want, body)
		}
	}
	var line bytes.Buffer
	runs.Main([]string{"line"}, &line, &se)
	if !strings.Contains(line.String(), "■voice-stop stopped") {
		t.Fatalf("runs line does not render the stop: %s", line.String())
	}
}

// A child that ignores TERM is KILLed after --kill-after, and the sentinel
// names the signal that actually ended it.
//
// FAIL-first: with the KILL escalation removed, runs stop waits out its 30 s
// sentinel window and exits 1 while the fake sleeps on.
func TestRunsStopEscalatesToKill(t *testing.T) {
	g := newVoiceRig(t, fakeIgnoresTerm, "")
	done := g.start("voice-kill")
	g.childPid(t)
	var so, se bytes.Buffer
	if rc := runs.Main([]string{"stop", "voice-kill", "--kill-after", "1"}, &so, &se); rc != 0 {
		t.Fatalf("runs stop rc=%d\nstdout: %s\nstderr: %s", rc, so.String(), se.String())
	}
	if !strings.Contains(so.String(), "sent KILL to process group") {
		t.Fatalf("runs stop did not say it escalated:\n%s", so.String())
	}
	waitRC(t, done)
	body := g.sentinel(t)
	if lineOf(body, "harness_signal") != "KILL" || lineOf(body, "signal_source") != "lead-stop" || lineOf(body, "stopped_by") != "lead" {
		t.Fatalf("sentinel after escalation:\n%s", body)
	}
}

// A TERM nobody on record sent: the sentinel says external, for a child that
// dies by the signal (rc -1, Go's exit code for a signalled child) and for one
// that catches it and exits 143 (the real CLI's shape) alike — rc kept as is.
//
// FAIL-first: with harnessSignal reading only the wait status, the 143 case
// has no harness_signal line.
func TestExternalTermIsRecordedAsExternal(t *testing.T) {
	for _, c := range []struct {
		fake, label string
		rc          int
	}{
		{fakeDiesByTerm, "voice-ext-dies", -1},
		{fakeCatchesTerm, "voice-ext-catch", 143},
	} {
		g := newVoiceRig(t, c.fake, "")
		done := g.start(c.label)
		child := g.childPid(t)
		if err := syscall.Kill(child, syscall.SIGTERM); err != nil {
			t.Fatal(err)
		}
		if rc := waitRC(t, done); rc != c.rc {
			t.Fatalf("%s: rc=%d, want %d", c.label, rc, c.rc)
		}
		body := g.sentinel(t)
		if lineOf(body, "harness_signal") != "TERM" || lineOf(body, "signal_source") != "external" {
			t.Fatalf("%s: sentinel:\n%s", c.label, body)
		}
		if lineOf(body, "stopped_by") != "<absent>" {
			t.Fatalf("%s: an external kill must not read as a lead stop:\n%s", c.label, body)
		}
		rec := recordByLabel(t, c.label)
		if rec.HarnessSignal != "TERM" || rec.SignalSource != "external" {
			t.Fatalf("%s: record harnessSignal=%q signalSource=%q", c.label, rec.HarnessSignal, rec.SignalSource)
		}
		var line, se bytes.Buffer
		runs.Main([]string{"line"}, &line, &se)
		if !strings.Contains(line.String(), "✗"+c.label+" TERM ext") {
			t.Fatalf("%s: runs line does not render the external kill: %s", c.label, line.String())
		}
	}
}

// The shared preamble points rounds at the notice by its heading, so the two
// spellings must not drift apart: a renamed heading would leave the pointer
// naming a section no prompt has. FAIL-first: with the preamble's quote cut
// to "Launcher notice", this fails naming the heading.
func TestSpecPreambleNamesTheNoticeHeading(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "skills", "outsource", "references", "spec-preamble.md"))
	if err != nil {
		t.Fatal(err)
	}
	heading := strings.TrimPrefix(strings.SplitN(leadNotice("", "t"), "\n", 2)[0], "## ")
	if !strings.Contains(strings.Join(strings.Fields(string(b)), " "), `"`+heading+`"`) {
		t.Fatalf("spec-preamble.md does not name the notice heading %q", heading)
	}
}
