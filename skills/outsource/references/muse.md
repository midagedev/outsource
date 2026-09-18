# Muse Code, on the muse harness

`muse-spark-1.3-contributor`, driven headlessly by the Muse Code CLI. The model
is the point; the harness is just how it is driven. Division of labor is
unchanged: the lead writes specs, reviews diffs, runs gates, commits; the
delegate burns the tokens.

The CLI is provider and harness in one, like `agy`. Auth is an **OAuth device
flow the CLI owns** (`muse login`, stored at `~/.config/muse/auth.json`), so
this launcher writes no key, adds no `internal/cred` row, and sets no API URL.

> **The Anthropic-compatible endpoint is not the path.** Muse advertises one
> (`ANTHROPIC_BASE_URL=https://api.meta.ai` with `ANTHROPIC_MODEL=
> muse-spark-1.3-contributor`), and it is tempting to wire this arm as a second
> `claude-code` provider the way `zai` and `xai` are. Measured 2026-09-18: a
> direct call to `api.meta.ai/v1/messages` carrying an API key answered
> `{"type":"billing_error","message":"Billing verification failed"}`, on both
> the `x-api-key` and `Authorization: Bearer` forms, while the CLI's own session
> ran in the same minute. The CLI's OAuth session is what works, so the CLI is
> the harness.

## Invocation

```bash
SP=<scratch-dir>
cat ~/.claude/skills/outsource/references/spec-preamble.md \
    $SP/task.md > $SP/spec.md

~/.claude/skills/outsource/bin/outsource-run.sh --detach \
  --provider muse \
  --cwd /absolute/path/to/worktree --spec $SP/spec.md \
  --label <what-this-track-is-for> \
  --config-dir $SP/muse-cfg-<track> --log $SP/muse-<track>.log \
  --done-marker DONE-<TRACK>
```

`--harness muse` is the default for this provider and can be omitted, and so is
`--model` (the row's default is `muse-spark-1.3-contributor`).

`--effort low|medium|high|xhigh|max` maps onto `muse exec --reasoning-effort`,
whose own scale is `none|minimal|low|medium|high|xhigh|max|ultra` — a superset,
so every level this launcher accepts is passed through verbatim and recorded in
the sentinel as `effort=`. muse's default is `high`.

`--detach` re-execs into its own session, before harness dispatch, exactly as
the other arms do. A non-TTY foreground launch is refused at exit 64; use
`--detach` or `--foreground`.

Follow a running round with `bin/tail.sh <label> -f`; read the finished one with
`bin/last-report.sh <log>`.

`--require-quota` is not available: `quota.sh` reads plan windows for the
subscription backends (zai, grok), and this provider has none. The launcher
prints the generic refusal and exits 66.

## The sandbox is a second switch, and headless needs it off

muse ships two safety switches, not one: `--approval-mode` and a shell
filesystem/network **sandbox**, both on by default. The launcher passes
`--disable-approval` (nobody is there to answer) — and it must pass
`--disable-sandbox` with it, which it does.

The reason is that the two interlock badly. A tool call that needs to leave the
sandbox asks for approval to leave it, so with approval off and the sandbox on
the call is refused outright:

```
tool denied: unsandboxed execution requires human approval, but approval
prompts are disabled
```

Measured 2026-09-18, twice in one round, on a round running a repo's own npm
gates. That pair is a dead end rather than a safety posture — the escalation is
lost instead of deferred, and the round carries on believing the gate could not
run.

This does not weaken what is actually enforced. Every other arm in this skill
runs with no sandbox at all, and the three layers that hold a round in bounds
are the same on all of them: the git guard, worktree isolation, and the spec's
own file whitelist. `--yolo` would also clear the sandbox, but it disables
approval wholesale and marks the workspace trusted past this run; the launcher
names the one switch it means.

## The git guard is a PATH shim here, and that is the interesting part

Every other arm attaches the guard the way its harness allows — `claude-code`
takes a `PreToolUse` hook, `opencode` and `agy` take a deny block in a generated
config. **muse takes neither.** Its safety model is an approval mode plus a
sandbox, and its `--permission-profile <ID>` names profiles that no
user-writable document appears to define: every candidate member of the
enterprise `defaults` and `policy` planes was rejected as `unknown_member`
(measured 2026-09-18, `muse config validate`).

Left alone, that is not a theoretical gap. **A plain `muse exec` round asked to
run `git commit --allow-empty` did it** — `exit_code: 0`, HEAD moved to a root
commit (measured 2026-09-18).

So the guard moved down a layer, to the name itself. Each round gets a `git`
shim in `<config-dir>/bin`, that directory goes first on the round's `PATH`, and
the shim asks `internal/guard` — still the single owner of what is refused —
before exec'ing the real git. Measured in the round: `command -v git` resolved
to the shim, the commit was refused with **exit 97**, and HEAD stayed unborn.
The delegate reported the refusal verbatim, which is what you want: the round
learns it cannot commit rather than silently failing.

> **It is a belt, not a cage**, and the same belt the other arms wear. A round
> that calls `/usr/bin/git` by absolute path, or commits through a language
> binding, is past it — exactly as a round that hides a commit from opencode's
> command-pattern deny list is past that one. Worktree isolation and the spec's
> own rules are the other layers.

Exit 97 is deliberately not 1: the round has to be able to tell "the guard said
no" from "git ran and failed".

## Vision

**Measured 2026-09-18, through the CLI's own read tool** (a spec naming an
absolute path; no `--image` flag needed, though `muse exec --image <path>` also
exists):

