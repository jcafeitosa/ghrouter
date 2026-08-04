# FAQ

## Does ghrouter require a remote API?

No. It is designed to run locally in front of provider CLIs.

## Can it work without a config file?

Yes. It can auto-discover providers from PATH when the config is empty or missing.

## How does the local Brain start?

An empty `local_brain` configuration receives the default Gemma MLX
model, or the model/source supplied through `GHR_LOCAL_BRAIN_MODEL` and
`GHR_LOCAL_BRAIN_SOURCE`. Ghrouter uses only its allowlisted argv commands for
the resulting download. If the local Brain is unavailable, a measured fast
model may serve as backup; with no eligible model, the request fails explicitly.

## Does it update itself automatically?

It can check GitHub releases and apply an update to a configured target path, but automatic startup update is opt-in via `GHR_AUTO_UPDATE=1`.

## What does silent startup do today?

It prepares the local cache structure, checks backend availability, checks auth
signals, confirms that a model is configured, and returns machine-readable next
steps for missing prerequisites. It does not perform an inference request; use
`ghrouter probe` for that.

## Does it support OpenAI-compatible requests?

Yes. `/v1/chat/completions` is implemented.

## Does it support Anthropic-compatible requests?

Yes. `/v1/messages` is implemented.
