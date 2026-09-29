package tools

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/katbyte/abs-mcp/lib/abs"
)

// deleteRoutes is a book in its own folder with two of the key user's
// bookmarks on it and one on another book.
func deleteRoutes(f *fakeABS, isFile bool) {
	path := "/audiobooks/Frank Herbert/Dune"
	if isFile {
		path = "/audiobooks/Dune.m4b"
	}
	f.json("GET /api/items/"+itemID, fmt.Sprintf(`{"id":%q,"libraryId":%q,"mediaType":"book","path":%q,"isFile":%t,"media":{"metadata":{"title":"Dune"}}}`, itemID, libID, path, isFile))
	f.json("GET /api/me", `{"id":"u1","username":"kt","type":"root","permissions":{"delete":true},"bookmarks":[`+
		`{"libraryItemId":"`+itemID+`","title":"Arrakis","time":5},`+
		`{"libraryItemId":"`+itemID+`","title":"Spice","time":7},`+
		`{"libraryItemId":"`+bookB1+`","title":"Other","time":1}]}`)
}

// Without confirm, item_delete changes nothing and says what a confirmed
// call would remove: the record, the folder or single file with
// delete_files, and the key user's bookmarks on it.
func TestItemDeleteSaysWhatItWouldRemove(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		isFile      bool
		deleteFiles bool
		files       string
	}{
		{false, true, "the folder /audiobooks/Frank Herbert/Dune and everything in it"},
		{true, true, "the file /audiobooks/Dune.m4b"},
		{false, false, ""},
	} {
		f := newFakeABS(t)
		deleteRoutes(f, tc.isFile)
		call := toolCaller(t, f)

		out, err := call("item_delete", map[string]any{"item": itemID, "delete_files": tc.deleteFiles})
		if err != nil {
			t.Fatal(err)
		}
		if got := f.changes(); len(got) != 0 {
			t.Errorf("a delete without confirm sent %v", got)
		}
		if str(t, out["would_delete"]) != "Dune" || out["deleted"] != nil || boolOf(t, out["files_removed"]) {
			t.Errorf("answer = %v, want Dune named as what would go, and nothing gone", out)
		}
		if got := str(t, out["files"]); got != tc.files {
			t.Errorf("files = %q, want %q", got, tc.files)
		}
		marks, ok := out["bookmarks"].([]any)
		if !ok {
			t.Fatalf("bookmarks = %v", out["bookmarks"])
		}
		if len(marks) != 2 || !strings.Contains(fmt.Sprint(marks), "Arrakis") || strings.Contains(fmt.Sprint(marks), "Other") {
			t.Errorf("bookmarks = %v, want the two on this book", marks)
		}
	}
}

// A removal that fails part way says how many bookmarks were already gone,
// and a delete that fails after them says they went.
func TestItemDeleteSaysWhatWentBeforeAFailure(t *testing.T) {
	t.Parallel()

	t.Run("the second bookmark", func(t *testing.T) {
		t.Parallel()

		f := newFakeABS(t)
		deleteRoutes(f, false)
		f.json("DELETE /api/me/item/"+itemID+"/bookmark/5", `OK`)
		f.mux.HandleFunc("DELETE /api/me/item/"+itemID+"/bookmark/7", func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "busy", http.StatusBadGateway)
		})
		call := toolCaller(t, f)

		_, err := call("item_delete", map[string]any{"item": itemID, "confirm": true})
		if err == nil || !strings.Contains(err.Error(), "1 of its 2 bookmarks were removed") || strings.Contains(err.Error(), "nothing was deleted") {
			t.Errorf("error = %v, want the one bookmark already removed named", err)
		}
		if got := f.requests("/api/items/" + itemID); len(got) != 1 {
			t.Errorf("item requests = %v, want the read only", got)
		}
	})

	t.Run("the item", func(t *testing.T) {
		t.Parallel()

		f := newFakeABS(t)
		deleteRoutes(f, false)
		f.json("DELETE /api/me/item/"+itemID+"/bookmark/5", `OK`)
		f.json("DELETE /api/me/item/"+itemID+"/bookmark/7", `OK`)
		f.mux.HandleFunc("DELETE /api/items/"+itemID, func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "busy", http.StatusBadGateway)
		})
		call := toolCaller(t, f)

		_, err := call("item_delete", map[string]any{"item": itemID, "confirm": true})
		if err == nil || !strings.Contains(err.Error(), "2 of its bookmarks were removed, but deleting") {
			t.Errorf("error = %v, want the removed bookmarks named", err)
		}
	})
}

// item_delete reads the account's bookmark list and removes from it, which
// user_bookmark_edit also does whole: it waits for the list to be free.
func TestItemDeleteHoldsTheBookmarks(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	deleteRoutes(f, false)
	f.json("DELETE /api/me/item/"+itemID+"/bookmark/5", `OK`)
	f.json("DELETE /api/me/item/"+itemID+"/bookmark/7", `OK`)
	f.json("DELETE /api/items/"+itemID, `OK`)
	r, call := registryCaller(t, f)

	release := r.locks.hold("bookmarks")
	done := make(chan error, 1)
	go func() {
		_, err := call("item_delete", map[string]any{"item": itemID, "confirm": true})
		done <- err
	}()
	time.Sleep(200 * time.Millisecond)
	if got := f.changes(); len(got) != 0 {
		t.Errorf("sent %v while the bookmarks were held", got)
	}
	release()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := f.changes(); len(got) != 3 {
		t.Errorf("sent %v, want both bookmarks then the item", got)
	}
}

// The chapter lookup asks the store the book was matched from, which the
// provider tag records: its asin may be sold in that region only.
func TestItemChaptersSetAsksTheBooksStore(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /api/items/"+itemID, item(itemID, "Dune", `"asin":"B0CA"`, `"tags":["Fiction","zz-provider:audible.ca"],"duration":3600`))
	f.json("GET /api/search/chapters", `{"chapters":[{"startOffsetMs":0,"lengthMs":3600000,"title":"One"}]}`)
	f.json("POST /api/items/"+itemID+"/chapters", `{"success":true,"updated":true}`)
	call := toolCaller(t, f)

	out, err := call("item_chapters_set", map[string]any{"item": itemID})
	if err != nil {
		t.Fatal(err)
	}
	got := f.requests("/api/search/chapters")
	if len(got) != 1 || !strings.Contains(got[0].Query, "region=ca") || str(t, out["region"]) != "ca" {
		t.Errorf("lookup = %v, region %v; want the Canadian store", got, out["region"])
	}

	if _, err := call("item_chapters_set", map[string]any{"item": itemID, "region": "uk"}); err != nil {
		t.Fatal(err)
	}
	if got := f.requests("/api/search/chapters"); !strings.Contains(got[len(got)-1].Query, "region=uk") {
		t.Errorf("an asked-for region was not used: %v", got[len(got)-1])
	}
}

// An explicit list is checked before it is sent: out of order, before the
// start or past the end of the audio is a chapter nobody can reach. A list
// with an asin to fetch as well would drop the asin unseen.
func TestItemChaptersSetChecksTheList(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /api/items/"+itemID, item(itemID, "Dune", "", `"duration":3600`))
	f.json("POST /api/items/"+itemID+"/chapters", `{"success":true,"updated":true}`)
	call := toolCaller(t, f)

	ch := func(start float64) map[string]any { return map[string]any{"title": "C", "start_s": start} }
	for _, tc := range []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"chapters": []any{ch(0), ch(600), ch(300)}}, "not after chapter 2"},
		{map[string]any{"chapters": []any{ch(0), ch(0)}}, "not after chapter 1"},
		{map[string]any{"chapters": []any{ch(0), ch(3600)}}, "past the end of the audio"},
		{map[string]any{"chapters": []any{ch(-1), ch(10)}}, "before the audio does"},
		{map[string]any{"chapters": []any{ch(0)}, "from_asin": "B0CA"}, "one or the other"},
	} {
		tc.args["item"] = itemID
		if _, err := call("item_chapters_set", tc.args); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: %v, want %q", tc.args, err, tc.want)
		}
	}
	if got := f.requests("/api/items/" + itemID + "/chapters"); len(got) != 0 {
		t.Errorf("a refused list was sent: %v", got)
	}

	if _, err := call("item_chapters_set", map[string]any{"item": itemID, "chapters": []any{ch(0), ch(1800)}}); err != nil {
		t.Errorf("a good list was refused: %v", err)
	}
}

