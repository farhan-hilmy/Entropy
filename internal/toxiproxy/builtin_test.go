package toxiproxy

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"testing"
	"time"
)

// echoServer answers each line with "echo:<line>" until the test ends.
func echoServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				s := bufio.NewScanner(c)
				for s.Scan() {
					fmt.Fprintln(c, "echo:"+s.Text())
				}
			}()
		}
	}()
	return ln.Addr().String()
}

// freePort returns a loopback address nothing listens on right now.
func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

// roundTrip sends one line to addr and returns the answer and how long it took.
func roundTrip(addr string) (string, time.Duration, error) {
	start := time.Now()
	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		return "", 0, err
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	fmt.Fprintln(c, "ping")
	line, err := bufio.NewReader(c).ReadString('\n')
	if err != nil {
		return "", 0, err
	}
	return line[:len(line)-1], time.Since(start), nil
}

func TestBuiltinCarriesTrafficAppliesFaultsAndFreesPorts(t *testing.T) {
	b, err := StartBuiltin()
	if err != nil {
		t.Fatal(err)
	}
	closed := false
	t.Cleanup(func() {
		if !closed {
			b.Close()
		}
	})
	ctx := context.Background()
	tp := New(b.URL())
	listen := freePort(t)

	if err := tp.CreateProxy(ctx, Proxy{Name: "echo", Listen: listen, Upstream: echoServer(t), Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if got, _, err := roundTrip(listen); err != nil || got != "echo:ping" {
		t.Fatalf("through the proxy: %q, %v", got, err)
	}

	err = tp.AddToxic(ctx, "echo", Toxic{Name: "slow", Type: "latency", Stream: "downstream", Toxicity: 1,
		Attributes: map[string]any{"latency": 300}})
	if err != nil {
		t.Fatal(err)
	}
	if _, took, err := roundTrip(listen); err != nil || took < 300*time.Millisecond {
		t.Fatalf("with 300 ms latency: took %v, %v", took, err)
	}

	if err := tp.SetEnabled(ctx, "echo", false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := roundTrip(listen); err == nil {
		t.Fatal("proxy disabled, but traffic still went through")
	}
	if err := tp.SetEnabled(ctx, "echo", true); err != nil {
		t.Fatal(err)
	}

	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	closed = true
	if c, err := net.DialTimeout("tcp", listen, time.Second); err == nil {
		c.Close()
		t.Fatalf("%s still accepts connections after Close", listen)
	}
	if _, err := tp.Proxies(ctx); err == nil {
		t.Fatal("API still answers after Close")
	}
}

func TestIsPortInUse(t *testing.T) {
	b, err := StartBuiltin()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { taken.Close() })

	err = New(b.URL()).CreateProxy(context.Background(), Proxy{Name: "x", Listen: taken.Addr().String(), Upstream: "127.0.0.1:1", Enabled: true})
	if !IsPortInUse(err) {
		t.Fatalf("IsPortInUse(%v) = false", err)
	}
	if IsPortInUse(&StatusError{Code: 500, Body: "something else"}) {
		t.Error("IsPortInUse true for an unrelated error")
	}
}
