package tools

import (
	"fmt"
	"image"
	"image/color"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

func TestFullSizeCoverURL(t *testing.T) {
	t.Parallel()

	for in, want := range map[string]string{
		"https://m.media-amazon.com/images/I/51abcDEF._SL500_.jpg": "https://m.media-amazon.com/images/I/51abcDEF.jpg",
		"https://m.media-amazon.com/images/I/51abcDEF._SX300_.png": "https://m.media-amazon.com/images/I/51abcDEF.png",
		"https://m.media-amazon.com/images/I/51abcDEF.jpg":         "https://m.media-amazon.com/images/I/51abcDEF.jpg",
		"https://covers.example/x/500.jpg":                         "https://covers.example/x/500.jpg",
	} {
		if got := fullSizeCoverURL(in); got != want {
			t.Errorf("fullSizeCoverURL(%q) = %q, want %q", in, got, want)
		}
	}
}

// coverBook is a book of a cover fixture: the asin it was matched to, none
// for a book never matched; the store's picture for that asin, by name; and
// the cover on disk. listed is whether the book says it has a cover, which it
// can say of a file that is gone.
type coverBook struct {
	id, title, asin, store string
	listed                 bool
	disk                   image.Image
}

// coverLibrary is an Audible library of these books and the store's image
// host beside it, a test server whose "_SL500_" renditions have a full-size
// original: a is one picture, b another, r one wearing the ribbon, and any
// other name a picture the store no longer has. The book coverFails sends
// its picture but fails the read of its size.
func coverLibrary(t *testing.T, books []coverBook) (*fakeABS, *httptest.Server) {
	t.Helper()

	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/libraries/"+libID, `{"id":"`+libID+`","name":"Books","mediaType":"book","provider":"audible"}`)
	store := fakeStore(t, func(name string, n int) image.Image {
		switch name {
		case "a":
			return artwork(n, 0)
		case "b":
			return artwork(n, 0.9)
		case "r":
			return ribboned(n, 0.5)
		}
		return nil
	})

	listing := make([]string, 0, len(books))
	sold := map[string]string{}
	onDisk := map[string]image.Image{}
	for _, b := range books {
		meta, media := "", ""
		if b.asin != "" {
			meta = `"asin":"` + b.asin + `"`
			sold[b.asin] = b.store
		}
		if b.listed {
			media = `"coverPath":"/` + b.id + `.jpg"`
		}
		listing = append(listing, item(b.id, b.title, meta, media))
		if b.asin != "" {
			f.json("GET /api/items/"+b.id, item(b.id, b.title, meta, media))
		}
		if b.disk != nil {
			onDisk[b.id] = b.disk
		}
	}
	f.json("GET /api/libraries/"+libID+"/items", page(listing...))
	f.mux.HandleFunc("GET /api/search/books", func(w http.ResponseWriter, r *http.Request) {
		asin := r.URL.Query().Get("title")
		_, _ = fmt.Fprintf(w, `[{"title":"x","asin":%q,"cover":"%s/img/%s._SL500_.jpg"}]`, asin, store.URL, sold[asin]) //nolint:gosec // a test fixture echoing its own query
	})
	f.mux.HandleFunc("GET /api/items/{id}/cover", func(w http.ResponseWriter, r *http.Request) {
		raw := r.URL.Query().Get("raw") == "1"
		img, ok := onDisk[r.PathValue("id")]
		switch {
		case r.PathValue("id") == coverFails && raw:
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		case r.PathValue("id") == coverFails:
			// the picture comes through: the store's copy is the same
			// picture, a little bigger
			img = artwork(300, 0)
		case !ok:
			http.NotFound(w, r)
			return
		}
		if raw {
			_, _ = io.WriteString(w, encodePNG(t, img))
			return
		}
		_, _ = io.WriteString(w, encodeJPEG(t, img, 85))
	})
	f.json("POST /api/items/{id}/cover", `{"success":true}`)
	return f, store
}

// coverFixture is a library against a store: a book whose cover is the
// store's picture at a third of the size, one with no cover, one whose cover
// is another picture, one never matched, one with a jacket scan for a cover,
// and one whose store copy wears the ribbon.
func coverFixture(t *testing.T) (*fakeABS, *httptest.Server) {
	t.Helper()

	jacket := image.NewRGBA(image.Rect(0, 0, 600, 1000)) // a portrait scan, plain grey
	for y := range 1000 {
		for x := range 600 {
			jacket.Set(x, y, color.RGBA{120, 120, 120, 255})
		}
	}
	return coverLibrary(t, []coverBook{
		{id: "li_1", title: "Small Same", asin: "B001", store: "a", listed: true, disk: artwork(500, 0)},
		{id: "li_2", title: "Bare", asin: "B002", store: "a"},
		{id: "li_3", title: "Other Art", asin: "B003", store: "a", listed: true, disk: artwork(900, 0.9)},
		{id: "li_4", title: "Unmatched", listed: true, disk: artwork(400, 0.5)},
		{id: "li_5", title: "Jacket", asin: "B005", store: "a", listed: true, disk: jacket},
		{id: "li_6", title: "Clean Here", asin: "B006", store: "r", listed: true, disk: artwork(500, 0.5)},
	})
}

func TestAuditCoversAgainstTheStore(t *testing.T) {
	t.Parallel()

	f, store := coverFixture(t)
	call := toolCaller(t, f)

	out, err := call("audit_covers", map[string]any{"store": true, "providers": []any{"audible"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := num(t, out["store_checked"]); got != 5 {
		t.Errorf("store_checked = %d, want the five matched books", got)
	}
	rows := map[string]map[string]any{}
	for _, row := range list(t, out["findings"]) {
		rows[str(t, row["id"])] = row
	}
	if r := rows["li_1"]; r == nil || str(t, r["problem"]) != "upgrade" || num(t, r["store_width"]) != 1500 || str(t, r["store_url"]) != store.URL+"/img/a.jpg" {
		t.Errorf("i1 = %v, want an upgrade to the 1500px original", r)
	}
	if r := rows["li_2"]; r == nil || str(t, r["problem"]) != "upgrade" || !strings.HasPrefix(str(t, r["why"]), "no cover") {
		t.Errorf("i2 = %v, want an upgrade from no cover", r)
	}
	if r := rows["li_3"]; r == nil || str(t, r["problem"]) != "differs" || str(t, r["distance"]) == "" || str(t, r["cover_url"]) == "" {
		t.Errorf("i3 = %v, want differs with both pictures", r)
	}
	if _, reported := rows["li_4"]; reported {
		t.Errorf("an unmatched book was compared: %v", rows["li_4"])
	}
	// the jacket is a ratio row twice: once from the library sweep, once
	// from the store with the square art attached, and never a differs row
	ratioRows := 0
	for _, row := range list(t, out["findings"]) {
		if str(t, row["id"]) == "li_5" {
			ratioRows++
			if str(t, row["problem"]) != "ratio" {
				t.Errorf("the jacket: %v", row)
			}
			if u := str(t, row["store_url"]); u != "" && u != store.URL+"/img/a.jpg" {
				t.Errorf("the jacket's store art: %v", row)
			}
		}
	}
	if ratioRows != 2 {
		t.Errorf("the jacket has %d rows, want 2", ratioRows)
	}
	// a clean cover whose store copy wears the ribbon is a differs row like any other picture
	if r := rows["li_6"]; r == nil || str(t, r["problem"]) != "differs" {
		t.Errorf("li_6 = %v, want differs", r)
	}
	counts, ok := out["counts"].(map[string]any)
	// the jacket's two rows are one ratio problem, counted once
	if !ok || num(t, counts["missing"]) != 1 || num(t, counts["upgrade"]) != 2 || num(t, counts["differs"]) != 2 || num(t, counts["ratio"]) != 1 {
		t.Errorf("counts = %v", counts)
	}
	if _, more := out["next_offset"]; more {
		t.Errorf("next_offset set with everything in one window: %v", out["next_offset"])
	}
}

func TestItemCoverUpgrade(t *testing.T) {
	t.Parallel()

	f, store := coverFixture(t)
	call := toolCaller(t, f)

	out, err := call("item_cover_upgrade", map[string]any{"items": []any{"li_1", "li_2", "li_3"}, "providers": []any{"audible"}})
	if err != nil {
		t.Fatal(err)
	}
	actions := map[string]string{}
	for _, row := range list(t, out["items"]) {
		actions[str(t, row["id"])] = str(t, row["action"])
	}
	if want := map[string]string{"li_1": "would_upgrade", "li_2": "would_upgrade", "li_3": "kept_picture"}; actions["li_1"] != want["li_1"] || actions["li_2"] != want["li_2"] || actions["li_3"] != want["li_3"] {
		t.Errorf("preview actions = %v, want %v", actions, want)
	}
	if got := f.requests("/api/items/li_1/cover"); len(got) > 0 && got[len(got)-1].Method == http.MethodPost {
		t.Error("preview set a cover")
	}

	out, err = call("item_cover_upgrade", map[string]any{"confirm": true, "items": []any{"li_1", "li_3"}, "providers": []any{"audible"}})
	if err != nil {
		t.Fatal(err)
	}
	if num(t, out["upgraded"]) != 1 {
		t.Errorf("upgraded = %v, want 1: the same picture bigger, not the other picture", out)
	}
	posted := 0
	for _, req := range f.requests("/api/items/li_1/cover") {
		if req.Method == http.MethodPost && strings.Contains(req.Body, store.URL+"/img/a.jpg") {
			posted++
		}
	}
	if posted != 1 {
		t.Errorf("the full-size url was posted %d times: %v (want %s)", posted, f.requests("/api/items/li_1/cover"), store.URL)
	}
	if got := f.requests("/api/items/li_3/cover"); len(got) > 0 && got[len(got)-1].Method == http.MethodPost {
		t.Error("the other picture was set without any_picture")
	}

	// Other Art is 900px against the store's 1500px, under the factor: with
	// any_picture the size rule does not apply, since it is another picture
	out, err = call("item_cover_upgrade", map[string]any{"confirm": true, "items": []any{"li_3"}, "providers": []any{"audible"}, "any_picture": true})
	if err != nil || num(t, out["upgraded"]) != 1 {
		t.Errorf("any_picture: %v %v", out, err)
	}

	// the jacket: kept for its picture without square, taken with it
	out, err = call("item_cover_upgrade", map[string]any{"confirm": true, "items": []any{"li_5"}, "providers": []any{"audible"}})
	if err != nil || num(t, out["upgraded"]) != 0 || str(t, list(t, out["items"])[0]["action"]) != "kept_picture" {
		t.Errorf("the jacket without square: %v %v", out, err)
	}
	out, err = call("item_cover_upgrade", map[string]any{"confirm": true, "items": []any{"li_5"}, "providers": []any{"audible"}, "square": true})
	if err != nil || num(t, out["upgraded"]) != 1 {
		t.Errorf("the jacket with square: %v %v", out, err)
	}

	// a store copy with the ribbon never goes over a clean cover, whatever the flags
	out, err = call("item_cover_upgrade", map[string]any{"confirm": true, "items": []any{"li_6"}, "providers": []any{"audible"}, "any_picture": true, "square": true})
	if err != nil || num(t, out["upgraded"]) != 0 || str(t, list(t, out["items"])[0]["action"]) != "kept_banner" {
		t.Errorf("the ribboned store copy: %v %v", out, err)
	}
	if got := f.requests("/api/items/li_6/cover"); len(got) > 0 && got[len(got)-1].Method == http.MethodPost {
		t.Error("the ribboned store copy was set")
	}
}

func TestAuditCoversFindsTheBanner(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/libraries/"+libID+"/items", page(
		item("i1", "Clean", "", `"coverPath":"/1.jpg"`),
		item("i2", "Ribboned", "", `"coverPath":"/2.jpg"`),
	))
	f.mux.HandleFunc("GET /api/items/{id}/cover", func(w http.ResponseWriter, r *http.Request) {
		img := map[string]image.Image{"i1": artwork(600, 0), "i2": ribboned(600, 0.3)}[r.PathValue("id")]
		if r.URL.Query().Get("raw") == "1" {
			_, _ = io.WriteString(w, encodePNG(t, img))
			return
		}
		_, _ = io.WriteString(w, encodeJPEG(t, img, 85))
	})
	call := toolCaller(t, f)

	out, err := call("audit_covers", map[string]any{"banner": true})
	if err != nil {
		t.Fatal(err)
	}
	counts, ok := out["counts"].(map[string]any)
	if !ok || num(t, counts["banner"]) != 1 {
		t.Fatalf("counts = %v, want one banner", counts)
	}
	rows := list(t, out["findings"])
	if len(rows) != 1 || str(t, rows[0]["id"]) != "i2" || str(t, rows[0]["problem"]) != "banner" {
		t.Errorf("findings = %v, want the ribboned cover alone", rows)
	}

	plain, err := call("audit_covers", nil)
	if err != nil || num(t, plain["total_findings"]) != 0 {
		t.Errorf("without banner: %v %v", plain, err)
	}
}

// storeFixture is a library of three matched books: one whose cover is the
// store's picture too small, one whose store image is gone, and one whose
// own cover file is gone.
func storeFixture(t *testing.T) *fakeABS {
	t.Helper()

	f, _ := coverLibrary(t, []coverBook{
		{id: "li_1", title: "Small", asin: "B001", store: "a", listed: true, disk: artwork(300, 0)},
		{id: "li_2", title: "Store Gone", asin: "B002", store: "gone", listed: true, disk: artwork(500, 0.5)},
		{id: "li_3", title: "File Gone", asin: "B003", store: "a", listed: true},
	})
	return f
}

// One book's store or cover failing stopped the whole window, and it could
// never be got past; every later window repeated the library-wide rows.
func TestAuditCoversStoreCarriesOnPastABook(t *testing.T) {
	t.Parallel()

	f := storeFixture(t)
	call := toolCaller(t, f)

	out, err := call("audit_covers", map[string]any{"store": true, "providers": []any{"audible"}, "library": "Books"})
	if err != nil {
		t.Fatal(err)
	}
	rows := map[string][]string{}
	for _, row := range list(t, out["findings"]) {
		rows[str(t, row["id"])] = append(rows[str(t, row["id"])], str(t, row["problem"]))
	}
	// the gone file is missing, as a book with no cover is, and the store's
	// copy an upgrade over it
	if !slices.Contains(rows["li_1"], "upgrade") || !slices.Equal(rows["li_2"], []string{"skipped"}) || !slices.Equal(rows["li_3"], []string{"missing", "upgrade"}) {
		t.Errorf("rows = %v, want an upgrade, a skipped store, and the gone file missing with an upgrade over it", rows)
	}
	if num(t, out["items_scanned"]) != 3 || num(t, out["total_findings"]) != 4 {
		t.Errorf("items_scanned %v total_findings %v, want 3 books and small, missing and two upgrades", out["items_scanned"], out["total_findings"])
	}

	// a book at a time: the library-wide rows come with the first only
	first, err := call("audit_covers", map[string]any{"store": true, "providers": []any{"audible"}, "library": "Books", "limit": 1})
	if err != nil {
		t.Fatal(err)
	}
	second, err := call("audit_covers", map[string]any{"store": true, "providers": []any{"audible"}, "library": "Books", "limit": 1, "offset": 1})
	if err != nil {
		t.Fatal(err)
	}
	problems := func(out map[string]any) []string {
		rows := list(t, out["findings"])
		got := make([]string, 0, len(rows))
		for _, row := range rows {
			got = append(got, str(t, row["id"])+" "+str(t, row["problem"]))
		}
		return got
	}
	if got := problems(first); !slices.Equal(got, []string{"li_1 small", "li_1 upgrade"}) || num(t, first["next_offset"]) != 1 {
		t.Errorf("offset 0 = %v, next_offset %v", got, first["next_offset"])
	}
	if got := problems(second); !slices.Equal(got, []string{"li_2 skipped"}) || num(t, second["total_findings"]) != 0 || num(t, second["items_scanned"]) != 1 || num(t, second["next_offset"]) != 2 {
		t.Errorf("offset 1 = %v %v, want the store row alone", got, second)
	}

	// the one-library rule is checked before anything is swept
	two := newFakeABS(t)
	two.json("GET /api/libraries", `{"libraries":[{"id":"`+libID+`","name":"A","mediaType":"book"},{"id":"`+otherLibID+`","name":"B","mediaType":"book"}]}`)
	if _, err := toolCaller(t, two)("audit_covers", map[string]any{"store": true}); err == nil || len(two.requests("/api/libraries/"+libID+"/items")) != 0 {
		t.Errorf("store over two libraries: err %v, requests %v", err, two.requests("/api/libraries/"+libID+"/items"))
	}
}

// A batch that fails part way has already set covers: those are reported,
// with the failure and what was never tried, rather than only an error.
func TestItemCoverUpgradeReportsWhatItDidBeforeAFailure(t *testing.T) {
	t.Parallel()

	f := storeFixture(t)
	call := toolCaller(t, f)

	out, err := call("item_cover_upgrade", map[string]any{"confirm": true, "items": []any{"li_1", "li_3", "li_2", "li_1"}, "providers": []any{"audible"}})
	if err != nil {
		t.Fatal(err)
	}
	rows := list(t, out["items"])
	actions := make([]string, 0, len(rows))
	for _, row := range rows {
		actions = append(actions, str(t, row["id"])+" "+str(t, row["action"]))
	}
	if !slices.Equal(actions, []string{"li_1 upgraded", "li_3 upgraded", "li_2 failed"}) || num(t, out["upgraded"]) != 2 {
		t.Errorf("actions = %v, want two upgrades then the failure", actions)
	}
	if str(t, rows[2]["error"]) == "" {
		t.Errorf("the failed row says nothing: %v", rows[2])
	}
	if nt, ok := out["not_tried"].([]any); !ok || len(nt) != 1 || nt[0] != "li_1" {
		t.Errorf("not_tried = %v, want the last li_1", out["not_tried"])
	}
}

// coverFails is a book whose cover the fake server fails to send.
const coverFails = "li_fail"

// A cover the server fails to send is no clean cover, and a cover file that
// is gone is a missing one: the library sweep says both, book by book, and
// audit_all says how many covers it could not judge.
func TestCoversTheServerFailsToSendAreSaid(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/libraries/"+libID+"/items", page(
		item("li_ok", "Fine", "", `"coverPath":"/ok.jpg"`),
		item("li_gone", "File Gone", "", `"coverPath":"/gone.jpg"`),
		item(coverFails, "Fails", "", `"coverPath":"/fail.jpg"`),
	))
	f.mux.HandleFunc("GET /api/items/{id}/cover", func(w http.ResponseWriter, r *http.Request) {
		switch r.PathValue("id") {
		case "li_ok":
			_, _ = io.WriteString(w, encodeJPEG(t, artwork(600, 0), 85))
		case coverFails:
			http.Error(w, "boom", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	})
	call := toolCaller(t, f)

	out, err := call("audit_covers", map[string]any{"library": "Books"})
	if err != nil {
		t.Fatal(err)
	}
	rows := map[string]map[string]any{}
	for _, row := range list(t, out["findings"]) {
		rows[str(t, row["id"])] = row
	}
	if r := rows["li_gone"]; r == nil || str(t, r["problem"]) != "missing" || !strings.Contains(str(t, r["why"]), "gone") {
		t.Errorf("the gone file = %v, want missing", r)
	}
	if r := rows[coverFails]; r == nil || str(t, r["problem"]) != "skipped" || !strings.Contains(str(t, r["why"]), "500") {
		t.Errorf("the failed cover = %v, want skipped with the server's 500", r)
	}
	if num(t, out["skipped"]) != 1 || num(t, out["covers_checked"]) != 1 {
		t.Errorf("skipped %v checked %v, want one each", out["skipped"], out["covers_checked"])
	}

	// audit_all counts findings only, so it says the cover it could not
	// judge, or an outage would read as clean covers. Its deep audits read
	// every book's files and ask the store about each
	f.json("POST /api/items/batch/get", `{"libraryItems":[`+item("li_ok", "Fine", "", "")+`,`+item("li_gone", "File Gone", "", "")+`,`+item(coverFails, "Fails", "", "")+`]}`)
	f.json("GET /api/libraries/"+libID+"/authors", `{"results":[],"total":0}`)
	f.json("GET /api/libraries/"+libID+"/series", `{"results":[],"total":0}`)
	f.json("GET /api/search/books", `[]`)
	f.json("GET /api/libraries/"+libID, `{"id":"`+libID+`","name":"Books","mediaType":"book","provider":"audible"}`)
	all, err := call("audit_all", map[string]any{"library": "Books", "deep": true})
	if err != nil {
		t.Fatal(err)
	}
	partial := list(t, all["partial"])
	if !slices.ContainsFunc(partial, func(row map[string]any) bool {
		return row["audit"] == "audit_covers" && strings.Contains(str(t, row["reason"]), "1 covers could not be read")
	}) {
		t.Errorf("audit_all partial = %v, want audit_covers saying one cover was not judged", partial)
	}
}

// A cover whose size the server fails to give is not replaced as if it had
// none: the store's copy may be smaller.
func TestACoverUpgradeWithTheSizeUnreadWritesNothing(t *testing.T) {
	t.Parallel()

	f := storeFixture(t)
	f.json("GET /api/items/"+coverFails, item(coverFails, "Fails", `"asin":"B001"`, `"coverPath":"/fail.jpg"`))
	call := toolCaller(t, f)

	_, err := call("item_cover_upgrade", map[string]any{"confirm": true, "items": []any{coverFails}, "providers": []any{"audible"}})
	wantErr(t, "an upgrade over a cover whose size failed", err, "size", "500")
	if got := f.requests("/api/items/" + coverFails + "/cover"); slices.ContainsFunc(got, func(r request) bool { return r.Method == http.MethodPost }) {
		t.Errorf("the cover was replaced: %v", got)
	}

	// and the store comparison lists the book as skipped, saying why,
	// rather than dropping its size and comparing on
	f.json("GET /api/libraries/"+libID+"/items", page(item(coverFails, "Fails", `"asin":"B001"`, `"coverPath":"/fail.jpg"`)))
	out, err := call("audit_covers", map[string]any{"store": true, "providers": []any{"audible"}, "library": "Books"})
	if err != nil {
		t.Fatal(err)
	}
	var skipped bool
	for _, row := range list(t, out["findings"]) {
		why := str(t, row["why"])
		if str(t, row["id"]) == coverFails && str(t, row["problem"]) == "skipped" && strings.Contains(why, "the cover's size") && strings.Contains(why, "500") {
			skipped = true
		}
	}
	if !skipped {
		t.Errorf("findings = %v, want the store's row for the book skipped over its size, with the server's 500", out["findings"])
	}
}

// A cover in a format Go cannot read is the file's problem: the sweep skips
// it, saying so, and goes on.
func TestAnUndecodableCoverIsSkippedNotAnError(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/libraries/"+libID+"/items", page(item("li_webp", "Webp", "", `"coverPath":"/c.webp"`)))
	f.mux.HandleFunc("GET /api/items/{id}/cover", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "RIFF....WEBPVP8 not an image Go reads")
	})
	call := toolCaller(t, f)

	out, err := call("audit_covers", map[string]any{"library": "Books"})
	if err != nil {
		t.Fatal(err)
	}
	rows := list(t, out["findings"])
	if num(t, out["skipped"]) != 1 || len(rows) != 1 || !strings.Contains(str(t, rows[0]["why"]), "cannot be decoded") {
		t.Errorf("a webp cover = %v, want one skipped row saying it cannot be decoded", out)
	}
}

