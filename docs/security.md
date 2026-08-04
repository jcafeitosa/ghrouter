# Security

`ghrouter` is designed to stay within documented CLI automation boundaries.

## Principles

- no hidden token interception
- no MITM of provider traffic
- no shell injection via untrusted input
- fail closed when auth or backend availability is missing

## Current Behavior

- provider commands are built from config and known flags
- subprocesses inherit only common runtime variables and credentials allowed
  for the selected provider; `Provider.Env` remains an explicit operator
  override
- requests are routed through local process execution only
- release updates are checked against GitHub and applied only with an explicit `update --apply`; `GHR_AUTO_UPDATE=1` is an opt-in startup path and still requires digest verification
- update application writes the downloaded binary to a configured target path instead of mutating provider traffic
- generated router keys are stored with restrictive local permissions and are masked in the dashboard
- an empty ACL allowlist accepts loopback only; generated GitHub/OpenAI/Anthropic keys have protocol endpoint scopes, while the configured environment token remains global
- `GET /v1/control-plane` and resource reads are operational surfaces; aggregate `PUT` and resource `PUT`/`DELETE` require the configured administrative token and never accept generated client keys
- when ACL is enabled, rate limiting keys requests by a short SHA-256 fingerprint of the authenticated token; changing `X-Ghrouter-Client` cannot bypass the window

## Remaining security work

SQLite request/usage metadata is local and sensitive. Retention controls,
per-route client quotas, and multi-tenant policy are not finished. The external `~/.9router` installation is outside
Ghrouter's ownership and was not modified or repaired by this project.

## Network binding

Ghrouter defaults to loopback. A configured non-loopback bind is rejected unless
ACL authentication is enabled, preventing an accidental unauthenticated LAN
listener. Port fallback never terminates an unrelated process.
