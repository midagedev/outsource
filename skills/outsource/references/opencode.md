# ox-alpha backend — OpenRouter stealth, on the opencode harness

The model is the point; **the harness is just how it is driven headlessly**.
`bin/outsource-run.sh --provider openrouter` picks this row and defaults
the harness to `opencode`. Division of labor is unchanged: the lead writes
specs, reviews diffs, runs gates, commits; ox-alpha burns the tokens,
which were free while listed as stealth (`step_finish.cost` was 0,
measured 2026-08-23).

opencode manages its own credentials (`opencode auth login`). This
launcher does not write a key, does not add an `internal/cred` row, and
does not set an API URL — opencode resolves OpenRouter itself.

**Stealth caveat:** model identity and limits can change without notice.
The launcher asserts identity per round via `opencode export` and fails
the round with exit 70 on a mismatch, even when the run itself succeeded.

The shared implementer preamble (`references/spec-preamble.md`) is
backend-agnostic; there is no opencode-specific preamble. Assemble the
shared file in front of every task spec.

**Vision (measured 2026-08-23):** ox-alpha **sees pixels** through
opencode's `read` tool. A spec that named an absolute path to a solid-red
PNG and asked the model to open it answered `Red`. The provider table
row is therefore `vision=true`, and the vision guard lets image-naming
specs through.

## Invocation

```bash
SP=<scratch-dir>
cat ~/.claude/skills/outsource/references/spec-preamble.md \
    $SP/task.md > $SP/spec.md

~/.claude/skills/outsource/bin/outsource-run.sh --detach \
  --provider openrouter \
  --cwd /absolute/path/to/worktree --spec $SP/spec.md \
  --label <what-this-track-is-for> \
  --config-dir $SP/oc-cfg-<track> --log $SP/oc-<track>.log \
  --done-marker DONE-<TRACK>
```

`--detach` re-execs into its own session (same as grok-run / the zai
harnesses — it happens before harness dispatch). A non-TTY foreground
launch is refused at exit 64; use `--detach` or `--foreground`.

`--harness opencode` is the default for this provider and can be omitted.
`--model` is `openrouter/<id>`; the default is `openrouter/stealth/ox-alpha`.
The model id itself may contain slashes (`stealth/ox-alpha`) — a naive
one-slash split is wrong.

`--label` is the track's purpose, and it is worth typing every time. The
last stdout line is `SESSION <id>` — pass it back with `--session <id>`
for a follow-up in the same context (`-s` on `opencode run`).

`--require-quota` is not available: quota.sh reads plan windows for the
subscription backends (zai, grok), and this provider has none. The
launcher prints the existing generic refusal and exits 66.

Flags the other harnesses also take work the same way: `--max-seconds N`
(exit 124), `--done-marker` (absent → exit 72), `--no-vision-check`,
`--detach`, `--foreground`.

Read the round's report with `bin/last-report.sh <log>`.

## Harness facts — opencode (measured 2026-08-23, CLI 1.18.21)

- `opencode run --format json -m openrouter/stealth/ox-alpha` with the
  spec on **stdin** is the headless form. JSONL events on stdout:
  `step_start`, `tool_use`, `text`, `step_finish`, each with a top-level
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
  (`stealth/ox-alpha`) and `providerID` (`openrouter`). Every assistant
  message must match the requested id minus the `openrouter/` prefix.
  Mismatch, no assistant message, or unparseable export → exit 70.
  The same export's `info.directory` must equal `--cwd` (symlinks
  resolved) — a round that ran somewhere else fails the same way.
  Timed-out rounds skip the assertion (truncated log, same reason as
  claude-code).
- Auth preflight is best-effort: if `~/.local/share/opencode/auth.json`
  (or `$XDG_DATA_HOME/opencode/auth.json`) exists, parses, and has no
  usable `openrouter` key, the launcher refuses before registering a
  round and points at `opencode auth login`. Absence of the file is
  **not** proof — newer opencode also has a credential table in
  `opencode.db`. When the launcher cannot tell, it launches (fail open).
- Cost: `step_finish` reported `"cost":0` on stealth. No plan-quota
  window, so `--require-quota` stays unsupported.

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
| 64 | usage (unknown flag, missing `--cwd`/`--spec`, pairing refused, done-marker not in the spec) |
| 65 | vision guard: spec names an image and the provider cannot see pixels (does not fire here; vision=true) |
| 66 | `--require-quota` is not available for this provider |
| 69 | `opencode` CLI not on PATH |
| 70 | model-identity mismatch or unverifiable, or the session's directory was not `--cwd` |
| 72 | clean harness exit, `--done-marker` absent from the final report |
| 1 | OpenRouter credentials positively absent from auth.json |
| 124 | `--max-seconds` ceiling; process group killed |

`<log>.rc` carries `harness=opencode`, `provider=openrouter`,
`model_requested`, `model_actual` (from export), `session`, and
`done_marker=found|absent (report)` with `done_marker_scope=report`.

## E1–E3 measured profile (2026-08-23)

| # | Question | Answer |
|---|---|---|
| E1 | Unattended tool round without `--auto`? | Yes, for an in-cwd `write`. Created `hello.txt` (`hi\n`). JSONL types: `step_start`, `tool_use`, `step_finish`, `text`. Log grew while pid was alive. |
| E2 | Is the git deny needed, and does isolation hold? | Without a permission config the commit succeeded (`831c89f x`). With the generated config the commit was blocked and HEAD stayed unborn, even though the parent env still had the orca `OPENCODE_CONFIG_DIR`. Same result with `--auto`. |
| E3 | Can an agentic round read an image named by path? | Without `--auto`: `read` of the PNG outside cwd → rejected, no colour answer. With `--auto`: `read` completed, final text `Red`. vision=true. |
