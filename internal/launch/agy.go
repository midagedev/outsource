package launch

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"github.com/midagedev/outsource/internal/telemetry"
)

// runAgy drives `agy -p … --output-format stream-json` — the Google
// Antigravity CLI, which is both the provider and the harness (its auth and
// quota live in Google's plan, not in a key this launcher holds).
//
// Field-measured 2026-08-27 (agy 1.1.21):
//   - non-TTY headless works: `-p` + `--output-format stream-json` completed
//     detached probes (the upstream hang reports #318/#76 did not reproduce)
//   - RELATIVE paths do not mean the process cwd: with an untrusted cwd the
//     round's file tools resolve "the current directory" to agy's own
//     ~/.gemini/antigravity-cli/scratch. `--add-dir <cwd>` plus
//     `--dangerously-skip-permissions` made ABSOLUTE-path writes land in the
//     requested tree; specs for this harness must use absolute paths only.
//   - permissions.deny in ~/.gemini/antigravity-cli/settings.json still
//     applies under --dangerously-skip-permissions (deny > skip): a
//     `git add … && git commit …` compound was refused by a
//     `command(git commit)` rule while a plain echo ran. That deny store is
//     the git guard here — there is no per-process config isolation, because
//     a HOME-isolated copy of ~/.gemini fails auth (credentials are bound to
//     the sidecar/keychain, not to files under HOME).
//   - exit code is a lifecycle signal only: a permission-denied round exits 0
//     with status "CANCELED", and a soft-denied write exits 0 with status
//     "SUCCESS" and no file on disk. The final result event's `status` field
//     is the authority, and anything but SUCCESS fails the round here.
//   - `--print-timeout` defaults to 5 minutes — far below a real round — so
//     the launcher always pins a large ceiling and leaves interruption to the
//     --max-seconds watchdog.
//   - unknown model ids error loudly (rc=1, "invalid model selection"); no
//     silent mapping was measured. The stream init event echoes the request
//     (`init.model`); the recorded trajectory in conversations/<id>.db is the
//     stronger identity evidence and is what the assertion reads first.
func (r *round) runAgy() int {
	if _, err := exec.LookPath("agy"); err != nil {
		fmt.Fprintln(r.stderr, "harness agy needs the 'agy' CLI on PATH")
		r.bailed = true
		return ExitHarnessMissing
	}
	if r.o.model == "" {
		r.o.model = r.p.defaultModel
	}

	// The guard is written before the round, and a guard that cannot be
	// written is a round that must not launch: without the deny rules,
	// --dangerously-skip-permissions auto-approves git writes too.
	if err := agyEnsureGitGuard(agySettingsPath()); err != nil {
		fmt.Fprintf(r.stderr, "outsource: could not install the git deny rules in agy's settings (%v); refusing to launch without the guard\n", err)
		r.bailed = true
		return ExitUsage
	}

	logPath := r.o.log
	if logPath == "" {
		logPath = os.DevNull
	}
	logf, err := os.Create(logPath)
	if err != nil {
		fmt.Fprintf(r.stderr, "outsource: cannot write the log: %v\n", err)
		r.bailed = true
		return ExitUsage
	}
	var errf *os.File
	if r.o.log != "" {
		errf, _ = os.Create(r.o.log + ".err")
	}

	args := []string{
		"-p", r.specBody,
		"--output-format", "stream-json",
		"--model", r.o.model,
		"--add-dir", r.o.cwd,
		"--dangerously-skip-permissions",
		"--print-timeout", agyPrintTimeout(r.o.maxSeconds),
	}
	if r.o.session != "" {
		args = append(args, "--conversation", r.o.session)
	}
	cmd := exec.Command("agy", args...)
	cmd.Dir = r.o.cwd
	cmd.Stdin = nil
	cmd.Stdout = logf
	if errf != nil {
		cmd.Stderr = errf
	} else {
		cmd.Stderr = r.stderr
	}
	cmd.Env = nestedEnv(os.Environ())
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	rc := r.runChild(cmd, logf, errf)
	st := parseAgyStream(logPath)
	r.sid = st.conversationID
	if r.timedOut {
		r.timedOutNote()
		return ExitTimedOut
	}

	// The harness can exit 0 for a round its own result event calls failed
	// (measured: CANCELED on a denied permission). Trust the stream over the
	// exit code, in the failing direction only.
	if rc == 0 && st.status != "" && st.status != "SUCCESS" {
		fmt.Fprintf(r.stderr, "outsource: agy exited 0 but the result event says status=%s (error: %s); failing the round. A CANCELED here usually means a permission deny fired — the git guard refusing a git write is this shape.\n",
			st.status, orNone(st.errText))
		telemetry.Note("why", "agy result status not SUCCESS")
		rc = 1
	}

	assertCode := 0
	switch {
	case rc != 0:
		fmt.Fprintf(r.stderr, "outsource: run failed (rc=%d); model-identity assertion skipped\n", rc)
	case r.o.log == "":
		fmt.Fprintln(r.stderr, "outsource: no --log given; model-identity assertion skipped (nothing to verify against)")
	case r.sid == "":
		fmt.Fprintf(r.stderr, "outsource: MODEL ASSERTION FAILED — no conversation_id in %s, cannot verify that '%s' answered (exit 70)\n",
			r.o.log, r.o.model)
		telemetry.Note("why", "model unverifiable: no conversation id")
		assertCode = ExitModelIdentity
	default:
		actual, src, verdict := assertAgyIdentity(r.sid, r.o.model, st.initModel)
		r.modelVerdict, r.modelSource = verdict, src
		switch verdict {
		case "ok":
			r.modelActual = actual
		case "mismatch":
			r.modelActual = actual
			fmt.Fprintf(r.stderr, "outsource: MODEL MISMATCH — requested '%s' but the trajectory records: %s (evidence: %s); failing the round (exit 70)\n",
				r.o.model, orUnknown(actual), orNone(src))
			telemetry.Note("why", "model mismatch: another model answered")
			assertCode = ExitModelIdentity
		default:
			fmt.Fprintf(r.stderr, "outsource: MODEL ASSERTION FAILED — %s; cannot verify that '%s' answered, so not claiming a pass (exit 70)\n",
				orNone(src), r.o.model)
			telemetry.Note("why", "model unverifiable: agy trajectory")
			assertCode = ExitModelIdentity
		}
	}
	if rc != 0 {
		return rc
	}
	return assertCode
}

