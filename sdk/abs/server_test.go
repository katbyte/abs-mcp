package abs

import (
	"net/http"
	"testing"
)

// CreateBackup handles both the backups list new servers answer with and the
// single backup older ones return.
func TestCreateBackupShapes(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		body string
		want int
	}{
		{`{"backups":[{"id":"b1"},{"id":"b2"}]}`, 2},
		{`{"id":"b1","filename":"x.audiobookshelf"}`, 1},
		{`{}`, 0},
	} {
		s := newJSONServer(t, func(*http.Request) (int, string) { return http.StatusOK, tc.body })
		got, err := newClient(t, s).CreateBackup(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != tc.want {
			t.Errorf("%s: %d backups, want %d", tc.body, len(got), tc.want)
		}
	}
}

// An embed is pending while it runs and while it waits in the queue, which
// the task list only carries when asked to include it.
func TestEmbedPendingReadsTasksAndQueue(t *testing.T) {
	t.Parallel()

	s := newJSONServer(t, func(*http.Request) (int, string) {
		return http.StatusOK, `{"tasks":[
			{"id":"t1","action":"embed-metadata","data":{"libraryItemId":"running"}},
			{"id":"t2","action":"library-scan","data":{"libraryItemId":"scanning"}}
		],"queuedTaskData":{"embedMetadata":[{"libraryItemId":"queued"}]}}`
	})
	c := newClient(t, s)

	for id, want := range map[string]bool{"running": true, "queued": true, "scanning": false, "done": false} {
		got, err := c.EmbedPending(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("EmbedPending(%s) = %v, want %v", id, got, want)
		}
	}
	if q := s.values(t); s.path != "/api/tasks" || q.Get("include") != "queue" {
		t.Errorf("EmbedPending asked %s?%s", s.path, s.query)
	}
}

// The whole server's year is not an account's: its sessions, books added and
// totals have names of their own, and were read as zero through YearStats.
func TestServerYearStatsShape(t *testing.T) {
	t.Parallel()

	s := newJSONServer(t, always(http.StatusOK, `{"numListeningSessions":40,"totalListeningTime":7200,"numBooksAdded":3,"totalBooksAddedSize":1048576,`+
		`"totalBooksAddedDuration":36000,"booksAddedWithCovers":["i1"],"numAuthorsAdded":2,"numBooks":120,"totalBooksSize":987654321,"totalBooksDuration":3600000,`+
		`"topAuthors":[{"name":"Frank Herbert","time":3600}],"topNarrators":[],"topGenres":[{"genre":"Science Fiction","time":7200}]}`))
	y, err := newClient(t, s).ServerYearStats(t.Context(), 2025)
	if err != nil {
		t.Fatal(err)
	}
	if y.ListeningSessions != 40 || y.BooksAdded != 3 || y.BooksAddedSize != 1048576 || y.AuthorsAdded != 2 || y.Books != 120 ||
		y.BooksSize != 987654321 || len(y.BooksAddedWithCovers) != 1 || y.TopAuthors[0].Name != "Frank Herbert" || y.TopGenres[0].Genre != "Science Fiction" {
		t.Errorf("year = %+v", y)
	}
	if got := s.requests(); len(got) != 1 || got[0] != "GET /api/stats/year/2025" {
		t.Errorf("sent %v", got)
	}
}

// The history of every account's listening, a page of it: who, the order and
// the page go in the query, an option left out is not sent, and the sessions
// come back with how many there are in all.
func TestSessionsPages(t *testing.T) {
	t.Parallel()

	s := newJSONServer(t, always(http.StatusOK, `{"total":41,"sessions":[{"id":"s1","userId":"u1","displayTitle":"Dune","timeListening":90.5}]}`))
	c := newClient(t, s)

	sessions, total, err := c.Sessions(t.Context(), SessionsOptions{UserID: "u1", Sort: "timeListening", Desc: true, ItemsPerPage: 10, Page: 3})
	if err != nil {
		t.Fatal(err)
	}
	if total != 41 || len(sessions) != 1 || sessions[0].ID != "s1" || sessions[0].DisplayTitle != "Dune" || sessions[0].TimeListening != 90.5 {
		t.Errorf("answer = %+v of %d", sessions, total)
	}
	q := s.values(t)
	if s.path != "/api/sessions" || q.Get("user") != "u1" || q.Get("sort") != "timeListening" || q.Get("desc") != "1" || q.Get("itemsPerPage") != "10" || q.Get("page") != "3" {
		t.Errorf("asked %s?%s", s.path, s.query)
	}

	// nothing chosen: the first page, oldest order off, and no filter sent
	if _, _, err := c.Sessions(t.Context(), SessionsOptions{}); err != nil {
		t.Fatal(err)
	}
	q = s.values(t)
	if q.Has("user") || q.Has("sort") || q.Has("itemsPerPage") || q.Get("desc") != "0" || q.Get("page") != "0" {
		t.Errorf("with no options asked ?%s", s.query)
	}
}

// One account's listening, by the route that names the account, whose id is
// escaped into the path.
func TestUserSessionsPages(t *testing.T) {
	t.Parallel()

	s := newJSONServer(t, always(http.StatusOK, `{"total":2,"sessions":[{"id":"s1"},{"id":"s2"}]}`))
	c := newClient(t, s)

	sessions, total, err := c.UserSessions(t.Context(), "u/1", 25, 1)
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(sessions) != 2 || sessions[1].ID != "s2" {
		t.Errorf("answer = %+v of %d", sessions, total)
	}
	if q := s.values(t); s.path != "/api/users/u/1/listening-sessions" || q.Get("itemsPerPage") != "25" || q.Get("page") != "1" {
		t.Errorf("asked %s?%s", s.path, s.query)
	}

	s = newJSONServer(t, always(http.StatusForbidden, "Forbidden"))
	if _, _, err := newClient(t, s).UserSessions(t.Context(), "u1", 0, 0); !IsForbidden(err) {
		t.Errorf("a refusal came back as %v", err)
	}
}
