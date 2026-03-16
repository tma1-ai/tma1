//go:build windows

package greptimedb

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

func setProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
	}
}

// sendInterrupt attempts to send CTRL_BREAK_EVENT via GenerateConsoleCtrlEvent.
// This is best-effort: it works when the process has a console (interactive use),
// but fails when running as a scheduled task (no console). The caller must
// fall back to Kill() on error.
func sendInterrupt(p *os.Process) error {
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	proc := kernel32.NewProc("GenerateConsoleCtrlEvent")
	r, _, err := proc.Call(uintptr(1), uintptr(p.Pid)) // 1 = CTRL_BREAK_EVENT
	if r == 0 {
		return fmt.Errorf("GenerateConsoleCtrlEvent: %w", err)
	}
	return nil
}
