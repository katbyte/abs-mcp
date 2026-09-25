package tools

import (
	"strings"
	"testing"
)

// folderStyleOf runs the folder check over a shelf and answers each
// finding's path, its suggested path and what differs.
func folderStyleOf(t *testing.T, books [][5]string) map[string][2]string {
	t.Helper()

	c := newNumberingCollector()
	for _, b := range books {
		it := numbered(b[0], b[1], b[3], b[4])
		it.Media.Metadata.Title = b[2]
		c.add(it)
	}
	out := map[string][2]string{}
	for _, f := range c.folderStyleFindings() {
		if f.Problem != "folder_style" {
			t.Errorf("a %s finding came out of the folder check", f.Problem)
		}
		out[f.Path] = [2]string{f.Suggest, f.Detail}
	}
	return out
}

// The shapes the zbooks import left: a book under its bare title beside its
// series' numbered folders, a double space, a number padded unlike the rest,
// and a series named without its article. Each is reported with the folder it
// would have; a series written one way throughout is not, whatever the way.
func TestFolderStyleFindsASeriesWrittenTwoWays(t *testing.T) {
	t.Parallel()

	got := folderStyleOf(t, [][5]string{
		// the style the library's series are written in
		{"h1", "Peter F. Hamilton/Pandora's Star", "Pandora's Star", "Commonwealth Saga", "1"},
		{"h2", "Peter F. Hamilton/Commonwealth Saga - 02 - Judas Unchained", "Judas Unchained", "Commonwealth Saga", "2"},
		{"c1", "Orson Scott Card/The Ender Saga - 01 - Ender's Game", "Ender's Game", "The Ender Saga", "1"},
		{"c2", "Orson Scott Card/The Ender Saga - 02 - Speaker for the Dead", "Speaker for the Dead", "The Ender Saga", "2"},
		{"c3", "Orson Scott Card/Ender Saga - 03 - Xenocide", "Xenocide", "The Ender Saga", "3"},
		{"c4", "Orson Scott Card/Children of the Mind (Unabridged)", "Children of the Mind (Unabridged)", "The Ender Saga", "4"},
		{"m1", "Orson Scott Card/Homecoming - 1 - The Memory of Earth", "The Memory of Earth", "Homecoming", "1"},
		{"m2", "Orson Scott Card/Homecoming  - 2 - The Call of Earth", "The Call of Earth", "Homecoming", "2"},
		{"l1", "Orson Scott Card/Laddertop - 1", "Laddertop", "Laddertop", "1"},
		{"l2", "Orson Scott Card/Laddertop 2", "Laddertop 2", "Laddertop", "2"},
		// padded to the width of the series' highest number: 12 books, two digits
		{"d1", "Terry Pratchett/Discworld - 01 - The Colour of Magic", "The Colour of Magic", "Discworld", "01"},
		{"d2", "Terry Pratchett/Discworld - 2 - The Light Fantastic", "The Light Fantastic", "Discworld", "02"},
		{"d12", "Terry Pratchett/Discworld - 12 - Witches Abroad", "Witches Abroad", "Discworld", "12"},
		// written one way throughout, even if not the library's way: fine
		{"w1", "Ursula K. Le Guin/A Wizard of Earthsea", "A Wizard of Earthsea", "Earthsea", "1"},
		{"w2", "Ursula K. Le Guin/The Tombs of Atuan", "The Tombs of Atuan", "Earthsea", "2"},
		// one book of a series in the folder has nothing to be unlike
		{"s1", "Liu Cixin/The Three-Body Problem", "The Three-Body Problem", "Remembrance of Earth's Past", "1"},
	})

	want := map[string]string{
		"Peter F. Hamilton/Pandora's Star":                     "Peter F. Hamilton/Commonwealth Saga - 01 - Pandora's Star",
		"Orson Scott Card/Ender Saga - 03 - Xenocide":          "Orson Scott Card/The Ender Saga - 03 - Xenocide",
		"Orson Scott Card/Children of the Mind (Unabridged)":   "Orson Scott Card/The Ender Saga - 04 - Children of the Mind",
		"Orson Scott Card/Homecoming  - 2 - The Call of Earth": "Orson Scott Card/Homecoming - 2 - The Call of Earth",
		"Orson Scott Card/Laddertop - 1":                       "Orson Scott Card/Laddertop - 1 - Laddertop",
		"Orson Scott Card/Laddertop 2":                         "Orson Scott Card/Laddertop - 2 - Laddertop 2",
		"Terry Pratchett/Discworld - 2 - The Light Fantastic":  "Terry Pratchett/Discworld - 02 - The Light Fantastic",
	}
	for path, suggest := range want {
		if g, ok := got[path]; !ok || g[0] != suggest {
			t.Errorf("%s: suggested %q, want %q", path, g[0], suggest)
		}
	}
	for path := range got {
		if _, expected := want[path]; !expected {
			t.Errorf("%s was reported and should not be: %v", path, got[path])
		}
	}
	if d := got["Orson Scott Card/Homecoming  - 2 - The Call of Earth"][1]; !strings.Contains(d, "spaced") {
		t.Errorf("the double space is described as %q", d)
	}
	if d := got["Peter F. Hamilton/Pandora's Star"][1]; !strings.Contains(d, "the title alone") {
		t.Errorf("the bare title is described as %q", d)
	}
}

// A single-file book sits as a file, not a folder: "Discworld - 09 -
// Eric.m4b" beside the folders is a file named in the series' style, and a
// bare "Eric.m4b" has no folder to rename.
func TestFolderStyleLeavesFilesAlone(t *testing.T) {
	t.Parallel()

	got := folderStyleOf(t, [][5]string{
		{"d1", "Terry Pratchett/Discworld - 01 - The Colour of Magic", "The Colour of Magic", "Discworld", "1"},
		{"d2", "Terry Pratchett/Eric.m4b", "Eric", "Discworld", "9"},
		{"d3", "Terry Pratchett/Discworld - 03 - Equal Rites", "Equal Rites", "Discworld", "3"},
	})
	if len(got) != 0 {
		t.Errorf("findings = %v, want none for a single-file book", got)
	}
}
