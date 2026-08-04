//go:build darwin || linux || freebsd || openbsd || netbsd || dragonfly

package account

import (
	"context"
	"os"
	"os/exec"
	"syscall"
	"time"
)

func prepareAuthCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killAuthProcess(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	_ = cmd.Process.Kill()
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
	cmd.WaitDelay = 500 * time.Millisecond
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
	if _, err := stdout.Seek(0, 0); err != nil {
		return nil, runErr
	}
	data, readErr := os.ReadFile(stdout.Name())
	if runErr == nil && readErr != nil {
		runErr = readErr
	}
	return data, runErr
}
