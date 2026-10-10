package tools

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/katbyte/abs-mcp/sdk/abs"
)

// movedLibrary is a canned server that remembers: its records, accounts,
// collections, playlists and keys change as they are written to, the way a
// real one's do, so a test can read back what a merge left behind. It follows
// the real server in the two things the merge has to work round: a book set
// finished and given a new position in one call is taken for begun again, and
// only an account's own key writes its progress, bookmarks and playlists.
type movedLibrary struct {
	f  *fakeABS
	mu sync.Mutex

	items       map[string]*abs.Item
	users       []*abs.User // the first is the admin, whose key is "test"
	tokens      map[string]string
	refused     map[string]bool // accounts the server lets no key act as
	keys        map[string]string
	collections []*abs.Collection
	playlists   []*abs.Playlist
	feeds       []abs.Feed
	covers      map[string][]byte
	// breakDetails makes every change of a record's details fail
	breakDetails bool
}

const (
	movedAdmin = "u-admin"
	movedAnn   = "u-ann"
	movedBob   = "u-bob"
)

// audio is a record's audio files, each by its name and size.
func audio(files ...any) []abs.AudioFile {
	out := make([]abs.AudioFile, 0, len(files)/2)
	for i := 0; i+1 < len(files); i += 2 {
		name, named := files[i].(string)
		size, sized := files[i+1].(int)
		if !named || !sized {
			panic("audio takes each file's name and then its size")
		}
		out = append(out, abs.AudioFile{Index: i/2 + 1, Duration: 1800, Metadata: abs.FileMetadata{Filename: name, Size: int64(size)}})
	}

	return out
}

// book is a record at a path with its audio, missing or not.
func book(id, rel, title string, missing bool, files []abs.AudioFile) *abs.Item {
	it := &abs.Item{ID: id, LibraryID: libID, MediaType: "book", RelPath: rel, Path: "/audiobooks/" + rel, IsMissing: missing, AddedAt: 1756000000000}
	it.Media.Metadata.Title = title
	it.Media.Metadata.Authors = []abs.NameRef{{Name: "Orson Scott Card"}}
	it.Media.AudioFiles, it.Media.NumAudioFiles = files, len(files)
	for _, f := range files {
		it.Media.Duration += f.Duration
	}

	return it
}