// item_batch_edit sends a hundred at a time, as the sweeps do, and a failure
// part way says how many had already gone.
func TestItemBatchEditSendsInPages(t *testing.T) {
	t.Parallel()

	ids := make([]string, 0, sweepBatchSize+5)
	for i := range sweepBatchSize + 5 {
		ids = append(ids, fmt.Sprintf("22222222-2222-4222-8222-%012d", i))
	}
	books := func(f *fakeABS) {
		for _, id := range ids {
			f.json("GET /api/items/"+id, item(id, "Book "+id, "", ""))
		}
	}
	sizes := func(f *fakeABS) []int {
		reqs := f.requests("/api/items/batch/update")
		out := make([]int, 0, len(reqs))
		for _, req := range reqs {
			var entries []json.RawMessage
			if err := json.Unmarshal([]byte(req.Body), &entries); err != nil {
				t.Fatal(err)
			}
			out = append(out, len(entries))
		}
		return out
	}

	f := newFakeABS(t)
	books(f)
	f.mux.HandleFunc("POST /api/items/batch/update", func(w http.ResponseWriter, r *http.Request) {
		var entries []json.RawMessage
		_ = json.NewDecoder(r.Body).Decode(&entries)
		_, _ = fmt.Fprintf(w, `{"updates":%d}`, len(entries))
	})
	call := toolCaller(t, f)
	out, err := call("item_batch_edit", map[string]any{"items": ids, "genres": []any{"Fiction"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := sizes(f); len(got) != 2 || got[0] != sweepBatchSize || got[1] != 5 || num(t, out["items_updated"]) != sweepBatchSize+5 {
		t.Errorf("batches = %v, updated %v; want %d then 5, all updated", got, out["items_updated"], sweepBatchSize)
	}

	f = newFakeABS(t)
	books(f)
	var sent atomic.Int32
	f.mux.HandleFunc("POST /api/items/batch/update", func(w http.ResponseWriter, _ *http.Request) {
		if sent.Add(1) > 1 {
			http.Error(w, "gateway timeout", http.StatusGatewayTimeout)
			return
		}
		_, _ = fmt.Fprintf(w, `{"updates":%d}`, sweepBatchSize)
	})
	call = toolCaller(t, f)
	_, err = call("item_batch_edit", map[string]any{"items": ids, "genres": []any{"Fiction"}})
	if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("items %d to %d of %d failed", sweepBatchSize+1, sweepBatchSize+5, sweepBatchSize+5)) ||
		!strings.Contains(err.Error(), fmt.Sprintf("%d items before it were updated", sweepBatchSize)) {
		t.Errorf("error = %v, want the batch that failed and the ones before it named", err)
	}
}

// A field given and cleared, or a cover set and removed in one call, did the
// one and dropped the other unseen: both are refused before anything is sent.
func TestContradictoryEditsAreRefused(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /api/items/"+itemID, item(itemID, "Dune", "", ""))
	call := toolCaller(t, f)

	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"item_edit", map[string]any{"narrators": []any{"Scott Brick"}, "clear": []any{"narrators"}}},
		{"item_edit", map[string]any{"description": "A desert planet.", "clear": []any{"Description"}}},
		{"item_edit", map[string]any{"tags": []any{"sf"}, "clear": []any{"tags"}}},
		{"item_cover_edit", map[string]any{"url": "https://example.test/c.jpg", "remove": true}},
		{"item_cover_edit", map[string]any{"url": "https://example.test/c.jpg", "file": "/audiobooks/Dune/cover.jpg"}},
	} {
		tc.args["item"] = itemID
		if _, err := call(tc.tool, tc.args); err == nil {
			t.Errorf("%s %v was not refused", tc.tool, tc.args)
		}
	}
	if got := f.changes(); len(got) != 0 {
		t.Errorf("a refused edit sent %v", got)
	}
}

// A store's chapters are for the recording its asin names, which is not
// always the one on disk: chapters that start past the end of this book's
// audio are another edition's, and are refused rather than written.
func TestItemChaptersSetRefusesAnotherRecordingsChapters(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /api/items/"+itemID, item(itemID, "Dune", `"asin":"B0DUNE"`, `"duration":1`))
	f.json("GET /api/search/chapters", `{"chapters":[{"startOffsetMs":0,"lengthMs":1800000,"title":"One"},{"startOffsetMs":1800000,"lengthMs":1800000,"title":"Two"}]}`)
	f.json("POST /api/items/"+itemID+"/chapters", `{"success":true,"updated":true}`)
	call := toolCaller(t, f)

	_, err := call("item_chapters_set", map[string]any{"item": itemID})
	if err == nil || !strings.Contains(err.Error(), "another recording") {
		t.Errorf("a recording's chapters past the end of the audio: %v", err)
	}
	if got := f.requests("/api/items/" + itemID + "/chapters"); len(got) != 0 {
		t.Errorf("the chapters were written: %v", got)
	}
}

// A cover url the server accepted but could not fetch leaves the book with no
// cover: the answer reads the cover back and says so rather than done.
func TestItemCoverEditReadsTheCoverBack(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /api/items/"+itemID, item(itemID, "Dune", "", ""))
	f.json("POST /api/items/"+itemID+"/cover", `{}`)
	call := toolCaller(t, f)

	_, err := call("item_cover_edit", map[string]any{"item": itemID, "url": "http://img/gone.jpg"})
	if err == nil || !strings.Contains(err.Error(), "has none afterwards") {
		t.Errorf("a cover that never arrived: %v", err)
	}
}

func TestAMatchWhoseLibraryCannotBeReadIsAnError(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /api/items/"+itemID, item(itemID, "Dune", `"authorName":"Frank Herbert"`, ""))
	f.fails("GET /api/libraries/" + libID)
	f.json("GET /api/search/books", `[]`)
	call := toolCaller(t, f)

	_, err := call("item_match", map[string]any{"item": itemID})
	wantErr(t, "item_match with no provider and the library unreadable", err, "library's provider", "500")
	if got := f.requests("/api/search/books"); len(got) != 0 {
		t.Errorf("searched anyway, with no provider: %v", got)
	}

	out, err := call("item_match_apply_batch", map[string]any{"matches": []any{map[string]any{"item": itemID, "asin": "B0X"}}})
	if err != nil {
		t.Fatal(err)
	}
	rows := list(t, out["results"])
	if num(t, out["failed"]) != 1 || len(rows) != 1 || !strings.Contains(str(t, rows[0]["error"]), "library's provider") {
		t.Errorf("batch = %v, want the row failed, saying why", out)
	}
	if got := f.requests("/api/items/" + itemID + "/match"); len(got) != 0 {
		t.Errorf("matched anyway, with no provider: %v", got)
	}
}

