package tools

import (
	"strings"
	"testing"

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
	if _, genres := out["genres"]; genres || len(anyStrings(out["tags"])) != 1 {
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
	if len(anyStrings(out["tags"])) != 1 || len(anyStrings(out["genres"])) != 1 {
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
	if pruned := anyStrings(out["pruned"]); len(pruned) != 1 || pruned[0] != "2026-09-24T1000" {
		t.Errorf("pruned = %v, want the oldest", out["pruned"])
	}
}
