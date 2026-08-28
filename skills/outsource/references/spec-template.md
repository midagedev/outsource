# Task: <one line>

## Files to read before starting (all of them — confirm in the report)
1. <every CLAUDE.md covering the edit-target directories, by absolute path —
   nested apps/*/libs/* CLAUDE.md files are NOT auto-injected by any
   claude-shaped CLI (grok included). On the claude-code harness the ROOT
   CLAUDE.md and .claude/rules/* ARE already injected — skip those there;
   on crush/opencode/agy assume nothing is injected>
2. <project contract doc, e.g. root CLAUDE.md sections that govern this task>
3. <the module(s) being touched or imitated — mark read-only ones>
4. <known-trap files worth quoting, e.g. a module whose header documents a footgun>

## Background (self-contained — this file alone must be enough to work)
<What the project is, what exists already, what this task adds and why.
Target files with exact paths. Current behavior / desired behavior.>

## Contract (violations are failures)
- <numeric contracts: ranges, palettes as hex, timings in ms/frames, size budgets>
- <structural contracts: config injection, determinism/seeding, allocation-free ticks, draw-call budgets>
- <bans: forbidden libraries/techniques, files that must not change>

## Depth requirements (part of the completion criteria)
- Gate file opens with a contract↔assertion mapping table; ≥2 assertions per
  clause (happy path + violation/boundary); coverage table (cases × paths);
  each assertion carries one line of FAIL-first evidence.
- Defend against defects you discover within your own output's scope;
  report anything out of scope with coordinates.
- Self-review pass: list 3 defect classes you may have missed; add an
  assertion for each or justify why not.
- Shared-lib edits: **consumer census table** (every importer × what it
  loses; "none" needs grep evidence). Moved/replaced functions:
  **options/guards kept-vs-dropped table** across all old call sites.
  Green tests alone are not completion — these tables are.

## Design principles (for logic/state-machine tasks — report where each applied)
1. derive-don't-store
2. re-normalize external input on load (never validate-then-discard)
3. 3-class input defense table: malicious / corrupted / stale-schema
4. adversarial API self-review: "3 ways to misuse my API", each blocked or gated

## Visual self-verification protocol (for rendered output — mandatory)
Open every capture yourself and compare against this checklist; log each
find→fix (fixes may only converge toward the numeric contract — inventing
new looks is banned); end with per-axis self-verdicts (SHIP/FIX predictions):
① **identity legibility — does it read as X? what could it be misread as?**
② <contract axis 2, e.g. palette discipline>
③ <contract axis 3, e.g. hard edges / no soft gradients>
④ <contract axis 4, e.g. bloom not swallowing the effect>

## Constraints unique to this task
- <file boundary: exact writable-path whitelist + "everything else read-only".
  Enumerate files one by one — never "the whole folder"; the list is the law>
- <parallel-track note if applicable>
- <for move/rename tasks: bidirectional reference check + "newly broken
  links: 0" as a numeric completion criterion>

## Verification commands (completion criteria — paste real output; never pipe away exit codes)
- [ ] <gate command> exit 0
- [ ] <build command> exit 0
- [ ] <captures/artifacts with exact output paths; zero console errors>
- [ ] <regression gates for neighboring systems>

Research/audit rounds with no gates: write "no gates for this round" —
never fabricate gate output to fill the section. And give every tracker
issue the spec excludes or references a one-line summary inline; the
delegate may not be able to query the tracker.

## Environment
- <worktree path if isolated; ports; where to copy final artifacts before reporting>
- No git state changes (lead commits). Writable: outputs + scratch only.

## Last line
Required when launching with `--done-marker` (the launcher refuses if this
exact string is missing). It is the contract the delegate prints.
DONE-<track>
