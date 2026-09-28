package tools

import (
	"slices"
	"strings"
	"testing"

	"github.com/katbyte/abs-mcp/lib/abs"
)

// The server joins genres with "; " on embed and writes an m4b's publisher
// only as its copyright, so neither could ever read back as embedded.
func TestEmbeddedReadsTheTagsTheServerWrites(t *testing.T) {
	t.Parallel()

	m := &abs.Metadata{Title: "X", Genres: []string{"Mystery, Thriller & Suspense", "Fiction"}, Publisher: "Tantor"}
	genre := "Mystery, Thriller & Suspense; Fiction"
	if got := embedMismatches(m, &abs.AudioFile{MimeType: "audio/mp4", Metadata: abs.FileMetadata{Ext: ".m4b"}, MetaTags: map[string]string{"tagTitle": "X", "tagGenre": genre}}); len(got) != 0 {
		t.Errorf("an m4b embedded by the server = %v, want nothing stale", got)
	}
	mp3 := &abs.AudioFile{MimeType: "audio/mpeg", MetaTags: map[string]string{"tagTitle": "X", "tagGenre": genre}}
	if got := embedMismatches(m, mp3); !slices.Equal(got, []string{"no publisher tag"}) {
		t.Errorf("an mp3 without its publisher = %v, want the publisher missing", got)
	}
	mp3.MetaTags["tagPublisher"] = "Tantor"
	if got := embedMismatches(m, mp3); len(got) != 0 {
		t.Errorf("an mp3 embedded by the server = %v, want nothing stale", got)
	}
	// a genre really split three ways is still stale, and another tool's
	// slashes still read as a list
	if sameList("Mystery; Thriller & Suspense; Fiction", m.Genres) {
		t.Error("three genres read as the two the item has")
	}
	if !sameList("Fiction/Classic", []string{"Classic", "Fiction"}) {
		t.Error("a slash-separated tag no longer reads as a list")
	}
}

// audit_unembedded reads the tags off the expanded items, fetched in batches:
// a book with nothing in its files, one whose tags are behind an edit, and
// one that was embedded after its last edit.
func TestAuditUnembeddedReadsFileTags(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/libraries/"+libID+"/items", page(
		item("i1", "Dune", "", `"numAudioFiles":2`),
		item("i2", "Neuromancer", "", `"numAudioFiles":1`),
		item("i3", "Foundation", "", `"numAudioFiles":1`),
		item("i4", "Ebook Only", "", ""),
	))
	expanded := func(id, title, tags string) string {
		return `{"id":"` + id + `","libraryId":"` + libID + `","mediaType":"book","relPath":"A/` + title + `",` +
			`"media":{"metadata":{"title":"` + title + `","authorName":"Frank Herbert","narratorName":"Scott Brick","seriesName":"Dune #1","genres":["Science Fiction","Classic"],"publishedYear":"1965"},` +
			`"audioFiles":[{"index":1,"metaTags":{` + tags + `}},{"index":2,"metaTags":{` + tags + `}}]}}`
	}
	f.json("POST /api/items/batch/get", `{"libraryItems":[`+
		expanded("i1", "Dune", `"tagTrack":"1","tagEncoder":"LAME"`)+","+
		expanded("i2", "Neuromancer", `"tagTitle":"Neuromancer","tagArtist":"William Gibson","tagGenre":"Science Fiction","tagDate":"1965"`)+","+
		expanded("i3", "Foundation", `"tagTitle":"Foundation","tagAlbumArtist":"frank herbert","tagComposer":"Scott Brick","tagGrouping":"Dune #1","tagGenre":"Classic; Science Fiction","tagDate":"1965-01-01"`)+
		`]}`)
	call := toolCaller(t, f)

	out, err := call("audit_unembedded", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := num(t, out["items_scanned"]); got != 3 {
		t.Errorf("items_scanned = %d, want the 3 books with audio", got)
	}
	if got := num(t, out["total_findings"]); got != 2 {
		t.Errorf("total_findings = %d, want 2", got)
	}
	detail := map[string]string{}
	for _, row := range list(t, out["findings"]) {
		detail[str(t, row["id"])] = str(t, row["detail"])
	}
	if !strings.HasPrefix(detail["i1"], "2 of 2 files carry no tags") {
		t.Errorf("untagged book: %q", detail["i1"])
	}
	for _, want := range []string{`author "William Gibson" vs "Frank Herbert"`, "no narrator tag", "no series tag", `genres "Science Fiction" vs "Science Fiction; Classic"`} {
		if !strings.Contains(detail["i2"], want) {
			t.Errorf("stale book lacks %q: %q", want, detail["i2"])
		}
	}
	if _, flagged := detail["i3"]; flagged {
		t.Errorf("an embedded book was flagged: %q", detail["i3"])
	}

	// the ebook-only item was never asked for
	batches := f.requests("/api/items/batch/get")
	if len(batches) != 1 || strings.Contains(batches[0].Body, `"i4"`) || !strings.Contains(batches[0].Body, `"i3"`) {
		t.Errorf("batch request %v", batches)
	}
}

// The per-file comparison, on the shapes an embed and a ripper leave behind.
func TestEmbedMismatches(t *testing.T) {
	t.Parallel()

	m := &abs.Metadata{Title: "Dune", AuthorName: "Frank Herbert", Genres: []string{"SF"}, PublishedYear: "1965"}
	af := func(tags map[string]string) *abs.AudioFile { return &abs.AudioFile{MetaTags: tags} }

	if got := embedMismatches(m, af(map[string]string{"tagTitle": "Chapter 1", "tagAlbum": "Dune: A Novel", "tagArtist": "FRANK HERBERT", "tagGenre": "sf", "tagDate": "1965"})); len(got) != 0 {
		t.Errorf("a chaptered track under the right album was flagged: %v", got)
	}
	got := embedMismatches(m, af(map[string]string{"tagTitle": "Dune", "tagArtist": "Unknown"}))
	if !slices.Equal(got, []string{`author "Unknown" vs "Frank Herbert"`, "no genres tag", "no year tag"}) {
		t.Errorf("mismatches = %v", got)
	}
	// fields the item lacks are not expected in the file
	if got := embedMismatches(&abs.Metadata{Title: "Dune"}, af(map[string]string{"tagTitle": "Dune"})); len(got) != 0 {
		t.Errorf("absent metadata demanded a tag: %v", got)
	}

	if !tagsEmpty(af(map[string]string{"tagTrack": "3", "tagEncoder": "LAME"})) || tagsEmpty(af(map[string]string{"tagAlbum": "x"})) {
		t.Error("tagsEmpty: ripper leftovers count as tags, or an album does not")
	}
	for tag, want := range map[string]bool{"SF; Classic": true, "classic/sf": true, "SF": false, "SF; Classic; Space Opera": false} {
		if got := sameList(tag, []string{"SF", "Classic"}); got != want {
			t.Errorf("sameList(%q) = %v", tag, got)
		}
	}
}
