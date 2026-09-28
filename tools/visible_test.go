package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

// An account kept from some books sees a search's authors, narrators, tags
// and genres counted over the books it can see: the server counts the whole
// library, so a count of two told an account of one book that another was
// hidden from it.
func TestRestrictedSearchCountsOnlyWhatTheKeySees(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/me", `{"id":"u1","username":"kid","type":"user","permissions":{"accessAllLibraries":true,"accessAllTags":true,"accessExplicitContent":false}}`)
	visible := item(bookB1, "Shared One", `"authorName":"Shared Author","narratorName":"Shared Narrator","genres":["Shared Genre"]`, `"tags":["shared-tag"],"duration":600`)
	f.json("GET /api/libraries/"+libID+"/items", page(visible))
	f.json("GET /api/libraries/"+libID+"/authors", `{"results":[{"id":"a1","name":"Shared Author"}],"total":1}`)
	f.json("GET /api/libraries/"+libID+"/series", `{"results":[],"total":0}`)
	f.json("GET /api/libraries/"+libID+"/search", `{"book":[{"libraryItem":`+visible+`}],"authors":[{"id":"a1","name":"Shared Author","numBooks":2}],`+
		`"narrators":[{"name":"Shared Narrator","numBooks":2}],"tags":[{"name":"shared-tag","numItems":2}],"genres":[{"name":"Shared Genre","numItems":2}]}`)
	call := toolCaller(t, f)

	out, err := call("library_search", map[string]any{"query": "Shared"})
	if err != nil {
		t.Fatal(err)
	}
	for _, group := range []string{"authors", "narrators", "tags", "genres"} {
		rows := list(t, out[group])
		if len(rows) != 1 || num(t, rows[0]["count"]) != 1 {
			t.Errorf("%s = %v, want the one it can see, counted once", group, out[group])
		}
	}
}

// An account kept from some books by tag is shown only what it can see: the
// server's filter data, stats, narrator list and search name groups cover the
// whole library, so they are not read for it.
func TestRestrictedKeySeesOnlyItsBooks(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/me", `{"id":"u1","username":"kid","type":"user","permissions":{"accessAllLibraries":true,"accessAllTags":false,"accessExplicitContent":true},"itemTagsSelected":["kids"]}`)
	visible := item(bookB1, "Visible Book", `"authorName":"Visible Author","narratorName":"Visible Narrator","genres":["Picture Books"],"publishedYear":"2011"`, `"tags":["kids"],"duration":600,"numAudioFiles":1`)
	f.json("GET /api/libraries/"+libID+"/items", page(visible))
	f.json("GET /api/libraries/"+libID+"/authors", `{"results":[{"id":"a1","name":"Visible Author"}],"total":1}`)
	f.json("GET /api/libraries/"+libID+"/series", `{"results":[],"total":0}`)
	hidden := `"Hidden Author"`
	f.json("GET /api/libraries/"+libID+"/filterdata", `{"authors":[{"id":"a2","name":`+hidden+`}],"narrators":["Hidden Narrator"]}`)
	f.json("GET /api/libraries/"+libID+"/narrators", `{"narrators":[{"name":"Hidden Narrator","numBooks":4}]}`)
	f.json("GET /api/libraries/"+libID+"/stats", `{"totalItems":9,"longestItems":[{"id":"x","title":"Hidden Book"}]}`)
	f.json("GET /api/libraries/"+libID+"/search", `{"book":[{"libraryItem":`+visible+`}],"authors":[{"id":"a1","name":"Visible Author"},{"id":"a2","name":`+hidden+`}],"narrators":[{"name":"Visible Narrator"},{"name":"Hidden Narrator"}]}`)
	call := toolCaller(t, f)

	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"library_filters", nil},
		{"narrator_list", nil},
		{"library_get", nil},
		{"library_search", map[string]any{"query": "e"}},
	} {
		out, err := call(tc.tool, tc.args)
		if err != nil {
			t.Fatalf("%s: %v", tc.tool, err)
		}
		raw, _ := json.Marshal(out)
		if strings.Contains(string(raw), "Hidden") {
			t.Errorf("%s names a book the key cannot see: %s", tc.tool, raw)
		}
		if !strings.Contains(string(raw), "Visible") {
			t.Errorf("%s lost what the key can see: %s", tc.tool, raw)
		}
	}
	for _, path := range []string{"/api/libraries/" + libID + "/filterdata", "/api/libraries/" + libID + "/narrators", "/api/libraries/" + libID + "/stats"} {
		if got := f.requests(path); len(got) != 0 {
			t.Errorf("%s was read for a restricted key", path)
		}
	}
}
