# Code review: Ghrouter Bubble Tea TUI architecture

## Scope and evidence

- Review request: read-only architecture review of the Bubble Tea / lipgloss CLI redesign; positions limited to state/event-loop structure and render/layout strategy.
- Inspected: `internal/cli/live_tui.go`, `internal/cli/live_tui_render.go`, `internal/cli/cli.go`, `internal/cli/cli_test.go`, `internal/server/server.go`, `internal/health/loop.go`, `internal/catalog/catalog.go`, `internal/local_brain/bootstrap.go`, the current diff, `kanban.md`, and existing evidence reports (treated as untrusted context and rechecked against source).
- Validation run: `go test ./internal/cli -count=1`, `go test -race ./internal/cli -count=1`, `go test ./... -count=1`, `go vet ./internal/cli`, `go build ./...`, and `git diff --check` all passed.
- The worktree is dirty. `internal/cli/` is untracked, so the review is of the on-disk redesign rather than a committed diff.

## Round 1 position: state model and event loop

**Position: BLOCK.** Do not extend this model into a complex router UI until the dashboard's data boundary and lifecycle are redesigned.

### HIGH

1. `ghrouter live` presents a separate, private server's counters as a live router dashboard, not the `ghrouter serve` process's state. `Runner.live` selects `runLiveTUI` (`internal/cli/cli.go:675-697`), which constructs a new `server.Server` and only starts its monitors (`internal/cli/live_tui.go:283-296`). It neither attaches to an existing serving process nor invokes `ListenAndServe`; the isolated instance therefore cannot observe request telemetry handled by another router process. The displayed port is configuration, not listener evidence. An operator can make incorrect decisions from an apparently healthy, zero-traffic UI.

2. The TUI starts health/catalog monitoring twice and normal `q` exit leaks both monitoring lifetimes. `runLiveTUI` starts monitoring (`internal/cli/live_tui.go:283-286`), then `withRuntime` immediately calls `withFreshData` (`internal/cli/live_tui.go:306-310`), whose zero `lastFetch` starts monitoring again with a new background context (`internal/cli/live_tui.go:199-210`). Both `health.Loop.Start` and `catalog.Catalog.Start` start a goroutine per call (`internal/health/loop.go:66-69`; `internal/catalog/catalog.go:98-101`). On `q`, `prog.Run` returns but `cancel` is only reached from the parent-context watcher (`internal/cli/live_tui.go:297-303`), so the normal-close path does not cancel either context. This produces duplicate provider checks and retained goroutines.

### MEDIUM

1. The model mixes UI state, mutable configuration, server ownership, polling, bootstrap I/O, and imperative command execution in one type (`internal/cli/live_tui.go:19-38, 199-271`). `withFreshData` performs synchronous bootstrap preparation/checking in `Update`'s tick path (`internal/cli/live_tui.go:85-87, 212-222`), so an external filesystem/backend operation can stall keyboard processing and rendering. A complex dashboard needs a narrow read-only snapshot client plus explicit effect commands/messages, a cancellable root context, and screen-local interaction state.

2. Provider selection is indexed against the unfiltered, unsorted snapshot in `Update` (`internal/cli/live_tui.go:121-137`) but rendered from a sorted and possibly filtered slice (`internal/cli/live_tui_render.go:181-201, 425-453`). It will not stay stable when search/filtering is used or data changes. Selection should be an identity (provider name) and each screen should derive an index from its current visible collection.

3. Tests do not cover the actual Bubble Tea lifecycle, command/event sequencing, resize behavior, cancellation, or a real attached data source. `TestRunLiveCommandProducesSnapshot` uses an arbitrary sleep and asserts only a JSON field on the non-interactive fallback (`internal/cli/cli_test.go:35-52`); the direct model tests bypass a terminal program. The green focused/race suites therefore do not substantiate TUI safety.

## Round 2 position: render and layout

**Position: BLOCK.** The visual direction has useful primitives, but the layout is a fixed card composition, not a responsive terminal layout system.

### MEDIUM

1. Height is captured but never used (`internal/cli/live_tui.go:30-31, 81-84`), while the root composes header, banner, two dashboard rows, a panel, and a footer without a viewport (`internal/cli/live_tui_render.go:17-33`). Long provider/model/route lists are simply truncated by arbitrary line counts (`internal/cli/live_tui_render.go:201, 228, 243, 255, 274`) and short terminals can push controls or errors offscreen. Establish a layout budget from `WindowSizeMsg`, reserve header/footer space, and give the active screen a viewport/table with a preserved selected row and explicit overflow indicators.

2. Responsiveness is limited to one 150-column breakpoint and fixed card widths (`internal/cli/live_tui_render.go:79-125, 620-660`). Cards impose a 26- or 30-column minimum (`internal/cli/live_tui_render.go:585-595, 631-639`), while the header is never width-constrained (`internal/cli/live_tui_render.go:17-33`). Narrow terminals can clip/wrap rather than degrade to a compact single-column summary. Define compact, normal, and wide density modes, with an intentional minimum-size/resize message instead of relying on truncation.

3. `View` is not render-pure: `modelsPanel` sorts `m.snapshot.Models` in place (`internal/cli/live_tui_render.go:219-224`). Because the slice backing array is shared after the value-model copy, rendering changes state/order. Rendering should operate on a copied/derived collection; side effects belong in explicit update/effect paths.

4. `internal/cli/live_tui_render.go` has 691 pure LOC, far beyond the 250-LOC ceiling from the programming review perspective, and contains screens, data transformation, charts, formatting, and layout primitives. This is a maintainability risk for the planned advanced UI. Split by cohesive screen/view-model responsibilities only after the Round 1 data contract is fixed; do not introduce generic render frameworks.

### LOW

1. The request “trend” sparkline is four current aggregate counters rather than a time series (`internal/cli/live_tui_render.go:277-287`), so it implies a historical signal that the snapshot does not contain. Label it as a current mix or defer it until there is a defined, bounded history contract.

## Skill-perspective check

- **Ran:** `omo:remove-ai-slops` and `omo:programming`, including the Go reference.
- **remove-ai-slops: violated.** The layout contains speculative visual complexity without a settled live-data contract; the tests include shallow UI-copy assertions and a sleep-based fallback test rather than behavior-level TUI coverage. No deletion-only tests or tests that merely verify a requested removal were found.
- **programming: violated.** Normal exit lacks context-driven shutdown, production state acquisition occurs inside the event loop, rendering mutates a slice, the source file exceeds the skill's size ceiling, and the test suite lacks a real TUI lifecycle scenario. No untyped escape hatch was found in the reviewed TUI files.

## Recommendation

- `codeQualityStatus`: **BLOCK**
- `recommendation`: **REQUEST_CHANGES**
- Required blockers: define an actual live-data boundary (attach to a selected running router, or rename/re-scope the command as a local diagnostic); create one owner for server/monitor lifecycle with cancellation on every program exit; separate effects from UI state; and redesign around height-aware, screen-local viewport/table layouts before adding more dashboard features.
