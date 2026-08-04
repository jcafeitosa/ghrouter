# ghrouter — Project Kanban

**Status:** Development: runtime foundation and compatibility path verified; production hardening remains • **Target:** Production-ready AI model router for GitHub Copilot CLI
**Repo:** https://github.com/jcafeitosa/ghrouter • **Default branch:** master • **Audited branch:** `codex/native-catalog-authority` • **Last Updated:** 2026-08-03

> The authoritative delivered/partial/not-implemented inventory is
> [`docs/implementation-status.md`](docs/implementation-status.md). This
> kanban contains historical planning detail and must not override runtime
> evidence.

---

## 🎯 Vision

> A small, transparent router that discovers local provider CLIs, exposes an OpenAI-compatible `/v1/chat/completions` endpoint for `gh copilot`, and routes with fallback, round-robin, bounded fusion, sticky and score-based selection. Automatic installation, model download, universal quota discovery and durable judge/cost policy remain roadmap or partial work. No MITM, no token scraping, documented CLI automation only.

---

## 📋 Epic Breakdown

### EPIC 1: Core Router & Provider Layer ✅ **FOUNDATION COMPLETE**
| ID | Task | Status | Owner | Notes |
|----|------|--------|-------|-------|
| 1.1 | Go module + project scaffold | ✅ Done | Dev | `go.mod`, `go.sum` |
| 1.2 | Types: Config, Provider, Route, OpenAI request/response | ✅ Done | Dev | `internal/types/types.go` |
| 1.3 | Config loader (YAML + env + defaults) | ✅ Done | Dev | `internal/config/config.go` |
| 1.4 | CLI auto-detector (claude, codex, opencode, mimo, pi, Cursor Agent) | ✅ Done | Dev | `internal/detectors/detector.go` |
| 1.5 | Provider runner (spawn headless, parse JSONL/text, SSE bridge) | ✅ Done | Dev | `internal/providers/runner.go` |
| 1.6 | HTTP server: `/v1/chat/completions`, `/v1/messages`, `/v1/models`, `/health` | ✅ Done | Dev | `internal/server/server.go`, `internal/server/anthropic.go` |
| 1.7 | SSE streaming + non-streaming responses | ✅ Done | Dev | `internal/server/stream.go` |
| 1.8 | Prefix routing (`cc/`, `cx/`, `oc/`, `mi/`, `pi/`, `cu/`) + fallback table | ✅ Done | Dev | `server.go:route()` |
| 1.9 | Build pipeline (fmt, vet, race, staticcheck) | ⚠️ Remote red / local verified | Dev | The current checkout passes all local gates. GitHub `master` is at `181b58d`; its CI currently fails `gofmt` on `internal/update/update.go`. The published tree also contains OMO worktree gitlinks, which make checkout cleanup fragile. |
| 1.10 | GitHub repo + README + config.example + kanban | ✅ Done | Dev | `jcafeitosa/ghrouter` |

---

