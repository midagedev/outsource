# Changelog

## 0.11.1 — 2026-08-23 — non-TTY foreground refusal, outsource-run --detach

- **A foreground launch whose stdin is not a TTY is refused (exit 64)** on
  both `grok-run` and `outsource-run`, unless `--detach` or `--foreground`
  is given. Measured: 6 field rounds (5 with `wrapper_signal=TERM`, rc=-1)
  died at wall-clock times aligned to `:08:26` / `:38:26` — a 30-minute-period
  external killer (seconds drifted 25→28 over days; identity unknown and
  outside this repo). Every victim was `grok-run.sh` launched foreground
  inside a harness-tracked background task. `--detach` rounds survived,
  including a 37-minute one. The 2026-08-19 orchestrator-timeout TERM is
  the same class. The detached child is marked `OUTSOURCE_DETACHED=1` on
  its `cmd.Env` only, so a nil stdin does not refuse itself.
- **`--detach` on `outsource-run`**, same re-exec-into-own-session
  semantics as grok-run, before harness dispatch (all three harnesses).
- **Test-suite state isolation.** Every `tests/*.test.sh` sets
  `XDG_STATE_HOME` (and `OUTSOURCE_TELEMETRY_FILE` where a test asserts
  telemetry). The field telemetry.jsonl was majority-noise on several
  axes: 151 of 153 `guard rc=2` rows, all 9 `rc=70`, all 9 vision-guard
  `rc=65` were test artifacts.
- **done-marker tests are hermetic** by default (fake `grok`/`crush`/`claude`
  on PATH; live rounds behind `OUTSOURCE_LIVE_TESTS=1`). The 2026-08-22
  live flake — model omitted the last-line marker, launcher correctly
  scored `absent` → 72, test went red — is pinned as a fake-backend case.
- **`last-report` exit 65** names what the sentinel knows (rc,
  `wrapper_signal`, finished) or that the round is still running, instead
  of a blind "no report".
- **spec-lint telemetry** carries `details.findings` / `exempt` / `missing`
  / `already-exists` so a stable ~40%/day exit-1 rate can be split into
  real wrong-premise catches vs cry-wolf.

## 0.11.0 — 2026-08-23 — opencode harness + OpenRouter stealth/ox-alpha

- **Third backend: `opencode` driving OpenRouter `stealth/ox-alpha`.**
  `--provider openrouter` defaults to harness `opencode` so
  `outsource-run.sh --provider openrouter --cwd … --spec … --log …`
  just works. The other five provider×harness cells refuse with a
  one-line reason before a round is registered (opencode is not wired
  for zai/xai; openrouter has no Anthropic-compatible URL and no cred
  row on claude-code/crush).
- **opencode owns its auth.** No `internal/cred` row: the launcher
  looks at `~/.local/share/opencode/auth.json` (XDG_DATA_HOME honoured)
  and only refuses when that file exists, parses, and has no openrouter
  key. A missing file is not proof — newer opencode also keeps a
  credential table in `opencode.db`, which this binary does not open.
- **Isolated `OPENCODE_CONFIG_DIR`**, inherited value filtered out
  before append (duplicate `KEY=value` in `os.Environ()` is
  runtime-dependent). Generated `opencode.json` denies the git-write
  class (and the listing forms the guard allows are re-allowed after,
  last-match-wins). Measured: without the deny, `git commit
  --allow-empty -m x` created a commit; with it — including under
  `--auto` and with the user's orca `OPENCODE_CONFIG_DIR` still in the
  parent env — the tool call was blocked and HEAD did not move.
- **`--auto --pure`** on the child. `--auto` is what lets a `read` of
  a path outside cwd through (`external_directory` defaults to ask;
  headless ask was rejected and the round answered nothing). `--pure`
  skips external plugins. Deny rules still hold under `--auto`.
- **Vision = true.** An agentic round that named a solid-red PNG and
  used the `read` tool answered `Red`. The `-f` attach flag remains
  documented as a trap (it is an array: `-f red.png "message"` swallows
  the message as a second file) but is not the vision path this skill
  uses.
- **`GLM_DELEGATE_MODEL` is zai-only.** It used to seed `--model` for
  every provider; a glm-* string is then rejected by opencode's
  `openrouter/<id>` check.
- **`$PWD` is replaced with `--cwd` on the child.** opencode resolves
  the session's directory from `$PWD`, not the process working
  directory (measured: the lead's own E2E, launched from the repo,
  wrote its artifact into the launcher shell's cwd while rc, marker
  and identity all read green). The export's `info.directory` is now
  asserted against `--cwd` — a round that ran elsewhere exits 70
  (`DIRECTORY MISMATCH`).
- **Live trail is the `--log` file** for this harness (`--format json`
  flushed per event while the process was still running). `Idle` now
  accepts a regular file `ProgressDir`; `HarnessShort` maps
  opencode→oc.
