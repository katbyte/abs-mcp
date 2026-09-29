package tools

import (
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/katbyte/go-kt/version"
)

// kind=Tags matched neither lowercase test and answered genres as well; an
// unknown kind answered both.
func TestServerTagsKindIgnoresCase(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /api/tags", `{"tags":["sf"]}`)
	f.json("GET /api/genres", `{"genres":["Fantasy"]}`)
	call := toolCaller(t, f)

	out, err := call("server_tags", map[string]any{"kind": "Tags"})
	if err != nil {
		t.Fatal(err)
	}
	if _, genres := out["genres"]; genres || len(strs(t, out["tags"])) != 1 {
		t.Errorf("kind=Tags = %v, want the tags alone", out)
	}
	if got := f.requests("/api/genres"); len(got) != 0 {
		t.Errorf("kind=Tags asked for genres: %v", got)
	}

	if _, err := call("server_tags", map[string]any{"kind": "labels"}); err == nil || !strings.Contains(err.Error(), "tags or genres") {
		t.Errorf("an unknown kind: %v", err)
	}

	out, err = call("server_tags", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(strs(t, out["tags"])) != 1 || len(strs(t, out["genres"])) != 1 {
		t.Errorf("no kind = %v, want both", out)
	}
}

// A server with no libraries answered "libraries": null.
func TestServerInfoListsNoLibrariesAsEmpty(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /status", `{"serverVersion":"2.30.0"}`)
	f.json("GET /api/me", `{"id":"u1","username":"root","type":"root"}`)
	f.json("GET /api/libraries", `{"libraries":[]}`)
	f.json("GET /api/stats/server", `{"books":{"numItems":0},"podcasts":{"numItems":0},"total":{"numItems":0}}`)
	f.json("GET /api/users", `{"users":[{"id":"u1","username":"root","type":"root"}]}`)
	f.json("GET /api/sessions/open", `{"sessions":[]}`)
	call := toolCaller(t, f)

	out, err := call("server_info", nil)
	if err != nil {
		t.Fatal(err)
	}
	libs, ok := out["libraries"].([]any)
	if !ok || len(libs) != 0 {
		t.Errorf("libraries = %#v, want an empty list", out["libraries"])
	}
	// and which build of this server answered, beside the server's own
	if out["version"] != "2.30.0" || out["abs_mcp_version"] != version.Version {
		t.Errorf("versions = %v and %v, want the server's and %q", out["version"], out["abs_mcp_version"], version.Version)
	}
}

// The server names a backup by the minute it was made, so a second backup in
// one minute replaces the first, and it deletes the oldest past the number it
// keeps: the answer names the backup made and says when either happened,
// where a count of the list read the replacement as nothing made.
func TestServerBackupCreateSaysWhichItMade(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /api/backups", `{"backups":[{"id":"2026-09-24T1000","filename":"2026-09-24T1000.audiobookshelf","createdAt":1000},{"id":"2026-09-24T1200","filename":"2026-09-24T1200.audiobookshelf","createdAt":2000}],"backupLocation":"/metadata/backups"}`)
	f.json("POST /api/backups", `{"backups":[{"id":"2026-09-24T1200","filename":"2026-09-24T1200.audiobookshelf","createdAt":3000,"fileSize":4096}]}`)
	call := toolCaller(t, f)

	out, err := call("server_backup_create", nil)
	if err != nil {
		t.Fatal(err)
	}
	created, ok := out["created"].(map[string]any)
	if !ok || created["id"] != "2026-09-24T1200" || num(t, created["size"]) != 4096 {
		t.Errorf("created = %v, want the newest backup", out["created"])
	}
	if !boolOf(t, out["replaced"]) {
		t.Error("a backup made in the minute of another did not say it replaced it")
	}
	if pruned := strs(t, out["pruned"]); len(pruned) != 1 || pruned[0] != "2026-09-24T1000" {
		t.Errorf("pruned = %v, want the oldest", out["pruned"])
	}
}

func TestServerInfoSaysWhatFailed(t *testing.T) {
	t.Parallel()

	base := func() *fakeABS {
		f := newFakeABS(t)
		f.json("GET /status", `{"serverVersion":"2.30.0"}`)
		f.json("GET /api/me", `{"id":"u1","username":"kt","type":"user"}`)
		f.json("GET /api/libraries", `{"libraries":[]}`)
		return f
	}

	// a key the totals are refused to is told so, and is no error
	f := base()
	f.mux.HandleFunc("GET /api/stats/server", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "Forbidden", http.StatusForbidden)
	})
	out, err := toolCaller(t, f)("server_info", nil)
	if err != nil || !strings.Contains(str(t, out["note"]), "need an admin key") {
		t.Errorf("totals refused to the key = %v, %v, want the note and no error", out, err)
	}

	for name, fail := range map[string]string{
		"the providers":     "GET /api/search/providers",
		"the totals":        "GET /api/stats/server",
		"the users":         "GET /api/users",
		"the open sessions": "GET /api/sessions/open",
	} {
		f := base()
		f.json("GET /api/stats/server", `{"books":{"numItems":0},"podcasts":{"numItems":0},"total":{"numItems":0}}`)
		f.json("GET /api/users", `{"users":[]}`)
		f.json("GET /api/sessions/open", `{"sessions":[]}`)
		f.json("GET /api/search/providers", `{"providers":{"books":[],"podcasts":[]}}`)
		f.fails(fail)
		_, err := toolCaller(t, f)("server_info", nil)
		wantErr(t, name+" failing", err, "500")
	}
}

