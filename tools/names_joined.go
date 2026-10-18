package tools

import (
	"context"
	"slices"
	"strings"

	"github.com/katbyte/abs-mcp/sdk/abs"
)

// The item listing carries a book's authors and narrators as one string each,
// joined with ", " (authorName, narratorName), and a comma is also something
// a name may hold: "Jane Doe, Ph.D." is one narrator, and split on the comma
// it reads as "Jane Doe" and a "Ph.D." fragment that no metadata_rename can
// find. joinedNames reads those strings back against the names the library
// holds, rejoining the parts wherever together they are one of its names. A
// string that reads more than one way (the library holds "Jane Doe, Ph.D."
// and also "Jane Doe" and "Ph.D." on other books) is settled by fetching the
// book, which carries the names as a list.
//
// The names come from the narrator and author routes rather than the filter
// data: the server keeps filter data for half an hour, and a narrator set by
// an edit is not added to it.

// joinedNames holds a sweep's books whose joined names need reading.
type joinedNames struct {
	held []abs.LibraryItemMinified
}

// namedBook is a book from a library's list with its authors and narrators read apart where the list joined them; a field read no way, or
// not joined, is left empty and values splits the joined string at its commas.
type namedBook struct {
	*abs.LibraryItemMinified
	authors, narrators []string
}

// values is a field's values on the book, its authors and narrators as they were read apart.
func (b namedBook) values(field string) []string {
	switch {
	case field == "authors" && len(b.authors) > 0:
		return b.authors
	case field == "narrators" && len(b.narrators) > 0:
		return b.narrators
	}
	return valuesOf(field, b.LibraryItemMinified)
}

// wholeNames is a book fetched whole, which lists its authors and narrators, as namedBook.
func wholeNames(it *abs.LibraryItemExpanded) namedBook {
	short := it.Minified()
	b := namedBook{LibraryItemMinified: &short, narrators: it.Media.Metadata.Narrators}
	for _, a := range it.Media.Metadata.Authors {
		if a.Name != "" {
			b.authors = append(b.authors, a.Name)
		}
	}
	return b
}

// hold keeps an item whose author or narrator string joins several names,
// for resolve; false when there is nothing to read and the item can be used
// as it is.
func (j *joinedNames) hold(it *abs.LibraryItemMinified) bool {
	if it.IsPodcast() || (!joinsNames(it.Media.Metadata.NarratorName) && !joinsNames(it.Media.Metadata.AuthorName)) {
		return false
	}
	j.held = append(j.held, *it)
	return true
}

// joinsNames is whether a joined string needs reading: several names in it.
func joinsNames(joined string) bool {
	return strings.Contains(joined, ", ")
}

// resolve reads the held items' authors and narrators apart and hands each
// to fn: from the library's names where the string reads one way, and from
// the book itself, fetched whole, where it reads more. It reads the
// library's narrators and authors only when a held item needs them.
func (j *joinedNames) resolve(ctx context.Context, gen *abs.Client, libraryID abs.LibraryID, fn func(b namedBook)) error {
	held := j.held
	j.held = nil
	var narrators, authors map[string]bool
	var fetch []abs.LibraryItemID
	for i := range held {
		m := held[i].Media.Metadata
		if joinsNames(m.NarratorName) && narrators == nil {
			got, err := record(gen.LibraryGetNarrators(ctx, libraryID))
			if err != nil {
				return err
			}
			narrators = map[string]bool{}
			for _, n := range got.Narrators {
				narrators[n.Name] = true
			}
		}
		if joinsNames(m.AuthorName) && authors == nil {
			records, err := allAuthors(ctx, gen, libraryID, abs.LibraryGetAuthorsOptions{})
			if err != nil {
				return err
			}
			authors = map[string]bool{}
			for k := range records {
				authors[records[k].Name] = true
			}
		}

		// a string none of whose readings are all names the library holds
		// (renamed since the lists were read) is left to values to split
		b := namedBook{LibraryItemMinified: &held[i]}
		ok := true
		if joinsNames(m.NarratorName) {
			b.narrators, ok = readJoined(m.NarratorName, narrators)
		}
		if ok && joinsNames(m.AuthorName) {
			b.authors, ok = readJoined(m.AuthorName, authors)
		}
		if !ok {
			fetch = append(fetch, held[i].Id)
			continue
		}
		fn(b)
	}

	for chunk := range slices.Chunk(fetch, embedBatchSize) {
		items, err := itemsBatch(ctx, gen, chunk)
		if err != nil {
			return err
		}
		for k := range items {
			fn(wholeNames(&items[k]))
		}
	}
	return nil
}

// readJoined splits a ", "-joined string into the names known holds, keeping
// together the parts that are one name. ok is false when it reads more than
// one way; names is nil when it reads no way at all.
func readJoined(joined string, known map[string]bool) (names []string, ok bool) {
	parts := strings.Split(joined, ", ")
	n := len(parts)
	// ways[i] is how many readings parts[i:] has, stopping at two, and
	// next[i] where the first name of the one reading ends
	ways := make([]int, n+1)
	next := make([]int, n+1)
	ways[n] = 1
	for i := n - 1; i >= 0; i-- {
		for k := i + 1; k <= n; k++ {
			if known[strings.Join(parts[i:k], ", ")] && ways[k] > 0 {
				ways[i] = min(ways[i]+ways[k], 2)
				next[i] = k
			}
		}
	}
	switch ways[0] {
	case 0:
		return nil, true
	case 1:
		for i := 0; i < n; i = next[i] {
			names = append(names, strings.Join(parts[i:next[i]], ", "))
		}
		return names, true
	}
	return nil, false
}
