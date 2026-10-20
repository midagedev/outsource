package runs

import (
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strconv"
	"strings"

	"github.com/midagedev/outsource/internal/human"
)

// Descendant is one live process under a round's harness child. An idle round
// names these because the trail is silent while a tool call runs: opencode
// writes the tool event only when the call ends. Measured 2026-10-20 (round
// sjcmp): "no output for 15m" while the harness child had a descendant
// `just remote-static` waiting for a remote slot at 0 % CPU, which the lead
// could only tell from a hung harness by running `ps` by hand.
type Descendant struct {
	Pid            int64   `json:"pid"`
	CPU            float64 `json:"cpu"`
	ElapsedSeconds int64   `json:"elapsedSeconds"`
	Command        string  `json:"command"`
}

const (
	descMaxDepth = 8  // a fork bomb must not hang `runs`
	descMaxPids  = 64 // total, across all levels
	descShown    = 3
	descCmdWidth = 100
)

// idleDescendants returns the leaves under root (the harness child, or the
// launcher when the record has none), the descendant walk bounded by
// descMaxDepth and descMaxPids. more counts the leaves not returned. A failing
// pgrep/ps yields nothing: the caller prints the "no child" line only when
// the walk itself succeeded, so ok is false then.
func idleDescendants(root string) (leaves []Descendant, more int, ok bool) {
	if _, err := strconv.Atoi(strings.TrimSpace(root)); err != nil {
		return nil, 0, false
	}
	// Without pgrep every walk looks childless; say nothing rather than
	// "no child process".
	if _, err := exec.LookPath("pgrep"); err != nil {
		return nil, 0, false
	}
	parent := map[string]string{} // pid -> parent
	hasKids := map[string]bool{}
	var order []string
	frontier := []string{strings.TrimSpace(root)}
	for depth := 0; depth < descMaxDepth && len(frontier) > 0 && len(order) < descMaxPids; depth++ {
		var next []string
		for _, p := range frontier {
			out, err := exec.Command("pgrep", "-P", p).Output()
			if err != nil {
				continue // exit 1 is "no children"; anything else adds nothing
			}
			for _, c := range strings.Fields(string(out)) {
				if len(order) >= descMaxPids {
					break
				}
				parent[c] = p
				hasKids[p] = true
				order = append(order, c)
				next = append(next, c)
			}
		}
		frontier = next
	}
	if len(order) == 0 {
		return nil, 0, true
	}
	out, err := exec.Command("ps", "-o", "pid=,pcpu=,etime=,command=", "-p", strings.Join(order, ",")).Output()
	if err != nil && len(out) == 0 {
		return nil, 0, false
	}
	for _, d := range parsePS(string(out)) {
		if !hasKids[strconv.FormatInt(d.Pid, 10)] {
			leaves = append(leaves, d)
		}
	}
	return pickLeaves(leaves)
}

// pickLeaves keeps the descShown leaves that have run longest (the ones most
// likely to be what the round is waiting on) and counts the rest.
func pickLeaves(leaves []Descendant) ([]Descendant, int, bool) {
	sort.SliceStable(leaves, func(i, j int) bool { return leaves[i].ElapsedSeconds > leaves[j].ElapsedSeconds })
	if len(leaves) > descShown {
		return leaves[:descShown], len(leaves) - descShown, true
	}
	return leaves, 0, true
}

// parsePS reads `ps -o pid=,pcpu=,etime=,command=` rows. The command is the
// rest of the line and may hold spaces; a row that does not parse is skipped.
func parsePS(out string) []Descendant {
	var ds []Descendant
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		pid, err1 := strconv.ParseInt(f[0], 10, 64)
		cpu, err2 := strconv.ParseFloat(f[1], 64)
		el, ok := parseEtime(f[2])
		if err1 != nil || err2 != nil || !ok {
			continue
		}
		// Rejoin from the original line so runs of spaces inside the command
		// survive; strip the three leading fields.
		cmd := strings.TrimSpace(line)
		for i := 0; i < 3; i++ {
			cmd = strings.TrimSpace(cmd[strings.IndexAny(cmd, " \t"):])
		}
		ds = append(ds, Descendant{Pid: pid, CPU: cpu, ElapsedSeconds: el, Command: cmd})
	}
	return ds
}

// parseEtime converts ps etime, `[[dd-]hh:]mm:ss` (the same on Linux and
// macOS), to seconds.
func parseEtime(s string) (int64, bool) {
	var days int64
	if i := strings.Index(s, "-"); i >= 0 {
		d, err := strconv.ParseInt(s[:i], 10, 64)
		if err != nil {
			return 0, false
		}
		days, s = d, s[i+1:]
	}
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, false
	}
	total := days
	total *= 24
	var h, m, sec int64
	var err error
	if len(parts) == 3 {
		if h, err = strconv.ParseInt(parts[0], 10, 64); err != nil {
			return 0, false
		}
		parts = parts[1:]
	}
	if m, err = strconv.ParseInt(parts[0], 10, 64); err != nil {
		return 0, false
	}
	if sec, err = strconv.ParseInt(parts[1], 10, 64); err != nil {
		return 0, false
	}
	return (total+h)*3600 + m*60 + sec, true
}

func clipCmd(s string) string {
	r := []rune(s)
	if len(r) <= descCmdWidth {
		return s
	}
	return string(r[:descCmdWidth-1]) + "…"
}

// idleRoot is the process whose descendants are listed: the harness child,
// else the launcher.
func idleRoot(r *Record) string {
	if r.ChildPid != "" {
		return r.ChildPid
	}
	return r.Pid
}

// printDescendants writes the "running:" lines under an idle round's verdict.
// Silent when ps/pgrep could not be run.
func printDescendants(w io.Writer, r *Record) {
	leaves, more, ok := idleDescendants(idleRoot(r))
	if !ok {
		return
	}
	if len(leaves) == 0 {
		fmt.Fprintln(w, "         running: no child process — the harness itself is idle (waiting on the API or hung)")
		return
	}
	for _, d := range leaves {
		fmt.Fprintf(w, "         running: %d %s · cpu %.0f%% · up %s\n", d.Pid, clipCmd(d.Command), d.CPU, human.Secs(d.ElapsedSeconds))
	}
	if more > 0 {
		fmt.Fprintf(w, "         … and %d more\n", more)
	}
}
