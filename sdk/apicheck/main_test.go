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

// clientGo is a cut of the client: each helper, a path put in a variable first, and a path handed to a helper with no verb.
const clientGo = `package abs

func (c *Client) a(ctx context.Context, id string) error {
	if err := c.get(ctx, "/api/libraries", q, &out); err != nil {
		return err
	}
	if err := c.do(ctx, http.MethodPatch, "/api/me/progress/"+url.PathEscape(id), q, body, &out); err != nil {
		return err
	}
	path := "/api/items/" + url.PathEscape(id)
	if err := c.del(ctx, path, q); err != nil {
		return err
	}
	return c.openPlaybackSession(ctx, "/api/items/"+url.PathEscape(id)+"/play/", body)
}
`

// clientTestGo is a test file, whose paths do not count.
const clientTestGo = `package abs

func f(c *Client) { c.post(ctx, "/api/libraries", q, body, &out) }
`

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

func TestClientPaths(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "client.go"), clientGo)
	writeFile(t, filepath.Join(dir, "client_test.go"), clientTestGo)
	writeFile(t, filepath.Join(dir, "notes.txt"), `c.get(ctx, "/api/notes", q, &out)`)

	got, err := clientPaths(dir)
	if err != nil {
		t.Fatal(err)
	}

	want := map[route]bool{
		{"GET", "/api/libraries"}:         true,
		{"PATCH", "/api/me/progress/:id"}: true,
		{"DELETE", ":id"}:                 true,
		{anyVerb, "/api/libraries"}:       true,
		{anyVerb, "/api/me/progress/:id"}: true,
		{anyVerb, "/api/items/:id"}:       true,
		{anyVerb, "/api/items/:id/play"}:  true,
	}
	if !maps.Equal(got, want) {
		t.Errorf("clientPaths:\n got %v\nwant %v", got, want)
	}
}

func TestClientPathsNoDir(t *testing.T) {
	t.Parallel()

	if _, err := clientPaths(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("clientPaths of a folder that is not there: no error")
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

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "client.go"), clientGo)

	t.Run("gaps listed", func(t *testing.T) {
		t.Parallel()

		var out bytes.Buffer
		missing, err := check{ref: "v1", router: srv.URL + "/routes.js", dir: dir, list: true}.run(t.Context(), &out)
		if err != nil {
			t.Fatal(err)
		}

		want := `Audiobookshelf v1: 6 routes
  covered by sdk/abs-byhand     5   83%
  not implemented        1

not implemented:
  PUT    /api/series/:id
`
		if missing != 1 || out.String() != want {
			t.Errorf("run: %d missing\n%s\nwant 1 missing\n%s", missing, out.String(), want)
		}
	})

	t.Run("gaps counted", func(t *testing.T) {
		t.Parallel()

		var out bytes.Buffer
		if _, err := (check{ref: "v1", router: srv.URL + "/routes.js", dir: dir}).run(t.Context(), &out); err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(out.Bytes(), []byte("not implemented:")) {
			t.Errorf("run without list listed the gaps:\n%s", out.String())
		}
	})

	t.Run("refusals", func(t *testing.T) {
		t.Parallel()

		for name, c := range map[string]check{
			"no route table":   {router: srv.URL + "/missing.js", dir: dir},
			"no routes in it":  {router: srv.URL + "/empty.js", dir: dir},
			"no client source": {router: srv.URL + "/routes.js", dir: filepath.Join(dir, "missing")},
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
