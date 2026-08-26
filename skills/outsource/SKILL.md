---
name: outsource
description: >
  Outsource implementation, investigation/research, numeric harnesses and
  vision-verdict work to third-party model CLIs running as headless
  sub-agents — the grok CLI (grok-4.6), GLM-5.3 (z.ai coding plan, run
  through headless Claude Code or the crush CLI), and OpenRouter stealth
  ox-alpha (opencode CLI) — while the lead Claude session stays
  orchestration-only. Use when the user asks to run work via grok / glm /
  crush / opencode / ox-alpha, to save tokens, or invokes /outsource. Pick
  the backend by task: GLM-5.3 cannot read images, so vision verdicts go to
  grok, ox-alpha, or a Claude agent.
---

# outsource — third-party models as headless implementation sub-agents

Division of labor, whatever the backend:

| Role | Owner |
|------|-------|
| Orchestration, spec writing, diff review, gates, commits, deploys | Lead session (Claude) |
| Implementation, mechanical edits, numeric harnesses, investigation, report writing | Outsourced backend (below) |

Core principle: **the delegate is an executor of tight specs.** It has zero
conversation context, so the spec must be self-contained (file paths,
contracts, completion criteria) and must never ask for taste judgments —
only numeric contracts.

## Backends — GLM-5.3 by default, grok or ox-alpha when the task needs eyes

| Backend | Runs via | Use it for | Hard limits |
|---|---|---|---|
| **GLM-5.3** — the default | z.ai coding plan, via `bin/outsource-run.sh` on either harness — `claude -p` (default) or the `crush` CLI (`references/glm.md`) | **every spec-able round**: implementation, mechanical edits, gate authoring, code investigation, reports. Strong disclosure and premise-correction | **cannot see images at all**; style/look/UI-interaction authoring measured weaker — route those elsewhere |
| **grok-4.6** — the exception | `grok` CLI, headless (`references/grok.md`) | what GLM structurally cannot do: **vision verdicts** and image reading, image/video generation, and web research when GLM's harness lacks the tool | verdicts contradicting instrumentation escalate to a Claude agent |
| **ox-alpha** — OpenRouter stealth | opencode CLI, via `bin/outsource-run.sh --provider openrouter` (`references/opencode.md`) | spec-able rounds when z.ai/xAI headroom is gone, and **vision through the read tool** (measured: named a solid-red PNG, answered "Red") | stealth: model identity and limits can change without notice; free while listed as stealth (`step_finish.cost` was 0) |

Selection rules:

- **Default to GLM-5.3.** Reach for grok when the task needs eyes (pixels,
  framing, colour), pixels generated (image/video), or a web tool the GLM
  harness does not have. Reach for ox-alpha when you want a free-while-stealth
  OpenRouter round with vision, or another process family besides GLM's two
  harnesses. "It feels exploratory" is not a reason — narrow the
  cause first, then delegate (see *When NOT to outsource*).
- Anything that must **look at pixels** → grok, ox-alpha, or a Claude agent;
  never GLM-5.3. This is a capability fact, not a preference: GLM reports
  `supports_attachments: false`. ox-alpha sees pixels through opencode's
  `read` tool when the launcher passes `--auto` (without it, a path outside
  cwd is `external_directory` default-ask and is rejected headless).
- All three backends parallelize: disjoint file whitelists, one worktree and one
  config/session scope per track. Spreading tracks across providers —
  and, for GLM, across its two harnesses — multiplies headroom.
- **Model vs harness are separate choices.** The harness is only how a model
  is driven headlessly; the same spec, preamble and review checklist apply
  whichever one runs. GLM-5.3 ships with two (`--harness claude-code|crush`);
  pin the model explicitly, because z.ai maps an unqualified `claude-*`
  request onto its plan default (measured: glm-4.7). openrouter defaults to
  harness `opencode` and is refused on the other two (no Anthropic-compatible
  URL, no cred row).
- Site-local defaults (which backend is *your* default, model overrides)
  belong in the user overlay (`references/local-overlay.md`); repo-specific
  gates and coordinates in the project overlay (`<repo>/.outsource/overlay.md`).
  See *Local overlays* below.

## Spec assembly (every delegation)

```bash
cat <skill-dir>/references/spec-preamble.md \      # shared rules — every clause from a real incident
    <skill-dir>/references/glm-preamble.md \       # GLM runs only: the runtime delta
    $(<skill-dir>/bin/outsource overlays --root <repo>) \   # user + declared + in-repo overlays, in order
    <scratch>/task.md > <scratch>/spec.md
```

