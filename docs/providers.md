# Providers

`ghrouter` currently recognizes these provider families:

- `claude-code`
- `codex`
- `opencode`
- `mimo`
- `pi`
- `cursor`
- `nvidia` (explicit hosted NIM connector)
- `local` (supervised host-local MLX/llama.cpp endpoint)

## Auto-Discovery

The CLI detector scans the PATH for known binaries and builds provider definitions from them. For OpenCode it also checks the official installer locations from `OPENCODE_INSTALL_DIR`, `XDG_BIN_DIR`, `$HOME/bin`, and `$HOME/.opencode/bin`. Discovery uses each installed CLI's supported catalog path: Codex uses its native app-server protocol, OpenCode and Mimo use their native model commands plus ACP capability probes, and Cursor Agent uses `agent --trust acp` with `session/new`. Flags are version-sensitive and are not assumed from another release. Native metadata is preserved when supplied, and discovery records `available`, `missing`, `auth`, `timeout`, `empty`, and `error` states explicitly. No provider or model is fabricated when a command is absent.

## Execution

Each provider is executed as a subprocess with:

- provider-specific flags
- inherited environment variables
- a working directory
- request prompt on the native adapter's documented transport (argument or
  stdin)

## Account Metadata

## NVIDIA NIM

NIM supports credential pools for separate accounts or quotas:

```yaml
accounts:
  - name: primary
    api_key_env: NVIDIA_API_KEY
    enabled: true
  - name: secondary
    api_key_env: NVIDIA_API_KEY_SECONDARY
    enabled: true
```

The connector rotates enabled accounts per request and retry. The account
name is metadata only; secrets remain environment-backed and masked.

NVIDIA NIM is an explicit hosted connector, not a local CLI. Configure the
provider with `auth_method: env` and
`auth_config.api_key_env: NVIDIA_API_KEY`; the secret remains in the process
environment and is never written to YAML or logs. With the key and
`GHR_NVIDIA_MODELS` set, discovery creates the `nvidia` HTTP provider from
operator-supplied model IDs. The router does not convert documentation names
into an inference-ready catalog. `ghrouter connect nvidia` prints this
environment contract, and configured requests use the NVIDIA OpenAI-compatible
chat endpoint.

Providers may expose optional configured account metadata for display and local
selection. This is not a universal live subscription or balance API.

The router reads these inputs from `auth_config` or environment variables:

- `account_json`
- `plan`
- `balance`
- `balance_currency`
- `reset_at`

The account payload is surfaced in:

- `ghrouter providers`
- `ghrouter live`
- selected catalog summaries where applicable

If `account_json` is present, it takes precedence and should contain the same fields in JSON form.

## Output Parsing

The runner accepts:

- JSONL-style event output
- text output
- streaming chunks where the CLI emits incremental content

OpenCode JSON text events with the `part.text` shape are normalized to the same
stream event contract as other adapters.

## Client-facing protocol contracts

- GitHub Copilot CLI: configure its documented custom provider environment to
  `http://127.0.0.1:<port>/v1` with provider type `openai` and wire API
  `responses`. `COPILOT_MODEL` is required, but BYOK accepts a custom model
  identifier such as `ghrouter/auto` or `ghrouter/codex`; it does not have to
  be a GitHub-hosted model ID. Tool calling and streaming are required by
  Copilot's custom provider contract. When a list name is not a well-known
  model, pair `COPILOT_PROVIDER_MODEL_ID` with a known capability profile
  (for example `gpt-5.4`) and send the Ghrouter list name through
  `COPILOT_PROVIDER_WIRE_MODEL`.
- Codex CLI: run `ghrouter connect codex --install` once, then evaluate
  `ghrouter connect codex`. This creates an isolated Codex `config.toml` with
  a named `ghrouter` model provider, `OPENAI_API_KEY`, and the `auto` model
  default. Codex model discovery uses the installed CLI's native
  `codex app-server --stdio` `model/list` protocol; request execution remains
  the documented `codex exec --json` path. The profile also prints the
  documented `codex login --with-api-key` bootstrap command when needed.
- Claude Code: configure `ANTHROPIC_BASE_URL` to the router origin. Requests
  arrive at `/v1/messages`; the router accepts string and content-block input,
  and forwards the user's gateway credential through ACL validation.
