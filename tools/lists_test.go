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

// A playlist copied from a collection takes the description asked for, not
// only when a new name is asked for too; a blank name keeps the collection's.
func TestPlaylistFromCollectionKeepsTheDescription(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /api/collections/"+colC, `{"id":"`+colC+`","libraryId":"`+libID+`","name":"Shelf","description":"the shelf's"}`)
	f.json("GET /api/libraries/"+libID+"/playlists", `{"results":[]}`)
	f.json("POST /api/playlists/collection/"+colC, `{"id":"`+playlistP+`","libraryId":"`+libID+`","name":"Shelf","description":"the shelf's","items":[]}`)
	f.json("PATCH /api/playlists/"+playlistP, `{"id":"`+playlistP+`","libraryId":"`+libID+`","name":"Shelf","description":"mine","items":[]}`)
	call := toolCaller(t, f)

	for _, name := range []string{"", "shelf"} {
		out, err := call("playlist_create", map[string]any{"from_collection": colC, "name": name, "description": "mine"})
		if err != nil {
			t.Fatal(err)
		}
		if str(t, out["description"]) != "mine" {
			t.Errorf("name %q: description = %v, want mine", name, out["description"])
		}
	}
	sent := f.requests("/api/playlists/" + playlistP)
	if len(sent) != 2 {
		t.Fatalf("sent %v, want the description set on each copy", sent)
	}
	for _, r := range sent {
		if r.Body != `{"description":"mine"}` {
			t.Errorf("sent %s, want the description alone", r.Body)
		}
	}

	if _, err := call("playlist_create", map[string]any{"from_collection": colC, "name": "   "}); err != nil {
		t.Fatal(err)
	}
	if got := f.requests("/api/playlists/" + playlistP); len(got) != 2 {
		t.Errorf("a blank name was sent as a rename: %v", got[len(got)-1])
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

// Audiobookshelf deletes a playlist its last entry leaves, so emptying one is
// a delete and needs the delete tools switched on, as playlist_delete does;
// otherwise it was a way round --enable-delete.
func TestEmptyingAPlaylistNeedsDeletesOn(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	shelfRoutes(f)
	f.json("GET /api/playlists/"+playlistP, `{"id":"`+playlistP+`","libraryId":"`+libID+`","name":"Books List","items":[{"libraryItemId":"`+bookB1+`"}]}`)
	f.json("POST /api/playlists/"+playlistP+"/batch/remove", `{}`)
	call := callerWith(t, f, Options{})

	_, err := call("playlist_entries_edit", map[string]any{"playlist": playlistP, "action": "remove", "entries": []any{map[string]any{"item": bookB1}}})
	if err == nil || !strings.Contains(err.Error(), "--enable-delete") {
		t.Errorf("emptying a playlist with deletes off: %v", err)
	}
	if got := f.requests("/api/playlists/" + playlistP + "/batch/remove"); len(got) != 0 {
		t.Errorf("the removal was sent: %v", got)
	}
}
