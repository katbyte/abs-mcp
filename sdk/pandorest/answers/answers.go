// Package answers keeps what a real Audiobookshelf server answers, one file for each operation of the API document, so the document's types can be
// checked against, and filled from, what the server says rather than what its source suggests. The live suites record them when ABS_TEST_ANSWERS
// is set, and the spec command reads them.
//
// A file keeps the first answer whole, and of each later one only the parts that hold a place or kind of value not seen before, so a run against
// the same server writes nothing. Secrets are blanked before anything is written.
package answers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/katbyte/go-kt/httpc"
)

// Env is the setting that has the live suites record: any value turns it on.
const Env = "ABS_TEST_ANSWERS"

// File is one operation's samples: what was sent, and what came back under each status.
type File struct {
	Operation string           `json:"operation"`
	Method    string           `json:"method"`
	Path      string           `json:"path"`
	Sent      []any            `json:"sent,omitempty"`
	Answers   map[string][]any `json:"answers,omitempty"`
}

// Dir is where the answers of an Audiobookshelf version are kept, beside its pandorest definitions.
func Dir(defs, version string) string {
	return filepath.Join(defs, "audiobookshelf-"+version, "answers")
}

// Read is every file in dir, with numbers kept as written so a whole number is told from a fraction. A dir that is not there holds none.
func Read(dir string) ([]*File, error) {
	fsys := os.DirFS(dir)
	entries, err := fs.ReadDir(fsys, ".")
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var files []*File
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}

		b, err := fs.ReadFile(fsys, e.Name())
		if err != nil {
			return nil, err
		}

		var f File
		if err := decode(b, &f); err != nil {
			return nil, fmt.Errorf("%s: %w", filepath.Join(dir, e.Name()), err)
		}
		files = append(files, &f)
	}

	return files, nil
}

// Whole is whether a number is written without a fraction.
func Whole(n json.Number) bool {
	_, err := n.Int64()
	return err == nil
}

// dataKey is a key that is a value rather than a name: an id, a day or a number, as in a map of listening time by day.
var dataKey = regexp.MustCompile(`^([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}|\d{4}-\d{2}-\d{2}|\d+)$`)

// Keyed is whether an object is a map keyed by values, such as ids or days, rather than a record with named fields.
func Keyed(m map[string]any) bool {
	if len(m) == 0 {
		return false
	}
	for k := range m {
		if !dataKey.MatchString(k) {
			return false
		}
	}

	return true
}

// Kind is what a decoded value is, in the document's words, with "integer" for a whole number.
func Kind(v any) string {
	switch v := v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case json.Number:
		if Whole(v) {
			return "integer"
		}
		return "number"
	case string:
		return "string"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	}

	return fmt.Sprintf("%T", v)
}

// places adds every place in v and the kind of value there, such as ".results[].media=object", the entries of a keyed map counting as one place.
func places(v any, at string, out map[string]bool) {
	out[at+"="+Kind(v)] = true

	switch v := v.(type) {
	case map[string]any:
		for k, x := range v {
			if Keyed(v) {
				k = "*"
			}
			places(x, at+"."+k, out)
		}
	case []any:
		for _, x := range v {
			places(x, at+"[]", out)
		}
	}
}

// keepNew is v with only the parts that hold a place or kind of value seen does not, and whether there are any: all of v when seen is empty.
// A record keeps the fields named in kinds with any part of it, as they say which of a union's records it is.
func keepNew(v any, at string, seen map[string]bool, kinds []string) (any, bool) {
	isNew := !seen[at+"="+Kind(v)]

	switch v := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, x := range v {
			sub := at + "." + k
			if Keyed(v) {
				sub = at + ".*"
			}
			if kept, ok := keepNew(x, sub, seen, kinds); ok {
				out[k] = kept
			}
		}
		if len(out) > 0 && !Keyed(v) {
			for _, k := range kinds {
				if x, ok := v[k]; ok {
					out[k] = x
				}
			}
		}
		return out, isNew || len(out) > 0

	case []any:
		out := []any{}
		for _, x := range v {
			if kept, ok := keepNew(x, at+"[]", seen, kinds); ok {
				out = append(out, kept)
			}
		}
		return out, isNew || len(out) > 0
	}

	return v, isNew
}

