package speclint

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every case here pins a seam where Python and Go semantics differ
// silently: the join that must not clean, the splitter whose offsets decide
// span membership, the dot a path keeps and prose loses, the line count
// that is an editor's and not wc's. The contract suite
// (tests/spec-lint.test.sh) covers behaviour end to end; these hold the
// arithmetic underneath it, where a port goes wrong without going loud.

func TestPyJoinDoesNotClean(t *testing.T) {
	// The resolved path is printed in findings, so join semantics are
	// visible output. os.path.join concatenates and leaves the result
	// alone; filepath.Join would collapse doubled separators and drop the
	// "./" a spec wrote — a different finding text for the same tree.
	for _, c := range []struct{ a, b, want string }{
		{"a", "b", "a/b"},
		{"a/", "b", "a/b"},
		{"a//", "b", "a//b"}, // a doubled separator stays: Python's, not Go's
		{"a", "./b.md", "a/./b.md"},
		{"a", "/b.md", "/b.md"}, // an absolute part wins outright
		{"", "b", "b"},
		{"a", "", "a/"}, // an empty part still adds the separator
	} {
		if got := pyJoin(c.a, c.b); got != c.want {
			t.Errorf("pyJoin(%q, %q) = %q, want %q", c.a, c.b, got, c.want)
		}
	}
}

func TestSplitTokenKeepsOffsetsAndEmpties(t *testing.T) {
	// re.split with a capturing group alternates text, sep, text — empty
	// pieces included — and every piece advances the offset. Those offsets
	// decide whether a token falls inside a <...> span; a drift of one byte
	// moves a finding in or out silently.
	for _, c := range []struct {
		in   string
		want []token
	}{
		{"plain.md", []token{{0, "plain.md"}}},
		{"`a.md`", []token{{0, ""}, {1, "a.md"}, {6, ""}}},
		// Interior parens split too (2026-08-20): prose-glued paths like
		// "word(a/b.go" used to fuse into one unresolvable token.
		{"[x](b.md)", []token{{0, "[x"}, {4, "b.md"}, {9, ""}}},
		{"svc(a/b.go", []token{{0, "svc"}, {4, "a/b.go"}}},
		{"``", []token{{0, ""}, {1, ""}, {2, ""}}},
		{"a`](b", []token{{0, "a"}, {2, ""}, {4, "b"}}},
	} {
		var got []token
		for _, tok := range splitToken(c.in, 0, nil) {
			got = append(got, token{tok.pos, tok.text})
		}
		if len(got) != len(c.want) {
			t.Errorf("splitToken(%q) = %v, want %v", c.in, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("splitToken(%q)[%d] = %+v, want %+v", c.in, i, got[i], c.want[i])
			}
		}
	}
}

