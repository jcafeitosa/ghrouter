# ghrouter — Project Kanban

**Status:** Development Sprint: core features ready • **Target:** Production-ready AI model router for GitHub Copilot CLI  
**Repo:** https://github.com/jcafeitosa/ghrouter • **Branch:** master • **Last Updated:** 2026-08-01

---

## 🎯 Vision

> A small, fast, transparent router that auto-discovers local provider CLIs (claude, codex, opencode, mimo, pi), exposes a fully OpenAI-compatible `/v1/chat/completions` endpoint for `gh copilot`, and routes with intelligent combo modes (fallback, round-robin, fusion, sticky sessions, auto-capacity switching, cooldowns). Zero-config first run. No MITM, no token scraping, fully documented CLI automation only.

---

## 📋 Epic Breakdown

### EPIC 1: Core Router & Provider Layer ✅ **FOUNDATION COMPLETE**
| ID | Task | Status | Owner | Notes |
|----|------|--------|-------|-------|
| 1.1 | Go module + project scaffold | ✅ Done | Dev | `go.mod`, `go.sum` |
| 1.2 | Types: Config, Provider, Route, OpenAI request/response | ✅ Done | Dev | `internal/types/types.go` |
| 1.3 | Config loader (YAML + env + defaults) | ✅ Done | Dev | `internal/config/config.go` |
| 1.4 | CLI auto-detector (claude, codex, opencode, mimo, pi) | ✅ Done | Dev | `internal/detectors/detector.go` |
| 1.5 | Provider runner (spawn headless, parse JSONL/text, SSE bridge) | ✅ Done | Dev | `internal/providers/runner.go` |
| 1.6 | HTTP server: `/v1/chat/completions`, `/v1/messages`, `/v1/models`, `/health` | ✅ Done | Dev | `internal/server/server.go`, `internal/server/anthropic.go` |
| 1.7 | SSE streaming + non-streaming responses | ✅ Done | Dev | `internal/server/stream.go` |
| 1.8 | Prefix routing (`cc/`, `cx/`, `oc/`, `mi/`, `pi/`) + fallback table | ✅ Done | Dev | `server.go:route()` |
| 1.9 | Build pipeline (fmt, vet, race, staticcheck) | 🚫 Blocked | Dev | Current source fails `go test ./...` and `go vet ./...` |
| 1.10 | GitHub repo + README + config.example + kanban | ✅ Done | Dev | `jcafeitosa/ghrouter` |

---

### EPIC 2: Intelligent Routing & Combo Modes 🔨 **IN PROGRESS**
| ID | Task | Status | Owner | Notes |
|----|------|--------|-------|-------|
| 2.1 | **Health Check Loop** — periodic goroutine, per-provider, non-blocking | 🚫 Blocked | Dev | Missing health types; not wired into server |
| 2.2 | **Model Catalog** — live struct, TTL cache, capability tags, cost tier, classification (fast/cheap/code/long-context/tool-use/vision/autonomous), virtual slots | 🔨 Partial | Dev | Source exists but has lock re-entry risk and no server integration |
| 2.3 | **Cooldown Manager** — per-model, error/timeout thresholds, exponential backoff, auto-reset | ⏳ Pending | Dev | 60s default, max 300s, threshold-based |
| 2.4 | **Fallback Chain** — sequential with health-aware skip, respects cooldown | ⏳ Pending | Dev | |
| 2.5 | **Round-Robin Pool** — distributes across healthy fallbacks, sticky option | ⏳ Pending | Dev | |
| 2.6 | **Fusion Mode** — parallel fan-out, first-complete wins, cancels others | ⏳ Pending | Dev | |
| 2.7 | **Sticky Sessions** — conversation ID → provider affinity, TTL, rebalance on failure | ⏳ Pending | Dev | |
| 2.8 | **Auto-Capacity Switching** — picks by health/latency/capability/cost scoring | ⏳ Pending | Dev | Scoring algorithm |
| 2.9 | **Circuit Breaker** — per-provider, auto-reset, hystrix-style | ⏳ Pending | Dev | |
| 2.10 | **Retry with Backoff** — per-request, configurable | ⏳ Pending | Dev | |
| 2.11 | **Routing Engine v2** — unified interface for all modes, intent-aware | ⏳ Pending | Dev | Replaces `route()` |

---

### EPIC 3: Provider Adapters (Per-CLI) 🔨 **PARTIAL**
| ID | Task | Status | Owner | Notes |
|----|------|--------|-------|-------|
| 3.1 | **Claude Adapter** — `--print --output-format=stream-json --no-session-persistence`, auth via `ANTHROPIC_API_KEY`/`claude auth login`, parse stream-json, health check | ✅ Implemented | Dev | `detector.go` + `runner.go` |
| 3.2 | **Codec Adapter** — `exec --json --ephemeral --skip-git-repo-check`, auth via `OPENAI_API_KEY`/`codex login`, parse JSONL, health check | ✅ Implemented | Dev | |
| 3.3 | **OpenCode Adapter** — `run --format json --no-remote`, auth via `opencode auth login`, parse JSONL, health check, supports `serve` mode for warm pool | ✅ Implemented | Dev | |
| 3.4 | **Mimo Adapter** — `run --format json --pure`, auth via `mimo auth login`, parse JSONL, health check, supports `serve` mode | ✅ Implemented | Dev | |
| 3.5 | **Pi Adapter** — `--mode json --print --no-session --no-context-files`, auth via `pi auth`/`GOOGLE_API_KEY`, parse JSON, health check, supports `serve` mode | ✅ Implemented | Dev | |
| 3.6 | **Adapter Interface** — unified `ProviderAdapter` interface for all CLIs | ⏳ Pending | Dev | Extract from runner |
| 3.7 | **Warm Pool** — reuse processes for CLIs with `serve` mode (opencode, mimo, pi) | ⏳ Pending | Dev | Performance critical |

