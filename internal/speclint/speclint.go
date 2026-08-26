// Package speclint lints a delegation spec for mechanically wrong premises
// before launch: file references that do not resolve from --root, and
// path:line citations beyond the end of the file they point at.
//
// Why this exists (measured 2026-08-15/16): five wrong premises in one
// session's specs — a nonexistent tool name, a nonexistent column, an absent
// fixture label, a wrong runner cwd, a wrong manifest path. The delegate
// caught each one mid-round, but each cost part of a round. This runs on the
// lead's side, before launch, on the class that is checkable without a
// model: paths and line citations.
//
// Ported from skills/outsource/bin/spec-lint.sh, which was a bash argument
// parser around an embedded Python program. Behaviour is byte-identical by
// contract — the parity gate compares the two implementations over every
// markdown document in this repository — and the one permitted difference is
// the tool's own name in diagnostics: the shell printed $0's basename, and
// the binary names the tool.
//
// Known limit, carried over unchanged: a path inside a command that sets its
// own root (`npx vitest run --root web src/lib/x.test.ts`, `make -C dir`) is
// resolved from --root and the spec's directory, not from that command's
// base, so it can report missing for a file that exists. Teaching the linter
// every tool's cwd flag would cost more precision than it buys; such paths
// are written repo-relative in the spec instead.
package speclint

import (
	"bytes"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/midagedev/outsource/internal/telemetry"
)

// Exit codes keep the shell contract: 0 clean · 1 findings · 2 usage error
// or unreadable spec. Callers and the test suite branch on these numbers.
const (
	ExitClean    = 0
	ExitFindings = 1
	ExitUsage    = 2
)

const usageLine = "usage: spec-lint [--root <dir>] [--quiet] <spec.md> [<spec.md>...]"

func usage(w io.Writer) {
	fmt.Fprintln(w, usageLine)
}

// Main is the spec-lint entry point. It returns an exit code rather than
// calling os.Exit so the multi-call binary owns exiting.
func Main(args []string, stdout, stderr io.Writer) int {
	// The shell's parse loop, carried over exactly: --root consumes the next
	// argument blindly (even "--"), -- ends flag parsing, help exits cleanly
	// from wherever it appears, and an unknown dash-argument is named and
	// refused the moment it is seen rather than after the loop.
	root, quiet := "", false
	rootSet := false
	var specs []string
parse:
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--root":
			if i+1 >= len(args) {
				fmt.Fprintln(stderr, "spec-lint: --root needs a value")
				usage(stderr)
				return ExitUsage
			}
			root, rootSet = args[i+1], true
			i++
		case a == "--quiet":
			quiet = true
		case a == "-h" || a == "--help":
			// Help goes to stdout and exits 0 — usage ERRORS go to stderr
			// and exit 2. The shell was equally explicit about that split.
			fmt.Fprintln(stdout, usageLine)
			return ExitClean
		case a == "--":
			specs = append(specs, args[i+1:]...)
			break parse
		case strings.HasPrefix(a, "-"):
			fmt.Fprintf(stderr, "spec-lint: unknown flag: %s\n", a)
			usage(stderr)
			return ExitUsage
		default:
			specs = append(specs, a)
		}
	}
	if !rootSet {
		// The shell's default was $PWD, not a getcwd — an explicit --root ""
		// is still the empty string, and still fails the directory check
		// below exactly as it did there.
		root = os.Getenv("PWD")
	}
	if len(specs) == 0 {
		usage(stderr)
		return ExitUsage
	}
	if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
		fmt.Fprintf(stderr, "spec-lint: --root is not a directory: %s\n", root)
		return ExitUsage
	}
	// An unreadable spec is a usage-class error, checked for ALL of them
	// before any linting, so a typo'd path cannot hide findings from an
	// earlier file.
	for _, s := range specs {
		if fi, err := os.Stat(s); err != nil || !fi.Mode().IsRegular() {
			fmt.Fprintf(stderr, "spec-lint: spec is not a readable file: %s\n", s)
			return ExitUsage
		}
	}

	var total lintStats
	for _, spec := range specs {
		s, err := lintSpec(spec, root, quiet, stdout)
		if err != nil {
			fmt.Fprintf(stderr, "spec-lint: spec is not a readable file: %s\n", spec)
			return ExitUsage
		}
		total.findings += s.findings
		total.exempt += s.exempt
		total.missing += s.missing
		total.already += s.already
	}
	// Counts are strings because telemetry.Note is a map[string]string — the
	// same shape every other tool writes. Mining splits on the keys.
	telemetry.Note("findings", strconv.Itoa(total.findings))
	telemetry.Note("exempt", strconv.Itoa(total.exempt))
	telemetry.Note("missing", strconv.Itoa(total.missing))
	telemetry.Note("already-exists", strconv.Itoa(total.already))
	// The to-be-created exemption only fires for paths the spec declares in
	// the marker language creationLines knows ("Create: <path>", a
	// colon-terminated line opening a list, the Korean forms). A spec that
	// introduces its new file some other way — a section heading naming the
	// path, with "New file." as the next sentence — gets one missing finding
	// per mention of a file the round exists to write, and nothing on the
	// screen says the exemption was even available (measured 2026-08-27: ten
	// findings, all one to-be-created tool, on a spec whose author had read
	// this linter's source). Say it once, and only when the shape matches: a
	// spec that already declares creations, or one with no missing findings,
	// hears nothing.
	if total.missing > 0 && total.exempt == 0 {
		fmt.Fprintln(stdout, "spec-lint: hint — if any of those are files this round CREATES, "+
			"declare them so: a line `Create: <path>` (or `New file: <path>`, `신규 파일: <path>`), "+
			"or `Create:` on its own line opening a list of them. Declared paths are exempt "+
			"everywhere else they are named, and are checked the other way instead: already-exists.")
	}
	if total.findings > 0 {
		return ExitFindings
	}
	return ExitClean
}

