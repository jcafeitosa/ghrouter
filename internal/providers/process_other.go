//go:build windows || plan9 || js

package providers

import "os/exec"

func prepareProviderCommand(cmd *exec.Cmd) {}

func killProviderProcess(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
