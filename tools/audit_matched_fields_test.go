package tools

import (
	"testing"

	"github.com/katbyte/abs-mcp/lib/abs"
)

func TestFieldDiffs(t *testing.T) {
	it := &abs.Item{MediaType: "book", RelPath: "K. Patrick/Mrs S", Media: abs.Media{Metadata: abs.Metadata{
		Title: "Mrs. S .mp3", AuthorName: "K. Patrick", NarratorName: "Nicolette Chin",
		Series: abs.SeriesRefs{{Name: "Nemesis", Sequence: "2"}}, Genres: []string{"Fiction", "LGBTQ+"},
		Publisher: "Europa Editions", PublishedYear: "2023", Language: "english",
	}}}
	hit := &abs.BookSearchResult{
		Title: "Mrs S", Author: "K. Patrick", Narrator: "Chin, Nicolette",
		Series: []abs.SearchSeries{{Series: "Nemesis", Sequence: "2"}}, Genres: []string{"LGBTQ+", "Fiction"},
		Publisher: "Europa Editions", PublishedYear: "2023", Language: "English", Description: "A novel.",
	}
	diffs := fieldDiffs(it, hit)
	want := map[string]bool{"title": true, "narrators": true, "description": true}
	for _, d := range diffs {
		if !want[d.Field] {
			t.Errorf("unexpected difference on %s: %q vs %q", d.Field, d.Local, d.Provider)
		}
		delete(want, d.Field)
	}
	for f := range want {
		t.Errorf("difference on %s not reported", f)
	}

	// a provider that is silent on a field is not a difference, and neither
	// is punctuation or case
	hit = &abs.BookSearchResult{Title: "MRS S MP3", Author: "k patrick"}
	if diffs := fieldDiffs(it, hit); len(diffs) != 0 {
		t.Errorf("silent provider reported differences: %+v", diffs)
	}
}