### EPIC 2: Intelligent Routing & Combo Modes 🔨 **IN PROGRESS**
| ID | Task | Status | Owner | Notes |
|----|------|--------|-------|-------|
| 2.1 | **Health Check Loop** — periodic goroutine, per-provider, non-blocking | ✅ Implemented | Dev | Configurable loop with bounded checks and live health state |
| 2.2 | **Model Catalog** — live struct, TTL cache, capability tags, cost tier, classification (fast/cheap/code/long-context/tool-use/vision/autonomous), virtual slots | 🔨 Partial | Dev | Native metadata lists and capability slots are wired; unverified native/configured models and stale reserved lists are excluded; cost/tool-use/quota provenance remains incomplete |
| 2.3 | **Cooldown Manager** — per-model, error/timeout thresholds, exponential backoff, auto-reset | 🔨 Partial | Dev | Request failures create 30s–10m model cooldowns; configured zero-balance/reset metadata applies provider cooldown at startup; expiry requires a fresh probe; scheduled verification rotates bounded batches; provider quota APIs remain unavailable |
| 2.4 | **Fallback Chain** — sequential with health-aware skip, respects cooldown | ✅ Implemented | Dev | Non-stream execution retries the next healthy candidate and records telemetry; streaming selects before headers |
| 2.5 | **Round-Robin Pool** — distributes across healthy fallbacks, sticky option | ✅ Implemented | Dev | Healthy candidate rotation with cooldown-aware skips |
| 2.6 | **Fusion Mode** — parallel fan-out, first-complete wins, cancels others | ✅ Implemented | Dev | Chat, Responses, Anthropic Messages; judge remains policy-limited |
| 2.7 | **Sticky Sessions** — conversation ID → provider affinity, TTL, rebalance on failure | ✅ Implemented | Dev | Session affinity and unhealthy rebalance |
| 2.8 | **Auto-Capacity Switching** — picks by health/latency/capability/cost scoring | ✅ Implemented | Dev | Native capability/cost/health scoring; intent policy remains partial |
| 2.9 | **Circuit Breaker** — per-provider, auto-reset, hystrix-style | ✅ Implemented | Dev | Configurable failure threshold/open duration, half-open recovery probe, provider availability gating, and fallback-safe circuit errors |
| 2.10 | **Retry with Backoff** — per-request, configurable | ✅ Implemented | Dev | Bounded retries with backoff before stream headers |
| 2.11 | **Routing Engine v2** — unified interface for all modes, intent-aware | 🔨 Partial | Dev | Unified route modes exist; prompt-intent policy remains limited |

---

### EPIC 3: Provider Adapters (Per-CLI) 🔨 **PARTIAL**
| ID | Task | Status | Owner | Notes |
|----|------|--------|-------|-------|
| 3.1 | **Claude Adapter** — `--print --output-format=stream-json --no-session-persistence`, auth via `ANTHROPIC_API_KEY`/`claude auth login`, parse stream-json, health check | ✅ Implemented | Dev | `detector.go` + `runner.go` |
| 3.2 | **Codex Adapter** — `exec --json --ephemeral --skip-git-repo-check`, auth via `OPENAI_API_KEY`/`codex login`, parse JSONL, health check | ✅ Implemented | Dev | |
| 3.3 | **OpenCode Adapter** — ACP when the live handshake is confirmed, otherwise `run --format json --pure`, parse JSONL, health check | ✅ Implemented | Dev | Observed serve-capable installs reuse a serialized warm ACP process; native fallback remains version-sensitive |
| 3.4 | **Mimo Adapter** — ACP when the live handshake is confirmed, otherwise `run --format json --pure`, parse JSONL, health check | ✅ Implemented | Dev | Provider quota/auth can still block inference |
| 3.5 | **Pi Adapter** — `--mode json --print --no-session --no-context-files`, auth via `pi auth`/provider credentials, parse JSON, health check | ✅ Implemented | Dev | Native RPC catalog; real auth remains environment-dependent |
| 3.6 | **Cursor Agent Adapter** — `agent --trust acp`, ACP catalog/session/model selection, auth and prompt path | ✅ Implemented | Dev | ACP handshake and catalog are validated; inference remains plan/auth-dependent |
| 3.7 | **Adapter Interface** — unified `ProviderAdapter` interface for all CLIs | ✅ Implemented | Dev | `internal/providers/adapter.go` provides native invocation contracts |
| 3.8 | **Warm Pool** — reuse processes for CLIs with `serve` mode (opencode, mimo, pi) | 🔨 Partial | Dev | Warm ACP process/session pool is implemented for observed serve-capable OpenCode/MiMo; Pi and HTTP serve contracts remain unvalidated |

---

