package tools

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The hashes have to read one picture as one picture at any size and
// through a JPEG, and two pictures as two. The pictures are drawn from a
// formula over unit coordinates, so the same picture at 800 and 300 pixels
// is the same picture and not a resampling of it.

// artwork draws a cover of n pixels: a diagonal wash with a dark disc and a
// light bar, placed by the seed so that different seeds are different covers.
func artwork(n int, seed float64) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, n, n))
	for y := range n {
		for x := range n {
			u, v := float64(x)/float64(n), float64(y)/float64(n)
			shade := 60 + 120*(u*0.6+v*0.4)
			dx, dy := u-(0.3+0.4*seed), v-(0.35+0.3*seed)
			if dx*dx+dy*dy < 0.04 {
				shade = 20
			}
			if v > 0.7+0.15*seed && v < 0.8+0.15*seed && u > 0.1 && u < 0.6+0.3*seed {
				shade = 230
			}
			c := uint8(min(255, max(0, int(shade))))
			g := uint8(min(255, int(shade)+20*int(seed*3))) //nolint:gosec // clamped
			img.Set(x, y, color.RGBA{c, g, c, 255})
		}
	}
	return img
}

func encodePNG(t *testing.T, img image.Image) string {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func encodeJPEG(t *testing.T, img image.Image, quality int) string {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func TestCoverHashSamePictureAcrossSizes(t *testing.T) {
	t.Parallel()

	big, small := hashImage(artwork(800, 0)), hashImage(artwork(300, 0))
	if p, d := big.distance(small); !big.samePicture(small) {
		t.Errorf("the same picture at 800 and 300 pixels is %d/%d bits apart", p, d)
	}
	rough, err := jpeg.Decode(strings.NewReader(encodeJPEG(t, artwork(500, 0), 30)))
	if err != nil {
		t.Fatal(err)
	}
	if p, d := big.distance(hashImage(rough)); !big.samePicture(hashImage(rough)) {
		t.Errorf("the same picture through a rough JPEG is %d/%d bits apart", p, d)
	}
	other := hashImage(artwork(600, 0.9))
	if p, d := big.distance(other); big.samePicture(other) || p < 16 {
		t.Errorf("a different picture is only %d/%d bits apart", p, d)
	}
	if big != hashImage(artwork(800, 0)) {
		t.Error("hashing is not deterministic")
	}
}

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

// coverFixture is a library of three matched books against a store: one
// whose cover is the store's picture at a third of the size, one with no
// cover, and one whose cover is another picture. The store's image host is
// a test server whose "_SL500_" renditions have a full-size original.
func coverFixture(t *testing.T) (*fakeABS, *httptest.Server) {
	t.Helper()

	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/libraries/"+libID, `{"id":"`+libID+`","name":"Books","mediaType":"book","provider":"audible"}`)
	pictures := map[string]image.Image{"a": artwork(1500, 0), "b": artwork(1500, 0.9), "r": ribboned(1500, 0.5)}
	store := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// /img/a._SL500_.jpg is the rendition, /img/a.jpg the original
		name := strings.TrimPrefix(r.URL.Path, "/img/")
		base, _, _ := strings.Cut(name, ".")
		img, ok := pictures[base]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if strings.Contains(name, "_SL500_") {
			img = artwork(500, map[string]float64{"a": 0, "b": 0.9, "r": 0.5}[base])
			if base == "r" {
				img = ribboned(500, 0.5)
			}
		}
		_, _ = io.WriteString(w, encodeJPEG(t, img, 85))
	}))
	t.Cleanup(store.Close)

	book := func(id, title, asin, cover string) string {
		return item(id, title, `"asin":"`+asin+`"`, cover)
	}
	f.json("GET /api/libraries/"+libID+"/items", page(
		book("li_1", "Small Same", "B001", `"coverPath":"/1.jpg"`),
		book("li_2", "Bare", "B002", ""),
		book("li_3", "Other Art", "B003", `"coverPath":"/3.jpg"`),
		item("li_4", "Unmatched", "", `"coverPath":"/4.jpg"`),
		book("li_5", "Jacket", "B005", `"coverPath":"/5.jpg"`),
		book("li_6", "Clean Here", "B006", `"coverPath":"/6.jpg"`),
	))
	asins := map[string]string{"li_1": "B001", "li_2": "B002", "li_3": "B003", "li_5": "B005", "li_6": "B006"}
	covers := map[string]string{"li_1": `"coverPath":"/1.jpg"`, "li_2": "", "li_3": `"coverPath":"/3.jpg"`, "li_5": `"coverPath":"/5.jpg"`, "li_6": `"coverPath":"/6.jpg"`}
	for id, asin := range asins {
		f.json("GET /api/items/"+id, book(id, id, asin, covers[id]))
	}
	f.mux.HandleFunc("GET /api/search/books", func(w http.ResponseWriter, r *http.Request) {
		cover := map[string]string{"B001": "a", "B002": "a", "B003": "a", "B005": "a", "B006": "r"}[r.URL.Query().Get("title")]
		asin := r.URL.Query().Get("title")
		_, _ = io.WriteString(w, `[{"title":"x","asin":"`+asin+`","cover":"`+store.URL+`/img/`+cover+`._SL500_.jpg"}]`) //nolint:gosec // a test fixture echoing its own query
	})
	jacket := image.NewRGBA(image.Rect(0, 0, 600, 1000)) // a portrait scan, plain grey
	for y := range 1000 {
		for x := range 600 {
			jacket.Set(x, y, color.RGBA{120, 120, 120, 255})
		}
	}
	onDisk := map[string]image.Image{"li_1": artwork(500, 0), "li_3": artwork(900, 0.9), "li_4": artwork(400, 0.5), "li_5": jacket, "li_6": artwork(500, 0.5)}
	f.mux.HandleFunc("GET /api/items/{id}/cover", func(w http.ResponseWriter, r *http.Request) {
		img, ok := onDisk[r.PathValue("id")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("raw") == "1" {
			_, _ = io.WriteString(w, encodePNG(t, img))
			return
		}
		_, _ = io.WriteString(w, encodeJPEG(t, img, 80))
	})
	f.json("POST /api/items/{id}/cover", `{"success":true}`)
	return f, store
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

	out, err := call("item_cover_upgrade", map[string]any{"items": []any{"li_1", "li_2", "li_3"}, "providers": []any{"audible"}, "preview": true})
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

	out, err = call("item_cover_upgrade", map[string]any{"items": []any{"li_1", "li_3"}, "providers": []any{"audible"}})
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
	out, err = call("item_cover_upgrade", map[string]any{"items": []any{"li_3"}, "providers": []any{"audible"}, "any_picture": true})
	if err != nil || num(t, out["upgraded"]) != 1 {
		t.Errorf("any_picture: %v %v", out, err)
	}

	// the jacket: kept for its picture without square, taken with it
	out, err = call("item_cover_upgrade", map[string]any{"items": []any{"li_5"}, "providers": []any{"audible"}})
	if err != nil || num(t, out["upgraded"]) != 0 || str(t, list(t, out["items"])[0]["action"]) != "kept_picture" {
		t.Errorf("the jacket without square: %v %v", out, err)
	}
	out, err = call("item_cover_upgrade", map[string]any{"items": []any{"li_5"}, "providers": []any{"audible"}, "square": true})
	if err != nil || num(t, out["upgraded"]) != 1 {
		t.Errorf("the jacket with square: %v %v", out, err)
	}

	// a store copy with the ribbon never goes over a clean cover, whatever the flags
	out, err = call("item_cover_upgrade", map[string]any{"items": []any{"li_6"}, "providers": []any{"audible"}, "any_picture": true, "square": true})
	if err != nil || num(t, out["upgraded"]) != 0 || str(t, list(t, out["items"])[0]["action"]) != "kept_banner" {
		t.Errorf("the ribboned store copy: %v %v", out, err)
	}
	if got := f.requests("/api/items/li_6/cover"); len(got) > 0 && got[len(got)-1].Method == http.MethodPost {
		t.Error("the ribboned store copy was set")
	}
}

