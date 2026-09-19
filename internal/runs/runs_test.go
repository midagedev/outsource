package runs

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/midagedev/outsource/internal/human"
)

// Every case here targets a seam that the shell version actually got wrong.
// Unit tests are the capability the port buys: in shell these behaviours were
// only reachable through a subprocess and a fixture directory.

func TestHumanSecsBoundaries(t *testing.T) {
	// The shell had two copies of this with different day handling, and they
	// had already drifted. Pin the boundaries so a third copy cannot appear.
	for _, c := range []struct {
		in   int64
		want string
	}{
		{-5, "0s"}, // a backwards clock must not render "-5s"
		{0, "0s"}, {59, "59s"},
		{60, "1m"}, {3599, "59m"},
		{3600, "1h00m"}, {3660, "1h01m"}, {86399, "23h59m"},
		{86400, "1d0h"}, {90000, "1d1h"},
	} {
		if got := human.Secs(c.in); got != c.want {
			t.Errorf("Secs(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestReadLastAssignmentWins(t *testing.T) {
	// finish() appends rather than rewrites, so the reader must take the last
	// assignment of a key. If it took the first, a finished round would read as
	// still running forever.
	dir := t.TempDir()
	p := filepath.Join(dir, "x.run")
	os.WriteFile(p, []byte("id=x\nrc=\nlabel=first\nlabel=second\nrc=0\n"), 0o644)
	r, err := Read(p)
	if err != nil {
		t.Fatal(err)
	}
	if r.Label != "second" {
		t.Errorf("label = %q, want second", r.Label)
	}
	if r.RC != "0" || r.State() != Done {
		t.Errorf("rc = %q state = %v, want 0/done", r.RC, r.State())
	}
}

func TestReadValueContainingEquals(t *testing.T) {
	// A model id or a path may contain '='; splitting on every '=' instead of
	// the first would silently truncate it.
	dir := t.TempDir()
	p := filepath.Join(dir, "x.run")
	os.WriteFile(p, []byte("id=x\nmodel=vendor/model=v2\n"), 0o644)
	r, _ := Read(p)
	if r.Model != "vendor/model=v2" {
		t.Errorf("model = %q, want vendor/model=v2", r.Model)
	}
}

func TestFindByLogMatchesAbsAndRelative(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("OUTSOURCE_RUNS_DIR", dir)
	log := filepath.Join(dir, "round.log")
	os.WriteFile(filepath.Join(dir, "x.run"), []byte("id=x\nlog="+log+"\npid=1\n"), 0o644)
	if got := FindByLog(log); got == nil || got.ID != "x" {
		t.Fatalf("exact path: got %+v", got)
	}
	// A relative spelling of the same file still hits.
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(cwd, log)
	if err != nil {
		t.Fatal(err)
	}
	if got := FindByLog(rel); got == nil || got.ID != "x" {
		t.Fatalf("relative path %q: got %+v", rel, got)
	}
	if got := FindByLog(filepath.Join(dir, "other.log")); got != nil {
		t.Fatalf("mismatch must be nil, got %+v", got)
	}
}

func TestReadRejectsRecordWithoutID(t *testing.T) {
	// A half-written record must not become a run with an empty name.
	dir := t.TempDir()
	p := filepath.Join(dir, "x.run")
	os.WriteFile(p, []byte("pid=1\nlabel=noid\n"), 0o644)
	if _, err := Read(p); err == nil {
		t.Error("a record with no id must be unreadable")
	}
}

func TestSanitizeFlattensNewlines(t *testing.T) {
	// One value is one line. A label carrying a newline would otherwise inject
	// a second key into the record.
	if got := sanitize("a\nb\rc"); got != "a b c" {
		t.Errorf("sanitize = %q, want %q", got, "a b c")
	}
}

func TestDisambiguatorTallies(t *testing.T) {
	// The shell ran this through command substitution, which is a subshell, so
	// the running tally was discarded after every call and no collision was
	// ever detected. The bug was invisible: the output just looked like two
	// identical rounds.
	d := disambiguator{}
	got := []string{d.next("api"), d.next("api"), d.next("api"), d.next("tests")}
	want := []string{"api", "api#2", "api#3", "tests"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("next[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestFilterMine(t *testing.T) {
	// An EMPTY filter means "show everything" — which is why a caller with no
	// identity must skip the call entirely rather than pass empty flags. That
	// trap shipped once: scoping degraded to the whole machine.
	rec := func(owner, pid string) *Record {
		return &Record{OwnerSession: owner, OwnerClaudePid: pid}
	}
	cases := []struct {
		f    filter
		r    *Record
		want bool
	}{
		{filter{}, rec("", ""), true},                               // no filter: everything
		{filter{}, rec("s1", "9"), true},                            // no filter: everything
		{filter{owner: "s1"}, rec("s1", ""), true},                  // session matches
		{filter{owner: "s1"}, rec("s2", ""), false},                 // session differs
		{filter{owner: "s1"}, rec("", ""), false},                   // unowned stays out of a scoped view
		{filter{ownerPid: "9"}, rec("s2", "9"), true},               // pid matches, session does not
		{filter{ownerPid: "9"}, rec("s2", ""), false},               // empty pid never matches
		{filter{owner: "s1", ownerPid: "9"}, rec("s2", "9"), true},  // either key counts
		{filter{owner: "s1", ownerPid: "9"}, rec("s1", "8"), true},  // either key counts
		{filter{owner: "s1", ownerPid: "9"}, rec("s2", "8"), false}, // neither
	}
	for i, c := range cases {
		if got := c.f.mine(c.r); got != c.want {
			t.Errorf("case %d: mine = %v, want %v (filter %+v record %+v)", i, got, c.want, c.f, c.r)
		}
	}
}

func TestIsIntMatchesJSONNullRule(t *testing.T) {
	// Emitted as a JSON number only when it really is one; anything else is
	// null, never 0. A pid of "notanumber" rendering as 0 would name a process.
	for in, want := range map[string]bool{
		"0": true, "42": true, "-1": true,
		"": false, "notanumber": false, "1.5": false, "1e3": false, "-": false, " 7": false,
	} {
		if got := isInt(in); got != want {
			t.Errorf("isInt(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestStallHonoursEnvOverrideAndRejectsGarbage(t *testing.T) {
	// A typo in the override must not silently disable stall detection.
	t.Setenv("OUTSOURCE_RUN_STALL", "120")
	if got := StallSeconds(); got != 120 {
		t.Errorf("StallSeconds = %d, want 120", got)
	}
	for _, bad := range []string{"", "abc", "0", "-5"} {
		t.Setenv("OUTSOURCE_RUN_STALL", bad)
		if got := StallSeconds(); got != DefaultStallSeconds {
			t.Errorf("StallSeconds(%q) = %d, want default %d", bad, got, DefaultStallSeconds)
		}
	}
}

func TestBareAndFlagShapedDispatchAgree(t *testing.T) {
	// `runs` and `runs list` must be the same verb, and a flag-shaped first
	// argument selects it without being eaten. The shell lost the bare form
	// once to a `shift` under `set -e`: rc=1, no output, on the invocation the
	// README leads with.
	t.Setenv("OUTSOURCE_RUNS_DIR", filepath.Join(t.TempDir(), "runs"))
	run := func(args ...string) (string, int) {
		var out bytes.Buffer
		code := Main(args, &out, &out)
		return out.String(), code
	}
	bare, c1 := run()
	explicit, c2 := run("list")
	flagged, c3 := run("--label", "anything")
	if c1 != 0 || c2 != 0 || c3 != 0 {
		t.Fatalf("exit codes = %d/%d/%d, want 0/0/0", c1, c2, c3)
	}
	if bare != explicit || bare != flagged {
		t.Errorf("bare=%q list=%q flagged=%q — all three must agree", bare, explicit, flagged)
	}
	if !strings.Contains(bare, "no delegated runs") {
		t.Errorf("empty registry must say so out loud, got %q", bare)
	}
}

func TestUnknownSubcommandIsUsageError(t *testing.T) {
	var out bytes.Buffer
	if code := Main([]string{"nope"}, &out, &out); code != ExitUsage {
		t.Errorf("exit = %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(out.String(), "unknown subcommand: nope") {
		t.Errorf("must name the offender, got %q", out.String())
	}
}

func TestIdleProgressDirAsFile(t *testing.T) {
	// FAIL-first (verbatim, before newestMtime accepted a regular file):
	//   Idle must observe a regular file ProgressDir; got ok=false
	// The opencode harness's live trail is the --log file itself (JSONL
	// flushed per event, measured 2026-08-23). Treating only directories as
	// a trail made a healthy opencode round look unobservable, which is the
	// same as ⏳-blind.
	dir := t.TempDir()
	log := filepath.Join(dir, "stream.jsonl")
	if err := os.WriteFile(log, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-30 * time.Second)
	if err := os.Chtimes(log, past, past); err != nil {
		t.Fatal(err)
	}
	r := &Record{ProgressDir: log}
	idle, ok := r.Idle(time.Now().Unix())
	if !ok {
		t.Fatal("Idle must observe a regular file ProgressDir; got ok=false")
	}
	if idle < 20 || idle > 60 {
		t.Errorf("Idle = %d, want ~30s", idle)
	}
}

func TestHarnessShortOpencode(t *testing.T) {
	// FAIL-first (verbatim): HarnessShort("opencode") = "opencode", want "oc"
	if got := HarnessShort("opencode"); got != "oc" {
		t.Errorf("HarnessShort(\"opencode\") = %q, want %q", got, "oc")
	}
	if got := HarnessShort("claude-code"); got != "cc" {
		t.Errorf("HarnessShort(\"claude-code\") = %q, want %q", got, "cc")
	}
}

// The listing is where a lead actually looks for a live round's trail, so the
// two states it can be in are both pinned here. FAIL-first: revert render.go's
// Running arm and the "pending"/"trail=" lines disappear while every other
// suite stays green — which is exactly how this shipped unguarded once.
func TestListShowsTheTrailForARunningRound(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("OUTSOURCE_RUNS_DIR", dir)
	var out bytes.Buffer
	// The test process's own pid: Alive() is a signal-0 probe, so a borrowed pid
	// like 1 answers EPERM and the fixture would read as an orphan.
	rc := Main([]string{"start", "--pid", strconv.Itoa(os.Getpid()), "--label", "watchme",
		"--provider", "zai", "--harness", "claude-code",
		"--trail-format", "claude-transcript"}, &out, os.Stderr)
	if rc != 0 {
		t.Fatalf("start rc=%d", rc)
	}
	id := strings.TrimSpace(out.String())

	// Before the reveal: the format is registered, the path is not. "pending" is
	// the honest word — "none" would read as "this harness leaves no trail".
	out.Reset()
	if rc := Main([]string{"list"}, &out, os.Stderr); rc != 0 {
		t.Fatalf("list rc=%d", rc)
	}
	if !strings.Contains(out.String(), "trail=pending") {
		t.Fatalf("a running round with a format but no path must say pending:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "outsource tail "+id) {
		t.Fatalf("the listing must say how to follow it:\n%s", out.String())
	}

	// After the reveal: the exact file, which is the whole point — it used to be
	// guessed at from the projects/ directory.
	trail := "/tmp/cfg/claude/projects/-Users-me-repo/2e156be9.jsonl"
	if err := SetTrail(id, trail); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	Main([]string{"list"}, &out, os.Stderr)
	if !strings.Contains(out.String(), "trail="+trail) {
		t.Fatalf("the revealed trail must be printed:\n%s", out.String())
	}

	// And a script can read it.
	out.Reset()
	Main([]string{"json"}, &out, os.Stderr)
	for _, want := range []string{`"trail":"` + trail + `"`, `"trailFormat":"claude-transcript"`} {
		if !strings.Contains(strings.ReplaceAll(out.String(), " ", ""), strings.ReplaceAll(want, " ", "")) {
			t.Fatalf("runs json is missing %s:\n%s", want, out.String())
		}
	}
}

// SetTrail is an append, like finish(): the reveal must never be able to
// rewrite what the launcher recorded at start.
func TestSetTrailAppendsAndRefusesUnknownRuns(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("OUTSOURCE_RUNS_DIR", dir)
	var out bytes.Buffer
	Main([]string{"start", "--pid", strconv.Itoa(os.Getpid()), "--label", "keepme",
		"--provider", "zai", "--harness", "claude-code", "--log", "/tmp/a.log"}, &out, os.Stderr)
	id := strings.TrimSpace(out.String())
	if err := SetTrail(id, "/first.jsonl"); err != nil {
		t.Fatal(err)
	}
	// A second, DIFFERENT trail is not a re-reveal — a round's transcript path is
	// fixed once its first turn starts. It is another round's hook writing here,
	// which is what a shared settings file produces (2026-09-19: five concurrent
	// rounds, one config dir, eleven `trail=` lines in one record and four rounds
	// reading `trail=pending` forever). "Last wins" made the wrong one the answer
	// silently; the first is kept and the intruder is parked where it is visible.
	if err := SetTrail(id, "/second.jsonl"); err != nil {
		t.Fatal(err)
	}
	r := FindByID(id)
	if r == nil || r.Trail != "/first.jsonl" {
		t.Fatalf("the round's own trail must survive a foreign one, got %+v", r)
	}
	if r.TrailConflict != "/second.jsonl" {
		t.Fatalf("the foreign trail must be recorded as a conflict, got %+v", r)
	}
	// The same hook firing twice is not a conflict.
	if err := SetTrail(id, "/first.jsonl"); err != nil {
		t.Fatal(err)
	}
	if r2 := FindByID(id); r2.Trail != "/first.jsonl" {
		t.Fatalf("an idempotent re-record must not disturb the trail: %+v", r2)
	}
	if r.Label != "keepme" || r.Log != "/tmp/a.log" {
		t.Fatalf("the append rewrote start fields: %+v", r)
	}
	if err := SetTrail("1-999999999", "/x.jsonl"); err == nil {
		t.Fatal("an unknown run id must be an error, not a new file")
	}
	if err := SetTrail(id, ""); err == nil {
		t.Fatal("an empty path must be refused")
	}
}
