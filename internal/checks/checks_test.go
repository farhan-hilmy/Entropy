package checks

import (
	"context"
	"net"
	"strings"
	"testing"
)

// serve accepts one connection and hands it to handle.
func serve(t *testing.T, handle func(net.Conn)) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		handle(c)
	}()
	return ln.Addr().String()
}

func TestRedisPing(t *testing.T) {
	addr := serve(t, func(c net.Conn) {
		c.Read(make([]byte, 64))
		c.Write([]byte("+PONG\r\n"))
	})
	if err := Dependency(context.Background(), "redis", addr, false); err != nil {
		t.Fatal(err)
	}
}

// Toxiproxy accepts the connection, then closes it when the upstream is down.
// That must count as unreachable, which is why port-open checks aren't used.
func TestRedisAcceptedThenClosedIsUnreachable(t *testing.T) {
	addr := serve(t, func(c net.Conn) {})
	err := Dependency(context.Background(), "redis", addr, false)
	if err == nil || !strings.Contains(err.Error(), "no reply to PING") {
		t.Fatalf("expected no reply error, got %v", err)
	}
}

func TestPostgresSSLRequest(t *testing.T) {
	addr := serve(t, func(c net.Conn) {
		c.Read(make([]byte, 8))
		c.Write([]byte("N"))
	})
	if err := Dependency(context.Background(), "postgres", addr, false); err != nil {
		t.Fatal(err)
	}
}

func TestBSONHelloDocument(t *testing.T) {
	doc := bsonDoc(bsonInt32("hello", 1), bsonString("$db", "admin"))
	// 4 (size) + 1+6+4 (hello) + 1+4+4+6 ($db) + 1 (terminator)
	if len(doc) != 31 || doc[0] != 31 || doc[len(doc)-1] != 0 {
		t.Fatalf("unexpected BSON document: % x", doc)
	}
}
