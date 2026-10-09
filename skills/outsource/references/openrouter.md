# OpenRouter — any id, a configured default, or a free pick

OpenRouter is a catalogue: hundreds of ids from many labs behind one key,
and a set of free ids that changes week to week. This launcher drives it on
the **claude-code harness by default** (`claude -p` against OpenRouter's
Anthropic-compatible endpoint), and on the opencode harness when you ask for
it (`references/opencode.md`). Division of labour is unchanged: the lead
writes specs, reviews diffs, runs gates, commits; the delegate burns the
tokens.

| Harness | `--model` form | How to pick it |
|---|---|---|
| claude-code (default) | the bare id: `z-ai/glm-5.3`, `nvidia/nemotron-3-ultra-550b-a55b:free` | nothing to add |
| opencode | `openrouter/<vendor>/<id>` | `--harness opencode` |

The row has **no table default**. Name an id, set one in your config
(`outsource config set providers.openrouter.defaultModel <vendor>/<id>`),
or pass `--model free`. With none of these, `--provider openrouter` refuses
at exit 64.

## The key

The claude-code harness resolves the key through `bin/credential.sh`, which
tries, in order:

1. `OPENROUTER_API_KEY`;
2. this skill's own store, written by `bin/setup-key.sh openrouter` (mode
   0600). setup-key checks the key against `GET /api/v1/key` first — a
   request that spends nothing — and stores nothing on a rejection;
3. the key `opencode auth login` already stored in opencode's auth file
   (`$XDG_DATA_HOME/opencode/auth.json`, by default
   `~/.local/share/opencode/auth.json`). It is read, never copied or
   rewritten.

The endpoint is `https://openrouter.ai/api`; the CLI appends
`/v1/messages`. To move it, set `OUTSOURCE_OPENROUTER_BASE_URL`. The
generic `OPENROUTER_BASE_URL` is deliberately ignored: other tools set it to
the OpenAI-compatible root (`…/api/v1`), and honouring it would send rounds
to `…/api/v1/v1/messages`.

## Launching

```bash
~/.claude/skills/outsource/bin/outsource-run.sh --detach \
  --provider openrouter --model z-ai/glm-5.3 \
  --cwd /absolute/path/to/worktree --spec $SP/spec.md \
  --label <what-this-track-is-for> --log $SP/or-<track>.log \
  --done-marker DONE-<TRACK>
```

The 0.20.0 docs taught `--model openrouter/<vendor>/<id>` with no
`--harness`. That form arriving on claude-code is rewritten to the bare id,
with one stderr line saying so. A two-segment id that starts with
`openrouter/` (`openrouter/auto`, or a stealth id under OpenRouter's own
namespace) is a real bare id and is left alone.

## Measured end to end (2026-10-09)

A launcher round on `--provider openrouter --model
nvidia/nemotron-3-ultra-550b-a55b:free --effort max` (claude-code harness):

- `rc=0`; `model_requested` and `model_actual` both
  `nvidia/nemotron-3-ultra-550b-a55b:free`, with the `:free` suffix;
- three tool calls (read a file, write a file, `git commit`); the commit was
  blocked by the `PreToolUse` git guard and the repository did not move;
- the transcript's model field was the requested id on all 8 assistant
  messages, and `modelUsage` named only that id — no background request to
  another model;
- the key's spend on `GET /api/v1/key` was the same before and after;
- stderr carried `[claude-code:unrecognized_model]`: the CLI does not know
  OpenRouter's ids. That line is expected, not an error.

Earlier the same day, raw probes of `/api/v1/messages` answered a tools
request with `stop_reason: tool_use`, and `max_tokens` 64000 (what the
launcher sets through `CLAUDE_CODE_MAX_OUTPUT_TOKENS`) against an id whose
listed output limit is 32768 answered `end_turn`: OpenRouter clamps rather
than refuses.

Not measured: a long round against the free rate limit, and any image read
through the claude-code harness on an OpenRouter id. Vision on this
provider is measured only on opencode (`references/opencode.md`), so for an
id with no model row the vision guard defers to you.

## The catalogue — `outsource models`

`outsource models [--provider openrouter|zen] [--free] [--tools] [--policy]
[--refresh] [--json]` prints what the catalogue providers offer right now:
context, price, tool support, input kinds, expiry, and a data policy per
id. OpenRouter's list comes from its public `/api/v1/models`; nothing is
sent with a key. Everything is cached for an hour under
`${XDG_CACHE_HOME:-~/.cache}/outsource/catalog`, and a failed fetch falls
back to the cache and says how old it is.

**Free** means both token prices are exactly zero and the id is not a
router. A router (`openrouter/auto`, `openrouter/free`, or any id priced at
`-1`) picks a different model per request, so the launcher's identity check
cannot pin it.

