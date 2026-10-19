//go:build !windows

package command

import (
	"context"
	"os/exec"
	"syscall"
)

func shellCommand(ctx context.Context, line string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", line)
	// Own process group, so the timeout can kill everything the command started.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	return cmd
}
