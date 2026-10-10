//go:build integration

package acceptance

import (
	"io"
	"maps"
	"net/http"
	"net/http/cookiejar"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	acc "github.com/katbyte/go-kt/mcp/acctest"
)

// fetch gets a url as a listener would, with no account: the status and the
// start of the body.
func fetch(t *testing.T, url string) (status int, body string) {
	t.Helper()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = res.Body.Close() }()
	head, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
	return res.StatusCode, string(head)
}

// download opens a link as a visitor's browser does and then asks for the
// book's files: the server gives the download only to a visitor it has given
// the link's page, whom it knows by a cookie.
func download(t *testing.T, server, slug string) (status int, body string) {
	t.Helper()

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	visitor := &http.Client{Jar: jar}
	for _, path := range []string{"/public/share/" + slug, "/public/share/" + slug + "/download"} {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, server+path, http.NoBody)
		if err != nil {
			t.Fatal(err)
		}
		res, err := visitor.Do(req)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		head, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		_ = res.Body.Close()
		status, body = res.StatusCode, string(head)
	}
	return status, body
}

// Feeds of a book, a series and a collection: each opens at a url a podcast
// player can read with no account, is listed, is handed back rather than
// opened twice, and closes.
func TestFeedEdit(t *testing.T) {
	server := strings.TrimRight(os.Getenv("ABS_SERVER"), "/")

	col := suite.Call(t, "collection_create", map[string]any{"name": "Zzyzx Feed Collection", "library": "Fiction", "items": []any{"Foundation"}})
	t.Cleanup(func() { _, _ = suite.Invoke("collection_delete", map[string]any{"collection": acc.Str(col["id"])}) })

	for _, target := range []map[string]any{
		{"item": "Foundation", "library": "Fiction"},
		{"series": "Foundation", "library": "Fiction"},
		{"collection": "Zzyzx Feed Collection"},
	} {
		out := suite.Call(t, "feed_edit", target)
		url := acc.Str(out["url"])
		if !acc.BoolOf(out["opened"]) || !strings.HasPrefix(url, server+"/feed/") {
			t.Fatalf("feed_edit %v = %v", target, out)
		}
		t.Cleanup(func() {
			args := maps.Clone(target)
			args["close"] = true
			_, _ = suite.Invoke("feed_edit", args)
		})
		if status, body := fetch(t, url); status != http.StatusOK || !strings.Contains(body, "<rss") {
			t.Errorf("%s answers %d: %.200s", url, status, body)
		}
		if again := suite.Call(t, "feed_edit", target); !acc.BoolOf(again["already"]) || again["url"] != url {
			t.Errorf("opening it again = %v, want the one open", again)
		}
		if listed := valuesIn(t, suite.Call(t, "feed_list", nil)["feeds"], "feeds", "url"); !slices.Contains(listed, url) {
			t.Errorf("feed_list = %v, without %s", listed, url)
		}
	}

	closeArgs := map[string]any{"item": "Foundation", "library": "Fiction", "close": true}
	if out := suite.Call(t, "feed_edit", closeArgs); !acc.BoolOf(out["closed"]) || acc.BoolOf(out["already"]) {
		t.Errorf("close = %v", out)
	}
	for _, f := range acc.Rows(t, suite.Call(t, "feed_list", nil)["feeds"], "feeds") {
		if f["for"] == "item" && f["title"] == "Foundation" {
			t.Errorf("the book's feed is still listed: %v", f)
		}
	}
	if out := suite.Call(t, "feed_edit", closeArgs); !acc.BoolOf(out["already"]) {
		t.Errorf("closing it again = %v, want already", out)
	}
}

// A link to one book: a page anyone holding it can play, named so it cannot
// be guessed, handed back rather than made twice, and closed.
func TestFeedEditLink(t *testing.T) {
	server := strings.TrimRight(os.Getenv("ABS_SERVER"), "/")
	book := map[string]any{"item": "Leviathan Wakes", "library": "Fiction", "link": true}

	out := suite.Call(t, "feed_edit", book)
	url := acc.Str(out["url"])
	slug := strings.TrimPrefix(url, server+"/share/")
	if !acc.BoolOf(out["opened"]) || len(slug) != 10 || slug == url {
		t.Fatalf("feed_edit link = %v", out)
	}
	t.Cleanup(func() {
		_, _ = suite.Invoke("feed_edit", map[string]any{"item": "Leviathan Wakes", "library": "Fiction", "link": true, "close": true})
	})
	if status, body := fetch(t, server+"/public/share/"+slug); status != http.StatusOK || !strings.Contains(body, `"slug":"`+slug+`"`) {
		t.Errorf("the link answers %d: %.200s", status, body)
	}
	if again := suite.Call(t, "feed_edit", book); !acc.BoolOf(again["already"]) || again["url"] != url {
		t.Errorf("a second link = %v, want the one open", again)
	}

	if out := suite.Call(t, "feed_edit", map[string]any{"item": "Leviathan Wakes", "library": "Fiction", "link": true, "close": true}); !acc.BoolOf(out["closed"]) {
		t.Errorf("close = %v", out)
	}
	if status, _ := fetch(t, server+"/public/share/"+slug); status != http.StatusNotFound {
		t.Errorf("the closed link answers %d", status)
	}
	if msg := suite.CallErr(t, "feed_edit", map[string]any{"item": "Behind the Bastards", "library": "Podcasts", "link": true}); !strings.Contains(msg, "podcast") {
		t.Errorf("a link to a podcast: %s", msg)
	}
}

