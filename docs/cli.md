# CLI

Runtime probes are available without client authentication: `GET /livez` only
confirms that the process is alive; `GET /readyz` returns `200` only after at
least one provider has completed a passive healthy/degraded executable check,
otherwise it returns `503`. Neither endpoint proves that a model can complete
inference; use `ghrouter probe` or `verify-models` for that evidence. The
dashboard endpoint remains `GET /live`.
`ghrouter` now exposes a small operational CLI wrapper around the runtime.

## Implemented Commands

- `ghrouter serve`
- `ghrouter doctor`
- `ghrouter providers`
- `ghrouter models`
- `ghrouter sync`
- `ghrouter bootstrap`
- `ghrouter provision`
- `ghrouter export`
- `ghrouter import <bundle.json>`
- `ghrouter routes`
- `ghrouter explain <model>`
- `ghrouter live`
- `ghrouter attach [url]`
- `ghrouter config`
- `ghrouter ping`
- `ghrouter connect <copilot|codex|claude|opencode|mimo|pi|cursor>`
- `ghrouter test <model>`
- `ghrouter probe <model>`
- `ghrouter verify-models [model...]`
- `ghrouter init`
- `ghrouter update`
- `ghrouter reset`

`ghrouter --help`, `ghrouter help`, and `ghrouter -h` print the command
surface and global options.

## Harness Capability Inventory

`ghrouter sync` probes each installed harness with bounded `--help` and
`--version` commands and persists the result under `providers[].harness`.
The inventory contains the observed version, command names, flags, output
formats, model-selection support, effort/thinking support, tools, images,
sessions, MCP, RPC, server mode, headless mode, and ACP advertisement. An ACP
advertisement is not enough to select ACP: `acp_handshake_confirmed` becomes
true only after a successful `initialize` exchange. A failed or timed-out
probe remains observable and does not become a healthy capability.

The adapter uses this inventory before constructing a request. Model and
reasoning arguments are added only when the installed harness advertises the
corresponding capability. OpenCode and MiMo use ACP only after the handshake;
otherwise they stay on their native JSON path. Codex uses its native
app-server catalog and JSON execution path, Pi uses its JSON/RPC path, Claude
Code uses print plus stream-json, and Cursor uses its ACP path when confirmed.

Slash commands are an interactive control surface, not a request protocol.
Ghrouter records them when they are visible in help or an installed runtime,
but never sends a `/command` as a model prompt. The inventory is intentionally
version-specific: Claude Code currently exposes observed commands such as
`/doctor` and `/resume`, while Pi has a richer installed command set including
`/model`, `/settings`, `/compact`, `/session`, `/tree`, `/trust`, and
`/reload`. OpenCode, MiMo, and Codex do not expose a stable complete slash
catalog through their non-interactive help, so their unknown commands remain
unknown rather than being fabricated. The live snapshot and `ghrouter
providers --json` expose the same evidence for the TUI and automation.

## Current Entry Point

- `ghrouter` without arguments opens the interactive router dashboard.
- `ghrouter serve` starts the headless router server.
- The process handles SIGINT for graceful shutdown.
- Send `SIGHUP` to reload routes, model lists, pools, combos, ACL, and rate-limit settings without restarting. Port, storage, CLI path, and provider model changes are rejected and require restart.
- If `listen_port` is occupied by another Ghrouter process recorded in its runtime session, the stale session is asked to stop before the port is reclaimed.
- If the configured port belongs to another process, Ghrouter keeps it untouched and binds an available loopback port automatically. The selected port is reflected by `/health`, `/live`, and the dashboard.

## Current Reality

`ghrouter init` writes a starter `config.yaml` with detected providers and a prompt for the listen port.

`ghrouter doctor` is a non-billing startup preflight. Its `auth_ok` field means
that a documented environment, credential-file, or native CLI status signal was
found; it does not refresh OAuth, prove provider quota, or perform an inference
request. The output also includes `router_ready`, `router_reason`, and
`router_model_readiness`; these fields reflect whether at least one verified
healthy model is currently available for routing. `ready` and `router_ready`
are intentionally separate. Use `ghrouter probe <model>` for one bounded real
request and `ghrouter verify-models` for an explicit catalog-wide verification.
The same output includes `build.binary_sha256`, which identifies the executable
that performed the diagnosis.

`ghrouter update` checks GitHub releases for a newer binary. Use `ghrouter update --apply` to download and replace the configured target path; the release must provide a valid SHA-256 digest. This is not a background updater.

`ghrouter version --json` includes the executable SHA-256 and, when the Go
toolchain provides them, VCS revision, commit time, and dirty-worktree state.
Use `binary_sha256` to confirm that a client is invoking the expected build;
the semantic development version alone is not a unique build identity.

`ghrouter reset` lists the detected global config locations for known provider CLIs, including Cursor. Use `ghrouter reset --apply` to remove those files and return the CLI configs to their defaults.

`ghrouter live` opens the same interactive terminal dashboard used by the default entrypoint. It shows server status, health, telemetry, providers, models, routes, and recent activity, with `--json` reserved for machine-readable snapshots.