### EPIC 4: CLI Experience & Operations 📦 **PLANNED**
| ID | Task | Status | Owner | Notes |
|----|------|--------|-------|-------|
| 4.1 | `ghrouter init` — interactive wizard (detects CLIs, auth status, models, writes config.yaml) | ✅ Implemented | Dev | Native discovery and config generation |
| 4.2 | `ghrouter doctor` — validates CLIs, auth, models, connectivity; exit code 0=healthy | ✅ Implemented | Dev | Bounded startup checklist |
| 4.3 | `ghrouter config` — view/edit/get/set config values, YAML merge + validate | 🔨 Partial | Dev | View/export/import paths exist; full key-level editor remains |
| 4.4 | `ghrouter providers` — list detected + models + health, table + JSON output | ✅ Implemented | Dev | Native catalog and account state |
| 4.5 | `ghrouter models` — catalog with health/cooldown/capability/tags, filter/sort | 🔨 Partial | Dev | Catalog and generated capability lists are live; only verified models enter virtual lists, while rich CLI filtering/sorting remains |
| 4.6 | `ghrouter routes` — show routing table + resolved targets, dry-run mode | ✅ Implemented | Dev | Route and control-plane summaries |
| 4.7 | `ghrouter test <model>` — real smoke test with latency report | ✅ Implemented | Dev | Resolves and invokes the selected provider, including externally managed local MLX |
| 4.8 | `ghrouter live` — TUI monitor (real-time health, requests, cooldowns, fallback events) | ✅ Implemented | Dev | Bubble Tea dashboard with compact mode, overlays, palette, stale/loading states |
| 4.9 | `ghrouter ping` — quick connectivity check | ✅ Partial | Dev | |
| 4.10 | `ghrouter explain <model>` — shows routing decision, provider, fallback chain | ✅ Partial | Dev | |
| 4.11 | `ghrouter serve` — run as daemon with PID file, log rotation | ✅ Partial | Dev | |
| 4.12 | `ghrouter sync` — re-scan CLIs/models, update catalog | ✅ Partial | Dev | |
| 4.13 | Config hot-reload (SIGHUP) — zero-downtime | 🔨 Partial | Dev | SIGHUP reload with restart safeguards; no file watcher |
| 4.14 | `ghrouter version` / `--version` / `-v` — git describe | ✅ Partial | Dev | |

---

### EPIC 5: Observability & Production Hardening 📦 **PLANNED**
| ID | Task | Status | Owner | Notes |
|----|------|--------|-------|-------|
| 5.1 | Structured logging (slog) — request-id, latency, provider, model, tokens | ✅ Implemented | Dev | JSON/text/color and debug/info/error levels |
| 5.2 | Request tracing — unique ID propagated through stack | ✅ Implemented | Dev | HTTP, provider logs and persistence correlation |
| 5.3 | Usage metrics — tokens, latency, cost, errors per provider/model | ✅ Implemented | Dev | In-memory plus Prometheus endpoint |
| 5.4 | Rate limiting — token bucket per client IP / API key | ✅ Implemented | Dev | Configurable keyed windows |
| 5.5 | Graceful shutdown + drain — wait for in-flight (max 30s), cancel queued | ✅ Implemented | Dev | Context cancellation and bounded server shutdown |
| 5.6 | Warm process pool — reuse CLI subprocesses for `serve`-capable CLIs (opencode, mimo, pi) | 🔨 Partial | Dev | Validated for observed OpenCode/MiMo ACP; broader provider pools remain pending |
| 5.7 | Circuit breaker — per-provider, auto-reset, hystrix-style | ✅ Implemented | Dev | Configurable open/half-open/closed lifecycle integrated with provider runners and live availability |
| 5.8 | Health endpoint enrichment — /health/detailed with per-provider status, cooldowns, catalog snapshot | ✅ Implemented | Dev | `/health`, `/live`, `/metrics` expose live state |
| 5.9 | Readiness/Liveness probes — /ready, /live for k8s | ✅ Implemented | Dev | `/readyz` and `/livez` |
| 5.10 | Config versioning + backup + export/import | 🔨 Partial | Dev | Snapshots/export/import exist; rollback UI remains |
| 5.11 | Audit log — security events, config changes | 🔨 Partial | Dev | Startup and control-plane events; broader security event coverage remains |

