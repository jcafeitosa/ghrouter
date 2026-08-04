# Compatibility matrix

This matrix records observed compatibility, not a promise that every CLI
version behaves identically. Last local verification: 2026-08-03.

| Surface | Detection/adapter | Catalog | Request path | Verification |
|---|---|---|---|---|
| Claude Code | `claude --print --output-format=stream-json` | Native when available | Anthropic `/v1/messages` and provider subprocess | Installed-client fixture prompt through Anthropic SSE verified; real provider auth/quota can still reject requests |
| Codex | `codex app-server --stdio` for `model/list`; isolated `model_providers.ghrouter` plus `codex exec --json` for requests | Native app-server catalog | OpenAI `/v1/chat/completions` and `/v1/responses` | Native catalog, installed custom provider profile, and local HTTP fixture verified; real provider auth/quota remains environment-dependent |
| OpenCode | `opencode run --format json --pure`; catalog `opencode models`; ACP `initialize` probe | Native plus ACP handshake | ACP `session/new`, model selection and `session/prompt`; native JSON fallback | Installed CLI returned a successful ACP session/prompt through Ghrouter; model discovery remains the native catalog command |
| Mimo | `mimo run --format json --pure`; ACP `initialize` probe | Native plus ACP handshake | ACP `session/new`, model selection and `session/prompt`; native JSON fallback | ACP session creation passed. The native Nvidia model timed out under the bounded probe, while the OpenRouter credential returned HTTP 402 for insufficient credits; inference remains environment-dependent |
| Pi | `pi --mode json --print`; native `models.json` profile | Native RPC catalog | OpenAI-compatible backend path | Bounded discovery, installed profile-backed fixture prompt, and explicit auth/provider errors |
| Cursor Agent | `agent --trust acp` | ACP `session/new` model catalog | ACP `session/set_config_option` plus `session/prompt` | Installed Cursor Agent `2026.07.23-e383d2b` completed ACP initialize, authentication, session creation and catalog discovery; live inference remains auth/plan-dependent |
| GitHub Copilot CLI | N/A, BYOK client profile | Custom IDs such as `ghrouter/auto` and `ghrouter/codex` | OpenAI `/v1/responses` (Responses SSE) | Profile, active-session port, local HTTP fixture, and installed-client prompt verified; authenticated provider inference remains dependent on configured backend credentials |

## Current Environment Blockers

The adapter and routing paths are implemented, but this host is not a
provider-ready release environment for every CLI. The latest real checks found:

| Provider | Observed state | Meaning |
|---|---|---|
| Claude Code | Missing Anthropic auth | The binary is installed, but no accepted Anthropic credential is configured. |
| Codex | Real `cx/gpt-5.4-mini` request passed | Native app-server catalog and request path are working for the authenticated account. |
| OpenCode | Real `oc/big-pickle` ACP request passed | Native model mapping and ACP request path are working. |
| MiMo | Real `mi/github-copilot/gpt-5-mini` request timed out under the bounded runtime request; native Nvidia also exceeded the 45-second direct test, and OpenRouter returned HTTP 402 `Insufficient credits` | The catalog is discovered, but provider capacity/credits must be repaired in the native CLI; Ghrouter preserves timeout and provider errors instead of claiming success. |
| Pi | Real `pi/github-copilot/gpt-5-mini` request returned HTTP 502 from Ghrouter; the native Pi path reported OAuth `401` | A local credential file is not proof that the provider refresh token is valid; Ghrouter keeps the public error redacted. |
| Cursor Agent | ACP initialize/catalog passed; live prompt hit the installed plan limit | ACP connectivity works, but the account plan must permit the selected model/request. |

These are environment-specific runtime results, not simulated health data. A
provider is only ready for generated lists after a successful explicit probe;
`ghrouter doctor` reports both its non-billing preflight and the separate
`router_ready` catalog-readiness result described in
[`docs/cli.md`](cli.md).

`/health` and `/readyz` report process and passive executable/provider health.
They do not send inference prompts and therefore do not override a later
provider auth, quota, plan, or timeout result.

## Metadata guarantees

The catalog preserves provider-reported metadata. It does not guarantee that a
CLI exposes effort, context window, pricing, vision, tool-use, quota, or
subscription information. Missing fields remain missing; they are not filled
with simulated values.

## Copilot model-name contract

In BYOK mode, Copilot requires a model identifier but accepts custom names.
Ghrouter virtual list names are therefore valid client-facing model IDs. The
wire model is the same name by default and is resolved by Ghrouter to a native
provider model. If Copilot needs a recognized capability profile, configure a
well-known `COPILOT_PROVIDER_MODEL_ID` separately from the list name in
`COPILOT_PROVIDER_WIRE_MODEL`. This distinction must not be mistaken for a
claim that every member of a list has the same capabilities.

## Not covered by this matrix

Automatic provider installation, Hugging Face downloads, local backend launch,
provider-specific cost discovery, and universal account balance discovery are
not implemented. First-complete fusion cancellation and budgets based on
explicit token costs are supported. Durable pools/connections have local
control-plane CRUD APIs and can be edited from the Bubble Tea control-plane panel.
