// Package abs is a minimal client for the Audiobookshelf HTTP API. It speaks
// the "old JSON" shapes the server returns to its own web client (the public
// API docs are out of date; the server source is the reference, see
// docs/README.md). Everything above this package works with the typed structs
// here and never sees raw payloads.
package abs

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	maxResponseBytes = 64 << 20
	errBodyPreview   = 300
)

type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

// New returns a client for the Audiobookshelf server at baseURL that
// authenticates with an API key (Settings -> Users -> API Keys) or a user
// token, sent as a bearer token.
func New(baseURL, token string) (*Client, error) {
	if baseURL == "" {
		return nil, errors.New("server URL is required (--server / ABS_SERVER)")
	}
	if token == "" {
		return nil, errors.New("API key is required (--token / ABS_TOKEN)")
	}

	u, err := url.Parse(baseURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("server URL %q must include a scheme and host, e.g. http://nas:13378", baseURL)
	}
	if u.User != nil {
		return nil, errors.New("server URL must not contain credentials; pass the API key via --token / ABS_TOKEN")
	}

	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    &http.Client{Timeout: 120 * time.Second},
	}, nil
}

// BaseURL is the server address the client was created with, without a
// trailing slash.
func (c *Client) BaseURL() string { return c.baseURL }

// HTTPError is returned for non-2xx responses. Callers can inspect the status
// (e.g. 404 for "not found", 403 for a permission the API key lacks).
type HTTPError struct {
	Method string
	Path   string
	Status int
	Body   string
}

func (e *HTTPError) Error() string {
	msg := fmt.Sprintf("%s %s: HTTP %d", e.Method, e.Path, e.Status)
	if e.Body != "" {
		msg += ": " + e.Body
	}
	switch e.Status {
	case http.StatusUnauthorized:
		msg += " (API key rejected; check ABS_TOKEN)"
	case http.StatusForbidden:
		msg += " (the API key's user lacks permission for this; most write operations need an admin account)"
	}

	return msg
}

// IsNotFound reports whether err is an HTTP 404 from the server.
func IsNotFound(err error) bool {
	var he *HTTPError
	return errors.As(err, &he) && he.Status == http.StatusNotFound
}

// do performs a request. body (when non-nil) is sent as JSON; out (when
// non-nil) receives the decoded JSON response.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	raw, err := c.doRaw(ctx, method, path, query, body)
	if err != nil {
		return err
	}

	if out == nil || len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}

	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%s %s: decoding response: %w", method, path, err)
	}

	return nil
}

func (c *Client) doRaw(ctx context.Context, method, path string, query url.Values, body any) ([]byte, error) {
	u := c.baseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}

	var reqBody io.Reader = http.NoBody
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reqBody = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, u, reqBody)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "abs-mcp")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, err
	}

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, &HTTPError{Method: method, Path: path, Status: resp.StatusCode, Body: truncate(strings.TrimSpace(string(raw)), errBodyPreview)}
	}

	return raw, nil
}

func (c *Client) get(ctx context.Context, path string, query url.Values, out any) error {
	return c.do(ctx, http.MethodGet, path, query, nil, out)
}

func (c *Client) post(ctx context.Context, path string, query url.Values, body, out any) error {
	return c.do(ctx, http.MethodPost, path, query, body, out)
}

func (c *Client) patch(ctx context.Context, path string, body, out any) error {
	return c.do(ctx, http.MethodPatch, path, nil, body, out)
}

func (c *Client) del(ctx context.Context, path string, query url.Values) error {
	return c.do(ctx, http.MethodDelete, path, query, nil, nil)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// EncodeFilter builds the value for the `filter` query parameter of list
// endpoints: "<group>.<base64(value)>", e.g. genres, tags, series, authors,
// narrators, languages, publishers, publishedDecades, progress, missing,
// issues, ebooks, tracks, abridged, feed-open, recent.
func EncodeFilter(group, value string) string {
	if group == "" {
		return ""
	}
	if value == "" {
		return group
	}
	return group + "." + base64.StdEncoding.EncodeToString([]byte(value))
}

func boolQuery(v bool) string {
	if v {
		return "1"
	}
	return "0"
}

func intQuery(q url.Values, key string, v int) {
	if v > 0 {
		q.Set(key, strconv.Itoa(v))
	}
}

// Millis converts an Audiobookshelf epoch-milliseconds timestamp to time.Time
// (zero when unset).
func Millis(ms int64) time.Time {
	if ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}