---

### EPIC 6: Real-Time Monitoring & TUI 📦 **PLANNED**
| ID | Task | Status | Owner | Notes |
|----|------|--------|-------|-------|
| 6.1 | `ghrouter live` — TUI monitor (real-time health, requests, cooldowns, fallback events) | ✅ Implemented | Dev | Bubble Tea dashboard with compact mode, overlays, palette, stale/loading states |
| 6.2 | JSON/stream mode for automation — `ghrouter live --json` | ✅ Partial | Dev | |
| 6.3 | Structured logs with request-id correlation | ✅ Implemented | Dev | |
| 6.4 | Request history — in-memory ring buffer + optional persistence | ✅ Partial | Dev | |
| 6.5 | Token/cost/time metrics per request + aggregated | ✅ Partial | Dev | |

---

### EPIC 7: Catalog & Slots 📦 **PLANNED**
: ID | Task | Status | Owner | Notes |
|------|------|--------|------|-------|
| 7.1 | **providers** — detected CLIs, auth status, capabilities | ✅ Partial | Dev | |
| 7.2 | **connections** — provider ↔ model mappings, health | ✅ Implemented | Dev | Typed, routed, persisted and admin-editable |
| 7.3 | **pools** — round-robin/fusion groups, sticky sessions | ✅ Implemented | Dev | Typed, routed, persisted and admin-editable |
| 7.4 | **slots** — virtual stable models for gh copilot, auto-reclassify | ✅ Implemented | Dev | Functional capability slots with cooldown filtering |
| 7.5 | **routes** — pattern → provider + combo mode, fallback chains | ✅ Partial | Dev | |
| 7.6 | **history** — requestDetails, usageHistory, usageDaily | 🔨 Partial | Dev | SQLite request/client/connection/attempt/token/cost history is wired; retention screens remain |
| 7.7 | **health** — per-model health, cooldown, circuit breaker state | ✅ Implemented | Dev | Health/cooldown and provider circuit-open/half-open state are exposed through runner availability and live snapshots |
| 7.8 | **settings** — global config, cooldown params, health intervals | 🔨 Partial | Dev | Runtime config and reload exist; durable settings CRUD remains |

---

### EPIC 8: Security & Compatibility 🛡️ **ENFORCED**
| ID | Task | Status | Owner | Notes |
|----|------|--------|-------|-------|
| 8.1 | Only documented CLI automation flags/env vars | ✅ Enforced | Dev | Code review gate |
| 8.2 | No hidden protocol interception / token scraping | ✅ Enforced | Dev | |
| 8.3 | No fake compatibility bypassing login | ✅ Enforced | Dev | Fail closed on missing auth |
| 8.4 | Input validation, no shell injection | ✅ Enforced | Dev | |
| 8.5 | OpenAI Chat Completions compatibility | ✅ Implemented | Dev | `/v1/chat/completions`, `/v1/models`, SSE |
| 8.6 | Anthropic-compatible compatibility | ✅ Implemented | Dev | `/v1/messages` endpoint |
| 8.7 | Tool calls pass-through intact | ✅ Implemented | Dev | |
| 8.7 | No payload mutation beyond routing | ✅ Enforced | Dev | |
| 8.8 | Transparent to Copilot client (BYOK env vars) | 🔨 Conditional | Dev | Current source build and freshly generated launcher pass Responses/tool/streaming E2E; an older installed launcher can embed a stale binary, so rerun `ghrouter connect copilot --install` after upgrades and verify `/readyz` plus build identity |

---

