//go:build darwin || linux || freebsd || openbsd || netbsd || dragonfly

package providers

import (
	"os/exec"
	"syscall"
)

func prepareProviderCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killProviderProcess(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	_ = cmd.Process.Kill()
}
