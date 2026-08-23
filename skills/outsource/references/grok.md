# grok backend — running the grok CLI headless

Loaded on demand from the outsource skill. Everything here is
field-measured on grok-4.6. Shared implementer rules live in
references/spec-preamble.md; spec-authoring guidance in
references/spec-authoring.md.

## Invocation recipe

### Profile picker — decide these three things first

| Delegation type | git profile | extra flags | spec must include |
|---|---|---|---|
| Implementation, single track | strict | — | file whitelist · numeric contract · verification commands · **every CLAUDE.md covering the targets** |
| Implementation, parallel / risky / registers gates | strict (or trusted for WIP commits) | lead-created worktree `--cwd` | + track boundary, per-package gates, copy-artifacts-out rule |
| Investigation / census (answer in the final message) | strict (git reads help) | `--research` belt; subagents stay off | narrow questions · premise-check invitation · **deliverable = final message, never a file** (the belt denies Write; the runner injects a notice, but do not spec a report file) |
| Report whose deliverable IS a file | strict, **no `--research`** | whitelist the one report path in the spec | measured 2026-08-17: a research-belt round specced to write a report looped "writing it" for 301 turns — the belt and the deliverable contradicted |
| Vision verdict | readonly-plus | `--research`; `-- --json-schema '<schema>'` for the verdict; **no `--done-marker`** | 3-element briefing (numeric context first · narrowed question · "do not judge" list); fresh SID, retire after one verdict |
| Image/asset generation | strict | worktree `--cwd` | copy-out path for `~/.grok/sessions/...` outputs · JPEG/matting plan |

Everything below details the rows of this table.

### One-shot task (default form)

**Launch through `bin/grok-run.sh`, not by assembling the raw block below**
(field incident 2026-08-17: a hand-assembled `nohup bash -c` launch broke on
nested quoting, died silently into `/dev/null`, wrote no sentinel, and never
appeared in the status line — two watchers waited on files that would never
exist):

```bash
bash <skill-dir>/bin/grok-run.sh --detach \
  --cwd <dir> --spec <scratch>/spec.md --log <scratch>/grok-<track>.ndjson \
  --label <track> --done-marker DONE-<TRACK> \
  [--git-profile strict|readonly-plus|trusted]
```

`--detach` makes the launcher own its single background layer: usage errors
(bad path, marker not in spec) still fail synchronously on your terminal,
then the round re-execs into its own session and survives you. Do not wrap
it in `nohup … &` yourself — the foreground form dies with your process
group (measured 2026-08-19: an orchestrator's 2-minute command timeout
TERM-ed a lane ten minutes into its work; rc=143, wrapper_signal=TERM).
The same class lost six more rounds on 2026-08-22 (wall-clock times aligned
to :08:26/:38:26, an external 30-minute killer; identity unknown) when
`grok-run.sh` was launched foreground inside a harness-tracked background
task with no TTY; a non-TTY foreground launch now refuses at exit 64 and
names `--detach` (recommended) or `--foreground` (deliberate block / tests).

It registers the round in `runs.sh` (the status line sees it), verifies grok
actually started (ndjson must grow within 30s, else exit 69 out loud), writes
the same `<log>.rc` sentinel shape as the zai launcher — including
`done_marker=found|absent`, with rc downgraded to 72 when a clean exit lacks
the marker in the final report (70 is the zai launcher's model-identity
failure, not this) — and owns the git-policy profiles below so they stop
being copy-pasted into shells. Read the round's report with
`bin/last-report.sh <log>`. To block until one or more rounds finish
(instead of hand-writing a `while [ ! -f $log.rc ]` loop per lane), run
`outsource wait [--interval N] [--timeout N] <log>...` — it refuses a
mistyped log path up front and prints each sentinel as it lands.

Prepend the shared preamble to the task spec first (`cat
<skill-dir>/references/spec-preamble.md <scratch>/task.md >
<scratch>/spec.md`). **Completion is proven by the `<log>.rc` sentinel —
never by your harness's task notification**, which reports your wrapper
shell's lifetime, not grok's (see "Round completion" below).

