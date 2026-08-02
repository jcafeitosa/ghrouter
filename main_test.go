package main

import (
	"bytes"
	"context"
	"testing"

	"ghrouter/internal/cli"
)

func TestMainCommandDispatchUnknown(t *testing.T) {
	r := &cli.Runner{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}
	if got := r.Run(context.Background(), []string{"unknown"}); got != 2 {
		t.Fatalf("expected exit code 2, got %d", got)
	}
}
