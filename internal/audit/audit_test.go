package audit

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/midagedev/outsource/internal/runs"
)

// The fixtures under testdata/ are synthetic: made-up paths under /tmp/round,
// made-up ids, neutral command text. They copy the SHAPES measured from real
// transcripts on 2026-10-06 (split assistant responses, hook-wrapped guard
// refusals, mod denials, the three-fold cross-session message, agy step
// updates) and none of their content.

func kinds(t *Trail, kind string) []Event {
	var out []Event
	for _, e := range t.Events {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

// Test 1: the main-loop claude-code fixture. Two assistant message ids spread
// over four lines must yield exactly two model_requests — the transcript
// splits one response into several lines, and a count by line doubles it.
func TestClaudeMainCountsRequestsById(t *testing.T) {
	tr, err := ParseTrail("testdata/claude-main.jsonl", "claude-code")
	if err != nil {
		t.Fatal(err)
	}
	if got := len(kinds(tr, KindModelRequest)); got != 2 {
		t.Fatalf("model_request: want 2 (by message id, not by line), got %d", got)
	}
	var bash, edit int
	for _, e := range kinds(tr, KindFunctionCall) {
		switch e.Name {
		case "Bash":
			bash++
		case "Edit":
			edit++
		}
	}
	if bash != 3 || edit != 1 {
		// Three Bash calls in the fixture: the suite run, the git commit the
		// guard denies, and the echo the mod denies.
		t.Fatalf("function_call: want Bash=3 Edit=1, got Bash=%d Edit=%d", bash, edit)
	}
}

func TestClaudeMainResponsesAndDenials(t *testing.T) {
	tr, err := ParseTrail("testdata/claude-main.jsonl", "claude-code")
	if err != nil {
		t.Fatal(err)
	}
	resps := map[string]Event{}
	for _, e := range kinds(tr, KindFunctionResponse) {
		resps[e.CallID] = e
	}
	for _, id := range []string{"call_TESTb0001", "call_TESTb0002", "call_TESTb0003", "call_TESTb0004"} {
		r, ok := resps[id]
		if !ok {
			t.Fatalf("no function_response for %s", id)
		}
		if r.SHA256 == "" || r.Bytes == 0 || r.Head == "" {
			t.Fatalf("response for %s missing bytes/sha256/head: %+v", id, r)
		}
	}
	if resps["call_TESTb0003"].IsError == nil || !*resps["call_TESTb0003"].IsError {
		t.Fatal("the git-guard-denied call must be an error response")
	}

	by := map[string]Event{}
	for _, e := range kinds(tr, KindPermissionDecision) {
		if e.Decision != "deny" {
			t.Fatalf("decision: want deny, got %q", e.Decision)
		}
		by[e.By] = e
	}
	if _, ok := by["git-guard"]; !ok {
		t.Fatalf("want a permission_decision by=git-guard; got %v", by)
	}
	if _, ok := by["mod"]; !ok {
		t.Fatalf("want a permission_decision by=mod; got %v", by)
	}
	// The guard's block arrives wrapped in the hook-error line; the specific
	// recogniser must win, or every guard denial would be blamed on "hook".
	if got := by["git-guard"].CallID; got != "call_TESTb0003" {
		t.Fatalf("git-guard denial on the wrong call: %s", got)
	}
	if got := by["mod"].CallID; got != "call_TESTb0004" {
		t.Fatalf("mod denial on the wrong call: %s", got)
	}
	// Both error results were recognised denials, so none is a bare error.
	if n := len(kinds(tr, KindError)); n != 0 {
		t.Fatalf("recognised denials must not also be error events; got %d", n)
	}
}

// The same cross-session message arrives as a queue-operation enqueue, as a
// queued_command attachment and as a queue-operation remove: one event.
func TestClaudeMainMessageDedup(t *testing.T) {
	tr, err := ParseTrail("testdata/claude-main.jsonl", "claude-code")
	if err != nil {
		t.Fatal(err)
	}
	msgs := kinds(tr, KindMessageReceived)
	if len(msgs) != 1 {
		t.Fatalf("message_received: want 1 (enqueue + queued_command + remove are one message), got %d", len(msgs))
	}
	m := msgs[0]
	if m.From != "uds:/tmp/cc-socks/424242.sock" || m.FromName != "lead-session-test" {
		t.Fatalf("from/from_name: got %q / %q", m.From, m.FromName)
	}
	if !strings.Contains(m.Head, "TESTTOKEN-MSG-1") {
		t.Fatalf("head lost the body: %q", m.Head)
	}
	if m.TextSHA256 == "" {
		t.Fatal("text_sha256 missing")
	}
}

// Test 2: subagent events carry the file-name id, and a subagent that answered
// on a model other than the spawn's request is MODEL DRIFT in the subagent
// counts (the fixture asks opus and runs glm-5.3 — the measured spawn-pin
// shape).
func TestClaudeSubagentTaggingAndDrift(t *testing.T) {
	tr, err := ParseTrail("testdata/claude-sub.jsonl", "claude-code")
	if err != nil {
		t.Fatal(err)
	}
	spawns := kinds(tr, KindSubagentSpawn)
	if len(spawns) != 1 || spawns[0].SubagentType != "general-purpose" || spawns[0].Model != "opus" {
		t.Fatalf("subagent_spawn: %+v", spawns)
	}
	var subReqs int
	for _, e := range kinds(tr, KindModelRequest) {
		if e.Agent == "agent-testsub00000001" && e.Model == "glm-5.3" {
			subReqs++
		}
	}
	if subReqs != 2 {
		t.Fatalf("subagent model_request: want 2, got %d", subReqs)
	}
	if len(tr.Subagents) != 1 || tr.Subagents[0].SpawnModel != "opus" {
		t.Fatalf("subagent link through meta toolUseId: %+v", tr.Subagents)
	}
	var buf bytes.Buffer
	modelsSection(tr, &runs.Record{Model: "glm-5.3"}, &buf)
	if !strings.Contains(buf.String(), "MODEL DRIFT (spawn asked opus)") {
		t.Fatalf("subagent drift not marked:\n%s", buf.String())
	}
}

// Test 3: the agy fixture. Tool steps become call+response pairs with state;
// an ERROR step is a response plus an error event; agent_response DONE steps
// are model requests carrying usage.
func TestAgyFixture(t *testing.T) {
	tr, err := ParseTrail("testdata/agy.log", "agy")
	if err != nil {
		t.Fatal(err)
	}
	var calls, done, errored int
	for _, e := range tr.Events {
		switch {
		case e.Kind == KindFunctionCall && e.Name == "run_command":
			calls++
			if e.InputSHA256 == "" {
				t.Fatal("agy function_call missing input_sha256")
			}
		case e.Kind == KindFunctionResponse && e.State == "DONE":
			done++
			if e.Bytes != 0 || e.Head != "" {
				t.Fatal("agy function_response must not carry result text fields")
			}
		case e.Kind == KindFunctionResponse && e.State == "ERROR":
			errored++
		}
	}
	if calls != 2 || done != 3 || errored != 1 {
		// DONE counts every tool: two run_command plus the write_to_file.
		t.Fatalf("run_command calls=%d DONE=%d ERROR=%d; want 2/3/1", calls, done, errored)
	}
	errs := kinds(tr, KindError)
	if len(errs) != 1 || !strings.Contains(errs[0].Head, "no such file or directory") {
		t.Fatalf("the ERROR step must surface one error event: %+v", errs)
	}
	var reqs int
	for _, e := range kinds(tr, KindModelRequest) {
		if e.Model != "gemini-3.8-flash-high" {
			t.Fatalf("model: %q", e.Model)
		}
		if e.InputTokens == 0 || e.OutputTokens == 0 {
			t.Fatalf("usage not carried: %+v", e)
		}
		reqs++
	}
	if reqs != 2 {
		t.Fatalf("model_request: want 2, got %d", reqs)
	}
	// The write tool and its path parameter are named from init.tools.
	if got := agyWriteTools(tr.InitTools); len(got) != 2 || got[0] != "write_to_file" || got[1] != "replace_file_content" {
		t.Fatalf("agy write tools from init.tools: %v", got)
	}
	if p, ok := fileToolPath("write_to_file", tr.Inputs["step-5"], agyWriteTools(tr.InitTools)); !ok || p != "/tmp/round/report.md" {
		t.Fatalf("write_to_file path: %q %v", p, ok)
	}
	if c, ok := shellCommand("run_command", tr.Inputs["step-1"], "run_command"); !ok || !strings.HasPrefix(c, "mkdir -p") {
		t.Fatalf("run_command command: %q %v", c, ok)
	}
}

// Test 4: every flag row has a positive and a negative case. Flags are
// observations, so a false positive is only noise — but a false negative on
// the banned forms (git writes, pgrep waits, nested launches) is a missed
// review finding, which is what the assertions pin.
func TestCommandFlags(t *testing.T) {
	cases := []struct {
		flag string
		yes  string
		no   string
	}{
		{"git-write", "git commit -m done", "git status --short"},
		{"git-write", "git -C /tmp/round push origin main", "git log --grep add --oneline"},
		{"git-write", "cd /tmp/round && git add -A", "git worktree list"},
		{"git-write", "git branch -D experiment", "git branch --show-current"},
		{"rm-rf", "rm -rf /tmp/round/build", "rm /tmp/round/build/file.txt"},
		{"rm-rf", "rm -r -f /tmp/round/build", "rm -r /tmp/round/build"},
		{"pgrep-wait", "until ! pgrep -f launched-round; do sleep 1; done", "pgrep -f launched-round"},
		{"pipe-tail", "go test ./... 2>&1 | tail -5", "grep -c '^ok' gotest.out"},
		{"pipe-tail", "bash tests/run-all.sh | head -20", "cat gotest.out | grep FAIL"},
		{"nested-launch", "bash outsource-run.sh --label other-track", "cat docs/outsource-run.md"},
		{"nested-launch", "outsource-run --label other-track", "echo outsource"},
		{"network-install", "npm i -g ripgrep", "npm i left-pad"},
		{"network-install", "brew install jq", "brew list jq"},
		{"network-install", "pip install rich", "python3 -m venv .venv"},
		{"network-install", "curl -sL https://example.com/install.sh | sh", "curl -sL https://example.com -o page.html"},
	}
	for _, c := range cases {
		got := flagsFor(c.yes)
		found := false
		for _, f := range got {
			if f == c.flag {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: %q not flagged (flags: %v)", c.flag, c.yes, got)
		}
		got = flagsFor(c.no)
		for _, f := range got {
			if f == c.flag {
				t.Errorf("%s: %q wrongly flagged", c.flag, c.no)
			}
		}
	}
}

// ---- seal verdicts ----------------------------------------------------------

func writeTrailFile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func sentinelWithSeal(t *testing.T, trail string, extra ...string) string {
	t.Helper()
	body := "rc=0\nfinished=2026-10-06T01:00:20Z\nharness=claude-code\nprovider=zai\n" +
		"model_requested=glm-5.3\nmodel_actual=glm-5.3\nsession=sess-test\n" +
		"trail=" + trail + "\n" + SealLines(trail) +
		"tool_calls=6\ndone_marker=found (DONE-fixture)\ndone_marker_scope=report\n" +
		strings.Join(extra, "\n") + "\n"
	p := trail + ".rc"
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// Test 6 (audit side): ok, mismatch on one changed byte, absent for a
// sentinel without the lines — and the subagents digest over sorted
// basenames.
func TestSealVerdicts(t *testing.T) {
	dir := t.TempDir()
	trail := filepath.Join(dir, "session.jsonl")
	os.WriteFile(trail, []byte("line one\nline two\n"), 0o644)

	// ok
	rc := sentinelWithSeal(t, trail)
	s, m := ReadSeal(rc)
	if s.Verdict != "ok" || m == nil {
		t.Fatalf("want ok, got %+v", s)
	}

	// mismatch: one byte INSIDE the sealed prefix changes after the fact
	os.WriteFile(trail, []byte("line one\nline twX\n"), 0o644)
	s, _ = ReadSeal(rc)
	if s.Verdict != "mismatch" || !strings.Contains(s.Detail, "trail_sha256") {
		t.Fatalf("tampered trail: want mismatch naming trail_sha256, got %+v", s)
	}

	// extended: a resumed session appends to the same transcript; the sealed
	// prefix still hashes to trail_sha256, so it is not tampering.
	os.WriteFile(trail, []byte("line one\nline two\nline three\n"), 0o644)
	s, _ = ReadSeal(rc)
	if s.Verdict != "extended" {
		t.Fatalf("appended trail: want extended, got %+v", s)
	}
	if !strings.Contains(s.Detail, "11 bytes appended after the seal; sealed prefix intact") {
		t.Fatalf("extended detail: %q", s.Detail)
	}

	// mismatch: the trail is SHORTER than the seal recorded — a truncation,
	// not an append.
	os.WriteFile(trail, []byte("line one\n"), 0o644)
	s, _ = ReadSeal(rc)
	if s.Verdict != "mismatch" || !strings.Contains(s.Detail, "shorter than the seal recorded") {
		t.Fatalf("truncated trail: %+v", s)
	}

	// absent: a sentinel that predates the seal
	old := filepath.Join(dir, "old.rc")
	os.WriteFile(old, []byte("rc=0\ntrail="+trail+"\ntool_calls=1\n"), 0o644)
	s, _ = ReadSeal(old)
	if s.Verdict != "absent" {
		t.Fatalf("pre-seal sentinel: want absent, got %+v", s)
	}

	// absent: no sentinel at all (a running round)
	s, m = ReadSeal(filepath.Join(dir, "none.rc"))
	if s.Verdict != "absent" || m != nil {
		t.Fatalf("missing sentinel: want absent/nil, got %+v %v", s, m != nil)
	}
}

func TestSealSubagentsSortedBasenames(t *testing.T) {
	dir := t.TempDir()
	trail := filepath.Join(dir, "session.jsonl")
	os.WriteFile(trail, []byte("main\n"), 0o644)
	sub := filepath.Join(dir, "session", "subagents")
	os.MkdirAll(sub, 0o755)
	os.WriteFile(filepath.Join(sub, "b-agent.jsonl"), []byte("B\n"), 0o644)
	os.WriteFile(filepath.Join(sub, "a-agent.jsonl"), []byte("A\n"), 0o644)
	os.WriteFile(filepath.Join(sub, "notes.txt"), []byte("not a transcript\n"), 0o644)

	rc := sentinelWithSeal(t, trail)
	body, _ := os.ReadFile(rc)
	var sealed string
	for _, l := range strings.Split(string(body), "\n") {
		if strings.HasPrefix(l, "subagents_sha256=") {
			sealed = strings.TrimPrefix(l, "subagents_sha256=")
		}
	}
	if sealed == "" {
		t.Fatal("no subagents_sha256 line for a trail with subagents/")
	}
	// The digest is over "<basename> <sha256>\n" lines sorted by basename —
	// b- before a- in creation order must not leak into it.
	want := sha256hex([]byte("a-agent.jsonl " + sha256hex([]byte("A\n")) + "\n" +
		"b-agent.jsonl " + sha256hex([]byte("B\n")) + "\n"))
	if sealed != want {
		t.Fatalf("subagents digest: want %s got %s", want, sealed)
	}
	s, _ := ReadSeal(rc)
	if s.Verdict != "ok" {
		t.Fatalf("subagents seal: %+v", s)
	}
	// Tampering a subagent transcript is a mismatch too.
	os.WriteFile(filepath.Join(sub, "a-agent.jsonl"), []byte("A tampered\n"), 0o644)
	s, _ = ReadSeal(rc)
	if s.Verdict != "mismatch" || !strings.Contains(s.Detail, "subagents") {
		t.Fatalf("tampered subagent: %+v", s)
	}

	// A resumed session appends to the trail AND can add subagent files: the
	// trail's sealed prefix is intact, so the verdict stays extended with the
	// subagent difference named — not a mismatch.
	os.WriteFile(filepath.Join(sub, "a-agent.jsonl"), []byte("A\n"), 0o644)
	os.WriteFile(trail, []byte("main\nmore\n"), 0o644)
	os.WriteFile(filepath.Join(sub, "z-new.jsonl"), []byte("new\n"), 0o644)
	s, _ = ReadSeal(rc)
	if s.Verdict != "extended" || !strings.Contains(s.Detail, "subagents_sha256 differs") {
		t.Fatalf("resumed round with a new subagent: want extended naming subagents, got %+v", s)
	}
}

// ---- the tree cross-check ---------------------------------------------------

// Test 5: one file written by a tool and changed, one changed only by a shell
// command, one written by a tool and reverted — each lands in its own list.
func TestTreeCrossCheck(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}
	os.WriteFile(filepath.Join(dir, "tool-changed.go"), []byte("base\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "shell-changed.go"), []byte("base\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "tool-reverted.go"), []byte("base\n"), 0o644)
	run("init", "-q")
	run("config", "user.email", "audit-test@example.invalid")
	run("config", "user.name", "audit test")
	run("add", "-A")
	run("commit", "-q", "-m", "base")
	os.WriteFile(filepath.Join(dir, "tool-changed.go"), []byte("changed by tool\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "shell-changed.go"), []byte("changed by shell\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "tool-reverted.go"), []byte("base\n"), 0o644) // written, then reverted

	tc := crossCheck(dir, "", []string{
		filepath.Join(dir, "tool-changed.go"),
		filepath.Join(dir, "tool-reverted.go"),
		"/definitely/outside/elsewhere.go", // outside cwd: not part of the cross-check
	})
	if tc == nil {
		t.Fatal("crossCheck skipped a git work tree")
	}
	if len(tc.WrittenChanged) != 1 || tc.WrittenChanged[0] != "tool-changed.go" {
		t.Fatalf("written-and-changed: %v", tc.WrittenChanged)
	}
	if len(tc.ChangedNotWritten) != 1 || tc.ChangedNotWritten[0] != "shell-changed.go" {
		t.Fatalf("changed-without-file-tool: %v", tc.ChangedNotWritten)
	}
	if len(tc.WrittenUnchanged) != 1 || tc.WrittenUnchanged[0] != "tool-reverted.go" {
		t.Fatalf("written-but-unchanged: %v", tc.WrittenUnchanged)
	}

	// --base: committed work compares against the base revision, not the index.
	run("add", "-A")
	run("commit", "-q", "-m", "work")
	base := "HEAD~1"
	tc = crossCheck(dir, base, []string{filepath.Join(dir, "tool-changed.go")})
	if len(tc.WrittenChanged) != 1 || tc.WrittenChanged[0] != "tool-changed.go" {
		t.Fatalf("with --base, written-and-changed: %v", tc.WrittenChanged)
	}
	if len(tc.ChangedNotWritten) != 1 || tc.ChangedNotWritten[0] != "shell-changed.go" {
		t.Fatalf("with --base, changed-without-file-tool: %v", tc.ChangedNotWritten)
	}

	// Not a work tree at all: skipped, and distinguishable from empty lists.
	tc = crossCheck(t.TempDir(), "", []string{"x"})
	if tc != nil {
		t.Fatal("a plain directory must skip the cross-check, not report empty lists")
	}
}

// ---- exit codes and the end-to-end summary ----------------------------------

// writeRecord registers a round in a private runs dir the way the launcher
// does, so Main resolves it through the real registry path.
func writeRecord(t *testing.T, id string, fields map[string]string) {
	t.Helper()
	var b strings.Builder
	fmt.Fprintf(&b, "id=%s\n", id)
	for k, v := range fields {
		fmt.Fprintf(&b, "%s=%s\n", k, v)
	}
	if err := os.WriteFile(filepath.Join(runs.Dir(), id+".run"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Test 7: the exit codes — 64 ambiguous, 65 unknown, 69 unsupported harness.
func TestExitCodes(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("OUTSOURCE_RUNS_DIR", dir)
	trail := writeTrailFile(t, `{"type":"assistant","timestamp":"2026-10-06T01:00:01.000Z","message":{"role":"assistant","id":"m1","model":"glm-5.3","content":[{"type":"text","text":"hi"}]}}`)

	writeRecord(t, "1790000001-111", map[string]string{
		"pid": "999991", "label": "twin", "provider": "zai", "harness": "claude-code",
		"model": "glm-5.3", "cwd": "/tmp/round", "log": filepath.Join(dir, "a.log"),
		"trail": trail, "startedAt": "1790000001", "rc": "0",
	})
	writeRecord(t, "1790000002-222", map[string]string{
		"pid": "999992", "label": "twin", "provider": "zai", "harness": "claude-code",
		"model": "glm-5.3", "cwd": "/tmp/round", "log": filepath.Join(dir, "b.log"),
		"trail": trail, "startedAt": "1790000002", "rc": "0",
	})
	writeRecord(t, "1790000003-333", map[string]string{
		"pid": "999993", "label": "crushy", "provider": "zai", "harness": "crush",
		"model": "glm-5.3", "cwd": "/tmp/round", "log": filepath.Join(dir, "c.log"),
		"trail": trail, "startedAt": "1790000003", "rc": "0",
	})
	writeRecord(t, "1790000004-444", map[string]string{
		"pid": "999994", "label": "notrail", "provider": "zai", "harness": "claude-code",
		"model": "glm-5.3", "cwd": "/tmp/round", "log": filepath.Join(dir, "d.log"),
		"startedAt": "1790000004", "rc": "0",
	})

	run := func(args ...string) (int, string, string) {
		var out, errb bytes.Buffer
		rc := Main(args, &out, &errb)
		return rc, out.String(), errb.String()
	}

	if rc, _, err := run("twin"); rc != ExitUsage || !strings.Contains(err, "name one of these") {
		t.Fatalf("ambiguous label: want 64 + candidates, got %d %q", rc, err)
	}
	if rc, _, err := run("1799999999-000"); rc != ExitNoTrail {
		t.Fatalf("unknown id: want 65, got %d %q", rc, err)
	}
	if rc, _, err := run("1790000003-333"); rc != ExitUnsupported || !strings.Contains(err, "harness crush is not supported yet (claude-code, agy)") {
		t.Fatalf("crush record: want 69 + refusal, got %d %q", rc, err)
	}
	if rc, _, err := run("1790000004-444"); rc != ExitNoTrail || !strings.Contains(err, "has not revealed a trail") {
		t.Fatalf("no trail: want 65, got %d %q", rc, err)
	}
}

// The end-to-end summary: a sealed round whose report renders every section,
// the JSON lines parse back as events, and the events view honours COLUMNS.
func TestSummaryEndToEnd(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("OUTSOURCE_RUNS_DIR", filepath.Join(dir, "runs"))
	os.MkdirAll(filepath.Join(dir, "runs"), 0o755)
	cwd := filepath.Join(dir, "cwd")
	os.MkdirAll(cwd, 0o755)
	trail := filepath.Join(dir, "session.jsonl")
	body, err := os.ReadFile("testdata/claude-main.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(trail, body, 0o644)
	log := filepath.Join(dir, "round.log")
	os.WriteFile(log, []byte("report body\nDONE-fixture-main\n"), 0o644)
	sentinelWithSeal(t, trail)
	if err := os.Rename(trail+".rc", log+".rc"); err != nil {
		t.Fatal(err)
	}

	writeRecord(t, "1790000005-555", map[string]string{
		"pid": "999995", "label": "fixture-main", "provider": "zai", "harness": "claude-code",
		"model": "glm-5.3", "cwd": cwd, "log": log, "trail": trail,
		"startedAt": "1790000005", "finishedAt": "1790000025", "rc": "0",
	})

	var out, errb bytes.Buffer
	rc := Main([]string{"fixture-main"}, &out, &errb)
	if rc != 0 {
		t.Fatalf("rc=%d stderr=%s", rc, errb.String())
	}
	s := out.String()
	for _, want := range []string{
		"── audit fixture-main",
		"seal ok",
		"glm-5.3", "2 requests",
		"Bash", "3 calls",
		"git commit -m done", "[git-write]",
		"/tmp/round/main.go",
		"OUTSIDE-CWD", // /tmp/round/main.go is outside the record's cwd
		"Tree cross-check:",
		"not a git work tree",
		"lead-session-test", "TESTTOKEN-MSG-1",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("summary missing %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "MODEL DRIFT (requested") {
		t.Error("main loop on the requested model must not be marked MODEL DRIFT")
	}

	// --json: one object per line, seq from 1, termination last.
	out.Reset()
	if rc := Main([]string{"fixture-main", "--json"}, &out, &errb); rc != 0 {
		t.Fatalf("json rc=%d %s", rc, errb.String())
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) < 10 {
		t.Fatalf("json too short: %d lines", len(lines))
	}
	var last Event
	for i, l := range lines {
		var e Event
		if err := json.Unmarshal([]byte(l), &e); err != nil {
			t.Fatalf("line %d not JSON: %v", i, err)
		}
		if e.Seq != i+1 {
			t.Fatalf("seq not from 1 in order: line %d has seq %d", i, e.Seq)
		}
		last = e
	}
	if last.Kind != KindTermination || last.RC != 0 || last.Seal != "ok" || last.DoneMarker != "found (DONE-fixture)" {
		t.Fatalf("termination event: %+v", last)
	}

	// --events: same count, cut to COLUMNS.
	t.Setenv("COLUMNS", "60")
	out.Reset()
	if rc := Main([]string{"fixture-main", "--events"}, &out, &errb); rc != 0 {
		t.Fatalf("events rc=%d %s", rc, errb.String())
	}
	evLines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(evLines) != len(lines) {
		t.Fatalf("events lines %d, json lines %d", len(evLines), len(lines))
	}
	for _, l := range evLines {
		if len([]rune(l)) > 61 {
			t.Fatalf("events line not clipped to 60 columns: %q", l)
		}
	}

	// The seal is live: one byte INSIDE the sealed prefix changes, the exit
	// code says 3. (An append after the seal is the resumed-session shape and
	// reads extended, exit 0 — checked just below.)
	flipped := append([]byte{}, body...)
	flipped[12] ^= 1
	os.WriteFile(trail, flipped, 0o644)
	var out2 bytes.Buffer
	if rc := Main([]string{"fixture-main"}, &out2, &errb); rc != ExitSealMismatch {
		t.Fatalf("tampered trail: want exit 3, got %d", rc)
	}
	if !strings.HasPrefix(strings.SplitN(out2.String(), "\n", 2)[0], "── audit fixture-main · SEAL MISMATCH") {
		t.Fatalf("mismatch must lead the header line:\n%s", out2.String())
	}

	// A pure append after the seal — the resumed-session shape — exits 0 with
	// the header saying extended, not mismatch.
	os.WriteFile(trail, body, 0o644) // restore the sealed content
	os.WriteFile(trail, append(body, []byte("resumed later\n")...), 0o644)
	var out3 bytes.Buffer
	if rc := Main([]string{"fixture-main"}, &out3, &errb); rc != 0 {
		t.Fatalf("appended trail: want exit 0, got %d (%s)", rc, errb.String())
	}
	if !strings.Contains(strings.SplitN(out3.String(), "\n", 2)[0], "seal extended") {
		t.Fatalf("extended must show in the header:\n%s", out3.String())
	}
}

// A pruned record: --log plus a sentinel is enough to audit, without registry.
func TestPrunedRecordFromSentinel(t *testing.T) {
	t.Setenv("OUTSOURCE_RUNS_DIR", t.TempDir()) // empty: nothing resolves
	dir := t.TempDir()
	trail := writeTrailFile(t, `{"type":"assistant","timestamp":"2026-10-06T01:00:01.000Z","message":{"role":"assistant","id":"m1","model":"glm-5.3","content":[{"type":"text","text":"hi"}]}}`)
	log := filepath.Join(dir, "round.log")
	os.WriteFile(log, []byte("x\n"), 0o644)
	// A pre-seal sentinel, as a round finished before this change leaves it.
	os.WriteFile(log+".rc", []byte("rc=0\nfinished=2026-10-06T01:00:20Z\nharness=claude-code\nprovider=zai\nmodel_requested=glm-5.3\nmodel_actual=glm-5.3\nsession=sess-pruned\ntrail="+trail+"\ntool_calls=1\n"), 0o644)

	var out, errb bytes.Buffer
	rc := Main([]string{"--log", log}, &out, &errb)
	if rc != 0 {
		t.Fatalf("rc=%d %s", rc, errb.String())
	}
	if !strings.Contains(out.String(), "seal absent") || !strings.Contains(out.String(), "sentinel predates") {
		t.Fatalf("pruned round must audit with seal absent:\n%s", out.String())
	}

	// And no sentinel anywhere: 65, not a crash.
	os.Remove(log + ".rc")
	var out2 bytes.Buffer
	if rc := Main([]string{"--log", log}, &out2, &errb); rc != ExitNoTrail {
		t.Fatalf("log without sentinel: want 65, got %d", rc)
	}
}

func TestMainHelpExitsZero(t *testing.T) {
	var out, errb bytes.Buffer
	if rc := Main([]string{"--help"}, &out, &errb); rc != 0 || !strings.Contains(out.String(), "Exit codes: 0 printed") {
		t.Fatalf("help: rc=%d out=%q", rc, out.String())
	}
}

// The termination event is absent while the round is running (no sentinel).
func TestNoSentinelNoTermination(t *testing.T) {
	tr, err := ParseTrail("testdata/claude-main.jsonl", "claude-code")
	if err != nil {
		t.Fatal(err)
	}
	seal, m := ReadSeal("/nonexistent/sentinel.rc")
	if seal.Verdict != "absent" || m != nil {
		t.Fatal("no sentinel must read as absent with no map")
	}
	tr.Finalize(seal)
	if n := len(kinds(tr, KindTermination)); n != 0 {
		t.Fatalf("running round must have no termination event; got %d", n)
	}
}

// A <synthetic> assistant line is Claude Code's own message (an API error such
// as a 429), not a model's answer: it is an error event, never a request, so
// it cannot read as MODEL DRIFT. Added 2026-10-06 by the lead after a round
// cut by z.ai's 5-hour limit audited as "<synthetic> 1 request MODEL DRIFT".
// FAIL-first: without the SyntheticModel branch in parseClaudeFile the request
// count reads 2 and the error count 0.
func TestSyntheticLineIsAnErrorNotARequest(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "s.jsonl")
	lines := `{"type":"assistant","timestamp":"2026-01-01T00:00:00Z","message":{"id":"m1","model":"glm-5.3","role":"assistant","content":[{"type":"text","text":"working"}],"usage":{"input_tokens":5,"output_tokens":1}}}
{"type":"assistant","timestamp":"2026-01-01T00:00:01Z","isApiErrorMessage":true,"error":"rate_limit","message":{"id":"m2","model":"<synthetic>","role":"assistant","content":[{"type":"text","text":"API Error: Request rejected (429)"}]}}
`
	if err := os.WriteFile(p, []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}
	tr, err := ParseTrail(p, "claude-code")
	if err != nil {
		t.Fatal(err)
	}
	if got := len(kinds(tr, KindModelRequest)); got != 1 {
		t.Fatalf("model_request: want 1 (the synthetic line is not a request), got %d", got)
	}
	errs := kinds(tr, KindError)
	if len(errs) != 1 || !strings.Contains(errs[0].Head, "429") {
		t.Fatalf("want one error event carrying the API error text, got %+v", errs)
	}
}

// The command headline skips leading assignment-only lines. FAIL-first: with
// the old first-line rule the first case reads "W=/tmp/round/work".
func TestCommandHeadline(t *testing.T) {
	cases := map[string]string{
		"W=/tmp/round/work\nbash $W/run.sh": "… bash $W/run.sh",
		"A=1 B=2;\n\nmake test":             "… make test",
		"git status --short":                "git status --short",
		"W=/tmp/x; ls $W":                   "W=/tmp/x; ls $W",
		"X=1":                               "X=1",
	}
	for in, want := range cases {
		if got := headline(in); got != want {
			t.Errorf("headline(%q) = %q, want %q", in, got, want)
		}
	}
}
