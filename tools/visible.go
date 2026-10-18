package tools

import (
	"cmp"
	"context"
	"slices"
	"strings"

	"github.com/katbyte/abs-mcp/sdk/abs"
	"github.com/katbyte/go-kt/pointer"
)

// An account can be kept from some of the books in a library it can open:
// by tag (only books carrying, or not carrying, the tags an admin picked) or
// by the explicit flag, which plain users cannot see by default. The server
// hides those books from its item, author and series routes, but its filter
// data, library stats, narrator list and the name groups of its search are
// built over the whole library, so they name the authors, narrators, series
// and titles of books the account is not allowed to see. For such a key the
// tools that report those build the answer from the books the key can see.

// restrictedKey reports whether the API key's user is kept from some books by
// tag or by the explicit flag.
func restrictedKey(ctx context.Context, gen *abs.Client) (bool, error) {
	me, err := record(gen.MeGetCurrentUser(ctx))
	if err != nil {
		return false, err
	}
	perms := pointer.From(me.Permissions)

	return !pointer.From(perms.AccessAllTags) || !pointer.From(perms.AccessExplicitContent), nil
}

// visibleLibrary is a library as one account sees it: counts and vocabulary
// taken from the books the item listing gives that account.
type visibleLibrary struct {
	Items, AudioFiles, Issues int
	Duration                  float64
	Size                      int64

	// value -> how many visible books carry it
	Genres, Tags, Narrators, Languages, Publishers, Decades, AuthorBooks map[string]int

	// from the author and series routes, which the server restricts itself
	Authors          []abs.AuthorMinimal
	Series           []abs.SeriesMinimal
	Longest, Largest []abs.LibraryItemMinified
}

const visibleTop = 10

// sweepVisible reads a library through the item listing, as the API key's
// user is allowed to see it.
func sweepVisible(ctx context.Context, gen *abs.Client, lib *abs.Library) (*visibleLibrary, error) {
	v := &visibleLibrary{
		Genres: map[string]int{}, Tags: map[string]int{}, Narrators: map[string]int{},
		Languages: map[string]int{}, Publishers: map[string]int{}, Decades: map[string]int{}, AuthorBooks: map[string]int{},
	}
	count := func(into map[string]int, values []string) {
		seen := map[string]bool{}
		for _, val := range values {
			if val = strings.TrimSpace(val); val != "" && !seen[val] {
				seen[val] = true
				into[val]++
			}
		}
	}
	var all []abs.LibraryItemMinified
	err := eachItem(ctx, gen, lib.Id, abs.LibraryGetLibraryItemsOptions{}, func(it *abs.LibraryItemMinified) bool {
		v.Items++
		v.AudioFiles += it.Media.NumAudioFiles
		v.Duration += it.Media.Duration
		v.Size += int64(it.Size)
		if it.IsMissing || it.IsInvalid {
			v.Issues++
		}
		count(v.Genres, valuesOf("genres", it))
		count(v.Tags, valuesOf("tags", it))
		count(v.Narrators, valuesOf("narrators", it))
		count(v.Languages, valuesOf("languages", it))
		count(v.Publishers, valuesOf("publishers", it))
		count(v.AuthorBooks, valuesOf("authors", it))
		if y := strings.TrimSpace(it.Media.Metadata.PublishedYear); len(y) == 4 {
			count(v.Decades, []string{y[:3] + "0"})
		}
		all = append(all, *it)
		return true
	})
	if err != nil {
		return nil, err
	}

	v.Longest = topItems(all, func(a, b abs.LibraryItemMinified) int { return cmp.Compare(b.Media.Duration, a.Media.Duration) })
	v.Largest = topItems(all, func(a, b abs.LibraryItemMinified) int { return cmp.Compare(b.Size, a.Size) })

	if lib.IsPodcast() {
		return v, nil
	}
	authors, err := allAuthors(ctx, gen, lib.Id, abs.LibraryGetAuthorsOptions{Sort: "name"})
	if err != nil {
		return nil, err
	}
	for i := range authors {
		v.Authors = append(v.Authors, abs.AuthorMinimal{Id: authors[i].Id, Name: authors[i].Name})
	}
	series, err := allSeries(ctx, gen, lib.Id)
	if err != nil {
		return nil, err
	}
	for i := range series {
		v.Series = append(v.Series, abs.SeriesMinimal{Id: series[i].Id, Name: series[i].Name})
	}

	return v, nil
}

// topItems is the first visibleTop items by an order.
func topItems[T any](items []T, order func(a, b T) int) []T {
	sorted := slices.Clone(items)
	slices.SortStableFunc(sorted, order)
	return sorted[:min(len(sorted), visibleTop)]
}

// topCounts is the visibleTop values with the most books, most first.
func topCounts(m map[string]int) []string {
	keys := keysOf(m)
	slices.SortStableFunc(keys, func(a, b string) int { return cmp.Compare(m[b], m[a]) })
	return keys[:min(len(keys), visibleTop)]
}

// keysOf lists a count map's values, sorted.
func keysOf(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
