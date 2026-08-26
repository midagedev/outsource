# agy — Google Antigravity CLI as a delegated backend

`agy` (the Antigravity CLI, Gemini CLI's successor) is **provider and harness
in one**: auth and quota live in the signed-in Google plan, so there is no
cred row, no base URL, and no `--harness` choice — `--provider agy` implies
harness `agy`. Everything below is field-measured 2026-08-27 on agy 1.1.21.

## Invocation

```bash
<skill-dir>/bin/outsource-run.sh --detach \
  --provider agy \
  --cwd <worktree> --spec <scratch>/spec.md --log <scratch>/run.log \
  --label <purpose> --done-marker DONE-<track>
```

The launcher drives `agy -p <spec> --output-format stream-json --model <id>
--add-dir <cwd> --dangerously-skip-permissions --print-timeout <pin>`.
Everything a lead already does — `runs.sh`, `last-report.sh`, the `.rc`
sentinel, `--done-marker` — works unchanged; the stream-json log flushes per
event, so the `--log` file is the live trail (same as opencode).

## Which model

`agy models` lists Gemini tiers plus pass-through models. Routing:

| Model | Route it | Why (measured) |
|---|---|---|
| `gemini-3.7-flash-high` | **default** — implementation, reports, and vision verdicts | user decision 2026-08-27: flash runs on high only. Vision is the standout: a solid `#1E50DC` PNG was named `#1e50dc` **exactly**, and the white-7 shape probe answered "7" — better color fidelity than any other wired backend |
| `gemini-3.7-flash-medium` / `-low` | not routed | same decision; low exists for cheap probes only |
| `gemini-3.1-pro-*` | worth an A/B before routing | untested here |
| `claude-*`, `gpt-oss-*` via agy | do not route | untested pass-throughs; the identity assertion covers them but nothing else is measured |

A nonexistent model id fails loudly (`invalid model selection`, rc=1, the
available list on stderr) — no silent mapping was measured, unlike z.ai.

## The traps this launcher already closes (do not re-open them)

- **Relative paths do not mean the process cwd.** With an untrusted cwd the
  file tools resolve "current directory" to agy's own
  `~/.gemini/antigravity-cli/scratch` — a probe's `hello.txt` landed there
  three runs in a row while the round reported SUCCESS. The launcher passes
  `--add-dir <cwd>` + `--dangerously-skip-permissions`, which makes
  ABSOLUTE paths land where they say. **Specs for this backend must use
  absolute paths for every file operation** (the shared preamble already
  demands this; here it is load-bearing, not hygiene).
- **Exit code 0 is not success.** A permission-denied round exits 0 with
  `status:"CANCELED"`; a soft-denied write exits 0 with `status:"SUCCESS"`
  and "DONE" in the response while no file exists. The launcher reads the
  final result event and fails any round whose status is not SUCCESS; judge
  by the sentinel and the tree, never by agy's exit code.
- **`--print-timeout` defaults to 5 minutes** — it would truncate most real
  rounds. The launcher pins it to 24h (or above `--max-seconds` when that is
  set, so the watchdog's kill stays attributed as exit 124).

## Git guard — shared settings, and why

There is **no per-track config isolation**: a HOME-isolated copy of
`~/.gemini` fails auth (`authentication failed or timed out`) even with
`oauth_creds.json` and the full CLI state copied — credentials are bound to
the sidecar/keychain, not to files under HOME. So every agy round shares
`~/.gemini/antigravity-cli/settings.json`, and the launcher installs the git
guard there before each round: `permissions.deny` rules, **one per
subcommand** (`command(git commit)`, `command(git push)`, …, plus the gh
write verbs).

Measured semantics that shaped this:

- deny beats `--dangerously-skip-permissions` (a `git add … && git commit`
  compound was refused while the flag was set, and the tree stayed clean);
- the matcher is a **substring match, not a regex** — a combined
  `command(git (commit|push|…))` alternation denied nothing and a commit
  went through (E2E, 2026-08-27); the per-subcommand form is the one that
  holds;
- deny > allow means the read-only listing forms other backends re-allow
  (`git worktree list`, `git branch -a`, `git config --get`) are denied too.
  `git log/show/diff/status` stay available. Spec phrasing: tell the
  delegate branch/worktree listings are unavailable and it should report
  rather than retry.

The rules also apply to the user's own interactive `agy` sessions — that is
the cost of no isolation. To remove them: delete the `command(git …)` /
`command(gh …)` lines from `permissions.deny` in
`~/.gemini/antigravity-cli/settings.json`; the next delegated round
reinstalls them.

## Identity, session, quota

- **Identity**: the sentinel's `model_actual` comes from the conversation's
  trajectory store (`~/.gemini/antigravity-cli/conversations/<id>.db{,-wal}`
  records the exact requested slug — stronger than the stream `init.model`,
  which is a request echo and is used only as the labelled fallback).
  Mismatch or no evidence is exit 70, as everywhere.
- **Session**: `SESSION <conversation_id>`; resume with `--session <id>`
  (mapped to `agy --conversation`).
- **Quota**: there is no readable plan quota — Google publishes relative
  tiers and a 5-hourly top-up, nothing queryable. `--require-quota` is not
  available for this provider; headroom management is by observation.
- **Auth**: cached from the user's interactive login. A signed-out machine
  fails loudly (`authentication failed or timed out`, rc=1) — re-run `agy`
  interactively to sign in.

## Report retrieval

`last-report.sh <log>` understands the agy shape: the final
`{"event":"result"}` carries the whole response in `result.response`, same
trust rank as claude-code's result event. `--done-marker` scoping works on
that report (scope `report`).
