package main

import (
	"slices"
	"testing"
)

// analyzed runs the scan over every route of the fake source, by verb and path.
func analyzed(t *testing.T) map[string]struct {
	ans *answer
	req *reqInfo
} {
	t.Helper()

	e := fakeProgram(t)
	out := map[string]struct {
		ans *answer
		req *reqInfo
	}{}
	for _, r := range readRoutes(e) {
		ans, req := e.analyze(r)
		out[r.verb+" "+r.path] = struct {
			ans *answer
			req *reqInfo
		}{ans, req}
	}

	return out
}

func TestAnalyze(t *testing.T) {
	t.Parallel()

	routes := analyzed(t)

	t.Run("a list of records, and the query it reads", func(t *testing.T) {
		t.Parallel()

		r := routes["get /api/libraries"]
		body := r.ans.json[200]
		if body == nil || body.kind != objectKind {
			t.Fatalf("answer: %+v", body)
		}

		results := body.field("results")
		if results == nil || results.kind != arrayKind || results.elem.name != "Library" {
			t.Errorf("results: %+v, want a list of Library", results)
		}
		if total := body.field("total"); total == nil || total.kind != integerKind {
			t.Errorf("total: %+v, want a whole number", total)
		}
		if limit := body.field("limit"); limit == nil || limit.kind != numberKind {
			t.Errorf("limit, set after the record was made: %+v, want a number", limit)
		}
		if !slices.Equal(r.req.query, []string{"limit", "page"}) {
			t.Errorf("query: %q, want limit and page", r.req.query)
		}
	})

	t.Run("middleware answers, and a field added to a named record", func(t *testing.T) {
		t.Parallel()

		r := routes["get /api/libraries/:id"]
		if !slices.Equal(r.ans.statuses(), []int{200, 403, 404}) {
			t.Errorf("statuses: %v, want 200, 403 and 404", r.ans.statuses())
		}

		body := r.ans.json[200]
		if body == nil || body.name != "Library" || body.field("extra") == nil {
			t.Errorf("answer: %+v, want Library with extra", body)
		}
		if folders := body.field("folders"); folders == nil || folders.kind != arrayKind || folders.elem.name != "Folder" {
			t.Errorf("folders, a link to many: %+v", folders)
		}
	})

	t.Run("a body handed whole to a record's method", func(t *testing.T) {
		t.Parallel()

		r := routes["patch /api/libraries/:id"]
		if !slices.Equal(r.req.body, []string{"displayOrder", "name"}) {
			t.Errorf("body: %q, want displayOrder and name", r.req.body)
		}
		if !r.ans.status[200] {
			t.Errorf("answer: %+v, want sendStatus(200)", r.ans)
		}
	})

	t.Run("a body read through a name it is given, and looped over", func(t *testing.T) {
		t.Parallel()

		r := routes["post /api/libraries/:id/notes"]
		if !slices.Equal(r.req.body, []string{"text"}) || !r.req.bodyWhole {
			t.Errorf("body: %q, whole %v, want text read through payload, and every field read by the loop", r.req.body, r.req.bodyWhole)
		}
	})

	t.Run("a body used as a list", func(t *testing.T) {
		t.Parallel()

		r := routes["post /api/libraries/order"]
		if len(r.req.body) != 0 || !r.req.bodyList {
			t.Errorf("body: %q, list %v, want a list and no fields called length or forEach", r.req.body, r.req.bodyList)
		}
	})

	t.Run("an answer written by a manager the server made", func(t *testing.T) {
		t.Parallel()

		r := routes["get /api/items/:id/play/:episodeId?"]
		body := r.ans.json[200]
		if body == nil || body.field("started") == nil || body.field("started").kind != integerKind {
			t.Errorf("answer: %+v, want started as a whole number", body)
		}
		if !r.ans.text[400] {
			t.Errorf("400: %+v, want text", r.ans)
		}
		if !slices.Equal(r.req.query, []string{"force"}) {
			t.Errorf("query: %q, want force", r.req.query)
		}
	})

	t.Run("an upload, and every status it answers", func(t *testing.T) {
		t.Parallel()

		r := routes["post /api/libraries/:id/cover"]
		if !slices.Equal(r.req.files, []string{"cover"}) {
			t.Errorf("files: %q, want cover", r.req.files)
		}
		if !slices.Equal(r.req.query, []string{"back", "kind", "wait"}) {
			t.Errorf("query: %q, want back, kind and wait", r.req.query)
		}
		if !slices.Equal(r.ans.statuses(), []int{201, 302, 400, 403, 404, 409, 500}) {
			t.Errorf("statuses: %v", r.ans.statuses())
		}
		if !r.ans.empty[201] || !r.ans.text[409] {
			t.Errorf("answer: %+v, want nothing at 201 and text at 409", r.ans)
		}
	})

	t.Run("a record answered as it is", func(t *testing.T) {
		t.Parallel()

		body := routes["get /api/libraries/:id/raw"].ans.json[200]
		if body == nil || body.kind != instanceKind || body.class.name != "Library" {
			t.Errorf("answer: %+v, want the Library itself", body)
		}
	})

	t.Run("a method called on either of two records", func(t *testing.T) {
		t.Parallel()

		body := routes["get /api/libraries/:id/label"].ans.json[200]
		if body == nil || body.field("label") == nil || body.field("label").kind != stringKind {
			t.Errorf("answer: %+v, want label as text", body)
		}
	})

	t.Run("a file", func(t *testing.T) {
		t.Parallel()

		if r := routes["get /api/items/:id/download"]; !r.ans.file {
			t.Errorf("answer: %+v, want a file", r.ans)
		}
	})

	t.Run("routes written in place", func(t *testing.T) {
		t.Parallel()

		body := routes["get /status"].ans.json[200]
		if body == nil || body.field("isInit").kind != booleanKind || body.field("version").kind != stringKind {
			t.Errorf("status: %+v", body)
		}
		if !routes["get /healthcheck"].ans.status[200] {
			t.Errorf("healthcheck: %+v", routes["get /healthcheck"].ans)
		}
	})
}
