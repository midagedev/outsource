package runs

import (
	"fmt"
	"io"
	"strings"

	"github.com/midagedev/outsource/internal/human"
)

// The table format is part of the contract: a human reads these columns and
// the tests assert on them, so the widths are carried over exactly.
const listFormat = "%-8s %-16s %-6s %-6s %8s %6s %-9s %s\n"

// clip truncates by runes rather than bytes. The shell used ${var:0:16}, which
// on bash 3.2 cuts bytes and can split a multi-byte character into mojibake.
// Labels are ASCII in every measured round, so this is identical in practice
// and strictly better when it is not.
func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

func cmdList(f filter, stdout io.Writer) int {
	recs, _ := List()
	now := nowUnix()
	any := false
	for _, r := range recs {
		if !f.mine(r) {
			continue
		}
		if !any {
			fmt.Fprintf(stdout, listFormat, "STATE", "LABEL", "PROV", "HARNESS", "ELAPSED", "IDLE", "OWNER", "SPEC")
			any = true
		}
		st := r.State()
		idleCol := "-"
		var idle int64
		idleKnown := false
		if st == Running {
			idle, idleKnown = r.Idle(now)
			if idleKnown {
				idleCol = human.Secs(idle)
			} else {
				idleCol = "?"
			}
		}
		owner := clip(r.OwnerSession, 8)
		if r.OwnerSession != "" {
			owner += "…"
		}
		fmt.Fprintf(stdout, listFormat,
			st, clip(r.Label, 16), r.Provider, HarnessShort(r.Harness),
			human.Secs(r.Elapsed(now)), idleCol, owner, r.Spec)

		logOr := func(fallback string) string {
			if r.Log != "" {
				return r.Log
			}
			return fallback
		}
		switch st {
		case Failed:
			fmt.Fprintf(stdout, "         rc=%s  log=%s\n", r.RC, logOr("none"))
		case Orphan:
			fmt.Fprintf(stdout, "         started but never finished — pid %s is gone; log=%s\n", r.Pid, logOr("none"))
		case Running:
			if idleKnown && idle >= StallSeconds() {
				// Deliberately not a kill instruction. A stall is a reason to
				// look at the log, and the round may still recover on its own.
				fmt.Fprintf(stdout, "         no output for %s — check %s before doing anything to it\n",
					human.Secs(idle), logOr("the harness log"))
			}
		}
	}
	if !any {
		fmt.Fprintf(stdout, "no delegated runs on record (%s)\n", Dir())
	}
	return 0
}

// dimIdle marks the number that follows as "silent for", not "running for".
const dimIdle = "⋯"

// disambiguator gives two tracks that share a label distinct names. A "#2"
// suffix is the honest minimum — it says "there is more than one of these and
// this line cannot tell them apart", and the fix is a real --label at launch,
// not a longer suffix here.
type disambiguator map[string]int

func (d disambiguator) next(label string) string {
	d[label]++
	if n := d[label]; n > 1 {
		return fmt.Sprintf("%s#%d", label, n)
	}
	return label
}

// cmdLine renders one line, no colour: whatever displays it owns the styling.
//
// LIVE work shows machine-wide, ownership or not: a running round spends the
// same plan quota whichever window launched it, and a round another session
// left editing a tree of yours is exactly the thing a status line exists to
// surface (measured 2026-08-27: a delegate launched a nested round in the
// lead's worktree, and the lead's scoped line showed nothing). A live round
// that is not yours carries a ⇄ prefix so the line never claims foreign work
// as your own. Finished rounds stay scoped — an outcome is only news to the
// window that launched it — and orphans age off this view after
// OrphanLineSeconds (the full `runs` listing keeps them all).
func cmdLine(f filter, stdout io.Writer) int {
	recs, _ := List()
	now := nowUnix()
	seen := disambiguator{}
	var live, past []string

	for _, r := range recs {
		st := r.State()
		mine := f.mine(r)
		foreign := ""
		if st == Running || st == Orphan {
			if !mine {
				foreign = "⇄"
			}
		} else {
			if !mine {
				continue
			}
			if now-atoi(r.FinishedAt) > RecentSeconds {
				continue
			}
		}
		if st == Orphan && r.Elapsed(now) > OrphanLineSeconds() {
			continue
		}
		el := human.Secs(r.Elapsed(now))
		lbl := seen.next(r.Label)
		switch st {
		case Running:
			// A long round that is still writing gets no alarm — that is just
			// a big task. Silence is what earns the hourglass, and the idle
			// time rides along so the two are never confused.
			mark, extra := "▶", ""
			if idle, ok := r.Idle(now); ok && idle >= StallSeconds() {
				mark, extra = "⏳", " "+dimIdle+human.Secs(idle)
			}
			live = append(live, fmt.Sprintf("%s%s%s %s·%s %s%s", foreign, mark, lbl, r.Provider, HarnessShort(r.Harness), el, extra))
		case Orphan:
			live = append(live, fmt.Sprintf("%s⚠%s %s·%s %s", foreign, lbl, r.Provider, HarnessShort(r.Harness), el))
		case Done:
			past = append(past, fmt.Sprintf("✅%s %s", lbl, el))
		case Failed:
			past = append(past, fmt.Sprintf("❌%s rc=%s", lbl, r.RC))
		}
	}

	// Live work reads first and outcomes trail it: a round still burning
	// tokens is the thing to act on, a finished one is only news.
	parts := []string{}
	if len(live) > 0 {
		parts = append(parts, strings.Join(live, "  "))
	}
	if len(past) > 0 {
		parts = append(parts, strings.Join(past, "  "))
	}
	if len(parts) == 0 {
		return 0
	}
	out := strings.Join(parts, "  ")
	// The count is a headline for work in flight. With nothing in flight it
	// would read "🛠0" next to a green tick — a zero where the eye expects an
	// alarm — so it is simply absent then.
	if len(live) > 0 {
		fmt.Fprintf(stdout, "🛠%d %s\n", len(live), out)
	} else {
		fmt.Fprintf(stdout, "%s\n", out)
	}
	return 0
}