- **`last-report.sh` learns the opencode shape**: concatenation of
  `part.text` after the last `tool_use`. `done_marker` therefore
  scopes `report`, not a whole-log grep.
- `--require-quota` stays the existing generic "not available for this
  provider" path — stealth reported `cost: 0` and has no plan window.

## 0.10.5 — 2026-08-22 — the marker is the report's last line, not a substring

- **`done_marker=found` now means the final report's last non-empty line IS
  the marker** (markdown backticks/bold stripped), on both launchers.
  Measured false-positive (gadak GDK-616 round): the delegate's final
  message was a promise — "…the report's last line will be \`DONE-GDK616\`"
  — and the report-scope `Contains()` scored it found while the round's
  Playwright gate was still running; the lead read a delivered round where
  there was none. The spec contract was always "the last line is exactly
  the marker"; the verdict now reads exactly that line
  (`report.EndsWithMarker`, pinned by unit test). A marker glued to
  punctuation or quoted mid-sentence scores absent — the safe direction:
  absent sends the lead to the tree, found ends the audit. The crush
  whole-log grep keeps its own `done_marker_scope=log` and is unchanged.

## 0.10.4 — 2026-08-18 — `--done-marker` and `--json-schema` cannot both be satisfied

- **`grok-run.sh` refuses `--done-marker` together with `--json-schema`
  (exit 64), before the provider is contacted.** Under a schema the final
  report *is* the JSON object, so a sentinel line beside it would violate
  the very schema the flag imposes — the marker can never be found.
  Measured: a vision round launched with both flags returned a complete,
  schema-valid verdict and still exited 72 `done_marker=absent`. Nothing was
  wrong with the round; the launch was contradictory, and the lead read it
  back as a failure. The existing spec-contains preflight cannot see this
  case (the spec *did* contain the marker), so it needed its own guard.
- Completion evidence for a schema round is `rc=0` plus stdout parsing as
  schema-valid JSON — stronger than a marker string, since a truncated or
  abandoned round cannot produce a schema-valid object. Put a marker field
  *inside* the schema if you want one.
- `references/grok.md`: the vision-verdict row and the "Structured results"
  section now say this outright, so the recipe stops prescribing a pair it
  cannot honour.
- `tests/done-marker.test.sh`: the pair is refused at 64 with no provider
  contact, no log, no registry entry; `--json-schema` **alone** still
  launches normally (the guard keys on the pair, not the schema flag).

## 0.10.3 — 2026-08-18 — refuse a done-marker the spec never asked for

- **`--done-marker X` is refused (exit 64) when the spec does not contain
  `X`.** Three rounds this session launched with a marker their specs never
  mentioned; all three delivered the work and all three reported `absent`.
  The 72 we just added names that absence as the round's failure, but the
  delegate was never told to print the string — neither launcher injects it
  into the prompt, and nothing checked the spec. That is a usage error by
  the caller, caught before the provider is contacted and before the
  registry records a round. 64 is already usage on both launchers; 72 stays
  "the round ran and the report lacks the marker."
- **Both launchers now look for the marker in the final report** (via
  `last-report.sh`). A plan that quotes the marker, an echoed spec, or a
  tool result used to count as `found` on the zai launcher because it
  grepped the whole log. crush's `--log` is the model's stdout as plain
  text, not JSONL, so that harness alone still greps the log when
  `last-report.sh` cannot extract a report, and the sentinel records
  `done_marker_scope=report|log` so the two verdicts are not the same word
  for different facts.
- **Docs** (`SKILL.md`, `spec-template.md`, `spec-authoring.md`) state that
  `--done-marker` requires the completion-marker last line.

## 0.10.2 — 2026-08-18 — a missing done-marker is one fact, one exit code

- **`--done-marker` absent is exit 72 on both launchers.** The two sisters
  used to answer the same fact in opposite ways: `grok-run.sh` downgraded a
  clean exit to 70, `outsource-run.sh` left rc=0 and only wrote
  `done_marker=absent` in the sentinel. Three delivered rounds in one
  session then showed up as `failed with exit code 70` or `completed`
  depending on which launcher ran them, and the lead nearly discarded a
  faithful research report. 70 was already the documented model-identity
  failure (`mismatch or unverifiable`), so a missing marker and a remapped
  model were indistinguishable. 72 is this case only. Both launchers now
  print one stderr line naming the missing string and leaving the verdict
  on the tree. The noisy fail stays — a silent rc=0 that lets an unfinished
  round through is the more expensive lie.
- **Vision-guard copy** still refuses a spec that names an image file on a
  provider that cannot see pixels (condition unchanged). The message now
  distinguishes a pixel *verdict* (change backend) from naming an image as
  an *artifact* — capture harness, pixel-decoding script; that path wants
  `--no-vision-check`, as `references/glm.md` already allowed.
