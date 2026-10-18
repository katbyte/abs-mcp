package answers

import (
	"bytes"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/katbyte/go-kt/httpc"
)

// fakeDocument is an API document with an operation whose path a more general one also matches.
const fakeDocument = `{"paths": {
  "/api/libraries": {"post": {"operationId": "LibraryCreate"}},
  "/api/libraries/{id}/items": {"get": {"operationId": "LibraryGetLibraryItems"}},
  "/api/libraries/{id}/{part}": {"get": {"operationId": "LibraryGetPart"}},
  "/api/me": {"get": {"operationId": "MeGetMe"}},
  "/api/me/stats": {"get": {"operationId": "MeGetStats"}},
  "/api/shelves": {"get": {"operationId": "LibraryGetShelves"}}
}, "components": {"schemas": {"Shelf": {"oneOf": [], "discriminator": {"propertyName": "type"}}}}}`

// fakeServer answers as Audiobookshelf would, with what each test asks of it in answers, by path: JSON, or text for an answer that is not.
func fakeServer(t *testing.T, answers map[string]string) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/status" {
			_, _ = io.WriteString(w, `{"app": "audiobookshelf", "serverVersion": "9.9.9"}`)
			return
		}
		body, ok := answers[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if strings.HasPrefix(body, "{") || strings.HasPrefix(body, "[") {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
		}
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)

	return srv
}

// fakeRoot is a checkout holding the API document of version 9.9.9.
func fakeRoot(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "api-defs"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "api-defs", "audiobookshelf-openapi-9.9.9.json"), []byte(fakeDocument), 0o600); err != nil {
		t.Fatal(err)
	}

	return root
}

// recording is a client whose exchanges r records.
func recording(r *Recorder) *http.Client {
	return &http.Client{Transport: r.Transport()}
}

func call(t *testing.T, c *http.Client, method, url, body string) string {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}

	return string(b)
}

// object is v as a JSON object, failing the test when it is not one.
func object(t *testing.T, v any) map[string]any {
	t.Helper()

	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("%v is %T, want an object", v, v)
	}

	return m
}

// list is v as a JSON list, failing the test when it is not one.
func list(t *testing.T, v any) []any {
	t.Helper()

	l, ok := v.([]any)
	if !ok {
		t.Fatalf("%v is %T, want a list", v, v)
	}

	return l
}

func readFile(t *testing.T, root, op string) *File {
	t.Helper()

	files, err := Read(Dir(filepath.Join(root, "api-defs"), "9.9.9"))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if f.Operation == op {
			return f
		}
	}
	t.Fatalf("no answers kept for %s", op)

	return nil
}

func TestRecorder(t *testing.T) {
	t.Parallel()

	items := `{"results": [{"id": "a", "title": "One"}, {"id": "b", "title": "Two", "series": "S"}, {"id": "c", "title": "Three"}], "total": 3}`
	srv := fakeServer(t, map[string]string{
		"/api/libraries/l1/items": items,
		"/api/libraries/l1/stats": `{"count": 1}`,
		"/api/libraries":          `{"id": "l2"}`,
		"/api/me":                 `{"id": "u1", "token": "a-real-token", "pash": "a-real-hash", "accessToken": "another"}`,
		"/api/nowhere":            `{}`,
	})
	root := fakeRoot(t)

	rec, err := Open(t.Context(), srv.URL, root)
	if err != nil {
		t.Fatal(err)
	}
	c := recording(rec)

	if got := call(t, c, http.MethodGet, srv.URL+"/api/libraries/l1/items", ""); got != items {
		t.Errorf("the caller was answered %s, want the answer as sent", got)
	}
	call(t, c, http.MethodGet, srv.URL+"/api/libraries/l1/stats", "")
	call(t, c, http.MethodPost, srv.URL+"/api/libraries", `{"name": "Books", "password": "a-real-password"}`)
	call(t, c, http.MethodGet, srv.URL+"/api/me", "")
	call(t, c, http.MethodGet, srv.URL+"/api/nowhere", "")
	if err := rec.Err(); err != nil {
		t.Fatal(err)
	}

	t.Run("a list keeps one item of each shape", func(t *testing.T) {
		t.Parallel()

		f := readFile(t, root, "LibraryGetLibraryItems")
		if f.Method != http.MethodGet || f.Path != "/api/libraries/{id}/items" || len(f.Answers["200"]) != 1 {
			t.Fatalf("file: %+v", f)
		}
		results := list(t, object(t, f.Answers["200"][0])["results"])
		if len(results) != 2 {
			t.Fatalf("results kept: %v, want the first and the one with a series", results)
		}
		if first := object(t, results[0]); first["id"] != "a" || first["title"] != "One" {
			t.Errorf("first item kept as %v, want it whole", first)
		}
		if second := object(t, results[1]); len(second) != 1 || second["series"] != "S" {
			t.Errorf("second item kept as %v, want only the series it adds", second)
		}
	})

	t.Run("the operation with the most literal parts is the one", func(t *testing.T) {
		t.Parallel()

		if f := readFile(t, root, "LibraryGetPart"); f.Path != "/api/libraries/{id}/{part}" {
			t.Errorf("stats kept as %+v", f)
		}
	})

	t.Run("secrets are blanked in what is sent and what comes back", func(t *testing.T) {
		t.Parallel()

		me := object(t, readFile(t, root, "MeGetMe").Answers["200"][0])
		for _, k := range []string{"token", "pash", "accessToken"} {
			if me[k] != redacted {
				t.Errorf("%s kept as %v", k, me[k])
			}
		}
		if me["id"] != "u1" {
			t.Errorf("id kept as %v, want it as sent", me["id"])
		}

		sent := object(t, readFile(t, root, "LibraryCreate").Sent[0])
		if sent["password"] != redacted || sent["name"] != "Books" {
			t.Errorf("sent kept as %v", sent)
		}
	})

	t.Run("a request the document has no operation for is reported", func(t *testing.T) {
		t.Parallel()

		if got := rec.Missed(); !slices.Equal(got, []string{"GET /api/nowhere"}) {
			t.Errorf("missed: %q", got)
		}
	})
}