// trim keeps, of a list's items and a keyed map's entries, the first whole, and of each one after it the parts that hold a place or kind of value
// the ones kept do not, with the fields named in kinds.
func trim(v any, kinds []string) any {
	switch v := v.(type) {
	case []any:
		out, seen := make([]any, 0, len(v)), map[string]bool{}
		for _, x := range v {
			x = trim(x, kinds)
			if kept, ok := keepNew(x, "", seen, kinds); ok {
				out = append(out, kept)
				places(x, "", seen)
			}
		}
		return out

	case map[string]any:
		if !Keyed(v) {
			for k, x := range v {
				v[k] = trim(x, kinds)
			}
			return v
		}

		out, seen := map[string]any{}, map[string]bool{}
		for _, k := range slices.Sorted(maps.Keys(v)) {
			x := trim(v[k], kinds)
			if kept, ok := keepNew(x, "", seen, kinds); ok {
				out[k] = kept
				places(x, "", seen)
			}
		}
		return out
	}

	return v
}

// redacted stands in for a secret, as the client's own trace writes one.
const redacted = "REDACTED"

// blank replaces every text value whose name is a credential's, by the rule the client's trace hides one by, and the stored password hash, pash.
func blank(v any) {
	switch v := v.(type) {
	case map[string]any:
		for k, x := range v {
			if _, ok := x.(string); ok && httpc.SecretName(k, "pash") {
				v[k] = redacted
				continue
			}
			blank(x)
		}
	case []any:
		for _, x := range v {
			blank(x)
		}
	}
}

func decode(b []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()

	return d.Decode(v)
}

// operation is one operation of the API document, its path in parts, each a literal or, written {name}, a parameter.
type operation struct {
	id, method, path string
	parts            []string
}

// Recorder writes what the clients made with its Option send and are answered into the answers of the server's version.
type Recorder struct {
	dir    string
	base   string // the server's own path, which every request path starts with
	ops    []operation
	kinds  []string // the fields that say which of a union's records a record is, kept with any part of one
	mu     sync.Mutex
	files  map[string]*File
	seen   map[string]map[string]bool // by operation, every place and kind of value its samples hold, under "sent" or the status
	missed []string
	err    error
}

// Open is a recorder for the server at address, which reads the API document of the server's version and the answers already kept for it from
// the api-defs folder under root.
func Open(ctx context.Context, address, root string) (*Recorder, error) {
	u, err := url.Parse(address)
	if err != nil {
		return nil, err
	}

	version, err := serverVersion(ctx, address)
	if err != nil {
		return nil, err
	}

	defs := filepath.Join(root, "api-defs")
	ops, kinds, err := readDocument(os.DirFS(defs), "audiobookshelf-openapi-"+version+".json")
	if err != nil {
		return nil, fmt.Errorf("the API document for Audiobookshelf %s: %w", version, err)
	}

	r := &Recorder{dir: Dir(defs, version), base: strings.TrimRight(u.Path, "/"), ops: ops, kinds: kinds, files: map[string]*File{}, seen: map[string]map[string]bool{}}

	files, err := Read(r.dir)
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		r.files[f.Operation] = f
		seen := map[string]bool{}
		for _, v := range f.Sent {
			places(v, "sent", seen)
		}
		for code, list := range f.Answers {
			for _, v := range list {
				places(v, code, seen)
			}
		}
		r.seen[f.Operation] = seen
	}

	return r, nil
}

