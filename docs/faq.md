# FAQ

## Does ghrouter require a remote API?

No. It is designed to run locally in front of provider CLIs.

## Can it work without a config file?

Yes. It can auto-discover providers from PATH when the config is empty or missing.

## Does it install providers automatically?

Not yet. It can detect availability, but installation is not implemented.

## Does it update itself automatically?

It can check GitHub releases and apply an update to a configured target path, but automatic startup update is opt-in via `GHR_AUTO_UPDATE=1`.

## What does silent startup do today?

It prepares the local cache structure, checks backend availability, checks auth, verifies the first configured model, and returns machine-readable next steps for any missing prerequisite.

## Does it support OpenAI-compatible requests?

Yes. `/v1/chat/completions` is implemented.

## Does it support Anthropic-compatible requests?

Yes. `/v1/messages` is implemented.
