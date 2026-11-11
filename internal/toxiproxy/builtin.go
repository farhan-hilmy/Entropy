package toxiproxy

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"

	tpserver "github.com/Shopify/toxiproxy/v2"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog"
)

// Builtin is a Toxiproxy server running inside Entropy's own process, used
// when entropy.yaml doesn't name a toxiproxy.api. Entropy talks to it over
// HTTP like to any other Toxiproxy, so faults, route checks and runs work
// the same in both modes.
type Builtin struct {
	srv  *tpserver.ApiServer
	http *http.Server
	url  string
}

// StartBuiltin starts the server with its API on a free loopback port: it
// can't clash with a Toxiproxy container on 8474, and nothing outside this
// machine can reach it.
func StartBuiltin() (*Builtin, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("starting the built-in Toxiproxy: %w", err)
	}
	srv := tpserver.NewServer(tpserver.NewMetricsContainer(prometheus.NewRegistry()), zerolog.Nop())
	b := &Builtin{
		srv:  srv,
		http: &http.Server{Handler: srv.Routes(), ReadHeaderTimeout: 10 * time.Second},
		url:  "http://" + ln.Addr().String(),
	}
	go b.http.Serve(ln)
	return b, nil
}

// URL is the base URL of the server's API.
func (b *Builtin) URL() string { return b.url }

// Reset removes every proxy, which closes their ports and drops their toxics.
func (b *Builtin) Reset() { b.srv.Collection.Clear() }

// Close stops the API and every proxy, freeing all their ports.
func (b *Builtin) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := b.http.Shutdown(ctx)
	b.Reset()
	return err
}
