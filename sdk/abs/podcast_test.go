package abs

import (
	"net/http"
	"testing"
)

// The feed search wraps each hit in an episode key, which collides with the
// episode-number field when decoded flat.
func TestSearchFeedEpisodesUnwraps(t *testing.T) {
	t.Parallel()

	s := newJSONServer(t, func(*http.Request) (int, string) {
		return http.StatusOK, `{"episodes":[{"episode":{"title":"Part 1","episode":"12","season":2}},{"episode":{"title":"Part 2","episode":13}}]}`
	})
	c := newClient(t, s)

	eps, err := c.SearchFeedEpisodes(t.Context(), "p1", "Part")
	if err != nil {
		t.Fatal(err)
	}
	if q := s.values(t); q.Get("title") != "Part" {
		t.Errorf("query %q", s.query)
	}
	if len(eps) != 2 || eps[0].Title != "Part 1" || eps[0].Episode != "12" || eps[0].Season != "2" || eps[1].Episode != "13" {
		t.Errorf("episodes = %+v", eps)
	}
}

// Asking a show's feed for episodes not yet downloaded: the episodes found,
// with a limit only when one is asked for.
func TestCheckNewEpisodesLimit(t *testing.T) {
	t.Parallel()

	s := newJSONServer(t, always(http.StatusOK, `{"episodes":[{"title":"New One","pubDate":"Mon, 28 Sep 2026 10:00:00 GMT","season":"3","episode":12}]}`))
	c := newClient(t, s)

	episodes, err := c.CheckNewEpisodes(t.Context(), "pod1", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(episodes) != 1 || episodes[0].Title != "New One" || episodes[0].Season.String() != "3" || episodes[0].Episode.String() != "12" {
		t.Errorf("answer = %+v", episodes)
	}
	if s.method != http.MethodGet || s.path != "/api/podcasts/pod1/checknew" || s.values(t).Get("limit") != "3" {
		t.Errorf("asked %s %s?%s", s.method, s.path, s.query)
	}
	if _, err := c.CheckNewEpisodes(t.Context(), "pod1", 0); err != nil {
		t.Fatal(err)
	}
	if s.query != "" {
		t.Errorf("with no limit asked ?%s", s.query)
	}

	// a feed with nothing new answers no episodes, and no error
	s = newJSONServer(t, always(http.StatusOK, `{"episodes":[]}`))
	if episodes, err = newClient(t, s).CheckNewEpisodes(t.Context(), "pod1", 0); err != nil || len(episodes) != 0 {
		t.Errorf("nothing new = %v, %v", episodes, err)
	}
}
