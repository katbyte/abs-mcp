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