func TestSessionsWhoseUsersCannotBeReadAreAnError(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /api/sessions/open", `{"sessions":[{"id":"s1","userId":"u1","libraryItemId":"`+bookB1+`","displayTitle":"First","duration":60,"currentTime":30}]}`)
	f.fails("GET /api/users")
	call := toolCaller(t, f)

	_, err := call("server_sessions", nil)
	wantErr(t, "sessions with the users unreadable", err, "reading the users", "500")
}

// nowMillis is the time as the server writes it, for a session the 60-day
// history window keeps.
func nowMillis() string { return strconv.FormatInt(time.Now().UnixMilli(), 10) }

// An open session names its user by id only; the tools say who it is.
func TestSessionsNameTheirUser(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	session := `{"id":"s1","userId":"u1","libraryItemId":"` + bookB1 + `","displayTitle":"First","duration":60,"currentTime":30}`
	f.json("GET /api/sessions/open", `{"sessions":[`+session+`]}`)
	f.json("GET /api/users", `{"users":[{"id":"u1","username":"reader","type":"user"}]}`)
	f.json("GET /api/users/u1", `{"id":"u1","username":"reader","type":"user"}`)
	f.json("GET /api/users/u1/listening-sessions", `{"total":1,"sessions":[`+strings.Replace(session, `"id":"s1"`, `"id":"s1","updatedAt":`+nowMillis(), 1)+`]}`)
	call := toolCaller(t, f)

	out, err := call("server_sessions", nil)
	if err != nil {
		t.Fatal(err)
	}
	if rows := list(t, out["sessions"]); len(rows) != 1 || str(t, rows[0]["user"]) != "reader" {
		t.Errorf("sessions = %v, want reader named", out["sessions"])
	}
	out, err = call("user_history", map[string]any{"user": "reader"})
	if err != nil {
		t.Fatal(err)
	}
	if rows := list(t, out["sessions"]); len(rows) != 1 || str(t, rows[0]["user"]) != "reader" {
		t.Errorf("history = %v, want reader named", out["sessions"])
	}
}

// A non-admin key is told why it cannot see open sessions: the server answers
// 404, not 403.
func TestServerSessionsSaysAdminOnly(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	call := toolCaller(t, f)
	if _, err := call("server_sessions", nil); err == nil || !strings.Contains(err.Error(), "admin only") {
		t.Errorf("err = %v, want it to say admin only", err)
	}
}

