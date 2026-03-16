//go:build !windows

package greptimedb

import (
	"os"
	"os/exec"
)

func setProcAttr(_ *exec.Cmd) {}

func sendInterrupt(p *os.Process) error {
	return p.Signal(os.Interrupt)
}
