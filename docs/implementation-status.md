# Implementation status

Status verified on 2026-08-03 against the current source tree and the real
runtime matrix in [`compatibility-matrix.md`](compatibility-matrix.md). Private
local QA artifacts are not required to interpret this page.

This page is the source of truth for the difference between the product that
exists today and the product roadmap. A feature is not marked complete because
its configuration key, schema, or placeholder exists.

## Delivered

| Area | Current behavior | Evidence |
|---|---|---|
| CLI discovery | Detects `claude`, `codex`, `opencode`, `mimo`, `pi`, and Cursor Agent when installed. Cursor discovery uses its advertised ACP command and keeps the ACP stdin session open long enough for the startup handshake. | `internal/detectors` tests and runtime catalog |
| Native model catalog | Reads provider-native model listings where available, normalizes IDs and metadata (including OpenCode context, output, cost, reasoning, tool-call, vision, and effort variants), preserves explicit discovery failures, persists real verification state in `model_info` and SQLite, restores that evidence across restart, refreshes generated provider/automatic capability lists, and generates `ghrouter/auto`, provider lists, and evidence-backed capability lists such as `ghrouter/context-1m`, `ghrouter/long-context`, `ghrouter/reasoning`, `ghrouter/tool-use`, `ghrouter/vision`, and `ghrouter/effort`. Native models without a real successful probe remain `unknown` and are excluded from generated lists; explicit failed/unhealthy/cooldown evidence is also excluded, and an expired cooldown stays excluded until a fresh successful probe. Restored verification evidence is sufficient for routing only when the catalog contains a successful timestamp; persisted verification errors remain fail-closed. | `ghrouter models --json`, detector/server/CLI/storage tests |
| Authentication checks | Uses documented environment/file/native status checks for the supported CLIs without scraping or printing credentials. | Claude/Codex native status checks and runtime QA |
| HTTP compatibility | OpenAI Chat Completions, `/v1/responses` with routed non-stream and `response.*` SSE, `/v1/models` with observed-inventory and `functional_only` readiness views, Anthropic `/v1/messages`, `/health`, and `/live` are implemented. | Broader official Responses/tool/client matrix validation |
| Routing basics | Explicit model/prefix routing, generated provider/capability lists, canonical `provider/model` list references, typed connections/pools/combos as virtual model resources, auth/account-reset/cooldown-aware selection, model failure backoff, list-member fallback, bounded post-failure verification recovery for automatic virtual routes, automatic exclusion of failed models, cooldown expiry re-verification, exact-target model verification, round-robin, sticky sessions, passive health-aware selection, provider circuit breaker with half-open recovery, active route `mode`, retries before a stream commits headers, and automatic loopback port fallback. | `internal/server`, `internal/catalog`, and `internal/providers` tests plus real `/v1/chat/completions` QA |
| Intelligent selection | Free-first selection inside the capability-compatible set; long-context, coding, tools, vision, reasoning, quota evidence, observed latency and error rate influence ranking; optional local Brain ranking is constrained to live catalog IDs, fails closed, and preserves a redacted selection reason in audit/explain surfaces. Configured maximum known token cost and native discovery age limits now apply consistently to automatic, explicit, fallback and advertised routes. | `internal/server` routing, Brain selector, catalog latency and telemetry tests |
| Provider execution | Uses per-CLI invocation adapters for documented model flags and prompt transport, bounded subprocesses, cancellation, typed empty-output errors, Codex envelope parsing, and redacted public errors. | `internal/providers` adapter/runner tests and `go test -race` |
| Native client profiles | `ghrouter connect copilot`, `connect codex`, `connect claude`, `connect opencode`, `connect mimo`, `connect pi`, and `connect cursor` emit shell-safe profiles without editing global client configuration. OpenCode/Mimo use native JSON client configuration and ACP server sessions when the live handshake is confirmed, with native JSON fallback. Pi uses native RPC `models.json`; its profile-backed fixture prompt validates configuration and parsing, not provider-side inference. | `internal/cli/connect.go`, `internal/detectors`, `internal/providers` |
| Copilot path | The generated Responses profile, active-session port handling, `/health`, `/v1/models`, structured Responses SSE, and a fixture-backed prompt through the installed Copilot binary are tested. | `internal/cli` and `internal/server` tests plus local runtime evidence; authenticated provider inference remains environment-dependent |
| Bubble Tea TUI | Dashboard, provider/model/routes/control-plane/activity/settings views, keyboard navigation, filtering, loading/stale states, telemetry, topology stream animation, masked client keys, resource JSON editing, and attach mode exist. | PTY QA in runtime artifact |
| Security baseline | Loopback default, hardened local key files, fail-closed empty ACL outside loopback, update digest verification, no token scraping, and redacted provider errors. | Security tests and audit artifact |
| SQLite foundation | WAL schema, provider/model catalog snapshots including native capability/cost/context/provenance and observed latency P50/P95 metadata, health samples, config snapshots, startup and administrative audit events, authenticated redacted audit listing, request/usage persistence APIs, bounded queue lifecycle, error accounting, close behavior, and file permission repair exist. | `internal/storage` and `internal/server` tests |
| Runtime tuning | `health.enabled`, `health.interval`, `health.timeout`, `cooldown.enabled/default_duration/max_duration`, and `circuit.enabled/failure_threshold/open_duration` are parsed and applied; defaults are preserved when omitted. The passive health loop does not send `health.test_prompt`; real provider calls are explicit through `probe`/`verification`. | `internal/config`, `internal/catalog`, `internal/providers`, and `internal/server` tests |
| Brain resource admission | The Brain selector has bounded auxiliary concurrency and observes host resource pressure. On macOS, the sampler uses real unified-memory evidence from `sysctl` and `vm_stat`; `/health` exposes timestamped metric provenance, and degraded/emergency states shed auxiliary Brain work without blocking deterministic provider routing. Unsupported host metrics remain explicitly unavailable. | `internal/resourcegov` tests and live `/health` QA |
| Logging configuration | YAML logging level/format/destination/file/color are applied at CLI startup, with environment overrides and stderr-safe defaults. | `internal/observability` tests and CLI startup |
| HTTP server tuning | YAML host and read/write/idle timeouts are applied at startup; listener changes remain restart-only for safe lifecycle management. | `internal/server` runtime and port tests |
| Scheduled model verification | Opt-in startup/periodic real probes use bounded workers/timeouts, fair rotating batches (`batch_size`/`max_per_provider`), enumerate the live native catalog rather than only stale YAML model entries, skip cooldown models, single-flight concurrent probes for the same model, update the live catalog, and persist verification state to the configured YAML path for embedded runtimes; disabled by default to avoid surprise quota use. | `internal/server` verification path and CLI probe tests |
| Storage retention | `storage.retention_days` prunes raw requests/attempts, health, audit, and config snapshots transactionally at SQLite startup while preserving aggregates and current snapshots. | `internal/storage` retention tests |