func TestRecorderKeepsOnlyWhatIsNew(t *testing.T) {
	t.Parallel()

	answers := map[string]string{"/api/me/stats": `{"days": {"2026-10-14": 10}, "total": 10}`}
	srv := fakeServer(t, answers)
	root := fakeRoot(t)
	kept := os.DirFS(Dir(filepath.Join(root, "api-defs"), "9.9.9"))

	rec, err := Open(t.Context(), srv.URL, root)
	if err != nil {
		t.Fatal(err)
	}
	call(t, recording(rec), http.MethodGet, srv.URL+"/api/me/stats", "")
	first, err := fs.ReadFile(kept, "MeGetStats.json")
	if err != nil {
		t.Fatal(err)
	}

	// another run, another day: the same shape, so nothing is written, by a recorder that reads what the last one kept
	answers["/api/me/stats"] = `{"days": {"2026-10-15": 20, "2026-10-16": 30}, "total": 50}`
	again, err := Open(t.Context(), srv.URL, root)
	if err != nil {
		t.Fatal(err)
	}
	call(t, recording(again), http.MethodGet, srv.URL+"/api/me/stats", "")
	second, err := fs.ReadFile(kept, "MeGetStats.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(second, first) {
		t.Errorf("rewritten with the same shape:\n%s", second)
	}

	// a fraction is a kind of value the samples do not hold, so it is kept
	answers["/api/me/stats"] = `{"days": {"2026-10-17": 1.5}, "total": 1.5}`
	call(t, recording(again), http.MethodGet, srv.URL+"/api/me/stats", "")
	f := readFile(t, root, "MeGetStats")
	if len(f.Answers["200"]) != 2 {
		t.Fatalf("samples: %v, want the whole and the fractional", f.Answers["200"])
	}
	if total, ok := object(t, f.Answers["200"][1])["total"].(json.Number); !ok || Whole(total) {
		t.Errorf("total read back as %v, want the fraction as written", total)
	}
	if err := again.Err(); err != nil {
		t.Error(err)
	}
}

func TestRecorderFailsOnABrokenAnswer(t *testing.T) {
	t.Parallel()

	srv := fakeServer(t, map[string]string{"/api/me": `{"id": `})
	rec, err := Open(t.Context(), srv.URL, fakeRoot(t))
	if err != nil {
		t.Fatal(err)
	}

	call(t, recording(rec), http.MethodGet, srv.URL+"/api/me", "")
	if err := rec.Err(); err == nil || !strings.Contains(err.Error(), "GET /api/me: the answer does not read as JSON") {
		t.Errorf("err = %v, want the answer that did not read", err)
	}
}

func TestOpenRefusesAVersionWithNoDocument(t *testing.T) {
	t.Parallel()

	srv := fakeServer(t, nil)
	if _, err := Open(t.Context(), srv.URL, t.TempDir()); err == nil || !strings.Contains(err.Error(), "the API document for Audiobookshelf 9.9.9") {
		t.Errorf("err = %v, want the document that is missing", err)
	}
}

