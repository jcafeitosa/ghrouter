# Code Quality Review — Ghrouter CLI/TUI Redesign, Round 5

## Scope and evidence

Architecture-only, read-only synthesis for the Bubble Tea + lipgloss lane. No implementation diff was supplied for this task. Inspected the current TUI implementation in `internal/cli/live_tui.go` and `internal/cli/live_tui_render.go`, its focused tests in `internal/cli/cli_test.go`, its snapshot contract in `internal/server/server.go`, `go.mod`, and `kanban.md`.

`omo ulw-loop status --json` reported `ULW_LOOP_PLAN_MISSING`; therefore the fallback evidence location is used.

## Skill-perspective check

Ran. Consulted `omo:programming` including its Go/Bubble Tea v2 guidance, and `omo:remove-ai-slops` including its overfit, needless-abstraction, boundary, and test-quality criteria.

The proposed architecture does not violate either perspective provided that implementation keeps I/O in `tea.Cmd`s, retains a typed snapshot boundary, uses behavior-level interaction tests, and does not introduce a generic component framework or prose/implementation-mirroring tests.

## Findings

### CRITICAL

None for this architecture-only round.

### HIGH

None for this architecture-only round.

### MEDIUM

- `internal/cli/live_tui.go:19-37,79-186,199-222,246-271`: the current single model conflates UI navigation, form state, server lifecycle, refresh/bootstrapping, and command execution. The redesign must split durable snapshot data, screen/navigation state, overlay state, and command execution state. Otherwise a command palette and confirmations will increase key-routing conflicts and make stale selection bugs likely.
- `internal/cli/live_tui_render.go:181-228,398-453`: provider selection is an index over a sorted/filtered projection while the model catalog is sorted in place. The redesign should store stable selected IDs and keep source snapshots immutable; otherwise refresh/filter/reorder can make details refer to the wrong record.
- `internal/cli/cli_test.go:54-153`: existing tests assert presentation strings and implementation-specific helpers. New tests should drive user-visible keys/messages and assert selection, confirmation, command dispatch, and observable results rather than exact decorative wording or helper output.

### LOW

- `internal/cli/live_tui_render.go:683-695`: rune count is not terminal-cell width. New layout code should use lipgloss width/layout measurements rather than extending custom truncation for interactive panes.

## Recommendation

Architecture can proceed. The implementation should be scoped to the live TUI shell and read-model adapter; it must not imply that unintegrated health/catalog roadmap work is live.

## Status

- `codeQualityStatus`: WATCH
- `recommendation`: APPROVE
- `blockers`: none for the architecture decision; the MEDIUM findings are implementation guardrails.
