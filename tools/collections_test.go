package tools

import (
	"io"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
)

// The same for collections, which the server does not delete when emptied,
// and which refuse a book from another library or a podcast by name.
func TestCollectionBooksEditReportsWhatChanged(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	shelfRoutes(f)
	f.json("GET /api/collections/"+colC, `{"id":"`+colC+`","libraryId":"`+libID+`","name":"Shelf","books":[{"id":"`+bookB1+`"}]}`)
	f.json("POST /api/collections/"+colC+"/batch/add", `{"id":"`+colC+`","libraryId":"`+libID+`","name":"Shelf","books":[{"id":"`+bookB1+`"},{"id":"`+bookB2+`"}]}`)
	call := toolCaller(t, f)

	out, err := call("collection_books_edit", map[string]any{"collection": colC, "action": "add", "items": []any{bookB1, bookB2, bookB2}})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(strs(t, out["added"]), []string{"Second"}) || !slices.Equal(strs(t, out["already_held"]), []string{"First"}) || num(t, out["books"]) != 2 {
		t.Errorf("out = %v", out)
	}
	if adds := f.requests("/api/collections/" + colC + "/batch/add"); len(adds) != 1 || strings.Contains(adds[0].Body, bookB1) {
		t.Errorf("batch add sent %v, want only the new book, once", adds)
	}
	out, err = call("collection_books_edit", map[string]any{"collection": colC, "action": "remove", "items": []any{bookB3}})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(strs(t, out["not_held"]), []string{"Third"}) || len(f.requests("/api/collections/"+colC+"/batch/remove")) != 0 {
		t.Errorf("removing what is not held: %v", out)
	}
	// a podcast cannot really sit in a book library; the check is there anyway
	strayPodcast := "c1c1c1c1-0000-4000-8000-000000000002"
	f.json("GET /api/items/"+strayPodcast, `{"id":"`+strayPodcast+`","libraryId":"`+libID+`","mediaType":"podcast","media":{"metadata":{"title":"Stray"}}}`)
	for ref, want := range map[string]string{otherBook: "another library", strayPodcast: "podcast"} {
		if _, err := call("collection_books_edit", map[string]any{"collection": colC, "action": "add", "items": []any{ref}}); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("adding %s: %v, want %q", ref, err, want)
		}
	}
}

// Deleting a collection, which every account shares, or a playlist is a
// delete: registered only with --enable-delete and marked destructive, in the
// organise set with the rest of the grouping tools.
func TestListDeletesAreDeleteTools(t *testing.T) {
	t.Parallel()

	infos, err := Describe(Options{EnableDelete: true})
	if err != nil {
		t.Fatal(err)
	}
	without := register(t, Options{})
	for _, name := range []string{"collection_delete", "playlist_delete"} {
		i := slices.IndexFunc(infos, func(ti ToolInfo) bool { return ti.Name == name })
		if i < 0 {
			t.Errorf("%s is not registered with --enable-delete", name)
			continue
		}
		if infos[i].Kind != "delete" || infos[i].Toolset != "organise" {
			t.Errorf("%s is a %s tool in %s, want a delete tool in organise", name, infos[i].Kind, infos[i].Toolset)
		}
		if slices.Contains(without, name) {
			t.Errorf("%s is registered without --enable-delete", name)
		}
	}
}

