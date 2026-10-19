//go:build windows

package command

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"syscall"
)

func shellCommand(ctx context.Context, line string) *exec.Cmd {
	comspec := os.Getenv("ComSpec")
	if comspec == "" {
		comspec = "cmd.exe"
	}
	cmd := exec.CommandContext(ctx, comspec)
	// Hand the line to cmd.exe untouched, so the user's own quoting works
	// exactly as it does when typed into a terminal.
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `cmd.exe /S /C "` + line + `"`}
	// Killing cmd.exe alone would leave bru and node running.
	cmd.Cancel = func() error {
		return exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
	}
	return cmd
}
