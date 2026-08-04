//go:build windows

package account

import (
	"context"
	"os"
	"os/exec"
	"time"
)

func prepareAuthCommand(cmd *exec.Cmd) {}

func killAuthProcess(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

func runAuthCommand(ctx context.Context, cmd *exec.Cmd) ([]byte, error) {
	stdout, err := os.CreateTemp("", "ghrouter-auth-stdout-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(stdout.Name())
	defer stdout.Close()
	stderr, err := os.CreateTemp("", "ghrouter-auth-stderr-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(stderr.Name())
	defer stderr.Close()
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			killAuthProcess(cmd)
		case <-done:
		}
	}()
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()
	var runErr error
	select {
	case runErr = <-waitCh:
	case <-ctx.Done():
		killAuthProcess(cmd)
		select {
		case runErr = <-waitCh:
		case <-time.After(time.Second):
			runErr = ctx.Err()
		}
	}
	close(done)
	if ctx.Err() != nil && runErr == nil {
		runErr = ctx.Err()
	}
	data, readErr := os.ReadFile(stdout.Name())
	if runErr == nil && readErr != nil {
		runErr = readErr
	}
	return data, runErr
}
