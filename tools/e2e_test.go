package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/katbyte/abs-mcp/lib/abs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakeABS serves the handful of endpoints the end-to-end test exercises with
// the server's real response shapes.
func fakeABS(t *testing.T) *httptest.Server {
	t.Helper()

	const item = `{"id":"f0e9d8c7-b6a5-4321-8765-0123456789ab","libraryId":"lib1","relPath":"Frank Herbert/Dune","mediaType":"book","addedAt":1700000000000,"size":524288000,
		"media":{"id":"b1","metadata":{"title":"Dune","authorName":"Frank Herbert","narratorName":"Scott Brick","seriesName":"Dune #1","publishedYear":"1965","asin":"B0"},
		"coverPath":"/c.jpg","tags":["sf"],"numTracks":3,"numChapters":40,"duration":75000}}`

	mux := http.NewServeMux()
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"app":"audiobookshelf","serverVersion":"2.30.0","isInit":true,"language":"en-us"}`))
	})
	mux.HandleFunc("GET /api/me", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":"u1","username":"kt","type":"admin","permissions":{"update":true,"delete":true},"mediaProgress":[],"bookmarks":[]}`))
	})
	mux.HandleFunc("GET /api/libraries", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"libraries":[{"id":"lib1","name":"Books","mediaType":"book","provider":"audible","folders":[{"id":"f1","fullPath":"/audiobooks"}]}]}`))
	})
	mux.HandleFunc("GET /api/search/providers", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"book":["audible","google"],"podcast":["itunes"]}`))
	})
	mux.HandleFunc("GET /api/libraries/lib1/search", func(w http.ResponseWriter, r *http.Request) {
		if !strings.EqualFold(r.URL.Query().Get("q"), "dune") {
			_, _ = w.Write([]byte(`{"book":[],"narrators":[],"tags":[],"genres":[],"series":[],"authors":[]}`))
			return
		}
		_, _ = w.Write([]byte(`{"book":[{"libraryItem":` + item + `,"matchKey":"title","matchText":"Dune"}],"narrators":[{"name":"Scott Brick","numBooks":3}],"tags":[],"genres":[],"series":[],"authors":[{"id":"a1","name":"Frank Herbert","numBooks":6}]}`))
	})
	mux.HandleFunc("GET /api/libraries/lib1/items", func(w http.ResponseWriter, r *http.Request) {
		// the audit's native filter must arrive encoded
		if f := r.URL.Query().Get("filter"); !strings.HasPrefix(f, "missing.") {
			t.Errorf("unexpected filter %q", f)
		}
		_, _ = w.Write([]byte(`{"results":[` + item + `],"total":1,"limit":100,"page":0}`))
	})
	mux.HandleFunc("GET /api/items/f0e9d8c7-b6a5-4321-8765-0123456789ab", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("expanded") != "1" {
			t.Error("item fetched without expanded=1")
		}
		_, _ = w.Write([]byte(item))
	})
	mux.HandleFunc("GET /api/me/progress/f0e9d8c7-b6a5-4321-8765-0123456789ab", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":"p1","libraryItemId":"f0e9d8c7-b6a5-4321-8765-0123456789ab","progress":0.25,"currentTime":18750,"isFinished":false,"lastUpdate":1700000000000}`))
	})
	mux.HandleFunc("PATCH /api/me/progress/f0e9d8c7-b6a5-4321-8765-0123456789ab", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("progress body: %v", err)
		}
		if finished, isBool := body["isFinished"].(bool); !isBool || !finished {
			t.Errorf("progress body = %v", body)
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request %s %s", r.Method, r.URL)
		http.NotFound(w, r)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// call invokes a tool through a real MCP client/server pair over in-memory
// transports and returns the structured result.
func call(ctx context.Context, t *testing.T, session *mcp.ClientSession, name string, args map[string]any) map[string]any {
	t.Helper()

	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if res.IsError {
		var msgs []string
		for _, c := range res.Content {
			if tc, ok := c.(*mcp.TextContent); ok {
				msgs = append(msgs, tc.Text)
			}
		}
		t.Fatalf("%s returned error: %s", name, strings.Join(msgs, "; "))
	}
	out, ok := res.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("%s: structured content is %T", name, res.StructuredContent)
	}
	return out
}

func TestEndToEnd(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	srv := fakeABS(t)
	client, err := abs.New(srv.URL, "key")
	if err != nil {
		t.Fatal(err)
	}

	server := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	if _, err := RegisterAll(server, client, Options{}); err != nil {
		t.Fatal(err)
	}
	st, ct := mcp.NewInMemoryTransports()
	if _, err := server.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()

	info := call(ctx, t, session, "server_info", nil)
	if canDelete, isBool := info["can_delete"].(bool); !isBool || !canDelete || info["version"] != "2.30.0" || info["user"] != "kt" {
		t.Errorf("server_info = %v", info)
	}

	search := call(ctx, t, session, "library_search", map[string]any{"query": "dune"})
	items, ok := search["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("library_search items = %v", search["items"])
	}
	first, ok := items[0].(map[string]any)
	if !ok {
		t.Fatalf("item = %T", items[0])
	}
	if first["title"] != "Dune" || first["narrator"] != "Scott Brick" || first["duration"] != "20h 50m" {
		t.Errorf("summary = %v", first)
	}
	if authors, ok := search["authors"].([]any); !ok || len(authors) != 1 {
		t.Errorf("authors = %v", search["authors"])
	}

	// name resolution: the title alone finds the item, and the native filter
	// path of the audit is used for "cover"
	got := call(ctx, t, session, "item_get", map[string]any{"item": "Dune"})
	if got["id"] != "f0e9d8c7-b6a5-4321-8765-0123456789ab" || got["asin"] != "B0" {
		t.Errorf("item_get = %v", got)
	}

	audit := call(ctx, t, session, "library_audit", map[string]any{"check": "cover"})
	if audit["total_findings"] != float64(1) {
		t.Errorf("library_audit = %v", audit)
	}

	prog := call(ctx, t, session, "me_progress_get", map[string]any{"item": "Dune"})
	p, isMap := prog["progress"].(map[string]any)
	if !isMap {
		t.Fatalf("progress = %T", prog["progress"])
	}
	if p["percent"] != float64(25) || p["current_time"] != "5h 12m" {
		t.Errorf("me_progress_get = %v", prog)
	}

	set := call(ctx, t, session, "me_progress_set", map[string]any{"item": "f0e9d8c7-b6a5-4321-8765-0123456789ab", "finished": true})
	if set["item"] != "Dune" {
		t.Errorf("me_progress_set = %v", set)
	}

	// a bad audit check is a tool error, not a transport error
	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "library_audit", Arguments: map[string]any{"check": "nope"}})
	if err != nil || !res.IsError {
		t.Errorf("bad check: err=%v isError=%v", err, res != nil && res.IsError)
	}
}