`ghrouter models` reports the normalized native catalog rather than only model
names. Each entry includes its provider, operational health, catalog
provenance, capability/slot metadata, and cooldown expiry when applicable;
generated `ghrouter/*` entries include only currently functional members.
Use `ghrouter models --functional-only` to show only healthy concrete models
and generated lists, or refine the output with `--provider NAME`, `--health
STATUS`, `--capability NAME`, and `--cost TIER`. The accepted capability names
include `vision`, `tool-use`, and `reasoning`; these filters are catalog
queries and do not perform a live probe.

`ghrouter test <model>` performs one real, bounded smoke invocation through the
resolved provider and reports its health, provider, model, and latency. For an
explicit catalog probe that also persists verification and cooldown state, use
`ghrouter probe <model>`. A
failed probe records the model in cooldown and returns its expiry; a successful
probe clears that model's cooldown and rebuilds the persisted provider and
automatic lists from models with healthy verification evidence. Probing is
explicit because validating every discovered model consumes provider quota.

`ghrouter verify-models` runs the same real probe concurrently for every
discovered model, or only the model IDs passed on the command line. A probe is
healthy when the CLI completes successfully and returns a bounded, non-empty
response. The request asks for a minimal `OK` response, but normal short
responses such as `hello` are accepted because different harnesses may wrap or
rephrase the answer. The `GHROUTER_MODEL_PROBE_OK` marker is accepted when a
provider emits it, but is not required. Empty output, explicit provider errors,
timeouts, and oversized output fail closed. Failed models enter cooldown, are
excluded from generated lists, and the verification state is persisted in
config; SQLite additionally records the historical probe and telemetry event
when enabled. The command returns one result per model and exits non-zero if
any model fails.

`ghrouter verify-models` does not treat an unprobed model as healthy evidence:
native models are tested before they can be trusted by a verified list. An
active cooldown is not probed again; the result reports `cooldown` and its
expiry. A quota/rate-limit failure can quarantine every model from that
provider until the explicit account reset time, when one is available.

`ghrouter provision --apply` is also verification-backed. It writes the
allowlisted execution plan, runs only actions marked safe, then repeats the
startup checklist. It exits non-zero and reports remaining actions when the
backend, model cache, or authentication is still not ready; executing a
command alone is never reported as readiness.

The local Brain is configured under `local_brain`. For an externally managed
official `mlx_lm.server`, set `managed_externally: true`, its exact model and
endpoint port; Ghrouter validates `/v1/models` and a real text completion but
does not own the process lifecycle. A measured fast model is used only as a
degraded backup when the Brain cannot start; when no model is eligible, request
handling reports an explicit no-model error.

Explicit `provider/model` targets remain bound to that exact model during
verification; a healthy fallback cannot make another model appear healthy.
Models already in cooldown are returned as `cooldown` with their expiry and are
not invoked again until the reset window, avoiding repeated paid calls.

Verification is bounded and concurrent, but it performs real provider calls and
can consume quota. It does not infer a subscription balance from catalog
output; only explicit account metadata and documented quota/rate-limit errors
can create a provider-wide reset cooldown.

When SQLite persistence is enabled, authenticated `GET /v1/audit?limit=100`
exposes the redacted startup and administrative audit history. Prompts,
credentials, API keys and tool arguments are never returned.

The verification result is also written to each model's `model_info` in the
config (`health_status`, `verified_at`, `cooldown_until`, and a redacted
`verification_error`). This keeps failed models excluded after restart even
when SQLite persistence is disabled; an expired cooldown changes the model to
`unknown`, keeps it out of generated lists, and makes it eligible for a fresh
probe. Only a successful probe restores it to those lists.

`ghrouter attach [url]` connects the dashboard to an already running `ghrouter serve` instance without starting a second listener. It reads the local `/live` snapshot; `--json` returns the snapshot and bootstrap report for automation.

`ghrouter connect <client>` prints an `eval`-safe environment profile for a
native client. It does not edit `~/.copilot`, `~/.claude`, OpenCode, MiMo, Pi,
or Cursor settings, and never prints or copies a provider credential. The
detector validates ACP only for CLIs that advertise and successfully negotiate
it; the generated request profiles remain native client configurations.

For Copilot, `ghrouter connect copilot --install` creates a managed launcher in
`~/.local/bin/copilot`. The launcher embeds the exact router executable used for
installation, waits for `/readyz` before invoking Copilot, and then exports the
documented `COPILOT_PROVIDER_*` variables. Re-run this command after rebuilding
or replacing ghrouter; a previously generated launcher can otherwise continue
to run an older binary even when the source tree is current.

Cursor has two intentionally separate paths. `sync` and `serve` detect and
invoke the installed Cursor Agent through ACP (`agent --trust acp`), including
model catalog discovery and `cu/<model>` requests. `ghrouter connect cursor`
only emits the Cursor client endpoint profile; that custom endpoint is not
claimed to be an OpenAI Chat Completions contract.

Codex is the exception to environment-only setup because its installed CLI
uses a named `model_providers` entry for custom endpoints. Run
`ghrouter connect codex --install` once before evaluating its profile; the file
is isolated under `~/.config/ghrouter/codex`.

The first server start creates three Ghrouter client keys in the user cache
with restrictive permissions. The dashboard displays only masked forms. The
keys are router credentials, not GitHub, OpenAI, or Anthropic provider keys.
