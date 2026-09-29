package tools

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// feed_list reads each feed's url from where the list puts it, inside meta.
func TestFeedList(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	// the server keeps the url as a path, and the list gives it inside meta
	f.json("GET /api/feeds", `{"feeds":[{"id":"fd1","slug":"dune","entityType":"libraryItem","entityId":"`+itemID+`","meta":{"title":"Dune","feedUrl":"/feed/dune"}},`+
		`{"id":"fd2","slug":"saga","entityType":"series","entityId":"`+seriesA+`","meta":{"title":"Saga","feedUrl":"/feed/saga"}}]}`)
	call := toolCaller(t, f)

	out, err := call("feed_list", nil)
	if err != nil {
		t.Fatal(err)
	}
	rows := list(t, out["feeds"])
	if len(rows) != 2 || rows[0]["for"] != "item" || rows[0]["url"] != f.srv.URL+"/feed/dune" || rows[1]["for"] != "series" {
		t.Errorf("feeds = %v", rows)
	}
}

// feed_edit opens a feed named by the thing's own id on the server's url,
// hands back one already open rather than opening a second, and closes by
// what the feed is for.
func TestFeedEdit(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /api/items/"+itemID, item(itemID, "Dune", "", ""))
	f.json("GET /api/feeds", `{"feeds":[]}`)
	f.json("POST /api/feeds/item/"+itemID+"/open", `{"feed":{"id":"fd1","entityType":"libraryItem","entityId":"`+itemID+`","feedUrl":"/feed/`+itemID+`"}}`)
	call := toolCaller(t, f)

	// the url answered is the server's address and the path it keeps
	out, err := call("feed_edit", map[string]any{"item": itemID})
	if err != nil {
		t.Fatal(err)
	}
	if !isTrue(out["opened"]) || out["url"] != f.srv.URL+"/feed/"+itemID || out["for"] != "Dune" {
		t.Errorf("open = %v", out)
	}
	sent := f.requests("/api/feeds/item/" + itemID + "/open")
	var body map[string]string
	if len(sent) != 1 || json.Unmarshal([]byte(sent[0].Body), &body) != nil || body["slug"] != itemID || !strings.HasPrefix(body["serverAddress"], "http://127.0.0.1:") {
		t.Errorf("sent %v", sent)
	}

	f.json("GET /api/feeds", `{"feeds":[{"id":"fd1","slug":"`+itemID+`","entityType":"libraryItem","entityId":"`+itemID+`","meta":{"title":"Dune","feedUrl":"/feed/x"}}]}`)
	out, err = call("feed_edit", map[string]any{"item": itemID, "address": "https://abs.example/"})
	if err != nil {
		t.Fatal(err)
	}
	if !isTrue(out["already"]) || out["url"] != "https://abs.example/feed/x" || len(f.requests("/api/feeds/item/"+itemID+"/open")) != 1 {
		t.Errorf("a second open = %v", out)
	}

	f.json("POST /api/feeds/fd1/close", `OK`)
	out, err = call("feed_edit", map[string]any{"item": itemID, "close": true})
	if err != nil {
		t.Fatal(err)
	}
	if !isTrue(out["closed"]) || isTrue(out["already"]) || len(f.requests("/api/feeds/fd1/close")) != 1 {
		t.Errorf("close = %v", out)
	}
}

// link makes a public page for one book, named by random letters so it
// cannot be guessed, hands back the one it has, and closes it.
func TestFeedEditLink(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	book := `{"id":"` + itemID + `","libraryId":"` + libID + `","mediaType":"book","media":{"id":"m1","metadata":{"title":"Dune"}}}`
	f.inTurn("GET /api/items/"+itemID, book, book, strings.TrimSuffix(book, "}")+`,"mediaItemShare":{"id":"sh1","slug":"abcdefghij","expiresAt":null}}`)
	f.json("POST /api/share/mediaitem", `{"id":"sh1","mediaItemId":"m1","slug":"kept","expiresAt":"2026-10-06T00:00:00.000Z"}`)
	f.json("DELETE /api/share/mediaitem/sh1", `OK`)
	call := toolCaller(t, f)

	out, err := call("feed_edit", map[string]any{"item": itemID, "link": true, "expires_days": 7, "downloadable": true})
	if err != nil {
		t.Fatal(err)
	}
	if !isTrue(out["opened"]) || !strings.HasSuffix(str(t, out["url"]), "/share/kept") || out["expires"] != "2026-10-06T00:00:00.000Z" {
		t.Errorf("open = %v", out)
	}
	var body struct {
		MediaItemID    string `json:"mediaItemId"`
		MediaItemType  string `json:"mediaItemType"`
		Slug           string `json:"slug"`
		ExpiresAt      int64  `json:"expiresAt"`
		IsDownloadable bool   `json:"isDownloadable"`
	}
	sent := f.requests("/api/share/mediaitem")
	if len(sent) != 1 || json.Unmarshal([]byte(sent[0].Body), &body) != nil {
		t.Fatalf("sent %v", sent)
	}
	if body.MediaItemID != "m1" || body.MediaItemType != "book" || len(body.Slug) != 10 || !feedSlug.MatchString(body.Slug) || body.ExpiresAt == 0 || !body.IsDownloadable {
		t.Errorf("sent %+v", body)
	}

	out, err = call("feed_edit", map[string]any{"item": itemID, "link": true})
	if err != nil {
		t.Fatal(err)
	}
	if !isTrue(out["already"]) || !strings.HasSuffix(str(t, out["url"]), "/share/abcdefghij") {
		t.Errorf("a second link = %v", out)
	}
	out, err = call("feed_edit", map[string]any{"item": itemID, "link": true, "close": true})
	if err != nil {
		t.Fatal(err)
	}
	if !isTrue(out["closed"]) || len(f.requests("/api/share/mediaitem/sh1")) != 1 {
		t.Errorf("close = %v", out)
	}
}

func TestFeedEditRefuses(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	call := toolCaller(t, f)

	for _, c := range []struct {
		args map[string]any
		says string
	}{
		{map[string]any{}, "name one of item, series or collection"},
		{map[string]any{"item": itemID, "series": seriesA}, "name one of"},
		{map[string]any{"series": seriesA, "link": true}, "a link plays one book"},
		{map[string]any{"item": itemID, "downloadable": true}, "pass link"},
		{map[string]any{"item": itemID, "close": true, "slug": "x"}, "close takes only"},
		{map[string]any{"item": itemID, "slug": "Dune Feed"}, "lowercase letters"},
	} {
		_, err := call("feed_edit", c.args)
		wantErr(t, fmt.Sprint(c.args), err, c.says)
	}
	if got := f.changes(); len(got) != 0 {
		t.Errorf("sent %v", got)
	}
}
