package tools

import (
	"fmt"
	"slices"
	"strings"

	api "github.com/katbyte/abs-mcp/sdk/abs"
)

// A match applied the default way fills only the empty fields, so what the
// book carried before survives: a title from a file tag, a narrator that is
// really a publisher, a series spelled the collector's way. Nothing later
// says which of those the provider would have written differently. This is
// that comparison, field by field, so the collector can choose what to
// override rather than override everything or nothing.

// descriptionStub is the length under which a description is a note rather
// than a description.
const descriptionStub = 120

// fieldDiff is one field the library and the provider disagree on.
type fieldDiff struct {
	Field    string `json:"field"`
	Local    string `json:"local"    jsonschema:"what the library has; empty when the field is blank"`
	Provider string `json:"provider" jsonschema:"what the provider's record has"`
}

// fieldDiffs compares the fields a match would write: title, subtitle,
// authors, narrators, series, genres, publisher, year, language, and whether
// there is a description at all. Names are compared spelling and order
// aside; a field the provider leaves blank is not a difference, since a
// match would not blank it either. series is the book's series as lines,
// "Discworld #3", from its whole record when the caller has one.
func fieldDiffs(m *api.LibraryItemMinifiedMediaMetadata, series []string, hit *api.SearchFindBooksAnswer) []fieldDiff {
	var out []fieldDiff
	text := func(field, local, provider string) {
		if strings.TrimSpace(provider) == "" || norm(local) == norm(provider) {
			return
		}
		out = append(out, fieldDiff{field, strings.TrimSpace(local), strings.TrimSpace(provider)})
	}
	names := func(field, local, provider string) {
		if strings.TrimSpace(provider) == "" || sameNames(splitNames(local), splitNames(provider)) {
			return
		}
		out = append(out, fieldDiff{field, strings.TrimSpace(local), strings.TrimSpace(provider)})
	}
	list := func(field string, local, provider []string) {
		if len(provider) == 0 || sameNames(local, provider) {
			return
		}
		out = append(out, fieldDiff{field, strings.Join(local, ", "), strings.Join(provider, ", ")})
	}

	text("title", m.Title, hit.Title)
	text("subtitle", m.Subtitle, hit.Subtitle)
	names("authors", m.AuthorDisplay(), hit.Author)
	names("narrators", m.NarratorDisplay(), hit.Narrator)

	var theirs []string
	for _, s := range hit.Series {
		if s.Sequence != "" {
			theirs = append(theirs, s.Title()+" #"+s.Sequence)
		} else {
			theirs = append(theirs, s.Title())
		}
	}
	list("series", series, theirs)
	list("genres", m.Genres, hit.Genres)
	text("publisher", m.Publisher, hit.Publisher)
	text("year", m.PublishedYear, string(hit.PublishedYear))
	text("language", m.Language, hit.Language)
	// a description is compared by presence, not text: providers differ on
	// markup. A stub under 120 characters ("Read by Lee Ewing") counts as
	// missing when the provider has a real one
	if local, provider := strings.TrimSpace(m.Description), strings.TrimSpace(hit.Description); len(local) < descriptionStub && len(provider) > len(local) {
		out = append(out, fieldDiff{"description", local, fmt.Sprintf("%d characters", len(hit.Description))})
	}
	return out
}

// sameNames reports whether two lists hold the same names, spelling, case
// and order aside.
func sameNames(as, bs []string) bool {
	if len(as) != len(bs) {
		return false
	}
	na := make([]string, len(as))
	for i, a := range as {
		na[i] = norm(a)
	}
	nb := make([]string, len(bs))
	for i, b := range bs {
		nb[i] = norm(b)
	}
	slices.Sort(na)
	slices.Sort(nb)
	return slices.Equal(na, nb)
}
