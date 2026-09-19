# GLM backend — z.ai coding plan, on either harness

The model is the point; **the harness is just how it is driven headlessly**,
and `bin/outsource-run.sh --harness` picks one. Division of labor is unchanged:
the lead writes specs, reviews diffs, runs gates, commits; GLM burns the
implementation tokens, which are close to free on a z.ai coding plan.

## Which model (measured 2026-08-27 against api.z.ai/api/anthropic)

| Model | Endpoint behavior | Vision | Route to it |
|---|---|---|---|
| **glm-5.3** (default) | honoured verbatim | **blind** (shape probe: answered "Y" to a white 7) | implementation, gate authoring, investigation, reports — every spec-able round where per-round intelligence matters |
| **glm-5.3-flash** | honoured verbatim | **sees pixels** — "7" on the shape probe; through the claude-code harness's Read tool it named a solid `#1E50DC` fill as `#2244DD` (~5%/channel). This is the officially unveiled identity of what OpenRouter listed as the stealth `ox-alpha`, which stopped serving 2026-09-10 — route it here, on the plan | mechanical edits, format conversions, large fan-out where 5.3 quota is the constraint (**3× the usable plan quota** at the same tier, vendor-stated), and **capture self-verification inside an implementation round** — the delegate can finally open its own screenshot. Precise color/luminance and aesthetic verdicts stay with a frontier vision judge until A/B-measured. Benched 2026-08-27 (3 tasks, identical specs, all objective gates perfect): flash was **slower than 5.3 on every task** (80s vs 74s, 166s vs 45s, 191s vs 159s) — its value is quota and eyes, not speed |
| glm-4.6 | honoured verbatim | not probed | legacy pin only |
| glm-5.2 | **silently answered by glm-5.3** — the response `model` field differed from the request, measured twice 2026-08-27. **Re-measured 2026-09-20: it now echoes `glm-5.2` back**, so the tell is gone and what answers it cannot be checked at all — a stronger reason to refuse, not a weaker one | — | never — the launcher refuses it at launch (exit 70; `OUTSOURCE_ALLOW_MAPPED_MODEL=1` exists only to re-measure) |
| anything else | nonexistent ids error loudly (`[1211][Unknown Model]`, re-measured 2026-09-20 — 1214 was the older code). Anthropic ids are **accepted and echoed**: `claude-opus-5` and `claude-sonnet-4-5-20250929` each came back naming themselves, where 2026-08-16 they mapped visibly to the plan default. See the re-measurement below | — | — |

The vision guard is per-model: a spec that names an image launches on
`--model glm-5.3-flash` without `--no-vision-check`, and is still refused on
the blind default. Identity assertion covers flash like any other id
(claude-code harness; measured: `model_actual=glm-5.3-flash`). Vendor
benchmark framing: flash's published deltas (DeepSWE 63.4, Terminal Bench
84.3) are vs **glm-5.2**, not vs 5.3 — treat 5.3 as the stronger coder until
an A/B on our own specs says otherwise.

| Harness | How | Pick it when |
|---|---|---|
| **claude-code** (default) | `claude -p` against z.ai's Anthropic-compatible endpoint | you want Claude Code's tooling — CLAUDE.md injection, mature headless mode, hook/permission system, `--resume` |
| **crush** | the crush CLI with an isolated `CRUSH_GLOBAL_CONFIG` | you want a second, independent process family (parallel headroom), or claude-code is unavailable |

Both attach the same `bin/git-guard.sh` and take the same specs; the guard
reads the command either from `$CRUSH_TOOL_INPUT_COMMAND` (crush) or from
hook JSON on stdin (claude-code).

**Context files — what a spec must carry differs by harness.** On
claude-code the CLI itself injects the target repo's **root CLAUDE.md,
CLAUDE.md files on the ancestor path of `--cwd`, and `.claude/rules/*`** —
so a glm round on this harness does NOT need those pasted into the spec;
list only the **nested** per-directory CLAUDE.md files covering the edit
targets, which inject zero times on any claude-shaped CLI (field-measured
2026-08-14). The user-scope memory comes from the round's isolated
`CLAUDE_CONFIG_DIR`, not your `~/.claude` — the lead's private CLAUDE.md
does not ride along, by construction. On **crush** no such injection is
established here: assume nothing is injected and keep the repo contract in
the spec (or point the spec at the file by absolute path). The same
assume-nothing rule goes for opencode and agy.