`bin/grok-run.sh` itself is the reference for the exact grok invocation (it
is ~150 readable lines: session pinning with `-s`, `--no-memory`,
`--always-approve --permission-mode bypassPermissions`, xhigh effort, a
1200-turn ceiling, `--no-plan --no-subagents`, `streaming-json` with stderr
split off — each choice justified in the flag notes below). A round that
needs a grok flag it has no option for gets it through `-- <flags…>`; a
round that needs one *repeatedly* gets a real option added here — never a
hand-assembled `nohup bash -c` block, which is the incident this launcher
closed.

### Round completion — what counts as evidence

Field-measured failure class (three incidents): the lead treats a
process-lifecycle signal as round completion and misdiagnoses a live round
as dead (or a dead one as alive). A harness "task completed" notification, a
wrapper shell exiting, `pgrep` matching your own watcher, and exit code 0
have each produced a wrong verdict. The rules:

- **The only completion proof is the `<log>.rc` sentinel** grok-run.sh
  writes. Notification without a sentinel = your wrapper exited early and
  the round is still running — re-attach a watcher, don't collect.
- Judge state with `bin/runs.sh`: `running` (pid alive), `orphan` (started,
  pid gone, no rc — the round died without finishing), `done`/`failed` from
  the recorded rc. For an orphan, the ndjson's last line is the tiebreak: an
  `end` event means grok finished and only the bookkeeping was lost; no
  `end` event means it was killed mid-run and the tree may be half-written.
- If you must poll by process, match the exact session:
  `pgrep -f "grok -s $SID"` — a bare `grok` pattern matches idle leftovers
  and your own watcher loop.
- A clean `git status` plus a truncated log is NOT "the agent did nothing" —
  check RUNNING first; the round may simply not have edited yet.

### Git policy — pick one profile per delegation

`--deny 'Bash(git *)'` blocks *all* git, including reads — grok then cannot
inspect commit history, blame, or PRs, which hurts investigation-heavy tasks.
`grok-run.sh --git-profile` owns the flag strings; choose deliberately:

- **strict** (recommended default): per-subcommand denies on every
  state-changing git/gh form; reads (`git log/show/diff/blame`, `gh pr
  list|view`) still work. Field-tested: reads pass, `git commit` is blocked,
  HEAD unchanged. The worktree denies are per-subcommand on purpose — a
  blanket `git worktree*` also blocks `git worktree list`, which every spec
  asks for as the first line of the report (measured 2026-08-16: two grok
  rounds had to work around their own evidence requirement and said so).
  Read-only git is deliberately open; keep it that way.
- **readonly-plus** (paranoid): the blanket ban, `git` entirely denied. For
  parallel tracks with tight file boundaries where even a git read prompt is
  unwanted — and for vision verdicts.
- **trusted**: no git denies. Only inside an isolated worktree the lead
  created (see "Parallel tracks"), when you WANT grok to make WIP commits at
  round boundaries. The lead still reviews history and merges.

Glob denies are a safety net, not a proof — exotic forms (`git -C <path>
commit`) can slip past subcommand patterns. Keep the preamble's git rules in
the spec as the second layer, and treat the trusted profile as trust +
isolation, not as enforcement.

### Read-only investigation profile (research / census / audit tasks)

For investigation-only delegations (web research, code census — no tree
changes wanted), add the write-block belt on top of a git profile with
`grok-run.sh --research` (`--deny Write --deny Edit
--disallowed-tools write,search_replace`). Under this belt the deliverable
is the **final message**: the runner prepends a notice saying so, because a
spec that asks for a report *file* contradicts the belt (2026-08-17: 301
turns of "writing the report" with no write tool). If the deliverable must
be a file, drop `--research` and whitelist that one path instead.

