package mcpserver

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Starter starts `entropy up` when an agent needs Entropy and nothing answers
// at the URL. The Entropy it starts lives as long as this MCP server: it gets
// a pipe on stdin and stops cleanly (removing every fault) when the pipe
// closes, which the OS does even if this process is killed.
type Starter struct {
	Exe    string // the entropy binary
	Config string // absolute path of entropy.yaml; "" = none found
	Addr   string // host:port for entropy up -addr, from the URL
	// Ready is how long to wait for the started Entropy to answer.
	Ready time.Duration

	mu    sync.Mutex
	cmd   *exec.Cmd
	stdin io.WriteCloser
	exit  chan struct{} // closed when the started process exits
	log   string
}

// NewStarter is a Starter for the Entropy at base, using the binary exe and
// the config file config ("" looks for entropy.yaml in the current folder).
// It returns nil when base isn't on this machine: a remote Entropy is
// someone else's to start.
func NewStarter(base, exe, config string) (*Starter, error) {
	u, err := url.Parse(base)
	if err != nil {
		return nil, err
	}
	host := u.Hostname()
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return nil, nil
	}
	port := u.Port()
	if port == "" {
		port = "80"
	}
	s := &Starter{Exe: exe, Addr: net.JoinHostPort(host, port), Ready: 60 * time.Second}
	if config == "" {
		if _, err := os.Stat("entropy.yaml"); err != nil {
			return s, nil // nothing to start with; Ensure says what to do
		}
		config = "entropy.yaml"
	}
	if s.Config, err = filepath.Abs(config); err != nil {
		return nil, err
	}
	if _, err := os.Stat(s.Config); err != nil {
		return nil, fmt.Errorf("-config: %w", err)
	}
	return s, nil
}

// Ensure starts Entropy unless this Starter already did and it's still
// running, then waits until it answers at base. started reports whether this
// call started it.
func (s *Starter) Ensure(ctx context.Context, base string) (started bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Config == "" {
		wd, _ := os.Getwd()
		return false, fmt.Errorf("there's no entropy.yaml in %s to start it with: add -config D:/path/to/entropy.yaml after `mcp` in the `claude mcp add` line, or start `entropy up` yourself", wd)
	}
	if !s.running() {
		if err := s.start(); err != nil {
			return false, err
		}
		started = true
	}
	return started, s.waitReady(ctx, base)
}

func (s *Starter) running() bool {
	if s.cmd == nil {
		return false
	}
	select {
	case <-s.exit:
		return false
	default:
		return true
	}
}

func (s *Starter) start() error {
	dir := filepath.Dir(s.Config)
	if err := os.MkdirAll(filepath.Join(dir, ".entropy"), 0o755); err != nil {
		return err
	}
	s.log = filepath.Join(dir, ".entropy", "entropy-up.log")
	logf, err := os.Create(s.log)
	if err != nil {
		return err
	}
	// Run where entropy.yaml is, as a person would: the database and the
	// commands' default folder are then the same as with a manual entropy up.
	cmd := exec.Command(s.Exe, "up", "-config", filepath.Base(s.Config), "-addr", s.Addr, "-stop-with-stdin")
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = logf, logf
	hideWindow(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		logf.Close()
		return err
	}
	if err := cmd.Start(); err != nil {
		logf.Close()
		return fmt.Errorf("starting entropy up: %w", err)
	}
	exit := make(chan struct{})
	go func() {
		cmd.Wait()
		logf.Close()
		close(exit)
	}()
	s.cmd, s.stdin, s.exit = cmd, stdin, exit
	fmt.Fprintf(os.Stderr, "entropy mcp: started entropy up (pid %d) for %s, log %s\n", cmd.Process.Pid, s.Config, s.log)
	return nil
}

// waitReady polls the API until it answers, the started process exits, or
// Ready passes.
func (s *Starter) waitReady(ctx context.Context, base string) error {
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(s.Ready)
	for {
		req, _ := http.NewRequestWithContext(ctx, "GET", base+"/api/environment", nil)
		if res, err := client.Do(req); err == nil {
			res.Body.Close()
			return nil
		}
		select {
		case <-s.exit:
			return fmt.Errorf("started entropy up, but it stopped: %s", s.logTail())
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("started entropy up, but it didn't answer within %s; its log: %s", s.Ready, s.log)
		}
	}
}

// logTail is the end of the started Entropy's log, where its error is.
func (s *Starter) logTail() string {
	b, err := os.ReadFile(s.log)
	if err != nil {
		return "see " + s.log
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	return strings.Join(lines[max(0, len(lines)-3):], " | ") + " (log: " + s.log + ")"
}

// Stop ends the Entropy this Starter started, if any, and waits for it to
// remove its faults and exit.
func (s *Starter) Stop(wait time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running() {
		return
	}
	s.stdin.Close()
	select {
	case <-s.exit:
	case <-time.After(wait):
		s.cmd.Process.Kill()
	}
}