- **`tests/done-marker.test.sh`** (new) — FAIL-first against the pre-fix
  sources (grok 70 / GLM 0, no stderr line), then the three contracts
  (found → 0, absent → 72 + line, both launchers agree) plus the 70
  identity regression.

## 0.10.1 — 2026-08-17 — a signal to the wrapper is not a verdict on the round

- **`bin/signal-hold.sh`** (new, sourced by both launchers) — TERM/INT/HUP to
  a launcher now mean "hold and finish the paperwork", never "abandon the
  evidence". Twice in one week a caller's timeout SIGTERMed the foreground
  wrapper while the child round was healthy: the round delivered, but the
  sentinel writer died with the wrapper and every watcher keyed on `.rc`
  waited forever. The child is deliberately not forwarded the signal — the
  incident is a disposable wrapper outliving its usefulness, and forwarding
  would turn a bookkeeping timeout into a round kill. `await_child` rides out
  trapped-signal interrupts to the child's real exit; the sentinel gains a
  `wrapper_signal=` breadcrumb naming what it survived.
- **`bin/grok-run.sh`** — last-resort EXIT trap: an exit that never reached a
  `write_sentinel` call (script bug, `set -u` trip) writes rc=71 instead of
  leaving the one outcome watchers cannot classify. Does not run on SIGKILL;
  that residue is accepted and documented in signal-hold.sh.
- **`tests/grok-run-signal.test.sh`** (new) — reproduces the incident against
  a fake grok (FAIL-first verified: pre-fix source loses the sentinel), and
  pins the normal path unregressed.

## 0.10.0 — 2026-08-17 — one owner per fact, and docs that cannot dangle

- **`bin/grok-run.sh`** — raw grok rounds join the registry. A hand-assembled
  `nohup bash -c` launch broke on nested quoting, died silently into
  `/dev/null`, wrote no sentinel, and never appeared in the status line — two
  watchers waited on files that would never exist. The launcher now owns the
  whole contract: runs.sh registration, startup proof (the ndjson must grow
  within 30s or exit 69 aloud), the same `.rc` sentinel shape as the zai
  launcher with `done_marker=found|absent`, rc=0 downgraded to 70 when the
  marker is missing from the final report, and the git-policy profiles
  encoded instead of copy-pasted. Field-validated on two real rounds.
- **`bin/last-report.sh`** — one extractor for both report shapes
  (claude-code's last `result` event, grok's text deltas after the last tool
  event), instead of the same throwaway Python four times in one night. Exit
  65 on a report-less log, so a died round is a branch and not a silence. 12
  fixture cases pair every wanted extraction with decoy content.
- **`runs.sh dismiss`** — prune keeps orphans on principle (evidence until
  read), but there was no verb for after reading: two fabricated records from
  a test draft wore ⚠ in the status line for a day. Dismiss removes one named
  record and refuses a running round — a live pid is work, not residue.
  FAIL-first: 6/8 red on the previous runs.sh.
- **Every documented grok launch now goes through `grok-run.sh`, and the
  flag strings live only there.** grok.md carried the full raw recipe and
  three `GIT_POLICY_FLAGS` blocks as a second copy of what the launcher
  encodes — the drift class this repo keeps measuring, kept in-house. The
  launcher grew the three options the raw blocks still covered — `--research`
  (the field-tested write-block belt for investigation and vision rounds),
  `--resume <SID>` (stop-then-revise on the same session), and `--
  <flags…>` passthrough (where `--json-schema` rides) — and grok.md keeps
  the rationale, pointing at the script for the strings.
- **`scripts/grok-round-status.py` is gone.** It judged round state for
  hand-launched grok rounds; with every launch registered and sentineled by
  `grok-run.sh`, its verdicts are `runs.sh` states (running / orphan /
  done / failed), and the one nuance it added — "finished but bookkeeping
  lost" vs "killed mid-run" — is the presence of the ndjson `end` event,
  now one line in grok.md.
- **`tests/doc-refs.test.sh`** — every repo file a doc points at must exist.
  spec-lint already did this for specs against a target repo; nothing did it
  for this repo's own docs, and the inventory had drifted both ways (README
  still selling the deleted tool, and missing two shipped ones). FAIL-first:
  4 red on the tree right after the deletion above.
- `runs.sh --help` printed a fixed line range of the header and had already
  been truncated mid-sentence by two edits; it now prints the whole comment
  block, however long it grows. The crush-harness crushrc assembly collapsed
  from five heredocs (with one comment duplicated verbatim) to three.

### From the spec-lint rounds, same day

- **The to-be-created exemption was by line, so it moved the cry-wolf defect
  one page down instead of fixing it.** A spec names the file it is creating
  more than once — in the whitelist, again in the completion criteria, again in
  a test section — and only the declaration was exempt. Measured on the next
  real spec written after 0.9.0: five findings, four of them the same three
  files named a second time. The exemption is now by resolved path, collected
  in a pre-pass so a mention *above* the declaration counts too, and
  `already-exists` still reports once at the declaration rather than at every
  mention. Four more test cases, FAIL-first against 0.9.0's version.
