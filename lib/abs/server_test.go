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
