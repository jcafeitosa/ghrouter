package local_brain

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"ghrouter/internal/types"
)

func TestProbeRuntimeRejectsOccupiedUnresponsiveEndpoint(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	started := time.Now()
	err = probeRuntime(ctx, "http://"+listener.Addr().String())
	if err == nil || !strings.Contains(err.Error(), "occupied") {
		t.Fatalf("expected occupied endpoint error, got %v", err)
	}
	if elapsed := time.Since(started); elapsed >= 5*time.Second {
		t.Fatalf("occupied endpoint probe waited for its full timeout: %s", elapsed)
	}
}

type runtimeProcess struct {
	mu     sync.Mutex
	killed bool
	done   chan struct{}
}

func (p *runtimeProcess) Wait() error {
	<-p.done
	return nil
}

func (p *runtimeProcess) Kill() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.done == nil {
		p.done = make(chan struct{})
	}
	if p.killed {
		return nil
	}
	p.killed = true
	close(p.done)
	return nil
}

func (p *runtimeProcess) Exit() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.killed {
		return
	}
	p.killed = true
	close(p.done)
}

type runtimeDetector struct {
	available bool
}

func (d runtimeDetector) IsBackendAvailable(backend BackendType) bool {
	return d.available && backend == BackendMLX
}

type runtimeModels struct {
	present bool
}

func (m runtimeModels) HasModel(backend BackendType, modelID string) bool {
	return m.present && backend == BackendMLX && modelID != ""
}

func TestSupervisorRequiresExplicitProvisioningAndModelSource(t *testing.T) {
	supervisor := newSupervisorWithDeps(types.LocalBrainConfig{
		Enabled: true,
		Model:   "Qwen/Qwen3-8B",
	}, runtimeDeps{
		detector: runtimeDetector{},
		models:   runtimeModels{},
	})

	_, err := supervisor.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "auto_provision") {
		t.Fatalf("expected opt-in provisioning error, got %v", err)
	}
}

func TestLauncherForModelUsesOfficialMLXServer(t *testing.T) {
	tests := []struct {
		name      string
		modelPath string
		launcher  string
		args      []string
	}{
		{
			name:      "qwen35",
			modelPath: "/tmp/mlx-community-Qwen3.5-0.8B-OptiQ-4bit",
			launcher:  "python3",
			args:      []string{"-m", "mlx_lm.server", "--model", "/tmp/mlx-community-Qwen3.5-0.8B-OptiQ-4bit", "--host", "127.0.0.1", "--port", "19090", "--chat-template-args", `{"enable_thinking":false}`},
		},
		{
			name:      "gemma4",
			modelPath: "/tmp/mlx-community-gemma-4-e2b-it-4bit",
			launcher:  "python3",
			args:      []string{"-m", "mlx_lm.server", "--model", "/tmp/mlx-community-gemma-4-e2b-it-4bit", "--host", "127.0.0.1", "--port", "19090", "--chat-template-args", `{"enable_thinking":false}`},
		},
		{
			name:      "text-model",
			modelPath: "/tmp/mlx-community-Qwen2.5-Coder-0.5B-Instruct-4bit",
			launcher:  "python3",
			args:      []string{"-m", "mlx_lm.server", "--model", "/tmp/mlx-community-Qwen2.5-Coder-0.5B-Instruct-4bit", "--host", "127.0.0.1", "--port", "19090", "--chat-template-args", `{"enable_thinking":false}`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := launcherName(BackendMLX, tt.modelPath); got != tt.launcher {
				t.Fatalf("launcherName() = %q, want %q", got, tt.launcher)
			}
			if got := launchArgs(BackendMLX, tt.modelPath, "127.0.0.1", 19090); strings.Join(got, "\x00") != strings.Join(tt.args, "\x00") {
				t.Fatalf("launchArgs() = %#v, want %#v", got, tt.args)
			}
		})
	}
}

func TestSupervisorStartsOnlyAfterReadiness(t *testing.T) {
	process := &runtimeProcess{done: make(chan struct{})}
	supervisor := newSupervisorWithDeps(types.LocalBrainConfig{
		Enabled:        true,
		Model:          "Qwen/Qwen3-8B",
		Backend:        string(BackendMLX),
		Host:           "127.0.0.1",
		Port:           19090,
		StartupTimeout: 0,
		AutoProvision:  false,
	}, runtimeDeps{
		detector: runtimeDetector{available: true},
		models:   runtimeModels{present: true},
		start: func(context.Context, string, []string) (managedProcess, error) {
			return process, nil
		},
		probe: func(context.Context, string) error { return nil },
	})

	status, err := supervisor.Start(context.Background())
	if err != nil {
		t.Fatalf("expected ready local runtime, got %v", err)
	}
	if !status.Ready || status.Backend != BackendMLX || status.Port != 19090 {
		t.Fatalf("unexpected local runtime status: %+v", status)
	}
	if err := supervisor.Stop(); err != nil {
		t.Fatalf("stop local runtime: %v", err)
	}
	process.mu.Lock()
	killed := process.killed
	process.mu.Unlock()
	if !killed {
		t.Fatal("expected supervisor stop to terminate the backend process")
	}
}

func TestSupervisorDoesNotReportReadyWhenProbeFails(t *testing.T) {
	process := &runtimeProcess{done: make(chan struct{})}
	supervisor := newSupervisorWithDeps(types.LocalBrainConfig{
		Enabled: true,
		Model:   "Qwen/Qwen3-8B",
		Backend: string(BackendMLX),
		Host:    "127.0.0.1",
		Port:    19091,
	}, runtimeDeps{
		detector: runtimeDetector{available: true},
		models:   runtimeModels{present: true},
		start: func(context.Context, string, []string) (managedProcess, error) {
			return process, nil
		},
		probe: func(context.Context, string) error { return errors.New("not ready") },
	})

	status, err := supervisor.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "readiness") {
		t.Fatalf("expected readiness failure, status=%+v err=%v", status, err)
	}
	process.mu.Lock()
	killed := process.killed
	process.mu.Unlock()
	if !killed {
		t.Fatal("expected failed readiness to terminate the process")
	}
}

func TestSupervisorRestartsUnexpectedBackendExit(t *testing.T) {
	first := &runtimeProcess{done: make(chan struct{})}
	second := &runtimeProcess{done: make(chan struct{})}
	started := make(chan struct{}, 1)
	starts := 0
	supervisor := newSupervisorWithDeps(types.LocalBrainConfig{
		Enabled:        true,
		Model:          "Qwen/Qwen3-8B",
		Backend:        string(BackendMLX),
		Host:           "127.0.0.1",
		Port:           19092,
		Restart:        true,
		RestartBackoff: time.Millisecond,
		MaxRestarts:    1,
	}, runtimeDeps{
		detector: runtimeDetector{available: true},
		models:   runtimeModels{present: true},
		start: func(context.Context, string, []string) (managedProcess, error) {
			starts++
			if starts == 1 {
				return first, nil
			}
			started <- struct{}{}
			return second, nil
		},
		probe: func(context.Context, string) error { return nil },
	})
	if _, err := supervisor.Start(context.Background()); err != nil {
		t.Fatalf("start local runtime: %v", err)
	}
	first.Exit()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("supervisor did not restart the backend")
	}
	deadline := time.Now().Add(time.Second)
	for !supervisor.Status().Ready && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !supervisor.Status().Ready {
		t.Fatalf("expected restarted backend to be ready, got %+v", supervisor.Status())
	}
	if err := supervisor.Stop(); err != nil {
		t.Fatalf("stop restarted runtime: %v", err)
	}
}
