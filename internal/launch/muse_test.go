package launch

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// A muse export, in the shape `muse export --out` really writes (CLI
// 1.3.0-R3401.1, 2026-09-18). The model that ANSWERED is in a run envelope's
// model_completed event — one per model completion, carrying that completion's
// own usage — and this fixture is trimmed from a real two-turn round.
const museExportFixture = `{
  "export_schema_version": 1,
  "sessions": [{"session_id":"01a0b3fc-eace-7d52-8ad6-dbdea5febfc9","turn_count":1}],
  "events": [
    {"kind":"record","envelope":{"payload_type":"runtime.session.metadata","payload":{"kind":"metadata","record":{"provider_id":"meta","model_id":"muse-spark-1.3-contributor"}}}},
    {"kind":"record","envelope":{"payload_type":"run.model.configured","payload":{"kind":"run_model","record":{"model_id":"muse-spark-1.3-contributor","source":"startup"}}}},
    {"kind":"record","envelope":{"payload_type":"runtime.session","payload":{"kind":"run","event":{"kind":"model_completed","model":"muse-spark-1.3-contributor","duration_ms":4482,"finish_reason":"tool_calls"}}}},
    {"kind":"record","envelope":{"payload_type":"runtime.session","payload":{"kind":"run","event":{"kind":"model_completed","model":"muse-spark-1.3-contributor","duration_ms":10873}}}}
  ]
}`

func TestParseMuseExportIdentity(t *testing.T) {
	actual, src, verdict := parseMuseExport([]byte(museExportFixture), "muse-spark-1.3-contributor")
	if verdict != "ok" {
		t.Fatalf("verdict=%q src=%q, want ok", verdict, src)
	}
	if actual != "muse-spark-1.3-contributor" {
		t.Fatalf("actual=%q", actual)
	}
	if !strings.Contains(src, "model_completed") {
		t.Fatalf("the source must name the evidence it used, got: %s", src)
	}

	// A round answered by something else fails even though the run succeeded.
	other := strings.Replace(museExportFixture, `"model":"muse-spark-1.3-contributor","duration_ms":10873`,
		`"model":"some-other-model","duration_ms":10873`, 1)
	actual, _, verdict = parseMuseExport([]byte(other), "muse-spark-1.3-contributor")
	if verdict != "mismatch" {
		t.Fatalf("a second completion by another model must be a mismatch, got %q", verdict)
	}
	if !strings.Contains(actual, "some-other-model") {
		t.Fatalf("the mismatch must name what answered, got %q", actual)
	}
}

// The live --json stream's run.model.configured says `source: "startup"` — it
// reports what the run was CONFIGURED with, not what replied, and an export
// carrying only that is not proof. This is the same trap claude-code's
// modelUsage sets (measured 2026-08-16: a run requesting claude-opus-5 and
// answered by glm-4.7 still logged modelUsage {"claude-opus-5": …}).
//
// FAIL-first: accept run.model.configured's model_id as evidence and this case
// passes a round nothing verified.
func TestMuseConfiguredModelIsNotProofOfWhoAnswered(t *testing.T) {
	echoOnly := `{"export_schema_version":1,"events":[
      {"kind":"record","envelope":{"payload_type":"run.model.configured","payload":{"kind":"run_model","record":{"model_id":"muse-spark-1.3-contributor","source":"startup"}}}},
      {"kind":"record","envelope":{"payload_type":"runtime.session.metadata","payload":{"kind":"metadata","record":{"model_id":"muse-spark-1.3-contributor"}}}}
    ]}`
	_, src, verdict := parseMuseExport([]byte(echoOnly), "muse-spark-1.3-contributor")
	if verdict == "ok" {
		t.Fatalf("a request echo must never clear the round; got ok from %q", src)
	}
	if verdict != "absent" {
		t.Fatalf("verdict=%q, want absent", verdict)
	}
}

func TestParseMuseExportRejectsGarbage(t *testing.T) {
	if _, _, v := parseMuseExport([]byte("not json"), "m"); v != "absent" {
		t.Fatalf("garbage export: verdict=%q, want absent", v)
	}
	if _, _, v := parseMuseExport([]byte(`{"events":[]}`), "m"); v != "absent" {
		t.Fatalf("empty export: verdict=%q, want absent", v)
	}
}

