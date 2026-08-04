package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"ghrouter/internal/cli"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	os.Exit(cli.Run(ctx, os.Args[1:]))
}
