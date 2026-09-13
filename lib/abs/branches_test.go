package abs

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The pure fallback chains. Only their first branch was exercised by the live
// suites, which is the worst place for a silent bug: a wrong fallback returns
// a plausible zero rather than an error.

func TestEpisodeDurationSeconds(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name string
		ep   Episode
		want float64
	}{
		{"its own duration wins", Episode{Duration: 120, AudioFile: &AudioFile{Duration: 999}}, 120},
		{"falls back to the audio file", Episode{AudioFile: &AudioFile{Duration: 300}}, 300},
		{"then to the audio track", Episode{AudioTrack: &AudioTrack{Duration: 42}}, 42},
		{"and is zero with nothing to go on", Episode{}, 0},
	} {
		if got := c.ep.DurationSeconds(); got != c.want {
			t.Errorf("%s: DurationSeconds() = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestCountRowN(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name string
		row  CountRow
		want int
	}{
		{"books win", CountRow{NumBooks: 3, NumItems: 9, Count: 9}, 3},
		{"then items", CountRow{NumItems: 7, Count: 9}, 7},
		{"then the plain count", CountRow{Count: 5}, 5},
		{"and zero when empty", CountRow{}, 0},
	} {
		if got := c.row.N(); got != c.want {
			t.Errorf("%s: N() = %d, want %d", c.name, got, c.want)
		}
	}
}

func TestTruncateAndBoolQuery(t *testing.T) {
	t.Parallel()

	if got := truncate("short", 10); got != "short" {
		t.Errorf("truncate under the limit = %q", got)
	}
	if got := truncate("exactly-10", 10); got != "exactly-10" {
		t.Errorf("truncate at the limit = %q", got)
	}
	if got := truncate("far too long to keep", 5); got != "far t..." {
		t.Errorf("truncate over the limit = %q", got)
	}
	if boolQuery(true) != "1" || boolQuery(false) != "0" {
		t.Errorf("boolQuery = %q/%q, want 1/0", boolQuery(true), boolQuery(false))
	}
}

// Parameters that change the request rather than just its contents. A bug here
// is silent: the call succeeds against the wrong URL, or omits a field.

func TestPlayURLDependsOnEpisode(t *testing.T) {
	t.Parallel()

	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_, _ = w.Write([]byte(`{"id":"s1"}`))
	}))
	defer srv.Close()

	c, err := New(srv.URL, "k")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := c.Play(t.Context(), "item1", "", PlayRequest{}); err != nil {
		t.Fatal(err)
	}
	if path != "/api/items/item1/play" {
		t.Errorf("a book played at %q", path)
	}

	if _, err := c.Play(t.Context(), "item1", "ep7", PlayRequest{}); err != nil {
		t.Fatal(err)
	}
	if path != "/api/items/item1/play/ep7" {
		t.Errorf("an episode played at %q", path)
	}
}

func TestCreateAPIKeyExpiry(t *testing.T) {
	t.Parallel()

	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body = nil
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(`{"apiKey":{"id":"k1"}}`))
	}))
	defer srv.Close()

	c, err := New(srv.URL, "k")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := c.CreateAPIKey(t.Context(), "n", "u", 0, true); err != nil {
		t.Fatal(err)
	}
	if _, present := body["expiresIn"]; present {
		t.Errorf("expiresIn sent for a key that never expires: %v", body)
	}

	if _, err := c.CreateAPIKey(t.Context(), "n", "u", 3600, true); err != nil {
		t.Fatal(err)
	}
	if body["expiresIn"] != float64(3600) {
		t.Errorf("expiresIn = %v, want 3600", body["expiresIn"])
	}
}

