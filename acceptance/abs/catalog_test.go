//go:build acceptance

package abs_test

import (
	"slices"
	"testing"

	"github.com/katbyte/abs-mcp/sdk/abs"
)

// --- catalogue ----------------------------------------------------------

// A narrator is renamed on every book carrying them, and back. A narrator has no record of its own, so the route names one by its name,
// written in base64.
func TestRenameNarrator(t *testing.T) {
	ctx := skipUnlessLive(t)
	id := library(t)

	narrators := must(client.LibraryGetNarrators(ctx, id)).Narrators
	if len(narrators) == 0 {
		t.Skip("no narrators set on the fixtures yet")
	}
	changed, err := record(client.LibraryUpdateNarrator(ctx, id, narrators[0].Id, abs.LibraryUpdateNarratorRequest{Name: "SDK Narrator"}))
	if err != nil {
		t.Fatal(err)
	}
	renamed, back := abs.PathName("SDK Narrator"), abs.LibraryUpdateNarratorRequest{Name: narrators[0].Name}
	t.Cleanup(func() { _ = errorOf(client.LibraryUpdateNarrator(afterwards(t), id, renamed, back)) })
	if changed.Updated == 0 {
		t.Error("the rename reported no items changed")
	}

	named := func(n abs.LibraryGetNarratorsAnswerNarrators) bool { return n.Name == "SDK Narrator" }
	if !slices.ContainsFunc(must(client.LibraryGetNarrators(ctx, id)).Narrators, named) {
		t.Error("the narrators read back do not name the new one")
	}
}

// The series listing carries no sequence numbers: only an item list filtered by the series does, which is what audit_series_gaps reads. It
// was silently broken until this was pinned down, so the shape is asserted outright.
func TestSeriesSequences(t *testing.T) {
	ctx := skipUnlessLive(t)
	id := library(t)

	series := must(client.LibraryGetAllSeriesForLibrary(ctx, id, abs.LibraryGetAllSeriesForLibraryOptions{Limit: new(100)})).Results
	if len(series) == 0 {
		t.Skip("no series on the fixtures yet")
	}

	s := series[0]
	for _, b := range s.Books {
		if b.Media != nil && b.Media.Metadata != nil && b.Media.Metadata.Series != nil && b.Media.Metadata.Series.Sequence != "" {
			t.Errorf("the series listing unexpectedly carries a sequence (%q) - the workaround in audit_series_gaps may no longer be needed", b.Media.Metadata.Series.Sequence)
		}
	}

	res := must(client.LibraryGetLibraryItems(ctx, id, abs.LibraryGetLibraryItemsOptions{Limit: new(100), Filter: abs.ListFilter("series", string(s.Id))}))
	if len(res.Results) == 0 {
		t.Fatalf("no items for series %s", s.Name)
	}
	var withSequence int
	for _, it := range res.Results {
		if it.Media == nil || it.Media.Metadata == nil {
			continue
		}
		if ref := it.Media.Metadata.Series; ref != nil && ref.Id == s.Id && ref.Sequence != "" {
			withSequence++
		}
	}
	if withSequence == 0 {
		t.Error("a series-filtered list returned no sequence numbers either")
	}
}

// --- vocabulary ---------------------------------------------------------

func TestRenameTag(t *testing.T) {
	ctx := skipUnlessLive(t)
	library(t)

	tags := must(client.MiscGetAllTags(ctx)).Tags
	if len(tags) == 0 {
		t.Skip("nothing tagged yet")
	}

	renamed, err := record(client.MiscRenameTag(ctx, abs.MiscRenameTagRequest{Tag: tags[0], NewTag: "sdk-renamed"}))
	if err != nil {
		t.Fatal(err)
	}
	if renamed.NumItemsUpdated == 0 {
		t.Error("the rename reported no items updated")
	}
	if err := errorOf(client.MiscRenameTag(ctx, abs.MiscRenameTagRequest{Tag: "sdk-renamed", NewTag: tags[0]})); err != nil {
		t.Error(err)
	}
}

func TestUpdateAuthorAndSeries(t *testing.T) {
	ctx := skipUnlessLive(t)
	id := library(t)

	authors := firstAuthors(ctx, id, 10)
	if len(authors) == 0 {
		t.Skip("no authors")
	}

	desc := "Written by the SDK suite."
	updated := must(client.AuthorUpdate(ctx, authors[0].Id, abs.AuthorUpdateRequest{Description: &desc}))
	if updated.Merged {
		t.Error("a description change reported a merge")
	}
	if updated.Author == nil || updated.Author.Description != desc {
		t.Errorf("the author answered = %+v, want the new description", updated.Author)
	}

	series := must(client.LibraryGetAllSeriesForLibrary(ctx, id, abs.LibraryGetAllSeriesForLibraryOptions{Limit: new(10)})).Results
	if len(series) == 0 {
		t.Skip("no series")
	}
	sdesc := "A series, per the SDK suite."
	if got := must(client.SeriesUpdate(ctx, series[0].Id, abs.SeriesUpdateRequest{Description: sdesc})); got.Description != sdesc {
		t.Errorf("series description = %q", got.Description)
	}
	if got := must(client.SeriesFindOne(ctx, series[0].Id)); got.Description != sdesc {
		t.Errorf("the series read back has description %q", got.Description)
	}
}
