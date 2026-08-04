# Performance

The current implementation favors bounded subprocesses, simple parsing, and a
warm ACP path where the installed harness exposes a validated server mode.

## Current Characteristics

- Provider requests are executed as subprocesses.
- Streaming is bridged through SSE.
- Health and catalog code are separated from the HTTP request path.
- Native model discovery is bounded (currently up to 12 seconds per command) and may make first startup slower on CLIs with interactive or slow listing commands.
- Observed serve-capable OpenCode and MiMo providers reuse one ACP process per
  provider and create a fresh ACP session per request. Requests are serialized
  per provider so JSON-RPC response correlation remains deterministic.
- Pi, Cursor, Claude Code, and other providers without this validated warm
  contract continue through their native bounded subprocess or ACP adapters.
- A canceled request, deadline, EOF, or dead ACP process invalidates the pool;
  the next request performs a fresh handshake instead of reusing a suspect
  process.

## Future Work

- broader warm pools after each provider's server contract is validated
- catalog caching policies
- health caching
- lower-lock hot paths
- faster model classification

No p50/p95/p99 product benchmark is published yet. The runtime QA artifact
contains observed local command behavior, not a cross-machine performance
claim.
