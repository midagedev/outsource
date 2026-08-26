// Package overlays resolves which overlay files apply to a delegation, in the
// order spec assembly must concatenate them.
//
// There are two ways to attach a project overlay, and they exist for two
// different shapes of working copy.
//
//   - **In-repo** — `<root>/.outsource/overlay.md`, committed next to the code
//     it describes. This is the right default: the overlay versions with the
//     branch, arrives with a fresh clone, and a teammate gets it for free.
//
//   - **Declared** — a file under `<skill>/references/overlays/` whose front
//     matter lists the paths it applies to, the way Claude Code's
//     `.claude/rules/*` declare theirs. The overlay lives once in user scope
//     and names the working copies it covers.
//
// The declared mode exists because the in-repo default inverts once one repo
// has several checkouts on one machine — clones plus worktrees, each parked on
// a different branch. Committing the overlay then means N copies drifting with
// N branches, and the lead who edits it in whichever checkout they are standing
// in forks the rules for every other one, silently. Measured on a repo with 16
// checkouts: two project overlays written months apart in two different clones,
// neither aware of the other, with disagreeing gate tables.
//
// Both modes can be active at once; a declared overlay does not suppress an
// in-repo one. Order is least specific first, so the in-repo file wins on
// conflict:
//
//	user overlay → declared overlays (filename order) → in-repo overlay
package overlays

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Resolved is one overlay file that applies, and why.
type Resolved struct {
	Path   string
	Kind   string // "user" | "declared" | "project"
	Reason string // for declared: the pattern that matched
}

// Main prints the overlay files that apply to --root, one per line, in
// assembly order.
func Main(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("overlays", flag.ContinueOnError)
	fs.SetOutput(stderr)
	root := fs.String("root", "", "repo root the delegation targets (default: cwd)")
	skillDir := fs.String("skill-dir", "", "installed skill directory (default: derived from this binary)")
	explain := fs.Bool("explain", false, "print kind and matching pattern beside each path")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	target, err := absDir(*root)
	if err != nil {
		fmt.Fprintf(stderr, "overlays: %v\n", err)
		return 2
	}
	skill, err := resolveSkillDir(*skillDir)
	if err != nil {
		fmt.Fprintf(stderr, "overlays: %v\n", err)
		return 2
	}

	for _, r := range Resolve(skill, target, stderr) {
		if *explain {
			fmt.Fprintf(stdout, "%s\t%s\t%s\n", r.Path, r.Kind, r.Reason)
		} else {
			fmt.Fprintln(stdout, r.Path)
		}
	}
	return 0
}

// Resolve returns the overlays that apply to target, in assembly order.
// Malformed declarations are reported on warn and skipped — one bad file must
// not block every delegation, but it must not be silent either, because an
// overlay that never applies looks exactly like an overlay that has nothing
// to say.
func Resolve(skillDir, target string, warn io.Writer) []Resolved {
	var out []Resolved

	if p := filepath.Join(skillDir, "references", "local-overlay.md"); fileExists(p) {
		out = append(out, Resolved{Path: p, Kind: "user", Reason: "always"})
	}

	dir := filepath.Join(skillDir, "references", "overlays")
	entries, err := os.ReadDir(dir)
	if err == nil {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
				names = append(names, e.Name())
			}
		}
		sort.Strings(names)
		for _, name := range names {
			path := filepath.Join(dir, name)
			patterns, err := declaredPaths(path)
			if err != nil {
				fmt.Fprintf(warn, "overlays: %s: %v — skipped\n", path, err)
				continue
			}
			if pat, ok := matchAny(patterns, target); ok {
				out = append(out, Resolved{Path: path, Kind: "declared", Reason: pat})
			}
		}
	}

	if p := filepath.Join(target, ".outsource", "overlay.md"); fileExists(p) {
		out = append(out, Resolved{Path: p, Kind: "project", Reason: "in-repo"})
	}
	return out
}

// declaredPaths reads the `paths:` list from a leading `---` front matter
// block. Only the one key is parsed: this is a declaration file, not a
// configuration language, and a YAML dependency would be the repo's first.
func declaredPaths(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	if !sc.Scan() || strings.TrimSpace(sc.Text()) != "---" {
		return nil, fmt.Errorf("no front matter — a declared overlay must open with --- and list paths:")
	}

	var patterns []string
	inPaths := false
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "---" {
			break
		}
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") || trimmed == "" {
			continue
		}
		if inPaths && strings.HasPrefix(trimmed, "- ") {
			patterns = append(patterns, unquote(strings.TrimSpace(trimmed[2:])))
			continue
		}
		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			// A new top-level key ends the paths block.
			inPaths = false
		}
		if rest, ok := strings.CutPrefix(trimmed, "paths:"); ok {
			inPaths = true
			if v := unquote(strings.TrimSpace(rest)); v != "" {
				patterns = append(patterns, v) // scalar form: paths: ~/repo/x
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(patterns) == 0 {
		return nil, fmt.Errorf("front matter has no paths: entries — this overlay could never apply")
	}
	return patterns, nil
}

func unquote(s string) string {
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1]
	}
	return s
}

// matchAny reports the first pattern that covers target.
//
// A pattern matches the checkout root, not files inside it, because that is
// what a delegation targets. `filepath.Match` does not cross separators, so a
// trailing `/**` is handled here: it means "this directory or anything under
// it", which is how a pattern covers worktrees parked beside their clone.
func matchAny(patterns []string, target string) (string, bool) {
	for _, raw := range patterns {
		pat := expandHome(raw)
		if base, ok := strings.CutSuffix(pat, "/**"); ok {
			if target == base || strings.HasPrefix(target, base+string(os.PathSeparator)) {
				return raw, true
			}
			if ok, _ := filepath.Match(base, target); ok {
				return raw, true
			}
			// A glob with /** still has to cover deeper paths: try the glob
			// against every ancestor of target.
			for dir := filepath.Dir(target); dir != "/" && dir != "."; dir = filepath.Dir(dir) {
				if ok, _ := filepath.Match(base, dir); ok {
					return raw, true
				}
			}
			continue
		}
		if ok, _ := filepath.Match(pat, target); ok {
			return raw, true
		}
		if pat == target {
			return raw, true
		}
	}
	return "", false
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}

func absDir(p string) (string, error) {
	if p == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		return wd, nil
	}
	abs, err := filepath.Abs(expandHome(p))
	if err != nil {
		return "", err
	}
	return abs, nil
}

// resolveSkillDir derives the installed skill directory from this binary's
// location (`<skill>/bin/outsource`), so the resolver works the same whether it
// was invoked through a shim, by path, or from PATH.
func resolveSkillDir(override string) (string, error) {
	if override != "" {
		return absDir(override)
	}
	if env := os.Getenv("OUTSOURCE_SKILL_DIR"); env != "" {
		return absDir(env)
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return filepath.Dir(filepath.Dir(exe)), nil
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}
