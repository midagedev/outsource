package launch

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// slotEnvRig is a skill dir holding bin/slot.sh, a fake crush on PATH that
// writes the environment it was given to a file, and a spec — enough to run
// a real foreground round through OutsourceMain and read what its harness
// child saw.
func slotEnvRig(t *testing.T) (skill, envOut, spec string) {
	t.Helper()
	tmp := t.TempDir()
	skill = filepath.Join(tmp, "skill")
	if err := os.MkdirAll(filepath.Join(skill, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skill, "bin", "slot.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	fakeBin := filepath.Join(tmp, "fakebin")
	os.MkdirAll(fakeBin, 0o755)
	crush := "#!/bin/sh\nif [ \"$1\" = session ]; then echo '{}'; exit 0; fi\ncat >/dev/null\nenv > \"$FAKE_ENV_OUT\"\necho working\n"
	if err := os.WriteFile(filepath.Join(fakeBin, "crush"), []byte(crush), 0o755); err != nil {
		t.Fatal(err)
	}
	envOut = filepath.Join(tmp, "child.env")
	spec = filepath.Join(tmp, "slot-env-spec.md")
	os.WriteFile(spec, []byte("do the thing\n"), 0o644)
	os.MkdirAll(filepath.Join(tmp, "cwd"), 0o755)

	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_ENV_OUT", envOut)
	t.Setenv("OUTSOURCE_SKILL_DIR", skill)
	t.Setenv("XDG_STATE_HOME", filepath.Join(tmp, "state"))
	t.Setenv("ZAI_API_KEY", "test-key-not-a-real-credential")
	// The launcher sets these with os.Setenv; t.Setenv first makes the test
	// restore whatever the caller's shell had once it ends.
	for _, k := range []string{slotEnvKey, runLabelEnvKey} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	return skill, envOut, spec
}

func childEnv(t *testing.T, path string) map[string]string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the fake harness child never ran: %v", err)
	}
	env := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			env[k] = v
		}
	}
	return env
}

// A harness child sees where the slot tool is and what this round is called.
//
// FAIL-first: delete the exportSlotEnv line in round.run and this fails with
// "OUTSOURCE_SLOT = \"\"".
func TestHarnessChildGetsTheSlotAndTheLabel(t *testing.T) {
	skill, envOut, spec := slotEnvRig(t)
	dir := filepath.Dir(spec)
	run := func(extra ...string) map[string]string {
		t.Helper()
		os.Remove(envOut)
		a := append([]string{"--foreground", "--harness", "crush", "--provider", "zai",
			"--cwd", filepath.Join(dir, "cwd"), "--spec", spec, "--log", filepath.Join(dir, "round.log"),
			"--config-dir", filepath.Join(dir, "cfg")}, extra...)
		var errBuf strings.Builder
		if rc := OutsourceMain(a, io.Discard, &errBuf); rc != 0 {
			t.Fatalf("round exited %d: %s", rc, errBuf.String())
		}
		return childEnv(t, envOut)
	}

	env := run("--label", "slot-env-probe")
	if want := filepath.Join(skill, "bin", "slot.sh"); env[slotEnvKey] != want {
		t.Errorf("%s = %q, want %q", slotEnvKey, env[slotEnvKey], want)
	}
	if env[runLabelEnvKey] != "slot-env-probe" {
		t.Errorf("%s = %q, want the --label", runLabelEnvKey, env[runLabelEnvKey])
	}
	if env[nestedEnvKey] != "1" {
		t.Errorf("not a harness child: %s = %q", nestedEnvKey, env[nestedEnvKey])
	}

	// Without --label the round's own default label is the holder name.
	env = run()
	if want := defaultLabel(spec); env[runLabelEnvKey] != want {
		t.Errorf("without --label: %s = %q, want %q", runLabelEnvKey, env[runLabelEnvKey], want)
	}
}

// OUTSOURCE_SLOT is exported only when the shim is there: a path that does not
// resolve would fail every wrapped step. The fallback beside this executable
// is the test binary's directory, which holds no slot.sh.
func TestSlotScriptResolution(t *testing.T) {
	skill, _, _ := slotEnvRig(t)
	if got, want := slotScript(), filepath.Join(skill, "bin", "slot.sh"); got != want {
		t.Errorf("slotScript() = %q, want %q", got, want)
	}
	t.Setenv("OUTSOURCE_SKILL_DIR", t.TempDir())
	if got := slotScript(); got != "" {
		t.Errorf("no slot.sh anywhere: slotScript() = %q, want empty", got)
	}
	exportSlotEnv("")
	if v, ok := os.LookupEnv(slotEnvKey); ok {
		t.Errorf("exported %s=%q with no shim to point at", slotEnvKey, v)
	}
	if v, ok := os.LookupEnv(runLabelEnvKey); ok {
		t.Errorf("exported %s=%q for an empty label", runLabelEnvKey, v)
	}
}
