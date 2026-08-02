# Troubleshooting

## The server exits on startup

Check whether config is present and whether providers are available on PATH.

## A model is not found

Check whether the provider declares the model in config or whether the local cache contains the model path.

## A provider returns an error

Verify the CLI binary, flags, auth state, and working directory for that provider.

## Staticcheck or tests fail

Fix the failing package, then rerun:

```bash
go test ./... -count=1
go test -race ./... -count=1
go build ./...
go run honnef.co/go/tools/cmd/staticcheck@v0.7.0 ./...
```