// serverVersion is the version the server says it is, which needs no key.
func serverVersion(ctx context.Context, address string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(address, "/")+"/status", http.NoBody)
	if err != nil {
		return "", err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	var status struct {
		ServerVersion string `json:"serverVersion"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return "", fmt.Errorf("reading the server's version from /status: %w", err)
	}
	if status.ServerVersion == "" {
		return "", errors.New("the server's /status gives no serverVersion")
	}

	return status.ServerVersion, nil
}

// readDocument is every operation of the API document named in fsys, and the fields its unions tell their records apart by.
func readDocument(fsys fs.FS, name string) (ops []operation, kinds []string, err error) {
	b, err := fs.ReadFile(fsys, name)
	if err != nil {
		return nil, nil, err
	}

	var doc struct {
		Paths map[string]map[string]struct {
			OperationID string `json:"operationId"`
		} `json:"paths"`
		Components struct {
			Schemas map[string]struct {
				Discriminator *struct {
					PropertyName string `json:"propertyName"`
				} `json:"discriminator"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, nil, err
	}

	for _, p := range slices.Sorted(maps.Keys(doc.Paths)) {
		for _, verb := range slices.Sorted(maps.Keys(doc.Paths[p])) {
			ops = append(ops, operation{id: doc.Paths[p][verb].OperationID, method: strings.ToUpper(verb), path: p, parts: strings.Split(strings.Trim(p, "/"), "/")})
		}
	}
	for _, s := range doc.Components.Schemas {
		if s.Discriminator != nil && !slices.Contains(kinds, s.Discriminator.PropertyName) {
			kinds = append(kinds, s.Discriminator.PropertyName)
		}
	}
	slices.Sort(kinds)

	return ops, kinds, nil
}

// Option has a client's exchanges recorded, and does nothing when r is nil, so a suite hands it to every client whether it records or not.
func (r *Recorder) Option() func(*httpc.Options) {
	return func(o *httpc.Options) {
		if r == nil {
			return
		}

		next := o.Base
		if next == nil {
			next = httpc.NewBaseTransport(*o)
		}
		o.Base = &transport{rec: r, next: next}
	}
}

// Transport has the exchanges of a client that takes a transport rather than an option recorded, as the generated client does through
// client.WithTransport, and is nil when r is nil.
func (r *Recorder) Transport() http.RoundTripper {
	if r == nil {
		return nil
	}

	var o httpc.Options
	r.Option()(&o)

	return o.Base
}

// Missed is every request no operation of the document matched, which is a route the document lacks.
func (r *Recorder) Missed() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	return slices.Clone(r.missed)
}

// Report writes the requests no operation of the document matched, which are routes it lacks, and is the first answer that could not be kept,
// which fails the run. A nil recorder reports nothing.
func (r *Recorder) Report(w io.Writer) error {
	if r == nil {
		return nil
	}

	if missed := r.Missed(); len(missed) > 0 {
		_, _ = fmt.Fprintf(w, "\nanswers: %d request(s) match no operation of the API document, which lacks them:\n", len(missed))
		for _, m := range missed {
			_, _ = fmt.Fprintln(w, "  "+m)
		}
	}

	return r.Err()
}

// Err is the first answer that could not be read or written down, which fails the run.
func (r *Recorder) Err() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.err
}

func (r *Recorder) fail(err error) {
	if r.err == nil {
		r.err = err
	}
}

// match is the operation a request is, the one with the most literal parts when parameters let more than one.
func (r *Recorder) match(method, p string) *operation {
	parts := strings.Split(strings.Trim(strings.TrimPrefix(p, r.base), "/"), "/")

	var best *operation
	bestScore := -1
	for i := range r.ops {
		op := &r.ops[i]
		if op.method != method || len(op.parts) != len(parts) {
			continue
		}

		score := 0
		for j, part := range op.parts {
			switch {
			case strings.HasPrefix(part, "{"):
			case part == parts[j]:
				score++
			default:
				score = -1
			}
			if score < 0 {
				break
			}
		}
		if score > bestScore {
			best, bestScore = op, score
		}
	}

	return best
}

