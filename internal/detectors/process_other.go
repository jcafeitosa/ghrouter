//go:build windows || plan9 || js

package detectors

import "os/exec"

func prepareDiscoveryCommand(cmd *exec.Cmd) {}

func killDiscoveryProcess(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
