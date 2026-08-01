# ghrouter Project Instructions

## Overview

`ghrouter` is a Go 1.26 service that exposes OpenAI-compatible HTTP endpoints
and invokes locally installed AI CLIs such as Claude, Codex, OpenCode, Mimo and
Pi. The project is still in recovery: the roadmap contains planned features,
while the source and tests are the authority for what is implemented.

## Structure

```text
.
├── main.go                    # process wiring and signal handling
├── internal/config            # YAML loading and config path resolution
├── internal/detectors         # local provider CLI discovery
├── internal/providers         # CLI process execution and output parsing
├── internal/server            # HTTP routes and OpenAI/Anthropic responses
├── internal/types             # configuration and wire-contract types
├── internal/health            # provider health state and polling loop
├── internal/catalog           # model metadata, slots and cooldown state
├── internal/local_brain       # optional MLX/llama.cpp detection
├── docs/                      # user-facing design and operations docs
├── kanban.md                  # roadmap and verified project status
└── .github/                   # CI and repository automation
```

## Where to Look

| Task | Location | Notes |
|------|----------|-------|
| Add or change an HTTP endpoint | `internal/server/` | Keep request parsing, routing and response format observable through tests. |
| Change provider invocation | `internal/providers/` | Preserve context cancellation and avoid shell interpolation. |
| Add a provider detector | `internal/detectors/detector.go` | Use documented CLI flags and explicit environment propagation. |
| Change model selection | `internal/server/server.go`, `internal/catalog/` | Do not claim combo modes until they route through a tested path. |
| Change health behavior | `internal/health/`, `internal/catalog/` | Cover state transitions and concurrent shutdown. |
| Change public behavior | `README.md`, relevant `docs/`, `kanban.md` | Update documentation only after the behavior is verified. |

## Development Rules

- Use the module's Go version and standard library unless a dependency is
  justified in the change description.
- Use `context.Context` for process and server lifetimes. Every subprocess
  must be cancellable and every goroutine must have a shutdown path.
- Treat HTTP, YAML and CLI output as untrusted boundaries. Parse it once into
  typed values and return an explicit error for malformed input.
- Preserve provider credentials. Never log API keys, inherited secret values,
  prompts, or full provider output.
- Keep provider routing separate from provider execution. A route must resolve
  to a concrete enabled provider before invoking a runner.
- Do not call a lock-taking method while holding the same `sync.RWMutex`.
- Do not describe buffered output as streaming. Streaming claims require a
  real incremental runtime check.
- Add a focused regression test before changing production behavior. Prefer
  `httptest`, a temporary fake CLI, and real in-memory objects over broad mocks.
- Keep tests deterministic: use temporary directories and bounded channels;
  do not use arbitrary sleeps to establish readiness.

## Validation Gates

Run these from the repository root before declaring a code change complete:

```bash
gofmt -w <changed-go-files>
test -z "$(gofmt -l $(rg --files -g '*.go'))"
go test -race ./... -count=1
go vet ./...
go build ./...
git diff --check
```

For HTTP changes, also run the built binary and exercise `/health`,
`/v1/models`, the relevant completion endpoint, an invalid request, and clean
SIGTERM shutdown. Record the observed status codes and process exit behavior.

## Worktree and Agent Coordination

- Read `kanban.md` before starting work. If a task is marked blocked, do not
  implement it unless the user explicitly unblocks it.
- The worktree may contain changes from other agents. Inspect `git status` and
  the full diff before editing; preserve unrelated modifications.
- Never use `git add -A`, `git reset --hard`, `git checkout --`, or broad cleanup
  commands in a mixed worktree.
- Stage explicit paths and keep commits atomic by behavior. Generated binaries,
  temporary logs and local credentials do not belong in commits.
- Use a feature branch for publishable work. Do not commit directly to
  `main`/`master` unless the user explicitly requests it.
- Push only a validated branch. Prefer a draft PR until independent review and
  QA evidence are complete; never imply that a local green check is GitHub CI.
- Before publishing, verify remote URL, branch upstream, clean intended scope,
  commit ancestry and the GitHub default branch.

## Current Known Boundaries

- Prefix routing exists; fallback, round-robin, fusion, sticky and auto modes
  remain separate roadmap work until implemented and tested.
- The provider runner must not be treated as real incremental streaming until
  stdout handling is changed and manually verified.
- Health and catalog code must be integrated with the server before their
  status can be reported as live provider readiness.
- Empty or skeleton documentation files are not evidence of implemented
  behavior. Prefer source, tests, runtime output and CI results.
