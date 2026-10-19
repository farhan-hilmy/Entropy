// Package checks proves that a dependency or service answers, using one cheap
// protocol-level request per kind. A plain "port is open" check is not enough:
// Toxiproxy accepts the connection first and only then dials the real dependency.
package checks

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const Timeout = 2 * time.Second

// Dependency checks a dependency through its proxy address.
func Dependency(ctx context.Context, kind, address string, redirect bool) error {
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	switch kind {
	case "redis":
		return withConn(ctx, address, redisPing)
	case "mongodb":
		return withConn(ctx, address, mongoHello)
	case "postgres":
		return withConn(ctx, address, postgresSSLRequest)
	case "rabbitmq":
		return withConn(ctx, address, amqpHeader)
	case "http":
		return httpGet(ctx, "http://"+address+"/", false)
	case "https":
		return tlsHandshake(ctx, address, redirect)
	}
	return fmt.Errorf("no check for kind %q", kind)
}

// Service checks the health URL if there is one, otherwise that the port accepts connections.
func Service(ctx context.Context, baseURL, healthCheck string) error {
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	if healthCheck != "" {
		return httpGet(ctx, strings.TrimRight(baseURL, "/")+healthCheck, true)
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return err
	}
	host := u.Host
	if u.Port() == "" {
		port := "80"
		if u.Scheme == "https" {
			port = "443"
		}
		host = net.JoinHostPort(u.Hostname(), port)
	}
	return withConn(ctx, host, func(net.Conn) error { return nil })
}

func withConn(ctx context.Context, address string, fn func(net.Conn) error) error {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", address)
	if err != nil {
		return err
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		conn.SetDeadline(deadline)
	}
	return fn(conn)
}

func redisPing(c net.Conn) error {
	if _, err := c.Write([]byte("*1\r\n$4\r\nPING\r\n")); err != nil {
		return err
	}
	line, err := bufio.NewReader(c).ReadString('\n')
	if err != nil {
		return fmt.Errorf("no reply to PING: %w", err)
	}
	// NOAUTH still proves Redis answered; credentials are the service's concern.
	if strings.HasPrefix(line, "+PONG") || strings.HasPrefix(line, "-NOAUTH") {
		return nil
	}
	return fmt.Errorf("unexpected reply to PING: %q", strings.TrimSpace(line))
}

// mongoHello sends an OP_MSG {hello: 1, $db: "admin"} and expects an OP_MSG reply.
func mongoHello(c net.Conn) error {
	const opMsg, requestID = 2013, 1
	doc := bsonDoc(bsonInt32("hello", 1), bsonString("$db", "admin"))
	body := append([]byte{0, 0, 0, 0, 0}, doc...) // flagBits, then section kind 0
	msg := make([]byte, 16, 16+len(body))
	binary.LittleEndian.PutUint32(msg[0:], uint32(16+len(body)))
	binary.LittleEndian.PutUint32(msg[4:], requestID)
	binary.LittleEndian.PutUint32(msg[12:], opMsg)
	msg = append(msg, body...)
	if _, err := c.Write(msg); err != nil {
		return err
	}
	header := make([]byte, 16)
	if _, err := io.ReadFull(c, header); err != nil {
		return fmt.Errorf("no reply to hello: %w", err)
	}
	if binary.LittleEndian.Uint32(header[8:]) != requestID || binary.LittleEndian.Uint32(header[12:]) != opMsg {
		return fmt.Errorf("unexpected reply to hello")
	}
	return nil
}

func bsonDoc(elems ...[]byte) []byte {
	size := 5
	for _, e := range elems {
		size += len(e)
	}
	out := binary.LittleEndian.AppendUint32(nil, uint32(size))
	for _, e := range elems {
		out = append(out, e...)
	}
	return append(out, 0)
}

func bsonInt32(name string, v int32) []byte {
	out := append([]byte{0x10}, name...)
	out = append(out, 0)
	return binary.LittleEndian.AppendUint32(out, uint32(v))
}

func bsonString(name, v string) []byte {
	out := append([]byte{0x02}, name...)
	out = append(out, 0)
	out = binary.LittleEndian.AppendUint32(out, uint32(len(v)+1))
	out = append(out, v...)
	return append(out, 0)
}

// postgresSSLRequest asks whether the server supports TLS; any 'S' or 'N' proves Postgres answered.
func postgresSSLRequest(c net.Conn) error {
	if _, err := c.Write([]byte{0, 0, 0, 8, 0x04, 0xd2, 0x16, 0x2f}); err != nil {
		return err
	}
	b := make([]byte, 1)
	if _, err := io.ReadFull(c, b); err != nil {
		return fmt.Errorf("no reply to SSLRequest: %w", err)
	}
	if b[0] != 'S' && b[0] != 'N' {
		return fmt.Errorf("unexpected reply to SSLRequest: %q", b[0])
	}
	return nil
}

// amqpHeader sends the AMQP 0-9-1 protocol header; the broker answers with
// Connection.Start (a method frame) or its own protocol header.
func amqpHeader(c net.Conn) error {
	if _, err := c.Write([]byte("AMQP\x00\x00\x09\x01")); err != nil {
		return err
	}
	b := make([]byte, 1)
	if _, err := io.ReadFull(c, b); err != nil {
		return fmt.Errorf("no reply to protocol header: %w", err)
	}
	if b[0] != 1 && b[0] != 'A' {
		return fmt.Errorf("unexpected reply to protocol header")
	}
	return nil
}

func httpGet(ctx context.Context, target string, requireOK bool) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if requireOK && resp.StatusCode >= 400 {
		return fmt.Errorf("GET %s: %s", target, resp.Status)
	}
	return nil
}

func tlsHandshake(ctx context.Context, address string, redirect bool) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	if redirect {
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return fmt.Errorf("resolve %s: %w", host, err)
		}
		for _, ip := range ips {
			if !ip.IP.IsLoopback() {
				return fmt.Errorf("%s resolves to %s, not 127.0.0.1: the hosts-file redirect is not active", host, ip.IP)
			}
		}
	}
	d := tls.Dialer{Config: &tls.Config{ServerName: host}}
	conn, err := d.DialContext(ctx, "tcp", address)
	if err != nil {
		return fmt.Errorf("TLS handshake: %w", err)
	}
	return conn.Close()
}
