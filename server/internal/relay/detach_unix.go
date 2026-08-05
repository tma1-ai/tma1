//go:build !windows

package relay

import (
	"os/exec"
	"syscall"
)

// setDetach puts the worker in its own process group so it survives the
// tma1-server request that spawned it.
func setDetach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}