// lintStats is one spec's counts, summed across files in Main for the
// telemetry row. findings is the exit-1 axis; exempt is the to-be-created
// suppression; missing vs already-exists split the findings that those two
// checks produce (line-out-of-range is a finding in neither bucket).
type lintStats struct {
	findings, exempt, missing, already int
}

// lintSpec lints one spec and prints its findings and its ok line. The
// returned error is only the read of the file itself, which the shell caught
// with a pre-flight [ -f ] check; the caller turns it into the same message.
func lintSpec(spec, root string, quiet bool, stdout io.Writer) (lintStats, error) {
	data, err := os.ReadFile(spec)
	if err != nil {
		return lintStats{}, err
	}
	// The shell read the file through open(..., "r", errors="replace") and
	// readlines(): undecodable bytes became U+FFFD instead of aborting, and
	// universal newlines turned CRLF and lone CR into \n before anything
	// looked at the text. Both matter here — markers are line-anchored, and
	// a CRLF spec's lines otherwise end in a stray \r.
	text := translateNewlines(replaceInvalidUTF8(data))
	lines := splitKeepNewlines(text)

	// A relative reference is a wrong premise only when it resolves under
	// none of the plausible bases: --root as given, --root's git toplevel
	// (a reference written repo-relative while --root is a subdirectory),
	// and the spec file's own directory. A path that exists somewhere sane
	// is not a wrong premise, only a differently-rooted one.
	bases := []string{root}
	for _, extra := range []string{toplevel(root), parentDir(spec)} {
		if extra != "" && !containsString(bases, extra) {
			bases = append(bases, extra)
		}
	}

	// Template spans are matched over the whole file because they may wrap
	// lines, so the token walk below carries file-level offsets to compare.
	var spans [][2]int
	spans = appendSpan(spans, spanRe.FindAllStringIndex(text, -1))
	spans = appendSpan(spans, commentRe.FindAllStringIndex(text, -1))

	toCreate := creationLines(lines)
	refs := collectRefs(lines, spans)

	// Pre-pass, so the to-be-created exemption is by PATH and not by
	// position: a spec names the file it is creating in its whitelist, then
	// again in the completion criteria and the test section — declaring it
	// once is the claim, and every later mention is the same claim. Reporting
	// those moved the cry-wolf defect a page down instead of fixing it.
	created := map[string]bool{}
	for _, r := range refs {
		if toCreate[r.lineno] {
			p, _ := resolve(r.path, bases)
			created[p] = true
		}
	}

	var st lintStats
	seen := map[lineTok]bool{}
	for _, r := range refs {
		key := lineTok{r.lineno, r.tok}
		if seen[key] {
			continue
		}
		seen[key] = true
		resolved, exists := resolve(r.path, bases)
		if created[resolved] {
			// A file the spec is creating, at its declaration or anywhere
			// else it is named. It is exempt from the missing check — and
			// gets the opposite one at its declaration: a to-be-created
			// path that already exists means the spec and the tree disagree
			// about what the round is for. That finding is only visible
			// here, before launch.
			st.exempt++
			if exists && toCreate[r.lineno] {
				fmt.Fprintf(stdout, "%s:%d: already-exists: %s (spec says create it; resolved: %s)\n",
					spec, r.lineno, r.tok, resolved)
				st.findings++
				st.already++
			}
			continue
		}
		if !exists {
			fmt.Fprintf(stdout, "%s:%d: missing: %s (resolved: %s)\n",
				spec, r.lineno, r.tok, resolved)
			st.findings++
			st.missing++
			continue
		}
		if r.cited && !isDir(resolved) {
			total := lineCount(resolved)
			if r.line < 1 || r.line > total {
				fmt.Fprintf(stdout, "%s:%d: line-out-of-range: %s (file has %d lines)\n",
					spec, r.lineno, r.tok, total)
				st.findings++
			}
		}
	}
	if st.findings == 0 && !quiet {
		// The exemption count is printed rather than kept quiet: a
		// suppression nobody can see is how a linter starts lying.
		note := ""
		if st.exempt > 0 {
			note = fmt.Sprintf(" (%d to-be-created exempt)", st.exempt)
		}
		fmt.Fprintf(stdout, "%s: ok%s\n", spec, note)
	}
	return st, nil
}