// agyPrintTimeout pins the harness's own ceiling out of the way. agy defaults
// --print-timeout to 5 minutes, which would truncate most real rounds; the
// launcher's posture is that only --max-seconds interrupts, so the pin sits
// above any plausible round and, when --max-seconds is set, above it too (the
// watchdog must win, so its kill is attributed as exit 124 and not as agy's
// own timeout).
func agyPrintTimeout(maxSeconds string) string {
	const floor = 24 * 60 // minutes
	if n, err := strconv.Atoi(maxSeconds); err == nil && n > 0 {
		if m := n/60 + 10; m > floor {
			return fmt.Sprintf("%dm", m)
		}
	}
	return fmt.Sprintf("%dm", floor)
}

func agySettingsPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".gemini", "antigravity-cli", "settings.json")
}

// agyGitDenyRules is the git guard for this harness, expressed in agy's
// permissions.deny syntax — ONE RULE PER SUBCOMMAND, because the matcher is
// a substring match, not a regex (measured 2026-08-27 twice: the single rule
// `command(git commit)` denied a `git add … && git commit …` compound, and a
// combined `command(git (commit|push|…))` alternation denied nothing and a
// commit went through). The subcommand list mirrors opencodeDenyGit; the
// listing forms opencode re-allows (worktree list, branch -a, …) CANNOT be
// re-allowed here because agy's precedence is deny > allow, so those
// read-only forms are denied too — the conservative direction. git log/show/
// diff/status are not in the list and keep working.
//
// These rules land in the USER's shared settings file (no isolation exists —
// see runAgy). They also apply to the user's own interactive agy sessions;
// references/agy.md documents that and the removal path.
var agyGitDenyRules = agyBuildDenyRules()

func agyBuildDenyRules() []string {
	rules := make([]string, 0, len(opencodeDenyGit)+16)
	for _, sub := range opencodeDenyGit {
		rules = append(rules, "command(git "+sub+")")
	}
	for _, g := range []string{
		"pr create", "pr merge", "pr close", "pr edit", "pr ready",
		"pr review", "repo create", "repo delete", "repo edit",
		"repo fork", "repo sync", "release", "workflow run",
	} {
		rules = append(rules, "command(gh "+g+")")
	}
	return rules
}