func TestAMatchThatCannotBeReadBackIsAnError(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	var matched atomic.Bool
	f.mux.HandleFunc("GET /api/items/"+itemID, func(w http.ResponseWriter, _ *http.Request) {
		if matched.Load() {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		_, _ = io.WriteString(w, item(itemID, "Dune", `"authorName":"Frank Herbert"`, ""))
	})
	f.mux.HandleFunc("POST /api/items/"+itemID+"/match", func(w http.ResponseWriter, _ *http.Request) {
		matched.Store(true)
		_, _ = io.WriteString(w, `{"updated":true,"libraryItem":`+item(itemID, "Dune", `"authorName":"Frank Herbert","asin":"B0"`, "")+`}`)
	})
	f.json("POST /api/items/batch/update", `{"updates":1}`)
	f.json("PATCH /api/items/"+itemID+"/media", `{"updated":true,"libraryItem":`+item(itemID, "Dune", `"asin":"B0"`, "")+`}`)
	call := toolCaller(t, f)

	_, err := call("item_match_apply", map[string]any{"item": itemID, "asin": "B0", "provider": "audible"})
	wantErr(t, "a match whose read-back failed", err, "matched", "reading the item back", "500")
}

// A book the server leaves out of a batch read, deleted meanwhile, is not
// edited from what was known of it before.
func TestABookLeftOutOfABatchReadIsNotEdited(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /api/items/"+bookB1, item(bookB1, "First", "", ""))
	f.json("POST /api/items/batch/get", `{"libraryItems":[]}`)
	f.json("POST /api/items/batch/update", `{"updates":1}`)
	call := toolCaller(t, f)

	_, err := call("item_batch_edit", map[string]any{"items": []any{bookB1}, "add_tags": []any{"x"}})
	wantErr(t, "a book left out of the read before the edit", err, "not in the server's reply", "nothing was changed")
	if got := f.requests("/api/items/batch/update"); len(got) != 0 {
		t.Errorf("edited anyway: %v", got)
	}
}

// clear has to reach the server as an empty list for each field, or the
// server sees nothing to change and the field keeps its value.
func TestItemEditClearSendsEmptyLists(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /api/items/"+itemID, item(itemID, "Dune", `"narratorName":"Scott Brick"`, ""))
	f.json("PATCH /api/items/"+itemID+"/media", `{"updated":true}`)
	call := toolCaller(t, f)

	out, err := call("item_edit", map[string]any{"item": itemID, "clear": []any{"narrators", "series", "genres", "tags"}})
	if err != nil {
		t.Fatal(err)
	}
	if updated, ok := out["updated"].(bool); !ok || !updated {
		t.Errorf("updated = %v", out["updated"])
	}

	sent := f.requests("/api/items/" + itemID + "/media")
	if len(sent) != 1 {
		t.Fatalf("PATCH sent %d times", len(sent))
	}
	var body struct {
		Metadata map[string]json.RawMessage `json:"metadata"`
		Tags     json.RawMessage            `json:"tags"`
	}
	if err := json.Unmarshal([]byte(sent[0].Body), &body); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"narrators", "series", "genres"} {
		if string(body.Metadata[field]) != "[]" {
			t.Errorf("%s sent as %s, want [] (body %s)", field, body.Metadata[field], sent[0].Body)
		}
	}
	if string(body.Tags) != "[]" {
		t.Errorf("tags sent as %s, want []", body.Tags)
	}
	if _, present := body.Metadata["title"]; present {
		t.Errorf("an untouched field was sent: %s", sent[0].Body)
	}
}

// Removing a cover is asked for with remove; a call that forgot its url is an
// error, not a deletion.
func TestItemCoverEditNeedsIntent(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	var covered atomic.Bool
	covered.Store(true)
	f.mux.HandleFunc("GET /api/items/"+itemID, func(w http.ResponseWriter, _ *http.Request) {
		if covered.Load() {
			_, _ = io.WriteString(w, item(itemID, "Dune", "", `"coverPath":"/c.jpg"`))
			return
		}
		_, _ = io.WriteString(w, item(itemID, "Dune", "", ""))
	})
	f.mux.HandleFunc("DELETE /api/items/"+itemID+"/cover", func(w http.ResponseWriter, _ *http.Request) {
		covered.Store(false)
		_, _ = io.WriteString(w, `{}`)
	})
	f.mux.HandleFunc("POST /api/items/"+itemID+"/cover", func(w http.ResponseWriter, _ *http.Request) {
		covered.Store(true)
		_, _ = io.WriteString(w, `{}`)
	})
	call := toolCaller(t, f)

	if _, err := call("item_cover_edit", map[string]any{"item": itemID}); err == nil {
		t.Error("a call with nothing to set was not refused")
	}
	if got := f.requests("/api/items/" + itemID + "/cover"); len(got) != 0 {
		t.Errorf("the cover was touched: %v", got)
	}

	out, err := call("item_cover_edit", map[string]any{"item": itemID, "remove": true})
	if err != nil {
		t.Fatal(err)
	}
	if got := f.requests("/api/items/" + itemID + "/cover"); len(got) != 1 || got[0].Method != http.MethodDelete {
		t.Errorf("remove sent %v, want one DELETE", got)
	}
	if out["cover"] != "" {
		t.Errorf("after the removal cover = %v, want none, read back", out["cover"])
	}

	if out, err = call("item_cover_edit", map[string]any{"item": itemID, "url": "http://img/c.jpg"}); err != nil {
		t.Fatal(err)
	}
	if out["cover"] != "/c.jpg" {
		t.Errorf("after setting cover = %v, want the one read back", out["cover"])
	}
	if got := f.requests("/api/items/" + itemID + "/cover"); len(got) != 2 || got[1].Method != http.MethodPost || !strings.Contains(got[1].Body, "http://img/c.jpg") {
		t.Errorf("url sent %v, want a POST carrying the url", got)
	}
}

// A match with nothing to name the book would take the provider's first hit
// unseen; the omitted argument is an error, not a quick match.
func TestItemMatchApplyNeedsACandidate(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /api/items/"+itemID, item(itemID, "Dune", `"authorName":"Frank Herbert"`, ""))
	f.json("POST /api/items/"+itemID+"/match", `{"updated":true,"libraryItem":`+item(itemID, "Dune", `"authorName":"Frank Herbert","asin":"B9"`, "")+`}`)
	f.json("GET /api/libraries/"+libID, `{"id":"`+libID+`","name":"Books","mediaType":"book","provider":"audible"}`) // a row naming no provider takes the library's
	call := toolCaller(t, f)

	if _, err := call("item_match_apply", map[string]any{"item": itemID, "override_details": true}); err == nil {
		t.Error("a match naming no candidate, asin or isbn was not refused")
	}
	if got := f.requests("/api/items/" + itemID + "/match"); len(got) != 0 {
		t.Errorf("the match was sent anyway: %v", got)
	}

	out, err := call("item_match_apply", map[string]any{"item": itemID, "asin": "B0"})
	if err != nil {
		t.Fatal(err)
	}
	if got := f.requests("/api/items/" + itemID + "/match"); len(got) != 1 || !strings.Contains(got[0].Body, `"asin":"B0"`) {
		t.Errorf("asin sent %v, want one POST carrying it", got)
	}
	// what was applied is echoed, and an id the item did not take is called out
	applied, ok := out["applied"].(map[string]any)
	if !ok || str(t, applied["asin"]) != "B0" {
		t.Errorf("applied = %v, want the asin sent", out["applied"])
	}
	if !strings.Contains(str(t, out["warning"]), "B0") {
		t.Errorf("warning = %q, want one saying the item kept its asin", out["warning"])
	}
}