// ─── the reference walk ─────────────────────────────────────────────────────

// ref is one reference found in a spec: the line it sits on, the token as it
// appeared after edge-punctuation trimming (what findings print), the path it
// claims, and the cited line number when the token was a path:line citation.
type ref struct {
	lineno int
	tok    string
	path   string
	cited  bool
	line   int
}

// lineTok is the dedup key: the same token on the same line is one claim,
// and findings for it are printed once.
type lineTok struct {
	line int
	tok  string
}

// token is one candidate reference: its byte offset within the line and the
// raw text. Python counted runes and Go counts bytes; span containment is
// invariant under that change of unit because both sides compare positions
// of the same characters.
type token struct {
	pos  int
	text string
}

// collectRefs walks the spec's lines in order and yields every path
// reference: a path:line citation (an explicit claim about a specific file,
// bare filename or not), or a slash-bearing token that ends in a known file
// extension. A bare filename with no citation is deliberately not checked:
// prose naming a file as a concept ("copy the relevant CLAUDE.md clauses")
// produced ~30 findings across this repo's own docs with zero real defects
// (measured 2026-08-16) — a linter at that precision gets ignored, which
// costs more than the class it catches.
func collectRefs(lines []string, spans [][2]int) []ref {
	var refs []ref
	base := 0
	for i, line := range lines {
		lineno := i + 1
		for _, t := range tokens(line) {
			lo := base + t.pos
			if isTemplate(t.text) {
				continue
			}
			tok := stripEdges(t.text)
			if tok == "" || isTemplate(tok) {
				continue
			}
			inSpan := false
			for _, sp := range spans {
				if sp[0] <= lo && lo < sp[1] {
					inSpan = true
					break
				}
			}
			if inSpan {
				continue // inside a <...> template span or an HTML comment
			}
			if m := citeRe.FindStringSubmatch(tok); m != nil &&
				(strings.Contains(m[1], "/") || hasExt(m[1])) {
				refs = append(refs, ref{lineno: lineno, tok: tok, path: m[1], cited: true, line: atoiClamped(m[2])})
			} else if strings.Contains(tok, "/") && hasExt(tok) {
				refs = append(refs, ref{lineno: lineno, tok: tok, path: tok})
			}
		}
		base += len(line)
	}
	return refs
}

