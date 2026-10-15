package main

import (
	"slices"
	"testing"
)

func TestReadRoutes(t *testing.T) {
	t.Parallel()

	routes := readRoutes(fakeProgram(t))
	got := make([]string, 0, len(routes))
	for _, r := range routes {
		got = append(got, r.verb+" "+r.path+" "+r.handler)
	}

	// the wildcard route has no OpenAPI form and is left out
	want := []string{
		"get /api/libraries LibraryController.findAll",
		"get /api/libraries/:id LibraryController.findOne",
		"patch /api/libraries/:id LibraryController.update",
		"post /api/libraries/:id/cover LibraryController.uploadCover",
		"get /api/libraries/:id/raw LibraryController.raw",
		"get /api/libraries/:id/label LibraryController.label",
		"get /api/libraries/:id/search LibraryController.search",
		"post /api/libraries/:id/notes LibraryController.notes",
		"post /api/libraries/order LibraryController.order",
		"get /api/items/:id/play/:episodeId? ItemController.play",
		"get /api/items/:id/download ItemController.download",
		"get /status inline",
		"get /healthcheck inline",
	}
	if !slices.Equal(got, want) {
		t.Errorf("routes:\n got %q\nwant %q", got, want)
	}
}

func TestVariants(t *testing.T) {
	t.Parallel()

	cases := map[string][]string{
		"/api/libraries":                   {"/api/libraries"},
		"/api/items/:id/play/:episodeId?":  {"/api/items/:id/play/:episodeId", "/api/items/:id/play"},
		"/api/me/progress/:id/:episodeId?": {"/api/me/progress/:id/:episodeId", "/api/me/progress/:id"},
		"/api/a/:b?/:c?":                   {"/api/a/:b/:c", "/api/a/:b", "/api/a"},
		"/api/items/:id?/cover":            {"/api/items/:id/cover"},
	}
	for in, want := range cases {
		if got := variants(in); !slices.Equal(got, want) {
			t.Errorf("variants(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestOpenAPIPath(t *testing.T) {
	t.Parallel()

	p, params := openAPIPath("/api/items/:id/file/:fileid")
	if p != "/api/items/{id}/file/{fileid}" || !slices.Equal(params, []string{"id", "fileid"}) {
		t.Errorf("openAPIPath = %q %q", p, params)
	}

	if p, params := openAPIPath("/status"); p != "/status" || params != nil {
		t.Errorf("openAPIPath(/status) = %q %q", p, params)
	}
}
