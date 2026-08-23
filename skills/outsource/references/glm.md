# GLM-5.3 backend — z.ai coding plan, on either harness

The model is the point; **the harness is just how it is driven headlessly**,
and `bin/outsource-run.sh --harness` picks one. Division of labor is unchanged:
the lead writes specs, reviews diffs, runs gates, commits; GLM-5.3 burns the
implementation tokens, which are close to free on a z.ai coding plan.

| Harness | How | Pick it when |
|---|---|---|
| **claude-code** (default) | `claude -p` against z.ai's Anthropic-compatible endpoint | you want Claude Code's tooling — CLAUDE.md injection, mature headless mode, hook/permission system, `--resume` |
| **crush** | the crush CLI with an isolated `CRUSH_GLOBAL_CONFIG` | you want a second, independent process family (parallel headroom), or claude-code is unavailable |

Both attach the same `bin/git-guard.sh` and take the same specs; the guard
reads the command either from `$CRUSH_TOOL_INPUT_COMMAND` (crush) or from
hook JSON on stdin (claude-code).

**z.ai's model mapping is a trap, and the launcher closes it.** Measured
2026-08-16 against `https://api.z.ai/api/anthropic`: a request for
`claude-opus-5` comes back as **glm-4.7** (the plan default), while
`glm-4.6`/`glm-5.3` are honoured verbatim. So the harness must pin
`ANTHROPIC_MODEL` — otherwise you believe you ran one model and actually ran
another.

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

**Hard limit (measured):** GLM-5.3 has `supports_attachments: false` — it
cannot see images. Every visual verdict — and style/look/UI-interaction
authoring (A/B-measured, below) — goes to a vision-capable frontier agent
(we use an Opus subagent). GLM-5.3 can still do the numeric half of visual
work: pixel-decoding scripts, capture harness wiring, gate authoring.

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
`data/logs/crush.log`, the claude-code harness's `claude/projects/**.jsonl`.
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
```

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

