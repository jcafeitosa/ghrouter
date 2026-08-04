# Routing

`ghrouter` routes based on the requested model name and provider prefix.

## Current Rules

- `cc/` routes to `claude-code`
- `cx/` routes to `codex`
- `oc/` routes to `opencode`
- `mi/` routes to `mimo`
- `pi/` routes to `pi`
- `cu/` routes to `cursor` through its ACP backend adapter

## Resolution Order

1. Match explicit provider prefix.
2. Resolve model-specific matches across enabled and healthy providers.

### Cost Policy

`model_policy` is applied before automatic selection and explicit execution.
The configured production policy prefers `oc/*` and `nv/*` capacity, keeps
MiMo, Pi, approved Cursor Grok/Composer, and the local brain eligible, and
allows paid Codex only for `sol`, `terra`, `luna`, and `gpt-5.4-mini`. Claude
Code is limited to Opus 5, Sonnet 5, and Haiku 4.5. These patterns filter
observed model IDs; they do not create aliases for models absent from native
discovery.
`model_policy.max_cost_micros` rejects models whose explicitly known per-1k
token cost exceeds the configured ceiling; unknown costs are not guessed.
`model_policy.max_discovery_age` rejects stale native catalog evidence when its
`discovered_at` timestamp exceeds the configured duration. A zero value
disables each limit.
3. Score available catalog metadata and health state when the request uses a generated list or `auto`.
4. Fall back to the route table and healthy catalog selections.

For automatic selection, required capabilities and a declared reasoning-effort
match are applied before cost. When a model advertises an effort list, an
unsupported requested effort is ineligible; missing effort metadata remains
unknown rather than being guessed. Within the compatible set, free models are
preferred first, then account headroom,
context window, declared output capacity, coding/reasoning capability, observed
latency and error rate. A declared `max_output` or context window that cannot
fit the requested input plus output is ineligible; unknown limits remain
unknown rather than being fabricated.
For high or critical complexity, planning, reasoning, or long-context
requests, larger observed context tiers receive a bounded ranking bonus
(`32k`, `128k`, and `1M`); simple requests do not pay this latency/quality
tradeoff automatically.
High-complexity, planning and reasoning requests prefer models with reasoning,
autonomous, coding or long-context evidence; they do not automatically fan out
to paid models. Fusion or graph fan-out must be explicitly configured because
each additional specialist can consume another provider request.

An explicit provider prefix pins the request to that provider. A configured but
unverified model may still be invoked explicitly so the provider's real auth,
quota, or plan response can be observed; it is not admitted to generated lists.
An unhealthy or cooldown model returns no route instead of silently moving to
another CLI. Route-table fallback applies to unprefixed patterns and routable
list members; `auto` is a virtual alias and is not a provider-specific model ID.

When an explicit `ghrouter/*` virtual model has no verified member, an
interactive request performs one bounded, single-flight verification cycle for
up to three eligible catalog candidates, then rebuilds the virtual list and
retries routing. This uses real provider evidence rather than a synthetic
health override: failed probes remain failed or cooldown, and the request
returns service unavailable when no candidate proves functional. An explicit
`verification.enabled: false` disables this on-demand cycle.

`GET /v1/models` intentionally exposes observed native inventory for diagnosis.
Clients that need a selectable catalog can request
`GET /v1/models?functional_only=true`; that view includes only models with real
verification evidence that are healthy, policy-eligible, and routable at the
time of the request. Virtual model lists already use the functional behavior
regardless of this query parameter.

## Current Shape

The HTTP router now consults the live catalog and health state in addition to model prefixes, so the routing surface is richer than the raw config table.

Configured account metadata is displayed and re-read during automatic model
selection. Explicit balance, reset, unavailable, and unhealthy quota evidence
can prefer high-headroom models, penalize low balance, or exclude an exhausted
provider. It is not a universal provider quota reader and is not proof of
capacity when the provider exposes no documented quota signal.

## Notes

- The route table is still static config.
- Fallback behavior is supported both during route selection and after a provider execution failure. The next healthy, routable candidate is tried sequentially and the fallback is recorded in telemetry. For a virtual automatic list, a failure before response output can trigger one bounded, single-flight verification pass over additional policy-eligible catalog candidates, so a failed local brain does not strand the request while still keeping `unknown` models out of routing until they pass a real probe. Config-backed candidates must have real verification evidence.
- Streaming requests select the first healthy candidate before headers are sent; they cannot safely switch providers after a stream has started.
- Round-robin and sticky route selection are available. Fusion fans out healthy candidates and can use a configured judge on every supported API surface.
- Explicit model routes, virtual lists, and capability slots all pass through
  the same provider-auth, account-reset, provider-health, model-health, and
  cooldown gate. A model in an account reset window or an explicitly unavailable
  provider cannot be selected even if its catalog entry is still shown for
  diagnosis.
- Generated model-list members use canonical `provider/model` references. This
  prevents a bare model name from accidentally resolving to a different
  provider when multiple CLIs expose the same model name.
- Pools and combos may reference configured connections or other model lists;
  the router expands those references to functional leaf models and rejects
  cyclic control-plane graphs before reload.
- A model discovered from a native CLI or ACP session is not automatically
  considered healthy. It remains explicitly addressable with `health: unknown`
  until a real probe succeeds; generated lists require verified healthy
  members.
- A local HTTP brain is not assumed to support tool calling from its URL alone.
  The attach/start path runs a semantic `VerifyTools` probe and marks each
  verified local model explicitly; unverified local endpoints stay out of
  tool-required routes.
- Fusion fan-out is available for Chat Completions, Responses, and Anthropic Messages. `max_candidates` bounds concurrent provider work and `judge_timeout` bounds optional synthesis. Set `first_complete: true` when the first valid candidate should win and sibling subprocesses should be cancelled cooperatively; leave it false when all candidates are needed for judging. `max_cost_micros` excludes candidates whose explicitly configured `model_info.<model>.token_cost` exceeds the estimated request budget; unknown costs are never guessed.
- Prompt-intent routing currently uses bounded multilingual keyword
  classification for tools, vision, cost, latency, context, code and reasoning.
  Required capabilities are
  hard eligibility gates; cost, account headroom, policy, latency and errors
  rank eligible candidates. Provider-specific authenticated quota adapters and
  richer TUI policy editing remain roadmap work.
- The optional local brain may rank two or more eligible candidates through its
  local OpenAI-compatible endpoint. Its answer is constrained to the observed
  catalog candidate IDs and fails closed to deterministic routing on timeout,
  malformed output or an unknown model. It is not a source of new models. Its
  redacted selection reason is retained in the request decision/audit record
  and `explain` output without persisting prompts or provider output.
- `latency_p50` and `latency_p95` are based on bounded observed request samples;
  missing samples remain unknown. A provider with no documented quota signal is
  not treated as quota-exhausted, while explicit auth, unhealthy, balance or
  reset evidence still blocks selection.
- The brain also builds a deterministic task graph: code tasks use
  `plan -> implement -> verify`, vision tasks use `extract -> answer`, and
  complex reasoning uses `analyze -> critique -> synthesize`. The graph marks
  when parallel specialists and a judge are appropriate; actual multi-model
  execution remains opt-in through bounded `fusion` or `graph` routes so routine Copilot
  requests do not multiply paid calls.
- `graph` routes execute the reasoning graph across Chat Completions, Responses,
  and Anthropic Messages. Specialist and judge attempts share the same health,
  quota, cost, cancellation and telemetry boundaries.