// ribboned is a cover with the Audible ribbon drawn across its bottom-right
// corner: a yellow band at 45 degrees with dark lettering on it.
func ribboned(n int, seed float64) image.Image {
	base := artwork(n, seed)
	img := image.NewRGBA(base.Bounds())
	for y := range n {
		for x := range n {
			c := base.At(x, y)
			s := float64(x+y) / float64(n)
			if s > 1.42 && s < 1.62 {
				c = color.RGBA{250, 230, 40, 255}
				if (x-y)%11 < 3 && s > 1.47 && s < 1.57 { // the lettering
					c = color.RGBA{30, 30, 30, 255}
				}
			}
			img.Set(x, y, c)
		}
	}
	return img
}

func TestFindAudibleBanner(t *testing.T) {
	t.Parallel()

	if _, found := findAudibleBanner(ribboned(600, 0)); !found {
		t.Error("the ribbon was not found")
	}
	if region, found := findAudibleBanner(ribboned(300, 0.9)); !found || region.Centre < 1.4 || region.Centre > 1.65 {
		t.Errorf("the ribbon on a small cover: found=%v at %v", found, region)
	}
	if region, found := findAudibleBanner(artwork(600, 0)); found {
		t.Errorf("plain art was read as a ribbon: %v", region)
	}
	lemon := image.NewRGBA(image.Rect(0, 0, 400, 400))
	for y := range 400 {
		for x := range 400 {
			lemon.Set(x, y, color.RGBA{250, 230, 40, 255})
		}
	}
	if region, found := findAudibleBanner(lemon); found {
		t.Errorf("an all-yellow cover was read as a ribbon: %v", region)
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
