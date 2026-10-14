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

// Before the day's first line the server answers the log with an empty
// string, not a list; and a line keeps its level and source.
func TestLoggerData(t *testing.T) {
	t.Parallel()

	for body, want := range map[string]int{
		`{"currentDailyLogs":""}`:   0,
		`{"currentDailyLogs":null}`: 0,
		`{"currentDailyLogs":[{"timestamp":"2026-09-29 10:00:00.000","source":"Scanner.js:1","message":"m","levelName":"WARN","level":3}]}`: 1,
	} {
		c := newClient(t, newJSONServer(t, always(http.StatusOK, body)))
		lines, err := c.LoggerData(t.Context())
		if err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		if lines == nil || len(lines) != want {
			t.Errorf("%s: %d lines (nil %v), want %d", body, len(lines), lines == nil, want)
		}
		if want == 1 && (lines[0].LevelName != "WARN" || lines[0].Level != 3 || lines[0].Source != "Scanner.js:1") {
			t.Errorf("line = %+v", lines[0])
		}
	}
}

// The server answers a file delete with a plain OK, which is not a record to
// decode: every delete that worked came back as an error.
func TestDeleteItemFileTakesAPlainOK(t *testing.T) {
	t.Parallel()

	s := newRawServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("OK"))
	})
	if err := newClient(t, s).DeleteItemFile(t.Context(), "i1", "f1"); err != nil {
		t.Errorf("DeleteItemFile: %v", err)
	}
	if got := s.requests(); len(got) != 1 || got[0] != "DELETE /api/items/i1/file/f1" {
		t.Errorf("sent %v", got)
	}
}

// A book's share link comes with its record when asked for, and nil when it
// has none.
func TestItemShare(t *testing.T) {
	t.Parallel()

	s := newJSONServer(t, always(http.StatusOK, `{"id":"i1","mediaItemShare":{"id":"sh1","slug":"abc","isDownloadable":true}}`))
	share, err := newClient(t, s).ItemShare(t.Context(), "i1")
	if err != nil {
		t.Fatal(err)
	}
	// the server honours include only on the expanded record
	if q := s.values(t); share == nil || share.Slug != "abc" || !share.IsDownloadable || q.Get("include") != "share" || q.Get("expanded") != "1" {
		t.Errorf("share = %+v, query %v", share, s.values(t))
	}
	none, err := newClient(t, newJSONServer(t, always(http.StatusOK, `{"id":"i1","mediaItemShare":null}`))).ItemShare(t.Context(), "i1")
	if err != nil || none != nil {
		t.Errorf("no share = %+v, %v", none, err)
	}
}

func TestEReaderDevices(t *testing.T) {
	t.Parallel()

	s := newJSONServer(t, always(http.StatusOK, `{"user":{},"ereaderDevices":[{"name":"Kindle","email":"k@x","availabilityOption":"userOrUp"}]}`))
	devices, err := newClient(t, s).EReaderDevices(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 1 || devices[0].Name != "Kindle" || devices[0].AvailableTo != "userOrUp" {
		t.Errorf("devices = %+v", devices)
	}
	if got := s.requests(); len(got) != 1 || got[0] != "POST /api/authorize" {
		t.Errorf("sent %v", got)
	}
}