// tokens splits a line into (offset, text) pairs the way the shell's Python
// did: maximal runs of non-whitespace first, then each run split again at
// every "](" and backtick, so a markdown link yields both label and target
// and prose hugging inline code ("레시피(`references/grok.md`") yields the
// code span on its own. Offsets stay exact because every separator is
// accounted for by its length.
func tokens(line string) []token {
	var out []token
	i := 0
	for i < len(line) {
		for i < len(line) {
			r, sz := utf8.DecodeRuneInString(line[i:])
			if !isSpaceRune(r) {
				break
			}
			i += sz
		}
		if i >= len(line) {
			break
		}
		start := i
		for i < len(line) {
			r, sz := utf8.DecodeRuneInString(line[i:])
			if isSpaceRune(r) {
				break
			}
			i += sz
		}
		out = splitToken(line[start:i], start, out)
	}
	return out
}

// splitToken reproduces Python's re.split with a capturing group, which
// alternates text, separator, text, separator, ... — empty text pieces
// included — with every piece advancing the offset. That accounting is what
// keeps sub-token positions exact, and positions decide span membership.
func splitToken(s string, base int, out []token) []token {
	pos := 0
	for {
		bt := strings.IndexByte(s[pos:], '`')
		lb := strings.Index(s[pos:], "](")
		// Interior parens are separators too: prose like
		// "install-service(cmd/gadak/service.go — …)" glues a real path to a
		// word, and the fused token neither resolves nor matches the tree —
		// a false MISSING that costs an edit-relint cycle (measured
		// 2026-08-20: three specs in one session). stripEdges only trims
		// edges; the split has to happen here, where offsets stay exact.
		pr := strings.IndexAny(s[pos:], "()")
		i, ln := -1, 0
		if bt >= 0 {
			i, ln = pos+bt, 1
		}
		if lb >= 0 && (i < 0 || pos+lb < i) {
			i, ln = pos+lb, 2
		}
		if pr >= 0 && (i < 0 || pos+pr < i) {
			i, ln = pos+pr, 1
		}
		if i < 0 {
			break
		}
		out = append(out, token{base + pos, s[pos:i]}) // text before the separator
		pos = i + ln                                   // the separator advances the offset but is never a token
	}
	return append(out, token{base + pos, s[pos:]}) // trailing text, empty allowed
}

// isSpaceRune answers the whitespace set of Python's \s in str patterns: the
// Unicode whitespace Go knows, plus U+001C-U+001F, which CPython counts via
// their bidirectional class and Go does not. Go's own \s is ASCII-only, so
// "\S+" and the marker regexes cannot use it directly without drifting from
// the shell on non-ASCII input.
func isSpaceRune(r rune) bool {
	if r >= 0x1c && r <= 0x1f {
		return true
	}
	return unicode.IsSpace(r)
}

// pyS spells Python's \s as an RE2 class (Go's \s is ASCII-only), and pyNotS
// its negation. Used wherever the ported patterns contained a \s or \S.
const (
	pyS    = `[\s\x{1c}-\x{1f}\x{85}\p{Zs}\p{Zl}\p{Zp}]`
	pyNotS = `[^\s\x{1c}-\x{1f}\x{85}\p{Zs}\p{Zl}\p{Zp}]`
)

var (
	// `path.md:12` or `path.md:12-34` — the range's first number is the
	// claim. Digits are ASCII here: Python's \d also matched Unicode decimal
	// digits (Nd), which the corpus never contained and int() alone could
	// have parsed anyway.
	citeRe = regexp.MustCompile(`^([^:]+):([0-9]+)(?:-[0-9]+)?$`)
	// $VAR/x.md and ${VAR}/x.md are the environment's phrasing, not a path
	// the tree owes the spec.
	dollarRe = regexp.MustCompile(`\$\{?[A-Za-z_][A-Za-z0-9_]*`)
	// A <...> span is template text: everything inside it is a placeholder,
	// even when the brackets sit on other whitespace tokens ("<e.g. root
	// CLAUDE.md sections ...>") or wrap several lines, as the spans in
	// references/spec-template.md do. The length cap keeps a stray "<<"
	// heredoc from opening a span that swallows the rest of the document.
	spanRe = regexp.MustCompile(`<[^<>]{0,300}>`)
	// An HTML comment is a note to the lead about the skill's own layout —
	// how to assemble the spec, which file owns which rule — not a citation
	// the delegate will act on. Linting it meant every assembled spec
	// reported the preamble's own `references/…` paths as missing from the
	// TARGET repo: one guaranteed finding per round, and the fastest way to
	// teach someone to stop reading a linter. Kept separate from spanRe
	// because that pattern caps its length to survive a stray "<<", and a
	// comment block is legitimately long.
	commentRe = regexp.MustCompile(`(?s)<!--.*?-->`)
)