func TestTokensWhitespaceIsPythons(t *testing.T) {
	// Go's \s is ASCII-only; Python's \s in str patterns is Unicode
	// whitespace (NBSP included) plus U+001C-U+001F. "a b" is TWO
	// tokens there and one here unless the classifier says otherwise, and
	// the offset must count the NBSP's two bytes.
	var got []token
	for _, tok := range tokens("a b.md") {
		got = append(got, token{tok.pos, tok.text})
	}
	want := []token{{0, "a"}, {3, "b.md"}}
	if len(got) != len(want) {
		t.Fatalf("tokens = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("tokens[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
	if toks := tokens("\x1cx.md"); len(toks) != 1 || toks[0] != (token{1, "x.md"}) {
		t.Errorf("U+001C must split like Python's \\s did, got %v", toks)
	}
}

func TestStripEdgesKeepsPathDotsAndStripsProseDots(t *testing.T) {
	// The dot rule is directional: a "/"-bearing token keeps its leading
	// dots (`.github/workflows/ci.yml` was once reported as a missing
	// `github/workflows/ci.yml` — the file sat right there), and a token
	// without "/" keeps losing them ("e.g.", "v0.14.1.").
	for raw, want := range map[string]string{
		".github/workflows/ci.yml":  ".github/workflows/ci.yml",
		"./pkg/x.go":                "./pkg/x.go",
		"..github/workflows/ci.yml": "..github/workflows/ci.yml", // two dots, both structural
		"`pkg/x.go`":                "pkg/x.go",
		"(pkg/x.go).":               "pkg/x.go",
		"..e.g.":                    "e.g", // no slash: prose dots go
		"v0.14.1.":                  "v0.14.1",
		"..":                        "",
	} {
		if got := stripEdges(raw); got != want {
			t.Errorf("stripEdges(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestLineCountIsAnEditorsNotWcs(t *testing.T) {
	// Python iterating a binary file counted "a\nb" as 2 lines and "a\n" as
	// 1; wc -l would say 1 and 1. The out-of-range check prints this count,
	// so the unit is a contract.
	dir := t.TempDir()
	for body, want := range map[string]int{
		"":           0,
		"a":          1,
		"a\n":        1,
		"\n":         1,
		"a\nb":       2,
		"a\nb\n":     2,
		"a\n\n":      2,
		"a\r\nb\r\n": 2, // raw bytes: line_count did not translate newlines
	} {
		p := filepath.Join(dir, "t")
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := lineCount(p); got != want {
			t.Errorf("lineCount(%q) = %d, want %d", body, got, want)
		}
	}
}

func TestHasExt(t *testing.T) {
	// The extension table is the gate between "a path" and "prose with
	// dots". A dotfile's dot is not an extension; the check is on the last
	// segment only; the comparison folds case.
	for path, want := range map[string]bool{
		"x.md": true, "X.MD": true, "pkg/archive.tar.diff": true,
		"archive.tar.gz": false, // gz is not in the table — the shell's, faithfully
		"dir.d/x":        false, // the dot is in a directory segment, not the file
		"Makefile":       false, ".gitignore": false, ".md": false,
		"example.com": false, // a bare domain, not a .com file
	} {
		if got := hasExt(path); got != want {
			t.Errorf("hasExt(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestCreationBlockBoundaries(t *testing.T) {
	// The exemption makes the linter say less, so its edges are the
	// dangerous part: a numbered list item is a marker (bullets-only was a
	// measured defect), a blank line inside the list does not end it, a
	// continuation is part of it, and unrelated prose does end it.
	body := strings.Join([]string{
		"Intro mentions pkg/brandnew.go", // 1: a mention is not a marker
		"",                               // 2
		"1. Create: pkg/a.go",            // 3: inline form on a numbered item
		"- new files:",                   // 4: opens a block (itself unmarked)
		"  - pkg/b.go",                   // 5
		"",                               // 6: blank inside the list
		"  - pkg/c.go",                   // 7
		"    wrapped pkg/d.go",           // 8: continuation
		"Prose pkg/e.go",                 // 9: ends the block
	}, "\n")
	got := creationLines(splitKeepNewlines(body + "\n"))
	want := map[int]bool{3: true, 5: true, 7: true, 8: true}
	if len(got) != len(want) {
		t.Fatalf("creationLines = %v, want %v", got, want)
	}
	for n := range want {
		if !got[n] {
			t.Errorf("line %d must be marked to-create, got %v", n, got)
		}
	}
}

func TestSplitKeepNewlinesAndTranslation(t *testing.T) {
	// readlines() keeps terminators, treats a trailing fragment as a line,
	// and (after universal newlines) ends a CRLF line with \n, not \r —
	// markers are line-anchored and a stray \r would break every one of
	// them on a CRLF spec.
	lines := splitKeepNewlines(translateNewlines("Create:\r\n- `x.go`\r\ntail"))
	if len(lines) != 3 || lines[0] != "Create:\n" || lines[1] != "- `x.go`\n" || lines[2] != "tail" {
		t.Errorf("CRLF translation + split = %q", lines)
	}
	if lines := splitKeepNewlines(""); len(lines) != 0 {
		t.Errorf("empty input must be zero lines, got %q", lines)
	}
}

func TestReplaceInvalidUTF8MergesSubsequences(t *testing.T) {
	// errors="replace" put ONE U+FFFD per maximal invalid subsequence, not
	// one per byte: a truncated multibyte lead plus its continuation bytes
	// is a single bad character to a reader, and byte-per-replacement would
	// shift every offset after it.
	if got, want := replaceInvalidUTF8([]byte("a\xf0\x9f\x80")), "a�"; got != want {
		t.Errorf("truncated sequence = %q, want %q", got, want)
	}
	// A valid rune right after the bad bytes ends the subsequence: \xe0\xa0
	// is truncated (nothing can continue it into a character), 'b' can.
	if got, want := replaceInvalidUTF8([]byte("\xe0\xa0b")), "�b"; got != want {
		t.Errorf("valid tail must survive, got %q want %q", got, want)
	}
	// But a byte sequence that IS a character survives whole: \xe0\xa0\xb5
	// is U+0835, not three bad bytes.
	if got, want := replaceInvalidUTF8([]byte("\xe0\xa0\xb5X")), "࠵X"; got != want {
		t.Errorf("valid multibyte must round-trip, got %q want %q", got, want)
	}
	if got := replaceInvalidUTF8([]byte("clean")); got != "clean" {
		t.Errorf("valid input must round-trip, got %q", got)
	}
}

func TestMainEndToEnd(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "root")
	if err := os.MkdirAll(filepath.Join(root, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "pkg", "exists.go"), []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	spec := filepath.Join(dir, "spec.md")
	run := func(body string, extra ...string) (string, int) {
		if err := os.WriteFile(spec, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		args := append([]string{"--root", root, spec}, extra...)
		var out bytes.Buffer
		code := Main(args, &out, &out)
		return out.String(), code
	}

	// A finding's resolved path is spelled by os.path.join semantics — the
	// "./" the spec wrote survives — and names the FIRST base's join.
	out, rc := run("See `pkg/absent.go` and `./pkg/absent.go`.\n")
	if rc != ExitFindings {
		t.Errorf("missing path rc = %d, want %d (out: %s)", rc, ExitFindings, out)
	}
	if !strings.Contains(out, "missing: pkg/absent.go (resolved: "+root+"/pkg/absent.go)") {
		t.Errorf("finding text must name the first base's join verbatim, got: %s", out)
	}

	out, rc = run("See `pkg/exists.go:99`.\n")
	if rc != ExitFindings || !strings.Contains(out, "line-out-of-range: pkg/exists.go:99 (file has 3 lines)") {
		t.Errorf("citation past EOF: rc=%d out=%s", rc, out)
	}

	// The exemption and its visible count, including on a CRLF spec.
	out, rc = run("Create:\n- `pkg/brandnew.go`\n")
	if rc != ExitClean || !strings.Contains(out, "ok (1 to-be-created exempt)") {
		t.Errorf("create block: rc=%d out=%s", rc, out)
	}
	out, rc = run("Create:\r\n- `pkg/brandnew.go`\r\n")
	if rc != ExitClean || !strings.Contains(out, "ok (1 to-be-created exempt)") {
		t.Errorf("CRLF create block: rc=%d out=%s", rc, out)
	}

	out, rc = run("Create:\n- `pkg/exists.go`\n")
	if rc != ExitFindings || !strings.Contains(out, "already-exists: pkg/exists.go") {
		t.Errorf("already-exists: rc=%d out=%s", rc, out)
	}

	// The hint fires exactly when the exemption was available and unused: a
	// spec whose new file is introduced by a heading rather than a marker
	// gets a finding per mention and no clue that the marker exists.
	out, rc = run("### `pkg/brandnew.go`\n\nNew file. Also `pkg/brandnew.go` in the criteria.\n")
	if rc != ExitFindings || !strings.Contains(out, "hint — if any of those are files this round CREATES") {
		t.Errorf("undeclared creation must carry the hint: rc=%d out=%s", rc, out)
	}
	// A spec that already declares creations does not get lectured, even
	// when some OTHER path is genuinely missing.
	out, rc = run("Create:\n- `pkg/brandnew.go`\n\nSee `pkg/absent.go`.\n")
	if rc != ExitFindings || strings.Contains(out, "hint —") {
		t.Errorf("a spec using the marker must not get the hint: rc=%d out=%s", rc, out)
	}
	// Nor does a clean run.
	out, rc = run("See `pkg/exists.go`.\n")
	if rc != ExitClean || strings.Contains(out, "hint —") {
		t.Errorf("clean run must not hint: rc=%d out=%s", rc, out)
	}

	out, rc = run("See `pkg/exists.go`.\n", "--quiet")
	if rc != ExitClean || out != "" {
		t.Errorf("quiet clean run must print nothing: rc=%d out=%q", rc, out)
	}

	// Help goes to stdout with exit 0; usage errors to stderr with exit 2.
	var buf bytes.Buffer
	if code := Main([]string{"-h"}, &buf, &buf); code != ExitClean || !strings.HasPrefix(buf.String(), "usage: spec-lint ") {
		t.Errorf("-h: code=%d out=%q", code, buf.String())
	}
	buf.Reset()
	if code := Main(nil, &buf, &buf); code != ExitUsage || strings.Count(buf.String(), "usage:") != 1 {
		t.Errorf("no specs: code=%d out=%q — the bare usage line only", code, buf.String())
	}
	buf.Reset()
	if code := Main([]string{"--root"}, &buf, &buf); code != ExitUsage || !strings.Contains(buf.String(), "--root needs a value") {
		t.Errorf("--root value: code=%d out=%q", code, buf.String())
	}
}