- The header now names a limit rather than leaving it to be rediscovered as a
  bug: a path inside a command that sets its own root (`vitest --root web
  src/…`, `make -C dir`) resolves from `--root` and the spec's directory, so it
  can read as missing. Teaching the linter every tool's cwd flag costs more
  precision than it buys.

## 0.9.0 — 2026-08-16 — the delegate is not the lead, and read-only git stays read-only

- **`bin/git-guard.sh` blocked a read-only call it was written to allow.**
  `git -C <repo> worktree list` was refused. The guard erases read-only forms
  and then runs the deny pass over what is left, but the two passes spelled
  "global flags before the subcommand" differently: the deny pass understood
  `-C <path>` and `-c <k=v>` (a flag that swallows the next word), the allow
  pass did not. So the read-only form was never erased and `worktree` tripped
  the deny list. A delegate that opened by proving which tree it was in — the
  thing the specs ask for — got blocked for it, and the next spec learns to
  drop the check that would have caught a wrong worktree. The flag grammar is
  now one definition used by both passes.
- **`tests/git-guard.test.sh`** — 54 cases, the guard's first test of any
  kind. It asserts both directions, because a security boundary fails two
  ways: a mutation that slips through costs a repository, and a read-only
  call that is refused makes agents work blind. FAIL-first recorded against
  the pre-fix script (3 red: the `-C` and `-c` forms).
- **Preamble §0: you are the executor of one spec, not the orchestrator.**
  A round was lost to this. The delegate read `git log`, saw commits made
  earlier that day, ran `ps`, saw other processes, and concluded it was the
  lead of the session — then wrote zero lines of code, filed an operations
  report about "duplicate launches", installed watchers, and spawned another
  agent of its own. The spec went untouched. The new section says the things
  that were missing: never spawn an agent, concurrent rounds beside you are
  normal and not yours to manage, recent commits are the lead's history and
  not yours, an apparent contradiction goes in the report rather than into
  taking over. `spec-preamble-core.md` carries the short form, because this
  failure costs the whole round on either preamble, and §11 names the ban
  alongside the git one.
- **`--done-marker <string>` writes `done_marker=found|absent` into the
  `<log>.rc` sentinel.** `rc` is a lifecycle signal — it says the harness
  exited cleanly and nothing about whether the round did its job. Both halves
  of that gap were measured the same day: one round exited `rc=0` having
  produced no code, and another exited `rc=0` with no edits because the
  spec's own precondition check correctly told it to stop. A failure and a
  good outcome, same exit code. The marker separates them from a file read
  instead of a transcript hunt.
- **The status line shows this session's rounds, not the machine's.** The
  registry is global by design — an orphan has to be findable from wherever
  you are — but every Claude Code window reading it unfiltered meant two
  windows on two repos narrated each other's work as if it were your own.
  Caught in the act: a diagnostic on the live status line came back carrying
  a different session's id than the one that installed it. So each launch
  records its owner and each status line asks only for its own. Two keys,
  because one does not cover it: `CLAUDE_CODE_SESSION_ID` is exact but
  differs for an in-process subagent, and `CLAUDE_PID` is the Claude Code
  process the lead shares with its teammates — either matching counts as
  yours. Unowned records (launched outside Claude Code, or predating this)
  stay out of scoped views rather than appearing in all of them, and
  `runs.sh` unfiltered still lists everything with an `OWNER` column.
  `OUTSOURCE_STATUSLINE_SCOPE=all` opts back out. Record ids also gained a
  collision suffix, since `<epoch>-<pid>` could silently overwrite another
  round when a pid was recycled inside one second.
- **`tests/run-all.sh`, and `tests/runs-owner.test.sh` under it.** The
  ownership filter that scopes the status line to one session shipped without
  a test, and it fails in two directions that look nothing alike: too wide and
  another window's rounds read as your own, too narrow and a round a teammate
  launched vanishes from the view you use to notice a round died. Twelve cases
  cover both, including the ones the status line actually produces — an empty
  `--owner-claude-pid`, which must narrow nothing and must not switch the
  filter off — and the same-second same-pid launch that the record id had to
  grow a suffix for. FAIL-first recorded against `d01bbb4`: 0 of 12.
  `run-all.sh` exists because there were by then two test files, each
  documenting its own invocation in a header comment and neither wired to
  anything; a test nobody runs is a record of a past check, not a gate.
  Dropping a `*.test.sh` into `tests/` now enrols it, and an empty directory
  exits 2 rather than reporting success.
