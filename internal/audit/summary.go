package audit

import (
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/midagedev/outsource/internal/runs"
)

// writeToolNames per harness. claude-code's set is fixed by the harness; agy's
// is intersected with the log's own init.tools at audit time (agyWriteTools)
// so the section can never claim a tool the round did not have.
var claudeWriteTools = []string{"Write", "Edit", "MultiEdit", "NotebookEdit"}

// agyWriteCandidates are the write-shaped tool names an agy round may expose;
// only those present in init.tools count.
var agyWriteCandidates = []string{
	"write_to_file", "replace_file_content", "multi_replace_file_content",
	"notebook_edit", "sed_file", "create_file", "edit_file", "write_file",
	"apply_patch",
}

// agyPathKeys are the parameter names a write tool may carry its target under;
// write_to_file uses TargetFile (measured 2026-10-06, maka round.log).
var agyPathKeys = []string{"TargetFile", "AbsolutePath", "Path", "target_file", "file_path", "path"}

func agyWriteTools(initTools []string) []string {
	have := map[string]bool{}
	for _, t := range initTools {
		have[t] = true
	}
	var out []string
	for _, c := range agyWriteCandidates {
		if have[c] {
			out = append(out, c)
		}
	}
	return out
}

// fileToolPath pulls the written path out of a tool input.
func fileToolPath(name string, in map[string]any, writeTools []string) (string, bool) {
	for _, w := range writeTools {
		if w != name {
			continue
		}
		for _, k := range append([]string{"file_path", "notebook_path"}, agyPathKeys...) {
			if s, ok := in[k].(string); ok && s != "" {
				return s, true
			}
		}
	}
	return "", false
}

// shellCommand extracts a shell command from a tool input, per harness.
func shellCommand(name string, in map[string]any, cmdTool string) (string, bool) {
	if name != cmdTool || in == nil {
		return "", false
	}
	for _, k := range []string{"command", "CommandLine"} {
		if s, ok := in[k].(string); ok {
			return s, true
		}
	}
	return "", false
}

// printSummary renders the default review output. Sections in the order the
// tool's contract fixes; empty sections still print their heading, because a
// reader must be able to tell "no messages" from "messages not checked".
func printSummary(t *Trail, rec *runs.Record, seal Seal, base string, w io.Writer) {
	// 1. The header — and when the trail's seal is broken, that fact comes
	// first, before anything the trail itself claims.
	mid := fmt.Sprintf(" · %s·%s", rec.Provider, rec.Harness)
	if rec.Model != "" || rec.ModelActual != "" {
		mid += fmt.Sprintf(" · model %s → %s", or(rec.Model, "?"), or(rec.ModelActual, "?"))
	}
	if rec.RC != "" {
		mid += " · rc=" + rec.RC
	}
	if rec.StartedAt != "" {
		mid += fmt.Sprintf(" · %s", time.Duration(rec.Elapsed(time.Now().Unix()))*time.Second)
	}
	var head string
	if seal.Verdict == "mismatch" {
		head = fmt.Sprintf("── audit %s · SEAL MISMATCH — %s%s", rec.Label, seal.Detail, mid)
	} else {
		head = fmt.Sprintf("── audit %s%s · seal %s", rec.Label, mid, seal.Verdict)
		if seal.Detail != "" {
			head += " (" + seal.Detail + ")"
		}
	}
	fmt.Fprintln(w, head)

	modelsSection(t, rec, w)
	toolsSection(t, w)
	commandsSection(t, rec, w)
	writeTools := claudeWriteTools
	if rec.Harness == "agy" {
		writeTools = agyWriteTools(t.InitTools)
	}
	writesSection(t, rec, writeTools, w)
	treeSection(rec, base, writesOf(t, writeTools), w)
	messagesSection(t, w)
	subagentsSection(t, w)
}

func or(s, def string) string {
	if s != "" {
		return s
	}
	return def
}