// item_edit edits the tag list the way item_batch_edit does.
func TestItemEditAddsAndRemovesTags(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /api/items/"+itemID, item(itemID, "Dune", "", `"tags":["sf","Classic"]`))
	f.json("PATCH /api/items/"+itemID+"/media", `{"updated":true}`)
	call := toolCaller(t, f)

	if _, err := call("item_edit", map[string]any{"item": itemID, "add_tags": []any{"desert", "classic"}, "remove_tags": []any{"SF"}}); err != nil {
		t.Fatal(err)
	}
	sent := f.requests("/api/items/" + itemID + "/media")
	if len(sent) != 1 {
		t.Fatalf("media requests = %v", sent)
	}
	var body struct {
		Tags []string `json:"tags"`
	}
	if err := json.Unmarshal([]byte(sent[0].Body), &body); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(body.Tags, []string{"Classic", "desert"}) {
		t.Errorf("tags sent = %v, want [Classic desert]", body.Tags)
	}
	if _, err := call("item_edit", map[string]any{"item": itemID, "tags": []any{"a"}, "add_tags": []any{"b"}}); err == nil {
		t.Error("tags with add_tags was not refused")
	}
}

// The server tags the files in the background and never reads them back, so
// item_embed_metadata waits for its task, rescans, and judges the tags the
// rescan found.
func TestItemEmbedMetadataWaitsAndReadsTheTagsBack(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	var polls, scanned atomic.Int32
	f.mux.HandleFunc("GET /api/items/"+itemID, func(w http.ResponseWriter, _ *http.Request) {
		tags := `{}`
		if scanned.Load() > 0 {
			tags = `{"tagTitle":"Dune"}`
		}
		_, _ = io.WriteString(w, item(itemID, "Dune", "", `"audioFiles":[{"index":1,"metadata":{"filename":"01.mp3"},"metaTags":`+tags+`}]`))
	})
	f.json("POST /api/tools/item/"+itemID+"/embed-metadata", `OK`)
	f.mux.HandleFunc("GET /api/tasks", func(w http.ResponseWriter, _ *http.Request) {
		if polls.Add(1) == 1 {
			_, _ = io.WriteString(w, `{"tasks":[],"queuedTaskData":{"embedMetadata":[{"libraryItemId":"`+itemID+`"}]}}`)
			return
		}
		_, _ = io.WriteString(w, `{"tasks":[]}`)
	})
	f.mux.HandleFunc("POST /api/items/"+itemID+"/scan", func(w http.ResponseWriter, _ *http.Request) {
		if polls.Load() < 2 {
			t.Error("rescanned while the embed was still queued")
		}
		scanned.Add(1)
		_, _ = io.WriteString(w, `{"result":"UPDATED"}`)
	})
	call := toolCaller(t, f)

	out, err := call("item_embed_metadata", map[string]any{"item": itemID})
	if err != nil {
		t.Fatal(err)
	}
	if !boolOf(t, out["embedded"]) || out["rescan"] != "UPDATED" || out["running"] != nil || out["differs"] != nil {
		t.Errorf("item_embed_metadata = %v, want embedded after the rescan", out)
	}
}

