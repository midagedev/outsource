package overlays

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// skill builds an installed-skill layout with the given declared overlays.
func skill(t *testing.T, declared map[string]string, withUser bool) string {
	t.Helper()
	dir := t.TempDir()
	refs := filepath.Join(dir, "references")
	if err := os.MkdirAll(filepath.Join(refs, "overlays"), 0o755); err != nil {
		t.Fatal(err)
	}
	if withUser {
		write(t, filepath.Join(refs, "local-overlay.md"), "user")
	}
	for name, body := range declared {
		write(t, filepath.Join(refs, "overlays", name), body)
	}
	return dir
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func kinds(rs []Resolved) []string {
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.Kind)
	}
	return out
}

func TestAssemblyOrderPutsInRepoLast(t *testing.T) {
	target := filepath.Join(t.TempDir(), "ds5")
	write(t, filepath.Join(target, ".outsource", "overlay.md"), "project")
	sk := skill(t, map[string]string{
		"solutions.md": "---\npaths:\n  - " + filepath.Dir(target) + "/*\n---\nbody",
	}, true)

	got := kinds(Resolve(sk, target, discard()))
	want := []string{"user", "declared", "project"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("order = %v, want %v — the in-repo overlay must be able to win on conflict", got, want)
	}
}

func TestDeclaredMatchesOnlyItsPaths(t *testing.T) {
	base := t.TempDir()
	sk := skill(t, map[string]string{
		"solutions.md": "---\npaths:\n  - " + base + "/ds*\n---\nbody",
	}, false)

	if rs := Resolve(sk, filepath.Join(base, "ds6-worktree"), discard()); len(rs) != 1 {
		t.Fatalf("ds6-worktree should match ds*: got %d overlays", len(rs))
	}
	if rs := Resolve(sk, filepath.Join(base, "other-repo"), discard()); len(rs) != 0 {
		t.Fatalf("other-repo must not pick up a foreign overlay: got %v", rs)
	}
}

// The failure this mode exists to prevent is a *silent* one, so a declaration
// that can never match has to be loud rather than absent.
func TestDeclarationWithoutPathsWarnsAndIsSkipped(t *testing.T) {
	sk := skill(t, map[string]string{"broken.md": "---\nname: nopaths\n---\nbody"}, false)
	var warn bytes.Buffer
	rs := Resolve(sk, t.TempDir(), &warn)
	if len(rs) != 0 {
		t.Fatalf("a pathless declaration must not apply: got %v", rs)
	}
	if !strings.Contains(warn.String(), "could never apply") {
		t.Fatalf("expected a warning naming the problem, got %q", warn.String())
	}
}

func TestNoFrontMatterIsReportedNotAppliedEverywhere(t *testing.T) {
	sk := skill(t, map[string]string{"plain.md": "just a body, no front matter\n"}, false)
	var warn bytes.Buffer
	if rs := Resolve(sk, t.TempDir(), &warn); len(rs) != 0 {
		t.Fatalf("a file without front matter must not apply globally: got %v", rs)
	}
	if !strings.Contains(warn.String(), "front matter") {
		t.Fatalf("expected a front-matter warning, got %q", warn.String())
	}
}

// `/**` is what lets one pattern cover a clone and the worktrees under it.
func TestDoubleStarCoversDescendants(t *testing.T) {
	base := t.TempDir()
	sk := skill(t, map[string]string{
		"solutions.md": "---\npaths:\n  - " + base + "/repo/**\n---\nbody",
	}, false)

	for _, target := range []string{
		filepath.Join(base, "repo"),
		filepath.Join(base, "repo", "nested", "worktree"),
	} {
		if rs := Resolve(sk, target, discard()); len(rs) != 1 {
			t.Fatalf("%s should be covered by /**: got %d", target, len(rs))
		}
	}
	if rs := Resolve(sk, filepath.Join(base, "elsewhere"), discard()); len(rs) != 0 {
		t.Fatalf("/** must not escape its base: got %v", rs)
	}
}

func TestScalarPathsForm(t *testing.T) {
	base := t.TempDir()
	sk := skill(t, map[string]string{
		"one.md": "---\npaths: " + base + "/only\n---\nbody",
	}, false)
	if rs := Resolve(sk, filepath.Join(base, "only"), discard()); len(rs) != 1 {
		t.Fatalf("scalar paths: form should match: got %d", len(rs))
	}
}

func TestMainPrintsPathsInOrder(t *testing.T) {
	target := filepath.Join(t.TempDir(), "clone")
	write(t, filepath.Join(target, ".outsource", "overlay.md"), "project")
	sk := skill(t, nil, true)

	var out, errBuf bytes.Buffer
	if rc := Main([]string{"--root", target, "--skill-dir", sk}, &out, &errBuf); rc != 0 {
		t.Fatalf("rc = %d, stderr = %q", rc, errBuf.String())
	}
	lines := strings.Fields(strings.TrimSpace(out.String()))
	if len(lines) != 2 || !strings.HasSuffix(lines[0], "local-overlay.md") || !strings.HasSuffix(lines[1], ".outsource/overlay.md") {
		t.Fatalf("unexpected output: %q", out.String())
	}
}

func discard() *bytes.Buffer { return &bytes.Buffer{} }
