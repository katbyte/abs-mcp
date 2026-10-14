package abs

import (
	"net/http"
	"testing"
)

// More parameters that reshape the request: the episode segment on a playlist
// removal, and the optional country on a podcast search.
func TestOptionalPathAndQuerySegments(t *testing.T) {
	t.Parallel()

	s := newJSONServer(t, func(r *http.Request) (int, string) {
		if r.Method == http.MethodDelete {
			return http.StatusOK, `{"id":"pl1"}` // a playlist
		}
		return http.StatusOK, `[]` // a search result list
	})
	c := newClient(t, s)

	// removing a book addresses the item; removing an episode addresses the
	// episode within it
	if _, err := c.RemoveItemFromPlaylist(t.Context(), "pl1", "item1", ""); err != nil {
		t.Fatal(err)
	}
	if s.path != "/api/playlists/pl1/item/item1" {
		t.Errorf("book removal hit %q", s.path)
	}
	if _, err := c.RemoveItemFromPlaylist(t.Context(), "pl1", "item1", "ep2"); err != nil {
		t.Fatal(err)
	}
	if s.path != "/api/playlists/pl1/item/item1/ep2" {
		t.Errorf("episode removal hit %q", s.path)
	}

	// country is iTunes' store, and must be absent rather than empty
	if _, err := c.SearchPodcasts(t.Context(), "bastards", ""); err != nil {
		t.Fatal(err)
	}
	if s.values(t).Has("country") {
		t.Errorf("an empty country was sent: %q", s.query)
	}
	if _, err := c.SearchPodcasts(t.Context(), "bastards", "ca"); err != nil {
		t.Fatal(err)
	}
	if s.values(t).Get("country") != "ca" {
		t.Errorf("country = %q", s.query)
	}
}

// The author lookup route answers with the bare provider record, or null
// when nobody is close enough; an earlier decode expected a results list and
// always came back empty.
func TestSearchAuthorDecodesOneRecordOrNull(t *testing.T) {
	t.Parallel()

	body := `{"asin":"B003RY2ISS","name":"Isaac Asimov","description":"Wrote a lot.","image":"https://img/asimov.jpg"}`
	s := newJSONServer(t, func(r *http.Request) (int, string) {
		if r.URL.Query().Get("q") == "Isaac Asimov" {
			return http.StatusOK, body
		}
		return http.StatusOK, `null`
	})
	c := newClient(t, s)

	cand, err := c.SearchAuthor(t.Context(), "Isaac Asimov")
	if err != nil {
		t.Fatal(err)
	}
	if s.method != http.MethodGet || s.path != "/api/search/authors" {
		t.Errorf("sent %s %s", s.method, s.path)
	}
	if cand == nil || cand.ASIN != "B003RY2ISS" || cand.Name != "Isaac Asimov" || cand.Image == "" {
		t.Errorf("candidate = %+v", cand)
	}

	none, err := c.SearchAuthor(t.Context(), "Nobody Atall")
	if err != nil {
		t.Fatal(err)
	}
	if none != nil {
		t.Errorf("a null answer decoded as %+v", none)
	}
}

// The feed list gives each url inside meta, where opening a feed gives it at
// the top: both are filled either way.
func TestFeedsFillTheURL(t *testing.T) {
	t.Parallel()

	s := newJSONServer(t, always(http.StatusOK, `{"feeds":[{"id":"f1","slug":"dune","meta":{"title":"Dune","feedUrl":"https://abs.test/feed/dune"}}],"minified":[]}`))
	feeds, err := newClient(t, s).Feeds(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(feeds) != 1 || feeds[0].FeedURL != "https://abs.test/feed/dune" || feeds[0].Meta.FeedURL != feeds[0].FeedURL {
		t.Errorf("feeds = %+v", feeds)
	}
}

// An author is matched by asin when there is one and by name when there is
// not, never both: the server would search the name and ignore the asin.
func TestMatchAuthorByASINOrName(t *testing.T) {
	t.Parallel()

	s := newJSONServer(t, always(http.StatusOK, `{"updated":true,"author":{"id":"a1","name":"Frank Herbert","asin":"B000AP9A6K"}}`))
	c := newClient(t, s)

	author, updated, err := c.MatchAuthor(t.Context(), "a1", "Frank Herbert", "B000AP9A6K", "ca")
	if err != nil {
		t.Fatal(err)
	}
	if !updated || author.ASIN != "B000AP9A6K" || author.Name != "Frank Herbert" {
		t.Errorf("answer = %+v, updated %v", author, updated)
	}
	body := s.sent(t)
	if s.method != http.MethodPost || s.path != "/api/authors/a1/match" || body["asin"] != "B000AP9A6K" || body["region"] != "ca" || body["q"] != nil {
		t.Errorf("by asin sent %s %s %v", s.method, s.path, body)
	}

	if _, _, err := c.MatchAuthor(t.Context(), "a1", "Frank Herbert", "", ""); err != nil {
		t.Fatal(err)
	}
	if body = s.sent(t); body["q"] != "Frank Herbert" || body["asin"] != nil || body["region"] != nil {
		t.Errorf("by name sent %v", body)
	}

	// nothing found is an answer: not updated, and no error
	s = newJSONServer(t, always(http.StatusOK, `{"updated":false,"author":{"id":"a1","name":"Frank Herbert"}}`))
	if author, updated, err = newClient(t, s).MatchAuthor(t.Context(), "a1", "Frank Herbert", "", ""); err != nil || updated || author.ID != "a1" {
		t.Errorf("nothing found = %+v, %v, %v", author, updated, err)
	}
}
