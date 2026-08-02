# Code review: Ghrouter CLI dashboard direction

## Verdict

- `codeQualityStatus`: **BLOCK**
- `recommendation`: **REQUEST_CHANGES**
- Scope reviewed: current CLI/TUI direction in `internal/cli/live_tui.go`, its command wiring, relevant server state, docs, roadmap, and available tests.

## Evidence inspected

- `internal/cli/live_tui.go` (untracked, 412 lines; 383 pure LOC)
- `internal/cli/cli.go:46-117,675-696`
- `internal/server/server.go:103-147,531-568`
- `internal/health/loop.go:66-92,150-172`
- `internal/cli/cli_test.go:32-49`
- `README.md:96-112`, `docs/cli.md:1-40`, `kanban.md:62-112`
- Validation run: `go test ./internal/cli ./internal/server -count=1`, `go vet ./...`, `go build ./...` all succeeded. `git diff --check` succeeded.

## Findings

### CRITICAL

None.

### HIGH

1. **The “live” dashboard is not connected to the running router and therefore presents fabricated/stale operational state.**  
   `internal/cli/live_tui.go:97-109` creates a fresh `server.New(m.cfg)` on every five-second refresh and immediately calls `LiveSnapshot()`. A server starts its health/catalog loops only in `ListenAndServe` (`internal/server/server.go:118-124`); the newly constructed server never starts them. Consequently provider health is `unknown`, the health map has no samples, and the latency/sparkline display cannot mean live telemetry. A dashboard must either attach to one explicitly selected running instance through a well-defined local endpoint/IPC contract or not claim live operational state.

2. **The default command no longer performs Ghrouter’s core job, while the new primary screen has no primary action.**  
   `internal/cli/cli.go:59-65` routes a no-argument invocation to `live`, while only `serve` starts the HTTP router. The TUI prompt says “Ask anything” (`live_tui.go:35-41`) but Enter merely deletes the input (`live_tui.go:61-63`). The footer advertises `tab agents` and `ctrl+p commands` without handlers (`live_tui.go:57-72,84`). This is a misleading, non-functional OpenCode/agent-workbench façade replacing the zero-config router start described by the product purpose. Decide whether the default is (a) start a router, (b) perform setup/doctor, or (c) open an actual interactive client; only then design its screen and interactions.

3. **The UI claims an “OpenCode-style CLI” without providing OpenCode-like workflow semantics.**  
   `live_tui.go:75-85` brands the UI as “OpenCode-style” and includes an unexplained “OpenKnowledge Helper” label. The implementation has no session, model selection, command palette, agent list, tool/run transcript, file context, or action execution. Styling a status page to resemble an agent client invites users to expect capabilities the router does not provide. The product must choose its identity: transparent local router/operator console, or an agent client. It should not borrow the latter’s affordances without its behavior.

### MEDIUM

1. **Dashboard density and terminal responsiveness are unsolved.**  
   Three fixed-width 34-column cards plus spacing require roughly 106 columns before outer borders (`live_tui.go:186-205,319-326`), and provider cards add more vertical chrome (`147-172,275-304`). The model records terminal width/height but never uses them (`20-29,50-53`). On the common 80-column terminal this will wrap or clip, leaving less room for the only actionable information. Do not introduce Mermaid into the terminal: it is not available today, will be less legible than text at narrow widths, and cannot resolve this hierarchy problem. If topology is essential, use a separate `routes`/`explain` command with compact, plain-text output; reserve Mermaid for rendered documentation.

2. **Telemetry is duplicated, low-signal, and unsupported by a collection model.**  
   The summary card and the full telemetry panel repeat provider/model/health counts and latency/sparkline (`live_tui.go:197-230`). Latency is only a per-provider health-check duration, not request latency; request count, error rate, fallback events, retention window, reset semantics, and privacy policy are undefined. The current `LiveSnapshot` carries no request history (`internal/server/server.go:49-71`). Do not add request instrumentation merely to populate cards. First decide the operator question each metric answers and define bounded, local-only collection with explicit overhead and retention.

