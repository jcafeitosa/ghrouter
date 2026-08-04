# Gap analysis

Updated 2026-08-03 from the current source and the runtime matrix in
[`compatibility-matrix.md`](compatibility-matrix.md). Private local audit
artifacts are not required to interpret this page.

## Closed in the current wave

| Area | Evidence |
|---|---|
| Virtual model slots | `ghrouter/auto` is now the Copilot slot; provider and capability lists are generated from native metadata. |
| Capability lists | `context-1m`, `long-context`, `reasoning`, `tool-use`, `vision`, and `effort` are exposed only when native metadata contains the corresponding signal. OpenCode verbose metadata now supplies context, output, cost, reasoning, tool-call, vision, and variants. |
| Functional membership | Runtime and persisted `ghrouter/*` lists filter missing-auth providers and explicit failed/unhealthy/cooldown model evidence before `/v1/models` and `/live` output; expired cooldowns remain excluded until a successful fresh probe. |
| Model cooldown | Requests and explicit `ghrouter probe <model>` invocations create exponential 30-second to 10-minute model cooldowns; virtual lists exclude them, provider recovery cannot clear a model cooldown, and expiry moves the model to `unknown` so it must be revalidated before returning to a slot. `/v1/models` exposes health and cooldown expiry. |
| List fallback | A virtual list resolves its best candidate first and appends other functional members as fallback candidates. |
| Auth probe cost | Native auth status checks are cached for 15 seconds so dashboard/list rendering does not spawn repeated CLI probes. |
| Copilot startup | The managed launcher waits up to 30 seconds for catalog startup and keeps the daemon detached from the shell process group. |

## Remaining release gaps

| Priority | Gap | Required completion |
|---|---|---|
| P1 | SQLite request records correlate request/client, selected connection, attempts, token estimates and explicitly known cost estimates; native model capability/context/cost metadata survives catalog persistence, but provider-specific cost discovery remains unavailable. | Add documented provider cost adapters without storing prompts or credentials. |
| P0 | Provider account/quota data is not universally discoverable from CLIs. | Implement provider-specific documented adapters where available; show `unknown` otherwise and never infer quota from a successful request. Explicit `available:false`/`healthy:false` metadata is now preserved. |
| P1 | Fusion semantics now cover every supported API surface; explicit `first_complete` cancellation and an opt-in budget for known token costs are available, while durable judge policy remains. | Add provider-specific cost adapters and durable judge policy. |
| P1 | Request history now carries per-request and per-attempt connection, token, cost, and client correlation; startup retention pruning is configurable; startup and control-plane audit events are redacted and queryable through storage and `/v1/audit`. | Add interactive retention/history screens and documented provider cost adapters. |
| P2 | `/metrics` exports real Prometheus counters/gauges for requests, provider usage/latency, model health, and cooldown expiry under the normal ACL boundary; internal request IDs correlate HTTP, provider logs, and persisted request records; ACL-enabled rate limits use authenticated token fingerprints. | Add OpenTelemetry traces/export, per-attempt spans, and richer metric histograms. |
| P1 | Real model verification now enumerates the live catalog, persists health/cooldown state in `model_info` as well as SQLite, skips active cooldowns, updates both runtime and generated lists, and persists from the embedded server when it owns an explicit config path. | Provider-specific quota-aware cadence remains. |
| P1 | Health interval/timeout and cooldown enable/default/max durations are now parsed from config and applied by the runtime; omitted values retain safe defaults. | Automatic model verification scheduling remains opt-in. |
| P2 | Logging and HTTP host/read/write/idle timeout settings are now parsed from config; environment logging overrides remain supported and server changes require restart. | Server-level TLS and remote deployment policy remain outside the local CLI scope. |
| P1 | Non-loopback server binds now fail closed unless ACL authentication is enabled. | TLS termination and remote deployment hardening remain external deployment responsibilities. |
| P1 | Connections, pools and combos are typed, routed, persisted, reloadable, exposed through authenticated aggregate/resource CRUD APIs, and editable from the TUI; connection-backed members and nested list expansion are now supported, and cycles are rejected at the boundary. | Add richer interactive creation workflows and policy validation for future resource types. |
| P1 | Invocation flags and prompt transport now use a unified per-CLI adapter interface; auth/discovery/parsing policies still have shared branches. | Extract auth, discovery, streaming, timeout, and cancellation contracts per CLI. |
| P2 | Full daemon supervision/restart policy remains incomplete. | `SIGHUP` reloads routing/control-plane/operational settings with restart-required safeguards; rate limiting is configurable per client/remotely keyed window; `/livez` and `/readyz` remain separate process liveness from usable-provider readiness. |
| P2 | Warm process pools and metrics export are incomplete; hot reload is limited to routing and operational settings with restart safeguards. | Implement bounded production operations before public release. |
| P2 | Responses coverage still needs broader provider/tool semantics and client matrix validation. | `/v1/responses` now supports routed non-stream output and `response.*` SSE events; expand official client/tool compatibility before release. |

The gap list is intentionally explicit: a schema, configuration key, or UI card
does not count as implemented until the runtime writes/reads it and a test or
real-client check proves the behavior.
