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

	"github.com/katbyte/go-kt/chttp"
)

const (
	maxResponseBytes = 64 << 20
	errBodyPreview   = 300
	// requestWait is how long a call has, and how long the server has to
	// start answering one: some calls are slow to start, a match that asks
	// a store first among them
	requestWait = 120 * time.Second
	// redirectAdvice is what to do about a server url that redirects
	redirectAdvice = "set the server url to the address Audiobookshelf itself answers on (--server / ABS_SERVER)"
)

type Client struct {
	baseURL string
	token   string
	// http carries the API's calls, each read whole. It is go-kt's client:
	// a read is sent again when a gateway could not reach the server or a
	// connection dropped, a write is sent once, and every exchange is
	// traced for the logger the client was made with, if any.
	http *chttp.Client
	// files carries downloads and uploads, which run as long as the file
	// takes: a limit on the whole request covers reading the body too, and
	// would cut an audiobook off part way. Only the wait for the server to
	// start answering is bounded; the caller's context bounds the rest.
	files *chttp.Client
}

// Option changes how a client is made: it is handed the options of the HTTP
// client underneath (go-kt's chttp), before either is built.
type Option func(*chttp.Options)

// WithLog has the client say what it is doing to a logger: each request and
// answer at trace, with the API key and anything else secret blanked, and a
// retry at debug. With none, which is the default, it logs nothing. clog.Log
// is one, and so is any logrus logger.
func WithLog(log chttp.Logger) Option {
	return func(o *chttp.Options) { o.Log = log }
}

// WithRetry sets when a request is sent again, in place of the default: a
// read three times in all, a second and then two apart, for a 502, 503 or
// 504 and for a dropped connection.
func WithRetry(retry chttp.Retry) Option {
	return func(o *chttp.Options) { o.Retry = retry }
}

// New returns a client for the Audiobookshelf server at baseURL that
// authenticates with an API key (Settings -> Users -> API Keys) or a user
// token, sent as a bearer token.
func New(baseURL, token string, opts ...Option) (*Client, error) {
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

	o := chttp.Options{Name: "Audiobookshelf", HeaderWait: requestWait}
	for _, opt := range opts {
		opt(&o)
	}

	// Audiobookshelf's API answers where it is asked, so a redirect means
	// the server url points at something in front of it: an http address a
	// proxy moves to https, or a login page. Following one is worse than
	// failing: Go turns a DELETE, PATCH or POST into a GET on a 301, 302 or
	// 303, the GET answers 200, and the write reports success having done
	// nothing
	calls, files := chttp.New(o), chttp.New(o)
	calls.Timeout = requestWait
	calls.CheckRedirect, files.CheckRedirect = chttp.RefuseRedirects(redirectAdvice), chttp.RefuseRedirects(redirectAdvice)

	return &Client{baseURL: strings.TrimRight(baseURL, "/"), token: token, http: calls, files: files}, nil
}

// As is the same client acting as another account: the same server, reached
// the same way and logged to the same place, with that account's key.
func (c *Client) As(token string) (*Client, error) {
	if token == "" {
		return nil, errors.New("API key is required to act as another account")
	}
	as := *c
	as.token = token

	return &as, nil
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

// BaseURL is the server address the client was created with, without a
// trailing slash.
func (c *Client) BaseURL() string { return c.baseURL }

// statusError is the server answering a call with a status that is not
// success: go-kt's StatusError, with what is known of that status on this
// API. Callers read the status with chttp.StatusCode, or IsNotFound and
// IsForbidden here.
func statusError(method, path string, resp *http.Response, body string) *chttp.StatusError {
	e := &chttp.StatusError{Method: method, Path: path, StatusCode: resp.StatusCode, Body: body, Tries: chttp.Tries(resp)}
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		e.Note = "API key rejected; check ABS_TOKEN"
	case http.StatusForbidden:
		e.Note = "the API key's user lacks permission for this: most write operations need an admin account, and an account limited to some libraries or tags cannot open the rest"
	}

	return e
}

// IsNotFound reports whether err is an HTTP 404 from the server.
func IsNotFound(err error) bool {
	return chttp.IsNotFound(err)
}

// IsForbidden reports whether err is the server refusing the key: a 401 or a
// 403, as an admin-only route answers any other key.
func IsForbidden(err error) bool {
	code := chttp.StatusCode(err)

	return code == http.StatusForbidden || code == http.StatusUnauthorized
}

// errorBody is what a failed request's body says, for its error, or why it
// could not be read: a body cut off is part of what went wrong.
func errorBody(r io.Reader) string {
	body, err := io.ReadAll(io.LimitReader(r, errBodyPreview+1))

	return unread(chttp.Preview(body), err)
}

// unread adds, to what was read of a failed request's body, why the rest
// could not be.
func unread(text string, err error) string {
	if err != nil {
		return strings.TrimSpace(text + " (reading the rest of the reply failed: " + err.Error() + ")")
	}

	return text
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

	// the whole answer, asked for again if it stops part way; with no
	// response there was no answer at all, and the error says why
	resp, raw, err := c.http.Fetch(req, maxResponseBytes) //nolint:bodyclose // Fetch hands the body back read and closed
	if resp == nil {
		return nil, err
	}
	tooLarge := errors.Is(err, chttp.ErrTooLarge)

	// a refusal keeps its status whether or not its body can be read: a 404
	// read as a network error would not be "none"
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		if tooLarge {
			err = nil
		}

		return nil, statusError(method, path, resp, unread(chttp.Preview(raw), err))
	}
	if tooLarge {
		return nil, fmt.Errorf("%s %s: the reply is over %d MiB, more than abs-mcp reads: ask for less at a time", method, path, maxResponseBytes>>20)
	}
	if err != nil {
		return nil, fmt.Errorf("%s %s: reading the reply: %w", method, path, err)
	}
	// the API answers JSON or a word of text; a web page is something in
	// front of it (a login wall, a proxy's own page) answering 200 for a
	// request that never arrived. The server's own text replies are labelled
	// text/html too (Express's default for a string), so the body decides
	if chttp.IsWebPage(resp.Header.Get("Content-Type"), raw) {
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
		body := errorBody(resp.Body)
		_ = resp.Body.Close()
		return nil, statusError(http.MethodGet, path, resp, body)
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
		return statusError(http.MethodPost, path, resp, errorBody(resp.Body))
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
