package tools

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/katbyte/abs-mcp/sdk/abs"
	"github.com/katbyte/go-kt/pointer"
)

// track is an audio file of a book at a place in its play order.
func track(ino, rel string, index int, seconds float64) abs.AudioFile {
	return abs.AudioFile{Ino: ino, Index: index, Duration: seconds, Metadata: &abs.FileMetadata{RelPath: rel, Filename: rel[strings.LastIndex(rel, "/")+1:]}}
}

// bookWith is a book holding these audio files.
func bookWith(files ...abs.AudioFile) *abs.LibraryItemExpanded {
	return &abs.LibraryItemExpanded{MediaType: "book", Media: &abs.LibraryItemExpandedMedia{AudioFiles: files, Metadata: &abs.LibraryItemExpandedMediaMetadata{Title: "Dune"}}}
}

func TestTrackOrder(t *testing.T) {
	t.Parallel()

	it := bookWith(track("a", "01.mp3", 1, 10), track("b", "02.mp3", 2, 20), track("c", "intro.mp3", -1, 5))
	it.Media.AudioFiles[2].Exclude = true

	order, listed, err := trackOrder(it, []string{"02.mp3", "intro.mp3", "01.mp3"})
	if err != nil {
		t.Fatal(err)
	}
	sent := make([]string, 0, len(order))
	for _, o := range order {
		sent = append(sent, fmt.Sprintf("%s exclude=%v", o.Ino, pointer.From(o.Exclude)))
	}
	if want := []string{"b exclude=false", "c exclude=true", "a exclude=false"}; !slices.Equal(sent, want) {
		t.Errorf("order = %v, want %v: every file, in the order named, excluded ones kept excluded", sent, want)
	}
	if slices.ContainsFunc(order, func(o abs.LibraryItemUpdateTracksRequestOrderedFileData) bool { return o.Exclude == nil }) {
		t.Error("a file sent without saying whether it is excluded: the server reads that as included")
	}
	if got := trackNames(listed); !slices.Equal(got, []string{"02.mp3", "intro.mp3", "01.mp3"}) {
		t.Errorf("listed = %v", got)
	}

	refused := []struct {
		names []string
		says  []string
	}{
		// the server would drop the file left out from the book
		{[]string{"02.mp3", "01.mp3"}, []string{"leaves out", `"intro.mp3"`, "all 3"}},
		{[]string{"01.mp3", "01.mp3", "02.mp3", "intro.mp3"}, []string{"twice", "01.mp3"}},
		{[]string{"01.mp3", "03.mp3", "02.mp3", "intro.mp3"}, []string{`"03.mp3"`, `"01.mp3"`}},
	}
	for _, c := range refused {
		_, _, err := trackOrder(it, c.names)
		wantErr(t, strings.Join(c.names, ","), err, c.says...)
	}
}

// Disc folders repeat names: a name two files share is refused with both
// paths, and the path picks one.
func TestTrackOrderByPath(t *testing.T) {
	t.Parallel()

	it := bookWith(track("a", "CD1/01.mp3", 1, 10), track("b", "CD2/01.mp3", 2, 20))

	_, _, err := trackOrder(it, []string{"01.mp3", "CD2/01.mp3"})
	wantErr(t, "a shared name", err, "2 audio files", `"CD1/01.mp3"`, `"CD2/01.mp3"`)

	order, _, err := trackOrder(it, []string{"CD2/01.mp3", "CD1/01.mp3"})
	if err != nil {
		t.Fatal(err)
	}
	if order[0].Ino != "b" || order[1].Ino != "a" {
		t.Errorf("order = %v", order)
	}
}

