package speclint

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/midagedev/outsource/internal/telemetry"
)

// Brace sets, Absent-ok and fence lists (2026-10-06). A lead running GLM
// rounds on bloomery met two false findings in one day: a brace set
// (`crates/serve/src/{qwenxml,dsml,api,lib}.rs`) taken as one literal path,
// and a peer round's new files, named only to fence them off, reported
// missing from a tree they were never claimed to be in. Each re-lint is an
// edit cycle on a spec that was right. Every exemption below makes the linter
// say less, so each comes beside the defect it must still report.

// lintCase is one temp repo and a spec runner over it.
type lintCase struct {
	t    *testing.T
	root string
	spec string
}

func newLintCase(t *testing.T, files map[string]string) *lintCase {
	t.Helper()
	dir := t.TempDir()
	root := filepath.Join(dir, "root")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return &lintCase{t: t, root: root, spec: filepath.Join(dir, "spec.md")}
}

func (c *lintCase) run(body string, extra ...string) (string, int) {
	c.t.Helper()
	if err := os.WriteFile(c.spec, []byte(body), 0o644); err != nil {
		c.t.Fatal(err)
	}
	var out bytes.Buffer
	code := Main(append([]string{"--root", c.root, c.spec}, extra...), &out, &out)
	return out.String(), code
}

// findings are the output lines that are findings, in order.
func findings(out string) []string {
	var got []string
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		l := sc.Text()
		for _, kind := range []string{": missing: ", ": line-out-of-range: ", ": already-exists: ", ": unchecked: "} {
			if strings.Contains(l, kind) {
				got = append(got, l)
				break
			}
		}
	}
	return got
}

// ─── brace sets ──────────────────────────────────────────────────────────────

func TestBraceSetExpandsTheFieldCase(t *testing.T) {
	// Three of the four files exist: exactly one finding, and it names the
	// expansion that is absent and the token it came from — "missing:
	// crates/serve/src/{qwenxml,dsml,api,lib}.rs" told the reader nothing
	// about which of the four was wrong.
	c := newLintCase(t, map[string]string{
		"crates/serve/src/qwenxml.rs": "x\n",
		"crates/serve/src/dsml.rs":    "x\n",
		"crates/serve/src/api.rs":     "x\n",
	})
	tok := "crates/serve/src/{qwenxml,dsml,api,lib}.rs"
	out, rc := c.run("Do the change in `" + tok + "`.\n")
	got := findings(out)
	want := "missing: crates/serve/src/lib.rs (resolved: " + c.root + "/crates/serve/src/lib.rs; from " + tok + ")"
	if rc != ExitFindings || len(got) != 1 || !strings.HasSuffix(got[0], want) {
		t.Errorf("field case: rc=%d findings=%q, want exactly one ending %q (out: %s)", rc, got, want, out)
	}

	// With the fourth file in place the same token is clean.
	c = newLintCase(t, map[string]string{
		"crates/serve/src/qwenxml.rs": "x\n",
		"crates/serve/src/dsml.rs":    "x\n",
		"crates/serve/src/api.rs":     "x\n",
		"crates/serve/src/lib.rs":     "x\n",
	})
	if out, rc := c.run("Do the change in `" + tok + "`.\n"); rc != ExitClean || len(findings(out)) != 0 {
		t.Errorf("all four exist: rc=%d out=%s", rc, out)
	}
}

