# Troubleshooting

## The server exits on startup

Run `ghrouter doctor --json` and `ghrouter sync --json` first. Check whether
config is present and whether at least one provider CLI is available and
authenticated on PATH.

## A model is not found

Run `ghrouter models --json`. The model must appear in the native catalog or in
an explicit route. For a local brain, configure `local_brain.model` and an
explicit `local_brain.source`, then enable `local_brain.auto_provision` if a
download is intended. A missing source is never guessed.

## The local brain does not start

Run `ghrouter doctor --json` and inspect the `local_brain` configuration. On
macOS the default backend is MLX; on Linux and Windows it is llama.cpp. The
runtime must answer `/health` or `/v1/models` before Ghrouter accepts traffic.
If readiness fails, the child process is terminated and the router exits with
the concrete backend/model error. Check the selected port and the model root
under `.localmodel/` (or the path set by `GHR_LOCAL_MODEL_ROOT`).

## A provider returns an error

Verify the CLI binary, native auth state, flags, and working directory for that
provider. Use `ghrouter probe <model>` with a real model. Missing auth, timeout,
empty output, and unavailable binaries are deliberately reported separately.
Check both `ready` and `router_ready` in `ghrouter doctor --json`: the former is
the non-billing preflight, while the latter requires a verified healthy model.
If `router_ready` is false, inspect `router_reason` and run an explicit probe or
`ghrouter verify-models` before retrying the client.
If `ghrouter doctor` reports `auth_ok` but a request returns `401` or `429`, that
is not contradictory: `doctor` only found a local credential/status signal;
the provider still rejected refresh, authorization, quota, or capacity. Fix the
native CLI account or wait for its documented reset, then probe again.

`/health` reports provider health separately from `health.model_readiness`:
`catalog` counts discovered models, `verified` counts models with a successful
probe recorded, and `verified_healthy` counts models currently eligible for
automatic routing. A provider can be healthy while its catalog still contains
models that are `unknown`; use `/readyz` and `verified_healthy` for routing
readiness rather than provider count alone.

## OpenCode models are missing

Ghrouter runs the official `opencode models` catalog command. Verify the binary
in the same environment as Ghrouter with `command -v opencode`, or set
`OPENCODE_INSTALL_DIR` when using the OpenCode installer. The installer may
place the binary in `$HOME/bin` or `$HOME/.opencode/bin`, which Ghrouter also
checks. If no binary is present, configured models remain labeled as
configured and are not treated as a live native catalog.

If the catalog is smaller than the native output, compare `opencode models` with
`ghrouter models --json`. Ghrouter keeps upstream namespaces in IDs such as
`oc/zenmux/...` and `oc/nvidia/...`; it also accepts model IDs containing `~`.
The first `oc/` segment is Ghrouter's adapter namespace, not a replacement for
the provider namespace returned by OpenCode.

When an OpenCode provider owns the model, the ACP request maps `oc/big-pickle`
to OpenCode's native `opencode/big-pickle`. Models from an upstream provider
keep their native path, such as `oc/github-copilot/gpt-5-mini` to
`github-copilot/gpt-5-mini`. This prevents the CLI error `Model not found:
big-pickle/.` caused by sending only the suffix.

If Mimo or another CLI reports quota exhaustion, the router preserves that
provider error and does not mark the catalog healthy. Check the CLI's native
account/provider status and retry after the provider quota reset; a discovered
model is not proof of available inference capacity.

## The dashboard is slow to open

Native model discovery is bounded but may take several seconds for slow or
interactive CLIs. Use `ghrouter live --json` or attach to an existing server;
the HTTP `/live` endpoint returns the last bootstrap report and refreshes its
diagnostics asynchronously. Do not increase timeouts indefinitely or treat a
timeout as a valid catalog.

## Staticcheck or tests fail

Fix the failing package, then rerun:

```bash
go test ./... -count=1
go test -race ./... -count=1
go build ./...
go run honnef.co/go/tools/cmd/staticcheck@v0.7.0 ./...
```