// modelsSection: request counts per model id, drift marked. Subagent requests
// are counted separately — a subagent on another model is a different fact
// from the main loop drifting, and the two must not average into one row.
func modelsSection(t *Trail, rec *runs.Record, w io.Writer) {
	fmt.Fprintln(w, "Models:")
	requested := rec.Model
	if t.Sentinel != nil && t.Sentinel["model_requested"] != "" {
		requested = t.Sentinel["model_requested"]
	}
	counts := map[string]int{}
	var order []string
	for _, e := range t.Events {
		if e.Kind == KindModelRequest && e.Agent == "" && e.Model != "" {
			if counts[e.Model] == 0 {
				order = append(order, e.Model)
			}
			counts[e.Model]++
		}
	}
	if len(order) == 0 {
		fmt.Fprintln(w, "  (no model requests seen)")
	}
	for _, m := range order {
		line := fmt.Sprintf("  %-28s %d request%s", m, counts[m], plural(counts[m]))
		if m != requested {
			line += fmt.Sprintf("   MODEL DRIFT (requested %s)", or(requested, "nothing"))
		}
		fmt.Fprintln(w, line)
	}
	if len(t.Subagents) > 0 {
		fmt.Fprintln(w, "  subagents:")
		for _, s := range t.Subagents {
			c := map[string]int{}
			var ids []string
			for _, m := range s.Models {
				if c[m] == 0 {
					ids = append(ids, m)
				}
				c[m]++
			}
			for _, m := range ids {
				// An unset spawn model inherits the session's; drift is only
				// nameable when something was actually requested.
				want := s.SpawnModel
				if want == "" {
					want = requested
				}
				line := fmt.Sprintf("    %-26s %-12s %d request%s", s.ID, m, c[m], plural(c[m]))
				if want != "" && m != want {
					line += fmt.Sprintf("   MODEL DRIFT (spawn asked %s)", want)
				}
				fmt.Fprintln(w, line)
			}
		}
	}
}

// toolsSection: call count per tool name, with error and denial counts —
// the three numbers a reviewer asks about a round's tool use first.
func toolsSection(t *Trail, w io.Writer) {
	fmt.Fprintln(w, "Tools:")
	nameOf := map[string]string{}
	calls, errs, denials := map[string]int{}, map[string]int{}, map[string]int{}
	var order []string
	seen := map[string]bool{}
	for _, e := range t.Events {
		switch e.Kind {
		case KindFunctionCall:
			nameOf[e.CallID] = e.Name
			if !seen[e.Name] {
				seen[e.Name] = true
				order = append(order, e.Name)
			}
			calls[e.Name]++
		case KindFunctionResponse:
			if e.IsError != nil && *e.IsError {
				errs[nameOf[e.CallID]]++
			}
		case KindPermissionDecision:
			denials[nameOf[e.CallID]]++
		}
	}
	if len(order) == 0 {
		fmt.Fprintln(w, "  (no tool calls)")
	}
	sort.Slice(order, func(i, j int) bool { return calls[order[i]] > calls[order[j]] })
	for _, n := range order {
		line := fmt.Sprintf("  %-24s %d call%s", n, calls[n], plural(calls[n]))
		if errs[n] > 0 {
			line += fmt.Sprintf(", %d error%s", errs[n], plural(errs[n]))
		}
		if denials[n] > 0 {
			line += fmt.Sprintf(", %d denial%s", denials[n], plural(denials[n]))
		}
		fmt.Fprintln(w, line)
	}
}

// commandsSection: every shell command, numbered, first line clipped to 200
// display columns, with the observation flags each command earned.
func commandsSection(t *Trail, rec *runs.Record, w io.Writer) {
	cmdTool := "Bash"
	if rec.Harness == "agy" {
		// The shell tool's name comes from the log's own init.tools, not from
		// an assumption — a backend that renames it must not silently produce
		// an empty command list.
		cmdTool = ""
		for _, it := range t.InitTools {
			if it == "run_command" {
				cmdTool = "run_command"
			}
		}
	}
	fmt.Fprintln(w, "Commands:")
	n := 0
	for _, e := range t.Events {
		if e.Kind != KindFunctionCall || cmdTool == "" {
			continue
		}
		cmd, ok := shellCommand(e.Name, t.Inputs[e.CallID], cmdTool)
		if !ok {
			continue
		}
		n++
		first := headline(cmd)
		agent := ""
		if e.Agent != "" {
			agent = "[" + e.Agent + "] "
		}
		line := fmt.Sprintf("  %3d  %s%s", n, agent, clipRunes(strings.Join(strings.Fields(first), " "), 200))
		if fl := flagsFor(cmd); len(fl) > 0 {
			line += "   [" + strings.Join(fl, ",") + "]"
		}
		fmt.Fprintln(w, line)
	}
	if n == 0 {
		fmt.Fprintln(w, "  (none)")
	}
}

