# The opencode harness — OpenRouter and OpenCode Zen

The model is the point; **the harness is just how it is driven headlessly**.
The `opencode` CLI drives two providers here. Zen defaults to it (Zen's free
tier answers no other client), so `--harness opencode` can be omitted there.
OpenRouter defaults to the claude-code harness since 0.21.0 — its
Anthropic-compatible endpoint works, and a provider that has one runs on
claude-code (`references/openrouter.md`) — so on OpenRouter this harness is
opt-in: `--harness opencode`. Division of labor is unchanged: the lead writes
specs, reviews diffs, runs gates, commits; the delegate burns the tokens.

| Provider | `--model` form here | Default | Login |
|---|---|---|---|
| `zen` — OpenCode Zen | `opencode/<id>` | `step-5-preview-free` | none for a free id |
| `openrouter` with `--harness opencode` | `openrouter/<vendor>/<id>` | **none** — name an id, set one in the config, or `--model free` | an OpenRouter key in opencode's auth store (`opencode auth login`) |

The `--model` form is `<qualifier>/<id>`, where the qualifier is the id
opencode's own CLI uses for the provider — the string `opencode models`
prints, so you can copy it from there. For OpenRouter it is the provider's
name; for Zen it is `opencode`, which is also the harness's name. That is
why the launcher calls the provider `zen`: a provider and a harness are
different things. `outsource-run --list-wiring` prints the form per row.

## zen — OpenCode Zen's free ids

`--provider zen` is OpenCode's own hosted provider. A bare
`--provider zen` runs `step-5-preview-free`.

What was measured on 2026-10-09 (opencode CLI 1.18.21):

- **Listed and free.** `opencode models --refresh` lists
  `opencode/step-5-preview-free`; `--verbose` shows cost 0 in and out, a
  1000000-token context, 65536 output tokens, and declared input text,
  image and video.
- **No login.** It answered while opencode's auth store held only an
  OpenRouter key, so the launcher's credential preflight stays
  OpenRouter-only.
- **Through the launcher, end to end:** `rc=0`, `model_verdict=ok` from
  `opencode export` (assistant `providerID: "opencode"`, `modelID:
  "step-5-preview-free"`), the done-marker found, the file the spec asked
  for written inside `--cwd`, and `git add && git commit` refused by the
  generated permission config with HEAD unmoved.
- **It sees shape and colour family, not exact colour.** Through the
  `read` tool: a drawn `7` → `7`, a drawn `L` → `L`; a `#1E50DC` fill →
  `#3A5BF0`, "Royal blue" (4–11% per channel). Two hazards, both measured:
  - one probe answered `S`, with high confidence, **without calling `read`
    at all** — no `tool_use` in its log. A pixel verdict from this arm
    counts only if the log shows the `read` call on that file;
  - one colour probe computed the hex with a bash/PIL script instead of
    looking. Exact, but it was not vision.
- **Two opencode runs started in the same second can collide.** One of a
  pair died with `database is locked`: opencode's own database is shared
  by every round, whatever the provider. Measured once. Until that is
  fixed, do not start two opencode rounds at the same moment.

**The free window is borrowed time.** Zen's docs say "free on OpenCode for a
limited time" and give no end date; OpenCode's announcement of 2026-10-09
said one week. When it ends, empty `defaultModel` on the zen row in
`internal/launch/wiring.go` and add `zen` to `emptyByDesign` in
`wiring_test.go`, in the same commit — the openrouter row below has done
this twice.