// A delete is read back rather than trusted: gone from the list is deleted,
// and one the server still lists is an error.
func TestListDeletesAreReadBack(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		tool, arg, id, get, del, list, row string
	}{
		{
			tool: "collection_delete", arg: "collection", id: colC,
			get: "/api/collections/" + colC, del: "/api/collections/" + colC, list: "/api/libraries/" + libID + "/collections",
			row: `{"id":"` + colC + `","libraryId":"` + libID + `","name":"Shelf"}`,
		},
		{
			tool: "playlist_delete", arg: "playlist", id: playlistP,
			get: "/api/playlists/" + playlistP, del: "/api/playlists/" + playlistP, list: "/api/libraries/" + libID + "/playlists",
			row: `{"id":"` + playlistP + `","libraryId":"` + libID + `","name":"Shelf"}`,
		},
	} {
		for _, keeps := range []bool{false, true} {
			f := newFakeABS(t)
			var gone atomic.Bool
			f.json("GET "+tc.get, tc.row)
			f.mux.HandleFunc("DELETE "+tc.del, func(w http.ResponseWriter, _ *http.Request) {
				gone.Store(!keeps)
				_, _ = io.WriteString(w, `OK`)
			})
			f.mux.HandleFunc("GET "+tc.list, func(w http.ResponseWriter, _ *http.Request) {
				if gone.Load() {
					_, _ = io.WriteString(w, `{"results":[]}`)
					return
				}
				_, _ = io.WriteString(w, `{"results":[`+tc.row+`]}`)
			})
			out, err := toolCaller(t, f)(tc.tool, map[string]any{tc.arg: tc.id})
			switch {
			case keeps && (err == nil || !strings.Contains(err.Error(), "still listed")):
				t.Errorf("%s on a server that kept it: %v %v, want an error", tc.tool, out, err)
			case !keeps && err != nil:
				t.Errorf("%s: %v", tc.tool, err)
			case !keeps && (str(t, out["deleted"]) != "Shelf" || str(t, out["id"]) != tc.id):
				t.Errorf("%s: %v", tc.tool, out)
			}
		}
	}
}

// A rename to spaces is refused: it would leave a collection or playlist no
// name can reach. A name with spaces round it is sent trimmed.
func TestBlankNamesAreRefused(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /api/collections/"+colC, `{"id":"`+colC+`","libraryId":"`+libID+`","name":"Shelf"}`)
	f.json("GET /api/libraries/"+libID+"/collections", `{"results":[{"id":"`+colC+`","libraryId":"`+libID+`","name":"Shelf"}]}`)
	f.json("PATCH /api/collections/"+colC, `{"id":"`+colC+`","libraryId":"`+libID+`","name":"New"}`)
	f.json("GET /api/playlists/"+playlistP, `{"id":"`+playlistP+`","libraryId":"`+libID+`","name":"Queue"}`)
	f.json("GET /api/libraries/"+libID+"/playlists", `{"results":[{"id":"`+playlistP+`","libraryId":"`+libID+`","name":"Queue"}]}`)
	f.json("PATCH /api/playlists/"+playlistP, `{"id":"`+playlistP+`","libraryId":"`+libID+`","name":"New"}`)
	call := toolCaller(t, f)

	for tool, ref := range map[string]map[string]any{"collection_edit": {"collection": colC}, "playlist_edit": {"playlist": playlistP}} {
		for _, extra := range []map[string]any{{"name": "  "}, {"name": "\t", "description": "d"}} {
			args := maps.Clone(ref)
			maps.Copy(args, extra)
			if _, err := call(tool, args); err == nil || !strings.Contains(err.Error(), "blank") {
				t.Errorf("%s %v: %v, want refused", tool, args, err)
			}
		}
	}
	for _, path := range []string{"/api/collections/" + colC, "/api/playlists/" + playlistP} {
		for _, r := range f.requests(path) {
			if r.Method != http.MethodGet {
				t.Fatalf("a blank name was sent: %s %s %s", r.Method, path, r.Body)
			}
		}
	}

	if _, err := call("collection_edit", map[string]any{"collection": colC, "name": " New "}); err != nil {
		t.Fatal(err)
	}
	if _, err := call("playlist_edit", map[string]any{"playlist": playlistP, "name": " New "}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/collections/" + colC, "/api/playlists/" + playlistP} {
		sent := f.requests(path)
		if last := sent[len(sent)-1]; last.Method != http.MethodPatch || last.Body != `{"name":"New"}` {
			t.Errorf("%s: sent %s %s, want the name trimmed", path, last.Method, last.Body)
		}
	}
}