- **`bin/spec-lint.sh` reported every file a spec asked the delegate to
  create as a missing premise.** Which is most specs, so most linting runs
  opened with guaranteed findings — the exact precision failure the file's
  own header warns about twice, arriving from the other side. A path marked
  to-be-created (a `Create:` / `New files:` heading or list, or an inline
  `Create: <path>`) is no longer a claim about the tree and is exempt. It
  gains the opposite check instead: a to-be-created path that already exists
  is reported, because then the spec and the tree disagree about what the
  round is for. The count of exemptions prints on the `ok` line, since a
  suppression nobody can see is how a linter starts lying.
- **`tests/spec-lint.test.sh`** — 12 cases, half of them the defects that
  must stay loud next to the ones that went quiet: prose after a creation
  block is still linted, a heading ends the block, the inline form exempts
  one line, and a sentence that merely begins with "Create" and ends in a
  colon does not swallow the rest of the document. FAIL-first against the
  pre-change script: exactly the 5 new-behaviour cases red, the 7 regression
  cases green — which is what makes them regression cases rather than
  decoration.

## 0.8.0 — 2026-08-16 — a round you can see while it runs

- **`bin/runs.sh`** — a registry of delegated runs. Every launch records what
  it launched (label, provider, harness, model, spec, log, pid, start time)
  and, on the way out, how it ended. `runs.sh` lists it back with elapsed
  time; `runs.sh line` compresses it to one line; `runs.sh json` is the same
  data for scripts. The state that motivates the whole file is **orphan** —
  started, pid gone, no exit code. A killed round leaves no process at all,
  so `ps` answers the same nothing for "finished cleanly" and "died an hour
  ago holding your worktree"; started-but-never-finished is a state only a
  written record can hold. Records are `key=value` lines, the same shape as
  the launcher's `<log>.rc` sentinel, and this script is their only writer.
- **`bin/outsource-run.sh --label <name>`** says what the track is *for*.
  Parallel rounds are the only time the listing matters, and they are also
  where a derived label fails: this skill's documented layout writes every
  track's spec to `<scratch>/spec.md`, one dir per track, so a basename
  default would register three rounds as `spec`. The default falls back to
  the directory holding the spec, a label that still collides renders as
  `name`, `name#2`, and both are fallbacks — the docs now ask for a real
  label at launch. Registration happens after the vision and
  quota guards and before the harness dispatch — a guard that refuses to
  launch has not started a round — and an `EXIT` trap covers the paths the
  normal completion path does not, so a killed launcher cannot leave a round
  reading "running" forever. Registry failures never fail a round.
- **Stall detection, measured on output rather than elapsed time.** Neither
  harness can stop itself — `crush run` exposes no turn or time limit in its
  flag set at all, and the `claude` CLI has no `--max-turns`, only
  `--max-budget-usd` at Anthropic's prices, which says nothing about a z.ai
  plan. The obvious response is a time limit, and ten local rounds read back
  from the harness session stores say it is the wrong one: they ran 13
  minutes to **1h50m**, duration tracking message count almost linearly (66
  messages / 13m … 848 messages / 1h50m). Long rounds were long because
  there was a lot of work; cutting at an hour truncates a working delegate
  mid-edit and still misses a round that wedged at minute three.

  So the registry records where each harness leaves a live trail —
  `data/crush.db-wal` and `data/logs/crush.log` for crush,
  `claude/projects/**.jsonl` for the claude-code harness, deliberately *not*
  the `--log` file, which that harness writes once at the end — and reports
  an `IDLE` column. `⏳` fires only when a running round has written nothing
  for ten minutes (`OUTSOURCE_RUN_STALL`). Verified against both halves at
  once: a real 1h41m round that had written a second earlier stayed `▶`,
  while a live pid whose directory had been silent 30 minutes flagged. An
  elapsed-time rule would have inverted both.
- **`--max-seconds N`** hard-kills at N seconds — SIGTERM then SIGKILL to
  the harness's whole *process group*, because a signal to the shell alone
  leaves the model CLI running and only looks like a stop. The round
  finishes as exit 124 in both the sentinel and the registry, with the
  session id still recovered so a follow-up can resume. No default, and it
  should not get one: the kill lands mid-edit. It is an escape hatch for
  rounds whose loss is accepted up front, not the answer to a slow round.
- **`bin/statusline.sh`** — a Claude Code status line built from the two
  scripts above plus `bin/quota.sh`: model, account, context, the 5-hour and
  weekly Claude windows, the z.ai and grok plan windows, and the rounds in
  flight. Every budget is one token, `NAME used%/until-it-resets`, because
  neither half is actionable without the other — which is also why there is
  no bar. Quota APIs are never called on the render path: a lock-guarded
  background refresh writes a small cache (default 180 s), and unmeasured
  shows `…` rather than a `0%` that would read like good news.
  Silence means one specific thing — "this backend is not set up here" — so
  a failed refresh never erases the last good numbers: they carry forward
  prefixed `~`. Found by shipping it: an expired grok sign-in made the whole
  segment disappear, reporting a backend that had just stopped working
  exactly like one that was never configured. ~120 ms per render.

