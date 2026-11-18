package localguard

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWrap(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	type req struct {
		name, method, host string
		headers            map[string]string
		want               int
	}
	cases := []struct {
		listen string
		reqs   []req
	}{
		{"127.0.0.1:7400", []req{
			{"the UI's own request", "POST", "127.0.0.1:7400", map[string]string{"Sec-Fetch-Site": "same-origin", "Origin": "http://127.0.0.1:7400"}, 200},
			{"localhost works too", "GET", "localhost:7400", nil, 200},
			{"IPv6 loopback", "GET", "[::1]:7400", nil, 200},
			{"curl or an agent: no browser headers", "POST", "127.0.0.1:7400", nil, 200},
			{"the Vite dev server's proxy", "PATCH", "localhost:5173", map[string]string{"Sec-Fetch-Site": "same-origin", "Origin": "http://localhost:5173"}, 200},
			{"typed in the address bar", "GET", "127.0.0.1:7400", map[string]string{"Sec-Fetch-Site": "none"}, 200},
			{"another site starts a run", "POST", "127.0.0.1:7400", map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "https://evil.example"}, 403},
			{"another site, an older browser", "POST", "127.0.0.1:7400", map[string]string{"Origin": "https://evil.example"}, 403},
			{"another local port", "DELETE", "127.0.0.1:7400", map[string]string{"Sec-Fetch-Site": "same-site", "Origin": "http://127.0.0.1:3000"}, 403},
			{"DNS rebinding reads the config", "GET", "evil.example:7400", nil, 403},
			{"DNS rebinding, no port", "GET", "evil.example", nil, 403},
			{"no Host", "GET", "", nil, 403},
			{"a cross-site GET passes (the browser's CORS keeps the answer from the page)", "GET", "127.0.0.1:7400", map[string]string{"Sec-Fetch-Site": "cross-site"}, 200},
		}},
		{"192.168.1.5:7400", []req{
			{"the address it listens on", "GET", "192.168.1.5:7400", nil, 200},
			{"loopback still works", "GET", "127.0.0.1:7400", nil, 200},
			{"another name", "GET", "evil.example:7400", nil, 403},
		}},
		{"0.0.0.0:7400", []req{
			{"opened to the network: any Host", "GET", "entropy.team.internal", nil, 200},
			{"but still no cross-site changes", "POST", "entropy.team.internal", map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "https://evil.example"}, 403},
		}},
	}
	for _, c := range cases {
		h := Wrap(ok, c.listen)
		for _, q := range c.reqs {
			r := httptest.NewRequest(q.method, "/api/experiments/x/run", nil)
			r.Host = q.host
			for k, v := range q.headers {
				r.Header.Set(k, v)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != q.want {
				t.Errorf("listen %s, %s: got %d, want %d (%s)", c.listen, q.name, w.Code, q.want, w.Body.String())
			}
		}
	}
}