Field-tested: five consecutive investigation runs with this belt +
`--permission-mode bypassPermissions` produced zero tree changes. The part
doing the enforcing is `--disallowed-tools` — bare tool-name `--deny`s are
unreliable on their own (see the flag notes below) — so keep the belt
intact as a set and still confirm with `git status --short` afterward.

Investigation specs **still get the preamble**: its premise-checking,
no-invented-copy and verdict-discipline rules all apply. The
implementation-shaped report items (changed-file list, gate outputs) simply
collapse — state in the spec "read-only task: report format items 1–2 are
'no tree changes' plus your verification greps". If the deliverable is a
large report, have grok **write it to files section by section** (one path
per section, listed in the spec) instead of returning it on stdout.

Two field-measured patterns for *consuming* investigation output:

- **Fact collection is dense and trustworthy; verdicts are not.** Trust the
  file:line citations, but re-derive every load-bearing conclusion from the
  cited source before acting on it. grok skews conservative or wrong at the
  judgment step — one census marked a finding "cannot determine" when a
  single comparison of two constants in the cited source settled it.
- **Premise corrections are signal, not noise.** The preamble instructs grok
  to challenge the spec's own premises; when a report says "your background
  claim / path is wrong", treat that as a top-priority finding (twice it
  changed the direction of the resulting PR).

**Always prepend the preamble** (`references/spec-preamble.md`). Every item
in it comes from a real incident. Do not tell grok to "go read that file" —
merge it into the prompt body; the spec must stand alone.

**⚠ Nested CLAUDE.md files are NOT injected** (field-measured 2026-08-14 via
`grok inspect`): grok walks only the **ancestor path of `--cwd`** for
CLAUDE.md (`.claude/rules/*` all inject regardless). With `--cwd` at the
repo root that means the global + root CLAUDE.md only — `apps/*/CLAUDE.md`
and `libs/*/CLAUDE.md` inject **zero** times, and a lib's CLAUDE.md is an
ancestor of **no** cwd, so it never injects at all. The lead must enumerate
every CLAUDE.md covering the edit-target directories at the top of the
spec's "Files to read before starting" list. (24h field audit: only 7 of 16
task files compensated in the prompt, and the incident cluster sat exactly
in the never-injected `libs/*`.)

Field-tested flag notes:

- **Use `--always-approve`.** `--permission-mode acceptEdits` plus individual
  `--allow` rules silently blocks the first unmatched tool call in headless
  mode — grok prints one intent line and exits 0 with zero tree changes
  (reproduced twice). The git ban stays enforced by `--deny`.
- **Verify completion by the tree, not the exit code.** exit 0 ≠ work done.
  Check `git status --short` for real changes and the end of the log for the
  completion checklist; on an empty turn, resume with `-r <SID>` and say
  "you must call tools and do the work this turn" (this is why the default
  form pins a session id with `-s`).
- **Enforce the git policy mechanically** (profiles above). Commits, restores
  and stashes belong to the lead in profiles 1–2; profile 3 delegates WIP
  commits but never pushes/merges.
- **Deny only with `Bash(...)` command patterns — tool-name denies are a
  trap.** grok's native tool names are lowercase (`write`,
  `search_replace`); `--deny write` passes without error and the file still
  gets written, while an unknown name like `--deny NotebookEdit` hard-errors
  the whole call. Get file-safety from worktree isolation plus the spec's
  file boundary, not from tool denies. (`--disallowed-tools
  write,search_replace` is a *different flag* and did hold across read-only
  investigation runs — see the investigation profile above — but for
  implementation runs, isolation + spec boundary remain the real fence.)
- **Pin the model with `-m`.** The default drifts across accounts and CLI
  versions (4.5 ↔ 4.6), and `grok models` shows the *unauthenticated*
  default when logged out — easy to misread. Every number in this skill was
  measured on grok-4.6.
- **Pass `--no-memory`.** If `--experimental-memory` is enabled in config,
  assumptions leak between rounds and reproducibility dies — same purpose as
  the `-s` pin-once / `-r` resume discipline.