// reAssignOnly is a line that only sets shell variables (`W=/a/b`, `A=1 B=2;`),
// with no command of its own.
var reAssignOnly = regexp.MustCompile(`^\s*([A-Za-z_][A-Za-z0-9_]*=\S*\s*)+;?\s*$`)

// headline is the line a reader recognises a command by: the first line that
// is not blank and not only variable assignments, marked "… " when lines were
// skipped. Delegates set a path variable on line one (W=/long/path) and run the
// command on line two, so the bare first line named nothing (added 2026-10-06
// by the lead after the first live audits listed 40 rows of `W=…`).
func headline(cmd string) string {
	lines := strings.Split(cmd, "\n")
	for i, l := range lines {
		if strings.TrimSpace(l) == "" || reAssignOnly.MatchString(l) {
			continue
		}
		if i > 0 {
			return "… " + l
		}
		return l
	}
	return lines[0]
}

// writesOf walks the events for every path the file tools wrote, in call
// order — the one list the write-focused sections share.
func writesOf(t *Trail, writeTools []string) []string {
	var out []string
	for _, e := range t.Events {
		if e.Kind != KindFunctionCall {
			continue
		}
		if p, ok := fileToolPath(e.Name, t.Inputs[e.CallID], writeTools); ok {
			out = append(out, p)
		}
	}
	return out
}

// writesSection: files the file tools wrote, in first-write order, with the
// paths outside the round's cwd marked — a write outside the tree the round
// was given is the first thing a reviewer wants to see, not a footnote.
func writesSection(t *Trail, rec *runs.Record, writeTools []string, w io.Writer) {
	fmt.Fprintln(w, "Files written by file tools:")
	type row struct {
		tools   map[string]int
		outside bool
	}
	rows := map[string]*row{}
	var order []string
	for _, e := range t.Events {
		if e.Kind != KindFunctionCall {
			continue
		}
		p, ok := fileToolPath(e.Name, t.Inputs[e.CallID], writeTools)
		if !ok {
			continue
		}
		r := rows[p]
		if r == nil {
			r = &row{tools: map[string]int{}}
			rows[p] = r
			order = append(order, p)
		}
		r.tools[e.Name]++
		if rec.Cwd != "" && relTo(p, rec.Cwd) == "" {
			r.outside = true
		}
	}
	if len(order) == 0 {
		fmt.Fprintln(w, "  (none)")
	}
	for _, p := range order {
		var parts []string
		for _, name := range writeTools {
			if rows[p].tools[name] > 0 {
				parts = append(parts, fmt.Sprintf("%s ×%d", name, rows[p].tools[name]))
			}
		}
		mark := ""
		if rows[p].outside {
			mark = "  OUTSIDE-CWD"
		}
		fmt.Fprintf(w, "  %s  (%s)%s\n", p, strings.Join(parts, ", "), mark)
	}
}

// treeSection cross-checks the file-tool writes against what git says changed.
// Only read-only git runs: rev-parse, diff, status, ls-files. A cwd that is
// not a work tree (a scratch dir, a round that never touched git) skips the
// section with a note — an absent cross-check must not read as an empty one.
func treeSection(rec *runs.Record, base string, writes []string, w io.Writer) {
	tc := crossCheck(rec.Cwd, base, writes)
	fmt.Fprintln(w, "Tree cross-check:")
	if tc == nil {
		fmt.Fprintf(w, "  (skipped: %s is not a git work tree)\n", or(rec.Cwd, "no cwd on record"))
		return
	}
	against := "git status"
	if base != "" {
		against = "diff against " + base
	}
	fmt.Fprintf(w, "  cwd=%s · %s\n", rec.Cwd, against)
	printList(w, "  written-and-changed:", tc.WrittenChanged)
	printList(w, "  changed-without-file-tool (shell writes, generated files, or someone else's edits):", tc.ChangedNotWritten)
	printList(w, "  written-but-unchanged:", tc.WrittenUnchanged)
}

