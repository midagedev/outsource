package audit

import (
	"regexp"
	"strings"
)

// The command observation flags. A flag is an observation, not a verdict: it
// says "this command looks like X", and the reader decides whether X was
// legitimate for that round. One table, one predicate per row, a reason each —
// the same shape as the recognisers, for the same reason: a row whose reason
// nobody can state is a flag nobody trusts.
type cmdFlag struct {
	name   string
	reason string
	match  func(cmd string) bool
}

var cmdFlags = []cmdFlag{
	{
		name:   "git-write",
		reason: "a git subcommand that changes repository state (commit, push, checkout, switch, stash, restore, add, reset, rebase, merge, cherry-pick, tag, branch -D) — lead-only in every delegation spec",
		match:  gitWrite,
	},
	{
		name:   "rm-rf",
		reason: "rm carrying both -r and -f — a recursive forced delete",
		match:  rmRF,
	},
	{
		name:   "pgrep-wait",
		reason: "'until ! pgrep -f …' — the banned wait form; pgrep matches the waiting shell itself, so the wait can never end (CLAUDE.md waits-and-signals rule)",
		match: func(cmd string) bool {
			return strings.Contains(collapse(cmd), "until ! pgrep")
		},
	},
	{
		name:   "pipe-tail",
		reason: "output piped into tail/head — on a gate this hides the FAIL lines ('never | tail a gate': output to a file, grep the summary)",
		match:  pipeTail,
	},
	{
		name:   "nested-launch",
		reason: "outsource-run or grok-run invoked from inside a round — a nested launch the launcher refuses (exit 64) since a delegate once relaunched itself and exited with zero implementation",
		match: func(cmd string) bool {
			return reNestedLaunch.MatchString(cmd)
		},
	},
	{
		name:   "network-install",
		reason: "installs from the network (npm i -g, brew install, pip install, curl|sh) — changes machine state outside the round's tree",
		match:  networkInstall,
	},
}

var reNestedLaunch = regexp.MustCompile(`(^|[^[:alnum:]_-])(outsource-run|grok-run)(\.sh)?($|[[:space:];&|<>'"])`)

var reNetwork = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bnpm\s+(i|install)\b[^|;&\n]*\s(-g|--global)\b`),
	regexp.MustCompile(`(?i)\bbrew\s+install\b`),
	regexp.MustCompile(`(?i)\bpip3?\s+install\b`),
}

// gitWrite reports a git subcommand that changes repository state. The walk
// mirrors guard.go's two-pass structure minus its rigour: split the command on
// shell separators, find the `git` token, skip the flags between it and the
// subcommand (-C/-c/--git-dir swallow their values), then look at the
// subcommand. `branch` alone is a listing form; only `branch -D` is a write.
func gitWrite(cmd string) bool {
	for _, seg := range splitSegments(cmd) {
		toks := strings.Fields(seg)
		for i, t := range toks {
			if t != "git" {
				continue
			}
			sub, rest := subcommandAfterGit(toks[i+1:])
			switch sub {
			case "commit", "push", "checkout", "switch", "stash", "restore", "add",
				"reset", "rebase", "merge", "cherry-pick", "tag":
				return true
			case "branch":
				for _, r := range rest {
					if r == "-D" {
						return true
					}
				}
			}
		}
	}
	return false
}

// subcommandAfterGit walks the tokens after `git` to its subcommand, skipping
// global flags. Flags that take a value as their next token (-C, -c,
// --git-dir, --work-tree) swallow it; inline --flag=value forms are whole.
func subcommandAfterGit(toks []string) (sub string, rest []string) {
	i := 0
	for i < len(toks) {
		t := toks[i]
		if !strings.HasPrefix(t, "-") {
			return t, toks[i+1:]
		}
		switch t {
		case "-C", "-c", "--git-dir", "--work-tree", "--namespace":
			i += 2 // flag plus its value
			continue
		}
		i++
	}
	return "", nil
}

// rmRF reports rm with both -r and -f among its immediately following option
// tokens, in one cluster (-rf, -fr) or across several (-r -f).
func rmRF(cmd string) bool {
	for _, seg := range splitSegments(cmd) {
		toks := strings.Fields(seg)
		for i, t := range toks {
			if t != "rm" {
				continue
			}
			r, f := false, false
			for j := i + 1; j < len(toks) && strings.HasPrefix(toks[j], "-"); j++ {
				for _, c := range toks[j][1:] {
					switch c {
					case 'r':
						r = true
					case 'f':
						f = true
					}
				}
			}
			if r && f {
				return true
			}
		}
	}
	return false
}

// pipeTail reports a pipe whose right side begins with tail or head.
func pipeTail(cmd string) bool {
	for _, seg := range strings.Split(cmd, "\n") {
		// Treat || as a separator, not a pipe into a command.
		for _, part := range strings.Split(seg, "||") {
			if !strings.Contains(part, "|") {
				continue
			}
			halves := strings.SplitN(part, "|", 2)
			right := strings.Fields(halves[1])
			if len(right) > 0 && (right[0] == "tail" || right[0] == "head") {
				return true
			}
		}
	}
	return false
}

// networkInstall covers the package-manager forms plus curl|sh. The pipe form
// is checked structurally (left begins curl/wget, right begins sh/bash) so
// `curl -s URL` alone does not trip it.
func networkInstall(cmd string) bool {
	for _, re := range reNetwork {
		if re.MatchString(cmd) {
			return true
		}
	}
	for _, seg := range strings.Split(cmd, "\n") {
		for _, part := range strings.Split(seg, "||") {
			if !strings.Contains(part, "|") {
				continue
			}
			halves := strings.SplitN(part, "|", 2)
			left := strings.Fields(halves[0])
			right := strings.Fields(halves[1])
			if len(left) == 0 || len(right) == 0 {
				continue
			}
			base := left[0]
			if i := strings.LastIndexByte(base, '/'); i >= 0 {
				base = base[i+1:]
			}
			rb := right[0]
			if rb == "sudo" && len(right) > 1 {
				rb = right[1]
			}
			if (base == "curl" || base == "wget") && (rb == "sh" || rb == "bash" || rb == "zsh") {
				return true
			}
		}
	}
	return false
}

// splitSegments cuts a command on the separators that end one shell command
// and start another, so `git status && git commit -m x` is two segments and
// only the second is judged.
func splitSegments(cmd string) []string {
	r := strings.NewReplacer("&&", "\n", "||", "\n", ";", "\n", "|", "\n", "&", "\n")
	return strings.Split(r.Replace(cmd), "\n")
}

// collapse squeezes runs of whitespace, for patterns written against the
// canonical spaced form.
func collapse(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// flagsFor returns every flag name whose predicate fires, in table order.
func flagsFor(cmd string) []string {
	var out []string
	for _, f := range cmdFlags {
		if f.match(cmd) {
			out = append(out, f.name)
		}
	}
	return out
}
