# Security

`ghrouter` is designed to stay within documented CLI automation boundaries.

## Principles

- no hidden token interception
- no MITM of provider traffic
- no shell injection via untrusted input
- fail closed when auth or backend availability is missing

## Current Behavior

- provider commands are built from config and known flags
- environment variables are inherited explicitly
- requests are routed through local process execution only
- release updates are checked against GitHub and only applied when explicitly requested or when `GHR_AUTO_UPDATE=1` is set
- update application writes the downloaded binary to a configured target path instead of mutating provider traffic
