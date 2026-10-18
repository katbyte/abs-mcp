package tools

import (
	"slices"
	"strings"
	"testing"

	api "github.com/katbyte/abs-mcp/sdk/abs"
)

func TestReadJoined(t *testing.T) {
	t.Parallel()

	known := map[string]bool{"Jane Doe, Ph.D.": true, "Jim Dale": true, "A, B, C": true, "A": true, "B": true}
	for _, tc := range []struct {
		in   string
		want []string
		ok   bool
	}{
		{"Jane Doe, Ph.D., Jim Dale", []string{"Jane Doe, Ph.D.", "Jim Dale"}, true},
		{"Jim Dale, Jane Doe, Ph.D.", []string{"Jim Dale", "Jane Doe, Ph.D."}, true},
		{"A, B, C", []string{"A, B, C"}, true}, // A and B are names, C is not
		{"A, B", []string{"A", "B"}, true},
		{"Jim Dale, Someone New", nil, true}, // a name the lists do not hold yet: left to valuesOf
	} {
		if got, ok := readJoined(tc.in, known); ok != tc.ok || !slices.Equal(got, tc.want) {
			t.Errorf("readJoined(%q) = %q, %v; want %q, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
	known["C"] = true
	if got, ok := readJoined("A, B, C", known); ok {
		t.Errorf("readJoined of a string that reads two ways = %q, want it fetched", got)
	}
}

// A book's values read the same whether its names came from a whole record's lists or were split from the list's joined strings.
func TestNamedBookValues(t *testing.T) {
	t.Parallel()

	whole := wholeNames(&api.LibraryItemExpanded{Media: &api.LibraryItemExpandedMedia{Tags: []string{"x"}, Metadata: &api.LibraryItemExpandedMediaMetadata{
		Narrators: []string{"Jim Dale", "Kate Reading"}, Authors: []api.AuthorMinimal{{Name: "A"}, {Name: "B"}},
		Language: "English", Publisher: "Bantam", Genres: []string{"SF"},
	}}})
	listed := namedBook{LibraryItemMinified: &api.LibraryItemMinified{Media: &api.LibraryItemMinifiedMedia{Tags: []string{"x"}, Metadata: &api.LibraryItemMinifiedMediaMetadata{
		NarratorName: "Jim Dale, Kate Reading", AuthorName: "A, B",
		Language: "English", Publisher: "Bantam", Genres: []string{"SF"},
	}}}}

	for _, field := range vocabFields {
		a, b := whole.values(field), listed.values(field)
		for i := range a {
			a[i] = strings.TrimSpace(a[i])
		}
		for i := range b {
			b[i] = strings.TrimSpace(b[i])
		}
		if !slices.Equal(a, b) {
			t.Errorf("%s: whole %v, listed %v", field, a, b)
		}
		if len(a) == 0 {
			t.Errorf("%s: nothing read", field)
		}
	}
	if got := whole.values("nope"); got != nil {
		t.Errorf("unknown field gave %v", got)
	}
}