// The live log, in the shape muse exec --json really emits.
const museLogFixture = `{"schema_version":1,"stream":{"kind":"session","id":"01a0b3fc-eace-7d52-8ad6-dbdea5febfc9"},"sequence":1,"payload_type":"runtime.command.accepted","payload":{"kind":"command_accepted"}}
{"schema_version":1,"stream":{"kind":"session","id":"01a0b3fc-eace-7d52-8ad6-dbdea5febfc9"},"sequence":3,"payload_type":"run.model.configured","payload":{"kind":"run_model_configured","model_id":"muse-spark-1.3-contributor","source":"startup"}}
{"schema_version":1,"stream":{"kind":"session","id":"01a0b3fc-eace-7d52-8ad6-dbdea5febfc9"},"sequence":9,"payload_type":"tool.result","payload":{"kind":"tool_result","text":"Read image file ` + "`/tmp/shape.png`" + ` as model-visible image output.\nmedia_type: image/png","correlation_facts":{"tool_name":"read_file","outcome":"success"}}}
{"schema_version":1,"stream":{"kind":"session","id":"01a0b3fc-eace-7d52-8ad6-dbdea5febfc9"},"sequence":20,"payload_type":"run.output.delta","payload":{"kind":"run_output_delta","text":"1. H"}}
{"schema_version":1,"stream":{"kind":"session","id":"01a0b3fc-eace-7d52-8ad6-dbdea5febfc9"},"sequence":21,"payload_type":"run.terminal.completed","payload":{"kind":"run_terminal","terminal":"completed","text":"1. H","reason":null}}
`

func TestMuseSessionIDComesFromTheSessionStream(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "run.log")
	if err := os.WriteFile(p, []byte(museLogFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := museSessionID(p); got != "01a0b3fc-eace-7d52-8ad6-dbdea5febfc9" {
		t.Fatalf("museSessionID = %q", got)
	}
	// No session id is a state, not a crash: the identity assertion turns it
	// into "unverifiable" rather than passing the round.
	empty := filepath.Join(dir, "empty.log")
	if err := os.WriteFile(empty, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := museSessionID(empty); got != "" {
		t.Fatalf("a log with no session stream must yield no id, got %q", got)
	}
	if _, src, v := assertMuseIdentity("", "m", dir); v != "absent" || !strings.Contains(src, "no session id") {
		t.Fatalf("no session id must be absent with a reason, got %q / %q", v, src)
	}
}

func TestMuseReportIsTheTerminalText(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "run.log")
	if err := os.WriteFile(p, []byte(museLogFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := museReport(p); got != "1. H" {
		t.Fatalf("museReport = %q", got)
	}
}

// A failed round must say why. muse reports a non-clean end as a terminal other
// than "completed", with the reason beside it, and a --detach round has no
// terminal left to print to — the sentinel is what survives.
func TestMuseLogErrorLiftsTheReason(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "run.log")
	body := strings.Replace(museLogFixture,
		`"payload_type":"run.terminal.completed","payload":{"kind":"run_terminal","terminal":"completed","text":"1. H","reason":null}`,
		`"payload_type":"run.terminal.failed","payload":{"kind":"run_terminal","terminal":"failed","reason":"provider stream exhausted after 10 attempts"}`, 1)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got := museLogError(p)
	for _, want := range []string{"failed", "provider stream exhausted"} {
		if !strings.Contains(got, want) {
			t.Fatalf("lifted reason %q is missing %q", got, want)
		}
	}
	// A clean round invents no reason.
	if err := os.WriteFile(p, []byte(museLogFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := museLogError(p); got != "" {
		t.Fatalf("a clean log must yield no error, got %q", got)
	}
}

// The flags that hold a muse round in bounds are each here because of a measured
// incident, so a dropped one is a silent regression: the round still runs, and
// what is lost (the guard's escalation, the repo's rules) only shows in its
// output. FAIL-first: removing --trust-workspace from museArgs fails this.
func TestMuseArgsCarryTheFlagsThatHoldARoundInBounds(t *testing.T) {
	got := museArgs("/tmp/spec.md", "muse-spark-1.3-contributor", "max", "")
	for _, want := range []string{
		"--prompt-file", "/tmp/spec.md",
		"--model", "muse-spark-1.3-contributor",
		"--reasoning-effort",
		"--disable-approval", // headless: nobody can answer a prompt
		"--disable-sandbox",  // with approval off, a sandboxed escalation is refused outright
		"--user-input-auto-resolve",
		"--trust-workspace", // without it the workspace's AGENTS.md is skipped
	} {
		if !slices.Contains(got, want) {
			t.Fatalf("muse exec line lost %q: %v", want, got)
		}
	}
	if slices.Contains(got, "--session-id") {
		t.Fatalf("a fresh round must not carry --session-id: %v", got)
	}
	if slices.Contains(got, "--yolo") {
		t.Fatalf("--yolo marks the workspace trusted past this run; --trust-workspace is the scoped one: %v", got)
	}
}
