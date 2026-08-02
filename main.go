package main

import (
	"context"
	"os"
	"os/signal"

	"ghrouter/internal/cli"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	os.Exit(cli.Run(ctx, os.Args[1:]))
}
