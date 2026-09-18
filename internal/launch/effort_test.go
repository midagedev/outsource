package launch

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --effort is a pre-flight: a level the CLI does not accept, or a harness that
// has no such flag, is refused before the run registry records anything. The
// alternative — dropping it silently — would let a "cheap" fan-out round run at
// the default and nobody would know (measured motive: z.ai glm-5.3 answers a
// one-word prompt in 3 output tokens at low and 50–113 at max, 2026-09-15).
func TestEffortRefusedBeforeLaunch(t *testing.T) {
	dir := t.TempDir()
	spec := filepath.Join(dir, "spec.md")
	if err := os.WriteFile(spec, []byte("do a thing\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OUTSOURCE_RUNS_DIR", filepath.Join(dir, "runs"))
	cases := []struct{ harness, effort, want string }{
		{"claude-code", "ultra", "--effort must be one of"},
		// The refusal's wording changed on 2026-09-18, when --effort stopped
		// being a claude-code-only flag (muse exec takes --reasoning-effort) and
		// the capability became a harness-table column. The standard is
		// unchanged — the round is still refused at exit 64 before it launches;
		// what the message must now do is name a harness that DOES take the
		// flag, which a hardcoded "claude-code harness flag" sentence could not
		// keep doing.
		{"crush", "low", "has no reasoning-effort control"},
	}
	for _, c := range cases {
		var stderr bytes.Buffer
		rc := OutsourceMain([]string{
			"--cwd", dir, "--spec", spec, "--log", filepath.Join(dir, "x.log"),
			"--provider", "zai", "--harness", c.harness, "--model", "zai/glm-5.3",
			"--effort", c.effort, "--label", "effort-test",
		}, &bytes.Buffer{}, &stderr)
		if c.harness == "claude-code" {
			// bare id on claude-code; the model form is not what this case is about
			rc = OutsourceMain([]string{
				"--cwd", dir, "--spec", spec, "--log", filepath.Join(dir, "x.log"),
				"--provider", "zai", "--harness", c.harness, "--model", "glm-5.3",
				"--effort", c.effort, "--label", "effort-test",
			}, &bytes.Buffer{}, &stderr)
		}
		if rc != ExitUsage {
			t.Errorf("%s/--effort %s: rc=%d, want %d; stderr=%s", c.harness, c.effort, rc, ExitUsage, stderr.String())
		}
		if !strings.Contains(stderr.String(), c.want) {
			t.Errorf("%s/--effort %s: stderr %q, want substring %q", c.harness, c.effort, stderr.String(), c.want)
		}
	}
}

// The refusal has to send the caller somewhere real, so it names every harness
// that honours the flag — derived from the table, so a new one that takes an
// effort level is offered without anyone remembering to edit a sentence.
func TestEffortRefusalNamesAHarnessThatTakesTheFlag(t *testing.T) {
	msg := effortRefusal("crush", "low")
	if msg == "" {
		t.Fatal("crush has no effort control and must be refused")
	}
	if !strings.Contains(msg, "crush") {
		t.Errorf("the refusal must name the harness that was refused: %s", msg)
	}
	takers := effortHarnesses()
	if len(takers) == 0 {
		t.Fatal("no harness declares effortFlag; --effort could never be used")
	}
	for _, h := range takers {
		if !strings.Contains(msg, h) {
			t.Errorf("refusal omits %s, which does take the flag: %s", h, msg)
		}
	}
	// And every harness that declares the column really is accepted.
	for _, h := range takers {
		if m := effortRefusal(h, "high"); m != "" {
			t.Errorf("harness %s declares effortFlag but was refused: %s", h, m)
		}
	}
	// An unknown harness is refused rather than silently allowed through.
	if effortRefusal("no-such-harness", "high") == "" {
		t.Error("an unknown harness must not be granted an effort level")
	}
}

func TestEffortLevelsMatchTheCLI(t *testing.T) {
	for _, l := range []string{"low", "medium", "high", "xhigh", "max"} {
		if msg := effortRefusal("claude-code", l); msg != "" {
			t.Errorf("%s: %s", l, msg)
		}
	}
	if effortRefusal("claude-code", "") != "" {
		t.Error("an unset effort is the harness default, never a refusal")
	}
}