// record keeps what one exchange adds to its operation's file and writes the file when it does.
func (r *Recorder) record(method, p string, status int, sent, answer []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()

	op := r.match(method, p)
	if op == nil {
		if !slices.Contains(r.missed, method+" "+p) {
			r.missed = append(r.missed, method+" "+p)
		}
		return
	}

	f := r.files[op.id]
	if f == nil {
		f = &File{Operation: op.id, Method: op.method, Path: op.path}
		r.files[op.id] = f
	}
	seen := r.seen[op.id]
	if seen == nil {
		seen = map[string]bool{}
		r.seen[op.id] = seen
	}

	changed := false
	if v, ok := r.sample(op, "sent", sent, seen); ok {
		f.Sent = append(f.Sent, v)
		changed = true
	}

	code := strconv.Itoa(status)
	if v, ok := r.sample(op, code, answer, seen); ok {
		if f.Answers == nil {
			f.Answers = map[string][]any{}
		}
		f.Answers[code] = append(f.Answers[code], v)
		changed = true
	}

	if changed {
		r.write(f)
	}
}

// sample is a body as it is kept, secrets blanked, lists trimmed and only what the file's samples do not already hold, and whether there is any.
func (r *Recorder) sample(op *operation, under string, body []byte, seen map[string]bool) (any, bool) {
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, false
	}

	var v any
	if err := decode(body, &v); err != nil {
		what := "the answer"
		if under == "sent" {
			what = "what was sent"
		}
		r.fail(fmt.Errorf("%s %s: %s does not read as JSON: %w", op.method, op.path, what, err))
		return nil, false
	}
	blank(v)
	v = trim(v, r.kinds)

	kept, ok := keepNew(v, under, seen, r.kinds)
	if ok {
		places(v, under, seen)
	}

	return kept, ok
}

func (r *Recorder) write(f *File) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(f); err != nil {
		r.fail(err)
		return
	}

	if err := os.MkdirAll(r.dir, 0o750); err != nil {
		r.fail(err)
		return
	}
	if err := os.WriteFile(filepath.Join(r.dir, f.Operation+".json"), buf.Bytes(), 0o600); err != nil {
		r.fail(err)
	}
}

// transport records each exchange that answers JSON with success, and hands the request on unchanged.
type transport struct {
	rec  *Recorder
	next http.RoundTripper
}

func (t *transport) RoundTrip(req *http.Request) (*http.Response, error) {
	var sent []byte
	if req.GetBody != nil && isJSON(req.Header.Get("Content-Type")) {
		body, err := req.GetBody()
		if err == nil {
			sent, err = io.ReadAll(body)
			_ = body.Close()
		}
		if err != nil {
			t.rec.mu.Lock()
			t.rec.fail(fmt.Errorf("%s %s: reading what was sent: %w", req.Method, req.URL.EscapedPath(), err))
			t.rec.mu.Unlock()
		}
	}

	resp, err := t.next.RoundTrip(req)
	if err != nil || resp.StatusCode < 200 || resp.StatusCode > 299 {
		return resp, err
	}
	// what was sent is kept whatever the answer, which is left unread when it is not JSON: text, nothing, or a file
	if !isJSON(resp.Header.Get("Content-Type")) {
		t.rec.record(req.Method, req.URL.EscapedPath(), resp.StatusCode, sent, nil)
		return resp, nil
	}

	answer, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		return nil, err
	}
	resp.Body = io.NopCloser(bytes.NewReader(answer))

	t.rec.record(req.Method, req.URL.EscapedPath(), resp.StatusCode, sent, answer)

	return resp, nil
}

func isJSON(contentType string) bool {
	return strings.HasPrefix(strings.TrimSpace(contentType), "application/json")
}