**z.ai's model mapping is a trap, and the launcher closes it.** Measured
2026-08-16 against `https://api.z.ai/api/anthropic`: a request for
`claude-opus-5` comes back as **glm-4.7** (the plan default), while
`glm-4.6`/`glm-5.3` are honoured verbatim. So the harness must pin
`ANTHROPIC_MODEL` — otherwise you believe you ran one model and actually ran
another.

> **The trap got quieter, not smaller (re-measured 2026-09-20).** That
> mismatch no longer reproduces, and the reason is worse than a fix: the
> endpoint now **echoes whatever id it is given**. A direct `curl` at
> `/v1/messages` asking for `claude-opus-5`, `claude-sonnet-4-5-20250929`,
> `glm-5.2`, `glm-5.3` and `glm-5.3-flash` got each id back naming itself,
> and only an invented id (`bogus-model-xyz`) was refused —
> `[1211][Unknown Model]`. Through the CLI the same thing: a `-p` round with
> no `ANTHROPIC_MODEL`, against a config dir whose `settings.json` says
> `"model": "opus"`, produced a per-turn `message.model` of `claude-opus-5`.
>
> So the response id has stopped discriminating, and a wrong model now passes
> silently where it used to fail loudly. Two consequences, and neither changes
> what the launcher does. **Pinning is more necessary, not less** — it is now
> the only thing standing between a round and a backend nobody chose. And the
> identity assertion's strength is narrower than it reads: it still catches an
> id the endpoint rejects or rewrites, but it can no longer prove that a
> `claude-*` id was not quietly served by something else. That is why the
> launcher's provider table only ever requests `glm-*` ids and refuses
> `glm-5.2` statically rather than relying on the reply to give it away.

The launcher now asserts this per round and **fails the round with exit 70**
on a mismatch, even when the run itself succeeded. Where the evidence comes
from matters: `modelUsage` in the JSON log was measured to echo the
**requested** id (a run that asked for `claude-opus-5` and was answered by
glm-4.7 still logged `modelUsage {"claude-opus-5": …}`), so a match there
proves nothing. The model that actually answered is the per-turn
`message.model` in the session transcript under the isolated config dir.
No transcript means *unverifiable*, which also exits 70 — a `modelUsage`
match is never accepted as a pass.

Also measured: `ANTHROPIC_BASE_URL`/`ANTHROPIC_AUTH_TOKEN` are genuinely
honoured by `claude -p` (an invalid token 401s, so a green run really did go
to z.ai, not to your own subscription), `CLAUDE_CONFIG_DIR` isolates the run
from the user's own Claude Code, and `total_cost_usd` in the log is Claude
Code's Anthropic-priced estimate — **not** what the z.ai plan charges. The
per-round figure that is actually attributable is the token count in
`usage`, which the launcher prints. Plan credits are not per-round: the
quota is a plan-wide counter that concurrent rounds and other sessions move
too, so `bin/quota.sh` is a **pre-flight** signal (`--require-quota`), not
an accounting mechanism.

The shared implementer preamble (`references/spec-preamble.md`) carries the
backend-agnostic rules; `references/glm-preamble.md` is the GLM-runtime
delta. Assemble both in front of every task spec.

**Hard limit (measured, per model):** the default **glm-5.3 cannot see
images** (shape probe failed; the old `supports_attachments: false` reading
holds for it). **glm-5.3-flash can** — see the model table above — which
restores the implementer's capture self-verification when a visual round
runs on flash. The judgment bar is unchanged: aesthetic/style verdicts and
precise color calls still go to a vision-capable frontier agent (we use an
Opus subagent) until flash is A/B-measured on verdict quality. GLM-5.3 keeps
doing the numeric half of visual work: pixel-decoding scripts, capture
harness wiring, gate authoring.

## Invocation

```bash
SP=<scratch-dir>
cat ~/.claude/skills/outsource/references/spec-preamble.md \
    ~/.claude/skills/outsource/references/glm-preamble.md \
    $SP/task.md > $SP/spec.md

~/.claude/skills/outsource/bin/outsource-run.sh --detach \
  --cwd /absolute/path/to/worktree --spec $SP/spec.md \
  --label <what-this-track-is-for> \
  --config-dir $SP/glm-cfg-<track> --log $SP/glm-<track>.log
# add --harness crush to run the same spec on the other harness
```