// Creation markers, in the spec's own language. A spec that has the delegate
// create files names files that do not exist yet — that is the point — and
// linting them as missing meant every creating spec (most of them) opened
// with guaranteed findings, which is the precision problem this linter's
// header keeps warning about, arriving from the other direction. English
// alone was its own measured defect: a spec written in Korean declared its
// new files just as clearly, matched nothing, and every creating round
// opened with the guaranteed findings this exemption exists to prevent — the
// feature had never once fired for those specs (measured 2026-08-18: four
// specs, six findings, all of them files the round was being sent to create).
const createWords = `create|creates?d?|new files?|files? to create|to create|add files?` +
	`|신규(?:` + pyS + `*파일)?|새` + pyS + `*파일|생성할?` + pyS + `*파일|만들` + pyS + `*파일|추가할?` + pyS + `*파일`

// A creation marker can open any kind of list item. Bullets were accepted
// and numbers were not, so `1. Create: <path>` reported the file as missing
// while `- Create: <path>` two lines away was exempt — a distinction no spec
// author would predict, and one that lands on a Deliverables section, where
// to-be-created paths are densest (measured 2026-08-19, fixed the same day).
const bullet = `(?:[-*+]|[0-9]+[.)])`

var (
	createOpenRe = regexp.MustCompile(`(?i)^` + pyS + `*(?:` + bullet + pyS + `+)?(?:#+` + pyS +
		`*)?(?:\*\*)?(?:` + createWords + `)(?:\*\*)?` + pyS + `*(?:\([^)]*\))?` + pyS + `*:` + pyS + `*$`)
	createInlineRe = regexp.MustCompile(`(?i)^` + pyS + `*(?:` + bullet + pyS + `+)?(?:#+` + pyS +
		`*)?(?:\*\*)?(?:` + createWords + `)(?:\*\*)?` + pyS + `*:` + pyS + `+` + pyNotS)
	// Inside a creation block: a list item, or a wrapped continuation of one.
	listItemRe     = regexp.MustCompile(`^` + pyS + `*` + bullet + pyS + `+` + pyNotS)
	continuationRe = regexp.MustCompile(`^` + pyS + `{2,}` + pyNotS)
)

// creationLines returns the 1-based line numbers whose path references are
// to-be-created. An inline "Create: <path>" marker marks its own line and
// closes any open block; a "Create:"-ending line opens one; inside a block,
// list items and continuations are marked, a blank line does not end the
// list, and any other prose does.
func creationLines(lines []string) map[int]bool {
	marked := map[int]bool{}
	inBlock := false
	for i, line := range lines {
		n := i + 1
		if createInlineRe.MatchString(line) {
			marked[n] = true
			inBlock = false
			continue
		}
		if createOpenRe.MatchString(line) {
			inBlock = true
			continue
		}
		if !inBlock {
			continue
		}
		if strings.TrimSpace(line) == "" {
			continue // a blank line inside a list does not end it
		}
		if listItemRe.MatchString(line) || continuationRe.MatchString(line) {
			marked[n] = true
		} else {
			inBlock = false
		}
	}
	return marked
}

// stripEdges trims prose punctuation, but keeps a path's leading dots.
//
// A plain strip turned `.github/workflows/ci.yml` into
// `github/workflows/ci.yml`, which does not exist, so a file that was right
// there was reported missing — and every spec citing a CI workflow (most of
// them) opened with a guaranteed false finding (measured 2026-08-19: two of
// six audit specs, one finding each, both false). Same failure mode as the
// HTML-comment and to-be-created exemptions: a linter that invents findings
// is one people stop reading. Restricted to tokens containing "/", so prose
// keeps losing its dots ("e.g.", "v0.14.1.") exactly as before.
//
// edge is a character SET, matching Python's str.strip(chars): both sides
// lose any run of these characters, not just one prefix.
const edge = "`\"'()[]{}<>,;:.!?*|\\…—–«»“”‘’"

func stripEdges(raw string) string {
	tok := strings.Trim(raw, edge)
	if tok == "" || !strings.Contains(tok, "/") {
		return tok
	}
	// Re-attach as many leading dots as the raw token had before trimming.
	head := raw[:len(raw)-len(strings.TrimLeft(raw, edge))]
	dots := len(head) - len(strings.TrimRight(head, "."))
	if dots > 0 {
		return strings.Repeat(".", dots) + tok
	}
	return tok
}

