# Codex on Cheaper Inference — an env-override wrapper, not a launcher backend

`bin/codex-ci` points the **Codex CLI** at Cheaper Inference's
OpenAI-compatible Responses endpoint by CLI config override, the same shape
as the GLM claude-code arm (`ANTHROPIC_BASE_URL`/`ANTHROPIC_AUTH_TOKEN`
against z.ai): the vendor's own harness, redirected at a cheaper endpoint,
with **nothing written to `~/.codex/config.toml`**. Your own Codex default
(`model`, `model_provider`, sandbox, plugins) is untouched — plain `codex`
still goes to OpenAI.

**This is not an `outsource-run.sh` backend.** No run registry, no
`bin/git-guard.sh` PreToolUse hook, no `--detach`/`--done-marker` sentinel,
no model-identity assertion, no quota pre-flight. It is a one-line way to
spend Cheaper Inference credit from a terminal, and the round is only as
supervised as the caller makes it. Route spec-able delegation rounds through
`outsource-run.sh` and the backends in SKILL.md instead.

## Credential

`CHEAPER_INFERENCE_API_KEY` in the environment wins; otherwise the wrapper
reads `~/.codex/cheaperinference.key` (chmod 600). It exports the variable
for Codex, because `env_key` in the provider config names the **variable**,
not the value — a key pasted there fails with `Missing environment variable:
ci_live_…`, which is Codex quoting your config back, not a 401. There is no
`internal/cred` row and `bin/setup-key.sh` does not know this key.

## Invocation

```bash
codex-ci                                        # interactive
codex-ci exec --sandbox read-only "<prompt>"    # headless
CI_MODEL=gpt-5.6-luna codex-ci exec "<prompt>"  # another catalog id
```

`CI_MODEL` defaults to `gpt-5.6-terra`. Ids come from the live catalog
(`GET https://api.cheaperinference.com/v1/models`); the same call reports
`context_length` and `max_output_tokens`, and returns `null` where the
upstream provider never declared one. Add `-c model_context_window=<n>` if a
long round gets truncated — the wrapper does not guess a window.

## Measured 2026-09-08 (Codex CLI v0.153.4, macOS)

- `codex-ci exec --sandbox read-only "Run pwd …"` completed on
  `gpt-5.6-terra`: shell tool call executed inside the sandbox, answer
  correct, **15,302 tokens** billed. The session header printed
  `provider: cheaper-inference`, so the override reached the client.
- A deliberately wrong key gives `unexpected status 401 Unauthorized:
  Invalid API key., url: https://api.cheaperinference.com/v1/responses`
  after 5 reconnect attempts — i.e. a green run really did go to Cheaper
  Inference, not to your ChatGPT plan.
- `wire_api = "responses"` is required; the endpoint serves `/v1/responses`
  and Codex's local tools (`apply_patch`) ride on it.

Not measured: vision, long-context behavior, `--resume` across a provider
switch, cost per round against the dashboard's settled charge.
