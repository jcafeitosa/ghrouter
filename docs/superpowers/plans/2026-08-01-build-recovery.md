# Build Recovery Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Restore a compiling, formatted ghrouter baseline with regression coverage for the existing contracts.

**Architecture:** Keep the current standard-library HTTP server and CLI runner. Define the missing health value types, repair compile-time contract mismatches, and add focused tests before touching roadmap features. Do not implement routing modes or claim real streaming in this cycle.

**Tech Stack:** Go 1.26, `net/http`, `testing`, `gopkg.in/yaml.v3`.

## Global Constraints

- Preserve the existing uncommitted worktree changes.
- No `any`/panic-based workarounds or weakened tests.
- Every production behavior change gets a failing regression test first.
- The completion gate is `gofmt`, `go test -race ./...`, `go vet ./...`, and `go build ./...`.

### Task 1: Health and local-brain compile contracts

**Files:**
- Create: `internal/health/types.go`
- Test: `internal/health/loop_test.go`
- Test: `internal/local_brain/detector_test.go`
- Modify: `internal/local_brain/detector.go`

- [ ] Write tests for a successful health result and a missing local model returning an empty path without an error.
- [ ] Run the focused tests and confirm they fail because the health types and local-brain return contract are incomplete.
- [ ] Add the health status/result types and return `(string, error)` consistently from model resolution.
- [ ] Run the focused tests again.

### Task 2: Anthropic server compile contract

**Files:**
- Test: `internal/server/anthropic_test.go`
- Modify: `internal/server/anthropic.go`
- Modify: `internal/server/stream.go`

- [ ] Add a conversion test that preserves all Anthropic messages as internal messages.
- [ ] Run it and confirm the current undefined type/signature errors fail the package.
- [ ] Repair request context propagation, stream flag handling, flusher checking, event invocation, and the duplicate token helper.
- [ ] Run the server package tests.

### Task 3: Catalog lock contract and baseline tests

**Files:**
- Test: `internal/catalog/catalog_test.go`
- Modify: `internal/catalog/catalog.go`

- [ ] Add a test that registers a healthy model and resolves a virtual slot without lock re-entry.
- [ ] Run it as the failing regression case.
- [ ] Make cooldown lookup safe while the catalog write lock is held and remove unused state.
- [ ] Run the catalog tests with the race detector.

### Task 4: Full verification

**Files:**
- No source changes unless a verification failure identifies a direct regression.

- [ ] Run `gofmt -w` only on changed Go files and confirm `gofmt -l` is empty.
- [ ] Run `go test -race ./... -count=1`.
- [ ] Run `go vet ./...` and `go build ./...`.
- [ ] Update `kanban.md` with the exact observed result.
