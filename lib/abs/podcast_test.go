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
