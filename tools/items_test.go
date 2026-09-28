package tools

import (
	"encoding/json"
	"fmt"
	"io"
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
	f.json("GET /api/me", `{"id":"u1","username":"kt","type":"root","bookmarks":[`+
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