## 0.7.0 — 2026-08-16 — guardrails with exit codes

- **`bin/credential.sh` + `bin/setup-key.sh`** — key resolution gets a single
  owner, and the skill stops requiring another CLI's config file to hold your
  key. Order: the provider's env var (`ZAI_API_KEY` / `XAI_API_KEY`), then
  this skill's own `~/.config/outsource/credentials` at mode 0600, then
  discovery of files another tool already wrote — a crush config, or z.ai's
  official Claude Code helper settings. That last one is read **only when its
  `ANTHROPIC_BASE_URL` is a z.ai host**, so a real Anthropic subscription
  token can never be lifted and sent to a third party.
  `setup-key.sh` is the interactive half: it prompts with echo off, verifies
  the key against z.ai *before* storing it, and writes 0600. The launcher
  never prompts — it runs headless in the background, where a prompt hangs a
  round instead of failing it, so it points at `setup-key.sh` and exits.
  The generated crush config now calls `credential.sh` at load time, so no
  file this skill writes ever contains a key (verified).
- **z.ai's own installer counts as setup.** Discovery reads
  `~/.chelper/config.yaml`, where `npx @z_ai/coding-helper` — the vendor's
  documented path — keeps the key it verified, so following z.ai's own
  instructions leaves nothing to paste here.
- **The plan's two regions.** `credential.sh <provider> --base-url <default>`
  now owns which host an account lives on: the global coding plan is
  `api.z.ai`, the mainland-China one `open.bigmodel.cn`, and the same key 401s
  against the wrong one. It reads the helper's `plan:` field, falls back to
  Claude Code's `ANTHROPIC_BASE_URL`, and otherwise hands back the provider
  table's default untouched; `$ZAI_BASE_URL` overrides all of it. The launcher
  and `quota.sh` both resolve through it — z.ai's own usage script derives its
  monitor endpoints from the base URL the same way. Measured end to end on the
  global plan; the China host is wired from the vendor's source, not verified.

Shipped alongside a 14-round, three-way comparison (Opus 5 / grok-4.6 /
GLM-5.3, same spec, five real tickets, isolated worktrees) — written up in
the README. Four changes below come straight out of it.

- **`references/spec-preamble-core.md`** — a short substitute for the full
  preamble. Measured: dropping the preamble entirely was 16-37% cheaper in
  output tokens and never lost a gate, but self-verification went 5/5 to 0/5
  and "what I could not do" went 5/5 to 1/5. FAIL-first survived at 5/5
  either way, because the task spec demands it. The core file carries back
  the disclosure half and nothing else.
- **grok's strict git profile no longer blocks `git worktree list`.** The
  blanket `git worktree*` deny also blocked the read every spec asks for as
  the first line of the report; two rounds had to work around their own
  evidence requirement. The denies are now per-subcommand
  (`add`/`remove`/`prune`).
- **Lead checklist: read your own spec for clauses that cannot both hold.**
  A spec of ours asked for a rule that would overwrite user edits *and* for
  user edits to stay protected; three of four delegates implemented it and
  shipped a data-loss bug with every gate green. The delegate-side rule for
  this already existed in the preamble and did not fire, so the check moved
  to the lead.
- **Lead checklist: verify negative premises one path at a time.** A
  two-pattern `ls` in zsh printed nothing because the second glob matched
  nothing and aborted the command, so an existing file was written into a
  spec as absent. Same family as the `tail` trap.

Renamed `bin/glm-run.sh` → **`bin/outsource-run.sh`**: the launcher is no
longer GLM-specific. All references updated; the flag surface is unchanged
apart from the additions below.

- **Provider table** replaces the hardcoded z.ai constants. A provider is one
  row — base URL, credential source, default model, vision capability — read
  by both harnesses. `--provider zai|xai` (or `OUTSOURCE_PROVIDER`); adding
  one is a row, not a code branch. `ZAI_ANTHROPIC_BASE` still works for zai.
- **Model-identity assertion (exit 70).** A round that silently ran the wrong
  model is a failed round. Correction to 0.6.0's claim: `modelUsage` in the
  JSON log echoes the **requested** id and cannot prove a match (measured — a
  run that asked for `claude-opus-5` and was answered by glm-4.7 still logged
  `modelUsage {"claude-opus-5": …}`). The assertion now reads the per-turn
  `message.model` from the session transcript; no transcript means
  *unverifiable*, which also fails.
