// Package containerhost finds the host where the container runtime publishes
// ports, by asking each likely host for Toxiproxy's version. It doesn't depend
// on any one runtime: Docker Desktop, Podman, Colima and others publish on one
// of these hosts.
package containerhost

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Candidate is a host to try and where Entropy heard of it.
type Candidate struct {
	Host   string `json:"host"`
	Source string `json:"source"`
}

const probeTimeout = 2 * time.Second

// Candidates lists the hosts to try, most likely first, without duplicates.
func Candidates(ctx context.Context) []Candidate {
	cands := []Candidate{{Host: "localhost", Source: "localhost"}}
	for _, env := range []string{"DOCKER_HOST", "CONTAINER_HOST"} {
		if h := hostFromURL(os.Getenv(env)); h != "" {
			cands = append(cands, Candidate{Host: h, Source: env})
		}
	}
	for _, ip := range wslAddresses(ctx) {
		cands = append(cands, Candidate{Host: ip, Source: "WSL"})
	}

	seen := map[string]bool{}
	var out []Candidate
	for _, c := range cands {
		if !seen[c.Host] {
			seen[c.Host] = true
			out = append(out, c)
		}
	}
	return out
}

// hostFromURL takes the host from a tcp:// or ssh:// engine address, e.g.
// DOCKER_HOST=tcp://192.168.64.2:2376. Local sockets and pipes have none.
func hostFromURL(s string) string {
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "tcp" && u.Scheme != "ssh") {
		return ""
	}
	return u.Hostname()
}

// Find returns the first candidate, in order, where Toxiproxy's API answers
// on port. All candidates are tried at once, so it takes at most probeTimeout,
// and returns as soon as every earlier candidate has failed.
func Find(ctx context.Context, port string, cands []Candidate) (Candidate, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	answers := make([]chan bool, len(cands))
	for i, c := range cands {
		answers[i] = make(chan bool, 1)
		go func() { answers[i] <- isToxiproxy(ctx, "http://"+net.JoinHostPort(c.Host, port)) }()
	}
	for i, answered := range answers {
		if <-answered {
			return cands[i], nil
		}
	}
	tried := make([]string, len(cands))
	for i, c := range cands {
		tried[i] = fmt.Sprintf("%s (%s)", c.Host, c.Source)
	}
	return Candidate{}, errors.New("Toxiproxy didn't answer on port " + port + " at any of: " + strings.Join(tried, ", "))
}

func isToxiproxy(ctx context.Context, base string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/version", nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}
