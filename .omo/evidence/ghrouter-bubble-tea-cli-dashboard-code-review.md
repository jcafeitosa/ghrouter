# Code review: Ghrouter Bubble Tea CLI dashboard

## Scope and evidence

- Goal reviewed: current live Bubble Tea dashboard implementation, rendering, key handling, settings/port save flow, and directly relevant tests.
- Changed dashboard files inspected: `internal/cli/live_tui.go`, `internal/cli/live_tui_render.go`, `internal/cli/cli.go`, and `internal/cli/cli_test.go` (all are untracked in this dirty worktree).
- The supplied ULW status has no plan (`ULW_LOOP_PLAN_MISSING`), so this is the required fallback report path.
- Independently run: `go test -race -shuffle=on -count=1 ./internal/cli ./internal/config ./internal/server`, `go vet ./internal/cli ./internal/config ./internal/server`, `go build ./...`, and `git diff --check`. All passed. This does not constitute TUI interaction coverage.
- The existing `.omo/evidence/ghrouter-cli-dashboard-direction-code-review.md` was inspected only as untrusted context; every finding below was rechecked against current source and tests.

## Findings

### CRITICAL

None.

### HIGH

1. **Port edit mode cannot accept user input.**  
   `internal/cli/live_tui.go`, `liveTUIModel.Update` (lines 138-149), changes the mode and prepopulates `m.input`, but never calls `m.input.Focus()`. `textinput.New()` begins blurred and its `Update` method returns without processing key events while blurred. Thus `p` shows a port value but subsequent digits/backspace do nothing; Enter can only save the pre-existing port. The direct `SetValue` in `TestLiveSavePortCmdWritesConfig` (`internal/cli/cli_test.go:97-99`) bypasses the actual transition and misses the failure.

2. **Startup, action, and stale errors share one permanent banner state, hiding their source and actionable issue list.**  
   `internal/cli/live_tui.go`, `withFreshData` (lines 183-192) assigns `Bootstrapper.Check` errors to `m.err`; that check deliberately returns an error whenever the report has any startup issue (`internal/local_brain/bootstrap.go`, `Bootstrapper.Check`, line 212). `liveTUIModel.Update` also stores failed settings/actions in the same field (lines 84-92) and no successful refresh/action clears it. `bannerView` (`internal/cli/live_tui_render.go:52-63`) prioritizes `m.err`, so its `startup pending` branch with individual issues is effectively bypassed for normal prerequisite failures. A single failed port save is then rendered as a persistent **startup** error, while settings reduces the failure to `save-port: failed`. This is both misleading and prevents operators from seeing the per-provider/model cause and recovery state.

3. **The dashboard is not live server observability: it creates a private, non-listening server instead of observing the router process.**  
   `internal/cli/cli.go`, `Runner.live` (lines 675-697), calls `runLiveTUI`; `internal/cli/live_tui.go`, `runLiveTUI` (lines 235-246), creates a new `server.Server` and starts monitoring but never calls `ListenAndServe` or connects to an existing server. Therefore its telemetry remains local to this isolated instance and cannot represent requests handled by `ghrouter serve`; its displayed port is only config data, not proof a listener exists. A dashboard labelled as a live router dashboard will show zero/stale activity and invite incorrect operational decisions.

4. **Settings actions do not preserve the selected config path; `sync` can overwrite a different config than the dashboard displays.**  
   `internal/cli/live_tui.go`, `runActionCmd` (lines 198-223), constructs `Runner{Stdout: ..., Stderr: ...}` without `Config: m.cfgPath`. `Runner.sync` (`internal/cli/cli.go:394-411`) resolves an empty `Runner.Config` to `GHR_CONFIG` or the working-directory `config.yaml`. Launching `ghrouter --config /tmp/custom.yaml`, then pressing `s`, therefore saves provider discovery to the default config rather than `/tmp/custom.yaml`. It also leaves the model’s in-memory configuration unchanged. This is a state/scope violation in a settings screen that performs writes.

### MEDIUM

1. **The error banner is unbounded and can consume the usable terminal height or overflow horizontally.**  
   `internal/cli/live_tui_render.go`, `bannerView` (lines 52-69), inserts the complete `m.err` string without wrapping/truncation, and otherwise appends every startup issue. `renderLiveTUIView` (lines 16-31) has no viewport or height budget. With multiple providers, `BootstrapReport.Error` joins every issue into one long message (`internal/local_brain/bootstrap.go`, `BootstrapReport.Error`, lines 87-96); the main panel and settings/error details can be pushed below the screen. This directly creates the reported startup-banner-dominates-screen failure mode.

