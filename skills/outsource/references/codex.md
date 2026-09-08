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
codex-ci                                          # interactive, gpt-5.6-sol
codex-ci exec --sandbox read-only "<prompt>"      # headless
CI_MODEL=gpt-6-astra codex-ci                     # another model
CI_EFFORT=xhigh CI_MODEL=gpt-6-astra codex-ci exec "<prompt>"  # medium by default
```

`CI_MODEL` defaults to **`gpt-5.6-sol`**, `CI_EFFORT` to **`medium`**.
`CI_EFFORT` is Codex's `model_reasoning_effort`
(`minimal|low|medium|high|xhigh`) and the wrapper always passes it, so
**`~/.codex/config.toml` does not apply here** — a machine set to `xhigh`
still gets `medium` under `codex-ci` unless you say otherwise. That is the
point: this endpoint is metered per token, and `xhigh` is a subscription
habit. The session header echoes model and effort, so read it rather than
assuming.

## Models (catalog read 2026-09-08, prices USD per 1M tokens, all 128k output)

| `CI_MODEL` | ctx | in / out / cache-read | Notes |
|---|---|---|---|
| **`gpt-5.6-sol`** — the wrapper default | 1.05M | 1.00 / 5.00 / 0.10 | vision, reasoning. The everyday arm |
| **`gpt-6-astra`** | 1.05M | 7.00 / 35.00 / 0.70 | vision, reasoning. **7× sol on input, 7× on output** — reach for it on the round that earns it, not the default |
| `gpt-5.6-terra` | 1.05M | 0.80 / 4.80 / 0.08 | vision. Marginally under sol |
| `gpt-5.6-luna` | 1.05M | 0.08 / 0.48 / 0.008 | vision. **~12× cheaper than sol** — mechanical edits, fan-out |
| `gpt-5.5` / `gpt-5.4` | 1M | 3.50 / 21.00 · 1.75 / 10.50 | 5.4 is `vision=false` |
| `gpt-5.2-codex` | 256k | 1.23 / 9.80 | the only short-context id here |
| `claude-opus-5`, `claude-fable-5.1`, `glm-5.3`, `kimi-k3`, … | — | — | the catalog is 65 ids wide and not OpenAI-only; non-OpenAI ids are **unmeasured through Codex's Responses wire** |

The live list, with the same fields:

```bash
curl -s https://api.cheaperinference.com/v1/models \
  -H "Authorization: Bearer $CHEAPER_INFERENCE_API_KEY" \
  | jq -r '.data[] | [.id, .context_length, .pricing.input_per_million,
                      .pricing.output_per_million, .capabilities.vision] | @tsv'
```

`context_length`/`max_output_tokens` are provider-reported and come back
`null` where nobody declared one. Add `-c model_context_window=<n>` if a long
round truncates — the wrapper does not guess a window.

## Measured 2026-09-08 (Codex CLI v0.153.4 and v0.147.0, macOS)

- `codex-ci exec --sandbox read-only "Run pwd …"` completed on
  `gpt-5.6-terra`: shell tool call executed inside the sandbox, answer
  correct, **15,302 tokens** billed. The session header printed
  `provider: cheaper-inference`, so the override reached the client. The
  same round is green on Codex **0.147.0** — the `-c` override shape is not
  new-version-only.
- Four-way probe (`gpt-5.6-sol` and `gpt-6-astra`, each at `xhigh` and at an
  explicit `CI_EFFORT`): all four answered, and the header echoed the
  requested model and effort every time — `minimal`, `low`, `medium` and
  `xhigh` all took, on both Codex versions.
- Harmless noise on every run: `failed to refresh available models: …
  missing field 'models'`. Codex's model-list refresh expects OpenAI's
  envelope and Cheaper Inference serves `{"object":"list","data":[…]}`. It
  costs the model picker, not the round.
- A deliberately wrong key gives `unexpected status 401 Unauthorized:
  Invalid API key., url: https://api.cheaperinference.com/v1/responses`
  after 5 reconnect attempts — i.e. a green run really did go to Cheaper
  Inference, not to your ChatGPT plan.
- `wire_api = "responses"` is required; the endpoint serves `/v1/responses`
  and Codex's local tools (`apply_patch`) ride on it.

- **Plain `codex exec -m gpt-6-astra` on a ChatGPT-account login is refused**
  (measured 2026-09-08, Codex 0.147.0): `400 invalid_request_error — The
  'gpt-6-astra' model is not supported when using Codex with a ChatGPT
  account.` The session header still prints `model: gpt-6-astra / provider:
  openai` before the request fails, so read the log, not the header. astra
  reaches a round only through an API-key auth or this wrapper's Cheaper
  Inference route — and the wrapper needs `~/.codex/cheaperinference.key`,
  which the launcher does not create.

Not measured: vision, long-context behavior, `--resume` across a provider
switch, cost per round against the dashboard's settled charge.
