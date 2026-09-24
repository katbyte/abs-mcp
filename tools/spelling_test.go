package tools

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
)

// A remove drops a value from every item that carries it, server-wide for a
// tag or genre, with nothing to put back: removing zz-provider:audible.ca
// would have erased the store record everywhere in one call. Without confirm
// it says how many items carry the value and which, and sends no change.
func TestMetadataRenameRemoveNeedsConfirm(t *testing.T) {
	t.Parallel()

	const podLib = "55555555-5555-4555-8555-555555555503"
	b64 := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	f := newFakeABS(t)
	f.json("GET /api/libraries", `{"libraries":[{"id":"`+libID+`","name":"Books","mediaType":"book"},{"id":"`+podLib+`","name":"Pods","mediaType":"podcast"}]}`)
	f.mux.HandleFunc("GET /api/libraries/"+libID+"/items", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("filter") {
		case "tags." + b64("zz-provider:audible.ca"):
			_, _ = io.WriteString(w, `{"results":[`+item("i1", "Dune", "", "")+`,`+item("i2", "Emma", "", "")+`],"total":2}`)
		case "narrators." + b64("Jim Dale"):
			_, _ = io.WriteString(w, `{"results":[`+item("i3", "Harry Potter", "", "")+`],"total":1}`)
		case "":
			_, _ = io.WriteString(w, page(item("i4", "Hobbit", `"language":"eng"`, ""), item("i5", "Ubik", `"language":"English"`, "")))
		default:
			_, _ = io.WriteString(w, page())
		}
	})
	f.mux.HandleFunc("GET /api/libraries/"+podLib+"/items", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("filter") == "tags."+b64("zz-provider:audible.ca") {
			_, _ = io.WriteString(w, `{"results":[`+item("p1", "A Podcast", "", "")+`],"total":1}`)
			return
		}
		_, _ = io.WriteString(w, page())
	})
	f.json("DELETE /api/tags/{id}", `{"numItemsUpdated":3}`)
	f.json("DELETE /api/libraries/{lib}/narrators/{id}", `{"updated":1}`)
	f.json("POST /api/items/batch/update", `{"updates":1}`)
	call := toolCaller(t, f)

	out, err := call("metadata_rename", map[string]any{"field": "tags", "from": "zz-provider:audible.ca", "remove": true})
	if err != nil {
		t.Fatal(err)
	}
	preview, ok := out["preview"].(map[string]any)
	if !ok || num(t, preview["found"]) != 3 || !slices.Equal(anyStrings(preview["items"]), []string{"Dune", "Emma", "A Podcast"}) || num(t, out["items_updated"]) != 0 {
		t.Errorf("tag preview = %v, want 3 items in both libraries named, nothing updated", out)
	}

	out, err = call("metadata_rename", map[string]any{"field": "narrators", "from": "Jim Dale", "remove": true})
	if err != nil {
		t.Fatal(err)
	}
	if preview, ok := out["preview"].(map[string]any); !ok || num(t, preview["found"]) != 1 {
		t.Errorf("narrator preview = %v", out)
	}
	// a podcast library has no narrators and answers an unknown filter with
	// everything: it is not asked
	for _, r := range f.requests("/api/libraries/" + podLib + "/items") {
		if strings.Contains(parseQuery(t, r.Query).Get("filter"), "narrators") {
			t.Errorf("the podcast library was asked for narrators: %s", r.Query)
		}
	}

	out, err = call("metadata_rename", map[string]any{"field": "languages", "from": "eng", "remove": true, "library": "Books"})
	if err != nil {
		t.Fatal(err)
	}
	if preview, ok := out["preview"].(map[string]any); !ok || num(t, preview["found"]) != 1 || !slices.Equal(anyStrings(preview["items"]), []string{"Hobbit"}) {
		t.Errorf("language preview = %v, want the one book spelled eng", out)
	}

	for _, r := range f.seen {
		if r.Method != http.MethodGet {
			t.Fatalf("a preview changed something: %s %s", r.Method, r.Path)
		}
	}

	// confirm does it
	out, err = call("metadata_rename", map[string]any{"field": "tags", "from": "zz-provider:audible.ca", "remove": true, "confirm": true})
	if err != nil {
		t.Fatal(err)
	}
	if num(t, out["items_updated"]) != 3 || out["preview"] != nil {
		t.Errorf("confirmed remove = %v", out)
	}
	if got := f.requests("/api/tags/" + b64("zz-provider:audible.ca")); len(got) != 1 || got[0].Method != http.MethodDelete {
		t.Errorf("confirm sent %v, want one DELETE", got)
	}
}

