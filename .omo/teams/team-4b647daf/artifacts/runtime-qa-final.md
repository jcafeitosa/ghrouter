# Runtime QA Gate: final staged foundation slice

## Verdict

PASS for the scoped five-file staged slice. This is not a product-wide release approval.

## Scope evidence

Worktree: `/Users/jcafeitosa/Develop/Ghrouter/.omo/teams/team-4b647daf/worktrees/A`

Branch: `team/team-4b647daf/A`

Staged files, exactly:

- `.github/pull_request_template.md`
- `.github/workflows/ci.yml`
- `.gitignore`
- `AGENTS.md`
- `CONTRIBUTING.md`

`git diff --cached --name-only` matched this exact five-file allowlist. No unstaged changes were present.

## Checks

- `git diff --cached --check`: PASS, exit 0.
- `go mod verify`: PASS, all modules verified.
- `.github/workflows/ci.yml`: PASS, parsed successfully with Ruby Psych YAML parser.
- Workflow/docs/template command parity: PASS. All five commands appear in the workflow, `CONTRIBUTING.md`, and the PR template: `gofmt`, `go mod verify`, `go vet`, race tests, and build.
- Scope: PASS, five staged files only.
- `.gitignore`: staged foundation files are not ignored; local `.omo/` state, credentials, build output, coverage, IDE, and temporary files are excluded.
- `AGENTS.md`: PASS for the governance slice. It matches the module Go version, repository paths, required local gates, worktree safety, and the leader/executor/reviewer/steward/QA authorization model.

`actionlint` and `yamllint` were unavailable on this host; YAML syntax was independently parsed successfully.

## Separate product baseline result

The product-wide Go gates remain FAIL outside this five-file slice:

- `gofmt -l` reports `internal/detectors/detector.go`, `internal/providers/runner.go`, `internal/config/config.go`, and `internal/types/types.go`.
- `go test -race ./... -count=1`: FAIL due to pre-existing compile errors (`sync`, `os`, `json` imports and illegal method on `types.Provider`).
- `go vet ./...`: FAIL on the same pre-existing compile errors.
- `go build ./...`: FAIL on the same pre-existing compile errors.

These failures are not caused by, and are not included in, the staged five-file governance slice.
