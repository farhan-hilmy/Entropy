package mcpserver

import (
	"os/exec"
	"syscall"
)

// hideWindow keeps Windows from opening a console window for the started
// entropy up when the agent runs without one.
func hideWindow(cmd *exec.Cmd) {
	const createNoWindow = 0x08000000
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow}
}