// isTemplate reports the tokens that are not claims about the tree: URLs
// and www.* domains, placeholders (<scratch>/task.md, DONE-<track>), globs,
// prose-abbreviated paths, and $VAR forms.
func isTemplate(tok string) bool {
	switch {
	case strings.Contains(tok, "://") || strings.HasPrefix(tok, "www."):
		return true
	case strings.Contains(tok, "<") || strings.Contains(tok, ">"):
		return true
	case strings.Contains(tok, "*") || strings.Contains(tok, "?"):
		return true
	case strings.Contains(tok, "..."):
		return true
	case dollarRe.MatchString(tok):
		return true
	}
	return false
}

// exts is the set a dotted token must end in to count as a file reference.
// Dotted non-paths — domains (example.com), versions (v0.14.1, glm-5.3),
// abbreviations ("e.g.") — fail it. No equivalent table existed in this repo
// before the shell version wrote one (searched skills/, scripts/,
// install.sh; only install.sh's copy/manifest diffs were found).
var exts = buildExts(`md markdown rst txt text log lock
sh bash zsh fish
py rb pl pm lua rake
go rs swift m mm c cc cpp cxx h hh hpp java kt scala cs
js jsx ts tsx mjs cjs json jsonc yaml yml toml xml html htm css scss sass less
svelte vue astro elm sql graphql gql proto
png jpg jpeg gif bmp svg webp ico mp3 mp4 mov wav csv tsv
diff patch env ini cfg conf properties plist`)

func buildExts(list string) map[string]bool {
	m := make(map[string]bool)
	for _, e := range strings.Fields(list) {
		m[e] = true
	}
	return m
}

// hasExt reports whether the path's last segment carries a known file
// extension. A dotfile's dot is not an extension (".gitignore" has none),
// and neither is a leading-dot directory (".github" — checked by the whole
// token containing "/", not by the segment).
func hasExt(path string) bool {
	seg := path
	if i := strings.LastIndex(seg, "/"); i >= 0 {
		seg = seg[i+1:]
	}
	i := strings.LastIndex(seg, ".")
	if i <= 0 {
		return false // no dot, or a dotfile whose base is empty
	}
	return exts[strings.ToLower(seg[i+1:])]
}

// ─── resolution ──────────────────────────────────────────────────────────────

// pyJoin is os.path.join, NOT filepath.Join: joining must not clean the
// result, because the resolved path is printed in findings and a relative
// part's "./" prefix or a doubled separator is visible output, not plumbing.
// Python concatenates and leaves it alone; filepath.Join would collapse
// both. An absolute second part wins outright.
func pyJoin(a, b string) string {
	switch {
	case strings.HasPrefix(b, "/"):
		return b
	case a == "":
		return b
	case strings.HasSuffix(a, "/"):
		return a + b
	default:
		return a + "/" + b
	}
}

// pyAbs reproduces os.path.abspath: a relative path is anchored at the
// working directory, the result is lexically cleaned, and "~" is left
// untouched (abspath does not expanduser). Cleaning here is faithful — the
// shell did it too — and these paths only ever serve as resolution bases,
// never as finding text on their own.
func pyAbs(p string) string {
	if !strings.HasPrefix(p, "/") {
		wd, err := os.Getwd()
		if err != nil {
			return p
		}
		p = wd + "/" + p
	}
	return filepath.Clean(p)
}

// toplevel is the nearest ancestor holding .git, so a reference written
// repo-relative in a spec whose --root is a subdirectory still resolves
// there. "" means no ancestor has one.
func toplevel(start string) string {
	d := pyAbs(start)
	for {
		if exists(pyJoin(d, ".git")) {
			return d
		}
		parent := filepath.Dir(d)
		if parent == d {
			return ""
		}
		d = parent
	}
}

// parentDir is the spec file's own directory, one of the plausible bases.
func parentDir(spec string) string {
	return filepath.Dir(pyAbs(spec))
}

