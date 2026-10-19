// Package toxiproxy is a minimal client for the Toxiproxy HTTP API,
// covering only what Entropy uses.
package toxiproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Client struct {
	base string
	http *http.Client
}

func New(base string) *Client {
	return &Client{
		base: strings.TrimRight(base, "/"),
		http: &http.Client{Timeout: 3 * time.Second},
	}
}

func (c *Client) Base() string { return c.base }

type Proxy struct {
	Name     string  `json:"name"`
	Listen   string  `json:"listen"`
	Upstream string  `json:"upstream"`
	Enabled  bool    `json:"enabled"`
	Toxics   []Toxic `json:"toxics,omitempty"`
}

type Toxic struct {
	Name       string         `json:"name"`
	Type       string         `json:"type"`
	Stream     string         `json:"stream"`
	Toxicity   float64        `json:"toxicity"`
	Attributes map[string]any `json:"attributes"`
}

// Proxies returns every proxy Toxiproxy knows, keyed by name.
func (c *Client) Proxies(ctx context.Context) (map[string]Proxy, error) {
	var out map[string]Proxy
	err := c.do(ctx, http.MethodGet, "/proxies", nil, &out)
	return out, err
}

func (c *Client) CreateProxy(ctx context.Context, p Proxy) error {
	return c.do(ctx, http.MethodPost, "/proxies", p, nil)
}

// UpdateProxy changes listen, upstream and enabled on an existing proxy.
func (c *Client) UpdateProxy(ctx context.Context, p Proxy) error {
	return c.do(ctx, http.MethodPost, "/proxies/"+url.PathEscape(p.Name), p, nil)
}

// Reset enables every proxy and removes every toxic.
func (c *Client) Reset(ctx context.Context) error {
	return c.do(ctx, http.MethodPost, "/reset", nil, nil)
}

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("toxiproxy not reachable at %s: %w", c.base, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("toxiproxy %s %s: %s: %s", method, path, resp.Status, strings.TrimSpace(string(msg)))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