func TestBraceSetsCrossProductAndEdges(t *testing.T) {
	c := newLintCase(t, map[string]string{
		"pkg/a_x.go":                    "x\n",
		"pkg/a_y.go":                    "x\n",
		"pkg/b_x.go":                    "x\n",
		"pkg/x.go":                      "one\ntwo\n",
		"lib/x.go":                      "x\n",
		"pkg/x.md":                      "x\n",
		"pkg/new1.go":                   "",
		".github/workflows/ci.yml":      "x\n",
		".github/workflows/release.yml": "x\n",
	})
	for _, tc := range []struct {
		name, body string
		rc         int
		want       []string // finding suffixes, in order
	}{
		// Two groups: the cartesian product, in bash's order.
		{"two groups", "See `pkg/{a,b}_{x,y}.go`.\n", ExitFindings,
			[]string{"missing: pkg/b_y.go (resolved: " + c.root + "/pkg/b_y.go; from pkg/{a,b}_{x,y}.go)"}},
		// A group at either end of the token keeps its braces: the edge
		// strip trims `{}` as prose punctuation, and "pkg,lib}/x.go" is a
		// path nobody wrote.
		{"group at the start", "See `{pkg,lib}/x.go`.\n", ExitClean, nil},
		{"group at the end", "See (pkg/x.{go,md}).\n", ExitClean, nil},
		// A set keeps a path's leading dots, as a plain token does.
		{"leading dots", "See `.github/workflows/{ci,release}.yml` and ./pkg/{x,a_x}.go.\n", ExitClean, nil},
		{"group at the end, one absent", "See pkg/x.{go,md,ts}.\n", ExitFindings,
			[]string{"missing: pkg/x.ts (resolved: " + c.root + "/pkg/x.ts; from pkg/x.{go,md,ts})"}},
		// Nested sets expand as bash expands them.
		{"nested", "See `pkg/{a_{x,y},b_x}.go`.\n", ExitClean, nil},
		// A citation expands too, and each expansion is checked for its line.
		{"cited", "See `pkg/{x,a_x}.go:2`.\n", ExitFindings,
			[]string{"line-out-of-range: pkg/a_x.go:2 (file has 1 lines; from pkg/{x,a_x}.go:2)"}},
		{"cited lines", "See `pkg/x.go:{2,9}`.\n", ExitFindings,
			[]string{"line-out-of-range: pkg/x.go:9 (file has 2 lines; from pkg/x.go:{2,9})"}},
		// Expansions that are not path-shaped are not claims, as before.
		{"not paths", "Mutants {M2,M3,M5,M5b} as written.\n", ExitClean, nil},
		// A Create declaration expands, and every expansion is exempt.
		{"create", "Create: `pkg/{brand,new}.go`\n", ExitClean, nil},
		// An empty alternative is one: `{,_test}` is two files.
		{"empty alternative", "See `pkg/new1{,_test}.go`.\n", ExitFindings,
			[]string{"missing: pkg/new1_test.go (resolved: " + c.root + "/pkg/new1_test.go; from pkg/new1{,_test}.go)"}},
	} {
		out, rc := c.run(tc.body)
		got := findings(out)
		bad := rc != tc.rc || len(got) != len(tc.want)
		for i := 0; !bad && i < len(got); i++ {
			bad = !strings.HasSuffix(got[i], tc.want[i])
		}
		if bad {
			t.Errorf("%s: rc=%d findings=%q, want rc=%d %q (out: %s)", tc.name, rc, got, tc.rc, tc.want, out)
		}
	}
	if out, _ := c.run("Create: `pkg/{brand,new}.go`\n"); !strings.Contains(out, "ok (2 to-be-created exempt)") {
		t.Errorf("each expansion of a created set is one exempt reference: %s", out)
	}
}

func TestBraceSetPastTheCapIsUnchecked(t *testing.T) {
	c := newLintCase(t, nil)
	// 5×5×3 = 75 alternatives: past the cap of 64. Reported once, as a
	// finding — the linter vouched for none of them, and an exit 0 here
	// would be a silent pass.
	tok := "pkg/{a,b,c,d,e}{a,b,c,d,e}{a,b,c}.go"
	out, rc := c.run("See `" + tok + "`.\n")
	got := findings(out)
	if rc != ExitFindings || len(got) != 1 || !strings.Contains(got[0], ": unchecked: "+tok+" (") {
		t.Errorf("past the cap: rc=%d findings=%q, want one unchecked finding naming %s", rc, got, tok)
	}
	// Exactly 64 is inside the cap: every expansion is checked.
	out, rc = c.run("See `pkg/{a,b,c,d}{a,b,c,d}{a,b,c,d}.go`.\n")
	if got := findings(out); rc != ExitFindings || len(got) != 64 || strings.Contains(out, "unchecked") {
		t.Errorf("64 alternatives: rc=%d, %d findings, want 64 missing and no unchecked", rc, len(got))
	}
	// A large set that is not path-shaped is prose, not an unchecked claim.
	if out, rc := c.run("Grid {a,b,c,d,e}{a,b,c,d,e}{a,b,c} as planned.\n"); rc != ExitClean {
		t.Errorf("non-path set past the cap: rc=%d out=%s", rc, out)
	}
}

