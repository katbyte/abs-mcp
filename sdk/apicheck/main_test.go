package main

import (
	"bytes"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// routerJS is a cut of the server's route table: verbs in lower case, a route over two lines, one written twice, and trailing optional parameters.
const routerJS = `
    this.router.get('/libraries', LibraryController.findAll.bind(this))
    this.router.post('/libraries', LibraryController.create.bind(this))
    this.router.get(
      '/items/:id/play/:episodeId?', LibraryItemController.startPlaybackSession.bind(this))
    this.router.delete('/items/:id', LibraryItemController.delete.bind(this))
    this.router.get('/libraries', LibraryController.findAll.bind(this))
    this.router.patch('/me/progress/:libraryItemId/:episodeId?', MeController.createUpdateMediaProgress.bind(this))
    this.router.put('/series/:id', SeriesController.update.bind(this))
`

// documentJSON is a cut of the API document: a parameter in braces, a route with and without its optional parameter, and a route the server
// does not route through the API router.
const documentJSON = `{"paths": {
  "/api/libraries": {"get": {}, "post": {}},
  "/api/items/{id}": {"delete": {}},
  "/api/items/{id}/play": {"get": {}},
  "/api/items/{id}/play/{episodeId}": {"get": {}},
  "/api/me/progress/{libraryItemId}/{episodeId}": {"patch": {}},
  "/status": {"get": {}}
}}`

func TestServerRoutes(t *testing.T) {
	t.Parallel()

	want := []route{
		{"DELETE", "/api/items/:id"},
		{"GET", "/api/items/:id/play/:episodeId?"},
		{"GET", "/api/libraries"},
		{"POST", "/api/libraries"},
		{"PATCH", "/api/me/progress/:libraryItemId/:episodeId?"},
		{"PUT", "/api/series/:id"},
	}
	if got := serverRoutes(routerJS); !slices.Equal(got, want) {
		t.Errorf("serverRoutes:\n got %v\nwant %v", got, want)
	}
}

func TestVariants(t *testing.T) {
	t.Parallel()

	cases := map[string][]string{
		"/api/libraries":                        {"/api/libraries"},
		"/api/items/:id":                        {"/api/items/:id"},
		"/api/items/:id/play/:episodeId?":       {"/api/items/:id/play/:id", "/api/items/:id/play"},
		"/api/me/progress/:a/:b?/:c?":           {"/api/me/progress/:id/:id/:id", "/api/me/progress/:id/:id", "/api/me/progress/:id"},
		"/api/items/:id?/cover":                 {"/api/items/:id/cover"},
		"/api/":                                 {"/api"},
		"/":                                     {"/"},
		"/api/podcasts/:id/episode/:episodeId?": {"/api/podcasts/:id/episode/:id", "/api/podcasts/:id/episode"},
	}
	for path, want := range cases {
		if got := variants(path); !slices.Equal(got, want) {
			t.Errorf("variants(%q):\n got %q\nwant %q", path, got, want)
		}
	}
}

func TestDocumentRoutes(t *testing.T) {
	t.Parallel()

	file := filepath.Join(t.TempDir(), "document.json")
	writeFile(t, file, documentJSON)

	got, err := documentRoutes(file)
	if err != nil {
		t.Fatal(err)
	}

	want := map[route]bool{
		{"GET", "/api/libraries"}:             true,
		{"POST", "/api/libraries"}:            true,
		{"DELETE", "/api/items/:id"}:          true,
		{"GET", "/api/items/:id/play"}:        true,
		{"GET", "/api/items/:id/play/:id"}:    true,
		{"PATCH", "/api/me/progress/:id/:id"}: true,
		{"GET", "/status"}:                    true,
	}
	if !maps.Equal(got, want) {
		t.Errorf("documentRoutes:\n got %v\nwant %v", got, want)
	}
}

func TestDocumentRoutesRefuses(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	for name, content := range map[string]string{"empty.json": `{"paths": {}}`, "broken.json": `{"paths":`} {
		writeFile(t, filepath.Join(dir, name), content)
	}
	for _, name := range []string{"missing.json", "empty.json", "broken.json"} {
		if _, err := documentRoutes(filepath.Join(dir, name)); err == nil {
			t.Errorf("documentRoutes of %s: no error", name)
		}
	}
}

func TestRun(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/routes.js":
			_, _ = w.Write([]byte(routerJS))
		case "/empty.js":
			_, _ = w.Write([]byte("module.exports = {}"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	document := filepath.Join(t.TempDir(), "document.json")
	writeFile(t, document, documentJSON)

	t.Run("gaps listed", func(t *testing.T) {
		t.Parallel()

		var out bytes.Buffer
		missing, err := check{ref: "v1", router: srv.URL + "/routes.js", document: document, list: true}.run(t.Context(), &out)
		if err != nil {
			t.Fatal(err)
		}

		want := `Audiobookshelf v1: 6 routes
  in the API document      5   83%
  not in it                1

not in the API document:
  PUT    /api/series/:id
`
		if missing != 1 || out.String() != want {
			t.Errorf("run: %d missing\n%s\nwant 1 missing\n%s", missing, out.String(), want)
		}
	})

	t.Run("gaps counted", func(t *testing.T) {
		t.Parallel()

		var out bytes.Buffer
		if _, err := (check{ref: "v1", router: srv.URL + "/routes.js", document: document}).run(t.Context(), &out); err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(out.Bytes(), []byte("not in the API document:")) {
			t.Errorf("run without list listed the gaps:\n%s", out.String())
		}
	})

	t.Run("refusals", func(t *testing.T) {
		t.Parallel()

		for name, c := range map[string]check{
			"no route table":  {router: srv.URL + "/missing.js", document: document},
			"no routes in it": {router: srv.URL + "/empty.js", document: document},
			"no document":     {router: srv.URL + "/routes.js", document: document + ".missing"},
		} {
			if _, err := c.run(t.Context(), &bytes.Buffer{}); err == nil {
				t.Errorf("%s: no error", name)
			}
		}
	})
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()

	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
