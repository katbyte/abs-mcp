package abs

import (
	"net/http"
	"strings"
	"testing"
)

func TestCreateAPIKeyExpiry(t *testing.T) {
	t.Parallel()

	s := newJSONServer(t, always(http.StatusOK, `{"apiKey":{"id":"k1"}}`))
	c := newClient(t, s)

	if _, err := c.CreateAPIKey(t.Context(), "n", "u", 0, true); err != nil {
		t.Fatal(err)
	}
	if body := s.sent(t); body["expiresIn"] != nil {
		t.Errorf("expiresIn sent for a key that never expires: %v", body)
	}

	if _, err := c.CreateAPIKey(t.Context(), "n", "u", 3600, true); err != nil {
		t.Fatal(err)
	}
	if body := s.sent(t); body["expiresIn"] != float64(3600) {
		t.Errorf("expiresIn = %v, want 3600", body["expiresIn"])
	}
}

// Both default to "book" when the caller leaves the type out, and the server
// rejects the request without it.
func TestMediaTypeDefaults(t *testing.T) {
	t.Parallel()

	s := newJSONServer(t, always(http.StatusOK, `{"provider":{"id":"p1"},"id":"s1"}`))
	c := newClient(t, s)

	if _, err := c.CreateCustomMetadataProvider(t.Context(), "n", "https://p", "", ""); err != nil {
		t.Fatal(err)
	}
	if got := s.sent(t)["mediaType"]; got != "book" {
		t.Errorf("custom provider mediaType = %v, want book", got)
	}
	if _, err := c.CreateCustomMetadataProvider(t.Context(), "n", "https://p", "podcast", ""); err != nil {
		t.Fatal(err)
	}
	if got := s.sent(t)["mediaType"]; got != "podcast" {
		t.Errorf("an explicit mediaType was overwritten: %v", got)
	}

	if _, err := c.ShareMediaItem(t.Context(), "m1", "", "slug", 0, false); err != nil {
		t.Fatal(err)
	}
	if got := s.sent(t)["mediaItemType"]; got != "book" {
		t.Errorf("share mediaItemType = %v, want book", got)
	}
	if _, err := c.ShareMediaItem(t.Context(), "m1", "podcastEpisode", "slug", 0, false); err != nil {
		t.Fatal(err)
	}
	if got := s.sent(t)["mediaItemType"]; got != "podcastEpisode" {
		t.Errorf("an explicit mediaItemType was overwritten: %v", got)
	}
}

func TestPlayURLDependsOnEpisode(t *testing.T) {
	t.Parallel()

	s := newJSONServer(t, always(http.StatusOK, `{"id":"s1"}`))
	c := newClient(t, s)

	if _, err := c.Play(t.Context(), "item1", "", PlayRequest{}); err != nil {
		t.Fatal(err)
	}
	if s.path != "/api/items/item1/play" {
		t.Errorf("a book played at %q", s.path)
	}

	if _, err := c.Play(t.Context(), "item1", "ep7", PlayRequest{}); err != nil {
		t.Fatal(err)
	}
	if s.path != "/api/items/item1/play/ep7" {
		t.Errorf("an episode played at %q", s.path)
	}
}

// The folder picker's route answers 400 for a path that is not there, which
// is an answer, not an error; anything else is still an error.
func TestServerPathExists(t *testing.T) {
	t.Parallel()

	s := newJSONServer(t, func(r *http.Request) (int, string) {
		switch r.URL.Query().Get("path") {
		case "/books":
			return http.StatusOK, `{"posix":true,"directories":[]}`
		case "/forbidden":
			return http.StatusForbidden, "Forbidden"
		default:
			return http.StatusBadRequest, `Invalid "path" query string`
		}
	})
	c := newClient(t, s)

	if ok, err := c.ServerPathExists(t.Context(), "/books"); err != nil || !ok {
		t.Errorf("/books = %v, %v; want true", ok, err)
	}
	if s.path != "/api/filesystem" || !strings.Contains(s.query, "path=%2Fbooks") || !strings.Contains(s.query, "level=0") {
		t.Errorf("request = %s?%s", s.path, s.query)
	}
	if ok, err := c.ServerPathExists(t.Context(), "/nope"); err != nil || ok {
		t.Errorf("/nope = %v, %v; want false and no error", ok, err)
	}
	if _, err := c.ServerPathExists(t.Context(), "/forbidden"); err == nil {
		t.Error("a 403 was taken for an answer")
	}
}
