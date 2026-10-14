package main

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSpec(t *testing.T) {
	t.Parallel()

	src, client, out := writeFakeSource(t), writeFS(t, fakeClient), t.TempDir()

	var report bytes.Buffer
	if err := spec([]string{"-v", "-client", client, src}, out, &report); err != nil {
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
	for code, want := range map[string]string{"302": "status 302", "409": "already there", "500": "the server failed", "201": "status 201"} {
		if got := cover.Responses[code].Description; got != want {
			t.Errorf("cover %s: %q, want %q", code, got, want)
		}
	}

	// a query value the code makes a number is one, and the length of a list from a call the scan cannot follow is a count
	search := doc.Paths["/api/libraries/{id}/search"]["get"]
	query := map[string]string{}
	for _, p := range search.Parameters {
		if p.In == "query" {
			query[p.Name] = p.Schema.Type
		}
	}
	if want := map[string]string{"page": "integer", "limit": "integer", "near": "number", "q": "string"}; !maps.Equal(query, want) {
		t.Errorf("search query: %v, want %v", query, want)
	}
	if total := search.Responses["200"].Content["application/json"].Schema.Properties["total"]; total == nil || total.Type != "integer" {
		t.Errorf("search total: %+v, want a whole number", total)
	}

	download := doc.Paths["/api/items/{id}/download"]["get"]
	if download.Responses["200"].Content["application/octet-stream"].Schema == nil {
		t.Errorf("download: %+v, want a file", download.Responses)
	}

	for _, line := range []string{"Audiobookshelf 9.9.9: 11 routes, 12 operations", "answer a file", "/api/items/{id}/play/{episodeId}", "wrote " + filepath.Join(out, "audiobookshelf-openapi-9.9.9.json")} {
		if !strings.Contains(report.String(), line) {
			t.Errorf("report has no %q:\n%s", line, report.String())
		}
	}
}

func TestSpecTrace(t *testing.T) {
	t.Parallel()

	var report bytes.Buffer
	if err := spec([]string{"-client", "", "-trace", "LibraryController.findOne", writeFakeSource(t)}, t.TempDir(), &report); err != nil {
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
		"no checkout":                {"-client", ""},
		"a client that is not there": {"-client", filepath.Join(empty, "missing"), writeFakeSource(t)},
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

// What the source leaves untyped the hand-written client types; what the two type differently stays as the source has it.
func TestSpecFillsFromClient(t *testing.T) {
	t.Parallel()

	src, client, out := writeFakeSource(t), writeFS(t, fakeClient), t.TempDir()

	var report bytes.Buffer
	if err := spec([]string{"-v", "-client", client, src}, out, &report); err != nil {
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

	// a JSON column the source cannot type, read by the client into a struct
	settings := doc.Components.Schemas["Library"].Properties["settings"]
	if settings.Type != "object" || settings.Properties["coverAspectRatio"].Type != "integer" || !settings.Properties["skipMatchingMediaWithAsin"].Nullable || settings.Properties["Ratio"].Type != "number" {
		t.Errorf("Library.settings: %+v", settings)
	}
	if _, ok := doc.Components.Schemas["Library"].Properties["raw"]; ok {
		t.Error("a field the client reads that the source never writes was added")
	}

	body := doc.Paths["/api/libraries/{id}"]["patch"].RequestBody.Content["application/json"].Schema
	if body.Properties["name"].Type != "string" || body.Properties["displayOrder"].Type != "integer" {
		t.Errorf("PATCH body: %+v %+v", body.Properties["name"], body.Properties["displayOrder"])
	}

	if isInit := doc.Paths["/status"]["get"].Responses["200"].Content["application/json"].Schema.Properties["isInit"]; isInit.Type != "boolean" {
		t.Errorf("isInit: %+v, want the source's boolean", isInit)
	}

	for _, line := range []string{"filled from " + client + "   ", "; 1 it types otherwise are left as the source has them; 1 of its requests match no operation", "differs: GET /status answer.isInit: the source says boolean, the client integer", "no operation for GET /api/nothing (Nothing)"} {
		if !strings.Contains(report.String(), line) {
			t.Errorf("report has no %q:\n%s", line, report.String())
		}
	}
}
