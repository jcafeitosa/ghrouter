//go:build darwin || linux || freebsd || openbsd || netbsd || dragonfly

package providers

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"ghrouter/internal/types"
)

func TestProviderTimeoutKillsChildProcesses(t *testing.T) {
	tmpDir := t.TempDir()
	cliPath := filepath.Join(tmpDir, "spawning-cli")
	pidPath := filepath.Join(tmpDir, "child.pid")
	script := "#!/bin/sh\nsleep 30 &\nprintf '%s\\n' \"$!\" > " + strconv.Quote(pidPath) + "\nwait\n"
	if err := os.WriteFile(cliPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	runner := NewProviderRunner(&types.Provider{Name: "spawning", CLIPath: cliPath, WorkDir: tmpDir, Models: []string{"model"}, Timeout: 3 * time.Second})
	_, errs := runner.Invoke(context.Background(), &types.OpenAIRequest{Model: "model"})
	err := <-errs
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected provider timeout, got %v", err)
	}

	var childPID int
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		data, readErr := os.ReadFile(pidPath)
		if readErr == nil {
			childPID, _ = strconv.Atoi(strings.TrimSpace(string(data)))
			if childPID != 0 {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if childPID == 0 {
		t.Fatal("CLI did not record its child process")
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(childPID, 0); err != nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("provider child process %d survived timeout", childPID)
}
