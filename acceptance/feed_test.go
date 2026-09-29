//go:build integration

package acceptance

import (
	"io"
	"maps"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
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

// Feeds of a book, a series and a collection: each opens at a url a podcast
// player can read with no account, is listed, is handed back rather than
// opened twice, and closes.
func TestFeedEdit(t *testing.T) {
	server := strings.TrimRight(os.Getenv("ABS_SERVER"), "/")

	col := call(t, "collection_create", map[string]any{"name": "Zzyzx Feed Collection", "library": "Fiction", "items": []any{"Foundation"}})
	t.Cleanup(func() { _, _ = invoke("collection_delete", map[string]any{"collection": text(col["id"])}) })

	for _, target := range []map[string]any{
		{"item": "Foundation", "library": "Fiction"},
		{"series": "Foundation", "library": "Fiction"},
		{"collection": "Zzyzx Feed Collection"},
	} {
		out := call(t, "feed_edit", target)
		url := text(out["url"])
		if !truth(out["opened"]) || !strings.HasPrefix(url, server+"/feed/") {
			t.Fatalf("feed_edit %v = %v", target, out)
		}
		t.Cleanup(func() {
			args := maps.Clone(target)
			args["close"] = true
			_, _ = invoke("feed_edit", args)
		})
		if status, body := fetch(t, url); status != http.StatusOK || !strings.Contains(body, "<rss") {
			t.Errorf("%s answers %d: %.200s", url, status, body)
		}
		if again := call(t, "feed_edit", target); !truth(again["already"]) || again["url"] != url {
			t.Errorf("opening it again = %v, want the one open", again)
		}
		if listed := valuesIn(t, call(t, "feed_list", nil)["feeds"], "feeds", "url"); !slices.Contains(listed, url) {
			t.Errorf("feed_list = %v, without %s", listed, url)
		}
	}

	closeArgs := map[string]any{"item": "Foundation", "library": "Fiction", "close": true}
	if out := call(t, "feed_edit", closeArgs); !truth(out["closed"]) || truth(out["already"]) {
		t.Errorf("close = %v", out)
	}
	for _, f := range rows(t, call(t, "feed_list", nil)["feeds"], "feeds") {
		if f["for"] == "item" && f["title"] == "Foundation" {
			t.Errorf("the book's feed is still listed: %v", f)
		}
	}
	if out := call(t, "feed_edit", closeArgs); !truth(out["already"]) {
		t.Errorf("closing it again = %v, want already", out)
	}
}

// A link to one book: a page anyone holding it can play, named so it cannot
// be guessed, handed back rather than made twice, and closed.
func TestFeedEditLink(t *testing.T) {
	server := strings.TrimRight(os.Getenv("ABS_SERVER"), "/")
	book := map[string]any{"item": "Leviathan Wakes", "library": "Fiction", "link": true}

	out := call(t, "feed_edit", book)
	url := text(out["url"])
	slug := strings.TrimPrefix(url, server+"/share/")
	if !truth(out["opened"]) || len(slug) != 10 || slug == url {
		t.Fatalf("feed_edit link = %v", out)
	}
	t.Cleanup(func() {
		_, _ = invoke("feed_edit", map[string]any{"item": "Leviathan Wakes", "library": "Fiction", "link": true, "close": true})
	})
	if status, body := fetch(t, server+"/public/share/"+slug); status != http.StatusOK || !strings.Contains(body, `"slug":"`+slug+`"`) {
		t.Errorf("the link answers %d: %.200s", status, body)
	}
	if again := call(t, "feed_edit", book); !truth(again["already"]) || again["url"] != url {
		t.Errorf("a second link = %v, want the one open", again)
	}

	if out := call(t, "feed_edit", map[string]any{"item": "Leviathan Wakes", "library": "Fiction", "link": true, "close": true}); !truth(out["closed"]) {
		t.Errorf("close = %v", out)
	}
	if status, _ := fetch(t, server+"/public/share/"+slug); status != http.StatusNotFound {
		t.Errorf("the closed link answers %d", status)
	}
	if msg := callErr(t, "feed_edit", map[string]any{"item": "Behind the Bastards", "library": "Podcasts", "link": true}); !strings.Contains(msg, "podcast") {
		t.Errorf("a link to a podcast: %s", msg)
	}
}