func TestServerSizesInBytes(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /status", `{"serverVersion":"2.30.0"}`)
	f.json("GET /api/me", `{"id":"u1","username":"root","type":"root"}`)
	f.json("GET /api/libraries", `{"libraries":[]}`)
	f.json("GET /api/stats/server", `{"books":{"totalSize":1610612736,"numItems":2},"podcasts":{"totalSize":52428800,"numItems":1},"total":{"totalSize":1663041536,"numItems":3,"numAudioFiles":4}}`)
	f.json("GET /api/backups", `{"backups":[{"id":"b1","filename":"b1.audiobookshelf","fileSize":5242880,"createdAt":1700000000000}],"backupLocation":"/metadata/backups"}`)
	f.json("GET /api/users", `{"users":[{"id":"u1","username":"root","type":"root"}]}`)
	f.json("GET /api/sessions/open", `{"sessions":[]}`)
	call := toolCaller(t, f)

	out, err := call("server_info", nil)
	if err != nil {
		t.Fatal(err)
	}
	// the podcasts' 50 MB was 0 in whole gigabytes
	wantNumbers(t, "server_info", out, map[string]float64{"totals.total_size": 1663041536, "totals.books_size": 1610612736, "totals.podcasts_size": 52428800})
	wantAbsent(t, "server_info", out, "totals.total_size_gb", "totals.books_size_gb", "totals.podcasts_size_gb")

	out, err = call("server_backups", nil)
	if err != nil {
		t.Fatal(err)
	}
	wantNumbers(t, "server_backups", out, map[string]float64{"backups.0.size": 5242880})
	wantAbsent(t, "server_backups", out, "backups.0.size_mb")
}

// log adds today's server log, newest first, from warnings up unless level
// says otherwise, narrowed by match and cut at limit; the log's options are
// refused without it.
func TestServerTasksLog(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /api/tasks", `{"tasks":[]}`)
	f.json("GET /api/logger-data", `{"currentDailyLogs":[`+
		`{"timestamp":"2026-09-29 10:00:00.000","source":"Scanner.js:1","message":"[Scanner] started","levelName":"INFO","level":2},`+
		`{"timestamp":"2026-09-29 10:00:01.000","source":"AbMergeManager.js:9","message":"[AbMergeManager] Failed to move m4b","levelName":"ERROR","level":4},`+
		`{"timestamp":"2026-09-29 10:00:02.000","source":"Scanner.js:2","message":"[Scanner] slow folder","levelName":"WARN","level":3}]}`)
	call := toolCaller(t, f)

	out, err := call("server_tasks", map[string]any{"log": true})
	if err != nil {
		t.Fatal(err)
	}
	if got := column(t, "message", out["log"]); !slices.Equal(got, []string{"[Scanner] slow folder", "[AbMergeManager] Failed to move m4b"}) {
		t.Errorf("log = %v, want warnings and errors, newest first", got)
	}
	wantNumbers(t, "server_tasks", out, map[string]float64{"log_matched": 2})

	out, err = call("server_tasks", map[string]any{"log": true, "level": "Info", "match": "scanner", "limit": 1})
	if err != nil {
		t.Fatal(err)
	}
	if got := column(t, "message", out["log"]); !slices.Equal(got, []string{"[Scanner] slow folder"}) {
		t.Errorf("log = %v", got)
	}
	wantNumbers(t, "server_tasks", out, map[string]float64{"log_matched": 2})

	out, err = call("server_tasks", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, has := out["log"]; has || len(f.requests("/api/logger-data")) != 2 {
		t.Errorf("without log the log was read: %v", out)
	}

	for _, c := range []struct {
		args map[string]any
		says string
	}{
		{map[string]any{"match": "x"}, "pass log too"},
		{map[string]any{"log": true, "level": "loud"}, "unknown level"},
		{map[string]any{"log": true, "limit": 900}, "1 to 500"},
	} {
		_, err := call("server_tasks", c.args)
		wantErr(t, fmt.Sprint(c.args), err, c.says)
	}
}
