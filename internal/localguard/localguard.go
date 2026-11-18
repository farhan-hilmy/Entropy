// Package localguard keeps web pages from using Entropy's local HTTP APIs.
//
// Listening on 127.0.0.1 keeps other machines out, but not the browser on
// this one: any page you open could send requests to 127.0.0.1:7400 (start a
// run, break Redis), or use DNS rebinding (a site's name that resolves to
// 127.0.0.1) to also read the answers, such as the config file. Two checks
// stop that, and neither affects curl, scripts or agents:
//
//   - Host: the request must be addressed to this machine by a loopback name
//     or IP (localhost, 127.0.0.1, ::1), or to the exact address Entropy was
//     told to listen on. A rebinding site's requests carry its own name.
//   - Cross-origin: changes (POST, PATCH, DELETE, …) from a page on another
//     origin are refused, using the browser's Sec-Fetch-Site and Origin
//     headers (net/http's CrossOriginProtection). Entropy's own UI is the
//     same origin, and tools that don't send these headers are unaffected.
package localguard

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
)

// Wrap guards h, served on the listen address (host:port). When that host is
// unspecified (0.0.0.0, ::, or empty: every interface), any Host is accepted,
// since Entropy was deliberately opened to the network; the cross-origin
// check still applies.
func Wrap(h http.Handler, listen string) http.Handler {
	listenHost := hostOnly(listen)
	anyHost := listenHost == ""
	if ip := net.ParseIP(listenHost); ip != nil && ip.IsUnspecified() {
		anyHost = true
	}

	cop := http.NewCrossOriginProtection()
	cop.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refuse(w, "refused a change sent by a web page on another site; use Entropy's own UI, or call the API directly (curl, scripts, agents)")
	}))
	guarded := cop.Handler(h)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !anyHost && !allowedHost(r.Host, listenHost) {
			refuse(w, fmt.Sprintf("refused a request addressed to %q: Entropy only answers requests addressed to localhost or 127.0.0.1", r.Host))
			return
		}
		guarded.ServeHTTP(w, r)
	})
}

func allowedHost(hostport, listenHost string) bool {
	h := hostOnly(hostport)
	if h == "" {
		return false
	}
	if h == "localhost" || h == listenHost {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// hostOnly is the host of host[:port], lowercased, without IPv6 brackets or
// a trailing dot.
func hostOnly(hostport string) string {
	h := hostport
	if host, _, err := net.SplitHostPort(hostport); err == nil {
		h = host
	}
	h = strings.TrimSuffix(strings.Trim(h, "[]"), ".")
	return strings.ToLower(h)
}

func refuse(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
