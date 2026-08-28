# Spec authoring — quality bundle, per-task template, lead checks

## Quality bundle (put these sections in every spec)

These are the empirically validated additions that closed most of the
quality gap against stronger implementer models in a 9-experiment
blind-judged series (see the repo README):

1. **Contract↔assertion mapping table** — the gate/test file must open with
   a table mapping every contract clause to at least one assertion; each
   assertion needs FAIL-first evidence (one line showing it actually fails
   on a violating fixture). This alone quadrupled self-authored gate depth.
2. **Quantified depth** — do not write "be thorough". Write: "≥2 assertions
   per contract clause (one happy path, one violation/boundary)", "coverage
   table of cases × paths", and "**defend against discovered defects within
   your own output's scope** — report only what is out of scope".
3. **Self-review pass** — after finishing, "list 3 defect classes you may
   have missed; add an assertion for each or justify why not".
4. **Visual self-verification** (for anything rendered) — grok must open its
   own screenshots and compare them against the spec's checklist, log every
   find→fix, and end with a per-axis self-verdict (SHIP/FIX predictions,
   later compared against an independent blind verdict). **Checklist item #1
   must always be identity legibility**: "does this read as X? what could it
   be misread as?" — that one line caught failures numeric gates cannot.
   Inventing new looks stays banned; the only allowed fixes are convergence
   toward the numeric contract.
5. **Logic design principles** (for state machines / serialization / cores):
   derive-don't-store (derive state from phase and inputs; restoration bugs
   live in stored state) · re-normalize external input on load (don't
   validate-then-discard) · a 3-class input defense table
   (malicious / corrupted / stale-schema, each with a rejection path and an
   assertion) · adversarial API self-review ("3 ways to misuse my API",
   each blocked structurally or gated).

## Per-task spec (task.md — appended after the preamble)

The preamble owns shared constraints and the report format. The task spec
contains only what is unique to this task:

```markdown
# Task: <one line>

## Files to read before starting (all of them — confirm in the report)
- <every CLAUDE.md covering the edit-target directories, by absolute path —
  nested ones are NOT auto-injected (see the warning above). Root CLAUDE.md
  and .claude/rules/* are already injected on the claude-code harness only;
  on crush/opencode/agy assume nothing is>
- <the project's contract docs / the modules being touched / prior-art files>

## Background (self-contained — the spec alone must be enough)
- Target file: <exact path:line>
- Current behavior / desired behavior
- <Quote the project pitfalls that apply to THIS task into the body>

## Contract (violations are failures)
- <pin the contract as values: supported range, behavior when unsupported, boundaries>

## Constraints unique to this task
- <file boundary: exact writable-path whitelist, enumerated file by file —
  never "the whole folder"; everything else read-only>
- <if parallel tracks exist: their broken builds are not your fault — report only>

## Verification commands (completion criteria — paste real output, never hide exit codes behind pipes)
- [ ] <command and expected output>
- [ ] <a real end-to-end artifact — build it and open it>

## Last line
DONE-<track>
```

`--done-marker` at launch requires that exact string in the spec; the launcher refuses if it is missing.

### What the lead checks while writing the spec

- **Quote the applicable pitfalls yourself.** The preamble tells grok to go
  find the trap docs, but what the lead already knows, the lead should quote.
- **Never pair an explicit file list with a folder-level phrase** ("all 10
  below, so move the whole folder"). When list and folder disagree, grok
  takes the wider reading. (Incident: the prose said "all", the list named
  8, the folder held 15 — 7 unverified documents were moved, one still
  live.) Enumerate exactly; the preamble makes the list a whitelist, but the
  lead must not write the ambiguity in the first place.
- **Never draw the scope fence at an app boundary when the edit target is a
  shared lib.** "Rewrite `libs/<x>`, don't touch app Y" reads as "ignore app
  Y" — but Y consumes the lib, and its regressions ship silently (two PRs
  blocked this way: a scroll-triggered re-download regression and an
  eviction-contract hole, both in the fenced-out app, both green on tests).
  Write the fence as **"don't edit Y's files; census Y as a consumer and
  report what it loses"**, and list the consumers you already know of in
  the spec.
- **Never order an unconditional "delete the dead code".** The lead's belief
  that code is dead is a hypothesis, not a fact — phrase it as "delete only
  with repo-wide consumer grep attached as evidence; otherwise leave it and
  report". (A "dead" URL-TTL cache ordered deleted was live on another path.)
- **Green is a necessary completion criterion, never a sufficient one.** All
  8 blocking findings across 5 consecutive CHANGES_REQUESTED PRs happened
  with tests and typecheck fully green — the defect classes (out-of-scope
  consumer regressions, dropped guards, duplicated helpers, broken
  references) live outside what green measures. Demand the preamble's report
  tables (consumer × lost behavior; options/guards kept-vs-dropped) and the
  numeric link check for moves as completion criteria in their own right.
- **Never put 3+ independent jobs in one spec.** Defect rates rise with spec
  length; split boundaries into parallel tracks instead.
- **Put a real artifact in the completion criteria.** Unit tests alone cover
  only pure functions; force a snapshot/roundtrip/`--help` execution and the
  integration defects surface immediately.
- **Fake-server coverage ≠ the real system.** If credentials exist, the lead
  runs one real pass; if not, mark "unverified" and run it when they appear.
  Mix localized/non-ASCII values into fixtures on purpose.
- **When secrets are involved, demand a whitelist implementation** plus a
  test that fails on unclassified fields — blacklists leak future fields.