`outsource overlays` is the one place that decides which overlays apply, so
the answer does not depend on which of them the lead happened to remember.
It prints nothing when none exist, `--explain` adds the kind and the pattern
that matched, and a declaration that could never match is named on stderr
rather than silently absent. Listing the files by hand still works — the
resolver is a lookup, not a new format.

**Keep the overlay repo-agnostic, and include only the part that applies.**
An overlay section written for one project reaches a spec for a different
one as wrong premises — repo-specific gates, capture tools and domain
contracts belong in that repo's own `CLAUDE.md`/`AGENTS.md`, which the
delegate loads on its own. There is a second cost measured on the way in:
`spec-lint` resolves every path in the *assembled* file, so a path from
another project fails the lint on something the task never claimed, and a
linter that always exits 1 is a linter you stop reading. Either keep
project blocks out of the overlay, or trim them for the target repo before
assembling.

**Which preamble.** `spec-preamble-core.md` is a short substitute for
`spec-preamble.md`. Measured (GLM-5.3 on the claude-code harness, 5 issues,
same task spec, full preamble vs none): removing the preamble entirely cost
16-37% fewer output tokens and never lost a gate — but the self-verification
section went 5/5 to 0/5 and "what I could not do" went 5/5 to 1/5, so a round
whose contract could not be met came back looking met. FAIL-first survived at
5/5 either way, because the *task spec* demands it. The core file carries
back exactly the part that vanished. Use the full preamble when the round
touches shared code or a contract you are unsure is satisfiable; use core for
mechanical rounds where you want the delegate's context small.

Write the per-task spec from `references/spec-template.md`, and read
`references/spec-authoring.md` before writing it — the quality bundle and
the lead-side checks there are where delegated quality is actually won.

**Lint the assembled spec before launching it:**

```bash
<skill-dir>/bin/spec-lint.sh --root <repo> <scratch>/spec.md
```

It resolves every `path:line` citation and every path-shaped reference, and
exits 1 on one that does not exist or a line number past the end of its
file. Wrong premises are the measured tax on delegation — in one session
five of them (a nonexistent tool, a nonexistent column, an absent fixture,
a wrong runner cwd, a wrong manifest path) each cost part of a round. The
delegate catches them, but only after it has started.

Then invoke the backend exactly as its reference describes. **Do not launch
either wrapper foreground under a harness-tracked background task with no
TTY** — that shape lost 6 rounds to an external 30-minute killer
(2026-08-22, wall-clock :08:26/:38:26). Both launchers refuse it at exit 64
and name `--detach` (recommended) or `--foreground` (tests / a deliberate
block). `outsource-run.sh --detach` matches `grok-run.sh --detach`. A
detached launch is only half the move — arm `bin/wait.sh` over the logs you
just started, or the round's completion reaches nobody (see *Knowing what is
in flight*).

- grok: `references/grok.md` — flag combo, git-safety profiles, sentinel
  completion proof, vision-verdict recipe, image generation, mid-round
  visibility and intervention.
- GLM-5.3: `references/glm.md` — `bin/outsource-run.sh` launcher (provider
  table, harness picker, isolated config per track, `SESSION <id>` resume,
  `--require-quota` pre-flight gate, model-identity assertion, `<log>.rc`
  sentinel), `bin/git-guard.sh` PreToolUse hook (works on both harnesses),
  z.ai model-mapping trap, measured behavior profile.
- ox-alpha: `references/opencode.md` — `bin/outsource-run.sh --provider
  openrouter` (harness `opencode` is the default for that provider), isolated
  `OPENCODE_CONFIG_DIR`, git-write permission deny, `SESSION <id>` resume via
  `-s`, model-identity via `opencode export`.

## Knowing what is in flight

A delegated round is invisible between "I launched it" and "it reported",
and that gap is where a lead loses track of which tracks are still alive,
how long they have been running, and which one died without a report:

```bash
<skill-dir>/bin/runs.sh          # every round: state, provider, harness, elapsed
<skill-dir>/bin/runs.sh line     # the same, compressed to one line
```

**Arm a waiter at launch; do not rely on remembering to poll.** `--detach`
returns immediately and nothing afterwards wakes the orchestrator, so with
only the commands above, noticing that a round finished depends on the lead
choosing to look — which is a coin flip, and lands wrong exactly when the
lead is busy with the next thing. Measured 2026-08-26: two rounds sat
finished for nine and eleven minutes, and what surfaced them was the user
asking, not the orchestrator.

```bash
<skill-dir>/bin/wait.sh <log.ndjson>...     # blocks until every sentinel exists, prints each
```

In an agent harness a **blocked wait is the notification**: run it as a
background command and the harness re-invokes you when it exits, which turns
"remember to check" into "get told". Launch, then arm it in the same breath —
one waiter over every log you just started, so one wake-up covers the whole
fan-out:

```bash
<skill-dir>/bin/grok-run.sh --detach --label a … --log <A>
<skill-dir>/bin/grok-run.sh --detach --label b … --log <B>
<skill-dir>/bin/wait.sh <A> <B>             # background this; it returns when both are done
```

`--timeout N` bounds it (exit 124, remaining rounds named) for a lane you are
willing to stop waiting on. The waiter only saves you the polling loop — it
does not interpret the sentinel, so **completion evidence is still the `.rc`
content**, and a returned waiter is the cue to read the report, not proof the
round succeeded.

`running` and `done` are the states you expect; `orphan` — started, pid
gone, no exit code — is the one worth acting on, because nothing else on
the machine still remembers that round existed. `ps` cannot report it: a
killed round leaves no process at all.

**A long round is not a stuck round — never cut one to find out.** Measured
across ten delivered rounds: 13 minutes to 1h50m, with duration tracking
message count almost linearly. Long rounds were long because there was a
lot of work, so a time limit truncates a working delegate mid-edit and a
90-minute round is not, by itself, evidence of anything.

What separates the two is output, not duration. The GLM harnesses write
continuously into their own data directory; opencode flushes JSONL into
`--log` per event, so `runs.sh` reports an `IDLE` column and flags `⏳`
only when a *running* round has written nothing for ten minutes
(`OUTSOURCE_RUN_STALL`):

```
▶refshot zai·crush 1h41m        # 101 minutes in, still writing — leave it
⏳frozen  zai·crush 22m ⋯14m     # silent for 14 of those 22 — go look
```

A stall is a reason to read the log, not to kill anything; a round often
recovers. `--max-seconds N` on the launcher does hard-kill at N (exit 124)
and exists only for rounds whose loss you accept in advance — it is not the
answer to "this is taking a while".

**Label every launch with what the track is for.** `--label api-migration`,
not `--label track-a` and not nothing: the listing is only useful in
parallel, and in parallel the derived default collides (this skill writes
every track's spec to `<scratch>/spec.md`). Colliding labels render as
`name`, `name#2` — a warning that the round you are looking at cannot be
identified, not a naming scheme.

`<skill-dir>/bin/statusline.sh` puts that line, and the plan quotas from
`bin/quota.sh`, into Claude Code's status line. See the README.

**Retrieving the report.** When the sentinel says the round finished, do not
hand-write a JSON extractor (measured 2026-08-17: four rounds, four
throwaway Python scripts, two log shapes):

```bash
<skill-dir>/bin/last-report.sh <log>              # the delegate's final report
<skill-dir>/bin/last-report.sh <log> --max-chars 4000
```

It understands three shapes — a claude-code `run.log` (last `result` event),
a grok CLI ndjson (text deltas after the last tool event), and opencode
`--format json` (concatenation of `part.text` after the last `tool_use`) —
and exits 65 when the log holds no report at all, which is what a
died-mid-run round looks like. On that path it now also names what the
sentinel already knows (`rc`, `wrapper_signal`, finished) or that the
round is still running. It prints the delegate's words; **completion
evidence is still the `.rc` sentinel**, never the report's existence.

## What the lead always does (backend-independent)

1. **Delegate "done" ≠ done.** Read `git diff` yourself and re-run the
   affected gates under your own ownership. Verify by the tree and the
   spec's completion marker (`DONE-<track>`), never by harness lifecycle
   notifications. Pass `--done-marker <string>` at launch only when the
   spec contains that exact string as its last line — the launcher refuses
   with exit 64 before starting the round if it does not, because the
   delegate is never told to print a marker the spec does not ask for.
   The sentinel records `done_marker=found|absent`. A clean harness exit
   without the marker is exit 72 on both launchers — 70 stays the
   model-identity assertion. Two rounds on one day both exited clean: one
   had written nothing at all, the other had correctly stopped because the
   spec's own precondition check said to. 72 names the missing marker; the
   tree still tells those two apart.
2. **Re-run the suite it called green from cold** and compare the test
   count with CI's — a differing count is a failed verification. **Never
   pipe a gate through `tail`/`head`**: the pipeline's exit status becomes
   the pager's, and a hard failure reads as green (measured: a `vitest run`
   that exited 1 with "No test files found" looked clean through `| tail`).
   Capture the full output to a file and grep that.
3. **Ask where the cause was closed.** A commit message or report paragraph
   is not a recurrence layer; demand the gate/test/config `file:line`.
4. **Look for artifact/code divergence** the change introduced, and check
   the tracker: closed tickets carry commit refs, discovered defects got
   filed as their own items.