### EPIC 9: Documentation 📦 **PARTIAL**
| ID | Task | Status | Owner | Notes |
|----|------|--------|-------|-------|
| 9.1 | `README.md` — professional, mermaid, badges, quickstart | ✅ Done | Dev | |
| 9.2 | `config.yaml.example` — complete with all options | ✅ Done | Dev | |
| 9.3 | `docs/architecture.md` — system design, data flow, routing logic, mermaid | ✅ Done | Dev | |
| 9.4 | `docs/providers.md` — each CLI: flags, auth, models, limitations, tested versions | ✅ Done | Dev | |
| 9.5 | `docs/routing.md` — combo modes, scoring, examples, decision trees | ✅ Done | Dev | |
| 9.6 | `docs/config.md` — full schema, env overrides, examples, auto-generated | ✅ Done | Dev | |
| 9.7 | `docs/cli.md` — every command, flags, examples | ✅ Done | Dev | |
| 9.8 | `docs/compatibility.md` — gh copilot matrix, OpenAI/Anthropic spec coverage | ✅ Done | Dev | |
| 9.9 | `docs/faq.md` — common issues, troubleshooting | ✅ Done | Dev | |
| 9.10 | `docs/performance.md` — benchmarks, tuning guide, p50/p95/p99 | ✅ Done | Dev | |
| 9.11 | `docs/security.md` — threat model, mitigations, audit log | ✅ Done | Dev | |
| 9.12 | `docs/compatibility-matrix.md` — model × provider × capability | ✅ Done | Dev | |
| 9.13 | `docs/troubleshooting.md` — error codes, solutions | ✅ Done | Dev | |
| 9.13 | `CHANGELOG.md` — Keep a Changelog format | ⏳ Pending | Dev | Not created; release history still needs ownership decisions |
| 9.14 | `LICENSE` (MIT) | ⏳ Pending decision | Dev | No license added because legal ownership/terms are unspecified |
| 9.15 | GitHub Actions CI — build, test, race, staticcheck, release | ✅ Implemented | Dev | `.github/workflows/ci.yml` runs formatting, module verification, vet, staticcheck, build and race tests |

---

### EPIC 10: Advanced Features (Post v1.0) 📦 **BACKLOG**
| ID | Task | Status | Owner | Notes |
|----|------|--------|-------|-------|
| 10.1 | Custom provider plugin system (Go plugin / WASM) | 📦 Backlog | Dev | |
| 10.2 | Remote provider support (Bedrock, Vertex, Foundry) | 📦 Backlog | Dev | |
| 10.3 | Multi-tenant sessions (API keys, quotas) | 📦 Backlog | Dev | |
| 10.4 | Built-in evaluation harness (prompt → expected → grade) | 📦 Backlog | Dev | |
| 10.5 | Model cost optimization (auto-pick cheapest meeting capability) | 📦 Backlog | Dev | |
| 10.6 | Container persistence + replay | 📦 Backlog | Dev | |
| 10.7 | A/B routing for gradual rollouts | 📦 Backlog | Dev | |

---

## 🛡️ Quality Gates (Zero Tolerance)

| Gate | Command | Required |
|------|---------|----------|
| Build | `go build ./...` | ✅ Every commit |
| Format | `gofmt -l .` (must be empty) | ✅ Every commit |
| Vet | `go vet ./...` | ✅ Every commit |
| Race | `go test -race ./...` | ✅ Every commit |
| Staticcheck | `staticcheck ./...` | ✅ Every PR |
| Live Integration | Test against ≥2 real CLIs | ✅ Every PR |
| Copilot Compat | `COPILOT_PROVIDER_BASE_URL=... copilot -p "test"` | ✅ Every PR |
| No Token Intercept | Code review check | ✅ Every PR |
| Docs Updated | Kanban + relevant `.md` files | ✅ Every PR |

---

## 🤖 Agent Orchestration Rules

| Role | Responsibility | Blocking |
|------|---------------|----------|
| **Dev Agent** | Implements task, writes tests, runs locally with real CLIs | — |
| **Critical Reviewer** | Reviews logic, architecture, edge cases; rejects if broken | Must approve |
| **Code Reviewer** | Style, patterns, security, performance (staticcheck, gosec) | Must approve |
| **QA Agent** | Runs full integration suite, verifies gh copilot compatibility | Must approve |