func TestChaptersMoved(t *testing.T) {
	t.Parallel()

	a, b, c := track("a", "01.mp3", 1, 100), track("b", "02.mp3", 2, 50), track("c", "03.mp3", 3, 30)
	before, after := []abs.AudioFile{a, b, c}, []abs.AudioFile{c, a, b}

	// one chapter per file, as the scanner cuts them: each goes with its
	// file and keeps its title
	perFile := []abs.Chapter{{Id: 0, Start: 0, End: 100, Title: "One"}, {Id: 1, Start: 100, End: 150, Title: "Two"}, {Id: 2, Start: 150, End: 180.2, Title: "Three"}}
	got, ok := chaptersMoved(perFile, before, after)
	if !ok {
		t.Fatal("chapters one per file were not moved")
	}
	want := []abs.Chapter{{Id: 0, Start: 0, End: 30, Title: "Three"}, {Id: 1, Start: 30, End: 130, Title: "One"}, {Id: 2, Start: 130, End: 180, Title: "Two"}}
	if !slices.Equal(got, want) {
		t.Errorf("moved = %v, want %v", got, want)
	}

	// a file's own chapters inside it keep their place in the file
	inside := []abs.Chapter{{Start: 0, End: 60, Title: "1a"}, {Start: 60, End: 100, Title: "1b"}, {Start: 100, End: 150, Title: "2"}, {Start: 150, End: 180, Title: "3"}}
	got, ok = chaptersMoved(inside, before, after)
	if !ok {
		t.Fatal("chapters inside files were not moved")
	}
	if titles := chapterTitles(got); !slices.Equal(titles, []string{"3", "1a", "1b", "2"}) {
		t.Errorf("order = %v", titles)
	}
	if got[2].Start != 90 || got[2].End != 130 {
		t.Errorf("1b = %v, want 90 to 130: 60s into the file, which now starts at 30", got[2])
	}

	// a chapter across two files cannot follow either
	if got, ok := chaptersMoved([]abs.Chapter{{Start: 0, End: 120, Title: "Long"}, {Start: 120, End: 180, Title: "Rest"}}, before, after); ok {
		t.Errorf("a chapter across two files was moved: %v", got)
	}
	// nor one past the end of the audio
	if got, ok := chaptersMoved([]abs.Chapter{{Start: 0, End: 100, Title: "One"}, {Start: 190, End: 200, Title: "Gone"}}, before, after); ok {
		t.Errorf("a chapter past the end was moved: %v", got)
	}
}

func chapterTitles(chapters []abs.Chapter) []string {
	out := make([]string, 0, len(chapters))
	for _, c := range chapters {
		out = append(out, c.Title)
	}
	return out
}

func TestEbookFlip(t *testing.T) {
	t.Parallel()

	ebook := func(ino, name string) abs.LibraryFile {
		return abs.LibraryFile{Ino: ino, FileType: "ebook", IsSupplementary: new(true), Metadata: &abs.FileMetadata{Filename: name, RelPath: name}}
	}
	book := func(main string) *abs.LibraryItemExpanded {
		it := bookWith()
		it.LibraryFiles = []abs.LibraryFile{ebook("e1", "Dune.epub"), ebook("e2", "Dune.pdf"), {Ino: "a1", FileType: "audio"}}
		for i := range it.LibraryFiles {
			if it.LibraryFiles[i].Ino == main {
				it.LibraryFiles[i].IsSupplementary = new(false)
				it.Media.EbookFile = &abs.EBookFile{Ino: main, Metadata: it.LibraryFiles[i].Metadata}
			}
		}
		return it
	}

	cases := []struct {
		main, name, flip, becomes string
	}{
		{"e1", "Dune.pdf", "e2", "Dune.pdf"}, // a supplementary file flips to main
		{"e1", "Dune.epub", "", "Dune.epub"}, // already the main one: flipping it would leave none
		{"e1", "none", "e1", "none"},         // the main one flips to supplementary
		{"", "none", "", "none"},             // already none
		{"", "Dune.epub", "e1", "Dune.epub"},
	}
	for _, c := range cases {
		flip, main, err := ebookFlip(book(c.main), c.name)
		if err != nil {
			t.Errorf("%s with %q main: %v", c.name, c.main, err)
			continue
		}
		got := ""
		if flip != nil {
			got = flip.Ino
		}
		if got != c.flip || main != c.becomes {
			t.Errorf("%s with %q main: flips %q to leave %q, want %q to leave %q", c.name, c.main, got, main, c.flip, c.becomes)
		}
	}

	_, _, err := ebookFlip(book("e1"), "Dune.mobi")
	wantErr(t, "an unknown ebook", err, `"Dune.mobi"`, `"Dune.epub"`, `"Dune.pdf"`)
	_, _, err = ebookFlip(book("e1"), "01.mp3")
	wantErr(t, "an audio file", err, `"01.mp3"`)

	_, _, err = ebookFlip(bookWith(), "Dune.epub")
	wantErr(t, "a book with no ebooks", err, "no ebook files")

	// a main ebook no longer among the files cannot be flipped back
	stale := book("e1")
	stale.LibraryFiles = stale.LibraryFiles[1:]
	_, _, err = ebookFlip(stale, "Dune.pdf")
	wantErr(t, "a stale main ebook", err, "not among the files", "item_rescan")
}
