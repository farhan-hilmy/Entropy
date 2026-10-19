package command

import (
	"context"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestRunCapturesOutputAndExitCode(t *testing.T) {
	res := Run(context.Background(), `echo hello "quoted world" && exit 3`, t.TempDir(), 10*time.Second, nil)
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	if res.ExitCode != 3 {
		t.Errorf("exit code = %d, want 3", res.ExitCode)
	}
	if !strings.Contains(res.Output, `hello "quoted world"`) && !strings.Contains(res.Output, "hello quoted world") {
		t.Errorf("output = %q", res.Output)
	}
}

func TestRunPassesEnvironment(t *testing.T) {
	line := "echo $ENTROPY_BASE_URL"
	if runtime.GOOS == "windows" {
		line = "echo %ENTROPY_BASE_URL%"
	}
	res := Run(context.Background(), line, t.TempDir(), 10*time.Second, []string{"ENTROPY_BASE_URL=http://127.0.0.1:8080"})
	if res.ExitCode != 0 || !strings.Contains(res.Output, "http://127.0.0.1:8080") {
		t.Fatalf("exit %d, output %q", res.ExitCode, res.Output)
	}
}

func TestRunTimesOut(t *testing.T) {
	line := "sleep 30"
	if runtime.GOOS == "windows" {
		line = "ping -n 30 127.0.0.1 >nul"
	}
	start := time.Now()
	res := Run(context.Background(), line, t.TempDir(), time.Second, nil)
	if !res.TimedOut {
		t.Fatalf("expected a timeout, got exit %d err %v", res.ExitCode, res.Err)
	}
	if took := time.Since(start); took > 10*time.Second {
		t.Errorf("took %v to give up; the process tree was probably not killed", took)
	}
}

func TestRunReportsBadDirectory(t *testing.T) {
	res := Run(context.Background(), "echo hi", "Z:/does/not/exist", 5*time.Second, nil)
	if res.Err == nil {
		t.Fatal("expected an error for a missing directory")
	}
}

func TestTailKeepsTheEnd(t *testing.T) {
	tl := &tail{max: 5}
	tl.Write([]byte("abc"))
	tl.Write([]byte("defgh"))
	if got := tl.String(); !strings.HasSuffix(got, "defgh") || !strings.Contains(got, "cut") {
		t.Errorf("tail = %q", got)
	}
}