**Data policy** is one of `no-train-no-retain`, `retains`, `trains` or
`unknown`. OpenRouter publishes a data policy per *provider*, not per id,
so the verdict must hold for every provider an id may be routed to: one
provider that trains makes the id `trains`; one the list does not cover
makes it `unknown`. `unknown` is a verdict, never a gap filled with a
guess. On 2026-10-09 most free, tool-capable ids were `trains` or
`retains`; two were `no-train-no-retain`.

## `--model free`

`--model free` asks the launcher to pick. The process that parses your
command line does it once — before the run is registered and before a
`--detach` re-exec, so the detached child runs exactly the id the parent
printed. A candidate must be:

1. free and not a router;
2. tool-capable;
3. not expired, and not marked withdrawn;
4. at least 128000 tokens of context (an unstated context fails);
5. not an id this launcher measured to be answered by another model;
6. `no-train-no-retain` — unless the config has `free.allowTraining: true`
   or the launch passes `--allow-free-training`, which also admit `trains`,
   `retains` and `unknown`.

Among the candidates, your config's `providers.openrouter.defaultModel`
wins if it qualifies; then ids this launcher has a model row for; then the
largest context. One stderr line names the pick, its policy and context,
and how fresh the catalogue was:

```
outsource: --model free → apodex/apodex-1.1-mini:free (no-train-no-retain, 262144 ctx; 2 candidates; catalogue network 0s)
```

The sentinel carries the id as run in `model_requested` and
`model_selector=free`. When nothing qualifies, the refusal (exit 64) counts
what each filter dropped. A detached child never asks the catalogue again:
the parent hands it the id and the listed context. `--model free` with
`--session` refuses — a resumed session keeps the model it ran, so name that
id (its sentinel's `model_requested`).

`outsource models --pick free [--provider openrouter|zen] [--cwd D]
[--allow-free-training]` prints the same pick, every filter's count and the
ranked candidates, without launching anything — the launcher and that
command call one resolver. With no `--provider` it answers for both
catalogues. On 2026-10-09 the strict pick on OpenRouter was
`apodex/apodex-1.1-mini:free` out of 2 candidates; with training allowed it
was `thinkingmachines/inkling-small:free` out of 14.

**Keep free ids out of a directory.** `free.denyPaths` in the config is a
list of globs (`~` allowed, `**` for any depth; a glob that is not absolute
after `~` matches nothing). A round whose `--cwd` — symlinks resolved — is
under one refuses `--model free`, and refuses any id the catalogue lists as
free, whether you named it or it came from a default. When the catalogue
cannot say whether an id is free (switched off, unreachable), a round in a
denied directory refuses rather than guess. A priced id is not gated: what
happens to its prompts is the paying account's contract.

## A named id is checked too

On a named `--model`, the launcher looks the id up in the catalogue before
the round starts: an id the catalogue does not list (after one refresh), a
router, an expired id, or one without tool support refuses at exit 64. When
the catalogue cannot be reached at all, the round launches with one stderr
line saying the check was skipped — the network must not stop a round you
named. `OUTSOURCE_CATALOG=off` turns all catalogue traffic off: named ids
launch unchecked, and `--model free` refuses.

## Context window and compaction

The claude-code CLI does not know OpenRouter's ids and applies its own
unknown-model ceiling unless told otherwise. The launcher sets
`CLAUDE_CODE_MAX_CONTEXT_TOKENS` from the model row when there is one, else
from the catalogue's `context_length`, and `CLAUDE_CODE_AUTO_COMPACT_WINDOW`
to the smaller of that window and the config's `context.autoCompactWindow`
(default 600000), which `--list-wiring` shows as a `CONFIG` line when the
file sets it. A value you export yourself wins. The launcher sets these
variables; when the CLI then compacts relative to them is its own rule
(measured once: a 30000 setting under a 1000000 window compacted at 69922
prompt tokens).

## Limits and 429s

OpenRouter limits `:free` ids to 20 requests a minute, and to 50 requests a
day, or 1,000 once the account has bought 10 credits
([limits](https://openrouter.ai/docs/api-reference/limits)). A free id can
also be rate-limited upstream (measured: `google/gemma-4-31b-it:free`
answered 429 "temporarily rate-limited upstream"). Those 429s name no reset
time, so `--resume-on-reset` is refused for this provider at launch. A
priced id needs credits on the account: with an empty balance OpenRouter
answers 402 `Insufficient credits`.

## What to keep off this arm

A free id's price is often your data: most free ids on 2026-10-09 trained
on prompts. A delegated round's prompt is your spec plus every file the
delegate reads. Leave the default policy gate on for anything you would not
publish, and put proprietary trees in `free.denyPaths`. The history of
OpenRouter's stealth ids — free models from unnamed labs that published no
data policy at all, then were withdrawn — is in `references/opencode.md`.
