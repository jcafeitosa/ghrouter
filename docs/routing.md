# Routing

`ghrouter` routes based on the requested model name and provider prefix.

## Current Rules

- `cc/` routes to `claude-code`
- `cx/` routes to `codex`
- `oc/` routes to `opencode`
- `mi/` routes to `mimo`
- `pi/` routes to `pi`

## Resolution Order

1. Match explicit provider prefix.
2. Resolve model-specific matches across enabled and healthy providers.
3. Use request intent and catalog slots for tool-use, vision, long-context, code, or reasoning-heavy prompts.
4. Fall back to the route table and healthy catalog selections.

## Current Shape

The HTTP router now consults the live catalog and health state in addition to model prefixes, so the routing surface is richer than the raw config table.

Provider account metadata also feeds the catalog weight so a provider with healthy balance or a stronger plan can win the auto slot more often than a depleted one.

## Notes

- The route table is still static config.
- Fallback behavior is supported in the configured route path when a provider is missing or unhealthy.
- Combo mode behavior is still mostly roadmap, not a full scheduler.
