// Package command runs a command line from entropy.yaml, such as a `bru run`,
// and reports its exit code, duration and output.
package command

import (
	"context"
	"errors"
	"os"
	"time"
)

// MaxOutput is how much output is kept: the last 16 KB, where errors usually are.
const MaxOutput = 16 << 10

type Result struct {
	ExitCode int
	TimedOut bool
	Duration time.Duration
	Output   string
	// Err is set when the command couldn't run at all, e.g. a bad working directory.
	Err error
}

// Run runs line in dir with a shell (cmd.exe on Windows, sh elsewhere). When
// the timeout passes, the whole process tree is killed, not just the shell.
func Run(ctx context.Context, line, dir string, timeout time.Duration, env []string) Result {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := shellCommand(ctx, line)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	out := &tail{max: MaxOutput}
	cmd.Stdout = out
	cmd.Stderr = out
	// A grandchild (e.g. node under bru) can keep the output pipe open after
	// the kill; don't wait for it forever.
	cmd.WaitDelay = 2 * time.Second

	start := time.Now()
	err := cmd.Run()
	res := Result{Duration: time.Since(start), Output: out.String(), ExitCode: -1}

	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		res.TimedOut = true
	case cmd.ProcessState != nil:
		res.ExitCode = cmd.ProcessState.ExitCode()
	case ctx.Err() != nil:
		res.Err = ctx.Err()
	default:
		res.Err = err
	}
	return res
}

// tail keeps the last max bytes written to it. exec.Cmd writes from a single
// goroutine when Stdout and Stderr are the same writer, so no lock is needed.
type tail struct {
	buf       []byte
	max       int
	truncated bool
}

func (t *tail) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - t.max; over > 0 {
		t.buf = t.buf[over:]
		t.truncated = true
	}
	return len(p), nil
}

func (t *tail) String() string {
	if t.truncated {
		return "… (earlier output cut)\n" + string(t.buf)
	}
	return string(t.buf)
}