func TestCommaLessBracesKeepTodaysBehaviour(t *testing.T) {
	// Pinned from the base binary (60b6f71): a brace pair without a comma
	// is not a set. `{id}` mid-token is part of a literal path, `{}` too,
	// `${VAR}` makes the token environment phrasing, and a pair at a token
	// edge is trimmed as punctuation.
	c := newLintCase(t, map[string]string{"pkg/{lit}.go": "x\n"})
	for _, tc := range []struct {
		name, body string
		rc         int
		want       string // finding suffix, or "" for none
	}{
		{"{id} mid-token is literal", "See `pkg/{id}.go`.\n", ExitFindings,
			"missing: pkg/{id}.go (resolved: " + c.root + "/pkg/{id}.go)"},
		{"{} mid-token is literal", "See `pkg/{}.go`.\n", ExitFindings,
			"missing: pkg/{}.go (resolved: " + c.root + "/pkg/{}.go)"},
		{"a literal brace file resolves", "See `pkg/{lit}.go`.\n", ExitClean, ""},
		{"${VAR} is a template", "See `${VAR}/x.md` and `$VAR/{a}.md`.\n", ExitClean, ""},
		{"edge braces are trimmed", "See {id}/x.go here.\n", ExitFindings,
			"missing: id}/x.go (resolved: " + c.root + "/id}/x.go)"},
	} {
		out, rc := c.run(tc.body)
		got := findings(out)
		bad := rc != tc.rc
		if tc.want == "" {
			bad = bad || len(got) != 0
		} else {
			bad = bad || len(got) != 1 || !strings.HasSuffix(got[0], tc.want)
		}
		if bad {
			t.Errorf("%s: rc=%d findings=%q, want rc=%d %q", tc.name, rc, got, tc.rc, tc.want)
		}
	}
}

// ─── Absent-ok ───────────────────────────────────────────────────────────────

func TestAbsentOkMarker(t *testing.T) {
	c := newLintCase(t, map[string]string{"pkg/exists.go": "one\ntwo\nthree\n"})
	for _, tc := range []struct {
		name, body string
		rc         int
		must       string // output that must appear
		mustNot    string
	}{
		{"single line", "Absent-ok: `pkg/peer.go`\n", ExitClean, "ok (1 absent-ok)", "missing"},
		{"numbered single line", "1. Absent-ok: `pkg/peer.go` — line1's branch.\n", ExitClean, "ok (1 absent-ok)", "missing"},
		{"list", "**Absent-ok** (peer branches):\n- `pkg/peer1.go`\n\n- `pkg/peer2.go`\n", ExitClean, "ok (2 absent-ok)", "missing"},
		{"bold heading list", "## Absent-ok:\n1. `pkg/peer1.go`\n   wrapped `pkg/peer2.go`\n", ExitClean, "ok (2 absent-ok)", "missing"},
		// By path, like Create: the declaration covers every mention.
		{"later mention", "Absent-ok: `pkg/peer.go`\n\nThen read `pkg/peer.go`.\n", ExitClean, "ok (2 absent-ok)", "missing"},
		{"beside create", "Create: `pkg/brandnew.go`\nAbsent-ok: `pkg/peer.go`\n", ExitClean,
			"ok (1 to-be-created exempt) (1 absent-ok)", "missing"},
		// An absent-ok path that exists is checked like any other.
		{"existing, bad line", "Absent-ok: `pkg/exists.go:99`\n", ExitFindings,
			"line-out-of-range: pkg/exists.go:99 (file has 3 lines)", "absent-ok)"},
		{"existing, good line", "Absent-ok: `pkg/exists.go:2`\n", ExitClean, ": ok\n", "absent-ok)"},
		// The inline form covers its own line only; the list ends at prose.
		{"inline is one line", "Absent-ok: `pkg/peer.go`\nAlso read `pkg/absent.go`.\n", ExitFindings,
			"missing: pkg/absent.go", "missing: pkg/peer.go"},
		{"prose ends the list", "Absent-ok:\n- `pkg/peer.go`\nRead `pkg/absent.go`.\n", ExitFindings,
			"missing: pkg/absent.go", "missing: pkg/peer.go"},
		// Prose that merely begins with the word is not a marker.
		{"prose is not a marker", "Absent-ok paths are below, then:\n- `pkg/absent.go`\n", ExitFindings,
			"missing: pkg/absent.go", "-"},
	} {
		out, rc := c.run(tc.body)
		if rc != tc.rc || !strings.Contains(out, tc.must) || (tc.mustNot != "-" && strings.Contains(out, tc.mustNot)) {
			t.Errorf("%s: rc=%d out=%q, want rc=%d with %q and without %q", tc.name, rc, out, tc.rc, tc.must, tc.mustNot)
		}
	}
}