// A sweep writes in batches, and one that failed part way threw away how many
// had already changed: the caller read the whole change as not made.
func TestMetadataRenameSaysHowFarItGotBeforeAFailure(t *testing.T) {
	t.Parallel()

	const libB = "55555555-5555-4555-8555-555555555504"
	f := newFakeABS(t)
	f.json("GET /api/libraries", `{"libraries":[{"id":"`+libID+`","name":"A","mediaType":"book"},{"id":"`+libB+`","name":"B","mediaType":"book"}]}`)
	books := make([]string, 0, 150)
	for i := range 150 {
		books = append(books, item(fmt.Sprintf("i%03d", i), fmt.Sprintf("Book %03d", i), `"publisher":"Bantom","genres":["SF & Fantasy"]`, ""))
	}
	f.json("GET /api/libraries/"+libID+"/items", page(books...))
	f.json("GET /api/libraries/"+libB+"/items", page())
	var mu sync.Mutex
	calls := 0
	f.mux.HandleFunc("POST /api/items/batch/update", func(w http.ResponseWriter, r *http.Request) {
		var body []json.RawMessage
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls%2 == 0 { // every second batch times out
			http.Error(w, "gateway timeout", http.StatusGatewayTimeout)
			return
		}
		_, _ = fmt.Fprintf(w, `{"updates":%d}`, len(body))
	})
	f.json("PATCH /api/libraries/"+libID+"/narrators/{id}", `{"updated":3}`)
	f.mux.HandleFunc("PATCH /api/libraries/"+libB+"/narrators/{id}", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	call := toolCaller(t, f)

	for _, tc := range []struct {
		args map[string]any
		want string
	}{
		// publishers go in batches of 100, genre splits in 50s
		{map[string]any{"field": "publishers", "from": "Bantom", "to": "Bantam", "library": "A"}, "100 items were changed"},
		{map[string]any{"field": "genres", "from": "SF & Fantasy", "into": []any{"Science Fiction", "Fantasy"}}, "50 items were changed"},
		// the first library renamed, the second failed
		{map[string]any{"field": "narrators", "from": "jim dale", "to": "Jim Dale"}, "3 items were changed"},
	} {
		mu.Lock()
		calls = 0
		mu.Unlock()
		_, err := call("metadata_rename", tc.args)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: %v, want %q", tc.args, err, tc.want)
		}
	}
}

// For authors items_updated preferred the count on the server's reply, which
// after a merge is the surviving author's books, both authors' together; the
// books that changed are the ones the renamed author had.
func TestMetadataRenameAuthorCountsTheBooksThatChanged(t *testing.T) {
	t.Parallel()

	const authorID = "44444444-4444-4444-8444-444444444445"
	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/libraries/"+libID+"/authors", `{"results":[{"id":"`+authorID+`","name":"jrr tolkien","libraryId":"`+libID+`"}],"total":1}`)
	f.json("GET /api/authors/"+authorID, `{"id":"`+authorID+`","name":"jrr tolkien","libraryId":"`+libID+`","libraryItems":[`+item("i1", "The Hobbit", "", "")+`,`+item("i2", "Smith of Wootton Major", "", "")+`]}`)
	f.json("PATCH /api/authors/"+authorID, `{"author":{"id":"a-keep","name":"J.R.R. Tolkien","numBooks":9},"merged":true}`)
	call := toolCaller(t, f)

	out, err := call("metadata_rename", map[string]any{"field": "authors", "from": "jrr tolkien", "to": "J.R.R. Tolkien"})
	if err != nil {
		t.Fatal(err)
	}
	if !boolOf(t, out["merged"]) || num(t, out["items_updated"]) != 2 {
		t.Errorf("out = %v, want merged with the 2 books that changed", out)
	}
}