// Calls of one turn run at once. Each edit of a book's tags reads the book
// and sends its tags back whole, so without holding the book eight adds kept
// one tag; held, and read again once held, every tag lands.
func TestItemEditsAtOnceKeepEveryTag(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	var mu sync.Mutex
	tags := []string{"sf"}
	f.mux.HandleFunc("GET /api/items/"+itemID, func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		have, _ := json.Marshal(tags)
		mu.Unlock()
		// a read that takes a while, so the calls overlap
		time.Sleep(20 * time.Millisecond)
		_, _ = io.WriteString(w, item(itemID, "Dune", "", `"tags":`+string(have)))
	})
	f.mux.HandleFunc("PATCH /api/items/"+itemID+"/media", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Tags []string `json:"tags"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		tags = body.Tags
		mu.Unlock()
		_, _ = io.WriteString(w, `{"updated":true}`)
	})
	call := toolCaller(t, f)

	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			if _, err := call("item_edit", map[string]any{"item": itemID, "add_tags": []any{strconv.Itoa(i)}}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	if len(tags) != 9 {
		t.Errorf("tags = %v, want sf and all eight added", tags)
	}
}

func TestItemEditAddsAndRemovesSeries(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /api/items/"+itemID, item(itemID, "Words of Radiance", `"series":[{"id":"s","name":"The Stormlight Archive","sequence":"2"},{"id":"c","name":"Cosmere"}]`, ""))
	f.json("PATCH /api/items/"+itemID+"/media", `{"updated":true}`)
	call := toolCaller(t, f)

	sentSeries := func() []abs.SeriesRef {
		t.Helper()
		sent := f.requests("/api/items/" + itemID + "/media")
		var body struct {
			Metadata struct {
				Series []abs.SeriesRef `json:"series"`
			} `json:"metadata"`
		}
		if err := json.Unmarshal([]byte(sent[len(sent)-1].Body), &body); err != nil {
			t.Fatal(err)
		}
		return body.Metadata.Series
	}

	if _, err := call("item_edit", map[string]any{"item": itemID, "add_series": []any{"Stormlight Archive #2"}}); err != nil {
		t.Fatal(err)
	}
	if want := []abs.SeriesRef{{ID: "s", Name: "The Stormlight Archive", Sequence: "2"}, {ID: "c", Name: "Cosmere"}, {Name: "Stormlight Archive", Sequence: "2"}}; !sameRefs(sentSeries(), want) {
		t.Errorf("add_series sent %v, want the two it had and the new one", sentSeries())
	}

	if _, err := call("item_edit", map[string]any{"item": itemID, "add_series": []any{"cosmere #1"}, "remove_series": []any{"The Stormlight Archive"}}); err != nil {
		t.Fatal(err)
	}
	if want := []abs.SeriesRef{{ID: "c", Name: "Cosmere", Sequence: "1"}}; !sameRefs(sentSeries(), want) {
		t.Errorf("add a number to one it is in and remove another: sent %v, want %v", sentSeries(), want)
	}

	for name, args := range map[string]map[string]any{
		"with series":         {"item": itemID, "series": []any{"X"}, "add_series": []any{"Y"}},
		"with clear":          {"item": itemID, "clear": []any{"series"}, "remove_series": []any{"Cosmere"}},
		"remove one not in":   {"item": itemID, "remove_series": []any{"Mistborn"}},
		"add an empty name":   {"item": itemID, "add_series": []any{" #3"}},
		"remove an empty one": {"item": itemID, "remove_series": []any{""}},
	} {
		if _, err := call("item_edit", args); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestItemBatchEditAddsSeries(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
	elantris := item(itemID, "Elantris", `"series":[{"id":"e","name":"Elantris","sequence":"1"}]`, "")
	f.json("GET /api/items/"+itemID, elantris)
	// read again, all at once, once the items are held
	f.json("POST /api/items/batch/get", `{"libraryItems":[`+elantris+`]}`)
	f.json("POST /api/items/batch/update", `{"updates":1}`)
	call := toolCaller(t, f)

	out, err := call("item_batch_edit", map[string]any{"items": []any{itemID}, "add_series": []any{"Cosmere"}})
	if err != nil {
		t.Fatal(err)
	}
	if num(t, out["items_updated"]) != 1 {
		t.Errorf("out = %v", out)
	}
	sent := f.requests("/api/items/batch/update")
	if len(sent) != 1 || !strings.Contains(sent[0].Body, `"name":"Elantris","sequence":"1"`) || !strings.Contains(sent[0].Body, `"name":"Cosmere"`) {
		t.Errorf("batch sent %v, want Elantris kept and Cosmere added", sent)
	}
	if !strings.Contains(sent[0].Body, `"metadata":{`) || strings.Contains(sent[0].Body, `"genres"`) {
		t.Errorf("batch sent %v, want only the series in the metadata", sent[0].Body)
	}
}

func TestEditSeriesList(t *testing.T) {
	t.Parallel()

	have := []abs.SeriesRef{{Name: "Discworld", Sequence: "36"}, {Name: "Discworld: Moist von Lipwig", Sequence: "2"}}
	got, err := editSeriesList(have, []string{"Discworld: Industrial Revolution #5", "discworld #36"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := append(slices.Clone(have), abs.SeriesRef{Name: "Discworld: Industrial Revolution", Sequence: "5"}); !sameRefs(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if len(have) != 2 {
		t.Error("the caller's list was changed")
	}
	got, err = editSeriesList(have, nil, []string{"Discworld: Moist von Lipwig #2", "Discworld"})
	if err != nil || len(got) != 0 || got == nil {
		t.Errorf("removing everything = %v, %v; want an empty list that clears the field", got, err)
	}
	if _, err := editSeriesList(nil, nil, []string{"Discworld"}); err == nil || !strings.Contains(err.Error(), "no series") {
		t.Errorf("removing from nothing: %v", err)
	}
}

func TestItemGetInSecondsAndBytes(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /api/items/"+itemID, `{"id":"`+itemID+`","libraryId":"`+libID+`","mediaType":"book","size":734003200,`+
		`"libraryFiles":[{"metadata":{"filename":"cover.jpg","size":20480},"fileType":"image"}],`+
		`"userMediaProgress":{"id":"p1","libraryItemId":"`+itemID+`","currentTime":1800.6,"progress":0.25},`+
		`"media":{"metadata":{"title":"Dune"},"duration":7200.4,`+
		`"audioFiles":[{"index":1,"duration":7200.4,"metadata":{"filename":"01.m4b","size":734003200}}],`+
		`"chapters":[{"id":0,"start":0,"end":1234.567,"title":"One"},{"id":1,"start":1234.567,"end":7200.4,"title":"Two"}]}}`)
	call := toolCaller(t, f)

	out, err := call("item_get", map[string]any{"item": itemID, "chapters": true, "files": true})
	if err != nil {
		t.Fatal(err)
	}
	wantNumbers(t, "item_get", out, map[string]float64{
		"duration_s":              7200,
		"size":                    734003200,
		"progress.current_time_s": 1801,
		"chapter_list.0.start_s":  0,
		"chapter_list.0.end_s":    1234.567,
		"chapter_list.1.start_s":  1234.567,
		"chapter_list.1.end_s":    7200.4,
		"track_list.0.duration_s": 7200,
		"track_list.0.size":       734003200,
		"other_files.0.size":      20480,
	})
	wantAbsent(t, "item_get", out, "duration", "size_mb", "progress.current_time", "progress.current_seconds", "chapter_list.0.start_seconds", "track_list.0.size_mb")
}

func TestItemMatchInSeconds(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/search/providers", `{"providers":{"books":[{"value":"audible"}],"podcasts":[]}}`)
	f.json("GET /api/items/"+itemID, item(itemID, "Dune", "", `"duration":75600.4`))
	// a provider gives the length in minutes
	f.json("GET /api/search/books", `[{"title":"Dune","author":"Frank Herbert","asin":"B0DUNE","duration":1260}]`)
	call := toolCaller(t, f)

	out, err := call("item_match", map[string]any{"item": itemID, "provider": "audible"})
	if err != nil {
		t.Fatal(err)
	}
	wantNumbers(t, "item_match", out, map[string]float64{"item_duration_s": 75600, "candidates.0.duration_s": 75600})
	wantAbsent(t, "item_match", out, "item_duration", "candidates.0.duration")
}

// threeTracks is Dune in three files, one chapter to each as the scanner cuts
// them, with its audio files in the order given as inos, and two ebooks of
// which main is the one readers open ("" for none).
func threeTracks(order []string, main string) string {
	files := map[string]string{
		"a1": `{"index":%d,"ino":"a1","duration":100,"metadata":{"filename":"01.mp3","relPath":"01.mp3"}}`,
		"a2": `{"index":%d,"ino":"a2","duration":50,"metadata":{"filename":"02.mp3","relPath":"02.mp3"}}`,
		"a3": `{"index":%d,"ino":"a3","duration":30,"metadata":{"filename":"03.mp3","relPath":"03.mp3"}}`,
	}
	audio := make([]string, 0, len(order))
	for i, ino := range order {
		audio = append(audio, fmt.Sprintf(files[ino], i+1))
	}
	ebook := func(ino, name string) string {
		return fmt.Sprintf(`{"ino":%q,"fileType":"ebook","isSupplementary":%t,"metadata":{"filename":%q,"relPath":%q,"ext":".epub"}}`, ino, ino != main, name, name)
	}
	primary := "null"
	switch main {
	case "e1":
		primary = `{"ino":"e1","ebookFormat":"epub","metadata":{"filename":"Dune.epub","relPath":"Dune.epub"}}`
	case "e2":
		primary = `{"ino":"e2","ebookFormat":"pdf","metadata":{"filename":"Dune.pdf","relPath":"Dune.pdf"}}`
	}
	return `{"id":"` + itemID + `","libraryId":"` + libID + `","mediaType":"book","relPath":"Frank Herbert/Dune",` +
		`"libraryFiles":[` + ebook("e1", "Dune.epub") + `,` + ebook("e2", "Dune.pdf") + `],` +
		`"media":{"metadata":{"title":"Dune"},"duration":180,"ebookFile":` + primary + `,"audioFiles":[` + strings.Join(audio, ",") + `],` +
		`"chapters":[{"id":0,"start":0,"end":100,"title":"One"},{"id":1,"start":100,"end":150,"title":"Two"},{"id":2,"start":150,"end":180,"title":"Three"}]}}`
}

// item_edit tracks sends every file in the order named, reads the order
// back, and moves the chapters with their files.
func TestItemEditReordersTracks(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /api/items/"+itemID, threeTracks([]string{"a1", "a2", "a3"}, "e1"))
	f.json("PATCH /api/items/"+itemID+"/tracks", threeTracks([]string{"a3", "a1", "a2"}, "e1"))
	f.json("POST /api/items/"+itemID+"/chapters", `{"success":true,"updated":true}`)
	call := toolCaller(t, f)

	out, err := call("item_edit", map[string]any{"item": itemID, "tracks": []any{"03.mp3", "01.mp3", "02.mp3"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := strs(t, out["tracks"]); !slices.Equal(got, []string{"03.mp3", "01.mp3", "02.mp3"}) {
		t.Errorf("tracks = %v", got)
	}
	if out["chapters"] != "moved" || !isTrue(out["updated"]) {
		t.Errorf("answer = %v", out)
	}

	sent := f.requests("/api/items/" + itemID + "/tracks")
	if len(sent) != 1 || !strings.Contains(sent[0].Body, `[{"ino":"a3","exclude":false},{"ino":"a1","exclude":false},{"ino":"a2","exclude":false}]`) {
		t.Errorf("tracks sent %v", sent)
	}
	chapters := f.requests("/api/items/" + itemID + "/chapters")
	if len(chapters) != 1 {
		t.Fatalf("chapters sent %d times", len(chapters))
	}
	var body struct{ Chapters []abs.Chapter }
	if err := json.Unmarshal([]byte(chapters[0].Body), &body); err != nil {
		t.Fatal(err)
	}
	want := []abs.Chapter{{ID: 0, Start: 0, End: 30, Title: "Three"}, {ID: 1, Start: 30, End: 130, Title: "One"}, {ID: 2, Start: 130, End: 180, Title: "Two"}}
	if !slices.Equal(body.Chapters, want) {
		t.Errorf("chapters sent %v, want %v", body.Chapters, want)
	}
}

// A list that would lose a file, or an order already so, sends nothing; an
// order the server does not keep, or chapters that fail after the tracks
// moved, is an error that says the tracks moved.
func TestItemEditTracksRefusesAndReports(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /api/items/"+itemID, threeTracks([]string{"a1", "a2", "a3"}, "e1"))
	call := toolCaller(t, f)

	_, err := call("item_edit", map[string]any{"item": itemID, "title": "Dune", "tracks": []any{"02.mp3", "01.mp3"}})
	wantErr(t, "a list short of a file", err, "leaves out", `"03.mp3"`)
	out, err := call("item_edit", map[string]any{"item": itemID, "tracks": []any{"01.mp3", "02.mp3", "03.mp3"}})
	if err != nil {
		t.Fatal(err)
	}
	if out["chapters"] != "unchanged" || isTrue(out["updated"]) || len(strs(t, out["fields_sent"])) != 0 {
		t.Errorf("an order already so: %v", out)
	}
	if got := f.changes(); len(got) != 0 {
		t.Errorf("sent %v", got)
	}

	// the server answers with the order it kept, which is not the one sent
	f.json("PATCH /api/items/"+itemID+"/tracks", threeTracks([]string{"a1", "a2", "a3"}, "e1"))
	_, err = call("item_edit", map[string]any{"item": itemID, "tracks": []any{"03.mp3", "01.mp3", "02.mp3"}})
	wantErr(t, "an order not kept", err, "the track order changed", "03.mp3, 01.mp3, 02.mp3", "plays 01.mp3, 02.mp3, 03.mp3")

	f.json("PATCH /api/items/"+itemID+"/tracks", threeTracks([]string{"a3", "a1", "a2"}, "e1"))
	f.json("PATCH /api/items/"+itemID+"/media", `{"updated":true}`)
	f.fails("POST /api/items/" + itemID + "/chapters")
	_, err = call("item_edit", map[string]any{"item": itemID, "title": "Dune", "tracks": []any{"03.mp3", "01.mp3", "02.mp3"}})
	wantErr(t, "chapters that failed", err, "the metadata and the track order changed", "moving the chapters", "item_chapters_set")
}

// item_edit ebook flips only the file that needs it, and reads the main
// ebook back: the server's route flips whatever it is given.
func TestItemEditSetsTheMainEbook(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.inTurn("GET /api/items/"+itemID, threeTracks([]string{"a1", "a2", "a3"}, "e1"), threeTracks([]string{"a1", "a2", "a3"}, "e1"), threeTracks([]string{"a1", "a2", "a3"}, "e2"))
	f.json("PATCH /api/items/"+itemID+"/ebook/e2/status", `{}`)
	call := toolCaller(t, f)

	out, err := call("item_edit", map[string]any{"item": itemID, "ebook": "Dune.pdf"})
	if err != nil {
		t.Fatal(err)
	}
	if out["ebook"] != "Dune.pdf" || !isTrue(out["updated"]) {
		t.Errorf("answer = %v", out)
	}
	if got := f.changes(); len(got) != 1 || got[0].Path != "/api/items/"+itemID+"/ebook/e2/status" {
		t.Errorf("sent %v", got)
	}

	// already the main one: one more flip would leave the book with none
	out, err = call("item_edit", map[string]any{"item": itemID, "ebook": "Dune.pdf"})
	if err != nil {
		t.Fatal(err)
	}
	if out["ebook"] != "Dune.pdf" || isTrue(out["updated"]) || len(f.changes()) != 1 {
		t.Errorf("a second call: %v, sent %v", out, f.changes())
	}
}

// A flip the server did not make is an error, not an answer.
func TestItemEditChecksTheMainEbook(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /api/items/"+itemID, threeTracks([]string{"a1", "a2", "a3"}, "e1"))
	f.json("PATCH /api/items/"+itemID+"/ebook/e1/status", `{}`)
	call := toolCaller(t, f)

	_, err := call("item_edit", map[string]any{"item": itemID, "ebook": "none"})
	wantErr(t, "a flip not made", err, "the main ebook changed", "checking the main ebook", "Dune.epub, not none")
}

// item_get shows a file's path when it is in a folder of its own, and which
// ebook is the main one.
func TestItemGetShowsPathsAndTheMainEbook(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /api/items/"+itemID, `{"id":"`+itemID+`","libraryId":"`+libID+`","mediaType":"book",`+
		`"libraryFiles":[{"ino":"e1","fileType":"ebook","metadata":{"filename":"Dune.epub","relPath":"Dune.epub"}},{"ino":"e2","fileType":"ebook","isSupplementary":true,"metadata":{"filename":"Dune.pdf","relPath":"extra/Dune.pdf"}}],`+
		`"media":{"metadata":{"title":"Dune"},"ebookFile":{"ino":"e1","metadata":{"filename":"Dune.epub"}},`+
		`"audioFiles":[{"index":1,"ino":"a1","metadata":{"filename":"01.mp3","relPath":"CD1/01.mp3"}},{"index":2,"ino":"a2","metadata":{"filename":"02.mp3","relPath":"02.mp3"}}]}}`)
	call := toolCaller(t, f)

	out, err := call("item_get", map[string]any{"item": itemID, "files": true})
	if err != nil {
		t.Fatal(err)
	}
	tracks := list(t, out["track_list"])
	if tracks[0]["path"] != "CD1/01.mp3" || tracks[1]["path"] != nil {
		t.Errorf("tracks = %v: a path only where it says more than the name", tracks)
	}
	other := list(t, out["other_files"])
	if !isTrue(other[0]["main"]) || isTrue(other[1]["main"]) || other[1]["path"] != "extra/Dune.pdf" {
		t.Errorf("other files = %v", other)
	}
}

// trimmable is Dune in its folder with an audio file, a cover, a note and two
// ebooks, the epub the main one.
func trimmable(f *fakeABS) {
	file := func(ino, name, kind string, extra string) string {
		return fmt.Sprintf(`{"ino":%q,"fileType":%q,"metadata":{"filename":%q,"relPath":%q,"path":"/audiobooks/Frank Herbert/Dune/%s"}%s}`, ino, kind, name, name, name, extra)
	}
	f.json("GET /api/items/"+itemID, `{"id":"`+itemID+`","libraryId":"`+libID+`","folderId":"fo1","mediaType":"book","path":"/audiobooks/Frank Herbert/Dune","relPath":"Frank Herbert/Dune",`+
		`"libraryFiles":[`+file("a1", "01.mp3", "audio", "")+`,`+file("i1", "cover.jpg", "image", "")+`,`+file("t1", "notes .txt", "text", "")+`,`+
		file("e1", "Dune.epub", "ebook", "")+`,`+file("e2", "Dune.pdf", "ebook", `,"isSupplementary":true`)+`],`+
		`"media":{"metadata":{"title":"Dune"},"coverPath":"/audiobooks/Frank Herbert/Dune/cover.jpg",`+
		`"ebookFile":{"ino":"e1","metadata":{"filename":"Dune.epub","relPath":"Dune.epub"}},`+
		`"audioFiles":[{"index":1,"ino":"a1","duration":100,"metadata":{"filename":"01.mp3","relPath":"01.mp3"}}]}}`)
	f.json("GET /api/libraries/"+libID, `{"id":"`+libID+`","name":"Books","mediaType":"book","folders":[{"id":"fo0","fullPath":"/other"},{"id":"fo1","fullPath":"/audiobooks"}]}`)
	f.json("GET /api/me", `{"id":"u1","username":"kt","type":"root","permissions":{"delete":true,"upload":true}}`)
}

// item_delete file takes out a file that is not the audio or the cover, says
// first what would go, and looks at the disk afterwards.
func TestItemDeleteFile(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	trimmable(f)
	f.json("DELETE /api/items/"+itemID+"/file/t1", `OK`)
	f.json("POST /api/filesystem/pathexists", `{"exists":true,"libraryItemTitle":"Dune"}`)
	call := toolCaller(t, f)

	out, err := call("item_delete", map[string]any{"item": itemID, "file": "notes .txt"})
	if err != nil {
		t.Fatal(err)
	}
	if out["would_delete"] != `"notes .txt" from "Dune"` || out["files"] != "the file /audiobooks/Frank Herbert/Dune/notes .txt" || len(f.changes()) != 0 {
		t.Errorf("preview = %v, sent %v", out, f.changes())
	}

	out, err = call("item_delete", map[string]any{"item": itemID, "file": "notes .txt", "confirm": true})
	if err != nil {
		t.Fatal(err)
	}
	if out["deleted"] != `"notes .txt" from "Dune"` || !isTrue(out["files_removed"]) {
		t.Errorf("answer = %v", out)
	}
	checked := f.requests("/api/filesystem/pathexists")
	if len(checked) != 1 || !strings.Contains(checked[0].Body, `"folderPath":"/audiobooks"`) || !strings.Contains(checked[0].Body, `"directory":"Frank Herbert/Dune/notes .txt"`) {
		t.Errorf("disk checked with %v", checked)
	}

	// still on disk: the server took it off the book but could not remove it
	f.json("POST /api/filesystem/pathexists", `{"exists":true}`)
	_, err = call("item_delete", map[string]any{"item": itemID, "file": "notes .txt", "confirm": true})
	wantErr(t, "a file left on disk", err, "taken out of the book but is still on disk", "the next scan puts it back")
}

// The audio, the cover, a podcast's files and a whole-folder delete beside a
// file are refused before anything is sent.
func TestItemDeleteFileRefuses(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	trimmable(f)
	call := toolCaller(t, f)

	for _, c := range []struct {
		args map[string]any
		says []string
	}{
		{map[string]any{"file": "01.mp3"}, []string{"audio file", "length", "item_rescan"}},
		{map[string]any{"file": "cover.jpg"}, []string{"cover", "item_cover_edit"}},
		{map[string]any{"file": "Dune.mobi"}, []string{`"Dune.mobi"`, `"notes .txt"`}},
		{map[string]any{"file": "notes .txt", "delete_files": true}, []string{"One or the other"}},
	} {
		c.args["item"], c.args["confirm"] = itemID, true
		_, err := call("item_delete", c.args)
		wantErr(t, fmt.Sprint(c.args), err, c.says...)
	}
	if got := f.changes(); len(got) != 0 {
		t.Errorf("sent %v", got)
	}
}

// Taking out the main ebook leaves the book with none, and says so; without
// the upload permission the disk cannot be looked at, and that is said too.
func TestItemDeleteFileSaysWhatElseChanges(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	trimmable(f)
	f.json("GET /api/me", `{"id":"u2","username":"curator","type":"user","permissions":{"delete":true}}`)
	f.json("DELETE /api/items/"+itemID+"/file/e1", `OK`)
	call := toolCaller(t, f)

	out, err := call("item_delete", map[string]any{"item": itemID, "file": "Dune.epub", "confirm": true})
	if err != nil {
		t.Fatal(err)
	}
	note := str(t, out["note"])
	for _, w := range []string{"main ebook", "left with none", "item_edit ebook", "not checked on disk", "upload permission"} {
		if !strings.Contains(note, w) {
			t.Errorf("note %q does not say %q", note, w)
		}
	}
	if isTrue(out["files_removed"]) || len(f.requests("/api/filesystem/pathexists")) != 0 {
		t.Errorf("answer = %v: the disk was not looked at", out)
	}
}

// twoTrackDune is Dune in two 64k mono mp3s beside an excluded intro, or once
// merged, the one m4b the merge made of them.
func twoTrackDune(merged bool, extra string) string {
	audio := `{"index":-1,"ino":"i0","exclude":true,"duration":5,"bitRate":128000,"channels":2,"metadata":{"filename":"intro.mp3","relPath":"intro.mp3"}},` +
		`{"index":1,"ino":"a1","duration":3600,"bitRate":64000,"channels":1,"metadata":{"filename":"01.mp3","relPath":"01.mp3"}},` +
		`{"index":2,"ino":"a2","duration":1800.5,"bitRate":63500,"channels":1,"metadata":{"filename":"02.mp3","relPath":"02.mp3"}}`
	if merged {
		audio = `{"index":-1,"ino":"i0","exclude":true,"duration":5,"metadata":{"filename":"intro.mp3","relPath":"intro.mp3"}},` +
			`{"index":1,"ino":"a1","duration":5400.9,"bitRate":64000,"channels":1,"metadata":{"filename":"Dune.m4b","relPath":"Dune.m4b"}}`
	}
	return `{"id":"` + itemID + `","libraryId":"` + libID + `","mediaType":"book","path":"/audiobooks/Frank Herbert/Dune","relPath":"Frank Herbert/Dune",` +
		`"libraryFiles":[` + extra + `],"media":{"metadata":{"title":"Dune"},"audioFiles":[` + audio + `]}}`
}

// Without confirm the merge is only described: the files that play, in
// order, into a file named after the folder, at the book's own bitrate and
// channels rather than the server's 128k stereo.
func TestItemEmbedMetadataM4BPreview(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /api/items/"+itemID, twoTrackDune(false, ""))
	f.json("GET /api/tasks", `{"tasks":[]}`)
	call := toolCaller(t, f)

	out, err := call("item_embed_metadata", map[string]any{"item": itemID, "m4b": true})
	if err != nil {
		t.Fatal(err)
	}
	plan := object(t, out["would_merge"])
	if !slices.Equal(strs(t, plan["files"]), []string{"01.mp3", "02.mp3"}) || plan["into"] != "Dune.m4b" || !slices.Equal(strs(t, plan["left"]), []string{"intro.mp3"}) {
		t.Errorf("plan = %v", plan)
	}
	wantNumbers(t, "would_merge", plan, map[string]float64{"bitrate_kbps": 64, "channels": 1})
	if !strings.Contains(str(t, plan["originals"]), "metadata/cache/items/"+itemID) {
		t.Errorf("originals = %v", plan["originals"])
	}

	out, err = call("item_embed_metadata", map[string]any{"item": itemID, "m4b": true, "bitrate_kbps": 96, "channels": 2})
	if err != nil {
		t.Fatal(err)
	}
	wantNumbers(t, "would_merge", object(t, out["would_merge"]), map[string]float64{"bitrate_kbps": 96, "channels": 2})
	if got := f.changes(); len(got) != 0 {
		t.Errorf("a preview sent %v", got)
	}
}

// Confirmed, the merge is started at the book's quality, waited on, and the
// book scanned and read back: it plays the one m4b, at its length.
func TestItemEmbedMetadataM4BMerges(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	var scanned atomic.Bool
	f.mux.HandleFunc("GET /api/items/"+itemID, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, twoTrackDune(scanned.Load(), ""))
	})
	running := `{"tasks":[{"id":"t1","action":"encode-m4b","isFinished":false,"data":{"libraryItemId":"` + itemID + `"}}]}`
	f.inTurn("GET /api/tasks", `{"tasks":[]}`, running, running, `{"tasks":[]}`)
	f.json("POST /api/tools/item/"+itemID+"/encode-m4b", `OK`)
	f.mux.HandleFunc("POST /api/items/"+itemID+"/scan", func(w http.ResponseWriter, _ *http.Request) {
		scanned.Store(true)
		_, _ = io.WriteString(w, `{"result":"UPDATED"}`)
	})
	call := toolCaller(t, f)

	out, err := call("item_embed_metadata", map[string]any{"item": itemID, "m4b": true, "confirm": true})
	if err != nil {
		t.Fatal(err)
	}
	if object(t, out["merged"])["into"] != "Dune.m4b" || out["rescan"] != "UPDATED" || out["running"] != nil {
		t.Errorf("answer = %v", out)
	}
	sent := f.requests("/api/tools/item/" + itemID + "/encode-m4b")
	if len(sent) != 1 {
		t.Fatalf("encode sent %d times", len(sent))
	}
	if q := parseQuery(t, sent[0].Query); q.Get("bitrate") != "64k" || q.Get("channels") != "1" {
		t.Errorf("encoded with %v", q)
	}
}

// A merge that ends without the book playing one m4b failed: the server only
// says so in its log.
func TestItemEmbedMetadataM4BFailure(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /api/items/"+itemID, twoTrackDune(false, ""))
	f.json("GET /api/tasks", `{"tasks":[]}`)
	f.json("POST /api/tools/item/"+itemID+"/encode-m4b", `OK`)
	f.json("POST /api/items/"+itemID+"/scan", `{"result":"UPTODATE"}`)
	call := toolCaller(t, f)

	_, err := call("item_embed_metadata", map[string]any{"item": itemID, "m4b": true, "confirm": true})
	wantErr(t, "a merge that failed", err, "ended without making the book one m4b", "01.mp3, 02.mp3", "match=AbMergeManager")
}

// A merge running is said, or stopped with cancel; cancel with none running
// is an error.
func TestItemEmbedMetadataM4BRunningAndCancel(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /api/items/"+itemID, twoTrackDune(false, ""))
	f.json("GET /api/tasks", `{"tasks":[{"id":"t1","action":"encode-m4b","isFinished":false,"data":{"libraryItemId":"`+itemID+`"}}]}`)
	f.json("DELETE /api/tools/item/"+itemID+"/encode-m4b", `OK`)
	call := toolCaller(t, f)

	out, err := call("item_embed_metadata", map[string]any{"item": itemID, "m4b": true, "confirm": true})
	if err != nil {
		t.Fatal(err)
	}
	if !isTrue(out["running"]) || len(f.requests("/api/tools/item/"+itemID+"/encode-m4b")) != 0 {
		t.Errorf("a merge already running: %v", out)
	}
	out, err = call("item_embed_metadata", map[string]any{"item": itemID, "m4b": true, "cancel": true})
	if err != nil {
		t.Fatal(err)
	}
	if !isTrue(out["cancelled"]) {
		t.Errorf("cancel = %v", out)
	}

	f.json("GET /api/tasks", `{"tasks":[]}`)
	_, err = call("item_embed_metadata", map[string]any{"item": itemID, "m4b": true, "cancel": true})
	wantErr(t, "cancel with none running", err, "no merge of", "is running")
}

// What cannot be merged, or would lose something, is refused before
// anything is sent.
func TestItemEmbedMetadataM4BRefuses(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /api/tasks", `{"tasks":[]}`)
	call := toolCaller(t, f)

	for _, c := range []struct {
		book string
		args map[string]any
		says []string
	}{
		{twoTrackDune(false, ""), map[string]any{"backup": true}, []string{"backup is for tagging"}},
		{twoTrackDune(false, ""), map[string]any{"bitrate_kbps": 640}, []string{"640k", "16 to 320"}},
		{twoTrackDune(false, ""), map[string]any{"channels": 6}, []string{"6 channels", "1 or 2"}},
		{twoTrackDune(true, ""), nil, []string{"already one m4b", "Dune.m4b"}},
		// a Dune.m4b in the folder that is not one of the files merged
		{twoTrackDune(false, `{"ino":"x1","fileType":"audio","metadata":{"filename":"Dune.m4b","relPath":"Dune.m4b"}}`), nil, []string{"already holds", "Dune.m4b", "overwrite"}},
		{strings.Replace(twoTrackDune(false, ""), `"relPath":"Frank Herbert/Dune"`, `"relPath":"Dune.mp3","isFile":true`, 1), nil, []string{"one file at the library root", "progress"}},
	} {
		f.json("GET /api/items/"+itemID, c.book)
		args := map[string]any{"item": itemID, "m4b": true, "confirm": true}
		maps.Copy(args, c.args)
		_, err := call("item_embed_metadata", args)
		wantErr(t, fmt.Sprint(c.args), err, c.says...)
	}
	_, err := call("item_embed_metadata", map[string]any{"item": itemID, "bitrate_kbps": 64})
	wantErr(t, "a bitrate without m4b", err, "are for m4b")
	if got := f.changes(); len(got) != 0 {
		t.Errorf("sent %v", got)
	}
}

// The server's file watcher holds the merge's new file for some seconds, and
// a rescan meanwhile skips it: the book reads as holding no audio, which is
// waited out rather than called a failed merge.
func TestItemEmbedMetadataM4BWaitsForTheWatcher(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	var scans atomic.Int32
	held := strings.Replace(twoTrackDune(true, ""), `,{"index":1,"ino":"a1","duration":5400.9,"bitRate":64000,"channels":1,"metadata":{"filename":"Dune.m4b","relPath":"Dune.m4b"}}`, "", 1)
	f.mux.HandleFunc("GET /api/items/"+itemID, func(w http.ResponseWriter, _ *http.Request) {
		switch scans.Load() {
		case 0:
			_, _ = io.WriteString(w, twoTrackDune(false, ""))
		case 1:
			_, _ = io.WriteString(w, held)
		default:
			_, _ = io.WriteString(w, twoTrackDune(true, ""))
		}
	})
	f.json("GET /api/tasks", `{"tasks":[]}`)
	f.json("POST /api/tools/item/"+itemID+"/encode-m4b", `OK`)
	f.mux.HandleFunc("POST /api/items/"+itemID+"/scan", func(w http.ResponseWriter, _ *http.Request) {
		scans.Add(1)
		_, _ = io.WriteString(w, `{"result":"UPDATED"}`)
	})
	call := toolCaller(t, f)

	out, err := call("item_embed_metadata", map[string]any{"item": itemID, "m4b": true, "confirm": true})
	if err != nil {
		t.Fatal(err)
	}
	if object(t, out["merged"])["into"] != "Dune.m4b" || scans.Load() != 2 {
		t.Errorf("answer = %v after %d scans, want merged on the second", out, scans.Load())
	}
	if _, has := out["embedded"]; has {
		t.Errorf("a merge answered embedded: %v", out)
	}
}

// The server grants delete by the permission alone: an admin without it was
// let through, lost its bookmarks on the book, and then had the delete
// refused. It is refused before anything goes.
func TestItemDeleteNeedsTheDeletePermission(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	deleteRoutes(f, false)
	f.json("GET /api/me", `{"id":"u1","username":"curator","type":"admin","permissions":{"delete":false},"bookmarks":[{"libraryItemId":"`+itemID+`","title":"Arrakis","time":5}]}`)
	call := toolCaller(t, f)

	for _, args := range []map[string]any{{"confirm": true}, {"file": "notes.txt", "confirm": true}} {
		args["item"] = itemID
		_, err := call("item_delete", args)
		wantErr(t, fmt.Sprint(args), err, "curator may not delete", "delete permission")
	}
	if got := f.changes(); len(got) != 0 {
		t.Errorf("sent %v", got)
	}
}
