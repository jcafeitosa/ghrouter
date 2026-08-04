//go:build darwin || linux || freebsd || openbsd || netbsd || dragonfly

package detectors

import (
	"os/exec"
	"syscall"
	"time"
)

func prepareDiscoveryCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 500 * time.Millisecond
	if cmd.Cancel != nil {
		cmd.Cancel = func() error {
			killDiscoveryProcess(cmd)
			return nil
		}
	}
}

func killDiscoveryProcess(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
