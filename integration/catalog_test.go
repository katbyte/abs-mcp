//go:build integration

package integration

import (
	"github.com/katbyte/abs-mcp/lib/abs"
	"testing"
)

// --- catalogue ----------------------------------------------------------

func TestAuthorsAndNarrators(t *testing.T) {
	ctx := skipUnlessLive(t)
	id := library(t)

	authors, total, err := client.Authors(ctx, id, abs.ListOptions{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if total == 0 || len(authors) == 0 {
		t.Fatalf("authors = %d (total %d), want the scanned authors", len(authors), total)
	}
	for i := range authors {
		if authors[i].ID == "" || authors[i].Name == "" {
			t.Errorf("author %d did not decode: %+v", i, authors[i])
		}
	}

	full := must(client.Author(ctx, authors[0].ID, true))
	if full.Name != authors[0].Name {
		t.Errorf("Author(%s) = %q", authors[0].ID, full.Name)
	}

	// narrators come from a separate endpoint with its own id encoding
	// (base64 of the name), and nothing called it before this suite
	narrators := must(client.Narrators(ctx, id))
	if len(narrators) == 0 {
		t.Skip("no narrators set on the fixtures yet")
	}
	from := narrators[0].Name
	changed, err := client.RenameNarrator(ctx, id, from, "SDK Narrator")
	if err != nil {
		t.Fatal(err)
	}
	if changed == 0 {
		t.Error("RenameNarrator reported no items changed")
	}
	t.Cleanup(func() { _, _ = client.RenameNarrator(t.Context(), id, "SDK Narrator", from) })
}

func TestSeries(t *testing.T) {
	ctx := skipUnlessLive(t)
	id := library(t)

	series, _, err := client.SeriesList(ctx, id, abs.ListOptions{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(series) == 0 {
		t.Skip("no series on the fixtures yet")
	}

	// the series listing carries no sequence numbers: only an item query
	// filtered by the series does. audit_series_gaps was silently broken until
	// this was pinned down, so assert the shape explicitly.
	s := series[0]
	for _, b := range s.Books {
		for _, ref := range b.Media.Metadata.Series {
			if ref.Sequence != "" {
				t.Errorf("the series listing unexpectedly carries a sequence (%q) - the workaround in audit_series_gaps may no longer be needed", ref.Sequence)
			}
		}
	}

	res := must(client.Items(ctx, id, abs.ItemsOptions{
		Limit: 100, Filter: abs.EncodeFilter("series", s.ID),
	}))
	if len(res.Results) == 0 {
		t.Fatalf("no items for series %s", s.Name)
	}
	var withSequence int
	for i := range res.Results {
		for _, ref := range res.Results[i].Media.Metadata.Series {
			if ref.ID == s.ID && ref.Sequence != "" {
				withSequence++
			}
		}
	}
	if withSequence == 0 {
		t.Error("a series-filtered query returned no sequence numbers either")
	}
}

// --- vocabulary ---------------------------------------------------------

func TestTagsAndGenres(t *testing.T) {
	ctx := skipUnlessLive(t)
	library(t)

	tags := must(client.Tags(ctx))
	genres := must(client.Genres(ctx))
	if len(tags) == 0 && len(genres) == 0 {
		t.Skip("nothing tagged yet")
	}

	if len(tags) > 0 {
		n, err := client.RenameTag(ctx, tags[0], "sdk-renamed")
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			t.Error("RenameTag reported no items updated")
		}
		if _, err := client.RenameTag(ctx, "sdk-renamed", tags[0]); err != nil {
			t.Error(err)
		}
	}
}

// FilterData is cached by the server and is not invalidated by an edit or a
// rescan, so this asserts only the parts that come from relational tables.
func TestFilterData(t *testing.T) {
	ctx := skipUnlessLive(t)
	id := library(t)

	fd := must(client.FilterData(ctx, id))
	if len(fd.Authors) == 0 {
		t.Error("filter data carried no authors")
	}
}

func TestCatalogueMethods(t *testing.T) {
	ctx := skipUnlessLive(t)
	id := library(t)

	authors, _, err := client.Authors(ctx, id, abs.ListOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(authors) == 0 {
		t.Skip("no authors")
	}

	desc := "Written by the SDK suite."
	updated, merged, err := client.UpdateAuthor(ctx, authors[0].ID, abs.AuthorUpdate{Description: &desc})
	if err != nil {
		t.Fatal(err)
	}
	if merged {
		t.Error("a description change reported a merge")
	}
	if updated.Description != desc {
		t.Errorf("description = %q", updated.Description)
	}

	series, _, err := client.SeriesList(ctx, id, abs.ListOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(series) == 0 {
		t.Skip("no series")
	}
	one := must(client.Series(ctx, series[0].ID))
	if one.Name != series[0].Name {
		t.Errorf("Series(%s) = %q", series[0].ID, one.Name)
	}
	sdesc := "A series, per the SDK suite."
	if got := must(client.UpdateSeries(ctx, series[0].ID, abs.SeriesUpdate{Description: &sdesc})); got.Description != sdesc {
		t.Errorf("series description = %q", got.Description)
	}
}

// SearchAuthors looks a name up on the provider without writing anything,
// where MatchAuthor applies the result. It goes through the proxy.
func TestSearchAuthors(t *testing.T) {
	ctx := skipUnlessLive(t)
	library(t)

	if _, err := client.SearchAuthors(ctx, "Isaac Asimov"); err != nil {
		t.Errorf("SearchAuthors: %v", err)
	}
}
