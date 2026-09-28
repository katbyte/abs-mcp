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
