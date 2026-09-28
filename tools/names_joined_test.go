package tools

import (
	"slices"
	"testing"
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
