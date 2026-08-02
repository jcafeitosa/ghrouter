# Architecture

`ghrouter` is a local API gateway that sits in front of CLI-based AI providers.

## Request Flow

1. `main.go` loads configuration.
2. If no providers are configured, auto-discovery scans the PATH.
3. The startup bootstrap checks backend and model readiness.
4. The default entrypoint opens the interactive terminal dashboard and shows startup/provider status.
5. `ghrouter serve` listens on `127.0.0.1:9090` by default.
6. Requests land on `/v1/chat/completions` or `/v1/messages`.
7. The server routes the requested model or prompt intent through the live catalog to a provider runner.
8. The runner spawns the provider CLI and parses its output.
9. The server returns JSON or SSE to the client.
10. A health loop and catalog snapshot stay active while the server runs.

## Main Packages

- `internal/config`: YAML loading and config path resolution.
- `internal/detectors`: CLI auto-discovery.
- `internal/local_brain`: backend detection, model cache checks, startup bootstrap.
- `internal/providers`: provider process execution and stream parsing.
- `internal/server`: OpenAI-compatible and Anthropic-compatible HTTP handlers.
- `internal/catalog`: model catalog, slots, and health-aware selection structures.
- `internal/health`: periodic provider health checks and health snapshots.

## Design Notes

- The current product is intentionally local-first.
- Provider CLIs are executed directly; no hidden MITM layer is used.
- Routing keeps the OpenAI-compatible surface stable for `gh copilot` while using catalog and health state to improve selection.