---

### EPIC 4: CLI Experience & Operations 📦 **PLANNED**
| ID | Task | Status | Owner | Notes |
|----|------|--------|-------|-------|
| 4.1 | `ghrouter init` — interactive wizard (detects CLIs, auth status, models, writes config.yaml) | ⏳ Planned | Dev | Cobra v2 |
| 4.2 | `ghrouter doctor` — validates CLIs, auth, models, connectivity; exit code 0=healthy | ⏳ Planned | Dev | |
| 4.3 | `ghrouter config` — view/edit/get/set config values, YAML merge + validate | ⏳ Planned | Dev | |
| 4.4 | `ghrouter providers` — list detected + models + health, table + JSON output | ⏳ Planned | Dev | |
| 4.5 | `ghrouter models` — catalog with health/cooldown/capability/tags, filter/sort | ⏳ Planned | Dev | |
| 4.6 | `ghrouter routes` — show routing table + resolved targets, dry-run mode | ⏳ Planned | Dev | |
| 4.7 | `ghrouter test <model>` — smoke test with latency report, `--stream` flag | ⏳ Planned | Dev | |
| 4.8 | `ghrouter live` — TUI monitor (real-time health, requests, cooldowns, fallback events) | ✅ Partial | Dev | Bubble Tea |
| 4.9 | `ghrouter ping` — quick connectivity check | ✅ Partial | Dev | |
| 4.10 | `ghrouter explain <model>` — shows routing decision, provider, fallback chain | ✅ Partial | Dev | |
| 4.11 | `ghrouter serve` — run as daemon with PID file, log rotation | ✅ Partial | Dev | |
| 4.12 | `ghrouter sync` — re-scan CLIs/models, update catalog | ✅ Partial | Dev | |
| 4.13 | Config hot-reload (SIGHUP) — zero-downtime | ⏳ Planted | Dev | File watcher |
| 4.14 | `ghrouter version` / `--version` / `-v` — git describe | ✅ Partial | Dev | |

---

### EPIC 5: Observability & Production Hardening 📦 **PLANNED**
| ID | Task | Status | Owner | Notes |
|----|------|--------|-------|-------|
| 5.1 | Structured logging (slog) — request-id, latency, provider, model, tokens | ⏳ Planted | Dev | JSON + human modes |
| 5.2 | Request tracing — unique ID propagated through stack | ⏳ Planted | Dev | |
| 5.3 | Usage metrics — tokens, latency, cost, errors per provider/model | ⏳ Planted | Dev | In-memory + Prometheus exporter |
| 5.4 | Rate limiting — token bucket per client IP / API key | ⏳ Planted | Dev | Configurable |
| 5.5 | Graceful shutdown + drain — wait for in-flight (max 30s), cancel queued | ⏳ Planted | Dev | |
| 5.6 | Warm process pool — reuse CLI subprocesses for `serve`-capable CLIs (opencode, mimo, pi) | ⏳ Planted | Dev | |
| 5.7 | Circuit breaker — per-provider, auto-reset, hystrix-style | ⏳ Planted | Dev | |
| 5.8 | Health endpoint enrichment — /health/detailed with per-provider status, cooldowns, catalog snapshot | ⏳ Planted | Dev | |
| 5.9 | Readiness/Liveness probes — /ready, /live for k8s | ⏳ Planted | Dev | |
| 5.10 | Config versioning + backup + export/import | ⏳ Planted | Dev | |
| 5.11 | Audit log — security events, config changes | ⏳ Planted | Dev | |

---

### EPIC 6: Real-Time Monitoring & TUI 📦 **PLANNED**
| ID | Task | Status | Owner | Notes |
|----|------|--------|-------|-------|
| 6.1 | `ghrouter live` — TUI monitor (real-time health, requests, cooldowns, fallback events) | ✅ Partial | Dev | Bubble Tea |
| 6.2 | JSON/stream mode for automation — `ghrouter live --json` | ✅ Partial | Dev | |
| 6.3 | Structured logs with request-id correlation | ⏳ Planted | Dev | |
| 6.4 | Request history — in-memory ring buffer + optional persistence | ✅ Partial | Dev | |
| 6.5 | Token/cost/time metrics per request + aggregated | ✅ Partial | Dev | |

---

