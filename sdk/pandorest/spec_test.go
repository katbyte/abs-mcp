package main

import (
	"bytes"
	"cmp"
	"encoding/json"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestSpec(t *testing.T) {
	t.Parallel()

	src, out := writeFakeSource(t), t.TempDir()

	var report bytes.Buffer
	if err := spec([]string{"-v", src}, out, &report); err != nil {
		t.Fatal(err)
	}

	b, err := fs.ReadFile(os.DirFS(out), "audiobookshelf-openapi-9.9.9.json")
	if err != nil {
		t.Fatal(err)
	}

	var doc document
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}

	if doc.Info.Version != "9.9.9" || doc.OpenAPI != "3.0.3" {
		t.Errorf("info: %+v %s", doc.Info, doc.OpenAPI)
	}

	// the optional episode makes two paths, each its own operation
	ids := map[string]bool{}
	for _, p := range []string{"/api/libraries", "/api/libraries/{id}", "/api/items/{id}/play/{episodeId}", "/api/items/{id}/play", "/api/items/{id}/download", "/status", "/healthcheck"} {
		ops := doc.Paths[p]
		if len(ops) == 0 {
			t.Errorf("no path %s", p)
			continue
		}
		for verb, op := range ops {
			if ids[op.OperationID] {
				t.Errorf("%s %s: operationId %s is used twice", verb, p, op.OperationID)
			}
			ids[op.OperationID] = true
			if len(op.Tags) != 1 || len(op.Responses) == 0 {
				t.Errorf("%s %s: tags %q, %d responses", verb, p, op.Tags, len(op.Responses))
			}
		}
	}

	lib := doc.Components.Schemas["Library"]
	if lib == nil || lib.Properties["extra"] == nil || lib.Properties["displayOrder"].Type != "integer" || lib.Properties["folders"].Items.Ref != "#/components/schemas/Folder" {
		t.Errorf("Library: %+v", lib)
	}

	patch := doc.Paths["/api/libraries/{id}"]["patch"]
	if patch.RequestBody == nil || patch.RequestBody.Content["application/json"].Schema.Properties["displayOrder"] == nil {
		t.Errorf("PATCH /api/libraries/{id} body: %+v", patch.RequestBody)
	}
	if patch.Responses["200"].Content["text/plain"].Schema == nil || patch.Responses["404"].Description != "not found" {
		t.Errorf("PATCH /api/libraries/{id} answers: %+v", patch.Responses)
	}

	// a record answered whole is written as its columns, named apart from what its toOldJSON writes
	if raw := doc.Components.Schemas["LibraryRecord"]; raw == nil || raw.Properties["createdAt"].Format != "date-time" || raw.Properties["settings"].Type != "" {
		t.Errorf("LibraryRecord: %+v", raw)
	}

	cover := doc.Paths["/api/libraries/{id}/cover"]["post"]
	if cover.RequestBody == nil || cover.RequestBody.Content["multipart/form-data"].Schema.Properties["cover"].Format != "binary" {
		t.Errorf("cover upload body: %+v", cover.RequestBody)
	}
	if files := doc.Paths["/api/libraries/{id}/files"]["post"].RequestBody; files == nil || !files.Content["multipart/form-data"].Schema.AdditionalProperties || files.Content["multipart/form-data"].Schema.Properties["folder"] == nil {
		t.Errorf("files upload body: %+v, want a form of any files and a folder", files)
	}
	for code, want := range map[string]string{"302": "status 302", "409": "already there", "500": "the server failed", "201": "status 201"} {
		if got := cover.Responses[code].Description; got != want {
			t.Errorf("cover %s: %q, want %q", code, got, want)
		}
	}

	// a query value the code makes a number is one, a helper handed the query and a name reads it, and the length of a list from a call the
	// scan cannot follow is a count
	search := doc.Paths["/api/libraries/{id}/search"]["get"]
	query := map[string]string{}
	for _, p := range search.Parameters {
		if p.In == "query" {
			query[p.Name] = cmp.Or(p.Schema.Type, p.Schema.Ref)
		}
	}
	if want := map[string]string{"page": "integer", "limit": "integer", "near": "number", "q": "string", "kind": "string", "sort": "#/components/schemas/SessionSort"}; !maps.Equal(query, want) {
		t.Errorf("search query: %v, want %v", query, want)
	}
	searched := search.Responses["200"].Content["application/json"].Schema
	if total := searched.Properties["total"]; total == nil || total.Type != "integer" {
		t.Errorf("search total: %+v, want a whole number", total)
	}

	// a value of the request checked against text written out takes those values, and so does an answer field only ever given codes, each set a
	// record of its own named by the table; one value written out, or words for a person, is no set
	if kind := doc.Paths["/api/libraries/{id}/notes"]["post"].RequestBody.Content["application/json"].Schema.Properties["mediaItemType"]; kind == nil || kind.Ref != "#/components/schemas/MediaItemType" {
		t.Errorf("notes mediaItemType: %+v, want a set of values", kind)
	}
	if key := searched.Properties["stringKey"]; key == nil || key.Ref != "#/components/schemas/ChapterSearchErrorKey" || searched.Properties["note"].Ref != "" || searched.Properties["note"].Enum != nil {
		t.Errorf("search stringKey: %+v, note %+v; want the two codes a set, and no set for words", key, searched.Properties["note"])
	}
	for name, want := range map[string][]string{"SessionSort": {"displayTitle", "timeListening"}, "MediaItemType": {"book", "podcastEpisode"}, "ChapterSearchErrorKey": {"MessageChaptersNotFound", "MessageInvalidAsin"}} {
		if set := doc.Components.Schemas[name]; set == nil || set.Type != "string" || !slices.Equal(set.Enum, want) {
			t.Errorf("%s: %+v, want %q", name, set, want)
		}
	}

	// a 204 has no body, though the code sends one with sendStatus
	if notes := doc.Paths["/api/libraries/{id}/notes"]["post"].Responses["204"]; notes.Description != "no content" || notes.Content != nil {
		t.Errorf("notes 204: %+v, want no content", notes)
	}

	// a body used as a list is one, and what a list has of its own is not a field of it
	if order := doc.Paths["/api/libraries/order"]["post"].RequestBody.Content["application/json"].Schema; order.Type != "array" || order.Items == nil {
		t.Errorf("order body: %+v, want a list", order)
	}

	download := doc.Paths["/api/items/{id}/download"]["get"]
	if download.Responses["200"].Content["application/octet-stream"].Schema == nil {
		t.Errorf("download: %+v, want a file", download.Responses)
	}

	for _, line := range []string{"Audiobookshelf 9.9.9: 15 routes, 16 operations", "answer a file", "unions stated", "no operation for the union stated for GET /api/libraries/{id}/personalized", "/api/items/{id}/play/{episodeId}", "wrote " + filepath.Join(out, "audiobookshelf-openapi-9.9.9.json")} {
		if !strings.Contains(report.String(), line) {
			t.Errorf("report has no %q:\n%s", line, report.String())
		}
	}
}

