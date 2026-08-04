# Configuration

The router reads YAML config through `internal/config`.

## Main Fields

- `listen_port`
- `providers`
- `routes`
- `connections`, `pools`, `combos`
- `acl`
- `cooldown`
- `circuit`
- `health`
- `logging`
- `server`
- `verification`
- `local_brain`

## Provider Fields

NVIDIA NIM providers may define multiple enabled `accounts`. Each account
resolves its credential from `api_key_env` (or an explicitly configured
`api_key`) and requests rotate across available credentials. Empty or
disabled accounts are skipped, credentials are never included in JSON output,
and `auth_config.api_key_env` remains the single-key fallback.

- `name`
- `type`
- `cli_path`
- `base_url` (OpenAI-compatible HTTP provider, used by the supervised local brain)
- `args`
- `env`
- `models`
- `model_info` (per-model metadata such as `token_cost`, `cost_tier`, context,
  capabilities, and verification state)
- `timeout`
- `max_tokens`
- `work_dir`
- `auth_method`
- `auth_config`
- `account`
- `enabled`

## Control plane

`connections` are named provider/model bindings. `pools` expose a named set of
model references with a routing strategy; `combos` expose a named set and may
declare a judge model. Pools and combos are active virtual model names in
`/v1/models`, `/live`, and routing. A `fusion` route fans out healthy
candidates for Chat Completions, Responses, and Anthropic Messages and can call
its configured judge; `max_candidates` and `judge_timeout` bound the work.
`first_complete` cancels sibling candidates after the first valid result, and
`max_cost_micros` filters candidates with explicitly known `token_cost` values.
Named combo resources still use their configured selection strategy.

A `graph` route is the explicit task-graph variant for reasoning requests. It
runs up to two specialist nodes in parallel and may send their results to the
configured judge. It supports `max_candidates`, `max_cost_micros`, and
`judge_timeout`; ordinary `auto` routing does not enable it implicitly because
graph execution can multiply paid calls.

The current control-plane endpoint is `GET`/`PUT /v1/control-plane`; individual
resources also support `GET`/`PUT`/`DELETE` at
`/v1/control-plane/{connection|pool|combo}/{name}`. `GET`
returns the active resources. `PUT` replaces connections, pools, and combos
atomically through the same validation and reload safeguards as `SIGHUP`;
resource-level `PUT` and `DELETE` update one named item. All mutations require
the configured administrative token.

`rate_limit` applies a fixed one-minute request window keyed by
`X-Ghrouter-Client` or the remote address. `burst` caps the number accepted in
the current window; health endpoints are exempt.

`cooldown.enabled` can disable model/provider backoff explicitly. When enabled,
`default_duration` controls the first failure and `max_duration` caps
exponential backoff. `health.enabled`, `interval`, and `timeout` control the
passive provider health loop; health is enabled by default when omitted.
`health.test_prompt` is retained for configuration compatibility but is not sent
by the passive loop. Use `ghrouter probe` or `verification` when a real model
request is intended, because those calls can consume provider quota.

`circuit.enabled` enables a provider-level circuit breaker. After
`failure_threshold` executed request failures, the provider becomes
`circuit_open` and is excluded from normal selection until `open_duration`
passes. The first request after that interval is a half-open recovery probe;
success closes the circuit and failure reopens it. Client cancellation is not
counted as a provider failure, and retries internal to one request count as a
single circuit outcome.

`verification` is disabled by default because it performs real model calls.
When enabled, `startup` controls an initial scan, `interval` controls periodic
scans, `timeout` bounds each model probe, and `workers` limits concurrent probes
to avoid an accidental quota spike. Concurrent requests for the same model
share one in-flight probe, while different models continue in parallel up to
the worker limit. Scheduled scans can use `batch_size` and
`max_per_provider` to rotate through bounded subsets of a large catalog; the
manual `ghrouter verify-models` command still scans every eligible model unless
specific model IDs are supplied. Models already in cooldown are skipped;
successful probes remain eligible for generated lists, while failed probes enter
model cooldown and are removed from those lists. When the cooldown expires the
model remains excluded as `unknown` until a fresh successful probe confirms it
works again. A request for a `ghrouter/*` virtual model also performs a bounded
single-flight probe of at most three eligible candidates when the virtual list
has no verified member, unless `verification.enabled` is explicitly `false`.
This avoids requiring a separate manual verification command for the first
interactive request while retaining real provider, auth, quota, and cooldown
outcomes.

`local_brain` is the primary selector. An enabled empty block defaults to the
official `mlx-community/gemma-4-e2b-it-4bit` model and its `hf://` source. Local
MLX checkpoints are stored under `.localmodel/<owner>/<model>` by default. The
supervisor launches `mlx_lm.server` for internally owned MLX checkpoints, or
attaches to an externally managed endpoint when `managed_externally: true`.
Externally managed endpoints are never started, stopped, or restarted by
Ghrouter. Only the configured model is advertised by default; set
`allow_model_switch: true` only when the companion cache is present. If the
local Brain cannot become ready, routing enters degraded fast-backup mode. If
no eligible model exists, requests return an explicit no-model error.
Set `restart: true` only when bounded recovery is desired; `restart_backoff`
and `max_restarts` cap the supervisor attempts and readiness is rechecked after
each launch.

