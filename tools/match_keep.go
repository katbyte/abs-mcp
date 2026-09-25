package tools

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/katbyte/abs-mcp/lib/abs"
)

// A match with override_details is all or nothing: the server replaces every
// field the provider has a value for, and a collector's series numbering goes
// with the file-tag title it was meant to fix. keep names the fields to put
// back afterwards, from the item as it was before the match, so an override
// can be everything except what the collector has already curated.

var keepFields = []string{"title", "subtitle", "authors", "narrators", "series", "genres", "tags", "publisher", "year", "language", "description"}

// parseKeep checks the names and normalises them.
func parseKeep(keep []string) ([]string, error) {
	var out []string
	for _, k := range keep {
		k = strings.ToLower(strings.TrimSpace(k))
		switch k {
		case "narrator":
			k = "narrators"
		case "author":
			k = "authors"
		case "genre":
			k = "genres"
		case "tag":
			k = "tags"
		}
		if !slices.Contains(keepFields, k) {
			return nil, fmt.Errorf("keep: unknown field %q; choose from %s", k, strings.Join(keepFields, ", "))
		}
		if !slices.Contains(out, k) {
			out = append(out, k)
		}
	}
	return out, nil
}

// keptUpdate is the update that restores the named fields to what the item
// held before the match. An empty field is restored as empty: keeping the
// series means the provider's does not arrive either.
func keptUpdate(before *abs.Item, keep []string) abs.MediaUpdate {
	m := &before.Media.Metadata
	md := abs.MetadataUpdate{}
	upd := abs.MediaUpdate{}
	str := func(v string) *string { return &v }
	for _, k := range keep {
		switch k {
		case "title":
			md.Title = str(m.Title)
		case "subtitle":
			md.Subtitle = str(m.Subtitle)
		case "authors":
			md.Authors = []abs.NameRef{}
			for _, a := range m.Authors {
				md.Authors = append(md.Authors, abs.NameRef{ID: a.ID, Name: a.Name})
			}
			if len(md.Authors) == 0 {
				for _, a := range splitNames(m.AuthorDisplay()) {
					md.Authors = append(md.Authors, abs.NameRef{Name: a})
				}
			}
		case "narrators":
			md.Narrators = []string{}
			if len(m.Narrators) > 0 {
				md.Narrators = slices.Clone(m.Narrators)
			} else {
				md.Narrators = append(md.Narrators, splitNames(m.NarratorDisplay())...)
			}
		case "series":
			md.Series = []abs.SeriesRef{}
			for _, s := range m.Series {
				md.Series = append(md.Series, abs.SeriesRef{ID: s.ID, Name: s.Name, Sequence: s.Sequence})
			}
		case "genres":
			md.Genres = slices.Clone(m.Genres)
			if md.Genres == nil {
				md.Genres = []string{}
			}
		case "tags":
			upd.Tags = slices.Clone(before.Media.Tags)
			if upd.Tags == nil {
				upd.Tags = []string{}
			}
		case "publisher":
			md.Publisher = str(m.Publisher)
		case "year":
			md.PublishedYear = str(m.PublishedYear.String())
		case "language":
			md.Language = str(m.Language)
		case "description":
			md.Description = str(m.Description)
		}
	}
	upd.Metadata = &md
	return upd
}

// tagsAfter is a book's tag list once a match, and the fields kept from it,
// are written: the match's own, since it fills an empty list, unless the tags
// were kept. The provider tag is added to this list, and one read from before
// the match would wipe the tags the match had just filled.
func tagsAfter(before *abs.Item, res *abs.MatchResult, keep []string) []string {
	if slices.Contains(keep, "tags") {
		return before.Media.Tags
	}
	return res.LibraryItem.Media.Tags
}

// restoreKept puts the kept fields back after a match.
func restoreKept(ctx context.Context, client *abs.Client, before *abs.Item, keep []string) error {
	if len(keep) == 0 {
		return nil
	}
	_, err := client.UpdateMedia(ctx, before.ID, keptUpdate(before, keep))
	return err
}