3. **No behavior-focused test covers the TUI or the advertised interactions.**  
   There are no references to `liveTUIModel` or `runLiveTUI` in tests. `TestRunLiveCommandProducesSnapshot` (`internal/cli/cli_test.go:32-49`) exercises only the non-interactive fallback, uses an arbitrary `time.Sleep`, and checks for one JSON field. It neither validates a usable TUI nor catches the false prompt/shortcuts or disconnected telemetry. This is insufficient evidence for making the TUI the default experience. Future tests should drive real Bubble Tea messages and assert observable state/action outcomes, not hard-code decorative copy or card formatting.

4. **The new TUI is already an oversized, mixed-responsibility module.**  
   `internal/cli/live_tui.go` is 383 pure LOC and combines lifecycle, refresh/data acquisition, layout, provider cards, metric bars, and sparkline rendering. The programming skill treats source files over 250 pure LOC as a structural defect; this one is both large and difficult to test because runtime acquisition is embedded in the model. This will make the inevitable product decision change expensive. Establish the data contract and interaction model before adding more rendering abstractions.

5. **The roadmap and documentation contradict the claimed implementation state.**  
   `docs/cli.md:5-40` and `README.md:96-112` call the TUI/current CLI implemented, while `kanban.md:62-112` still marks the CLI experience, observability, and TUI epics as planned. The roadmap must distinguish implemented command scaffolding from verified operational behavior; otherwise it gives the team false confidence and muddles the debate.

### LOW

1. **Terminology is overloaded.** “Telemetry” traditionally suggests instrumentation/export, whereas the README promises no telemetry; even if that means no external telemetry, users will read the dashboard label differently. Use “local status” until a documented metrics model exists.

2. **A spinning frame is decorative rather than informative.** `live_tui.go:33,54-56,186-190` advances a frame every refresh even when no live work is occurring. It creates a sense of activity without an operator decision behind it.

## Required decisions before implementation

1. **Primary job and default command:** Is Ghrouter first a headless OpenAI-compatible router, an onboarding/repair CLI, or an agent client? Specify the no-argument behavior and the explicit `serve`, `doctor`, and `live` contracts.
2. **Operator workflow:** Name the first three tasks a user must complete (for example: see readiness, fix authentication, verify a model route). Every visible element must support one of those tasks; delete the chat prompt/agent affordances unless agent execution is in scope.
3. **Live-data boundary:** Decide whether `live` observes a running router or is a one-shot local diagnostic. If it observes a server, define the authenticated local transport, instance selection, lifecycle, snapshot schema, error states, and whether it may change state.
4. **Observability budget:** Define the minimal local metrics/event set, source of truth, sampling, retention, display semantics, and the performance/privacy budget. Do not build request tracing/history/cost dashboards by default.
5. **Terminal information architecture:** Set a narrow-terminal baseline (80x24), a minimal status layout, keyboard behavior, accessibility/color fallback, and overflow rules. Keep Mermaid in docs only; use plain terminal tables/text for CLI output.
6. **Product language:** Either remove “OpenCode-style”/“OpenKnowledge Helper” or write down the precise supported parity claim. A router console should be confident in its own purpose rather than imitate an interactive coding agent.
7. **Acceptance evidence:** Define behavior tests for the chosen workflow, a real interactive-terminal smoke test, and runtime proof that displayed status is from the same server handling requests. Decorative snapshot/prompt tests are explicitly not acceptance evidence.

## Skill-perspective check

This check ran with the available `remove-ai-slops` and `programming` skills.

- **remove-ai-slops:** violated. The TUI includes speculative/dead-on-arrival affordances (prompt and advertised shortcuts without behavior), duplicated telemetry rendering, a decorative animation, and missing behavior coverage. No deletion-only tests were added, but the existing live test is too shallow to justify the new behavior.
- **programming:** violated. The diff lacks a behavior-first TUI test seam, has a 383-pure-LOC mixed-responsibility source file, hard-wires data acquisition into rendering, and uses a UX claim that is not represented by typed, observable capabilities. No untyped escape hatch was identified in the TUI itself.

## Blockers

- Stop presenting a fresh, unstarted server snapshot as live router telemetry.
- Resolve the product’s primary job/default command and remove unsupported interactive affordances.
- Define the dashboard’s real data boundary and minimal operator workflow before adding cards, Mermaid, or further telemetry.
- Add behavior-level TUI/runtime evidence after those decisions; the current fallback JSON test is not relevant coverage.
