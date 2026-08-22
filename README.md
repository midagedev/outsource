# outsource

**Your Claude session does the thinking. Cheaper model subscriptions do the typing.**

[English](./README.md) | [한국어](./README.ko.md)

A Claude Code skill that runs **third-party model CLIs as headless implementation sub-agents**. The lead Claude session keeps the work where judgment matters — specs, diff review, gates, commits — and outsources implementation, mechanical edits, investigation, and screenshot verdicts to models whose tokens are effectively free on a subscription.

It is not a wrapper. It is an operating manual with receipts: every rule in it came from a measured round, and the [comparison below](#the-three-models) is how the rules were found.

| Backend | Runs via | Use it for | Hard limit |
|---|---|---|---|
| **GLM-5.3** — the default | [z.ai coding plan](https://z.ai/subscribe), driven by `bin/outsource-run.sh` on **either harness** — headless Claude Code (`claude -p`, default) or the `crush` CLI | every spec-able round: implementation, gate authoring, code investigation | **cannot see images**; does not flag a contract it cannot satisfy |
| **grok-4.6** — the exception | `grok` CLI | what GLM structurally can't do: **vision verdicts**, image/video generation, web research | notices a hazard and implements it anyway unless the spec forbids it |
| **ox-alpha** — OpenRouter stealth | opencode CLI, via `bin/outsource-run.sh --provider openrouter` | spec-able rounds plus **vision through the read tool** (measured: named a solid-red PNG, answered "Red"); `step_finish.cost` was 0 while stealth | stealth identity/limits can change without notice |

A provider that talks Anthropic-compat (zai, xai) is a table row — base URL, default model, vision — plus its key resolution in `bin/credential.sh`. A provider that brings its own CLI and auth store (openrouter via opencode) is a table row with an empty URL, a dedicated harness, and no cred row — opencode already logged the user in.

It also ships the [status line](#status-line) that makes delegation legible while it happens — what stops this session, what stops the next round, and what is running right now:

```
opus │ you@example.com │ CTX 12% │ 5H 8%/3h20m │ 1W 38%/4d2h
z.ai 29%/6d4h │ grok 98%/2h19m │ 🛠2 ▶api zai·crush 12m  ▶tests zai·cc 4m │ repo (main)
```

## Install

```
/plugin marketplace add midagedev/outsource
/plugin install outsource@outsource
```

Or with the install script (preferred if you'll use a [local overlay](#local-overlay)):

```bash
git clone https://github.com/midagedev/outsource
cd outsource
./install.sh            # user scope: ~/.claude/skills/outsource/
./install.sh --project  # project scope: ./.claude/skills/outsource/
```

You need [Claude Code](https://claude.com/claude-code) plus at least one backend: an authenticated `grok` CLI, a z.ai coding-plan key, and/or an authenticated `opencode` CLI (`opencode auth login` for OpenRouter).

**If you already set up z.ai** — with `npx @z_ai/coding-helper`, or the `crush` CLI — there is nothing to do. Your key is found where those tools put it.

**Otherwise**, set it once. This prompts, verifies the key against z.ai before storing it, and writes `~/.config/outsource/credentials` at mode 0600:

```bash
~/.claude/skills/outsource/bin/setup-key.sh zai
```

`export ZAI_API_KEY=…` beats both. `bin/credential.sh` is the single owner of that resolution — and of which host your account lives on, so a mainland-China plan reaches `open.bigmodel.cn` instead of 401ing against the global one. The launcher never prompts: it runs headless in the background, where a prompt would hang a round instead of failing it. The `crush` CLI itself is only needed for `--harness crush`.

> **Referral link:** https://z.ai/subscribe?ic=P7NR6BGEGL — 10% off for you, credit for this project. Optional; every other z.ai link here is the plain https://z.ai/subscribe.

**Updating.** Marketplace: `/plugin marketplace update outsource`, then `claude plugin update outsource`. Script installs: `git pull && ./install.sh` — a checksum manifest lets unmodified installs upgrade without flags; hand-edited installs need `--force` (`references/local-overlay.md` always survives).

## Use

Say **"run this via glm"** or **"run this via grok"** in any Claude Code session, or invoke `/outsource`. Claude then:

1. writes a **self-contained spec** — file paths, numeric contracts, verification commands,
2. **lints the spec and checks the plan's quota** before spending anything,
3. launches the backend **headless in the background**,
4. **reviews the result like a lead**: reads the diff itself, re-runs the gates cold, walks a checklist of the places delegated reports actually leak.

The core principle: **the delegate is an executor of tight specs.** It has zero conversation context, so every delegation stands alone — and it is never asked for taste judgments, only numeric contracts.

## Seeing the rounds you launched

A delegated round runs in the background, and between "I launched it" and "it reported" it is invisible. Every launch therefore registers itself, and `bin/runs.sh` reads the registry back:

```
$ bin/runs.sh
STATE    LABEL            PROV   HARNESS  ELAPSED  SPEC
running  api-migration    zai    crush       12m   /tmp/sp/spec-api.md
running  test-backfill    zai    cc           4m   /tmp/sp/spec-tests.md
orphan   docs-sweep       xai    crush     1h07m   /tmp/sp/spec-docs.md
         started but never finished — pid 48120 is gone; log=/tmp/sp/docs.log
```

`orphan` is the reason this exists. A round that was killed, or died when the machine slept, leaves *no process at all* — so a `ps` grep reports the same nothing for "finished cleanly" and "died an hour ago holding your worktree". Started-but-never-finished is a state only a written record can hold. `bin/runs.sh json` is the same data for scripts.

### A long round is not a stuck round

Neither harness can stop itself — `crush run` exposes no turn or time limit in its flag set at all, and this `claude` CLI has no `--max-turns`, only `--max-budget-usd` at Anthropic's prices, which says nothing about a z.ai plan. The tempting fix is a time limit. Measured across ten delivered rounds, it is the wrong one: they ran 13 minutes to **1h50m**, with duration tracking message count almost linearly (66 messages / 13m … 848 messages / 1h50m). Long rounds were long because there was a lot of work. A time limit truncates those and still misses a round that wedged at minute three.

So the registry measures **output, not duration**. The GLM harnesses write continuously into their own data directory; opencode flushes JSONL into `--log` per event. `runs.sh` reports an `IDLE` column and flags `⏳` only when a running round has written nothing for ten minutes (`OUTSOURCE_RUN_STALL`):

```
▶refshot zai·crush 1h41m        # 101 minutes in, wrote a second ago — leave it alone
⏳frozen  zai·crush 22m ⋯14m     # silent for 14 of its 22 minutes — go read the log
```

Those two are the real discrimination: an elapsed-time rule would have flagged the healthy 101-minute round and said nothing about the wedged one. The `--log` file is *not* the signal for claude-code — that harness writes it once, at the end, so a perfectly healthy round shows an empty log for its entire life; the trail is `data/crush.db-wal` and `data/logs/crush.log` for crush, `claude/projects/**.jsonl` for the claude-code harness. opencode is the exception: `--format json` flushes one event at a time onto `--log` while the process is still running, so that file *is* the trail.

A stall is a reason to read the log, not to kill anything. `bin/outsource-run.sh --max-seconds N` does hard-kill at N seconds (SIGTERM then SIGKILL to the whole process group, exit 124 in both the sentinel and the registry, session id still recovered so a follow-up can resume) — it has no default and should not get one, because the kill lands mid-edit. Use it only where losing the round is acceptable up front.

`--label` is what the track is **for**, and it is worth typing on every launch, because the listing only earns its keep in parallel and that is exactly when the derived default fails: this skill's documented layout writes every track's spec to `<scratch>/spec.md`, one dir per track, so a basename-derived label would read `spec` three times. The default therefore falls back to the directory holding the spec — usually the track's own scratch dir — and a label that still collides renders as `name`, `name#2`: a warning that the round you are looking at cannot be identified, not a naming scheme.

## Status line

`bin/statusline.sh` puts the registry above, and the plan quotas from [`bin/quota.sh`](#guardrails), into Claude Code's status line — the budgets that stop this session, the ones that stop the next round, and what is running right now:

```
opus │ you@example.com │ CTX 12% │ 5H 8%/3h20m │ 1W 38%/4d2h
z.ai 29%/6d4h │ grok 98%/2h19m │ 🛠2 ▶api zai·crush 12m  ▶tests zai·cc 4m │ repo (main)
```

Add it to `~/.claude/settings.json`:

```json
"statusLine": {
  "type": "command",
  "command": "bash ~/.claude/skills/outsource/bin/statusline.sh"
}
```

Every budget is one token — `NAME used%/until-it-resets`. The percentage says how much is gone; the second half says how long until it comes back. Neither is actionable alone, which is why there is no bar here: a bar spends thirty columns on the first half and cannot render the second at all. Colour carries the alarm instead (green under 50, yellow under 80, red at 80+), and `grok 98%/2h19m` reads at a glance as *nearly out, but not for long*.

It costs about 120 ms per render because it never calls a quota API on the render path: those take one to two seconds, so a lock-guarded background refresh writes a small cache every `OUTSOURCE_STATUSLINE_TTL` seconds (default 180) and a burst of renders makes one fetch.

**Silence means exactly one thing: this backend is not set up here.** Everything else has its own mark, so an absent segment is never ambiguous — a number not measured yet shows `…`, and a measurement that can no longer be refreshed is carried forward prefixed `~` rather than erased. That last rule was written after shipping without it: an expired `grok` sign-in made the whole segment vanish, reporting a backend that had just stopped working exactly like one that was never configured. Nothing is ever silently rendered as `0%`.

**The rounds shown are this session's.** The registry is machine-wide on purpose — an orphan has to be findable from wherever you are — but a status line reports on *your* window, and two Claude Code windows open on two repos would otherwise narrate each other's work as if it were yours. So the store stays global and the filter lives at the reading end: each launch records the session that owns it, and each status line asks only for its own. Ownership is matched on both the session id and the Claude Code process, so a round an in-process teammate launched still counts as yours. `runs.sh` unfiltered still shows the whole machine with an `OWNER` column, which is where you look when something is missing; `OUTSOURCE_STATUSLINE_SCOPE=all` puts that view back in the status line.

Set `OUTSOURCE_STATUSLINE_PROVIDERS=""` to drop the quota row entirely, or e.g. `"zai"` to keep one. No runtime dependencies: the tools are one static Go binary.

## Telemetry — local only

Every tool call records one line: which tool, its exit code, how long it took, and
which flag *names* were passed. Nothing leaves the machine — there is no endpoint,
no upload and no identifier. `OUTSOURCE_TELEMETRY=0` turns it off.

```
$ bin/outsource telemetry --since 7d
TOOL            CALLS   FAIL   RATE      p50      p95
outsource-run      31      4    13%    11m04s   1h22m
guard            —(blocks only)
runs              210      0     0%      4ms      9ms

failures by kind
    3 x guard          exit 2    a delegate tried a git/gh command it is not allowed
    2 x outsource-run  exit 72   the round ran and its completion marker never appeared
    1 x outsource-run  exit 65   a spec that needs eyes was sent to a backend that has none
```

The point is the second table. Each of those exit codes names a way a *launch* was
wrong rather than a way the provider failed, so a rate on one is a finding about
how you are running rounds: 64s mean flags are being guessed, 65s mean vision work
is going to a blind backend, 72s mean completion markers are not being written into
specs, and the guard count says which delegates keep trying to do the lead's job.

**What is never recorded:** flag values, paths, spec text, stdin, environment, or
any credential. Flag *names* are the signal; what they pointed at is not. The only
values kept are three closed enums this repo defines — harness, provider, git
profile. The guard records which *kind* of command was blocked, never the command.
Two tests assert this, one of them by planting a codename in a path, a spec, a
label and a marker and then failing if any of it appears in the file.

The file lives beside the run registry, is mode 0600, and rolls at 2MB keeping one
generation.

## Verifying the binary

`bin/outsource` is committed as a prebuilt binary, because neither install path has
a build step. If you would rather not run bytes you did not build, you do not have
to: the source is in this repository and the build is reproducible.

```bash
CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags="-s -w" \
  -o /tmp/outsource ./cmd/outsource
shasum -a 256 /tmp/outsource skills/outsource/bin/outsource   # the two must match
```

Every flag there is load-bearing. `-trimpath` keeps build paths out of the
artifact; `CGO_ENABLED=0` makes it static and stops the host toolchain from
mattering; and `-buildvcs=false` stops Go stamping the commit hash and a `+dirty`
marker into the module version, which would otherwise change the bytes on every
commit and make this comparison impossible. `tests/reproducible-build.test.sh`
runs exactly this and fails if the committed binary does not match its source, so
a source edit that forgot `./build.sh` cannot ship. `./build.sh --all`
cross-compiles every shipped platform from one machine; Go's linker ad-hoc signs
darwin/arm64, which is what lets a cross-compiled macOS build execute at all.

## The three models

Fourteen rounds, five real tickets from a Go + Svelte product, each ticket sent to every arm as the **same task spec** in its own git worktree.

**All fourteen passed `build` / `vet` / the affected suite when the lead re-ran the gates himself.** That is the finding that matters most: on ordinary work, the gates do not separate these models. Everything below lives outside the gates.

| | **Opus 5** | **grok-4.6** | **GLM-5.3** |
|---|---|---|---|
| Wrong *fact* in the spec | catches it | catches it | catches it |
| Contract that **cannot be satisfied** | refuses, argues why, redesigns | **notices, then implements it anyway** | doesn't notice |
| Evidence it cannot obtain | names what's undecidable, flags a partial miss | — | produces a plausible-looking answer instead |
| Second-order effects | finds them unprompted | missed one the GLM arm caught | caught one grok missed |
| Reads images | yes | **yes — the only cheap arm that does** | **no, at all** (`supports_attachments: false`) |
| Self-verification / disclosure | unprompted | with the preamble | **only with the preamble** |
| Relative cost | highest | subscription | **lowest** |

Two of those cells decide the routing. **GLM cannot see pixels**, so vision verdicts and image generation go to grok. **Neither cheap arm reliably stops at an impossible contract**, so that judgment stays with the lead — or with a Claude agent when the round is design-weight.

### How we found out

The method is the point, because "which model is better" is not answerable without one.

- **Same spec, isolated worktrees.** Every arm gets a byte-identical task spec and its own `git worktree`, so nothing is confounded by phrasing or by arms colliding.
- **The lead re-runs every gate.** A delegate's green is not evidence. When all fourteen came back green under the lead's own runs, the comparison moved to the reports and the diffs.
- **Seeded spec defects.** Specs carried the reporter's hypothesis plus one line: *don't trust this diagnosis — confirm or refute it with an intervention.* One spec also happened to contain two clauses that cannot both hold; that accident turned out to be the sharpest discriminator in the series.
- **A/B with one variable.** The preamble question was answered by sending the same spec to the same model on the same harness, with the preamble and without it.

<details>
<summary><b>The intervention experiment</b> — why "I changed it and the symptom went away" is not a diagnosis</summary>

A sync tick spent **19.4 s of 21.4 s** re-reading 71 unchanged Confluence pages. The spec carried the reporter's hypothesis: *"the watermark window never narrows on a quiet tick."*

| | Manipulation | Result | What it settles |
|---|---|---|---|
| **Opus 5** | **removed** the window slack (`overlap 5min → 0`) | still **6/6 bodies** re-read | the slack is **not** the cause — no constant can close this |
| **grok-4.6** | pushed the watermark **1 h past** every page, so the query matched nothing | **0 body fetches** | an empty match is already free — "stalled floor → full backfill" is false |

Opposite manipulations, same refutation. The cause was neither: there was simply **no decision between a search hit and the body fetch**, and minute-granularity CQL re-matches the same cluster forever. Both arms then built the same shape of fix — one owner for "does this page need its body?" — instead of tuning a constant.

The asymmetry is why that line is in the spec at all: making a suspected cause *false* and watching the symptom **stay** refutes it. Watching a symptom disappear proves nothing, because it may only be masked.

(The 6/6 is the test fixture; the 71 pages is the production measurement.)

</details>

<details>
<summary><b>The contract that could not be satisfied</b> — the one place the arms genuinely split</summary>

One ticket asked for two things at once: treat a file carrying `name: gadak` as ours and overwrite it, **and** keep protecting files the user authored. A user who customises our skill keeps that line — it is what makes the skill load — so the two clauses are jointly unsatisfiable.

- **grok** noticed and wrote it down — *"a user who customized the body but left `name: gadak` is treated as ours and overwritten"* — and implemented it as specified anyway.
- **Both GLM arms** implemented it without noticing.
- **Opus** refused, argued why, and designed around it: an install receipt with a content hash, plus a deliberately **frozen** digest table for pre-receipt installs, with a test asserting it stays frozen.

Three of four arms shipped a data-loss bug **with every gate green**. That is the failure mode this skill now spends the most effort on: not a red gate, but a green one with a gap nobody named.

The same shape repeated on an auth ticket whose contract asked to distinguish three failure cases the available evidence cannot distinguish. All three arms reached that conclusion; only Opus said so, classifying the one case that *is* decidable and flagging the rest as a partial miss. The GLM arms concatenated every hint into every error — satisfying the letter of "show the user a string" while quietly failing its point.

</details>

<details>
<summary><b>Does the preamble earn its length?</b> — same five tickets, GLM-5.3, full preamble vs none</summary>

| Ticket | output tokens, none ÷ full | input tokens, full → none |
|---|---|---|
| new CLI verb | 0.63× | 298k → 139k |
| sync perf | 0.78× | 146k → 112k |
| UI ordering | 0.84× | 107k → 95k |
| auth onboarding | 0.86× | 166k → 96k |
| CLI upgrade | 0.94× | 81k → 78k |

Cheaper in all five, fewer turns in four, **never worse on a gate**. On the sync bug the no-preamble arm even caught a second-order trap the grok arm missed (a page skipped by the new gate must still be reachable by the comments-only pass) and wrote the regression test for it.

But the cost was not where the preamble was earning its keep:

| | full preamble | no preamble |
|---|---|---|
| FAIL-first evidence | 5/5 | **5/5** |
| self-verification section | 5/5 | **0/5** |
| "what I could not do" | 5/5 | **1/5** |

FAIL-first survives without the preamble because the *task spec* demands it. What disappears is disclosure — which is exactly how the auth round came back looking complete when it wasn't.

**Not established, and not claimed:** which individual sections of the full preamble are dead weight. Only all-or-nothing was measured.

</details>

### How each weakness was closed

Every row is a mechanism with an exit code, not advice in a document.

| Weakness found | What now stops it |
|---|---|
| GLM cannot see images, but a spec might hand it a screenshot | **Vision guard, exit 65** — driven by the provider table's capability column, never a provider-name test at the call site. `--no-vision-check` overrides. |
| z.ai silently answers an unqualified `claude-*` request as its plan default | **Model-identity assertion, exit 70** — read from the per-turn `message.model` in the session transcript. *Not* from `modelUsage`, which was measured to echo the **requested** id and so can never prove a match. No transcript means "unverifiable", which also fails. |
| A cheap arm doesn't stop at an unsatisfiable contract | **A lead checklist item, before launch.** The delegate-side rule for this already existed in the preamble and did **not** fire, so it moved to the lead rather than becoming more prose. |
| Without the preamble, disclosure vanishes | **`references/spec-preamble-core.md`** — the short substitute carrying back exactly the half that vanished, and nothing else. |
| Specs carry wrong premises (five in one session — a nonexistent tool, a nonexistent column, an absent fixture, a wrong runner cwd, a wrong manifest path) | **`bin/spec-lint.sh`**, before launch: every `path:line` citation and path-shaped reference resolved, exit 1 on a miss. Bare filenames only when they carry a `:line`; anything resolving under any plausible base is not flagged, because a linter people ignore is worse than none. |
| A negative premise ("this file does not exist") that a linter can't check | **Lead checklist: verify absence one path at a time.** A two-pattern `ls` in zsh printed nothing because the *second* glob matched nothing and aborted the command — so a file that exists went into a spec as absent. |
| grok blocked from producing its own required evidence | **Per-subcommand git denies.** A blanket `git worktree*` also blocked `git worktree list`, which every spec asks for as the first line of the report. |
| The plan runs dry mid-round | **`--require-quota N`, exit 66** — keyed on the **tightest** window, not the shortest (measured: weekly at 81.7% remaining while the 5-hour sat at 83.8%). Fails closed. |
| A delegate reports "done" that isn't | **Completion sentinel `<log>.rc`** with `rc`, `finished`, `harness`, `provider`, `model_requested`, `model_actual`, `session`. The harness's own lifecycle is not completion proof. |
| A clean exit without the spec's completion marker | **`--done-marker`, exit 72** on both launchers. Was 70 on grok (colliding with model-identity) and a silent rc=0 on GLM, so the same fact read as failed or completed depending on the sister. 72 names the missing marker; the tree is still the verdict. |
| Repository-state git from a delegate | **`bin/git-guard.sh`**, a `PreToolUse` hook parsing the real command string — `git -C … commit`, `env … git push`, `sudo git …`, chained mutations all blocked; read-only git deliberately open. One file, both harnesses' calling conventions. |

<details>
<summary><b>Earlier series</b> — 9 blind-judged grok rounds, and three shipped GLM rounds + an A/B</summary>

**grok-4.6, nine blind-judged rounds vs Opus 5 / Fable 5.** Each round: implement a module *and author its own verification gate* in a Three.js/WebGPU project, judged blind with labels swapped.

- Baseline: clear loss, 0:5 — grok wrote 14 test assertions where Opus wrote 24.
- With the quality bundle the gap closed where it matters: assertion depth 14 → 42 → 67 → **81**; the visual axis flipped to grok in the last two rounds; grok ran 2–4× faster throughout.
- What stayed hard: design-weight logic cores (state machines, serialization) stayed with the Claude side all three times tested.
- Best finding: grok's blind losses were mostly **missing defaults, not missing capability** — and defaults can be written into a spec.

| Exp | Task | Device added | Verdict | Measured |
|---|---|---|---|---|
| E1 | hit ripple + rim shader with a numeric gate | — (baseline) | lost 0:5 | assertions 14 vs 24 |
| E2 | debris burst + flash timing | — (replication) | lost | assertions 12 vs 32; 1.5× faster |
| E3 | 3-plane parallax cloud billboards | fairness + self visual verification | lost — didn't read as clouds | failure traced to the checklist |
| E5 | exposure flash + hitstop | contract↔assertion mapping table | **won 2:0:1** | assertions 10 → 42 |
| E6 | near-miss graze sparks | reference-image injection (A/B) | **injection rejected 4:0:1** | references help only same-effect |
| E7 | timeScale state machine + cel clock motif | quantified depth, self-review | **split: visual won**, logic lost | assertions 67 vs 95; 2.2× faster |
| E8 | camera FOV ladder | v2, vs Fable 5 | split | Fable caught a defect in *our own spec* |
| E9 | QTE hit windows + combo state machine | 4 logic design principles | lost; design credited | assertions 81; fastest run |

**GLM-5.3, three shipped solo rounds and a same-spec A/B vs an Opus subagent.** A CLI/MCP warning feature, a store-level schema-divergence repair with a FAIL-first test, and a docs-contract CI gate — each landed on main after lead review with no rework, and GLM corrected five wrong premises in the lead's own specs along the way. In the A/B (N=3) both arms independently chose the same shared utility and independently invented the same AST-based test workaround; the Opus arm won all three artifact picks, on second-order state interactions, evidence strength, and naming the failure modes a script's options handle. Each of those three became a spec rule here.

</details>

## Guardrails

**Before launch**

```bash
bin/spec-lint.sh --root <repo> <scratch>/spec.md     # 0 clean · 1 findings
bin/outsource-run.sh --require-quota 15 …            # 66 if the plan is too low
```

**After the round** — the model-identity assertion (exit 70), the done-marker check (exit 72 when a clean exit lacks the marker), the completion sentinel, and a cost line carrying the round's token counts from the log's `usage`. The `total_cost_usd` beside them is Anthropic-priced and wrong for every provider here.

Plan credits are deliberately **not** reported per round: a plan quota is a plan-wide counter that concurrent rounds and other sessions move too, so a before/after delta around one round measures the machine, not the round. Quota is a pre-flight signal — which provider this session should use, and whether to start at all.

```
$ bin/quota.sh
z.ai coding plan: level max — GLM Coding Max (status VALID, valid 2026-08-15~09-15)
5h window: 6692/28000 consumed, 21307 remaining, 23% used / 76.1% left, resets at 12:24 (in 3h 46m)  <- tightest
1w window: 27758/140000 consumed, 112241 remaining, 19% used / 80.2% left, resets at 17:52 (in 153h 14m)

$ bin/quota.sh --provider grok
1w window: exact counts not exposed by this API, 98.0% used / 2.0% left, resets at 15:13 (in 6h 36m)
```

## What's inside

| File | Purpose |
|---|---|
| `skills/outsource/SKILL.md` | The router: backend table, spec assembly, lead review checklist |
| `references/grok.md` · `references/glm.md` | Per-backend operating manuals: flags, git-safety profiles, harness quirks, measured behavior |
| `references/spec-preamble.md` | Shared rules prepended to every spec — every clause from a real incident |
| `references/spec-preamble-core.md` | The short substitute: the disclosure half, measured to vanish without it |
| `references/glm-preamble.md` | GLM runtime delta (no images, hooks not flags, evidence rules) |
| `references/spec-authoring.md` · `references/spec-template.md` | The quality bundle, and the per-task spec skeleton |
| `bin/outsource` | **One Go binary is every tool below.** The `bin/*.sh` names beside it are three-line compatibility shims that exec into it — kept because docs, hooks, installed copies and tests all call these tools by path. Invoke `outsource <tool>` directly to save a fork |
| `outsource-run` | The launcher: provider table, harness picker, isolated config per track, session resume, vision/quota guards, model-identity assertion, completion sentinel |
| `grok-run` | The grok launcher: same registry entry, sentinel and done-marker verdict, the git-profile flag strings it owns, and a startup proof |
| `guard` | The git-ban `PreToolUse` hook, one implementation for both harnesses (54 regression cases + a 670-verdict golden) |
| `credential` · `setup-key.sh` | The single owner of key *and* host resolution, and its interactive half. `setup-key.sh` stays shell on purpose — its whole job is TTY interaction, and `tests/shell-boundary.test.sh` enforces that boundary |
| `verify-key` | Checks a key before it is stored; the key arrives on stdin, never in argv |
| `spec-lint` · `quota` | Pre-launch spec check; plan quota with `--require-window` as a gate |
| `runs` | The run registry: which rounds are alive, on what, for how long — and which started and never finished |
| `last-report` | The round's final report out of either log shape, exit 65 when there is none |
| `statusline` | A Claude Code status line: session budgets, plan quotas, live rounds — 7ms per render |
| `telemetry` | A local record of tool calls, exit codes and reasons, and a summary of them. Local only, never uploaded, `OUTSOURCE_TELEMETRY=0` to disable |
| `scripts/grok-progress.py` | Compress a grok NDJSON stream into one-line progress events (lead-side; not installed) |

## The quality bundle

What closed the measured quality gap, each device with an effect behind it:

1. **Contract↔assertion mapping table** + FAIL-first evidence — quadrupled self-authored test depth on its own
2. **Quantified depth** — never "be thorough"; instead "≥2 assertions per contract clause, coverage table"
3. **Self-review pass** — "list 3 defect classes you may have missed; assert or justify"
4. **Visual self-verification** — the implementer opens its own captures; item #1 is always *identity legibility*
5. **Logic design principles** — derive-don't-store · re-normalize on load · 3-class input defense
6. **Evidence rules** — verify from a cold start and compare test counts with CI; every number carries the command that produced it; the recurrence layer lands as a file, not a sentence

## Local overlays

Two layers, most specific last:

- **User overlay** — `references/local-overlay.md` next to the installed skill. Only what is true for you on every repo (default backend, model flags). Preserved by `install.sh` across upgrades, never shipped by this repo.
- **Project overlay** — `<repo>/.outsource/overlay.md`, checked into the target repo. Base branch, house gate recipes, incident history — the facts that version with the code they describe.

Both are applied automatically and included in spec assembly, user first, project second.

## Known limits

- Exploratory problems that can't be specced aren't delegation material — the lead narrows first.
- Design-weight logic didn't fully close even with bundle v3; write those with Claude, review with a backend.
- GLM-5.3 cannot read images, full stop — and, measured, it says so instead of guessing.
- Neither cheap arm reliably stops at a contract it cannot satisfy. That check is the lead's.
- Plan quota is readable for the two subscription backends only; pay-per-token API keys expose no window to gate on.
- Claude Code only for now. The SKILL.md format is portable, but we publish only what we've verified end to end.

## License

MIT
