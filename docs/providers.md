# Providers

`ghrouter` currently recognizes these provider families:

- `claude-code`
- `codex`
- `opencode`
- `mimo`
- `pi`
- `cursor`

## Auto-Discovery

The CLI detector scans the PATH for known binaries and builds provider definitions from them.

## Execution

Each provider is executed as a subprocess with:

- provider-specific flags
- inherited environment variables
- a working directory
- request prompt on stdin

## Account Metadata

Providers may expose optional account metadata for routing and balance-aware selection.

The router reads these inputs from `auth_config` or environment variables:

- `account_json`
- `plan`
- `balance`
- `balance_currency`
- `reset_at`

The account payload is surfaced in:

- `ghrouter providers`
- `ghrouter live`
- model weighting for catalog slot selection

If `account_json` is present, it takes precedence and should contain the same fields in JSON form.

## Output Parsing

The runner accepts:

- JSONL-style event output
- text output
- streaming chunks where the CLI emits incremental content

## Current Limits

- Auto-install is not implemented.
- Model download orchestration is not implemented.
- Providers that are missing from PATH are skipped during auto-discovery.
