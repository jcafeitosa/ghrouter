# Configuration

The router reads YAML config through `internal/config`.

## Main Fields

- `listen_port`
- `providers`
- `routes`

## Provider Fields

- `name`
- `type`
- `cli_path`
- `args`
- `env`
- `models`
- `timeout`
- `max_tokens`
- `work_dir`
- `auth_method`
- `auth_config`
- `account`
- `enabled`

## Account Data

`auth_config` may include an `account_json` field for provider plan and balance metadata.

The same information can also be supplied through environment variables:

- `GHR_PROVIDER_<NAME>_PLAN`
- `GHR_PROVIDER_<NAME>_BALANCE`
- `GHR_PROVIDER_<NAME>_BALANCE_CURRENCY`
- `GHR_PROVIDER_<NAME>_RESET_AT`
- `GHR_PROVIDER_<NAME>_ACCOUNT_JSON`

## Defaults

- If `listen_port` is zero, the server uses `9090`.
- If config cannot be loaded, the process falls back to auto-discovery.
- If no providers are present, the detector scans known provider binaries.

## Environment

- `GHR_CONFIG` overrides the config file path.

## Backup and Restore

- `ghrouter export` writes a JSON bundle with the current config and runtime snapshot.
- `ghrouter import <bundle.json>` restores a config file from a previously exported bundle.