## Partial or intentionally limited

| Area | What exists | What is still missing |
|---|---|---|
| Local MLX/llama.cpp | Brain-first runtime detects the host backend, attaches to externally managed official `mlx_lm.server` endpoints or launches the official server for internally owned MLX runtimes, verifies `/health` or `/v1/models` plus real text inference, exposes the endpoint as an OpenAI-compatible local provider, and only advertises tool capability when independently verified. | A measured fast backup is used when the Brain is unavailable; no eligible model returns an explicit request error. A sandboxed local tool executor/agent loop, provider-specific licensing automation, and broader HTTP warm pools are not implemented. |
| Routing intelligence | Health/latency/capability-aware scoring, cost tiers, and bounded multilingual keyword intent routing for tools, vision, cost, latency, context, code and reasoning. | A learned/configurable classifier, reliable cost-aware selection from provider billing data, and a full independent routing policy engine. |
| Fusion | Chat Completions, Responses, and Anthropic Messages `fusion` routes fan out healthy candidates, support an optional judge, bounded candidate count, judge timeout, explicit first-complete cancellation, and an opt-in budget that admits only candidates with known token costs under the ceiling; failures are recorded per model. | Provider-specific cost adapters and durable judge policy. |
| Provider accounts | Configurable plan/balance/reset metadata can be supplied and displayed without secrets; explicit HTTP 429/402 capacity responses and bounded `Retry-After` evidence suppress provider routing; explicit zero-balance with a future reset and explicit unavailable/unhealthy quota JSON suppress routing; explicit evidence is preserved. | A universal subscription/quota/balance API for every CLI; most CLIs do not expose a stable documented balance endpoint. |
| Cursor | Cursor Agent ACP detection, session/model catalog discovery, model selection, and prompt execution are implemented. ACP-discovered models are explicit evidence only until a real probe succeeds; unverified models are listed as `unknown` and excluded from generated automatic lists. | A stable, documented, OpenAI-compatible custom endpoint contract for Cursor client mode; real inference still depends on installed Cursor auth, plan and quota state. |
| OpenCode/Mimo/Pi | OpenCode/Mimo have native invocation/discovery paths plus live ACP handshake probes; Pi uses native invocation/discovery and RPC `models.json`. All three preserve explicit unavailable/timeout/auth states and persisted model verification state. | Provider-specific guarantees when a binary is absent, its auth is missing, its quota is exhausted, or its native listing command is interactive/slow. An ACP handshake does not prove prompt inference; no fake fallback catalog is used. |
| SQLite telemetry | Request history, request/client/connection correlation, ordered attempts with connection identity, token estimates, explicitly known cost estimates, native model metadata, usage totals, catalog, health samples, config snapshots, startup and administrative audit events, and connection/pool/combo persistence are wired. | Provider-specific cost discovery and retention screens. |
| Observability | TUI telemetry including the animated model graph and observed Brain Log, `/health`, `/live`, `/metrics`, `/v1/audit`, request counters, per-model latency P50/P95, fallback state and cooldown gauges; `/health.model_readiness` separates catalog, verified, verified-healthy, healthy, degraded, unhealthy, cooldown and unknown model counts; internal request IDs reach provider logs and persisted request records without leaking into public payloads. | OpenTelemetry spans/export, richer token/cost metrics, and interactive TUI history views. |
| Client ACL | Generated GitHub/OpenAI/Anthropic-shaped router keys, loopback protection, protocol endpoint scopes, admin-only control-plane mutation, and authenticated-token keyed rate limiting. | Per-route quotas, rotation policy UI, and multi-tenant isolation. |
| Updates | Release lookup and explicit `update --apply` with mandatory SHA-256 verification. | Background scheduler, unattended restart, rollback orchestration, and signed metadata beyond the required digest. |
| Operations | Graceful shutdown, export/import, attach, doctor, sync, automatic port selection, separate `/livez`/`/readyz` probes, configurable client/remotely keyed rate limiting, SIGHUP reload with restart safeguards, validated connection/pool/combo composition with cycle rejection, and warm ACP sessions for observed serve-capable OpenCode/MiMo providers. | Broader provider warm pools, daemon supervision, and log rotation. |