- **`bin/quota.sh`** — plan quota for the subscription backends, human or
  `--json`, with `--require-window N%` as a gate (exit 3).
  - `zai`: both rolling windows with real credit counts, plus plan identity.
    The console endpoint answers a bad credential with **HTTP 200** and
    `success:false`, so the body decides success, not the status line.
  - `grok`: the Grok CLI's billing proxy, authenticated with the OAuth token
    the CLI stores. Percent only — xAI exposes no counts. Carries three
    measured traps: `creditUsagePercent` is omitted when it is exactly zero
    (resolved via matching billing bounds), an expired token means "run grok
    once", not "log in again", and unified-billing accounts expose only a
    monthly budget in the default billing view.
  - The gate keys on the **tightest** window, not the shortest — measured, the
    weekly sat at 81.7% remaining while the 5-hour sat at 83.8%.
- **`--require-quota N`** on the launcher refuses to start a round the plan
  cannot finish (exit 66), and fails closed when it cannot be evaluated.
- **Cost honesty.** The launcher prints the round's token counts from the
  log's `usage` — the only per-round figure worth quoting — and says plainly
  that `total_cost_usd` is an Anthropic-priced estimate. Plan credits are
  deliberately *not* reported per round: the quota is a plan-wide counter
  that concurrent rounds and other sessions move too, so a before/after
  delta around one round measures the machine, not the round. Quota stays a
  pre-flight signal.
- **Completion sentinel `<log>.rc`** for both harnesses: `rc`, `finished`,
  `harness`, `provider`, `model_requested`, `model_actual`, `session`. The
  harness's lifecycle is not completion proof.
- **`bin/spec-lint.sh`** — pre-launch spec check for unresolvable paths and
  out-of-range `path:line` citations, the class behind five measured wrong
  premises in one session. Bare filenames are only checked when they carry a
  `:line` citation, and a reference resolving under any plausible base is not
  flagged: at the first cut it produced 30+ findings on this repo's own docs
  with zero real defects, and a linter people ignore is worse than none.
- **Vision guard (exit 65)** when a spec references an image file and the
  provider's row says it cannot see images. Driven by the table, never by a
  provider-name test at the call site.
- Lead checklist: never pipe a gate through `tail`/`head` — the pipeline's
  exit status becomes the pager's, and a hard failure reads as green
  (measured: a `vitest run` that exited 1 looked clean through `| tail`).

## 0.6.0 — 2026-08-16 — GLM on two harnesses

- `bin/glm-run.sh` gains `--harness claude-code|crush`. **claude-code is now
  the default**: `claude -p` against z.ai's Anthropic-compatible endpoint,
  with an isolated `CLAUDE_CONFIG_DIR`, the git guard attached as a
  `PreToolUse` hook, and `--session` mapped to `--resume`. The crush path is
  unchanged and still available.
- `bin/git-guard.sh` now accepts **both call conventions** — the command in
  `$CRUSH_TOOL_INPUT_COMMAND` (crush) or hook JSON on stdin (claude-code) —
  so one guard serves every harness. Regression-tested on both.
- Documented the measured z.ai model-mapping trap: an unqualified
  `claude-*` request comes back as the plan default (glm-4.7), so the
  launcher pins `ANTHROPIC_MODEL`; `modelUsage` in the log is the proof of
  which model answered. Also: `ANTHROPIC_BASE_URL`/`AUTH_TOKEN` are honoured
  (an invalid token 401s), and the harness's `total_cost_usd` is an
  Anthropic-priced estimate, not the plan's charge.

## 0.5.0 — 2026-08-16 — one skill, two backends: outsource

- **Renamed** grok-delegate → **outsource**, and absorbed the glm-delegate
  skill: one skill now routes to two backends — grok-4.6 (grok CLI) and
  GLM-5.3 (z.ai coding plan via the crush CLI).
- **Restructured**: SKILL.md is a thin router (backend table, spec assembly,
  unified 11-point lead review checklist); per-backend operating manuals
  moved to `references/grok.md` / `references/glm.md`; spec-authoring
  material (quality bundle, per-task template checks) to
  `references/spec-authoring.md`. The shared `spec-preamble.md` is
  backend-neutral; `glm-preamble.md` carries the GLM runtime delta
  (no images, hook-based git ban, evidence rules §6–§11).
- **New GLM backend tooling**: `bin/glm-run.sh` (isolated
  `CRUSH_GLOBAL_CONFIG`, scratch data dir, `SESSION <id>` resume) and
  `bin/git-guard.sh` (command-string PreToolUse guard, 29 regression cases).
- **New receipts** in the README: three shipped GLM solo rounds plus a
  same-spec A/B vs an Opus subagent (N=3) — parity on pattern discovery,
  premise correction and FAIL-first; three measured gaps, each promoted to
  a spec rule.


All notable changes to the `grok-delegate` skill/plugin are documented here.
Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/);
versions track `.claude-plugin/plugin.json`. Every rule added to the skill
maps to a real field incident — the sections below say which.

## Pre-0.5.0 — shipped before this file carried version headings