// The cover audit measures the file on disk (raw=1, not the server's 400-wide
// cache), and does not ask for a cover the listing already says is absent.
func TestAuditCoversMeasuresTheFile(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/libraries/"+libID+"/items", page(
		item("i1", "Square", "", `"coverPath":"/1.jpg"`),
		item("i2", "Tall", "", `"coverPath":"/2.jpg"`),
		item("i3", "Tiny", "", `"coverPath":"/3.jpg"`),
		item("i4", "Bare", "", ""),
	))
	covers := map[string]string{"i1": pngOf(t, 600, 600), "i2": pngOf(t, 300, 600), "i3": pngOf(t, 200, 200)}
	f.mux.HandleFunc("GET /api/items/{id}/cover", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("raw") != "1" {
			http.Error(w, "the cache, not the file", http.StatusBadRequest)
			return
		}
		_, _ = io.WriteString(w, covers[r.PathValue("id")])
	})
	call := toolCaller(t, f)

	out, err := call("audit_covers", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := num(t, out["covers_checked"]); got != 3 {
		t.Errorf("covers_checked = %d, want 3", got)
	}
	if _, skipped := out["skipped"]; skipped {
		t.Errorf("skipped = %v, want none: a missing cover is a finding, not a skip", out["skipped"])
	}
	if got := f.requests("/api/items/i4/cover"); len(got) != 0 {
		t.Errorf("a coverless item was fetched: %v", got)
	}

	why := map[string]string{}
	problem := map[string]string{}
	for _, row := range list(t, out["findings"]) {
		why[str(t, row["id"])] = str(t, row["why"])
		problem[str(t, row["id"])] = str(t, row["problem"])
	}
	if problem["i4"] != "missing" {
		t.Errorf("the coverless item: %q %q", problem["i4"], why["i4"])
	}
	if problem["i2"] != "ratio" || problem["i3"] != "small" {
		t.Errorf("problems = %v", problem)
	}
	if !strings.HasPrefix(why["i2"], "not square") {
		t.Errorf("the tall cover: %q", why["i2"])
	}
	if !strings.HasPrefix(why["i3"], "only 200px") {
		t.Errorf("the small cover: %q", why["i3"])
	}
	if _, flagged := why["i1"]; flagged {
		t.Errorf("the square cover was flagged: %q", why["i1"])
	}
}