**Privacy is per id, not per provider.** Zen's docs say of this id: "Its
provider follows a zero-retention policy and does not use your data for
model training." Other free ids on the same provider say otherwise:
big-pickle ("During its free period, collected data may be used to improve
the model"), the muse-spark contributor-free ids (training permission is
the price), nemotron ("Trial use only — do not submit personal or
confidential data"). Read the id's own line on
https://opencode.ai/docs/zen/ before naming another one.

## openrouter on this harness

The provider itself — key, catalogue, free picks, data policy, rate limits —
is `references/openrouter.md`. What follows is what the opencode harness
adds, and the history measured on it.

> **There is no default model on this row.** On this harness the form is
> `--model openrouter/<vendor>/<id>`; OpenRouter ids are `vendor/model`, so
> the flag value carries two slashes. With no `--model` and no default in the
> user config, `--provider openrouter` refuses at exit 64 with
> `provider openrouter has no default model`.
>
> **The stealth slot has now emptied twice, and that is the fact to carry.**
> `stealth/ox-alpha` held it until 2026-09-10 and was unveiled as
> glm-5.3-flash (which the `zai` provider routes directly, and cheaper).
> `stealth/union-alpha` held it from 2026-09-16 and stopped serving on
> **2026-09-18**, measured here: a probe round came back `rc=1` with the
> endpoint's own 404 body — *"Thank you for participating in the Stealth Union
> Alpha testing period. This model was Unbiased's Pareto."* The pattern is a
> pattern, not a product: an unnamed lab puts a model up free while it
> evaluates it, then withdraws it and names it. **Do not plan a round around
> free capacity here.** The unveiled successor, `unbiased/pareto`, is live and
> priced — $2.5/M in, $7.5/M out, 262144 context — so it is an id you may name,
> but it is not free and it is not this row's default. When some future
> occupant takes the slot, fill `defaultModel` in the openrouter row of
> `internal/launch/wiring.go` and drop `openrouter` from `emptyByDesign` in
> `wiring_test.go`, in the same commit.

On this harness opencode resolves its own credentials (`opencode auth
login`) and endpoints: the launcher sets no API URL and passes no key. The
launcher's `internal/cred` row for openrouter is what the claude-code harness
reads; it may read the key opencode stored, but never writes opencode's file.

**Identity caveat:** an OpenRouter id can be re-pointed by the catalogue
without notice (that is how the stealth slot worked, and how it ended).
The launcher asserts identity per round via `opencode export` and fails
the round with exit 70 on a mismatch, even when the run itself succeeded.

**Privacy caveat, and it is the price of "free".** The stealth endpoint
published **no data policy at all** — `/api/v1/models/stealth/union-alpha/
endpoints` returned `"data_policy": null` (checked 2026-09-17; by 2026-10-09
the endpoints answer carried no policy field at all, and `outsource models`
reads policy from OpenRouter's provider list instead), and the lab behind it
was not named. A stealth id is by construction an id whose terms you
cannot read. You therefore cannot establish what happens to a
round's prompt, and a delegated round's prompt is your spec plus every file
the delegate reads. Treat this arm as disclosure to an undisclosed party:
fine for open source and throwaway work, not for proprietary code, secrets,
or anything under an NDA. The zai and agy arms are on named accounts with
stated terms; use them when that matters.

**Vision:** the harness path carries pixels — measured 2026-08-23 on ox-alpha
(a spec naming an absolute path to a solid-red PNG answered `Red` through
opencode's `read` tool) and again 2026-09-17 on union-alpha. For an id with
no model row the openrouter row's vision answer (`unlistedVision`) **defers**
rather than deciding, because the launcher cannot speak for an arbitrary
catalogue id: image-naming specs pass the guard here and
the caller owns the choice of a model that can actually see. Do not read that
pass as this skill certifying the id.

> **Shape yes, colour no — measured on `stealth/union-alpha`, 2026-09-17.**
> Two rounds, two independent probes, through the launcher and opencode's own
> `read` tool:
>
> | probe | truth | answered | verdict |
> |---|---|---|---|
> | drawn glyph on black | `4` | `4` | correct |
> | drawn glyph on black | `T` | `T` | correct |
> | uniform fill | `#1E50DC` (blue) | `#560000`, "dark maroon-red" | **wrong hue** |
> | uniform fill | `#E8A020` (orange) | `#F5F5F5`, "off-white" | **wrong hue and lightness** |
>
> Both colour answers came with stated high confidence ("both images were
> delivered to me as actual rendered attachments and I read them directly").
> That combination is the hazard: the model is not blind and it does not report
> uncertainty, so a round asked to verify a palette will return a fluent,
> confident, wrong hex. Compare glm-5.3-flash on the same `#1E50DC` fill:
> `#2244DD`, ~5% per channel and the right colour name.
>
> So: this arm is usable for shape, layout, presence and "is the element
> there". **Send precise colour and luminance judgment elsewhere** — the same
> rule the rest of this skill applies, for a sharper reason than usual.

## Invocation

The shared implementer preamble (`references/spec-preamble.md`) is
backend-agnostic; there is no opencode-specific preamble. Assemble the
shared file in front of every task spec.

```bash
SP=<scratch-dir>
cat ~/.claude/skills/outsource/references/spec-preamble.md \
    $SP/task.md > $SP/spec.md

~/.claude/skills/outsource/bin/outsource-run.sh --detach \
  --provider zen \
  --cwd /absolute/path/to/worktree --spec $SP/spec.md \
  --label <what-this-track-is-for> \
  --config-dir $SP/oc-cfg-<track> --log $SP/oc-<track>.log \
  --done-marker DONE-<TRACK>
# OpenRouter here: --provider openrouter --harness opencode --model openrouter/<vendor>/<id>
```

`--detach` re-execs into its own session (same as grok-run / the zai
harnesses — it happens before harness dispatch). A non-TTY foreground
launch is refused at exit 64; use `--detach` or `--foreground`.

`--harness opencode` is zen's default and can be omitted there; OpenRouter
needs it spelled out. `--model` can be omitted on a row that has a default
(zen does; openrouter has one only when the user config sets it). When you
pass one, it is `<qualifier>/<id>` — `opencode/<id>` on zen,
`openrouter/<vendor>/<id>` on openrouter, where the id itself contains a
slash, so a naive one-slash split is wrong. The form is checked before the
`--detach` re-exec, so a malformed value refuses on your terminal (exit 64,
naming the provider and the prefix it wants) instead of dying silently in the
detached child.

`--label` is the track's purpose, and it is worth typing every time. The
last stdout line is `SESSION <id>` — pass it back with `--session <id>`
for a follow-up in the same context (`-s` on `opencode run`).

`--require-quota` is not available: quota.sh reads plan windows for the
subscription backends (zai, grok), and neither provider here has one. The
launcher prints the existing generic refusal and exits 66.

Flags the other harnesses also take work the same way: `--max-seconds N`
(exit 124), `--done-marker` (absent → exit 72), `--no-vision-check`,
`--detach`, `--foreground`.

Read the round's report with `bin/last-report.sh <log>`.

## Harness facts — opencode (measured 2026-08-23, CLI 1.18.21)

- `opencode run --format json -m openrouter/<vendor>/<id>` with the
  spec on **stdin** is the headless form (measured on `stealth/ox-alpha`,
  since withdrawn; the harness contract is the model-independent part).
  JSONL events on stdout: `step_start`, `tool_use`, `text`,
  `step_finish`, each with a top-level
  `sessionID`. `part.text` holds assistant text; `part.tool` /
  `part.state` describe a tool call.
- **Stdout is flushed per event while the process is still running.**
  A watch on the log file saw it grow at 4s, 6s, 15s, 16s, 19s of a
  20s round. `runs.sh` therefore registers `progressDir` as the `--log`
  file itself. (claude-code's `--log` is *not* a live trail — do not
  copy that assumption here.)
- An unattended **in-cwd write** completed without `--auto` (created
  `hello.txt` containing `hi\n`). A `read` of a path **outside cwd**
  hit `external_directory` (default ask) and was rejected headless:
  `The user rejected permission to use this specific tool call.` With
  `--auto` the same `read` completed (`Image read successfully`) and
  the model answered `Red`. The launcher always passes `--auto`.
  Explicit `"deny"` rules still hold under `--auto`.
- `--pure` skips external plugins. The user's environment already sets
  `OPENCODE_CONFIG_DIR` to an orca hooks directory; a launched round
  **filters that variable out** of `os.Environ()` and sets its own
  isolated dir (`<config-dir>/opencode/`). Duplicate `KEY=value`
  entries are runtime-dependent; filter-then-append is the only safe
  override. `--pure` is the second belt.
- **`$PWD` decides the session's directory, not the process cwd**
  (measured 2026-08-23: with process cwd=A and PWD=B the session
  recorded B and the round's writes landed in B — every other signal
  stayed green, so the artifact quietly landed outside `--cwd`). The
  launcher replaces the inherited PWD with `--cwd` and, as the
  recurrence gate, fails the round (exit 70, `DIRECTORY MISMATCH`)
  when the export's `info.directory` is not `--cwd`.
- Isolated config is `opencode.json` in that dir (this is what
  `OPENCODE_CONFIG_DIR` replaces — it becomes Path.config). The
  permission block denies the git-write class (commit/push/checkout/
  stash/restore/add and the rest of the guard's `denyGit` list) and
  re-allows the listing forms the guard allows (`git worktree list`,
  `git branch --list`, `git remote -v`, `git config --get`).
  FAIL-first: with no permission config, `git commit --allow-empty -m x`
  created a commit. With the generated config — parent env still
  carrying the orca `OPENCODE_CONFIG_DIR` — the tool call was blocked
  (`The user has specified a rule which prevents you from using this
  specific tool call`) and HEAD did not move. Same under `--auto`.
- Diagnostics go to **stderr** (a FORCE_COLOR/NO_COLOR warning on this
  machine). The launcher writes stderr to `<log>.err` and keeps the
  log pure JSONL.
- Session id is the first JSONL event's `sessionID`. Resume is
  `opencode run -s <id>`.
- **Model identity** is `opencode export <sessionID>`:
  `messages[].info` where `role=="assistant"` carries `modelID`
  (the requested id) and `providerID` (the provider's qualifier:
  `openrouter`, or `opencode` for zen). Every assistant message must match
  that qualifier and the requested id minus its `<qualifier>/` prefix — a
  zen round answered under `openrouter`, or the reverse, is a mismatch.
  Mismatch, no assistant message, or unparseable export → exit 70.
  The same export's `info.directory` must equal `--cwd` (symlinks
  resolved) — a round that ran somewhere else fails the same way.
  Timed-out rounds skip the assertion (truncated log, same reason as
  claude-code).
- Auth preflight (openrouter on this harness only — zen's free ids measured
  to run with no Zen key, and on claude-code the key resolves through
  `internal/cred`) is best-effort: if `~/.local/share/opencode/auth.json`
  (or `$XDG_DATA_HOME/opencode/auth.json`) exists, parses, and has no
  usable `openrouter` key, the launcher refuses before registering a
  round and points at `opencode auth login`. Absence of the file is
  **not** proof — newer opencode also has a credential table in
  `opencode.db`. When the launcher cannot tell, it launches (fail open).
- Cost: `step_finish` reports per-round cost. It reads `"cost":0` for a free
  stealth listing — measured on ox-alpha, and again on union-alpha
  (2026-09-17) — which is not what a priced id will report. There is no
  plan-quota window either way, so `--require-quota` stays unsupported on this
  arm.
- **A priced id needs credits on the account, and the refusal says so.** With
  an empty balance, `openrouter/z-ai/glm-5.3-flash` came back
  `Insufficient credits` (status 402) while the free stealth id ran fine in the
  same minute (measured 2026-09-17). The launcher lifts that message out of the
  log onto stderr and into the sentinel as `harness_error=`, because a
  `--detach` round has no terminal left to print it to.

## `-f` is an array flag (do not rediscover)

`-f red.png "message"` swallows the message as a second file
(`Error: File not found: …`). If you attach files on a raw
`opencode run` line, the positional message must come **before** `-f`.
This skill's vision path does not use `-f`: it names an absolute path
in the spec and lets the `read` tool open it.

## Sentinel / exit codes

Same family as the zai launcher:

| rc | meaning |
|---:|---|
| 0 | harness exited cleanly **and** identity matched **and** the done-marker was found in the final report (when one was requested) |
| 64 | usage (unknown flag, missing `--cwd`/`--spec`, `--model` not in the provider's `<qualifier>/<id>` form, pairing refused, done-marker not in the spec — plus **`--model` absent** once the row's default lapses) |
| 65 | vision guard: spec names an image and the model cannot see pixels (does not fire for an unmeasured id here — both providers defer to the caller; zen's step-5-preview-free is measured colour-family, so it passes too) |
| 66 | `--require-quota` is not available for this provider |
| 69 | `opencode` CLI not on PATH |
| 70 | model-identity mismatch or unverifiable, or the session's directory was not `--cwd` |
| 72 | clean harness exit, `--done-marker` absent from the final report |
| 73 | claude-code harness only — transcript held zero `tool_use` blocks (`--allow-no-tools` allows it); not reachable on this harness |
| 1 | OpenRouter credentials positively absent from auth.json (openrouter on this harness only) |
| 124 | `--max-seconds` ceiling; process group killed |

`<log>.rc` carries `harness=opencode`, `provider=openrouter|zen`,
`model_requested`, `model_actual` (from export), `session`, and
`done_marker=found|absent (report)` with `done_marker_scope=report`.

## E1–E3 measured profile (2026-08-23)

| # | Question | Answer |
|---|---|---|
| E1 | Unattended tool round without `--auto`? | Yes, for an in-cwd `write`. Created `hello.txt` (`hi\n`). JSONL types: `step_start`, `tool_use`, `step_finish`, `text`. Log grew while pid was alive. |
| E2 | Is the git deny needed, and does isolation hold? | Without a permission config the commit succeeded (`831c89f x`). With the generated config the commit was blocked and HEAD stayed unborn, even though the parent env still had the orca `OPENCODE_CONFIG_DIR`. Same result with `--auto`. |
| E3 | Can an agentic round read an image named by path? | Without `--auto`: `read` of the PNG outside cwd → rejected, no colour answer. With `--auto`: `read` completed, final text `Red`. The harness path carries pixels; whether a given catalogue id uses them is the caller's pick. |