- Cursor Agent backend: Ghrouter invokes `agent --trust acp`, negotiates the
  ACP session, reads `models.availableModels`, selects a requested model with
  `session/set_config_option`, and sends prompts with `session/prompt`. The
  `CURSOR_API_ENDPOINT` emitted by `connect cursor` is a separate client-mode
  profile and is not claimed to be an OpenAI Chat Completions endpoint.
- OpenCode and MiMo backends: when detector capability is `protocol: acp`,
  Ghrouter starts `opencode acp --pure` or `mimo acp --pure`, creates an ACP
  session, selects the requested model from `configOptions.model`, and sends
  `session/prompt`. If ACP is not confirmed, the provider remains on its
  documented `run --format json` native adapter instead of being mislabeled as
  ACP.

The router filters client-only endpoint and access-token variables before
spawning a backend CLI. This prevents a client connection from recursively
pointing the backend at Ghrouter or leaking the router token to a provider.

### Native client profiles

- `ghrouter connect codex --install` writes an isolated Codex provider profile;
  `ghrouter connect codex` exports its `CODEX_HOME`, router key, and `auto`
  model selection.
- `ghrouter connect opencode` emits `OPENCODE_CONFIG_CONTENT` for the official
  OpenAI-compatible provider schema and runs with `opencode run --format json
  --pure`. The generated client profile is native JSON configuration; the
  server uses the confirmed ACP backend when available and the native JSON
  adapter otherwise.
- `ghrouter connect mimo` emits the equivalent `MIMOCODE_CONFIG_CONTENT`
  profile and runs with `mimo run --format json --pure`. The server uses the
  confirmed ACP backend when available and the native JSON adapter otherwise.
- `ghrouter connect pi` emits a native OpenAI-compatible profile; with
  `--install` it writes a dedicated `~/.config/ghrouter/pi/models.json` file
  and points `PI_CODING_AGENT_DIR` at that directory, using Pi's native RPC
  configuration without overwriting the user's default Pi profile. Pi is not
  an ACP connector.
- `ghrouter connect cursor` emits `CURSOR_API_ENDPOINT` for client-mode
  experiments only. The supported backend path is Cursor Agent ACP, which is
  detected and invoked independently of that custom endpoint profile.

## Local Brain

The `local_brain` runtime uses the official `mlx_lm.server` on MLX hosts and
`llama-server` on Linux/Windows, waits for a successful readiness endpoint and
registers it as an OpenAI-compatible provider. Set `managed_externally: true`
when launchd or another supervisor owns `mlx_lm.server`; Ghrouter probes and
uses that process but never starts, stops, or restarts it. A managed endpoint
is accepted only after `/v1/models` and a real text completion succeed. Tool
capability is advertised only after an independent real tool probe succeeds.
For an internally
owned runtime, `restart` enables bounded restart attempts and readiness checks.

The Brain's local model is currently a selector/text backend. It does not
execute tools, manage a sandbox, or run an agent loop, so tool-bearing
requests are routed only to a harness with independently verified native tool
support. A local model being available does not by itself prove tool support.

## Current Limits

- Automatic local provisioning is disabled unless `local_brain.auto_provision`
  is enabled.
- Automatic restart is opt-in and bounded by `restart_backoff` and
  `max_restarts`; it is not a warm process pool.
- Observed serve-capable OpenCode and MiMo installations use a serialized warm
  ACP session pool: the ACP process is initialized once, each request gets a
  fresh session, and a timeout, cancellation, EOF, or process failure
  invalidates the pool before the next request starts a clean process. Pi and
  other CLIs retain their native per-request adapters; an HTTP `serve` pool is
  not claimed without a validated provider contract.
- Optional `model_info.<model>.token_cost` values are local cost metadata in
  micro-units per 1,000 estimated tokens. They drive fusion budgets and
  telemetry only; unknown provider pricing remains unknown.
- Native OpenCode input cost is converted to the same micro-units-per-1,000
  representation. A zero native cost is classified as free; this is not used
  to infer a quota or subscription balance.
- Providers that are missing from PATH are skipped during auto-discovery.
