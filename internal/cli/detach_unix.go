//go:build darwin || linux || freebsd || openbsd || netbsd || dragonfly

package cli

import (
	"syscall"
)

func detachProcess() {
	_ = syscall.Setpgid(0, 0)
}