func TestNoRecorderChangesNoClient(t *testing.T) {
	t.Parallel()

	var r *Recorder
	var o httpc.Options
	r.Option()(&o)
	if o.Base != nil {
		t.Errorf("Base = %v, want it left as it was", o.Base)
	}
	if rt := r.Transport(); rt != nil {
		t.Errorf("Transport = %v, want none", rt)
	}
}

func TestKeyed(t *testing.T) {
	t.Parallel()

	cases := map[string]bool{
		`{"2026-10-14": 1}`:                           true,
		`{"8c2a6b0e-1f3d-4c5a-9b7e-0123456789ab": 1}`: true,
		`{"12": 1, "13": 2}`:                          true,
		`{"Monday": 1}`:                               false,
		`{"2026-10-14": 1, "total": 2}`:               false,
		`{}`:                                          false,
	}
	for in, want := range cases {
		var m map[string]any
		if err := json.Unmarshal([]byte(in), &m); err != nil {
			t.Fatal(err)
		}
		if got := Keyed(m); got != want {
			t.Errorf("Keyed(%s) = %v, want %v", in, got, want)
		}
	}
}

func TestKind(t *testing.T) {
	t.Parallel()

	cases := map[string]string{`null`: "null", `true`: "boolean", `1`: "integer", `1.5`: "number", `"a"`: "string", `[]`: "array", `{}`: "object"}
	for in, want := range cases {
		var v any
		if err := decode([]byte(in), &v); err != nil {
			t.Fatal(err)
		}
		if got := Kind(v); got != want {
			t.Errorf("Kind(%s) = %s, want %s", in, got, want)
		}
	}
}

func TestReadOfNoFolderIsNone(t *testing.T) {
	t.Parallel()

	files, err := Read(filepath.Join(t.TempDir(), "not-there"))
	if err != nil || files != nil {
		t.Errorf("Read = %v, %v, want none and no error", files, err)
	}
}

// A record keeps the field that says which of a union's records it is with any part of it kept, within one answer and across them, so the
// part is put with its kind when the document is filled.
func TestRecorderKeepsAKind(t *testing.T) {
	t.Parallel()

	answers := map[string]string{"/api/shelves": `[{"type": "book", "id": "a", "entities": [{"id": "1"}]}, {"type": "series", "id": "b", "entities": [{"id": "2", "books": []}]}]`}
	srv := fakeServer(t, answers)
	root := fakeRoot(t)

	rec, err := Open(t.Context(), srv.URL, root)
	if err != nil {
		t.Fatal(err)
	}
	call(t, recording(rec), http.MethodGet, srv.URL+"/api/shelves", "")
	answers["/api/shelves"] = `[{"type": "authors", "id": "c", "entities": [{"id": "3", "numBooks": 2}]}]`
	call(t, recording(rec), http.MethodGet, srv.URL+"/api/shelves", "")
	if err := rec.Err(); err != nil {
		t.Fatal(err)
	}

	kept := readFile(t, root, "LibraryGetShelves").Answers["200"]
	if len(kept) != 2 {
		t.Fatalf("answers kept: %v, want both", kept)
	}
	series := object(t, list(t, kept[0])[1])
	if series["type"] != "series" || series["id"] != nil {
		t.Errorf("the second shelf of an answer kept as %v, want its new part with its type, and not what the first showed", series)
	}
	authors := object(t, list(t, kept[1])[0])
	if authors["type"] != "authors" || authors["id"] != nil {
		t.Errorf("a shelf of a later answer kept as %v, want its new part with its type", authors)
	}
}

// What was sent is kept when the answer is not JSON, and the answer is left as it came.
func TestRecorderKeepsWhatWasSentToATextAnswer(t *testing.T) {
	t.Parallel()

	srv := fakeServer(t, map[string]string{"/api/libraries": "OK"})
	root := fakeRoot(t)

	rec, err := Open(t.Context(), srv.URL, root)
	if err != nil {
		t.Fatal(err)
	}
	if got := call(t, recording(rec), http.MethodPost, srv.URL+"/api/libraries", `{"name": "Books"}`); got != "OK" {
		t.Errorf("the caller was answered %q, want the text as sent", got)
	}
	if err := rec.Err(); err != nil {
		t.Fatal(err)
	}

	f := readFile(t, root, "LibraryCreate")
	if len(f.Sent) != 1 || object(t, f.Sent[0])["name"] != "Books" || len(f.Answers) != 0 {
		t.Errorf("file: %+v, want what was sent and no answer", f)
	}
}
