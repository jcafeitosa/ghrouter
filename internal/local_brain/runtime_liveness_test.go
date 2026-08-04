package local_brain

import (
	"context"
	"errors"
	"strings"
	"testing"

	"ghrouter/internal/types"
)

type exitedRuntimeProcess struct{}

func (exitedRuntimeProcess) Wait() error { return nil }
func (exitedRuntimeProcess) Kill() error { return nil }
func (exitedRuntimeProcess) Alive() bool { return false }

func TestSupervisorRejectsReadyProbeFromExitedProcess(t *testing.T) {
	supervisor := newSupervisorWithDeps(types.LocalBrainConfig{
		Enabled: true,
		Model:   "Qwen/Qwen3-8B",
		Backend: string(BackendMLX),
	}, runtimeDeps{
		detector: runtimeDetector{available: true},
		models:   runtimeModels{present: true},
		start: func(context.Context, string, []string) (managedProcess, error) {
			return exitedRuntimeProcess{}, nil
		},
		probe: func(context.Context, string) error { return nil },
	})

	_, err := supervisor.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "process exited") {
		t.Fatalf("expected exited process rejection, got %v", err)
	}
	if errors.Is(err, context.Canceled) {
		t.Fatalf("unexpected context cancellation: %v", err)
	}
}
