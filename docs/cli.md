# CLI

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
- `ghrouter config`
- `ghrouter ping`
- `ghrouter test <model>`
- `ghrouter init`
- `ghrouter update`
- `ghrouter reset`

## Current Entry Point

- `ghrouter` without arguments opens the interactive router dashboard.
- `ghrouter serve` starts the headless router server.
- The process handles SIGINT for graceful shutdown.

## Current Reality

`ghrouter init` writes a starter `config.yaml` with detected providers and a prompt for the listen port.

`ghrouter update` checks GitHub releases for a newer binary. Use `ghrouter update --apply` to download and replace the configured target path.

`ghrouter reset` lists the detected global config locations for known provider CLIs, including Cursor. Use `ghrouter reset --apply` to remove those files and return the CLI configs to their defaults.

`ghrouter live` opens the same interactive terminal dashboard used by the default entrypoint. It shows server status, health, telemetry, providers, models, routes, and recent activity, with `--json` reserved for machine-readable snapshots.
