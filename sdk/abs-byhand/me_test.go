package abs

import (
	"net/http"
	"testing"
)

func TestProgressNotFoundIsNil(t *testing.T) {
	t.Parallel()

	c := newClient(t, newJSONServer(t, always(http.StatusNotFound, "")))
	p, err := c.Progress(t.Context(), "i1", "")
	if err != nil || p != nil {
		t.Errorf("got %v, %v", p, err)
	}
}

// The key's own listening on one item, and on one episode of it: the episode
// is one more part of the path, and only when there is one.
func TestItemListeningSessionsPath(t *testing.T) {
	t.Parallel()

	s := newJSONServer(t, always(http.StatusOK, `{"total":7,"sessions":[{"id":"s1","libraryItemId":"i1","episodeId":"e1"}]}`))
	c := newClient(t, s)

	sessions, total, err := c.ItemListeningSessions(t.Context(), "i1", "", 10, 2)
	if err != nil {
		t.Fatal(err)
	}
	if total != 7 || len(sessions) != 1 || sessions[0].LibraryItemID != "i1" {
		t.Errorf("answer = %+v of %d", sessions, total)
	}
	if q := s.values(t); s.path != "/api/me/item/listening-sessions/i1" || q.Get("itemsPerPage") != "10" || q.Get("page") != "2" {
		t.Errorf("a book asked %s?%s", s.path, s.query)
	}

	if _, _, err := c.ItemListeningSessions(t.Context(), "i1", "e1", 0, 0); err != nil {
		t.Fatal(err)
	}
	if q := s.values(t); s.path != "/api/me/item/listening-sessions/i1/e1" || q.Has("itemsPerPage") || q.Get("page") != "0" {
		t.Errorf("an episode asked %s?%s", s.path, s.query)
	}
}

// The key's own listening, a page of it, with how many sessions there are.
func TestListeningSessionsPages(t *testing.T) {
	t.Parallel()

	s := newJSONServer(t, always(http.StatusOK, `{"total":300,"sessions":[{"id":"s1"},{"id":"s2"},{"id":"s3"}]}`))
	c := newClient(t, s)

	sessions, total, err := c.ListeningSessions(t.Context(), 3, 9)
	if err != nil {
		t.Fatal(err)
	}
	if total != 300 || len(sessions) != 3 || sessions[2].ID != "s3" {
		t.Errorf("answer = %+v of %d", sessions, total)
	}
	if q := s.values(t); s.path != "/api/me/listening-sessions" || q.Get("itemsPerPage") != "3" || q.Get("page") != "9" {
		t.Errorf("asked %s?%s", s.path, s.query)
	}

	// an answer that is not the shape promised is an error, not no sessions
	s = newJSONServer(t, always(http.StatusOK, `{"total":"many"}`))
	if _, _, err := newClient(t, s).ListeningSessions(t.Context(), 0, 0); err == nil {
		t.Error("a total that is not a number came back as no error")
	}
}

// What the key's account is part way through: the items come under
// libraryItems, and a limit is sent only when one is asked for.
func TestItemsInProgressLimit(t *testing.T) {
	t.Parallel()

	s := newJSONServer(t, always(http.StatusOK, `{"libraryItems":[{"id":"i1","mediaType":"book","media":{"metadata":{"title":"Dune"}}}]}`))
	c := newClient(t, s)

	items, err := c.ItemsInProgress(t.Context(), 12)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Title() != "Dune" {
		t.Errorf("answer = %+v", items)
	}
	if s.path != "/api/me/items-in-progress" || s.values(t).Get("limit") != "12" {
		t.Errorf("asked %s?%s", s.path, s.query)
	}
	if _, err := c.ItemsInProgress(t.Context(), 0); err != nil {
		t.Fatal(err)
	}
	if s.query != "" {
		t.Errorf("with no limit asked ?%s", s.query)
	}
}
