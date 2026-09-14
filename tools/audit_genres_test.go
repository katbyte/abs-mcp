package tools

import (
	"testing"

	"github.com/katbyte/abs-mcp/lib/abs"
)

func TestGenresCollector(t *testing.T) {
	book := func(id string, genres, tags []string) *abs.Item {
		return &abs.Item{ID: id, MediaType: "book", Media: abs.Media{Tags: tags, Metadata: abs.Metadata{Title: id, Genres: genres}}}
	}
	c := newGenresCollector()
	c.add(book("a", []string{"Audiobook"}, []string{"Fantasy", "zz-provider:audible"}))
	c.add(book("b", []string{"Science Fiction & Fantasy, Fantasy"}, []string{"Fantasy"}))
	c.add(book("c", []string{"Fantasy", "Hamsters"}, []string{"Fantasy", "Mice"}))
	c.add(book("d", nil, nil))
	c.add(book("e", []string{"Audiobook - Fantasy"}, nil))
	c.add(book("f", []string{"Fantasy"}, nil))
	c.add(&abs.Item{ID: "p", MediaType: "podcast", Media: abs.Media{Metadata: abs.Metadata{Genres: []string{"Podcasts"}}}})

	out := c.findings(2, 10)
	if out.Counts.Books != 6 {
		t.Errorf("books = %d", out.Counts.Books)
	}
	if out.Counts.NoGenres != 1 || out.Counts.PlaceholderOnly != 1 { // "Audiobook - Fantasy" still names a genre
		t.Errorf("no_genres = %d placeholder_only = %d", out.Counts.NoGenres, out.Counts.PlaceholderOnly)
	}
	if len(out.Placeholders) != 2 || out.Placeholders[0].Value != "Audiobook" || out.Placeholders[1].Suggest != `metadata_rename field=genres from="Audiobook - Fantasy" to=Fantasy` {
		t.Errorf("placeholders = %+v", out.Placeholders)
	}
	if len(out.Compound) != 1 || out.Compound[0].Parts[1].Value != "Fantasy" || !out.Compound[0].Parts[1].Known ||
		out.Compound[0].Suggest.Genres[0] != "Science Fiction & Fantasy" || out.Compound[0].Suggest.Tags[0] != "Fantasy" {
		t.Errorf("compound = %+v", out.Compound)
	}
	// "Science Fiction & Fantasy" exists only inside the compound value, so it is not known on its own
	if out.Compound[0].Parts[0].Known {
		t.Error("a part that exists only inside the compound was reported as known")
	}
	if len(out.Narrow) != 1 || out.Narrow[0].Value != "Hamsters" {
		t.Errorf("narrow = %+v", out.Narrow)
	}
	if len(out.Redundant) != 1 || out.Redundant[0].Value != "Fantasy" || out.Redundant[0].Items != 1 {
		t.Errorf("redundant = %+v", out.Redundant)
	}
	if len(out.Markers) != 1 || out.Markers[0] != "zz-provider:audible" {
		t.Errorf("markers = %v", out.Markers)
	}
	if out.Found != 2+1+1+1+1 {
		t.Errorf("total_findings = %d", out.Found)
	}
}
