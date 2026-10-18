package tools

import (
	"testing"

	api "github.com/katbyte/abs-mcp/sdk/abs"
)

func TestFieldDiffs(t *testing.T) {
	t.Parallel()

	m := &api.LibraryItemMinifiedMediaMetadata{
		Title: "Mrs. S .mp3", AuthorName: "K. Patrick", NarratorName: "Nicolette Chin",
		SeriesName: "Nemesis #2", Genres: []string{"Fiction", "LGBTQ+"},
		Publisher: "Europa Editions", PublishedYear: "2023", Language: "english",
	}
	hit := &api.SearchFindBooksAnswer{
		Title: "Mrs S", Author: "K. Patrick", Narrator: "Chin, Nicolette",
		Series: []api.SearchFindBooksAnswerSeries{{Series: "Nemesis", Sequence: "2"}}, Genres: []string{"LGBTQ+", "Fiction"},
		Publisher: "Europa Editions", PublishedYear: "2023", Language: "English", Description: "A novel.",
	}
	diffs := fieldDiffs(m, m.SeriesDisplay(), hit)
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
	hit = &api.SearchFindBooksAnswer{Title: "MRS S MP3", Author: "k patrick"}
	if diffs := fieldDiffs(m, m.SeriesDisplay(), hit); len(diffs) != 0 {
		t.Errorf("silent provider reported differences: %+v", diffs)
	}
}