2. **The settings view reports a fictitious config path.**  
   `internal/cli/live_tui_render.go`, `settingsPanel` (line 205) calls `configDisplayPath`; that helper (`configDisplayPath`, lines 297-299) always returns `config.yaml` and does not receive `m.cfgPath`. For `--config` and `GHR_CONFIG`, the screen claims a different file from the one `savePortCmd` writes. This is especially harmful alongside the wrong-path `sync` defect above.

3. **Port parsing accepts invalid ports and does not enforce the TCP range.**  
   `internal/cli/live_tui.go`, `savePortCmd` (lines 255-272), only rejects `port <= 0`. Its `parseInt` implementation (`internal/cli/cli.go:297-300`) uses `fmt.Sscanf("%d")`, which can accept a numeric prefix with trailing text; values above 65535 are also accepted and persisted. The later server bind will fail, separating error discovery from the settings action that introduced it.

4. **The TUI starts duplicate monitoring loops and does not cancel all of them on normal quit.**  
   `runLiveTUI` starts monitoring (lines 235-239), then calls `withRuntime`, which calls `withFreshData`. Because `lastFetch` is zero, `withFreshData` starts monitoring again with a fresh background context (lines 171-182). That second context is only cancelled after a 30-minute timer; quitting with `q` does not cancel either context in `runLiveTUI` because cancellation occurs only in the parent-context goroutine (lines 240-244). `health.Loop.Start` and `catalog.Catalog.Start` create goroutines per call. This wastes provider health checks and is a lifecycle leak not caught by the current suite.

5. **Tests are shallow and partially tautological for the user-visible port workflow.**  
   `TestLiveSettingsPanelShowsActionStatusAndCommands` (`internal/cli/cli_test.go:51-66`) only mirrors rendered hint strings. `TestLiveSavePortCmdWritesConfig` directly invokes the save command after `SetValue` (`lines 90-123`) rather than driving `p`, typing, Enter, cancellation, failure rendering, and retry. No test covers banner precedence, clearing recovered errors, config-path propagation, port bounds, or a constrained terminal. `TestRunLiveCommandProducesSnapshot` (`lines 32-49`) uses an arbitrary sleep and executes only the non-interactive JSON fallback. These tests create confidence in internal/decorative details while leaving the actual Bubble Tea state transitions untested.

### LOW

1. **The TUI does not export a real cursor for text input and therefore is not IME-correct.**  
   `internal/cli/live_tui.go`, `newLiveTUIModel`/`View` (lines 63-69 and 161-163), retains Bubble’s virtual cursor and `renderLiveTUIView` (`internal/cli/live_tui_render.go:16-31`) does not assign `tea.View.Cursor`. The selected Bubble Tea v2 stack supports correct real-cursor positioning. This is lower priority than the fact that port editing is currently non-functional, but it will affect CJK IME input once focus is fixed.

## Skill-perspective check

- **Ran:** Yes. I loaded and applied `omo:remove-ai-slops` and `omo:programming`, including the Go, Bubble Tea v2, testing, and concurrency references.
- **remove-ai-slops:** Violated. The new rendering-hint test is a brittle implementation-mirroring test; the port-save test bypasses the requested user behavior; and the dashboard contains unbounded display/status plumbing without tests for the actual states it adds. No deletion-only tests or prompt/prose tests were found.
- **programming:** Violated. The TUI lacks behavior-level Bubble Tea tests, has brittle state transitions, uses background contexts/goroutines without a normal-quit cancellation path, and does not apply Bubble Tea v2 focus/real-cursor requirements. No untyped escape hatch was found in the dashboard files.

## Recommendation

- `codeQualityStatus`: **BLOCK**
- `recommendation`: **REQUEST_CHANGES**
- `blockers`:
  1. Focus and test the port input state transition so editing works.
  2. Separate startup status from action errors, retain actionable details, clear recovered errors, and bound the banner.
  3. Define and implement a real data boundary for a live dashboard (or stop claiming live router telemetry).
  4. Pass the active config path to actions and show the actual path; prevent `sync` from writing another config.
  5. Add behavior tests for the above interaction paths and lifecycle cancellation.
