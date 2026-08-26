package launch

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// runChild starts the harness, arms the watchdog, and waits for the child's real
// exit. Shared by both harness paths so the watchdog and the wait have one
// implementation.
//
// The wait is not interrupted by a held signal: signal.Notify takes the signal
// out of the default disposition, so this process is not torn down and the child
// is left alone. That is the whole point — the paperwork after the wait is the
// only record a watcher has.
func (r *round) runChild(cmd *exec.Cmd, closers ...*os.File) int {
	if err := cmd.Start(); err != nil {
		for _, f := range closers {
			if f != nil {
				f.Close()
			}
		}
		fmt.Fprintf(r.stderr, "outsource: could not start %s: %v\n", r.o.harness, err)
		return ExitHarnessMissing
	}
	done := make(chan struct{})
	r.startWatchdog(cmd, done)
	err := cmd.Wait()
	close(done)
	for _, f := range closers {
		if f != nil {
			f.Close()
		}
	}
	return exitCode(err)
}

// runCrush drives the crush CLI with an isolated CRUSH_GLOBAL_CONFIG directory.
// Its logs carry no model-identity field, so the assertion is skipped there —
// reported, never fabricated.
func (r *round) runCrush() int {
	if _, err := exec.LookPath("crush"); err != nil {
		fmt.Fprintln(r.stderr, "harness crush needs the 'crush' CLI on PATH")
		r.bailed = true
		return ExitHarnessMissing
	}
	// The default is qualified HERE and not at registration, which is why the
	// registry field and the sentinel disagree on an unqualified launch.
	if r.o.model == "" {
		r.o.model = r.p.name + "/" + r.p.defaultModel
	}
	if msg, ok := crushModelFormError(r.o.model, r.p.name); !ok {
		fmt.Fprintln(r.stderr, msg)
		r.bailed = true
		return ExitUsage
	}

	// crush's data_directory defaults to `.crush` RELATIVE TO THE WORKING
	// DIRECTORY, so an unqualified run drops a multi-MB session DB into the tree it
	// is editing (self-gitignored, so invisible in `git status`, which is why it
	// goes unnoticed). Keeping it in scratch also scopes `crush session list` to
	// this track, since that is keyed by data dir and not by --cwd.
	dataDir := filepath.Join(r.o.configDir, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		fmt.Fprintf(r.stderr, "outsource: %v\n", err)
		r.bailed = true
		return ExitUsage
	}
	if err := r.writeCrushrc(); err != nil {
		fmt.Fprintf(r.stderr, "outsource: could not write the crush config: %v\n", err)
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
	specf, err := os.Open(r.o.spec)
	if err != nil {
		logf.Close()
		fmt.Fprintf(r.stderr, "outsource: cannot read the spec: %v\n", err)
		r.bailed = true
		return ExitUsage
	}
	defer specf.Close()

	args := []string{"run", "-q", "-c", r.o.cwd, "-D", dataDir}
	if r.o.session != "" {
		args = append(args, "-s", r.o.session)
	}
	cmd := exec.Command("crush", args...)
	cmd.Dir = r.o.cwd
	cmd.Stdin = specf
	// crush's diagnostics belong with its output here: unlike the claude-code
	// harness there is no JSON document to corrupt.
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.Env = nestedEnv(append(os.Environ(), "CRUSH_GLOBAL_CONFIG="+r.o.configDir))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	rc := r.runChild(cmd, logf)
	if r.timedOut {
		r.timedOutNote()
		// The session id is still worth recovering: crush wrote it, and it is what
		// a follow-up round would resume from.
		r.sid = r.crushSession(dataDir)
		return ExitTimedOut
	}

	fmt.Fprintln(r.stderr, "outsource: crush harness — model-identity assertion skipped (no modelUsage equivalent in crush logs)")
	r.sid = r.crushSession(dataDir)
	return rc
}

// writeCrushrc rebuilds the provider declaration inside an isolated config.
//
// The isolated config replaces the user's global one, so the provider and its key
// have to be re-declared. The key is resolved inside the crushrc AT LOAD TIME
// through the credential tool — the single owner — so the secret never transits a
// file this code writes and never reaches a log.
func (r *round) writeCrushrc() error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString("#!/usr/bin/env bash\nset -euo pipefail\n\n")
	fmt.Fprintf(&b, "_key=\"$(%q credential %s)\" || exit 1\n", self, r.p.name)
	fmt.Fprintf(&b, "provider add %s --api-key \"$_key\"\n\n", r.p.name)
	b.WriteString("# xhigh is this model's own default_reasoning_effort (providers.json), and the\n")
	b.WriteString("# crushrc flag validates against low|medium|high — so the slot is set without\n")
	b.WriteString("# --reasoning-effort and the provider default carries it.\n")
	fmt.Fprintf(&b, "model large %s\n\n", r.o.model)
	b.WriteString("# = grok --always-approve. Denied tools are hidden entirely, so bash stays\n")
	b.WriteString("# allowed here and the git ban lives in the PreToolUse hook instead.\n")
	b.WriteString("permissions allow bash view ls grep glob edit write multiedit fetch download diagnostics sourcegraph\n")
	if !r.o.allowAgent {
		// Hooks fire only on the top-level agent's tool calls, so a sub-agent's bash
		// would bypass the git guard.
		b.WriteString("permissions deny agent task\n")
	}
	// The guard is invoked as the binary directly rather than through the
	// git-guard.sh shim: it fires on every bash call, and the shim costs an extra
	// fork each time (measured 15ms vs 10ms). Same code, same verdict.
	fmt.Fprintf(&b, "\nhook add PreToolUse --matcher \"^bash\\$\" --command %q --name git-guard --timeout 10\n", self+" guard")
	b.WriteString("option progress false\n")
	return os.WriteFile(filepath.Join(r.o.configDir, "crushrc"), []byte(b.String()), 0o644)
}

// crushSession recovers the session id crush recorded. It lives at .meta.id in
// --json output, not at the top level.
func (r *round) crushSession(dataDir string) string {
	cmd := exec.Command("crush", "session", "last", "--json", "-c", r.o.cwd, "-D", dataDir)
	cmd.Env = append(os.Environ(), "CRUSH_GLOBAL_CONFIG="+r.o.configDir)
	cmd.Stdin = nil
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	var d map[string]any
	if json.Unmarshal(out, &d) != nil {
		return ""
	}
	if meta, ok := d["meta"].(map[string]any); ok {
		if id, ok := meta["id"].(string); ok {
			return id
		}
	}
	if id, ok := d["id"].(string); ok {
		return id
	}
	return ""
}

// crushModelFormError is the single owner of crush's provider/id rule. It is
// checked twice: here, when the round starts, and again before the --detach
// re-exec — because a check that only runs in the detached child has nowhere
// to print. That is how an unqualified --model came back as "detached
// (pid=…)" and exit 0 over a round that was already dead (2026-08-26).
//
// An empty model is not an error: runCrush qualifies the default itself.
func crushModelFormError(model, provider string) (string, bool) {
	if model == "" {
		return "", true
	}
	prefix, _, hasSlash := strings.Cut(model, "/")
	if !hasSlash {
		return fmt.Sprintf("--model must be provider/id for the crush harness, got: %s", model), false
	}
	if prefix != provider {
		return fmt.Sprintf("--model %s does not match --provider %s", model, provider), false
	}
	return "", true
}