// resolve turns a claimed path into (resolved, exists). Absolute and ~
// paths are the claim itself. A relative one is a wrong premise only when
// it resolves under none of the plausible bases; the first base's join is
// what the failure message names, so its exact spelling is output.
func resolve(path string, bases []string) (string, bool) {
	if strings.HasPrefix(path, "~") {
		p := expanduser(path)
		return p, exists(p)
	}
	if strings.HasPrefix(path, "/") {
		if exists(path) {
			return path, true
		}
		// A leading "/" that exists nowhere on disk is usually a markdown
		// root-relative link ("[English](/README.md)"), which GitHub
		// resolves against the repo root — retry it that way before calling
		// it a wrong premise.
		for _, b := range bases {
			q := pyJoin(b, strings.TrimLeft(path, "/"))
			if exists(q) {
				return q, true
			}
		}
		return path, false
	}
	first, haveFirst := "", false
	for _, b := range bases {
		p := pyJoin(b, path)
		if !haveFirst {
			first, haveFirst = p, true
		}
		if exists(p) {
			return p, true
		}
	}
	return first, false
}

// expanduser carries os.path.expanduser for the forms a spec actually
// writes: "~" and "~/…". "~user/…" needs the password database; it is
// returned unchanged rather than guessed at.
func expanduser(p string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return p // expanduser with no HOME left the path alone too
	}
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") {
		return home + p[1:]
	}
	return p
}

// exists answers what os.path.exists answered, including the case where it
// differs from a naive stat check: a BROKEN symlink does not exist, because
// stat (not lstat) follows it and fails.
func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// lineCount counts lines the way iterating a binary file did: "a\nb" is 2
// (an editor's view, not wc -l's 1), "a\n" is 1, "" is 0. It reads raw
// bytes, so a non-UTF-8 target is counted, not rejected.
func lineCount(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0 // checked exists() a moment ago; a vanished file has no lines either way
	}
	n := bytes.Count(data, []byte{'\n'})
	if len(data) > 0 && data[len(data)-1] != '\n' {
		n++
	}
	return n
}

// atoiClamped parses a citation's line number. The regex guarantees digits
// but not a size that fits: Python's int() is unbounded and a 30-digit
// citation is simply "past the end of every file", so an overflow clamps to
// a value that stays past every end rather than failing.
func atoiClamped(s string) int {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return math.MaxInt
	}
	return int(n)
}

// ─── reading the spec the way Python read it ─────────────────────────────────

// replaceInvalidUTF8 mirrors errors="replace": every undecodable byte
// becomes U+FFFD instead of aborting the lint. CPython replaces one maximal
// invalid subsequence with one replacement character — a truncated multibyte
// lead plus its continuation bytes is one bad character to a reader — so
// consecutive undecodable bytes merge into a single U+FFFD here too.
func replaceInvalidUTF8(data []byte) string {
	if utf8.Valid(data) {
		return string(data)
	}
	var b strings.Builder
	for len(data) > 0 {
		r, size := utf8.DecodeRune(data)
		if r != utf8.RuneError || size != 1 {
			b.WriteRune(r)
			data = data[size:]
			continue
		}
		b.WriteRune(0xFFFD)
		data = data[1:]
		for len(data) > 0 {
			r2, s2 := utf8.DecodeRune(data)
			if r2 != utf8.RuneError || s2 != 1 {
				break // a valid rune ends the bad subsequence
			}
			data = data[1:]
		}
	}
	return b.String()
}

// translateNewlines mirrors text mode's universal newlines: CRLF and lone
// CR both become \n. Markers are line-anchored, so a CRLF spec must have
// its lines end where Python's readlines ended them, not with a stray \r.
func translateNewlines(s string) string {
	if !strings.ContainsRune(s, '\r') {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\r' {
			if i+1 < len(s) && s[i+1] == '\n' {
				i++
			}
			b.WriteByte('\n')
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// splitKeepNewlines splits after each \n like readlines(): every line keeps
// its terminator, a trailing fragment without one is still a line, and empty
// input is zero lines.
func splitKeepNewlines(s string) []string {
	var lines []string
	for len(s) > 0 {
		i := strings.IndexByte(s, '\n')
		if i < 0 {
			lines = append(lines, s)
			break
		}
		lines = append(lines, s[:i+1])
		s = s[i+1:]
	}
	return lines
}

func appendSpan(spans [][2]int, idx [][]int) [][2]int {
	for _, m := range idx {
		spans = append(spans, [2]int{m[0], m[1]})
	}
	return spans
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