// A link that closes on its own and lets whoever holds it download the
// book: the server keeps both as asked, and the download answers. Opened
// again with other settings it is handed back as it is. A link opened with
// neither never closes and refuses the download.
func TestFeedEditLinkExpiresAndDownloads(t *testing.T) {
	server := strings.TrimRight(os.Getenv("ABS_SERVER"), "/")
	admin := adminClient(t)
	book := map[string]any{"item": "Abaddon's Gate", "library": "Fiction", "link": true}
	id := itemID(t, "Fiction", "Abaddon's Gate")
	t.Cleanup(func() { _, _ = suite.Invoke("feed_edit", withArgs(book, map[string]any{"close": true})) })

	asked := time.Now()
	out := suite.Call(t, "feed_edit", withArgs(book, map[string]any{"slug": "zzyzx-lent", "expires_days": 2, "downloadable": true}))
	if !acc.BoolOf(out["opened"]) || out["url"] != server+"/share/zzyzx-lent" {
		t.Fatalf("feed_edit link = %v", out)
	}
	expires, err := time.Parse(time.RFC3339, acc.Str(out["expires"]))
	if err != nil || expires.Before(asked.Add(48*time.Hour-time.Minute)) || expires.After(time.Now().Add(48*time.Hour+time.Minute)) {
		t.Errorf("expires = %v (%v), want two days from now", out["expires"], err)
	}
	share, err := admin.ItemShare(ctx, id)
	if err != nil || share == nil {
		t.Fatalf("the book's link, as the server holds it: %v %v", share, err)
	}
	if share.Slug != "zzyzx-lent" || !share.IsDownloadable || share.ExpiresAt != acc.Str(out["expires"]) {
		t.Errorf("the server holds %+v, want it downloadable and closing %v", share, out["expires"])
	}
	if status, body := download(t, server, "zzyzx-lent"); status != http.StatusOK {
		t.Errorf("the download answers %d: %.200s", status, body)
	}

	// one link to a book: asked for again, it is the one open, as it is
	again := suite.Call(t, "feed_edit", withArgs(book, map[string]any{"slug": "zzyzx-other", "expires_days": 9}))
	if !acc.BoolOf(again["already"]) || again["url"] != out["url"] || again["expires"] != out["expires"] {
		t.Errorf("a second link = %v, want the one open", again)
	}
	suite.Call(t, "feed_edit", withArgs(book, map[string]any{"close": true}))

	out = suite.Call(t, "feed_edit", withArgs(book, map[string]any{"slug": "zzyzx-kept"}))
	if !acc.BoolOf(out["opened"]) || out["expires"] != nil {
		t.Fatalf("a link with no end = %v", out)
	}
	if share, err = admin.ItemShare(ctx, id); err != nil || share == nil || share.IsDownloadable || share.ExpiresAt != "" {
		t.Errorf("the server holds %+v (%v), want it neither downloadable nor closing", share, err)
	}
	if status, body := download(t, server, "zzyzx-kept"); status != http.StatusForbidden {
		t.Errorf("the download of a link that allows none answers %d: %.200s", status, body)
	}

	for _, c := range []struct {
		args map[string]any
		says string
	}{
		{map[string]any{"item": "Foundation", "library": "Fiction", "expires_days": 2}, "pass link"},
		{map[string]any{"item": "Foundation", "library": "Fiction", "downloadable": true}, "pass link"},
		{map[string]any{"item": "Foundation", "library": "Fiction", "link": true, "expires_days": -1}, "in the past"},
		{map[string]any{"item": "Foundation", "library": "Fiction", "link": true, "close": true, "downloadable": true}, "only what to close"},
	} {
		if msg := suite.CallErr(t, "feed_edit", c.args); !strings.Contains(msg, c.says) {
			t.Errorf("feed_edit %v: %s", c.args, msg)
		}
	}
}