func printList(w io.Writer, head string, items []string) {
	fmt.Fprintln(w, head)
	if len(items) == 0 {
		fmt.Fprintln(w, "    (none)")
		return
	}
	for _, it := range items {
		fmt.Fprintf(w, "    %s\n", it)
	}
}

type treeCheckResult struct {
	WrittenChanged    []string
	ChangedNotWritten []string
	WrittenUnchanged  []string
}

// crossCheck runs the three-way comparison. It is factored out of the printer
// so the test can drive it against a real temp repo without rendering.
func crossCheck(cwd, base string, writePaths []string) *treeCheckResult {
	if cwd == "" {
		return nil
	}
	if out, err := exec.Command("git", "-C", cwd, "rev-parse", "--is-inside-work-tree").Output(); err != nil || strings.TrimSpace(string(out)) != "true" {
		return nil
	}
	var changed []string
	seen := map[string]bool{}
	add := func(p string) {
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		changed = append(changed, p)
	}
	if base != "" {
		if out, err := exec.Command("git", "-C", cwd, "diff", "--name-only", "-z", base).Output(); err == nil {
			for _, p := range strings.Split(string(out), "\x00") {
				add(p)
			}
		}
		if out, err := exec.Command("git", "-C", cwd, "ls-files", "--others", "--exclude-standard", "-z").Output(); err == nil {
			for _, p := range strings.Split(string(out), "\x00") {
				add(p)
			}
		}
	} else {
		out, err := exec.Command("git", "-C", cwd, "status", "--porcelain", "-z").Output()
		if err != nil {
			return nil
		}
		recs := strings.Split(string(out), "\x00")
		for i := 0; i < len(recs); i++ {
			r := recs[i]
			if len(r) < 4 {
				continue
			}
			add(r[3:])
			// A rename entry is followed by the original path — it changed too.
			if strings.Contains(r[:2], "R") && i+1 < len(recs) {
				add(recs[i+1])
				i++
			}
		}
	}
	changedSet := map[string]bool{}
	for _, p := range changed {
		changedSet[p] = true
	}
	writtenSet := map[string]bool{}
	var writtenRel []string
	for _, p := range writePaths {
		rel := relTo(p, cwd)
		if rel == "" || writtenSet[rel] {
			continue // outside cwd, or already counted
		}
		writtenSet[rel] = true
		writtenRel = append(writtenRel, rel)
	}
	sort.Strings(writtenRel)
	var wc, cnw, wu []string
	for _, p := range writtenRel {
		if changedSet[p] {
			wc = append(wc, p)
		} else {
			wu = append(wu, p)
		}
	}
	sortedChanged := append([]string{}, changed...)
	sort.Strings(sortedChanged)
	for _, p := range sortedChanged {
		if !writtenSet[p] {
			cnw = append(cnw, p)
		}
	}
	return &treeCheckResult{WrittenChanged: wc, ChangedNotWritten: cnw, WrittenUnchanged: wu}
}

// relTo makes a write path relative to cwd when it is inside it, else "". A
// relative path in a tool call was relative to the ROUND's cwd, not to this
// process's, so that is what resolves it.
func relTo(p, cwd string) string {
	if !filepath.IsAbs(p) {
		p = filepath.Join(cwd, p)
	}
	rel, err := filepath.Rel(cwd, p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ""
	}
	return rel
}

// messagesSection: what reached the round from outside mid-flight.
func messagesSection(t *Trail, w io.Writer) {
	fmt.Fprintln(w, "Inbound messages:")
	n := 0
	for _, e := range t.Events {
		if e.Kind != KindMessageReceived {
			continue
		}
		n++
		who := e.From
		if e.FromName != "" {
			who = e.FromName + " (" + e.From + ")"
		}
		fmt.Fprintf(w, "  %s  %s  %s\n", or(clockDate(e.TS), "?"), or(who, "?"), clipRunes(strings.Join(strings.Fields(e.Head), " "), 160))
	}
	if n == 0 {
		fmt.Fprintln(w, "  (none)")
	}
}