func TestSpecTrace(t *testing.T) {
	t.Parallel()

	var report bytes.Buffer
	if err := spec([]string{"-trace", "LibraryController.findOne", writeFakeSource(t)}, t.TempDir(), &report); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(report.String(), "GET /api/libraries/:id 200:\n  record Library\n") || !strings.Contains(report.String(), ".extra\n") {
		t.Errorf("trace:\n%s", report.String())
	}
}

func TestSpecRefuses(t *testing.T) {
	t.Parallel()

	empty := t.TempDir()
	file := filepath.Join(empty, "a-file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	broken := writeFakeSource(t)
	if err := os.WriteFile(filepath.Join(broken, "server", "Broken.js"), []byte("class {"), 0o600); err != nil {
		t.Fatal(err)
	}

	for name, args := range map[string][]string{
		"no checkout":                {"-v"},
		"two checkouts":              {empty, empty},
		"a folder that is not there": {filepath.Join(empty, "missing")},
		"a file":                     {file},
		"no package.json":            {empty},
		"a file that does not parse": {broken},
	} {
		if err := spec(args, t.TempDir(), &bytes.Buffer{}); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestPackageVersion(t *testing.T) {
	t.Parallel()

	if v, err := packageVersion([]byte(`{"version": "2.37.1"}`)); err != nil || v != "2.37.1" {
		t.Errorf("packageVersion = %q, %v", v, err)
	}

	for _, b := range []string{`{}`, `not json`} {
		if _, err := packageVersion([]byte(b)); err == nil {
			t.Errorf("packageVersion(%s): no error", b)
		}
	}
}

func TestSpecFillsFromAnswers(t *testing.T) {
	t.Parallel()

	src, out, saved := writeFakeSource(t), t.TempDir(), t.TempDir()
	search := `{"operation": "LibrarySearch", "method": "GET", "path": "/api/libraries/{id}/search", "answers": {"200": [{"results": [{"id": "a", "n": 1}], "total": 2.5, "page": 0, "limit": 10}]}}`
	if err := os.WriteFile(filepath.Join(saved, "LibrarySearch.json"), []byte(search), 0o600); err != nil {
		t.Fatal(err)
	}

	var report bytes.Buffer
	if err := spec([]string{"-v", "-answers", saved, src}, out, &report); err != nil {
		t.Fatal(err)
	}

	b, err := fs.ReadFile(os.DirFS(out), "audiobookshelf-openapi-9.9.9.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc document
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}

	answer := doc.Paths["/api/libraries/{id}/search"]["get"].Responses["200"].Content["application/json"].Schema
	if results := answer.Properties["results"]; results.Type != "array" || results.Items.Properties["id"].Type != "string" || results.Items.Properties["n"].Type != "number" {
		t.Errorf("results, which the source cannot type: %+v, want a list typed from the answer", results)
	}
	if total := answer.Properties["total"]; total.Type != "number" {
		t.Errorf("total, a count that came back with a fraction: %+v, want a number", total)
	}

	for _, line := range []string{"answers from " + saved + "   1 files: filled ", "corrected: GET /api/libraries/{id}/search answer.total: the server sent a fraction"} {
		if !strings.Contains(report.String(), line) {
			t.Errorf("report has no %q:\n%s", line, report.String())
		}
	}
}

// A fixed set of values is two or more codes a program compares against; one value written out is a default, and words for a person are no set.
func TestCodes(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name   string
		values []string
		want   []string
	}{
		{"codes, sorted", []string{"podcastEpisode", "book"}, []string{"book", "podcastEpisode"}},
		{"codes with dashes", []string{"recent-series", "continue-listening"}, []string{"continue-listening", "recent-series"}},
		{"one value", []string{"book"}, nil},
		{"words for a person", []string{"Library not found", "Media item not found"}, nil},
		{"a code beside words", []string{"book", "Not a book"}, nil},
		{"none", nil, nil},
	} {
		if got := codes(tt.values); !slices.Equal(got, tt.want) {
			t.Errorf("%s: %q, want %q", tt.name, got, tt.want)
		}
	}
}