| probe | truth | answered | verdict |
|---|---|---|---|
| drawn glyph on black | `H` | `H` | correct |
| uniform fill | `#1E50DC` | `#0000FF`, "blue" | right colour **name**, exact value well off |

So: shape, layout, presence and colour *family* are usable; exact hex is not.
That is the standing rule everywhere in this skill, and muse lands on the good
side of it — unlike the openrouter stealth slot's occupant, which named a blue
fill "dark maroon" with high confidence. Precise colour and luminance verdicts
still go to a frontier vision judge.

## Model identity — the live log is a request echo, the export is the evidence

The `--json` stream carries `run.model.configured`, and its own payload says
`source: "startup"`: it reports what the run was **configured** with, never what
replied. Treating it as proof would repeat the trap `claude-code`'s `modelUsage`
sets (measured 2026-08-16: a run requesting `claude-opus-5` and answered by
`glm-4.7` still logged `modelUsage {"claude-opus-5": …}`).

The response-side evidence lives in the durable session log, reached with
`muse export --session <id>`: envelopes whose payload is
`{"kind":"run","event":{"kind":"model_completed","model":"<id>"}}` — one per
model completion, each carrying that completion's own usage and duration. Every
completion must match the requested id; a round with none is *unverifiable*
rather than passed, and both outcomes exit 70. A real round produced five of
them (2026-09-18) and the sentinel recorded
`model_source=muse export (5 model_completed event(s))`.

## Harness facts — muse (measured 2026-09-18, CLI 1.3.0-R3401.1)

- `muse exec --json --prompt-file <spec> --model <id>` is the headless form.
  JSONL envelopes on stdout, one per line, each with `stream`, `sequence` and
  `payload_type`.
- **Stdout flushes per event while the process runs.** A watch saw the log grow
  at 4s, 8s, 16s, 20s and 24s of a 24s round, so `runs.sh` registers the `--log`
  file itself as both the progress signal and the readable trail — the same as
  opencode and agy, and unlike claude-code, whose `--log` is written once at the
  end.
- The round's prose arrives as many small `run.output.delta` events (one
  paragraph came in fifteen pieces), so `bin/tail.sh` joins consecutive speech
  into one line and lets tool calls break it.
- Session id is the `stream.id` of any envelope whose `stream.kind` is
  `session`. Resume with `--session <id>` (`muse exec --session-id`).
- The final report is `run.terminal.completed`'s `payload.text`;
  `bin/last-report.sh` reads it as an explicit result, the same trust rank as
  claude-code's.
- The launcher passes `--disable-approval` (headless: nobody can answer a
  prompt) and `--user-input-auto-resolve` (a round that asks a question would
  otherwise hang until `--max-seconds`). Neither weakens the git guard, which is
  the PATH shim and not the approval mode.
- Diagnostics go to **stderr** — workspace root, skills loaded, and a warning
  when a rules file exceeds the 32000-byte startup context limit. The launcher
  writes stderr to `<log>.err` and keeps the log pure JSONL.
- Cost: the export's `model_completed` events carry per-completion `usage`
  (input, output, cached, reasoning tokens). There is no plan-quota window, so
  `--require-quota` stays unsupported.

## Sentinel / exit codes

| rc | meaning |
|---:|---|
| 0 | harness exited cleanly **and** identity matched **and** the done-marker was found |
| 64 | usage (unknown flag, missing `--cwd`/`--spec`, pairing refused, done-marker not in the spec, the git shim could not be written) |
| 65 | vision guard (does not fire here — this provider is measured to see pixels) |
| 66 | `--require-quota` is not available for this provider |
| 69 | `muse` CLI not on PATH |
| 70 | model-identity mismatch or unverifiable |
| 72 | clean harness exit, `--done-marker` absent from the final report |
| 124 | `--max-seconds` ceiling; process group killed |

Inside a round, the git shim's own refusal is **exit 97** — that is the
delegate's shell seeing a blocked command, not the launcher's exit code.

`<log>.rc` carries `harness=muse`, `provider=muse`, `model_requested`,
`model_actual` (from the export), `session`, `effort`, `model_verdict`,
`model_source`, `trail`, and `done_marker=found|absent (report)`.