// agyEnsureGitGuard makes sure the deny rules are present in settings.json,
// creating the file if agy has never written one. Idempotent by rule string;
// every other key is preserved verbatim; the write is temp+rename so a crash
// cannot leave the user's settings half-written.
func agyEnsureGitGuard(path string) error {
	if path == "" {
		return fmt.Errorf("cannot resolve the agy settings path")
	}
	doc := map[string]json.RawMessage{}
	if b, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(b, &doc); err != nil {
			return fmt.Errorf("settings.json is not parseable JSON, refusing to rewrite it: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	perms := map[string]json.RawMessage{}
	if raw, ok := doc["permissions"]; ok {
		if err := json.Unmarshal(raw, &perms); err != nil {
			return fmt.Errorf("settings.json permissions is not an object: %w", err)
		}
	}
	var deny []string
	if raw, ok := perms["deny"]; ok {
		if err := json.Unmarshal(raw, &deny); err != nil {
			return fmt.Errorf("settings.json permissions.deny is not a string list: %w", err)
		}
	}
	have := make(map[string]bool, len(deny))
	for _, d := range deny {
		have[d] = true
	}
	changed := false
	for _, rule := range agyGitDenyRules {
		if !have[rule] {
			deny = append(deny, rule)
			changed = true
		}
	}
	if !changed {
		return nil
	}

	db, err := json.Marshal(deny)
	if err != nil {
		return err
	}
	perms["deny"] = db
	pb, err := json.Marshal(sortedRawMap(perms))
	if err != nil {
		return err
	}
	doc["permissions"] = pb
	out, err := json.MarshalIndent(sortedRawMap(doc), "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".outsource-tmp"
	if err := os.WriteFile(tmp, append(out, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// sortedRawMap gives the marshalled settings a stable key order, so repeated
// guard installs do not churn the user's file.
func sortedRawMap(m map[string]json.RawMessage) json.RawMessage {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		kb, _ := json.Marshal(k)
		b.Write(kb)
		b.WriteByte(':')
		b.Write(m[k])
	}
	b.WriteByte('}')
	return json.RawMessage(b.String())
}

// agyStreamState is what the launcher needs back out of the stream-json log:
// who to resume as, what the harness said happened, and the request echo the
// identity fallback compares against.
type agyStreamState struct {
	conversationID string
	initModel      string
	status         string
	errText        string
}

func parseAgyStream(logPath string) agyStreamState {
	var st agyStreamState
	f, err := os.Open(logPath)
	if err != nil {
		return st
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 256*1024), 32*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var ev struct {
			Event          string `json:"event"`
			ConversationID string `json:"conversation_id"`
			Init           struct {
				Model string `json:"model"`
			} `json:"init"`
			Result struct {
				ConversationID string `json:"conversation_id"`
				Status         string `json:"status"`
				Error          string `json:"error"`
			} `json:"result"`
		}
		if json.Unmarshal([]byte(line), &ev) != nil {
			continue
		}
		switch ev.Event {
		case "init":
			if st.conversationID == "" {
				st.conversationID = ev.ConversationID
			}
			if st.initModel == "" {
				st.initModel = ev.Init.Model
			}
		case "result":
			if ev.Result.ConversationID != "" {
				st.conversationID = ev.Result.ConversationID
			}
			st.status = ev.Result.Status
			st.errText = ev.Result.Error
		}
	}
	return st
}

// agyModelSlug matches the model ids agy's trajectory store records.
var agyModelSlug = regexp.MustCompile(`(?:gemini|claude|gpt)[a-z0-9.-]*-[a-z0-9.-]+`)

// agyConversationDBPaths are where a conversation's recorded trajectory
// lives. The -wal sidecar matters: a just-finished round's rows may still be
// in the write-ahead log, not the main file.
func agyConversationDBPaths(sid string) []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	base := filepath.Join(home, ".gemini", "antigravity-cli", "conversations", sid+".db")
	return []string{base, base + "-wal"}
}

// assertAgyIdentity answers "did the requested model answer" from the
// strongest evidence available. The conversation db records model slugs in
// its trajectory blobs (measured: a flash-low round's db held the exact
// requested slug); the stream init event only echoes the request. So:
// requested slug in the db → ok; a different model FAMILY in the db with the
// requested one absent → mismatch; db silent on models (or unreadable) → the
// init echo, explicitly labelled as echo-level, because agy rejects unknown
// ids loudly and no silent mapping has been measured — the echo is weak
// evidence for "which", but the loud-error behaviour makes "some other model
// quietly answered" the unlikely branch.
func assertAgyIdentity(sid, requested, initModel string) (actual, source, verdict string) {
	found := map[string]bool{}
	readable := false
	for _, p := range agyConversationDBPaths(sid) {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		readable = true
		for _, m := range agyModelSlug.FindAll(b, -1) {
			found[string(m)] = true
		}
	}
	if found[requested] {
		return requested, "conversation db trajectory", "ok"
	}
	// Effort tiers of one family share a prefix (gemini-3.7-flash records
	// both "gemini-3.7-flash" and "gemini-3.7-flash-low"), so only a slug
	// from a DIFFERENT family is a mismatch; a bare family prefix of the
	// requested id is not.
	var foreign []string
	for m := range found {
		if strings.HasPrefix(requested, m) || strings.HasPrefix(m, requested) {
			continue
		}
		foreign = append(foreign, m)
	}
	if readable && len(foreign) > 0 {
		sort.Strings(foreign)
		return strings.Join(foreign, ","), "conversation db trajectory", "mismatch"
	}
	if initModel != "" && initModel == requested {
		return requested, "stream init event (request echo, weaker evidence)", "ok"
	}
	if initModel != "" {
		return initModel, "stream init event", "mismatch"
	}
	return "", "no model evidence in conversation db or stream init", "absent"
}