`--detach` is the same re-exec-into-own-session as `grok-run.sh`: usage
errors still fail on your terminal, then the round survives the caller's
process group. A foreground launch whose stdin is not a TTY is refused
(exit 64) — that shape lost 6 rounds to an external 30-min killer
(2026-08-22). Pass `--foreground` only for tests or a deliberate block.

`--label` is the track's purpose, and it is worth typing every time: with
one spec file per scratch dir — the layout above — the derived default is
the same word for every parallel round, which is exactly when you need to
tell them apart. `api-migration`, `test-backfill`, `docs-sweep`; not
`track-a`.

`--detach` returns immediately; the child's stdout is discarded (completion
evidence is `<log>.rc`, session id is in the sentinel). Do not also wrap it
in a harness `run_in_background` task — that is the shape the non-TTY
refusal closes. For a blocking wait on a TTY, omit `--detach`. A foreground
run's last stdout line is `SESSION <id>` — pass it back with `--session
<id>` for a follow-up in the same context (keep that rare; a fresh round
with a summarized spec is usually better).

Flags: `--harness claude-code|crush` (default claude-code, or
`OUTSOURCE_HARNESS`); `--model` — bare id on claude-code (`glm-5.3`),
`provider/id` on crush (`zai/glm-5.3`), or `GLM_DELEGATE_MODEL`;
`--config-dir` — one per parallel track; `--provider zai|xai` (default zai,
or `OUTSOURCE_PROVIDER`) selects the provider-table row; `--require-quota N`
refuses to launch below an N% floor on the plan's tightest window (exit 66);
`--no-vision-check` overrides the image-spec refusal (exit 65);
`--allow-agent` (crush only) re-enables sub-agent tools and **weakens the
git ban** (hooks fire only on top-level tool calls) — only for tasks with
zero repository-state risk; `--label <name>` names the track in the run
registry; `--max-seconds N` kills the harness at N seconds (exit 124);
`--done-marker <string>` records in the sentinel whether the transcript
carries the spec's completion marker (`done_marker=found|absent`);
`--effort low|medium|high|xhigh|max` (claude-code only; refused on the
other harnesses, exit 64) passes the CLI's effort level through and records
it in the sentinel (`effort=…`). **z.ai honours it** (measured 2026-09-15,
glm-5.3, three probes per level, a one-word answer: `low` → 3 output tokens
every time, `max` → 113/50/52; the endpoint reports `thinking_tokens: 0` and
folds the thinking into `output_tokens`, so the saving shows there). Unset
means the harness default. Pick it per round, not per session: mechanical
edits, fan-out, format conversions → `low`; ordinary implementation against a
tight spec → `medium`; multi-file contracts, gate authoring, cause narrowing
→ `high`; `max` only when a lower level has demonstrably failed on the same
spec — a round that ran everything at `max` is the pattern this flag exists
to end;
`--detach` re-execs into its own session; `--foreground` opts out of the
non-TTY refusal. A clean harness exit without the marker is **exit 72**
(same code and same-intent stderr as `grok-run.sh`). 70 stays the
model-identity assertion.

Pass `--done-marker` whenever the task spec ends with one, because **`rc=0`
only means the harness exited cleanly.** Measured on one day: a round exited
`rc=0` having written no code (it mistook itself for the lead — see
preamble §0), and another exited `rc=0` with no edits because the spec's own
precondition check correctly told it to stop. The first is a lost round, the
second is a good one; 72 names only the missing marker. The tree still
separates those two without opening the transcript.

**Rounds run long, and that is usually fine.** Measured on ten delivered
rounds: 13 minutes to **1h50m**, duration tracking message count almost
linearly (66 messages / 13m … 848 messages / 1h50m). Neither harness can
stop itself — `crush run` has no turn or time limit in its flag set at all,
and this `claude` CLI has no `--max-turns`, only `--max-budget-usd` at
Anthropic's prices, which says nothing about a z.ai plan — but that is an
argument for watching, not for cutting: a time limit truncates the working
rounds and misses the stuck ones.

`runs.sh` therefore measures **output, not duration**. It flags `⏳` when a
running round has written nothing for ten minutes, reading the trail each
harness leaves in its own `--config-dir`: crush's `data/crush.db-wal` and
`data/logs/crush.log`, the claude-code harness's `claude/projects/**.jsonl` (the round's own file inside it is printed as `trail=` — see below).
Note that the `--log` file is not that trail — the claude-code harness
writes it once, at the end, so a healthy round shows an empty log for its
whole life.

`--max-seconds N` does hard-kill at N seconds (exit 124, whole process
group). It has no default and should not get one: the kill lands mid-edit.
Use it only where losing the round is acceptable up front.

**Mid-round visibility.** Every launch registers itself, so a background
round is visible while it runs and not only once it reports:

```bash
~/.claude/skills/outsource/bin/runs.sh          # state, provider, harness, elapsed
~/.claude/skills/outsource/bin/runs.sh line     # one line, for a status line
~/.claude/skills/outsource/bin/runs.sh json     # for a script
~/.claude/skills/outsource/bin/tail.sh <label>  # what the round said and ran
~/.claude/skills/outsource/bin/tail.sh <label> -f   # follow; ends with the round
```

`tail.sh` renders the live trail one line per turn (`💬` said, `🔧` ran, `✗` a
failed tool call), and on this harness it is the only way to watch a round
work — `--log` is empty until exit. It does not have to be told which file to
read: the round records its own transcript path into the registry on its first
turn (a `SessionStart` hook), `runs.sh` prints it as `trail=`, and the sentinel
keeps it afterwards. Measured 2026-09-15 — before that, finding a running
round's transcript meant guessing the newest `.jsonl` under
`<config-dir>/claude/projects/<cwd-slug>/`, which is the wrong file as soon as
two rounds share a cwd.

The state worth knowing is `orphan`: started, pid gone, no exit code — the
round died without finishing, and nothing else on the machine still
remembers it existed. `bin/statusline.sh` renders the same registry into
Claude Code's status line, next to the plan quotas.

Before launching, lint the assembled spec — wrong premises are the measured
tax on delegation:

```bash
~/.claude/skills/outsource/bin/spec-lint.sh --root <repo> $SP/spec.md
```

## Harness facts — claude-code (measured 2026-08-16)

- The launcher writes `settings.json` into an isolated `CLAUDE_CONFIG_DIR`
  with the git guard as a `PreToolUse` `Bash` hook; a blocked call surfaces
  to the model as `PreToolUse:Bash hook error: … BLOCKED: …` and the round
  continues. Field-verified end to end: file edits land, `git worktree list`
  answers, `git commit -am probe` is blocked, HEAD unchanged.
- Diagnostics go to **stderr**, and one of them (`[claude-code:
  unrecognized_model]`, because `glm-5.3` is not an Anthropic id) will
  corrupt the log if merged — the launcher writes stderr to `<log>.err` and
  keeps the log pure JSON.
- The spec is fed on stdin; `session_id` in the JSON result is what
  `--session` resumes (mapped to `claude -p --resume`).
- `--permission-mode bypassPermissions` is what makes the run non-interactive;
  the git ban is the hook, not the permission mode.

## Harness facts — crush (CLI quirks; do not rediscover)

- `crush run` has no `--yolo`, `--deny`, `--prompt-file`, or `--max-turns`.
  The launcher maps everything to config in a scratch directory named by
  `CRUSH_GLOBAL_CONFIG` (a directory, not a file). The user's
  `~/.config/crush` stays untouched; the API key is read from it at load
  time and never written into our files or logs. Resolution itself belongs to
`bin/credential.sh`: `$ZAI_API_KEY`, then this skill's 0600 store, then
discovery — `~/.chelper/config.yaml` (written by `npx @z_ai/coding-helper`,
the vendor's own installer), a crush config, or the helper's Claude Code
settings (that last one only when its `ANTHROPIC_BASE_URL` is a z.ai host, so
a real Anthropic token can never be lifted). `bin/setup-key.sh zai` is the
interactive half.
- **Two hosts, one plan family.** The coding plan is `api.z.ai` globally and
  `open.bigmodel.cn` in mainland China; the same key 401s against the wrong
  one. `credential.sh <provider> --base-url <default>` is the single owner of
  that choice — it reads the helper's `plan:` field, falls back to the base
  URL in Claude Code's settings, and otherwise returns the provider table's
  default unchanged. `$ZAI_BASE_URL` overrides everything. Both the launcher
  and `quota.sh` (whose monitor endpoints hang off the same host) go through
  it. Measured on the global plan only; the China host is wired, not verified.
- Auto-approval = `permissions allow …`; sub-agents off = `permissions deny
  agent task`; there is no turn cap — rely on the `DONE-<track>` marker.
- The git ban is a `PreToolUse` hook (`git-guard.sh`) that reads the actual
  command string, so `git -C <path> commit`, `env FOO=1 git push`,
  `sudo git …`, and mutations chained after a listing are all blocked.
  Read-only git (`log`/`show`/`diff`/`blame`/`status`/`worktree list`/
  `branch -a`/`remote -v`/`config --get`, `gh pr list|view`) is allowed on
  purpose. Regression-tested at 29 cases; after editing the guard re-run:
  `CRUSH_TOOL_INPUT_COMMAND='git -C /tmp commit -am x' bash ~/.claude/skills/outsource/bin/git-guard.sh </dev/null; echo $?  # 2 = blocked`
  and the stdin form:
  `echo '{"tool_input":{"command":"env FOO=1 git push"}}' | bash ~/.claude/skills/outsource/bin/git-guard.sh; echo $?  # 2`
- The launcher passes `-D <scratch>/data`; without it crush drops a multi-MB
  `.crush/` session DB into the tree it edits (self-gitignored — invisible
  in `git status`, which is why it goes unnoticed).
- The session id is `.meta.id` in `--json` output, not top level.

## How GLM-5.3 behaves (measured on delivered rounds, 2026-08)

The code it returns is usually sound; prose and disclosure are strong — it
reports its own mistakes and corrects wrong spec premises prominently.
Audit the **evidence around the code**, not the style. Failure modes that
actually happened, and the preamble rail that now blocks each:

| Tendency | Rail |
|---|---|
| Trusts a warm environment (claimed 167 green; CI ran 173 and failed — a reused dev server had served the pre-edit bundle) | §6 |
| Reports counts as sentences, without the producing command | §7 |
| Takes benchmark numbers on a busy machine without saying so | §8 |
| Narrates the cause in a commit message, fixes only the symptom | §9 |
| Ships a committed artifact the code can no longer produce, undetected | §10 |
| Flips tickets to done silently; files discovered defects in docs, not the tracker | §11 |

**A/B vs an Opus arm, identical specs (N=3: web UI fix, self-verifying doc,
shell scripts).** Parity on pattern discovery (both arms independently chose
the same shared popover util and invented the same AST-test workaround),
premise correction, FAIL-first, and boundaries. GLM lost all three artifact
picks, each to a specific gap — write the spec against these:

- **Second-order state interactions.** It left two popovers able to occupy
  the same slot and Escape doing two things at once; the Opus arm arbitrated
  both. For UI tasks, the spec must enumerate what else occupies the same
  space/keys and demand an explicit precedence decision.
- **Evidence strength.** It proved claims with greps plus a prose comment
  where a focused `go test -run` existed; the Opus arm ran the tests. For
  verification-carrying docs, the spec says: prefer invoking an existing
  test over a grep, and a comment inside a command block must not carry the
  claim.
- **Silent hazard avoidance.** Its `sh`/`set -eu` choice happened to dodge a
  SIGPIPE failure mode the Opus arm measured and designed around — luck and
  judgment are indistinguishable in review. For robustness-sensitive
  scripts, the spec demands naming the failure modes the chosen options
  handle (empty selection, missing binary, SIGPIPE, cron PATH).

**Standalone caveat:** when the user drives the model directly (no lead, no
hook), nothing enforces lead-only commits — that is how a red main once got
a second unrelated push. A spec for a maybe-standalone task carries three
lines: CI green is the definition of done, not the push; never push onto a
red main; one commit at a time through the gate.


## Sitting in front of it yourself — `bin/glm.sh`

Everything above drives GLM as a delegate. `bin/glm.sh` is the other
direction: your own interactive Claude Code session, on the same z.ai coding
plan, with you at the keyboard.

```bash
glm.sh                           # interactive, glm-5.3
glm.sh --resume                  # every claude flag passes through
GLM_MODEL=glm-5.3-flash glm.sh   # the model that can see pixels
```

Worth an alias, since that is how it gets used:

```zsh
alias glm='~/.claude/skills/outsource/bin/glm.sh'
```

**Six model variables, not one.** `ANTHROPIC_MODEL` covers requests that name
an id, and measured 2026-09-20 it does beat a config dir's own `"model"`
setting. It does not cover the paths that ask by **alias** — the `model`
setting itself, `/model opus` mid-session, a Task subagent asking for sonnet,
the background haiku queries — and z.ai accepts an Anthropic id without
complaint and now echoes it back (see the re-measurement above). So the script
sets `ANTHROPIC_MODEL`, `ANTHROPIC_DEFAULT_{OPUS,SONNET,HAIKU}_MODEL`,
`ANTHROPIC_SMALL_FAST_MODEL` and `CLAUDE_CODE_SUBAGENT_MODEL` to the same id.
Measured, in the same session: with only the opus alias redirected, a request
that would have gone out as `claude-opus-5` came back as `glm-5.3`.

**The config dir is shared with your normal sessions.** No
`CLAUDE_CONFIG_DIR` is set, so your skills, MCP servers, history and
`--resume` all work. The costs are real: your hooks run inside it and
`~/.claude/CLAUDE.md` loads (~32k input tokens before you type). Export
`CLAUDE_CONFIG_DIR` yourself for an isolated session.

**No git guard, deliberately.** A delegate round gets one because nobody is
watching it. Here you are, and the standalone caveat above is the rule that
applies instead. `glm-5.2` is still refused (exit 64): its reply used to name
a different model than the request, and now that the endpoint echoes every id
it accepts, what actually answers it cannot be checked at all.

The key never reaches a command line: `credential.sh zai` resolves it and the
script exports it into its own process, so `ps` never sees it. Sourcing the
script is refused rather than leaking that export into your shell.
`tests/glm-interactive.test.sh` holds all of this.

### A subagent asking for opus gets glm-5.3, and there is no way around it

Measured 2026-09-20, inside a `glm.sh` session: an `Agent` call with
`model: "opus"` produced a subagent whose recorded request was
`"model": "opus"` (its `.meta.json`) and whose per-turn `message.model` was
**`glm-5.3`**. The redirect caught it, which is the point of pinning all six.

What matters is that the redirect is not the only thing standing in the way.
`ANTHROPIC_BASE_URL` is set for the **process**, and a subagent is a request
from that process — so main turns, subagents, sidechains and background
queries all go to z.ai. There is no per-subagent endpoint override. **A
`glm` session cannot reach Anthropic at all**, pinned or not; the pin only
decides whether you find out. Without it the request leaves as
`claude-opus-5`, and z.ai now accepts and echoes that id (see the
re-measurement above), so it would look answered by Opus and not be.

The trap this sets is specific: **glm-5.3 is blind** (model table, top of this
file). A vision judge, a "fall back to opus" round, or any rule that reaches
for a frontier model gets a GLM subagent instead, and a blind one. Orchestrate
from a normal `claude` session and keep `glm` for the hands-on work — or spawn
the judge on `glm-5.3-flash`, which does see pixels, knowing it is not a
frontier verdict.

### What z.ai's own Claude Code page recommends, and what this takes from it

[docs.z.ai/devpack/tool/claude](https://docs.z.ai/devpack/tool/claude) and its
model-switching page describe the same setup from the vendor's side. Checked
2026-09-20:

| Vendor says | Here |
|---|---|
| `ANTHROPIC_DEFAULT_{OPUS,SONNET,HAIKU}_MODEL` in `~/.claude/settings.json` | **Same three variables, opposite scope.** Putting them in `settings.json` makes them global — your Anthropic sessions get them too. This script exports them into its own process, so `glm` and `claude` coexist |
| Those three are the whole model story | **Not enough.** They cover alias paths only; a request naming an id goes straight through. `ANTHROPIC_MODEL`, `ANTHROPIC_SMALL_FAST_MODEL` and `CLAUDE_CODE_SUBAGENT_MODEL` close the rest — measured above |
| Default the aliases to `GLM-5.3-Flash` | **`glm-5.3`**, deliberately. Flash's measured value is quota and eyes, not strength (model table at the top); `GLM_MODEL=glm-5.3-flash` when you want it |
| `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1` | **Taken.** A session answered by z.ai has no business reporting to Anthropic. The one item from that page this script adopts |
| `API_TIMEOUT_MS=3000000` | **Not taken.** Nothing in this repo's history is a client timeout, and the variable appears nowhere in it. A 50-minute ceiling turns a hung request into a 50-minute hang; a human is present here and `--max-seconds` covers the headless side. Adopt it if a round is ever measured dying on one |
| `glm-5.3-flash[1m]` plus `CLAUDE_CODE_AUTO_COMPACT_WINDOW=1000000` for 1M context | **Unreachable on this account.** Measured 2026-09-20: `glm-5.3-flash[1m]` and `glm-5.3[1m]` are both refused `[1211][Unknown Model]` — on `api.z.ai/api/anthropic` *and* on the coding-plan `api/coding/paas/v4`, while bare `glm-5.3-flash` answers on both. Our measured `contextWindow` stays 200000, so the compact window is moot. Re-probe if the plan tier changes |