// ─── fence lists ─────────────────────────────────────────────────────────────

func TestFenceListsAreAbsentOk(t *testing.T) {
	c := newLintCase(t, map[string]string{"pkg/exists.go": "one\ntwo\nthree\n"})
	for _, tc := range []struct {
		name, body string
		rc         int
		must       string
		mustNot    string
	}{
		// The same line, in every phrasing the fence knows.
		{"do not touch", "Do not touch `pkg/peer.go` (line1's branch).\n", ExitClean, "ok (1 absent-ok)", "missing"},
		{"DON'T TOUCH", "DON'T TOUCH `pkg/peer.go`.\n", ExitClean, "ok (1 absent-ok)", "missing"},
		{"curly don’t touch", "Don’t touch `pkg/peer.go`.\n", ExitClean, "ok (1 absent-ok)", "missing"},
		{"off-limits", "`pkg/peer.go` is off-limits.\n", ExitClean, "ok (1 absent-ok)", "missing"},
		{"off limits", "Off limits: `pkg/peer.go`.\n", ExitClean, "ok (1 absent-ok)", "missing"},
		{"read-only for you", "`pkg/peer.go` is read-only for you.\n", ExitClean, "ok (1 absent-ok)", "missing"},
		{"not yours", "`pkg/peer.go` is not yours.\n", ExitClean, "ok (1 absent-ok)", "missing"},
		{"Korean", "`pkg/peer.go` 는 건드리지 마라.\n", ExitClean, "ok (1 absent-ok)", "missing"},
		{"Korean 말 것", "- `pkg/peer.go`: 건드리지 말 것.\n", ExitClean, "ok (1 absent-ok)", "missing"},
		// The list under a colon-ended fence line.
		{"list", "- **Do not touch:**\n  - `pkg/peer1.go`;\n  - `pkg/peer2.go`\n    and `pkg/peer3.go` (wrapped).\n",
			ExitClean, "ok (3 absent-ok)", "missing"},
		{"plain list", "Off-limits (line1's branch):\n- `pkg/peer1.go`\n- `pkg/peer2.go`\n", ExitClean, "ok (2 absent-ok)", "missing"},
		// By path: a fenced file named again later is the same claim.
		{"later mention", "Do not touch `pkg/peer.go`.\n\nThe peer adds `pkg/peer.go`.\n", ExitClean, "ok (2 absent-ok)", "missing"},
		// An existing fenced path is still checked for its line.
		{"existing, bad line", "Do not touch `pkg/exists.go:99`.\n", ExitFindings, "line-out-of-range: pkg/exists.go:99", "-"},
		// "yourself" contains "yours"; it is not a fence.
		{"not yourself", "Do it not yourself: `pkg/absent.go`.\n", ExitFindings, "missing: pkg/absent.go", "-"},
	} {
		out, rc := c.run(tc.body)
		if rc != tc.rc || !strings.Contains(out, tc.must) || (tc.mustNot != "-" && strings.Contains(out, tc.mustNot)) {
			t.Errorf("%s: rc=%d out=%q, want rc=%d with %q and without %q", tc.name, rc, out, tc.rc, tc.must, tc.mustNot)
		}
	}
}

