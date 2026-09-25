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
	"mime/multipart"
	"net/http"
	"net/url"
	"reflect"
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
	// files carries downloads and uploads, which run as long as the file
	// takes: http's two-minute limit covers reading the body too, and would
	// cut an audiobook off part way. Only the wait for the server to start
	// answering is bounded; the caller's context bounds the rest.
	files *http.Client
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

	transport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, errors.New("the default HTTP transport is not an *http.Transport")
	}
	files := transport.Clone()
	files.ResponseHeaderTimeout = 120 * time.Second

	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    &http.Client{Timeout: 120 * time.Second, CheckRedirect: refuseRedirect},
		files:   &http.Client{Transport: files, CheckRedirect: refuseRedirect},
	}, nil
}

// isNil reports whether v is nil or holds a nil map, slice or pointer.
func isNil(v any) bool {
	if v == nil {
		return true
	}
	switch rv := reflect.ValueOf(v); rv.Kind() {
	case reflect.Map, reflect.Slice, reflect.Pointer, reflect.Interface:
		return rv.IsNil()
	default:
		return false
	}
}

// isWebPage reports whether an answer is an HTML document rather than the
// API's JSON or its bare "OK".
func isWebPage(contentType string, body []byte) bool {
	if !strings.HasPrefix(strings.ToLower(contentType), "text/html") {
		return false
	}
	head := strings.ToLower(strings.TrimSpace(string(body[:min(len(body), 512)])))

	return strings.HasPrefix(head, "<!doctype html") || strings.HasPrefix(head, "<html")
}

// refuseRedirect stops the client following a redirect. Audiobookshelf's API
// answers where it is asked, so a redirect means the server url points at
// something in front of it: an http address a proxy moves to https, or a
// login page. Following one is worse than failing. Go turns a DELETE, PATCH
// or POST into a GET on a 301, 302 or 303, the GET answers 200, and the write
// reports success having done nothing; and it keeps the key on a redirect
// from https to http on the same host.
func refuseRedirect(req *http.Request, via []*http.Request) error {
	return fmt.Errorf("%s %s was redirected to %s: set the server url to the address Audiobookshelf itself answers on (--server / ABS_SERVER)",
		via[0].Method, via[0].URL.Path, req.URL.Redacted())
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
		msg += " (the API key's user lacks permission for this: most write operations need an admin account, and an account limited to some libraries or tags cannot open the rest)"
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

	if out == nil {
		return nil
	}
	// the API answers what it was asked for; nothing at all is something in
	// front of it, and decoding nothing would hand back a blank record that
	// reads as a real one with every field empty
	if len(bytes.TrimSpace(raw)) == 0 {
		return fmt.Errorf("%s %s: answered with nothing where the server sends a record: check the server url (--server / ABS_SERVER)", method, path)
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

	// a nil map or pointer is no body: marshalled it is the JSON null, which
	// the server's parser refuses, so a session closed with no final
	// position was never closed
	if isNil(body) {
		body = nil
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
	// the API answers JSON or a word of text; a web page is something in
	// front of it (a login wall, a proxy's own page) answering 200 for a
	// request that never arrived. The server's own text replies are labelled
	// text/html too (Express's default for a string), so the body decides
	if isWebPage(resp.Header.Get("Content-Type"), raw) {
		return nil, fmt.Errorf("%s %s: answered with a web page, not the Audiobookshelf API: check the server url (--server / ABS_SERVER)", method, path)
	}

	return raw, nil
}

// stream performs a request and hands back the response body unread, for the
// endpoints that return a file rather than JSON. The caller must close it.
// doRaw buffers into memory with a cap, which is wrong for an audiobook.
func (c *Client) stream(ctx context.Context, path string, query url.Values) (io.ReadCloser, error) {
	resp, err := c.open(ctx, path, query, "")
	if err != nil {
		return nil, err
	}

	return resp.Body, nil
}

// open is stream with the whole response handed back, and a byte range
// asked for when rng is set, for a reader that needs the status and headers
// of a ranged answer. The caller closes the body.
func (c *Client) open(ctx context.Context, path string, query url.Values, rng string) (*http.Response, error) {
	u := c.baseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, http.NoBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("User-Agent", "abs-mcp")
	if rng != "" {
		req.Header.Set("Range", rng)
	}

	resp, err := c.files.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, errBodyPreview))
		_ = resp.Body.Close()
		return nil, &HTTPError{Method: http.MethodGet, Path: path, Status: resp.StatusCode, Body: truncate(strings.TrimSpace(string(body)), errBodyPreview)}
	}

	return resp, nil
}

// uploadMultipart posts a multipart form with one file part, for the two endpoints that
// take a file rather than JSON. The form is written as it is sent, so an
// audiobook is never held in memory whole.
func (c *Client) uploadMultipart(ctx context.Context, path, field, filename string, content io.Reader, fields map[string]string) error {
	pr, pw := io.Pipe()
	w := multipart.NewWriter(pw)
	go func() {
		pw.CloseWithError(writeForm(w, field, filename, content, fields))
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, pr)
	if err != nil {
		_ = pr.CloseWithError(err)
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("User-Agent", "abs-mcp")

	resp, err := c.files.Do(req)
	if err != nil {
		_ = pr.CloseWithError(err)
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, errBodyPreview))
		return &HTTPError{Method: http.MethodPost, Path: path, Status: resp.StatusCode, Body: truncate(strings.TrimSpace(string(body)), errBodyPreview)}
	}

	return nil
}

// writeForm writes the fields and then the file into a multipart form.
func writeForm(w *multipart.Writer, field, filename string, content io.Reader, fields map[string]string) error {
	for k, v := range fields {
		if err := w.WriteField(k, v); err != nil {
			return err
		}
	}
	part, err := w.CreateFormFile(field, filename)
	if err != nil {
		return err
	}
	if _, err := io.Copy(part, content); err != nil {
		return err
	}

	return w.Close()
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