// subagentsSection: what was spawned, what it asked for, what answered.
func subagentsSection(t *Trail, w io.Writer) {
	fmt.Fprintln(w, "Subagents:")
	if len(t.Subagents) == 0 {
		fmt.Fprintln(w, "  (none)")
		return
	}
	for _, s := range t.Subagents {
		models := strings.Join(s.Models, ", ")
		if models == "" {
			models = "?"
		}
		line := fmt.Sprintf("  %s  %s · %s", s.ID, or(s.Type, "?"), models)
		if s.SpawnModel != "" {
			line += " · spawn asked " + s.SpawnModel
		}
		fmt.Fprintln(w, line)
	}
}

// ---- the machine-readable outputs -------------------------------------------

func printJSON(t *Trail, w io.Writer) {
	enc := json.NewEncoder(w)
	for _, e := range t.Events {
		if err := enc.Encode(e); err != nil {
			return // stdout to a pipe that closed is not this tool's error
		}
	}
}

// printEvents renders one readable line per event, cut to the terminal width.
func printEvents(t *Trail, w io.Writer, width int) {
	for _, e := range t.Events {
		fmt.Fprintln(w, clipRunes(eventLine(e), width))
	}
}

func eventLine(e Event) string {
	ts := clock(e.TS)
	who := ""
	if e.Agent != "" {
		who = "[" + e.Agent + "]"
	}
	var body string
	switch e.Kind {
	case KindModelRequest:
		body = fmt.Sprintf("%s in=%d out=%d", e.Model, e.InputTokens, e.OutputTokens)
	case KindFunctionCall:
		body = fmt.Sprintf("%s %s", e.Name, firstArg(e.Input))
	case KindFunctionResponse:
		state := "ok"
		if e.IsError != nil && *e.IsError {
			state = "ERROR"
		}
		body = state
		if e.State != "" {
			body = e.State
		}
		if e.Bytes > 0 {
			body += fmt.Sprintf(" %d bytes", e.Bytes)
		}
		if e.Head != "" {
			body += " " + clipRunes(strings.Join(strings.Fields(e.Head), " "), 80)
		}
	case KindPermissionDecision:
		body = fmt.Sprintf("%s by=%s %s", e.Decision, e.By, clipRunes(strings.Join(strings.Fields(e.Text), " "), 100))
	case KindMessageReceived:
		body = fmt.Sprintf("from=%s %s", or(e.FromName, e.From), clipRunes(strings.Join(strings.Fields(e.Head), " "), 100))
	case KindSubagentSpawn:
		body = fmt.Sprintf("%s model=%s", e.SubagentType, e.Model)
	case KindError:
		body = clipRunes(strings.Join(strings.Fields(e.Head), " "), 100)
	case KindTermination:
		body = fmt.Sprintf("rc=%d tool_calls=%s done=%s seal=%s", e.RC, or(e.ToolCalls, "?"), or(e.DoneMarker, "?"), e.Seal)
	default:
		body = e.Name
	}
	return fmt.Sprintf("%4d %s %-20s %s %s", e.Seq, ts, e.Kind, who, body)
}

// firstArg shows the one field of a call worth a glance: the command, the
// path, the pattern — tail's toolArg order, kept so both views lead the eye
// to the same thing.
func firstArg(input string) string {
	var m map[string]any
	if json.Unmarshal([]byte(input), &m) != nil {
		return ""
	}
	for _, k := range []string{"command", "CommandLine", "file_path", "TargetFile", "AbsolutePath", "url", "pattern", "path", "prompt", "description", "query"} {
		if s, ok := m[k].(string); ok && strings.TrimSpace(s) != "" {
			return clipRunes(strings.Join(strings.Fields(s), " "), 100)
		}
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return "(" + strings.Join(keys, ",") + ")"
}

// clipRunes cuts to n display columns (runes, not bytes) with an ellipsis.
func clipRunes(s string, n int) string {
	ru := []rune(s)
	if len(ru) <= n {
		return s
	}
	return string(ru[:n]) + "…"
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