func TestFenceBoundaries(t *testing.T) {
	// The fence's edges are where it lies: each case puts a real missing
	// path right past one, and that path must still be a finding.
	c := newLintCase(t, nil)
	for _, tc := range []struct{ name, body string }{
		{"a blank line ends it", "Do not touch:\n- `pkg/peer.go`\n\n- `pkg/absent.go`\n"},
		{"a heading ends it", "Off-limits:\n- `pkg/peer.go`\n## Next\n- `pkg/absent.go`\n"},
		{"prose ends it", "Do not touch:\n- `pkg/peer.go`\nThen edit `pkg/absent.go`.\n"},
		// A fence item's sibling is not its list: `- **Signals:**` after
		// `- **Do not touch:**` and its sub-items (q38big, hfdraft).
		{"a sibling bullet ends it", "- **Do not touch:**\n  - `pkg/peer.go`\n- **Signals:** see `pkg/absent.go`.\n"},
		// Boilerplate whose wrapped line says "not yours", followed by the
		// rules list (q3harness:630, q3kvround:624): the line does not end
		// in a colon, so it introduces no list.
		{"a fence sentence is not a list opener",
			"- **File boundary:** edit only your files. Another round's files may\n" +
				"  change under a peer; an unrelated failing gate is not yours — report it, do not fix it.\n" +
				"- **Design references:** mistral.rs (`pkg/absent.go`) is a design reference.\n"},
		// A fence sentence inside a numbered step, then that step's own
		// sub-list at the same indent (ctxslots:527).
		{"a fence sentence does not take its item's sub-list",
			"1. **Red:** in `searched_ctx`, make the answer one lower\n" +
				"     when it is above the floor. Do not touch the trained probe.\n" +
				"     - Run `pkg/absent.go` and predict.\n"},
	} {
		out, rc := c.run(tc.body)
		got := findings(out)
		if rc != ExitFindings || len(got) != 1 || !strings.Contains(got[0], "missing: pkg/absent.go") {
			t.Errorf("%s: rc=%d findings=%q, want exactly the missing pkg/absent.go", tc.name, rc, got)
		}
	}
}

// ─── what the tool says about them ───────────────────────────────────────────

func TestHelpHintAndTelemetryNameAbsentOk(t *testing.T) {
	var buf bytes.Buffer
	if code := Main([]string{"-h"}, &buf, &buf); code != ExitClean {
		t.Fatalf("-h: code=%d", code)
	}
	if !lineWith(buf.String(), "Absent-ok:", "do not touch") {
		t.Errorf("-h must name Absent-ok: and the fence phrases in one line, got %q", buf.String())
	}

	c := newLintCase(t, nil)
	out, rc := c.run("See `pkg/absent.go`.\n")
	if rc != ExitFindings || !lineWith(out, "hint —", "Absent-ok:", "do not touch") {
		t.Errorf("a missing finding's hint must name Absent-ok: and fences in one line, got %q", out)
	}

	// The count has its own telemetry key, beside exempt.
	tf := filepath.Join(t.TempDir(), "telemetry.jsonl")
	t.Setenv("OUTSOURCE_TELEMETRY_FILE", tf)
	t.Setenv("OUTSOURCE_TELEMETRY", "1")
	_, rc = c.run("Absent-ok:\n- `pkg/peer1.go`\n- `pkg/peer2.go`\nDo not touch `pkg/peer3.go`.\n")
	telemetry.Record("spec-lint", nil, rc, time.Now())
	data, err := os.ReadFile(tf)
	if err != nil {
		t.Fatal(err)
	}
	var ev struct {
		Details map[string]string `json:"details"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(data), &ev); err != nil {
		t.Fatalf("telemetry row: %v (%s)", err, data)
	}
	if ev.Details["absent-ok"] != "3" || ev.Details["exempt"] != "0" || ev.Details["missing"] != "0" {
		t.Errorf("telemetry details = %v, want absent-ok=3 exempt=0 missing=0", ev.Details)
	}
}

// lineWith reports whether one line of out contains every one of subs.
func lineWith(out string, subs ...string) bool {
	for _, l := range strings.Split(out, "\n") {
		all := true
		for _, s := range subs {
			if !strings.Contains(l, s) {
				all = false
				break
			}
		}
		if all {
			return true
		}
	}
	return false
}
