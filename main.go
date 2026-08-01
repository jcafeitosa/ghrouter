package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"ghrouter/internal/config"
	"ghrouter/internal/detectors"
	"ghrouter/internal/server"
	"ghrouter/internal/types"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	cfgPath := config.ResolveConfigPath("")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		log.Printf("config load failed (%v), using auto-detection", err)
		cfg = &types.Config{ListenPort: 9090}
	}

	if len(cfg.Providers) == 0 {
		det := detectors.NewDetector()
		provs, err := det.DetectAll()
		if err != nil {
			log.Fatalf("auto-detection failed: %v", err)
		}
		cfg.Providers = provs
		log.Printf("auto-detected %d providers", len(provs))
		for _, p := range provs {
			log.Printf("  %s (%s): %v", p.Name, p.CLIPath, p.Models)
		}
	}

	srv := server.New(cfg)
	if err := srv.ListenAndServe(ctx); shouldReportServerError(err) {
		log.Fatalf("server error: %v", err)
	}
	log.Println("ghrouter stopped")
}

func shouldReportServerError(err error) bool {
	return err != nil && !errors.Is(err, http.ErrServerClosed)
}