- **`--output-format streaming-json` is the recommended log.** `plain` is
  still the CLI default and still works; a dummy turn's stream included
  `tool_call` (`toolName`, `rawInput`), `tool_call_update` (`rawOutput` when
  `status` is `completed`), token-chunk `text`/`thought`, and `end`. Split
  stderr so the NDJSON file stays line-parseable. See "Visibility and
  intervention".
- For potentially destructive large tasks, isolate in a **lead-created git
  worktree** (recipe under "Parallel tracks") and collect only the diff.
  The CLI's own `--worktree` flag belongs to interactive sessions — in
  headless `-p`/`--prompt-file` runs no worktree is created (field-tested,
  and stated in `--help`).
- `--reasoning-effort xhigh` and `--max-turns 1200` keep depth requirements
  from being squeezed by the turn cap; with `--prompt-file` this combination
  completes multi-hundred-line packages in one turn. The cap is a ceiling,
  not a target — 800 has also been sufficient for large censuses; never
  *lower* it to save tokens, that only truncates depth.
- **The "implementation tokens are free" premise is measurable, not an
  article of faith**: `--output-format json` returns `usage`,
  `total_cost_usd` and `modelUsage` — sample a round and check.

### Follow-up in the same context

grok-run.sh mints the session id and writes it to `<log-basename>.sid`; a
follow-up round passes it back with `--resume "$(cat …/<track>.sid)"`. A
session id is **pinned once** (`-s`, the launcher's default) and only
**resumed** afterwards (`-r`, what `--resume` maps to) — re-invoking `-s`
with a used id fails with "Session ID already in use". For a new round
(e.g. a FIX round whose spec is self-contained anyway), just launch fresh;
nothing is lost.

### Parallel tracks (worktree isolation)

When two delegations touch modules that import each other, a shared tree
produces phantom gate failures (track A's gate reads track B's half-edited
file). Isolate each track:

```bash
git -C <repo> worktree add ../<repo>-wt-<track> -b wt/<track> HEAD
ln -sfn <repo>/node_modules ../<repo>-wt-<track>/node_modules  # deps without reinstall
# launch grok with --cwd <absolute worktree path>; lead merges diffs
# sequentially after review, then removes the worktree.
```

Give each track an explicit writable-file list (code + its own gate file),
keep the gate files disjoint, and state in each spec that other tracks'
breakage is report-only. The lead applies diffs one track at a time.

### Structured results

When you need to parse a verdict, add `--json-schema '<JSON Schema>'` —
stdout becomes schema-constrained JSON.

**`--json-schema` and `--done-marker` are mutually exclusive.** The marker is
looked for in the round's final report, and under a schema the final report
*is* the JSON object — a sentinel line cannot appear beside it without
violating the schema the same flag imposes. Passing both is a contradiction
the lead writes into the spec and then reads back as a failure: measured
2026-08-18, a vision round produced a complete, correct verdict and still
exited 72 `done_marker=absent`. Nothing was wrong with the round.

For a schema round, completion evidence is the sentinel's `rc=0` plus
**stdout parsing as valid JSON against the schema**. Only add a marker field
*inside* the schema if you want one, and never as the launcher's
`--done-marker`.

**But schema validity is not proof that the work happened.** Measured
2026-08-19: a `--json-schema` round made **zero tool calls** (ndjson event
census: 534 `text`, 35 `thought`, 1 `end` — no tool events, 39.5k input
tokens = the spec alone) and returned a fully schema-valid object whose
string fields were `"placeholder"` and whose ranked list was `["a","b","c"]`.
`rc=0`, schema-valid, and worthless. The constraint appears to suppress tool
use: the model answers the schema directly instead of working first.

So **do not use `--json-schema` for any round whose value comes from tool
calls** — reading files, opening images, running probes. Use it only for
rounds that are pure judgement over material already inlined in the spec.
For everything else use `--done-marker` and ask for a fenced ```json block in
the report, then parse and validate that block yourself.

Whatever the shape, add a work-happened check to the completion criteria:

```bash
python3 - <<'EOF'
import json, collections
c = collections.Counter()
for line in open('<log>.ndjson'):
    try: c[json.loads(line).get('type', '?')] += 1
    except Exception: pass
print(c.most_common())   # no tool_use / tool_result events => the round did not work
EOF
```

and reject placeholder values (`placeholder`, `TBD`, `a`/`b`/`c`, `<...>`)
outright rather than reading past them.

### Vision verdict (one-shot judge)

The lead never reads screenshots itself — image Reads bloat the lead
transcript; only the judge's **text verdict** comes back. Recipe:

- Fresh SID per verdict; the judge **retires after one verdict** (image
  turns make these the heaviest sessions). A FIX round gets a *new* judge.
- Flags: `--git-profile readonly-plus --research`, plus `--done-marker`.
  **Do not use `--json-schema` on a vision round** — measured 2026-08-19 it
  suppressed tool use entirely and the judge returned a schema-valid
  placeholder object without opening a single PNG (see "Structured results").
  Ask for a fenced ```json block instead and validate it yourself.
- **Name the frames the judge must open, and require a per-frame observation
  line before the verdict.** A judge that has to write "frame-009 — what is
  visible" for twelve named files cannot skip the images. Then confirm the
  ndjson has tool events.
- The briefing has three mandatory elements (each earned by a round of
  decisive verdicts): ① **numeric context first** — the measured table, so
  the judge spends itself on perception, not re-measurement; ② a **narrowed
  question** ("does it read as the same postcard?", "mood or underexposure?"
  — never an open "evaluate this"); ③ a **"do not judge" list** for defects
  a parallel track is already fixing, so rounds don't block each other. If
  FIX is likely, pre-narrow the adjustable axes and safe floors.
- Give the judge absolute image paths on disk; do not inline images into
  the spec file.
- A verdict that contradicts instrumented measurements escalates to a
  Claude agent — that is the standing fallback, not a retry-with-grok.

## Visibility and intervention

Headless `--output-format plain` (CLI default) does not mark tool-call
boundaries. `--output-format streaming-json` does. `--stream-events` is
**not** a flag (`unexpected argument '--stream-events'`). Token deltas on
the Messages-shaped stream use `--include-partial-messages` with
`--output-format streaming-messages-json`.

### Mid-round check (lead)

From a clone of this repository (this script is **not** copied by
`install.sh`):

```bash
python3 scripts/grok-progress.py --last 20 <scratch>/grok-<track>.ndjson
python3 scripts/grok-progress.py --tail --tools-only <scratch>/grok-<track>.ndjson
```

Default output is at most 100 lines (over that: a count summary + last 5).
`--tail` is uncapped and stamps `[mm:ss]` from follow start. Offline
`streaming-json` has no per-event timestamp, so the clock prints `[--:--]`.

The same session writes `~/.grok/sessions/<url-encoded-cwd>/<sid>/updates.jsonl`
(ACP updates with unix timestamps, including `tool_call`). The progress
script accepts that file. A `--output-format plain` dummy still wrote
`tool_call` there; its stdout had no `"type":"tool_call"` lines.

```bash
grok sessions list          # from the same --cwd; shows id + summary
grok sessions search <word>
```

After the process exits, `grok -r <SID> -p "…"` continues the same
conversation (verified: named the three files already read). `grok export
<SID>` and `grok trace --local -o <path> <SID>` both succeeded on a
finished id.

### Human path

- Same `--cwd`: `grok sessions list` / `grok sessions search`.
- After the run ends: `grok -r <SID> -p "…"` continues the conversation
  (verified). Interactive `grok -r <SID>` with no `-p` was not captured
  as a usable TUI attach in this work.
- `grok dashboard` is a TUI of sessions in **that pager process**.
  `grok dashboard --leader` is rejected (`unexpected argument '--leader'`;
  tip: `--leader-socket`). A short TTY attach to
  `grok dashboard --leader-socket <sock>` produced only a terminal query —
  **not verified** as a way to watch a separate headless `-p` process.

### Intervention

**A second client does not steer a live `-p` turn.** While a dummy was in
the tool loop, each of these printed `INTERVENED` **and** the original
process finished all of its tools:

- `grok -r <SID> -p "INTERVENTION…"` (no leader)
- the same pair with `--leader --leader-socket <sock>`
- ACP `session/load` + `session/prompt` on that id (the stream showed
  `_x.ai/queue/changed` then `runningPromptId` for the new prompt)

Do not use a second `-r` or ACP prompt to redirect a live round.

**Stop, then resume with a revised spec:**

```bash
kill <pid>    # SIGTERM: wait-status 143; the NDJSON log has no `end` line
nohup bash <skill-dir>/bin/grok-run.sh \
  --cwd <dir> --spec <revised-spec.md> --log <scratch>/grok-<track>.ndjson \
  --label <track> --resume "$(cat <scratch>/grok-<track>.sid)" \
  > <scratch>/launch.out 2>&1 &
```

Completed tool results stay in the session (after SIGTERM, `-r` answered
`alpha.txt` when that read had finished, and `none` when only `list_dir`
had). Work after the last completed tool is lost. File edits already on
disk are **not** rolled back (headless docs; the dummy itself was
read-only). A 6s PTY `grok -r <SID>` with no `-p` did not stop the
headless client.

`grok leader list` did not discover a local
`grok agent leader --leader-socket` (`No leader candidates found`) even
while `--leader --leader-socket` clients ran against that socket.

### Failure modes

- Non-JSON / truncated last lines are skipped. Kill mid-write leaves no
  `end` event.
- `--tail` follows the open file descriptor. Path replacement (log
  rotation) was not exercised — do not assume it is followed.
- `available_commands` and `usage` are dropped; `thought` only with
  `--thinking`. Missing those is not a hung agent.
- Unknown `type` values are ignored. If a CLI upgrade goes silent, read
  the checkpoint file from the preamble.

## Image generation (built-in `image_gen` / `image_edit`)

Headless grok CLI sessions have image generation built in — the `image_gen`
and `image_edit` tools work under `grok -p/--prompt-file` with
`--always-approve` (field-verified 2026-08-13, grok-4.6). **Video generation
is also present**: `image_to_video` and `reference_to_video` report available
in the same headless sessions (availability field-verified 2026-08-13;
generation itself not yet exercised — 6s/10s shots per the bundled `imagine`
skill, frame-harvest pipeline per `game-animation-frames`). Guidance for the
tools themselves ships with grok at `~/.grok/bundled/skills/imagine/SKILL.md`
(prompt-craft, reference-first rules, consistency via `image_edit`
anchoring); related bundled skills cover game assets
(`game-tilesets`, `game-asset-core`, `game-animation-frames`, ...).

Field-tested facts the spec must account for:

- **Output lands outside the worktree**: the tool writes to
  `~/.grok/sessions/<url-encoded-cwd>/<session-uuid>/images/N.jpg`. Always
  instruct grok to **copy the result into the worktree** at an explicit path
  and `ls -la` it in the report.
- **Output is JPEG** even when you ask for PNG — no alpha channel. If the
  asset needs transparency (sprites, atlases), the spec must include a
  matting step (generate on a distinct flat key color, then key it out in
  post) or accept opaque cards.
- Observed resolution tier: 1024×1024 at `aspect_ratio 1:1`. `aspect_ratio`
  works (`16:9`, `9:16`, ...); there is **no `n`/count parameter** — issue
  multiple calls for variations.
- For a recurring look across assets, generate one canonical reference and
  derive the rest with `image_edit` (independent `image_gen` calls drift).
- Moderation blocks are terminal: the spec should say "report the block,
  do not paraphrase-retry".
- Treat generated assets like any other artifact: numeric contract
  (palette bands, coverage) + the lead's independent vision verdict before
  commit. Generation quality is high enough to beat procedural texturing
  for painterly/organic assets (clouds, terrain washes), so prefer
  generate→post-process→gate over shader-only approaches there.

## Bundled grok skills — index

grok ships built-in skills at `~/.grok/bundled/skills/<name>/SKILL.md`.
grok auto-loads them when the task matches; **naming the skill in the spec**
("load the imagine skill", "follow game-tilesets") force-loads it. This is
the notable subset — read the SKILL.md at that path for details:

| Skill | What it gives a delegated task |
|---|---|
| `imagine` | `image_gen`/`image_edit` prompt-craft, reference-first rules, consistency anchoring (see section above) |
| `game-asset-core` | Core rules + engine-ready defaults for generated game assets — base skill for the `game-*` family |
| `game-tilesets` | Seamless/transition tilesets **that actually tile** — use for ground/terrain textures |
| `game-animation-frames` | Video-first animation frame sets that actually cycle |
| `game-character-consistency` | Same character across every generated image |
| `game-ui-icons` | Game UI kits and icon sets |
| `design` / `implement` / `review` / `execute-plan` | grok-internal multi-agent loops (writer↔reviewer consensus, implement-review-fix, PR-plan DAG execution). NOTE: these spawn grok subagents — drop `--no-subagents` if a spec asks for them; normally we keep our own lead-owned loop instead. Mind the stdout-interleaving caveat under Operational tips |
| `code-review` | Strict maintainability audit (abstraction quality, giant files, condition growth) |
| `pr-babysit` | Monitor PRs: fix CI, address review comments, resolve conflicts, restack |
| `pdf` / `docx` / `pptx` | Read/create/transform documents and slide decks |
| `resume-claude` / `resume-codex` / `resume-cursor` | Continue from another agent's recent session — lets grok pick up a Claude Code session's context |
| `create-skill` / `create-workflow` / `skill-design-principles` | Author new grok skills/workflows |

## Operational tips (field-tested)

- Headless grok sometimes finishes a `-p` turn with partial work. Define a
  completion marker (`DONE-<track>`) in the spec and, as a safety net, loop
  `-r <SID> -p "continue"` until the marker appears. `--no-plan` is required.
  With `--prompt-file` + xhigh + high turn caps this is rarely needed.
- **Subagent stdout interleaves and corrupts report-shaped output.** On a
  749-file census with grok subagents running in parallel, sections and
  tables arrived garbled mid-report (field-tested; some tables unreadable).
  Keep `--no-subagents` whenever the deliverable is a single report, or have
  the report written to files section by section instead of stdout —
  especially when tables carry the payload.
- Keep gates from dumping data:URL bundle stacks — trap
  `process.on('uncaughtException')` and print the message only.
- **Scope gates per package for parallel tracks.** Whole-tree builds fail on
  other tracks' half-finished code; the lead runs the full gate serially
  after tracks close.
- Entry-point files (CLI switch tables, help text) attract every track —
  schedule those tasks sequentially, not in parallel.
- With browser E2E suites, kill zombie server processes first; a wiped suite
  looks like a code regression when it is a port squatter (0ms failures =
  suspect the environment).
- Reference images help only when they show **the same effect type** as the
  task. A reference of a different effect type transplants the wrong visual
  language — if you attach references, say "borrow the color/edge
  discipline, not the shapes".
- **Absolute paths everywhere**: `--prompt-file` resolves against `--cwd`,
  and shell cwd resets between the lead's own tool calls — relative paths
  have produced "the edit didn't land" misdiagnoses (it read the wrong
  copy). Put absolute paths in the spec's file lists and verification
  commands too.
- **Don't wrap grok in `timeout`.** Killing it mid-run leaves a half-written
  tree that reads as a grok defect on review. (Stock macOS also lacks
  `timeout(1)`; coreutils adds one — but the half-written-tree reason holds
  everywhere.) Run it in the background through your harness and watch the
  log/tree instead.
- Before diagnosing a hung delegation or lock contention, `pgrep -fl grok` —
  idle sessions left over from earlier rounds are common and easy to
  mistake for your run.