> **Rule:** No merge without all 4 approvals. Kanban updated at each transition.

---

## 📊 Current Sprint: **EPIC 2 — Functional model catalog and cooldown-aware routing**

| Task | Assignee | Started | ETA | Blockers |
|------|----------|---------|-----|----------|
| 1.9 Fix build pipeline (fmt, vet, race, staticcheck) | Dev | 2025-08-01 | ✅ Done | — |
| 2.1 Health Check Loop | — | ✅ Implemented | — | Configurable periodic loop with bounded checks |
| 2.2 Model Catalog | — | ✅ Implemented | — | Native metadata, capability lists and functional membership |
| 2.3 Cooldown Manager | — | ✅ Implemented | — | Per-model backoff, provider reset evidence, expiry restoration |
| 2.4 Fallback Chain | — | ✅ Implemented | — | Health/cooldown-aware candidates and stream-safe retry |
| 2.5 Round-Robin Pool | — | ✅ Implemented | — | Healthy candidate rotation |
| 2.6 Fusion Mode | — | ✅ Implemented | — | API fan-out and cancellation; durable judge policy remains partial |
| 2.7 Sticky Sessions | — | ✅ Implemented | — | Conversation affinity with failure rebalance |
| 2.8 Auto-Capacity Switching | — | ✅ Implemented | — | Health/latency/capability/cost scoring |
| 2.9 Circuit Breaker | — | 🔨 Partial | — | Cooldown/debounced health exist; full breaker policy remains |
| 2.10 Retry with Backoff | — | ✅ Implemented | — | Bounded retries before stream headers |
| 2.11 Routing Engine v2 | — | 🔨 Partial | — | Unified route modes exist; intent classifier remains |

---

## 📝 Changelog (This Session)

| Date | Change |
|------|--------|
| 2025-08-01 | Project scaffold, types, config, detector, runner, HTTP server, SSE, routing v1, README, config.example, kanban v1, GitHub repo created |
| 2025-08-01 | Added anthropic-compatible `/v1/messages` endpoint |
| 2025-08-01 | Fixed duplicate type definitions (health, estimateTokens) |
| 2025-08-01 | Kanban v2 — full requirements breakdown from user specs |
| 2025-08-01 | Build now passes (`go build ./...` succeeds) |
| 2026-08-02 | Pipeline gate 1.9 fixed: CI now runs gofmt, go mod verify, vet, staticcheck, build, `go test -race`; staticcheck and vet both green; sprint blockers cleared |
| 2026-08-02 | Native model wave: OpenCode verbose metadata, 1M-context/tool-use/effort catalog slots, live-catalog verification, and persisted automatic-list filtering for failed/unhealthy/active-cooldown models |

*Kanban is source of truth. Update on every commit. This file lives in repo root.*

---

## Documentation reconciliation — 2026-08-02

The previous board overstated several items as complete or blocked. The current
implementation and evidence are reconciled in
[`docs/implementation-status.md`](docs/implementation-status.md). In summary:

- Marked delivered: native CLI discovery/catalog, Bubble Tea TUI, fallback,
  round-robin, sticky selection, security hardening, port conflict handling,
  Copilot local compatibility, and SQLite request/usage foundation.
- Marked partial: local MLX/llama.cpp readiness, account metadata, fusion,
  adapter policy extraction, durable telemetry screens, ACL scopes, updates,
  broader warm pools, and operations.
- Kept as roadmap: silent installation/model download, Hugging Face retrieval,
  universal balances, multi-tenant controls, OpenTelemetry export, and
  production daemon supervision.
- Reconciled fusion claims: bounded fan-out, optional judge, first-complete
  cancellation and explicit known-cost budgets are implemented; durable judge
  and provider cost policy remain partial. A missing `LICENSE` is still not
  presented as a completed release surface.

Future status changes must include runtime evidence and update the status page,
the relevant guide, and this board together.
