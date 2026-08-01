# Contributing

Thanks for helping improve Ghrouter.

## Before opening a pull request

- Keep changes focused and explain the user-visible or operational outcome.
- Do not commit local configuration, credentials, generated output, or profiling artifacts.
- Run the same checks used by CI from the repository root:

```sh
test -z "$(gofmt -l .)"
go mod verify
go vet ./...
go test ./...
go build ./...
```

## Pull requests

Describe the problem, the approach, and how you validated the change. Call out any known
limitations or follow-up work. Keep unrelated refactors out of the pull request so review
can stay focused.