### Added
- README "Updating" sections (marketplace and script paths, EN/KO).
- **Round-completion evidence protocol** (SKILL.md) + `scripts/grok-round-status.py`.
  Incident (2026-08-15, third in class): a lead nested the launch recipe one
  background layer deep (`launch.sh &` inside a harness background command);
  the harness fired "task completed" when the wrapper exited while grok kept
  working as an orphan, and the truncated log plus clean tree read as a dead
  round. Earlier members of the class: a watcher `pgrep` matching itself, and
  exit-0-with-empty-turn. Structural fix: the launch recipe now writes a
  `done-<track>.rc` sentinel after grok exits, the sentinel is the ONLY
  completion proof, one-background-layer-only is an explicit rule, and the
  status script renders the verdict (COMPLETED / RUNNING / DIED-NO-SENTINEL,
  the last split by ndjson `end` into "sentinel lost" vs "killed mid-run").
  Validated against live data: a running round, a finished pre-sentinel
  round, and a missing track each got the correct verdict.

### Changed
- `install.sh` now writes a checksum manifest (`.install-checksums`) and
  refuses only when the installed copy was **hand-edited since the last
  install** — plain upgrades no longer need `--force`. Installs are now
  clean (stale files removed); `references/local-overlay.md` is still
  preserved and never checksummed.

## [0.4.0] — 2026-08-14

Two field-audit rounds folded in: a 9-delegation live session (2026-08-13:
5 investigations, 3 implementations, 1 mixed) and a 5-PR CHANGES_REQUESTED
audit (2026-08-14: PRs 13791/13798/13828/13842/13858 — 8 blocking findings,
all with tests and typecheck green).

### Added
- **Profile picker table**: delegation type (implementation single/parallel,
  investigation, vision verdict, asset generation) → git profile, extra
  flags, required spec sections.
- **Read-only investigation profile** (`--deny Write --deny Edit
  --disallowed-tools write,search_replace`), field-tested 5/5 with zero tree
  changes, plus guidance for consuming investigation output: trust the
  file:line facts, re-derive the verdicts; treat premise corrections as
  top-priority findings.
- **Vision-verdict one-shot recipe**: fresh SID, retire after one verdict,
  readonly belt, `--json-schema`, and the 3-element briefing (numeric
  context first · narrowed question · "do not judge" list).
- Preamble: **file list is a whitelist** (list wins over folder-level
  wording; out-of-list files are reported, never touched), **tests must earn
  their green** (no proxy waits; negative assertions right after render;
  reuse neighboring mock patterns), **shared-lib consumer census** (importer
  grep + per-consumer lost-behavior table; fences mean "report regressions",
  not "ignore the app"), **no dead-code deletion without repo-wide grep
  evidence** (even when the spec orders it), **options/guards kept-vs-dropped
  table** for moved/replaced functions, **file moves check references in both
  directions** with a code-file-aware link counter ("newly broken: 0" as a
  number).
- Report format: "deliberately left untouched" is now separate from
  "could not verify".
- Lead spec-writing rules: never pair a file list with a folder phrase,
  never fence at an app boundary when editing a shared lib, never order an
  unconditional "delete the dead code", and treat green as necessary but
  not sufficient — the report tables are completion criteria in their own
  right.
- Review checklist: duplicated helpers diffed against the latest sibling for
  dropped guards; proxy-wait detection in test diffs; bidirectional
  reference grep after moves.

### Fixed
- **Corrected a wrong claim in the preamble**: CLAUDE.md files are injected
  from the ancestor path of `--cwd` only (measured via `grok inspect`) —
  nested `apps/*/CLAUDE.md` and `libs/*/CLAUDE.md` never inject, so specs
  must enumerate them explicitly. The previous text said they were injected
  wholesale.

## [0.3.0] — 2026-08-13

### Added
- Plugin marketplace distribution (`.claude-plugin/plugin.json`,
  `marketplace.json`) and bilingual READMEs with the 9-experiment
  blind-judged evidence table.
- Mid-round visibility: `--output-format streaming-json` recipe,
  `scripts/grok-progress.py`, ACP `updates.jsonl` notes, and the honest
  intervention path (a second client cannot steer a live `-p` turn — stop
  with SIGTERM, resume with `-r` and a revised spec).
- Built-in `image_gen`/`image_edit` (and video tool availability) field
  notes; bundled grok skill index (`imagine`, `game-*`, `pdf`, ...).
- Social preview card assets.

### Fixed
- 7 field-audit findings applied to the skill (flag corrections, session
  pin/resume discipline, completion-by-tree verification).

## [0.2.0] — 2026-08-13 (pre-manifest)

### Added
- Git policy profiles (strict / readonly-plus / trusted) replacing the
  blanket git ban, so investigation-heavy tasks keep read access.
- Local overlay hook (`references/local-overlay.md`) for project/user
  context, preserved across installer upgrades.

## [0.1.0] — 2026-08-13 (pre-manifest)

### Added
- Initial `grok-delegate` skill: invocation recipe, shared spec preamble,
  spec template, quality bundle, lead review checklist, `install.sh`.