`storage.retention_days` prunes old request/attempt history, health samples,
audit events, and config snapshots when SQLite opens. Zero disables pruning.
Aggregated usage totals and current provider/model/control-plane snapshots are
retained.

`GET /metrics` exposes Prometheus text metrics for request totals, fallbacks,
active requests, provider usage/latency, model health, and cooldown expiry. It
is protected by the normal ACL middleware when ACL is enabled.

The running server reloads routing/control-plane and operational settings on
`SIGHUP`. Listener, storage, executable, and discovered-model changes require a
restart so an update cannot leave a partially replaced provider graph.

## Account Data

`auth_config` may include an `account_json` field for provider plan and balance metadata.

`model_info.<model>.cost_tier` accepts `free`, `cheap`, `standard`, `premium`,
or `unknown`. Ghrouter uses this field only when explicitly configured or when
the provider reports equivalent metadata. A missing `cost_tier` and missing
positive `token_cost` remain `unknown`; they are never inferred as free.

The same information can also be supplied through environment variables:

- `GHR_PROVIDER_<NAME>_PLAN`
- `GHR_PROVIDER_<NAME>_BALANCE`
- `GHR_PROVIDER_<NAME>_BALANCE_CURRENCY`
- `GHR_PROVIDER_<NAME>_RESET_AT`
- `GHR_PROVIDER_<NAME>_ACCOUNT_JSON`

## Client ACL

When `acl.enabled` is true, loopback clients authenticate with the value in
`token_env` (default `GHR_ACCESS_TOKEN`). The router accepts `Authorization:
Bearer`, `X-Ghrouter-Token`, and `x-api-key` for Anthropic-compatible clients.
Keep the token in the process environment or a platform secret store; do not
write it to provider configuration files.

## Defaults

- If `listen_port` is zero, the server uses `9090`.
- If config cannot be loaded, the process falls back to auto-discovery.
- If no providers are present, the detector scans known provider binaries.
- Cooldown defaults are 30 seconds initially and 10 minutes maximum.
- Health defaults are a 30-second interval and 10-second probe timeout.

## SQLite boundary

The optional local SQLite foundation stores request history, ordered attempts,
token estimates, usage totals, provider/model catalog snapshots, health
samples, config snapshots, startup audit events, and control-plane resources
asynchronously where applicable, with queue/error/lifecycle safeguards. It
must not store API keys, OAuth tokens,
prompts, tool arguments or provider credential files. The storage design and
current gap list are in
[`docs/storage.md`](storage.md).

## Environment

- `GHR_CONFIG` overrides the config file path.
- `GHR_RUNTIME_DIR` overrides the directory used for the local Ghrouter session record. By default it is stored below the user cache directory with restrictive permissions.

## Port conflicts

Ghrouter never terminates an arbitrary process holding `listen_port`. It only stops a process when the session record identifies its PID and the process command is Ghrouter. Otherwise it selects a free loopback port automatically.

## Backup and Restore

- `ghrouter export` writes a JSON bundle with the current config and runtime snapshot.
- `ghrouter import <bundle.json>` restores a config file from a previously exported bundle.

## Example-file caveat

The sample still contains roadmap-oriented sections outside the active schema.
`server.host` and the read/write/idle timeout fields are applied when the
listener starts; changing them requires restart. Non-loopback binds fail closed
unless `acl.enabled` is true. Logging can be configured in
YAML with `logging.level`, `logging.format`, `logging.output`, `logging.file`,
and `logging.color`; the equivalent `GHR_LOG_*` environment variables override
it. Route `mode`, control-plane resources, cooldown, health, and logging
settings are active.
# Model Policy

Use `model_policy.allowed` to constrain expensive provider families and
`model_policy.preferred` to bias `ghrouter/auto` toward lower-cost capacity.
Patterns match canonical IDs such as `cx/gpt-5.6-sol`, `cc/claude-opus-5`,
`cu/cursor-grok-4.5-high`, `cu/composer-2.5`, `oc/...`, and `nv/...`.
Models outside the allowlist are not routable or advertised by `ghrouter
models`. A policy only filters observed/configured IDs; it never simulates a
model or bypasses health, authentication, cooldown, or verification gates.
`model_policy.max_cost_micros` rejects models whose explicitly known per-1k
token cost exceeds the ceiling; unknown costs are not guessed. Set
`model_policy.max_discovery_age` to reject native catalog entries whose
`discovered_at` evidence is older than the configured duration. A zero value
disables each limit.