### EPIC 7: Catalog & Slots (9router-inspired) 📦 **PLANNED**
| ID | Task | Status | Owner | Notes |
|----|------|--------|-------|-------|
| 7.1 | **providers** — detected CLIs, auth status, capabilities | ✅ Partial | Dev | |
| 7.2 | **connections** — provider ↔ model mappings, health | ⏳ Planted | Dev | |
| 7.3 | **pools** — round-robin/fusion groups, sticky sessions | ⏳ Planted | Dev | |
| 7.4 | **slots** — virtual stable models for gh copilot, auto-reclassify | ⏳ Planted | Dev | |
| 7.5 | **routes** — pattern → provider + combo mode, fallback chains | ✅ Partial | Dev | |
| 7.6 | **history** — requestDetails, usageHistory, usageDaily | ⏳ Planted | Dev | SQLite optional |
| 7.7 | **health** — per-model health, cooldown, circuit breaker state | ⏳ Planted | Dev | |
| 7.8 | **settings** — global config, cooldown params, health intervals | ⏳ Planted | Dev | |

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
| 8.8 | Transparent to gh copilot (BYOK env vars) | ✅ Verified | Dev | `COPILOT_PROVIDER_BASE_URL` |

---

### EPIC 9: Documentation 📦 **PARTIAL**
| ID | Task | Status | Owner | Notes |
|----|------|--------|-------|-------|
| 9.1 | `README.md` — professional, mermaid, badges, quickstart | ✅ Done | Dev | |
| 9.2 | `config.yaml.example` — complete with all options | ✅ Done | Dev | |
| 9.3 | `docs/architecture.md` — system design, data flow, routing logic, mermaid | ⏳ Planted | Dev | |
| 9.4 | `docs/providers.md` — each CLI: flags, auth, models, limitations, tested versions | ⏳ Planted | Dev | |
| 9.5 | `docs/routing.md` — combo modes, scoring, examples, decision trees | ⏳ Planted | Dev | |
| 9.6 | `docs/config.md` — full schema, env overrides, examples, auto-generated | ⏳ Planted | Dev | |
| 9.7 | `docs/cli.md` — every command, flags, examples | ⏳ Planted | Dev | |
| 9.8 | `docs/compatibility.md` — gh copilot matrix, OpenAI/Anthropic spec coverage | ⏳ Planted | Dev | |
| 9.9 | `docs/faq.md` — common issues, troubleshooting | ⏳ Planted | Dev | |
| 9.10 | `docs/performance.md` — benchmarks, tuning guide, p50/p95/p99 | ⏳ Planted | Dev | |
| 9.11 | `docs/security.md` — threat model, mitigations, audit log | ⏳ Planted | Dev | |
| 9.12 | `docs/compatibility-matrix.md` — model × provider × capability | ⏳ Planted | Dev | |
| 9.13 | `docs/troubleshooting.md` — error codes, solutions | ⏳ Planted | Dev | |
| 9.13 | `CHANGELOG.md` — Keep a Changelog format | ⏳ Planted | Dev | |
| 9.14 | `LICENSE` (MIT) | ⏳ Planted | Dev | |
| 9.15 | GitHub Actions CI — build, test, race, staticcheck, release | ⏳ Planted | Dev | |

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
| gh copilot Compat | `COPILOT_PROVIDER_BASE_URL=... gh copilot "test"` | ✅ Every PR |
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

## 📊 Current Sprint: **EPIC 1 — Fix Build Pipeline & EPIC 2 — Intelligent Routing & Combo Modes**

| Task | Assignee | Started | ETA | Blockers |
|------|----------|---------|-----|----------|
| 1.9 Fix build pipeline (fmt, vet, race, staticcheck) | Dev | 2025-08-01 | 2025-08-01 | — |
| 2.1 Health Check Loop | — | — | — | 1.9 |
| 2.2 Model Catalog | — | — | — | 1.9 |
| 2.3 Cooldown Manager | — | — | — | 1.9 |
| 2.4 Fallback Chain | — | — | — | 1.9 |
| 2.5 Round-Robin Pool | — | — | — | 1.9 |
| 2.6 Fusion Mode | — | — | — | 1.9 |
| 2.7 Sticky Sessions | — | — | — | 1.9 |
| 2.8 Auto-Capacity Switching | — | — | — | 1.9 |
| 2.9 Circuit Breaker | — | — | — | 1.9 |
| 2.10 Retry with Backoff | — | — | — | 1.9 |
| 2.11 Routing Engine v2 | — | — | — | 1.9 |

---

## 📝 Changelog (This Session)

| Date | Change |
|------|--------|
| 2025-08-01 | Project scaffold, types, config, detector, runner, HTTP server, SSE, routing v1, README, config.example, kanban v1, GitHub repo created |
| 2025-08-01 | Added anthropic-compatible `/v1/messages` endpoint |
| 2025-08-01 | Fixed duplicate type definitions (health, estimateTokens) |
| 2025-08-01 | Kanban v2 — full requirements breakdown from user specs |
| 2025-08-01 | Build now passes (`go build ./...` succeeds) |
| 2025-08-01 | Staticcheck and vet still fail — need to resolve |

*Kanban is source of truth. Update on every commit. This file lives in repo root.*
