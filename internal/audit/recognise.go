package audit

import "strings"

// The denial recognisers: what turns an error tool result into a
// permission_decision event, and who is blamed for it.
//
// One table, one row per refusal text, each row carrying where its text came
// from. A row added without its source comment is a recogniser nobody can
// audit — which is exactly the failure mode this tool exists to review.
//
// Order is part of the contract: the specific rows run before the generic
// ones. The git guard's block arrives WRAPPED in Claude Code's hook-error
// line ("PreToolUse:Bash hook error: [<path>/outsource guard]: BLOCKED: …",
// measured 2026-10-06 in real round transcripts), so the
// git-guard rows must win over the hook row that carries them.
type recogniser struct {
	// contains matches the result text by substring.
	contains string
	// contains2, when set, must ALSO be present (used by the hook row so a
	// transcript that merely discusses "PreToolUse:" is not a hook denial).
	contains2 string
	by        string
	source    string
}

var recognisers = []recogniser{
	{
		contains: "BLOCKED: git 상태 변경은",
		by:       "git-guard",
		source:   "internal/guard/guard.go msgGit (guard.go:83); lands wrapped in a PreToolUse hook-error line (measured 2026-10-06 in real round transcripts)",
	},
	{
		contains: "BLOCKED: PR/릴리스/",
		by:       "git-guard",
		source:   "internal/guard/guard.go msgGH (guard.go:84)",
	},
	{
		contains: "Subagent spawn denied by a plugin",
		by:       "mod",
		source:   "cfg-spawn-deny transcript, 2026-10-06: an Agent call refused by this machine's probe mod",
	},
	{
		contains: "denied by a plugin",
		by:       "mod",
		source:   "Claude Code's plugin-deny phrasing; superset of the row above, reached only when that one did not match",
	},
	{
		contains: "probe-mod: denied",
		by:       "mod",
		source:   "cfg-basic transcript (toolDenialKind=permission-rule), 2026-10-06: the probe mod's Bash refusal",
	},
	{
		contains:  "PreToolUse:",
		contains2: "hook error:",
		by:        "hook",
		source:    "Claude Code's hook-block line, measured carrying the guard's block (2026-10-06); both substrings required so text about hooks is not itself a denial",
	},
}

// recogniseDenial returns the blamed party for a refusal text, or "" when the
// text is not a known denial (the caller then has an error event, not a
// permission_decision). It returns the text it matched on — the input with
// wrapping <tool_use_error> tags stripped, so the event quotes the refusal
// itself.
func recogniseDenial(text string) (by, matched string) {
	s := text
	if rest, ok := strings.CutPrefix(s, "<tool_use_error>"); ok {
		s = rest
	}
	s = strings.TrimSuffix(s, "</tool_use_error>")
	for _, r := range recognisers {
		if strings.Contains(s, r.contains) && (r.contains2 == "" || strings.Contains(s, r.contains2)) {
			return r.by, s
		}
	}
	return "", ""
}
