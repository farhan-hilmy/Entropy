package containerhost

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFindPrefersEarlierCandidates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/version" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"version":"2.12.0"}`))
	}))
	defer srv.Close()
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())

	// The server listens on 127.0.0.1 only, so 127.0.0.2 refuses.
	got, err := Find(context.Background(), port, []Candidate{
		{Host: "127.0.0.2", Source: "nothing there"},
		{Host: "127.0.0.1", Source: "first answer"},
		{Host: "localhost", Source: "later answer"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != "first answer" {
		t.Errorf("found %+v, want the first candidate that answers", got)
	}
}

func TestFindListsWhatItTried(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	ln.Close() // nothing listens on port now

	_, err = Find(context.Background(), port, []Candidate{{Host: "127.0.0.1", Source: "localhost"}})
	if err == nil || !strings.Contains(err.Error(), "127.0.0.1 (localhost)") {
		t.Fatalf("expected an error naming the tried host, got %v", err)
	}
}

func TestHostFromURL(t *testing.T) {
	for in, want := range map[string]string{
		"tcp://192.168.64.2:2376":        "192.168.64.2",
		"ssh://user@build-box":           "build-box",
		"unix:///var/run/docker.sock":    "",
		"npipe:////./pipe/docker_engine": "",
		"":                               "",
	} {
		if got := hostFromURL(in); got != want {
			t.Errorf("hostFromURL(%q) = %q, want %q", in, got, want)
		}
	}
}