func newMovedLibrary(t *testing.T, items ...*abs.Item) *movedLibrary {
	t.Helper()

	w := &movedLibrary{
		f: newFakeABS(t), items: map[string]*abs.Item{}, tokens: map[string]string{"test": movedAdmin},
		refused: map[string]bool{}, keys: map[string]string{}, covers: map[string][]byte{},
		users: []*abs.User{
			{ID: movedAdmin, Username: "kt", Type: "root", IsActive: true},
			{ID: movedAnn, Username: "ann", Type: "user", IsActive: true},
			{ID: movedBob, Username: "bob", Type: "user", IsActive: true},
		},
	}
	for _, it := range items {
		w.items[it.ID] = it
	}
	oneLibrary(w.f)
	mux := w.f.mux

	send := func(rw http.ResponseWriter, v any) {
		rw.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(rw).Encode(v)
	}
	// locked runs a handler with the library held and the account the
	// request's key acts as, or refuses a key the server does not take
	locked := func(h func(rw http.ResponseWriter, r *http.Request, as *abs.User)) http.HandlerFunc {
		return func(rw http.ResponseWriter, r *http.Request) {
			w.mu.Lock()
			defer w.mu.Unlock()

			id, known := w.tokens[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
			if !known || w.refused[id] {
				http.Error(rw, "Unauthorized", http.StatusUnauthorized)
				return
			}
			h(rw, r, w.user(id))
		}
	}
	read := func(r *http.Request, into any) { _ = json.NewDecoder(r.Body).Decode(into) }

	mux.HandleFunc("GET /api/libraries/{lib}/items", locked(func(rw http.ResponseWriter, r *http.Request, _ *abs.User) {
		var rows []*abs.Item
		for _, id := range w.ids() {
			if it := w.items[id]; r.URL.Query().Get("filter") == "" || it.IsMissing {
				rows = append(rows, it)
			}
		}
		if r.URL.Query().Get("page") != "0" {
			send(rw, map[string]any{"results": []any{}, "total": len(rows)})
			return
		}
		send(rw, map[string]any{"results": rows, "total": len(rows)})
	}))
	mux.HandleFunc("POST /api/items/batch/get", locked(func(rw http.ResponseWriter, r *http.Request, _ *abs.User) {
		var asked struct {
			IDs []string `json:"libraryItemIds"`
		}
		read(r, &asked)
		rows := []*abs.Item{}
		for _, id := range asked.IDs {
			if it, ok := w.items[id]; ok {
				rows = append(rows, it)
			}
		}
		send(rw, map[string]any{"libraryItems": rows})
	}))
	mux.HandleFunc("GET /api/items/{id}", locked(func(rw http.ResponseWriter, r *http.Request, _ *abs.User) {
		it, ok := w.items[r.PathValue("id")]
		if !ok {
			http.NotFound(rw, r)
			return
		}
		send(rw, it)
	}))
	mux.HandleFunc("PATCH /api/items/{id}/media", locked(func(rw http.ResponseWriter, r *http.Request, _ *abs.User) {
		it, ok := w.items[r.PathValue("id")]
		if !ok || w.breakDetails {
			http.Error(rw, "the database is locked", http.StatusInternalServerError)
			return
		}
		var upd struct {
			Metadata *struct {
				Title, ASIN *string
				Series      []abs.SeriesRef
				Authors     []abs.NameRef
			} `json:"metadata"`
			Tags []string `json:"tags"`
		}
		read(r, &upd)
		if m := upd.Metadata; m != nil {
			if m.Title != nil {
				it.Media.Metadata.Title = *m.Title
			}
			if m.ASIN != nil {
				it.Media.Metadata.ASIN = *m.ASIN
			}
			// a book is tied to its series and its authors, and lists
			// them in the order it was tied: one it already has stays where
			// it is, one it lacks goes last, and one not sent is untied
			if m.Series != nil {
				var next abs.SeriesRefs
				for _, has := range it.Media.Metadata.Series {
					if k := slices.IndexFunc(m.Series, func(s abs.SeriesRef) bool { return s.Name == has.Name }); k >= 0 {
						next = append(next, m.Series[k])
					}
				}
				for _, sent := range m.Series {
					if !slices.ContainsFunc(next, func(s abs.SeriesRef) bool { return s.Name == sent.Name }) {
						next = append(next, sent)
					}
				}
				it.Media.Metadata.Series = next
			}
			if m.Authors != nil {
				var next []abs.NameRef
				for _, has := range it.Media.Metadata.Authors {
					if slices.ContainsFunc(m.Authors, func(a abs.NameRef) bool { return a.Name == has.Name }) {
						next = append(next, has)
					}
				}
				for _, sent := range m.Authors {
					if !slices.ContainsFunc(next, func(a abs.NameRef) bool { return a.Name == sent.Name }) {
						next = append(next, sent)
					}
				}
				it.Media.Metadata.Authors = next
			}
		}
		if upd.Tags != nil {
			it.Media.Tags = upd.Tags
		}
		send(rw, map[string]any{"updated": true})
	}))
	mux.HandleFunc("POST /api/items/{id}/chapters", locked(func(rw http.ResponseWriter, r *http.Request, _ *abs.User) {
		var sent struct {
			Chapters []abs.Chapter `json:"chapters"`
		}
		read(r, &sent)
		w.items[r.PathValue("id")].Media.Chapters = sent.Chapters
		send(rw, map[string]any{"success": true, "updated": true})
	}))
	mux.HandleFunc("GET /api/items/{id}/cover", locked(func(rw http.ResponseWriter, r *http.Request, _ *abs.User) {
		img, ok := w.covers[r.PathValue("id")]
		if !ok {
			http.NotFound(rw, r)
			return
		}
		_, _ = rw.Write(img)
	}))
	mux.HandleFunc("POST /api/items/{id}/cover", locked(func(rw http.ResponseWriter, r *http.Request, _ *abs.User) {
		file, _, err := r.FormFile("cover")
		if err != nil {
			http.Error(rw, err.Error(), http.StatusBadRequest)
			return
		}
		img, _ := io.ReadAll(file)
		id := r.PathValue("id")
		w.covers[id], w.items[id].Media.CoverPath = img, "/metadata/items/"+id+"/cover.jpg"
		send(rw, map[string]any{"success": true})
	}))
	mux.HandleFunc("DELETE /api/items/{id}", locked(func(rw http.ResponseWriter, r *http.Request, _ *abs.User) {
		id := r.PathValue("id")
		delete(w.items, id)
		for _, c := range w.collections {
			c.Books = slices.DeleteFunc(c.Books, func(b abs.Item) bool { return b.ID == id })
		}
		for _, p := range w.playlists {
			p.Items = slices.DeleteFunc(p.Items, func(e abs.PlaylistItem) bool { return e.LibraryItemID == id })
		}
		_, _ = io.WriteString(rw, "OK")
	}))

	mux.HandleFunc("GET /api/me", locked(func(rw http.ResponseWriter, _ *http.Request, as *abs.User) { send(rw, as) }))
	mux.HandleFunc("GET /api/users", locked(func(rw http.ResponseWriter, _ *http.Request, _ *abs.User) {
		rows := make([]abs.User, 0, len(w.users))
		for _, u := range w.users {
			rows = append(rows, abs.User{ID: u.ID, Username: u.Username, Type: u.Type, IsActive: u.IsActive})
		}
		send(rw, map[string]any{"users": rows})
	}))
	mux.HandleFunc("GET /api/users/{id}", locked(func(rw http.ResponseWriter, r *http.Request, _ *abs.User) { send(rw, w.user(r.PathValue("id"))) }))
	mux.HandleFunc("GET /api/feeds", locked(func(rw http.ResponseWriter, _ *http.Request, _ *abs.User) { send(rw, map[string]any{"feeds": w.feeds}) }))

	mux.HandleFunc("GET /api/libraries/{lib}/collections", locked(func(rw http.ResponseWriter, _ *http.Request, _ *abs.User) {
		send(rw, map[string]any{"results": w.collections})
	}))
	mux.HandleFunc("POST /api/collections/{id}/book", locked(func(rw http.ResponseWriter, r *http.Request, _ *abs.User) {
		var sent struct {
			ID string `json:"id"`
		}
		read(r, &sent)
		for _, c := range w.collections {
			if c.ID == r.PathValue("id") {
				c.Books = append(c.Books, abs.Item{ID: sent.ID})
				send(rw, c)
				return
			}
		}
		http.NotFound(rw, r)
	}))
	mux.HandleFunc("PATCH /api/collections/{id}", locked(func(rw http.ResponseWriter, r *http.Request, _ *abs.User) {
		var sent struct {
			Books []string `json:"books"`
		}
		read(r, &sent)
		for _, c := range w.collections {
			if c.ID == r.PathValue("id") {
				slices.SortStableFunc(c.Books, func(a, b abs.Item) int { return slices.Index(sent.Books, a.ID) - slices.Index(sent.Books, b.ID) })
				send(rw, c)
				return
			}
		}
		http.NotFound(rw, r)
	}))
	mux.HandleFunc("PATCH /api/playlists/{id}", locked(func(rw http.ResponseWriter, r *http.Request, as *abs.User) {
		var sent struct {
			Items []abs.PlaylistEntry `json:"items"`
		}
		read(r, &sent)
		for _, p := range w.playlists {
			if p.ID != r.PathValue("id") || p.UserID != as.ID {
				continue
			}
			if len(sent.Items) != len(p.Items) {
				http.Error(rw, "Invalid playlist items. Length mismatch", http.StatusBadRequest)
				return
			}
			at := func(e abs.PlaylistItem) int {
				return slices.IndexFunc(sent.Items, func(s abs.PlaylistEntry) bool { return s.LibraryItemID == e.LibraryItemID })
			}
			slices.SortStableFunc(p.Items, func(a, b abs.PlaylistItem) int { return at(a) - at(b) })
			send(rw, p)
			return
		}
		http.NotFound(rw, r)
	}))
	mux.HandleFunc("GET /api/libraries/{lib}/playlists", locked(func(rw http.ResponseWriter, _ *http.Request, as *abs.User) {
		rows := []*abs.Playlist{}
		for _, p := range w.playlists {
			if p.UserID == as.ID {
				rows = append(rows, p)
			}
		}
		send(rw, map[string]any{"results": rows})
	}))
	mux.HandleFunc("POST /api/playlists/{id}/item", locked(func(rw http.ResponseWriter, r *http.Request, as *abs.User) {
		var sent abs.PlaylistEntry
		read(r, &sent)
		for _, p := range w.playlists {
			if p.ID == r.PathValue("id") && p.UserID == as.ID {
				p.Items = append(p.Items, abs.PlaylistItem{LibraryItemID: sent.LibraryItemID})
				send(rw, p)
				return
			}
		}
		http.NotFound(rw, r)
	}))

	mux.HandleFunc("PATCH /api/me/progress/{id}", locked(func(rw http.ResponseWriter, r *http.Request, as *abs.User) {
		var sent struct {
			CurrentTime, Duration     *float64
			IsFinished                *bool
			HideFromContinueListening *bool
			FinishedAt, LastUpdate    *int64
		}
		read(r, &sent)
		p := mergeProgressOn(as, r.PathValue("id"))
		if p == nil {
			as.MediaProgress = append(as.MediaProgress, abs.MediaProgress{LibraryItemID: r.PathValue("id"), StartedAt: 1790000000000})
			p = &as.MediaProgress[len(as.MediaProgress)-1]
		}
		const now = 1790000000000
		moved := sent.CurrentTime != nil && *sent.CurrentTime != p.CurrentTime
		switch {
		case sent.IsFinished != nil && *sent.IsFinished && !p.IsFinished:
			p.IsFinished, p.FinishedAt = true, now
			if sent.FinishedAt != nil {
				p.FinishedAt = *sent.FinishedAt
			}
		case sent.IsFinished != nil && !*sent.IsFinished && p.IsFinished:
			p.IsFinished, p.FinishedAt, p.CurrentTime = false, 0, 0
		case sent.FinishedAt != nil:
			p.FinishedAt = *sent.FinishedAt
		}
		if sent.CurrentTime != nil {
			p.CurrentTime = *sent.CurrentTime
		}
		if sent.Duration != nil {
			p.Duration = *sent.Duration
		}
		if sent.HideFromContinueListening != nil {
			p.HideFromContinueListening = *sent.HideFromContinueListening
		}
		// a finished book given a new position short of its end is begun again
		if p.IsFinished && moved && p.Duration-p.CurrentTime >= 10 {
			p.IsFinished, p.FinishedAt = false, 0
		}
		if p.Duration > 0 {
			p.Progress = p.CurrentTime / p.Duration
		}
		p.LastUpdate = now
		if sent.LastUpdate != nil {
			p.LastUpdate = *sent.LastUpdate
		}
		_, _ = io.WriteString(rw, "OK")
	}))
	mux.HandleFunc("POST /api/me/item/{id}/bookmark", locked(func(rw http.ResponseWriter, r *http.Request, as *abs.User) {
		var sent struct {
			Time  float64 `json:"time"`
			Title string  `json:"title"`
		}
		read(r, &sent)
		b := abs.Bookmark{LibraryItemID: r.PathValue("id"), Time: sent.Time, Title: sent.Title}
		as.Bookmarks = append(as.Bookmarks, b)
		send(rw, b)
	}))

	mux.HandleFunc("POST /api/api-keys", locked(func(rw http.ResponseWriter, r *http.Request, as *abs.User) {
		var sent struct {
			UserID    string `json:"userId"`
			ExpiresIn int    `json:"expiresIn"`
		}
		read(r, &sent)
		if as.Type != "root" || sent.ExpiresIn <= 0 {
			http.Error(rw, "a key that never expires, or not an admin", http.StatusBadRequest)
			return
		}
		id := "key-" + sent.UserID
		w.keys[id], w.tokens["secret-"+sent.UserID] = "secret-"+sent.UserID, sent.UserID
		send(rw, map[string]any{"apiKey": abs.APIKey{ID: id, UserID: sent.UserID, Key: "secret-" + sent.UserID}})
	}))
	mux.HandleFunc("DELETE /api/api-keys/{id}", locked(func(rw http.ResponseWriter, r *http.Request, _ *abs.User) {
		delete(w.tokens, w.keys[r.PathValue("id")])
		delete(w.keys, r.PathValue("id"))
		send(rw, map[string]any{"success": true})
	}))

	return w
}

func (w *movedLibrary) ids() []string {
	ids := make([]string, 0, len(w.items))
	for id := range w.items {
		ids = append(ids, id)
	}
	slices.Sort(ids)

	return ids
}

func (w *movedLibrary) user(id string) *abs.User {
	return w.users[slices.IndexFunc(w.users, func(u *abs.User) bool { return u.ID == id })]
}

// stonefather is the book the tests move: an old record, curated and
// listened to, whose folder is gone, and the bare record the scan made for
// the folder under its new name.
func stonefather(t *testing.T) (w *movedLibrary, fresh *abs.Item) {
	t.Helper()

	files := audio("Stonefather - 1.m4b", 797460, "Stonefather - 2.m4b", 179485029)
	old := book("old", "Orson Scott Card/Stonefather", "Stonefather", true, files)
	old.Media.Metadata.ASIN = "B071RWQ7MD"
	old.Media.Metadata.Series = abs.SeriesRefs{{Name: "Mithermages", Sequence: "0.5"}}
	old.Media.Tags = []string{"zz-provider:audible.ca"}
	old.Media.Chapters = []abs.Chapter{{ID: 0, Title: "Opening Credits", End: 20}, {ID: 1, Title: "Stonefather", Start: 20, End: 3600}}
	old.Media.CoverPath = "/metadata/items/old/cover.jpg"
	fresh = book("new", "Orson Scott Card/Mithermages - 00.5 - Stonefather", "Mithermages - 00.5 - Stonefather", false, files)
	fresh.AddedAt = 1790000000000
	fresh.Media.Chapters = []abs.Chapter{{ID: 0, Title: "Stonefather - 1", End: 20}}
	fresh.Media.CoverPath = fresh.Path + "/folder.jpg"

	w = newMovedLibrary(t, old, fresh,
		book("other", "Orson Scott Card/Empire", "Empire", false, audio("Empire.m4b", 5000)),
		// deleted for good: nothing holds its audio
		book("gone", "Orson Scott Card/Lost Boys", "Lost Boys", true, audio("Lost Boys.m4b", 7000)),
		// held twice: nothing says which copy took its place
		book("twice", "Orson Scott Card/Enchantment", "Enchantment", true, audio("Enchantment.m4b", 9000)),
		book("copy1", "Orson Scott Card/Enchantment (1)", "Enchantment", false, audio("Enchantment.m4b", 9000)),
		book("copy2", "Orson Scott Card/Enchantment (2)", "Enchantment", false, audio("Enchantment.m4b", 9000)),
	)
	w.covers["old"], w.covers["new"] = []byte("the store's cover"), []byte("a picture in the folder")
	kt, ann := w.user(movedAdmin), w.user(movedAnn)
	kt.MediaProgress = []abs.MediaProgress{{LibraryItemID: "old", CurrentTime: 1200, Duration: 3600, Progress: 1, IsFinished: true, FinishedAt: 1711000000000, LastUpdate: 1711000000000, StartedAt: 1710000000000}}
	kt.Bookmarks = []abs.Bookmark{{LibraryItemID: "old", Time: 90, Title: "the stone"}}
	ann.MediaProgress = []abs.MediaProgress{{LibraryItemID: "old", CurrentTime: 1440, Duration: 3600, Progress: 0.4, LastUpdate: 1750000000000, StartedAt: 1749000000000}}
	ann.Bookmarks = []abs.Bookmark{{LibraryItemID: "old", Time: 30, Title: "ann's"}, {LibraryItemID: "other", Time: 5, Title: "elsewhere"}}
	// the book first in each, where a merge that only added it would leave it last
	w.collections = []*abs.Collection{{ID: "c1", LibraryID: libID, Name: "Card", Books: []abs.Item{{ID: "old"}, {ID: "other"}}}}
	w.playlists = []*abs.Playlist{
		{ID: "p-kt", LibraryID: libID, UserID: movedAdmin, Name: "Next", Items: []abs.PlaylistItem{{LibraryItemID: "old"}, {LibraryItemID: "other"}}},
		{ID: "p-ann", LibraryID: libID, UserID: movedAnn, Name: "Ann's queue", Items: []abs.PlaylistItem{{LibraryItemID: "old"}, {LibraryItemID: "other"}}},
	}

	return w, fresh
}

// A book whose folder was renamed, on a server that made a new record for
// it: the preview names the pair and what would go across and writes nothing;
// confirmed, everything the old record had is on the new one, read back from
// the server, the old record is gone, and so is every key made on the way.
func TestIssuesMergePutsAMovedBookBackTogether(t *testing.T) {
	t.Parallel()

	w, fresh := stonefather(t)
	call := toolCaller(t, w.f)

	out, err := call("library_issues_merge", nil)
	if err != nil {
		t.Fatal(err)
	}
	// the batch read of records is a POST, and the only one a preview makes
	for _, r := range w.f.changes() {
		if r.Path != "/api/items/batch/get" {
			t.Fatalf("the preview wrote: %s %s", r.Method, r.Path)
		}
	}
	if num(t, out["found"]) != 3 || num(t, out["merged"]) != 0 || num(t, out["remaining"]) != 3 {
		t.Errorf("preview = %v, want 3 missing, none merged", out)
	}
	pairs := list(t, out["pairs"])
	if len(pairs) != 1 {
		t.Fatalf("pairs = %v, want Stonefather alone", out["pairs"])
	}
	pair := pairs[0]
	if object(t, pair["from"])["id"] != "old" || object(t, pair["into"])["id"] != "new" || isTrue(pair["merged"]) ||
		str(t, pair["same"]) != "2 audio files of the same names and sizes" {
		t.Errorf("the pair = %v", pair)
	}
	carry := object(t, pair["carry"])
	for field, want := range map[string][]string{
		"details": {"title", "asin", "tags", "series"}, "progress": {"kt", "ann"}, "bookmarks": {"kt", "ann"},
		"collections": {"Card"}, "playlists": {"Next (kt)"},
	} {
		if got := strs(t, carry[field]); !slices.Equal(got, want) {
			t.Errorf("would carry %s = %v, want %v", field, got, want)
		}
	}
	if !isTrue(carry["chapters"]) || !isTrue(carry["cover"]) {
		t.Errorf("would carry = %v, want the chapters and the cover too", carry)
	}
	if lost := strings.Join(strs(t, pair["lost"]), "; "); !strings.Contains(lost, "the day it was added (2025-08-24)") || !strings.Contains(lost, "the day ann started it") {
		t.Errorf("lost = %q, want the day added and the days it was started", lost)
	}
	why := map[string]string{}
	for _, row := range list(t, out["unpaired"]) {
		why[str(t, row["id"])] = str(t, row["why"])
		if str(t, row["id"]) == "twice" && len(list(t, row["candidates"])) != 2 {
			t.Errorf("the book held twice = %v, want both copies named", row)
		}
	}
	if !strings.Contains(why["gone"], "no record holds the same audio files") || !strings.Contains(why["twice"], "2 records hold the same audio files") || len(why) != 2 {
		t.Errorf("unpaired = %v", why)
	}

	out, err = call("library_issues_merge", map[string]any{"confirm": true})
	if err != nil {
		t.Fatal(err)
	}
	if num(t, out["merged"]) != 1 || num(t, out["remaining"]) != 2 || !isTrue(list(t, out["pairs"])[0]["merged"]) {
		t.Fatalf("confirmed = %v, want the one pair merged and two records still missing", out)
	}
	w.mu.Lock()
	defer w.mu.Unlock()

	if _, there := w.items["old"]; there {
		t.Error("the old record is still there")
	}
	m := fresh.Media
	if m.Metadata.Title != "Stonefather" || m.Metadata.ASIN != "B071RWQ7MD" || len(m.Metadata.Series) != 1 || m.Metadata.Series[0].Sequence != "0.5" ||
		!slices.Equal(m.Tags, []string{"zz-provider:audible.ca"}) || len(m.Chapters) != 2 || m.Chapters[1].Title != "Stonefather" {
		t.Errorf("the new record = %+v, want the old record's details, tags and chapters", m)
	}
	if !bytes.Equal(w.covers["new"], []byte("the store's cover")) {
		t.Errorf("the new record's cover = %q, want the one the server kept for the old record", w.covers["new"])
	}
	kt, ann, bob := w.user(movedAdmin), w.user(movedAnn), w.user(movedBob)
	if p := mergeProgressOn(kt, "new"); p == nil || !p.IsFinished || p.FinishedAt != 1711000000000 || p.CurrentTime != 1200 || p.LastUpdate != 1711000000000 {
		t.Errorf("kt's progress on the new record = %+v, want finished in March 2024 at 20 minutes in", p)
	}
	if p := mergeProgressOn(ann, "new"); p == nil || p.IsFinished || p.CurrentTime != 1440 || p.LastUpdate != 1750000000000 {
		t.Errorf("ann's progress on the new record = %+v, want 24 minutes in, unfinished, last listened when it was", p)
	}
	if mergeProgressOn(bob, "new") != nil {
		t.Error("bob, who never listened to it, has progress on it")
	}
	marks := func(u *abs.User) (titles []string) {
		for _, b := range u.Bookmarks {
			if b.LibraryItemID == "new" {
				titles = append(titles, b.Title)
			}
		}
		return titles
	}
	if !slices.Equal(marks(kt), []string{"the stone"}) || !slices.Equal(marks(ann), []string{"ann's"}) {
		t.Errorf("bookmarks on the new record: kt %v, ann %v", marks(kt), marks(ann))
	}
	shelf := make([]string, 0, len(w.collections[0].Books))
	for _, b := range w.collections[0].Books {
		shelf = append(shelf, b.ID)
	}
	queued := func(p *abs.Playlist) (ids []string) {
		for _, e := range p.Items {
			ids = append(ids, e.LibraryItemID)
		}
		return ids
	}
	if want := []string{"new", "other"}; !slices.Equal(shelf, want) || !slices.Equal(queued(w.playlists[0]), want) || !slices.Equal(queued(w.playlists[1]), want) {
		t.Errorf("the collection %v, kt's playlist %v and ann's %v, want the new record where the old one was in each: %v", shelf, queued(w.playlists[0]), queued(w.playlists[1]), want)
	}
	if len(w.keys) != 0 || len(w.tokens) != 1 {
		t.Errorf("keys left behind: %v", w.keys)
	}
	if _, present := out["keys_left"]; present {
		t.Errorf("keys_left = %v with every key deleted", out["keys_left"])
	}
}

// What cannot be carried keeps the old record where it is: an open feed that
// deleting it would close, an account no key can act as, and a write the
// server refuses part way. Each says why, and nothing is deleted.
func TestIssuesMergeKeepsARecordItCannotCarry(t *testing.T) {
	t.Parallel()

	kept := func(t *testing.T, w *movedLibrary, args map[string]any) (map[string]any, string) {
		t.Helper()

		out, err := toolCaller(t, w.f)("library_issues_merge", args)
		if err != nil {
			t.Fatal(err)
		}
		w.mu.Lock()
		defer w.mu.Unlock()
		if _, there := w.items["old"]; !there || num(t, out["merged"]) != 0 {
			t.Fatalf("the old record was deleted, or counted merged: %v", out)
		}
		if len(w.keys) != 0 {
			t.Errorf("keys left behind: %v", w.keys)
		}

		return out, str(t, list(t, out["pairs"])[0]["kept"])
	}

	t.Run("an open feed", func(t *testing.T) {
		t.Parallel()
		w, fresh := stonefather(t)
		w.feeds = []abs.Feed{{ID: "f1", EntityID: "old", Slug: "stonefather"}}
		for _, args := range []map[string]any{nil, {"confirm": true}} {
			if _, why := kept(t, w, args); !strings.Contains(why, "an RSS feed is open on the old record") {
				t.Errorf("kept = %q with %v", why, args)
			}
		}
		if fresh.Media.Metadata.Title != "Mithermages - 00.5 - Stonefather" {
			t.Error("the new record was written to though the pair was not merged")
		}
	})

	t.Run("an account no key acts as", func(t *testing.T) {
		t.Parallel()
		w, _ := stonefather(t)
		w.refused[movedAnn] = true
		out, why := kept(t, w, map[string]any{"confirm": true})
		if !strings.Contains(why, "will not let a key act as ann") {
			t.Errorf("kept = %q", why)
		}
		if got := strs(t, out["accounts_unreached"]); !slices.Equal(got, []string{"ann"}) {
			t.Errorf("accounts_unreached = %v", got)
		}
	})

	t.Run("a write the server refuses", func(t *testing.T) {
		t.Parallel()
		w, _ := stonefather(t)
		w.breakDetails = true
		if _, why := kept(t, w, map[string]any{"confirm": true}); !strings.Contains(why, "the details") || !strings.Contains(why, "the database is locked") {
			t.Errorf("kept = %q, want what failed and what the server said", why)
		}
	})
}

// One record can be merged on its own, named by its id or its path; a name
// that is no missing record is refused before anything is read further.
func TestIssuesMergeOnlyTheRecordsAskedFor(t *testing.T) {
	t.Parallel()

	w, _ := stonefather(t)
	call := toolCaller(t, w.f)
	out, err := call("library_issues_merge", map[string]any{"items": []any{"Orson Scott Card/Lost Boys"}})
	if err != nil {
		t.Fatal(err)
	}
	if num(t, out["found"]) != 3 || len(list(t, out["pairs"])) != 0 || len(list(t, out["unpaired"])) != 1 {
		t.Errorf("asked for one record = %v, want it alone looked at", out)
	}
	if _, err := call("library_issues_merge", map[string]any{"items": []any{"other"}}); err == nil || !strings.Contains(err.Error(), "is not a record whose folder is missing") {
		t.Errorf("a record that is not missing = %v", err)
	}
}

// A record holding the same audio that was in the library long before the
// folder went missing is a second copy of the book, not the folder under a
// new name: its own details are not written over on a guess. It is named as
// a candidate, and merged into only when the caller names it.
func TestIssuesMergeLeavesACopyThatWasAlwaysThere(t *testing.T) {
	t.Parallel()

	files := audio("Enchantment.m4b", 9000)
	old := book("old", "Orson Scott Card/Enchantment", "Enchantment", true, files)
	old.LastScan = 1790000000000 // found missing a year after the copy was added
	standing := book("copy", "Orson Scott Card/Enchantment (Blackstone)", "Enchantment: A Novel", false, files)
	w := newMovedLibrary(t, old, standing, book("other", "Orson Scott Card/Empire", "Empire", false, audio("Empire.m4b", 5000)))
	w.user(movedAdmin).MediaProgress = []abs.MediaProgress{{LibraryItemID: "old", CurrentTime: 600, Duration: 1800}}
	call := toolCaller(t, w.f)

	out, err := call("library_issues_merge", map[string]any{"confirm": true})
	if err != nil {
		t.Fatal(err)
	}
	unpaired := list(t, out["unpaired"])
	if len(list(t, out["pairs"])) != 0 || len(unpaired) != 1 || !strings.Contains(str(t, unpaired[0]["why"]), "already in the library before this one went missing") ||
		!slices.Equal(column(t, "id", unpaired[0]["candidates"]), []string{"copy"}) {
		t.Fatalf("a copy that was always there = %v, want it named as a candidate and nothing merged", out)
	}
	if standing.Media.Metadata.Title != "Enchantment: A Novel" || len(w.f.changes()) != 1 {
		t.Errorf("the copy was written to: %v", w.f.changes())
	}

	if _, err := call("library_issues_merge", map[string]any{"into": "copy"}); err == nil || !strings.Contains(err.Error(), "give that one record in items") {
		t.Errorf("into with no record named = %v", err)
	}
	out, err = call("library_issues_merge", map[string]any{"items": []any{"old"}, "into": "other"})
	if err != nil {
		t.Fatal(err)
	}
	if why := str(t, list(t, out["unpaired"])[0]["why"]); !strings.Contains(why, `"other" does not hold the same audio files`) {
		t.Errorf("into a record of another book = %q", why)
	}

	out, err = call("library_issues_merge", map[string]any{"items": []any{"old"}, "into": "Orson Scott Card/Enchantment (Blackstone)", "confirm": true})
	if err != nil {
		t.Fatal(err)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if p := mergeProgressOn(w.user(movedAdmin), "copy"); num(t, out["merged"]) != 1 || p == nil || p.CurrentTime != 600 || w.items["old"] != nil {
		t.Errorf("named with into = %v, progress %+v, want the old record merged into the copy", out, p)
	}
}

// The server lists a book's series and authors in the order they were tied
// to it, and sent the same ones another way round it changes nothing: a scan
// that ties two in one moment can list them either way, as it did on the
// library this was written for. The merge unties and ties them again, one
// call each, so the new record lists them as the old one did.
func TestIssuesMergeCarriesTheOrderOfSeriesAndAuthors(t *testing.T) {
	t.Parallel()

	files := audio("Children of the Mind.m4b", 8000)
	old := book("old", "Orson Scott Card/Children of the Mind", "Children of the Mind", true, files)
	old.Media.Metadata.Series = abs.SeriesRefs{{Name: "The Ender Saga", Sequence: "4"}, {Name: "The Enderverse", Sequence: "14"}}
	old.Media.Metadata.Authors = []abs.NameRef{{Name: "Orson Scott Card"}, {Name: "Aaron Johnston"}}
	fresh := book("new", "Orson Scott Card/The Ender Saga - 04 - Children of the Mind", "Children of the Mind", false, files)
	fresh.Media.Metadata.Series = abs.SeriesRefs{{Name: "The Enderverse", Sequence: "14"}, {Name: "The Ender Saga", Sequence: "4"}}
	fresh.Media.Metadata.Authors = []abs.NameRef{{Name: "Aaron Johnston"}, {Name: "Orson Scott Card"}}
	w := newMovedLibrary(t, old, fresh)
	call := toolCaller(t, w.f)

	out, err := call("library_issues_merge", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := strs(t, object(t, list(t, out["pairs"])[0]["carry"])["details"]); !slices.Equal(got, []string{"authors", "series"}) {
		t.Errorf("would carry the details %v, want the authors and the series, for their order", got)
	}

	out, err = call("library_issues_merge", map[string]any{"confirm": true})
	if err != nil {
		t.Fatal(err)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if num(t, out["merged"]) != 1 || w.items["old"] != nil {
		t.Fatalf("confirmed = %v, want the pair merged", out)
	}
	if got := fresh.Media.Metadata.SeriesDisplay(); !slices.Equal(got, []string{"The Ender Saga #4", "The Enderverse #14"}) {
		t.Errorf("the new record's series = %v, want them in the old record's order", got)
	}
	if got := authorNames(fresh.Media.Metadata.Authors); !slices.Equal(got, []string{"Orson Scott Card", "Aaron Johnston"}) {
		t.Errorf("the new record's authors = %v, want them in the old record's order", got)
	}
}

func TestSameAudioFiles(t *testing.T) {
	t.Parallel()

	two := audio("a.mp3", 100, "b.mp3", 200)
	for _, tc := range []struct {
		name       string
		old, fresh []abs.AudioFile
		want       string
	}{
		{"the same files", two, audio("b.mp3", 200, "a.mp3", 100), "2 audio files of the same names and sizes"},
		{"one file", audio("a.mp3", 100), audio("a.mp3", 100), "one audio file of the same name and size"},
		{"one file renamed", audio("a.mp3", 100), audio("Title.mp3", 100), "one audio file of the same size and length, under another name"},
		{"one file renamed, another size", audio("a.mp3", 100), audio("Title.mp3", 101), ""},
		{"another size", two, audio("a.mp3", 100, "b.mp3", 201), ""},
		{"another name", two, audio("a.mp3", 100, "c.mp3", 200), ""},
		{"fewer files", two, audio("a.mp3", 100), ""},
		{"a file counted twice", audio("a.mp3", 100, "a.mp3", 100), audio("a.mp3", 100, "b.mp3", 100), ""},
		{"no audio", nil, nil, ""},
	} {
		if same, how := sameAudioFiles(tc.old, tc.fresh); how != tc.want || same != (tc.want != "") {
			t.Errorf("%s: %v %q, want %q", tc.name, same, how, tc.want)
		}
	}
}

// Progress has arrived when the new record says what the old one did. A
// finish the old record has no day for cannot be kept to the day, and is not
// held against the new one.
func TestSameProgress(t *testing.T) {
	t.Parallel()

	was := &abs.MediaProgress{CurrentTime: 1200, IsFinished: true, FinishedAt: 1711000000000}
	for name, tc := range map[string]struct {
		has  abs.MediaProgress
		same bool
	}{
		"as it was":              {abs.MediaProgress{CurrentTime: 1200.4, IsFinished: true, FinishedAt: 1711000000000}, true},
		"finished another day":   {abs.MediaProgress{CurrentTime: 1200, IsFinished: true, FinishedAt: 1790000000000}, false},
		"begun again":            {abs.MediaProgress{CurrentTime: 1200}, false},
		"somewhere else in it":   {abs.MediaProgress{CurrentTime: 30, IsFinished: true, FinishedAt: 1711000000000}, false},
		"off the shelf, wrongly": {abs.MediaProgress{CurrentTime: 1200, IsFinished: true, FinishedAt: 1711000000000, HideFromContinueListening: true}, false},
	} {
		if got := sameProgress(was, &tc.has); got != tc.same {
			t.Errorf("%s: sameProgress = %v, want %v", name, got, tc.same)
		}
	}
	if !sameProgress(&abs.MediaProgress{IsFinished: true}, &abs.MediaProgress{IsFinished: true, FinishedAt: 1790000000000}) {
		t.Error("a finish with no day recorded is never carried, as the server dates it on the way")
	}
}

// The old record wins on everything it says, an empty field too: a subtitle
// the scan read off the folder is not what the curator left there.
func TestMergeDetails(t *testing.T) {
	t.Parallel()

	old, fresh := &abs.Item{}, &abs.Item{}
	if upd, names := mergeDetails(old, fresh); upd != nil || names != nil {
		t.Errorf("two records that agree = %v %v", upd, names)
	}

	old.Media.Metadata = abs.Metadata{Title: "Stonefather", Narrators: []string{"Emily Janice Card"}, Authors: []abs.NameRef{{ID: "a1", Name: "Orson Scott Card"}}}
	fresh.Media.Metadata = abs.Metadata{Title: "Mithermages", Subtitle: "00.5 - Stonefather", Narrators: []string{"Emily Janice Card"}, Authors: []abs.NameRef{{ID: "a2", Name: "Orson Scott Card"}}, Genres: []string{"Fantasy"}}
	fresh.Media.Tags = []string{"scanned"}
	upd, names := mergeDetails(old, fresh)
	if !slices.Equal(names, []string{"title", "subtitle", "genres", "tags"}) {
		t.Fatalf("differing = %v", names)
	}
	sent, err := json.Marshal(upd)
	if err != nil {
		t.Fatal(err)
	}
	if string(sent) != `{"metadata":{"title":"Stonefather","subtitle":"","genres":[]},"tags":[]}` {
		t.Errorf("the update = %s, want the old title, and the subtitle, genres and tags emptied", sent)
	}
}
