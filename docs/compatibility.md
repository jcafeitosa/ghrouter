# Compatibility

The server exposes three compatible client surfaces:

- OpenAI Chat Completions
- OpenAI Responses
- Anthropic Messages

## Implemented Endpoints

- `POST /v1/chat/completions`
- `POST /v1/messages`
- `GET /v1/models` (observed inventory; `?functional_only=true` returns only verified, healthy, routable models)
- `POST /v1/responses` with non-stream and SSE response events
- `GET /health`

## Streaming

Streaming uses SSE for both OpenAI-style and Anthropic-style request handling.
Provider failure after headers are committed is represented by a terminal
protocol error event; it is not reported as a successful completion.

## Client matrix

| Client/surface | Status | Notes |
|---|---|---|
| GitHub Copilot CLI | Verified for the current source build and a freshly generated launcher | `connect copilot` emits the Responses custom-provider environment; the installed Copilot binary completed non-interactive prompt and streaming tests through `/v1/responses` when pointed at the current router build. An older pre-existing launcher can still point at a stale binary and must be regenerated after upgrades. |
| OpenAI-compatible clients | Implemented | `/v1/chat/completions`, JSON and SSE. Tool behavior depends on native provider events. |
| Claude Code client | Installed-client fixture prompt verified | The installed Claude binary completed a non-interactive streaming prompt through `/v1/messages` with exit code 0; real provider auth/quota remains environment-dependent. |
| Cursor client | Limited | `connect cursor` emits a profile, but Cursor's custom endpoint is not claimed as an OpenAI Chat Completions contract. |
| Cursor Agent backend | ACP adapter and catalog support | `agent --trust acp` is auto-discovered when installed; `initialize`, authentication, `session/new`, model selection and `session/prompt` are implemented. Catalog discovery is verified on Cursor Agent `2026.07.23-e383d2b`; real inference remains dependent on Cursor auth and plan state. |
| Provider backends | Adapter and native catalog support | Claude, Codex, OpenCode, Mimo, Pi and Cursor Agent are auto-discovered when installed. Codex uses its native app-server protocol; OpenCode/Mimo use ACP when the live handshake is confirmed and otherwise use native JSON fallback; Pi uses native RPC; Cursor uses ACP for catalog and requests. On the current host, OpenCode completed an ACP request, while MiMo's provider prompt remained blocked by native timeout and OpenRouter credit evidence. |

Compatibility is version-sensitive. Run `ghrouter doctor`, `ghrouter models`
and `ghrouter probe <model>` against the installed CLI instead of assuming that
model, effort, context, quota, or tool metadata exists.

The managed Copilot launcher embeds the exact `ghrouter` executable that ran
`connect copilot --install`. Before starting Copilot it waits for `/readyz`, not
just `/health`; this prevents a live but not-yet-routable process from causing
retries against `ghrouter/auto`. After replacing or rebuilding ghrouter, rerun
the install command and compare `ghrouter version --json` with the binary used
by the launcher.

OpenCode and Mimo advertise ACP subcommands and their live `initialize`
handshakes are validated during discovery on the current installed binaries.
When that capability is recorded, their request connection path creates an ACP
session, selects `configOptions.model`, and prompts; otherwise the native JSON
CLI plus generated OpenAI-compatible provider configuration is used. Codex
uses its native app-server model protocol, not ACP. Pi uses its native RPC
configuration. Cursor Agent uses its ACP session protocol for both model
discovery and backend requests.

Model discovery alone does not prove inference health. Native or ACP models
without a successful real probe remain `unknown` and are excluded from
generated automatic lists; explicit requests may target them so the provider's
real auth, quota, or plan response can be observed.
