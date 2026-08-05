//go:build windows

package relay

import "os/exec"

// setDetach is a no-op on Windows (Phase 1 targets mac/linux for the
// interactive wakers; the worker fallback still starts, just without an
// explicit process-group detach). CREATE_NEW_PROCESS_GROUP can be added
// here if Windows worker support is pursued.
func setDetach(cmd *exec.Cmd) {}
