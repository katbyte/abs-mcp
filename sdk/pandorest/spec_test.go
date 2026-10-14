package main

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
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
	for code, want := range map[string]string{"302": "status 302", "409": "already there", "500": "the server failed", "201": "status 201"} {
		if got := cover.Responses[code].Description; got != want {
			t.Errorf("cover %s: %q, want %q", code, got, want)
		}
	}

	download := doc.Paths["/api/items/{id}/download"]["get"]
	if download.Responses["200"].Content["application/octet-stream"].Schema == nil {
		t.Errorf("download: %+v, want a file", download.Responses)
	}

	for _, line := range []string{"Audiobookshelf 9.9.9: 10 routes, 11 operations", "answer a file", "/api/items/:id/play/:episodeId", "wrote " + filepath.Join(out, "audiobookshelf-openapi-9.9.9.json")} {
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
		"no checkout":                nil,
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