5. **Check your own spec for clauses that cannot both hold** — before you
   launch, not after. Measured: a spec of ours asked for a rule that would
   overwrite user edits *and* for user edits to stay protected. Three of four
   delegates implemented it as written and shipped a data-loss bug with every
   gate green; only the Claude arm refused and designed around it. The
   delegate-side rule for this already exists in the preamble and did not
   fire, so this one is yours: list the contract clauses, and ask whether any
   pair is jointly unsatisfiable.
6. **Verify negative premises one path at a time.** "This file does not
   exist" is the class of claim a spec linter cannot check. Measured: a
   two-pattern `ls` in zsh printed nothing because the *second* glob matched
   nothing and aborted the command, so a file that existed was written into a
   spec as absent. Same family as the `tail` trap below: a shell behavior
   that turns a failed check into a confident answer.
7. Anything visual gets **one blind vision verdict before commit** (fresh
   judge per round; numeric context first, narrowed question, "do not
   judge" list). Never mix look-core changes with mechanical work in one
   spec or commit.
8. Commits, pushes, merges, deploys: **lead-only**, on every backend.

### Review checklist (where delegated defects actually leak)

Reports are largely honest — the problem is what they do *not* say. Review
the `git diff`, not the report:

1. grep for newly invented mapping/constant tables and duplicated helpers;
   diff near-copies against the **latest** sibling for dropped guards.
2. Compare against equivalent implementations on other surfaces
   (web/TUI/CLI parity).
3. Execute user-facing text yourself (`--help`, error strings) against the
   code — invented copy passes tests.
4. For refactors ask "what was lost" — ordering, caches, fallbacks,
   shortcuts.
5. Read changed test assertions in the diff — a bumped constant means a
   rewritten contract; demand the original.
6. Check new imports for inverted dependency directions.
7. Re-run secret scanners *after* committing new files (`git ls-files`
   scanners skip untracked files).
8. For conditional features, verify the disabled path is unchanged.
9. Read test wait conditions — a `waitFor` on anything but the asserted
   state is a proxy wait; PASS means "it ran", not "it's right".
10. After moves/renames, grep reference integrity in both directions
    yourself; a `.claude/**`/skill link repointed into an archive is a red
    flag, not a fix.
11. Grep for **dangerous defaults** — an empty/omitted field that means
    "all" (a `{"id": ""}` once destroyed every tab; the tell was the
    delegate's own mock getting the case wrong first).

Fix small precision defects yourself on the spot; re-delegate only repeated
patterns or large volumes.

## When NOT to outsource

- Problems too exploratory to spec — the lead narrows the cause first.
- git / deploy / release actions (lead only).
- Design-weight logic cores (state machines, serialization): measured to
  stay with Claude — write with Claude, have a backend review.
- Vision work on GLM-5.3, ever.

## Local overlays (user-level and project-level)

Two layers, most specific last so it wins on conflict:

1. **User overlay** — `references/local-overlay.md` next to this skill.
   Holds only what is true for this user on every repo: default backend,
   model/effort flags, provider headroom notes. The installer preserves it
   on upgrade; this repository never ships one.
2. **Project overlay** — what is true only in one repo: base branch and repo
   coordinates, house gate recipes, incident history, files-to-read lists. A
   repo-specific fact in the user overlay is a bug — move it here. There are
   two ways to attach one, for two shapes of working copy:

   - **In-repo** — `<repo>/.outsource/overlay.md`, committed next to the code
     it describes. The default: it versions with the branch, arrives with a
     fresh clone, and teammates get it for free.
   - **Declared** — `<skill-dir>/references/overlays/<name>.md` whose front
     matter lists the paths it applies to, the way `.claude/rules/*` declare
     theirs. The file lives once in user scope and names the working copies
     it covers.

   ```markdown
   ---
   paths:
     - ~/repo/ds*          # every clone
     - ~/repo/uf*/**       # and the worktrees under them
   ---
   # Project overlay — <repo>
   ```

   Reach for **declared** when one repo has **several checkouts on one
   machine** — clones plus worktrees, each parked on a different branch.
   Committing the overlay there means N copies drifting with N branches, and
   a lead who edits it in whichever checkout they are standing in forks the
   rules for every other one, silently. Measured: 16 checkouts of one repo,
   two overlays written months apart in two different clones, neither aware
   of the other, with disagreeing gate tables — so which rules a delegate got
   depended on which clone the lead was in.

   Both modes can be active at once, and an in-repo overlay comes last so it
   wins on conflict. Prefer one per repo: two active overlays put the same
   subject in the spec twice, and the delegate has no way to tell which
   sentence is current.

Read both when they exist and apply them on top of these instructions.
Include both in spec assembly, user overlay first, project overlay second,
between the shared preamble and the task spec.