## Not implemented

These items are roadmap work, not supported behavior:

- unattended/default installation of provider CLIs or local backends;
- implicit model selection, automatic license acceptance, or downloads without
  an explicit local-brain source and opt-in provisioning;
- durable 9router-style settings CRUD and control-plane management screens;
- provider-plugin/WASM extension loading;
- multi-tenant quotas and remote providers;
- A/B routing, evaluation harnesses, replay, and container persistence;
- a universal provider subscription/balance reader;
- a claim that all discovered models expose reliable effort, context, vision,
  tool-use, cost, or quota metadata.

## Verified gates

The current tree was checked with:

```text
gofmt -l .
go mod verify
go vet ./...
go test ./... -count=1 -timeout 600s
go test -race ./... -count=1 -timeout 900s
go build ./...
git diff --check
```

The Go tests, race tests, vet, build, formatting, module verification, staticcheck
and diff checks pass for the current implementation. These checks do not turn
the partial and roadmap items above into supported features. Real-provider
validation on the development host included Codex and OpenCode HTTP requests,
Claude/Codex status checks, fixture-backed installed Claude and GitHub Copilot
prompts, the OpenAI HTTP/SSE surface, and a Bubble Tea PTY session. MiMo and Pi
remain blocked by provider-side timeout/auth evidence, while authenticated
provider inference remains dependent on each CLI's configured credentials,
quota, and plan.

## Documentation rule

Every new feature must update this page and the relevant guide in the same
change. Configuration keys or database tables must not be described as active
until the runtime reads or writes them and a test covers the behavior.
