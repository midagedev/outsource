package telemetry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The privacy rule is the load-bearing property of this package, so it is the
// first test: flag NAMES are the signal, flag VALUES are not, and the only values
// recorded are from closed sets this repo defines.
func TestOnlyFlagNamesAndAllowlistedValues(t *testing.T) {
	args := []string{
		"--cwd", "/Users/someone/private/repo",
		"--spec", "/tmp/scratch/secret-plan.md",
		"--done-marker", "DONE-CONFIDENTIAL-PROJECT",
		"--harness", "crush",
		"--provider", "zai",
		"--model", "zai/glm-5.3",
		"--label", "acquisition-diligence",
		"--max-seconds", "600",
	}
	names, vals := flagNames(args)

	joined := strings.Join(names, " ")
	for k, v := range vals {
		joined += " " + k + "=" + v
	}
	for _, leak := range []string{
		"/Users/someone", "private", "secret-plan", "CONFIDENTIAL",
		"acquisition-diligence", "600",
	} {
		if strings.Contains(joined, leak) {
			t.Errorf("value leaked into telemetry: %q", leak)
		}
	}
	// The names must all be there — that is what makes the log useful.
	for _, want := range []string{"--cwd", "--spec", "--done-marker", "--label", "--max-seconds"} {
		found := false
		for _, n := range names {
			if n == want {
				found = true
			}
		}
		if !found {
			t.Errorf("flag name %q was not recorded", want)
		}
	}
	// Exactly the allowlist, and nothing else.
	if vals["harness"] != "crush" || vals["provider"] != "zai" {
		t.Errorf("allowlisted enums missing: %v", vals)
	}
	if _, ok := vals["model"]; ok {
		t.Error("--model is user-supplied and must not be recorded by value")
	}
	if len(vals) != 2 {
		t.Errorf("recorded %d values, want exactly the 2 allowlisted enums: %v", len(vals), vals)
	}
}

// A flag written as --k=v must still be reduced to its name.
func TestInlineValueIsStripped(t *testing.T) {
	names, vals := flagNames([]string{"--json-schema={\"secret\":1}", "--quiet"})
	for _, n := range names {
		if strings.Contains(n, "secret") || strings.Contains(n, "=") {
			t.Errorf("inline value survived: %q", n)
		}
	}
	if len(vals) != 0 {
		t.Errorf("no values should be recorded here: %v", vals)
	}
}

// Words after `--` are a wrapped command's (outsource slot -- go test
// --count=1), not the tool's: neither their names nor an allowlisted value
// may be recorded as the tool's flags.
func TestFlagsStopAtDoubleDash(t *testing.T) {
	names, vals := flagNames([]string{"--max", "2", "--", "go", "test", "--count=1", "--harness", "crush"})
	if strings.Join(names, " ") != "--max" {
		t.Errorf("names = %v, want only --max", names)
	}
	if len(vals) != 0 {
		t.Errorf("a wrapped command's value was recorded: %v", vals)
	}
}

// The two high-frequency tools would bury the log and grow it by megabytes a day
// if their successes were recorded.
func TestHighFrequencyToolsRecordOnlyFailures(t *testing.T) {
	for _, tool := range []string{"guard", "statusline"} {
		if worthRecording(tool, 0) {
			t.Errorf("%s must not record a successful call", tool)
		}
		if !worthRecording(tool, 2) {
			t.Errorf("%s must record a failure", tool)
		}
	}
	if !worthRecording("outsource-run", 0) {
		t.Error("a launcher's successful round is worth a line")
	}
}

func TestDisabledByEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "t.jsonl")
	t.Setenv("OUTSOURCE_TELEMETRY_FILE", path)
	t.Setenv("OUTSOURCE_TELEMETRY", "0")
	Record("runs", []string{"list"}, 0, time.Now())
	if _, err := os.Stat(path); err == nil {
		t.Error("OUTSOURCE_TELEMETRY=0 must write nothing at all")
	}
	t.Setenv("OUTSOURCE_TELEMETRY", "")
	Record("runs", []string{"list"}, 0, time.Now())
	if _, err := os.Stat(path); err != nil {
		t.Error("recording should be on by default")
	}
}

func TestRecordShapeAndFileMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "t.jsonl")
	t.Setenv("OUTSOURCE_TELEMETRY_FILE", path)
	t.Setenv("OUTSOURCE_TELEMETRY", "")
	Note("why", "a named reason")
	Record("outsource-run", []string{"--harness", "crush"}, 72, time.Now().Add(-2*time.Second))

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var e Event
	if err := json.Unmarshal(b[:len(b)-1], &e); err != nil {
		t.Fatalf("each line must be one JSON object: %v", err)
	}
	if e.Tool != "outsource-run" || e.RC != 72 || e.Details["why"] != "a named reason" {
		t.Errorf("unexpected event: %+v", e)
	}
	if e.MS < 1900 {
		t.Errorf("duration not measured: %dms", e.MS)
	}
	// The log can hold a reason a user wrote, so it is theirs to read and nobody
	// else's.
	fi, _ := os.Stat(path)
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode is %v, want 0600", fi.Mode().Perm())
	}
}

func TestRollKeepsOneGeneration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "t.jsonl")
	big := make([]byte, maxBytes+1)
	os.WriteFile(path, big, 0o600)
	roll(path)
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Error("an oversized log must roll aside, not be truncated")
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("the live file should be gone after a roll, ready to be recreated")
	}
}

// A borrowed label is worse than no label. The first version of the report had a
// fallback for every code, so `runs` exit 65 — "no such run id" — was printed as
// "a spec that needs eyes was sent to a backend that has none", and 66 became a
// quota refusal. The summary is read to decide what to fix; a summary that
// misnames a failure sends the reader after the wrong thing.
func TestMeaningNeverBorrowsALabelAcrossTools(t *testing.T) {
	// The same small integer means different facts in different tools, and each
	// must say its own.
	cases := []struct {
		tool string
		rc   int
		want string
	}{
		{"runs", 65, "no such run id"},
		{"runs", 66, "still running"},
		{"last-report", 65, "died mid-run"},
		{"last-report", 66, "could not be read"},
		{"outsource-run", 65, "needs eyes"},
		{"outsource-run", 66, "plan could not finish"},
		{"grok-run", 66, "no such --cwd"},
		{"quota", 3, "--require-window floor"},
		{"guard", 2, "not allowed"},
	}
	for _, c := range cases {
		got := meaning(c.tool, c.rc)
		if !strings.Contains(got, c.want) {
			t.Errorf("meaning(%s, %d) = %q, want it to mention %q", c.tool, c.rc, got, c.want)
		}
	}
	// 64 is the one genuinely universal code, so it may fall back.
	if m := meaning("some-future-tool", 64); !strings.Contains(m, "usage") {
		t.Errorf("64 should fall back to usage, got %q", m)
	}
	// Anything else with no entry says nothing rather than borrowing.
	for _, c := range []struct {
		tool string
		rc   int
	}{
		{"runs", 70}, {"spec-lint", 65}, {"credential", 72}, {"some-future-tool", 65},
	} {
		if m := meaning(c.tool, c.rc); m != "" {
			t.Errorf("meaning(%s, %d) borrowed a label: %q", c.tool, c.rc, m)
		}
	}
}
