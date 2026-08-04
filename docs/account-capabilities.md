# Account, quota, and cost capabilities

Ghrouter distinguishes three different facts:

1. authentication is present;
2. a provider/model answered a real probe;
3. an official account or billing API reported quota or cost.

The first two are available for the supported local CLIs. The third is not
universal and must never be inferred from a successful request.

## Supported evidence

| Surface | Native evidence | Ghrouter behavior |
| --- | --- | --- |
| Claude Code | environment or persisted/native login state; print mode supports a documented `--max-budget-usd` limit | detect auth; route failures and quota markers into cooldown; do not scrape subscription balance |
| Codex | environment or persisted CLI auth state | detect auth and real model response; do not claim an OpenAI account balance |
| OpenCode | `/connect`, `opencode auth list`, provider configuration, and model catalog | discover configured provider/model identity; preserve provider-specific auth state; do not treat catalog presence as quota |
| Cursor Agent | installed Agent command, model listing, and configured auth | discover models and probe availability; do not infer plan balance from the CLI |
| GitHub Copilot CLI | native model/session selection and GitHub's authenticated billing APIs where the user has the required permissions | keep Copilot as a client; only an explicitly configured GitHub billing adapter may report account usage |

GitHub's official billing APIs require a GitHub token and organization/user
permissions, and may report AI credits or premium requests rather than a
provider-local token balance. The router therefore does not silently reuse a
client credential or call a billing endpoint without explicit configuration.

## Cooldown policy

- A real provider quota/rate-limit response applies provider-wide cooldown.
- HTTP providers treat documented `429 Too Many Requests` and `402 Payment
  Required` responses as provider capacity evidence; a valid `Retry-After`
  header is honored for up to 24 hours, otherwise the configured cooldown
  policy applies.
- Structured CLI `rate_limit_info` envelopes with a rejected status preserve
  explicit `reset_at` or `retry_after` evidence under the same bounded policy;
  generic quota text still receives only the configured cooldown.
- A model probe failure applies exponential model cooldown.
- A configured zero balance or explicit unavailable/unhealthy account with a
  future `reset_at` applies provider-wide cooldown at startup; a quota failure
  extends the cooldown to the declared reset when that evidence is present.
- Unknown quota is represented as `unknown`, never as zero.
- Automatic selection re-reads explicit account metadata and uses balance/reset
  headroom as a routing signal; it does not infer quota from a model list.
- Virtual model lists contain only currently routable members, and generated
  references are canonicalized as `provider/model` to avoid cross-provider
  ambiguity.
- `ghrouter verify-models` skips models already in cooldown and rechecks them
  with the real CLI after the reset window.

The router does not claim to discover a subscription's private balance from a
CLI that has no documented account API. It uses only explicit account metadata,
documented quota/rate-limit errors, and a real model probe. A model remaining
visible in the catalog with `health=cooldown` is intentional: it exposes why
the model is unavailable while excluding it from generated routing lists.

## References

- [GitHub Copilot CLI command reference](https://docs.github.com/en/copilot/reference/copilot-cli-reference/cli-command-reference)
- [GitHub Copilot usage limits](https://docs.github.com/en/enterprise-cloud@latest/copilot/concepts/usage-limits)
- [GitHub billing usage API](https://docs.github.com/en/rest/billing/usage)
- [Claude Code CLI reference](https://code.claude.com/docs/en/cli-usage)
- [OpenCode models](https://opencode.ai/v2/docs/models)
- [OpenCode providers and authentication](https://opencode.ai/v2/docs/providers)
