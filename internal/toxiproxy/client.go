// Package toxiproxy is a minimal client for the Toxiproxy HTTP API,
// covering only what Entropy uses.
package toxiproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

func (c *Client) Proxy(ctx context.Context, name string) (Proxy, error) {
	var out Proxy
	err := c.do(ctx, http.MethodGet, proxyPath(name), nil, &out)
	return out, err
}

func (c *Client) CreateProxy(ctx context.Context, p Proxy) error {
	return c.do(ctx, http.MethodPost, "/proxies", p, nil)
}

// UpdateProxy changes listen, upstream and enabled on an existing proxy.
func (c *Client) UpdateProxy(ctx context.Context, p Proxy) error {
	return c.do(ctx, http.MethodPost, proxyPath(p.Name), p, nil)
}

// SetEnabled enables or disables one proxy, leaving listen and upstream as
// they are. A disabled proxy closes its connections and refuses new ones.
func (c *Client) SetEnabled(ctx context.Context, proxy string, enabled bool) error {
	return c.do(ctx, http.MethodPost, proxyPath(proxy), map[string]bool{"enabled": enabled}, nil)
}

func (c *Client) AddToxic(ctx context.Context, proxy string, t Toxic) error {
	return c.do(ctx, http.MethodPost, proxyPath(proxy)+"/toxics", t, nil)
}

func (c *Client) RemoveToxic(ctx context.Context, proxy, toxic string) error {
	return c.do(ctx, http.MethodDelete, proxyPath(proxy)+"/toxics/"+url.PathEscape(toxic), nil, nil)
}

// Reset enables every proxy and removes every toxic.
func (c *Client) Reset(ctx context.Context) error {
	return c.do(ctx, http.MethodPost, "/reset", nil, nil)
}

// StatusError is a response from Toxiproxy outside 2xx.
type StatusError struct {
	Method, Path string
	Code         int
	Status       string
	Body         string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("toxiproxy %s %s: %s: %s", e.Method, e.Path, e.Status, e.Body)
}

// IsNotFound reports whether Toxiproxy answered 404: no such proxy or toxic.
func IsNotFound(err error) bool { return hasCode(err, http.StatusNotFound) }

// IsConflict reports whether Toxiproxy answered 409, e.g. the toxic already exists.
func IsConflict(err error) bool { return hasCode(err, http.StatusConflict) }

func hasCode(err error, code int) bool {
	var se *StatusError
	return errors.As(err, &se) && se.Code == code
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
		return &StatusError{Method: method, Path: path, Code: resp.StatusCode, Status: resp.Status, Body: strings.TrimSpace(string(msg))}
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func proxyPath(name string) string { return "/proxies/" + url.PathEscape(name) }
