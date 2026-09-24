package tools

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
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

// methodsSeen lists the requests that were not reads.
func methodsSeen(f *fakeABS) []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	var out []string
	for _, r := range f.seen {
		if r.Method != http.MethodGet {
			out = append(out, r.Method+" "+r.Path)
		}
	}

	return out
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
		if got := methodsSeen(f); len(got) != 0 {
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
	if got := methodsSeen(f); len(got) != 0 {
		t.Errorf("sent %v while the bookmarks were held", got)
	}
	release()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := methodsSeen(f); len(got) != 3 {
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
	if got := methodsSeen(f); len(got) != 0 {
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