// Both default to "book" when the caller leaves the type out, and the server
// rejects the request without it.
func TestMediaTypeDefaults(t *testing.T) {
	t.Parallel()

	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body = nil
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(`{"provider":{"id":"p1"},"id":"s1"}`))
	}))
	defer srv.Close()

	c, err := New(srv.URL, "k")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := c.CreateCustomMetadataProvider(t.Context(), "n", "https://p", "", ""); err != nil {
		t.Fatal(err)
	}
	if body["mediaType"] != "book" {
		t.Errorf("custom provider mediaType = %v, want book", body["mediaType"])
	}
	if _, err := c.CreateCustomMetadataProvider(t.Context(), "n", "https://p", "podcast", ""); err != nil {
		t.Fatal(err)
	}
	if body["mediaType"] != "podcast" {
		t.Errorf("an explicit mediaType was overwritten: %v", body["mediaType"])
	}

	if _, err := c.ShareMediaItem(t.Context(), "m1", "", "slug", 0, false); err != nil {
		t.Fatal(err)
	}
	if body["mediaItemType"] != "book" {
		t.Errorf("share mediaItemType = %v, want book", body["mediaItemType"])
	}
	if _, err := c.ShareMediaItem(t.Context(), "m1", "podcastEpisode", "slug", 0, false); err != nil {
		t.Fatal(err)
	}
	if body["mediaItemType"] != "podcastEpisode" {
		t.Errorf("an explicit mediaItemType was overwritten: %v", body["mediaItemType"])
	}
}

// intQuery omits a zero rather than sending it, because the server treats an
// explicit 0 as a real limit.
func TestIntQueryOmitsZero(t *testing.T) {
	t.Parallel()

	q := map[string][]string{}
	intQuery(q, "limit", 0)
	if _, present := q["limit"]; present {
		t.Error("intQuery sent a zero")
	}
	intQuery(q, "limit", 5)
	if q["limit"][0] != "5" {
		t.Errorf("intQuery = %v", q["limit"])
	}
}

// More parameters that reshape the request: the episode segment on a playlist
// removal, and the optional country on a podcast search.
func TestOptionalPathAndQuerySegments(t *testing.T) {
	t.Parallel()

	var path, query string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, query = r.URL.Path, r.URL.RawQuery
		if r.Method == http.MethodDelete {
			_, _ = w.Write([]byte(`{"id":"pl1"}`)) // a playlist
			return
		}
		_, _ = w.Write([]byte(`[]`)) // a search result list
	}))
	defer srv.Close()

	c, err := New(srv.URL, "k")
	if err != nil {
		t.Fatal(err)
	}

	// removing a book addresses the item; removing an episode addresses the
	// episode within it
	if _, err := c.RemoveItemFromPlaylist(t.Context(), "pl1", "item1", ""); err != nil {
		t.Fatal(err)
	}
	if path != "/api/playlists/pl1/item/item1" {
		t.Errorf("book removal hit %q", path)
	}
	if _, err := c.RemoveItemFromPlaylist(t.Context(), "pl1", "item1", "ep2"); err != nil {
		t.Fatal(err)
	}
	if path != "/api/playlists/pl1/item/item1/ep2" {
		t.Errorf("episode removal hit %q", path)
	}

	// country is iTunes' store, and must be absent rather than empty
	if _, err := c.SearchPodcasts(t.Context(), "bastards", ""); err != nil {
		t.Fatal(err)
	}
	if q, _ := parseQuery(query); q.Has("country") {
		t.Errorf("an empty country was sent: %q", query)
	}
	if _, err := c.SearchPodcasts(t.Context(), "bastards", "ca"); err != nil {
		t.Fatal(err)
	}
	if q, _ := parseQuery(query); q.Get("country") != "ca" {
		t.Errorf("country = %q", query)
	}
}

// A cover that is absent and a cover that is empty are both ErrNoCover rather
// than a decode failure, because audit_cover_ratio has to tell "no cover" from
// "broken cover".
func TestCoverSizeNoCover(t *testing.T) {
	t.Parallel()

	var status int
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	c, err := New(srv.URL, "k")
	if err != nil {
		t.Fatal(err)
	}

	status, body = http.StatusNotFound, []byte(`{"error":"nope"}`)
	if _, _, err := c.CoverSize(t.Context(), "i1"); !errors.Is(err, ErrNoCover) {
		t.Errorf("a missing cover gave %v, want ErrNoCover", err)
	}

	status, body = http.StatusOK, nil
	if _, _, err := c.CoverSize(t.Context(), "i1"); !errors.Is(err, ErrNoCover) {
		t.Errorf("an empty cover gave %v, want ErrNoCover", err)
	}

	status, body = http.StatusOK, []byte("not an image at all")
	if _, _, err := c.CoverSize(t.Context(), "i1"); err == nil || errors.Is(err, ErrNoCover) {
		t.Errorf("an undecodable cover gave %v, want a decode error", err)
	}
}
