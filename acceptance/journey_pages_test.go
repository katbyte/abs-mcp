//go:build integration

// Journeys through the catalogue: every listing read a page at a time the way
// a session walks one, a matched shelf audited while it is being fixed, a
// missing book imported into its series and taken out again, and the reads a
// caller takes the first row of. Each checks what the rows are, in what order
// and how many, against another way of asking the server the same thing.
package acceptance

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// pageWalk reads a listing that pages by offset from one offset to its end,
// following next_offset, and checks each page against what a caller relies
// on: it starts where it was asked to, is full unless it is the last, says
// where the next begins, and the total never moves. It returns the ids in
// the order read.
func pageWalk(t *testing.T, tool, key string, args map[string]any, from, limit int) []string {
	t.Helper()

	var ids []string
	total := -1
	for offset := from; ; {
		out := call(t, tool, withArgs(args, map[string]any{"limit": limit, "offset": offset}))
		if got := num(t, out["offset"], "offset"); got != offset {
			t.Fatalf("%s %v from %d answers offset %d", tool, args, offset, got)
		}
		n := num(t, out["total"], "total")
		if total >= 0 && n != total {
			t.Errorf("%s %v: the total moved from %d to %d at offset %d", tool, args, total, n, offset)
		}
		total = n
		page := rows(t, out[key], key)
		ids = append(ids, valuesIn(t, out[key], key, "id")...)
		next, more := out["next_offset"]
		if !more {
			if offset+len(page) < total {
				t.Errorf("%s %v stopped at %d of %d with no next_offset", tool, args, offset+len(page), total)
			}
			return ids
		}
		if len(page) != limit {
			t.Errorf("%s %v: a page of %d at offset %d is short of the limit %d and not the last", tool, args, len(page), offset, limit)
		}
		if got := num(t, next, "next_offset"); got != offset+len(page) {
			t.Fatalf("%s %v: next_offset %d after %d rows from %d", tool, args, got, len(page), offset)
		}
		offset += len(page)
		if len(ids) > 5000 {
			t.Fatalf("%s %v never reached its last page", tool, args)
		}
	}
}

// everyPageOnce reads a listing whole, then a page at a time from the start
// and from two offsets that fall inside a page, and each walk must be the
// whole listing from where it began: the same rows in the same order, none
// twice. Past the end there is nothing and nowhere to go next. It returns
// the rows of the whole listing.
func everyPageOnce(t *testing.T, tool, key string, args map[string]any, limit int) []map[string]any {
	t.Helper()

	whole := call(t, tool, withArgs(args, map[string]any{"limit": 1000}))
	all := rows(t, whole[key], key)
	ids := valuesIn(t, whole[key], key, "id")
	if total := num(t, whole["total"], "total"); total != len(all) || whole["next_offset"] != nil {
		t.Fatalf("%s %v unpaged: %d rows of %d, next_offset %v", tool, args, len(all), total, whole["next_offset"])
	}
	if len(all) <= 2*limit {
		t.Fatalf("%s %v: %d rows is too few to page by %d", tool, args, len(all), limit)
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			t.Errorf("%s %v lists %s twice", tool, args, id)
		}
		seen[id] = true
	}
	for _, from := range []int{0, 3, limit + 3} {
		if got := pageWalk(t, tool, key, args, from, limit); !slices.Equal(got, ids[from:]) {
			t.Errorf("%s %v by %d from %d read\n  %v\nwant\n  %v", tool, args, limit, from, got, ids[from:])
		}
	}
	past := call(t, tool, withArgs(args, map[string]any{"limit": limit, "offset": len(all) + 2}))
	if got := rows(t, past[key], key); len(got) != 0 || past["next_offset"] != nil {
		t.Errorf("%s %v past the end: %d rows, next_offset %v", tool, args, len(got), past["next_offset"])
	}
	return all
}

// ordered checks a column runs one way down a listing, text compared the way
// the server's NOCASE collation does.
func ordered(t *testing.T, what string, all []map[string]any, key string, desc bool) {
	t.Helper()

	for i := 1; i < len(all); i++ {
		a, b := all[i-1][key], all[i][key]
		var cmp int
		switch av := a.(type) {
		case float64:
			bv, _ := b.(float64)
			cmp = int(av - bv)
		default:
			cmp = strings.Compare(strings.ToLower(fmt.Sprint(a)), strings.ToLower(fmt.Sprint(b)))
		}
		if (!desc && cmp > 0) || (desc && cmp < 0) {
			t.Errorf("%s: %v comes before %v", what, a, b)
		}
	}
}

// Every listing that pages, walked a page at a time the way a session walks
// one, in each order it can be sorted: the pages together are the listing
// asked for whole, no row twice and none missed, whether the walk starts at
// zero or part way into a page, and the rows run in the order asked for. Two
// Messy books are titled Mort, so a title sort has a tie across which a page
// break must not lose or repeat a book. It also refuses the sorts and filter
// groups the server would silently ignore - and answer with every book in no
// order - by name, and takes the ones it knows in any case.
func TestJourneyEveryPageReadOnce(t *testing.T) {
	if !ready {
		t.Skip("ABS_SERVER and ABS_TOKEN are not set")
	}

	t.Run("library_items", func(t *testing.T) {
		asc := everyPageOnce(t, "library_items", "items", withMessy(map[string]any{"sort": "title"}), 5)
		ordered(t, "title", asc, "title", false)
		if n := countOf(valuesIn(t, toRows(asc), "items", "title"), "Mort"); n != 2 {
			t.Errorf("Mort is listed %d times, want both books", n)
		}
		desc := everyPageOnce(t, "library_items", "items", withMessy(map[string]any{"sort": "title", "desc": true}), 5)
		ordered(t, "title descending", desc, "title", true)
		up, down := valuesIn(t, toRows(asc), "items", "title"), valuesIn(t, toRows(desc), "items", "title")
		slices.Reverse(down)
		if !slices.Equal(up, down) {
			t.Errorf("descending is not ascending reversed:\n  %v\n  %v", up, down)
		}
		everyPageOnce(t, "library_items", "items", withMessy(map[string]any{"sort": "added", "desc": true}), 5)
		byAuthor := everyPageOnce(t, "library_items", "items", withMessy(map[string]any{"sort": "author"}), 5)
		ordered(t, "author", byAuthor, "author", false)

		// the sort and the filter group are read in any case
		title := valuesIn(t, call(t, "library_items", withMessy(map[string]any{"sort": "title", "limit": 100}))["items"], "items", "id")
		if got := valuesIn(t, call(t, "library_items", withMessy(map[string]any{"sort": "Title", "limit": 100}))["items"], "items", "id"); !slices.Equal(got, title) {
			t.Errorf("sort Title = %v, want sort title's %v", got, title)
		}
		for _, pair := range [][2]string{{"genres:Fantasy", "GENRES:Fantasy"}, {"missing:asin", "Missing:ASIN"}, {"narrators:Nigel Planer", "narrator:Nigel Planer"}} {
			a := num(t, call(t, "library_items", withMessy(map[string]any{"filter": pair[0]}))["total"], "total")
			b := num(t, call(t, "library_items", withMessy(map[string]any{"filter": pair[1]}))["total"], "total")
			if a == 0 || a == len(messyBooks) || a != b {
				t.Errorf("%s finds %d, %s finds %d: want the same few books", pair[0], a, pair[1], b)
			}
		}
		// a limit past the cap is taken, not refused
		if got := rows(t, call(t, "library_items", withMessy(map[string]any{"limit": 5000}))["items"], "items"); len(got) != len(messyBooks) {
			t.Errorf("limit 5000 answered %d rows, want all %d", len(got), len(messyBooks))
		}

		for _, bad := range []struct {
			args map[string]any
			want string
		}{
			{map[string]any{"sort": "titel"}, `unknown sort "titel"; choose one of: added, author`},
			{map[string]any{"filter": "genra:Fantasy"}, `unknown filter group "genra"`},
			{map[string]any{"filter": "missing:asn"}, "missing:asn is not a filter the server knows"},
			{map[string]any{"filter": "recent:yes"}, "filter recent takes no value"},
			{map[string]any{"filter": "genres"}, "filter genres needs a value"},
		} {
			if msg := callErr(t, "library_items", withMessy(bad.args)); !strings.Contains(msg, bad.want) {
				t.Errorf("%v: %s, want %q", bad.args, msg, bad.want)
			}
		}
		if msg := callErr(t, "library_items", map[string]any{"library": "Podcasts", "filter": "authors:Robert Evans"}); !strings.Contains(msg, "a podcast library cannot be filtered by authors") {
			t.Errorf("a podcast library by author: %s", msg)
		}
	})

	t.Run("author_list", func(t *testing.T) {
		byName := everyPageOnce(t, "author_list", "authors", withMessy(map[string]any{"sort": "name"}), 2)
		ordered(t, "name", byName, "name", false)
		ordered(t, "name descending", everyPageOnce(t, "author_list", "authors", withMessy(map[string]any{"sort": "name", "desc": true}), 2), "name", true)
		ordered(t, "books descending", everyPageOnce(t, "author_list", "authors", withMessy(map[string]any{"sort": "Books", "desc": true}), 2), "books", true)
		if msg := callErr(t, "author_list", withMessy(map[string]any{"sort": "surname"})); !strings.Contains(msg, `unknown sort "surname"`) {
			t.Errorf("an unknown author sort: %s", msg)
		}
	})

	t.Run("series_list", func(t *testing.T) {
		byName := everyPageOnce(t, "series_list", "series", withMessy(map[string]any{"sort": "name"}), 2)
		ordered(t, "name", byName, "name", false)
		ordered(t, "books descending", everyPageOnce(t, "series_list", "series", withMessy(map[string]any{"sort": "books", "desc": true}), 2), "books", true)
		everyPageOnce(t, "series_list", "series", withMessy(map[string]any{"sort": "added"}), 2)
		if msg := callErr(t, "series_list", withMessy(map[string]any{"sort": "length"})); !strings.Contains(msg, `unknown sort "length"`) {
			t.Errorf("an unknown series sort: %s", msg)
		}
	})
}

// toRows turns rows back into the decoded list valuesIn and titlesIn read.
func toRows(rs []map[string]any) any {
	out := make([]any, 0, len(rs))
	for _, r := range rs {
		out = append(out, r)
	}
	return out
}

// A shelf of matched books audited a window at a time while it is being
// fixed, then tagged with its store a window at a time. audit_matched counts
// matched books in the order they were added, which no fix changes: a title
// fixed between two calls must not move the book across the offset,
// skipping one. item_match_tag walks the matched books the same way, and
// each must be tagged once, however many windows it takes.
func TestJourneyMatchedBooksPageInTheOrderAdded(t *testing.T) {
	requireProviders(t)

	// Foundation, and four Discworld books given its asin for the journey:
	// one recording on the cassette serves every lookup
	paths := []string{
		"Isaac Asimov/Foundation",
		"Terry Pratchett/Discworld - 02 - The Light Fantastic",
		"Terry Pratchett/Discworld - 03 - Equal Rites",
		"Terry Pratchett/Discworld - 05 - Sourcery",
		"Terry Pratchett/Discworld - 06 - Wyrd Sisters",
	}
	var ids []string
	before := map[string]map[string]any{}
	for _, p := range paths {
		id := messyID(t, p)
		ids = append(ids, id)
		before[id] = bookNow(t, id)
	}
	t.Cleanup(func() {
		for _, id := range ids {
			putBackBook(t, id, before[id])
		}
	})
	for _, id := range ids[1:] {
		call(t, "item_edit", map[string]any{"item": id, "asin": foundationASIN})
	}

	var added []string
	for _, id := range valuesIn(t, call(t, "library_items", withMessy(map[string]any{"sort": "added", "limit": 100}))["items"], "items", "id") {
		if slices.Contains(ids, id) {
			added = append(added, id)
		}
	}
	auditFrom := func(t *testing.T, offset int, extra map[string]any) ([]string, int, any) {
		t.Helper()
		out := call(t, "audit_matched", withMessy(withArgs(map[string]any{"providers": []any{"audible"}, "limit": 2, "offset": offset}, extra)))
		return valuesIn(t, out["findings"], "findings", "id"), num(t, out["items_scanned"], "items_scanned"), out["next_offset"]
	}

	found, scanned, next := auditFrom(t, 0, nil)
	if !slices.Equal(found, added[:2]) || scanned != 2 || fmt.Sprint(next) != "2" {
		t.Errorf("offset 0 = %v (%d scanned, next %v), want %v", found, scanned, next, added[:2])
	}
	// the first book fixed between calls: a new title puts it last by title,
	// and not anywhere else in the order added
	call(t, "item_edit", map[string]any{"item": added[0], "title": "Zzyzx Retitled Mid-Audit"})
	found, scanned, next = auditFrom(t, 2, nil)
	if !slices.Equal(found, added[2:4]) || scanned != 2 || fmt.Sprint(next) != "4" {
		t.Errorf("offset 2 = %v (%d scanned, next %v), want %v", found, scanned, next, added[2:4])
	}
	found, scanned, next = auditFrom(t, 4, nil)
	if !slices.Equal(found, added[4:]) || scanned != 1 || next != nil {
		t.Errorf("offset 4 = %v (%d scanned, next %v), want %v and the end", found, scanned, next, added[4:])
	}
	// an offset inside a window is honoured, not rounded down to one
	if found, _, next = auditFrom(t, 1, nil); !slices.Equal(found, added[1:3]) || fmt.Sprint(next) != "3" {
		t.Errorf("offset 1 = %v (next %v), want %v", found, next, added[1:3])
	}
	// under a filter the window is still two matched books, however many
	// of the filter's books have no asin: Discworld holds unmatched books
	// between the four given an asin
	var discworld []string
	for _, id := range added {
		if id != messyID(t, "Isaac Asimov/Foundation") {
			discworld = append(discworld, id)
		}
	}
	series := map[string]any{"filter": "series:Discworld"}
	if found, scanned, next = auditFrom(t, 0, series); !slices.Equal(found, discworld[:2]) || scanned != 2 || fmt.Sprint(next) != "2" {
		t.Errorf("series:Discworld offset 0 = %v (%d scanned, next %v), want %v", found, scanned, next, discworld[:2])
	}
	if found, scanned, next = auditFrom(t, 2, series); !slices.Equal(found, discworld[2:]) || scanned != 2 || next != nil {
		t.Errorf("series:Discworld offset 2 = %v (%d scanned, next %v), want %v and the end", found, scanned, next, discworld[2:])
	}

	// tagged a window at a time: every book once, and none left for a sweep
	// that is not windowed
	var tagged []string
	for offset, calls := 0, 0; calls < 10; calls++ {
		out := call(t, "item_match_tag", map[string]any{"library": "Messy", "providers": []any{"audible"}, "limit": 2, "offset": offset})
		got := rows(t, out["rows"], "rows")
		if num(t, out["tagged"], "tagged") != len(got) || num(t, out["checked"], "checked") != len(got) {
			t.Errorf("offset %d: %v, want each book checked and tagged", offset, out)
		}
		for _, r := range got {
			tagged = append(tagged, text(r["id"]))
			if r["provider"] != "audible" {
				t.Errorf("%v was tagged %v", r["title"], r["provider"])
			}
		}
		if out["next_offset"] == nil {
			break
		}
		if n := num(t, out["next_offset"], "next_offset"); n != offset+len(got) {
			t.Fatalf("offset %d says next_offset %d after %d books", offset, n, len(got))
		}
		offset = num(t, out["next_offset"], "next_offset")
	}
	slices.Sort(tagged)
	want := slices.Clone(ids)
	slices.Sort(want)
	if !slices.Equal(tagged, want) {
		t.Errorf("tagged %v, want each of %v once", tagged, want)
	}
	rest := call(t, "item_match_tag", map[string]any{"library": "Messy", "providers": []any{"audible"}, "limit": 100})
	if num(t, rest["already_tagged"], "already_tagged") != len(ids) || num(t, rest["tagged"], "tagged") != 0 {
		t.Errorf("after the pages: %v, want all %d already tagged", rest, len(ids))
	}
	for _, id := range ids {
		if tags := listOrNone(t, bookNow(t, id)["tags"], "tags"); countOf(tags, storeTag) != 1 {
			t.Errorf("%s tags = %v, want %s once", id, tags, storeTag)
		}
	}
}

// expanseShelf is everything the catalogue says about The Expanse in Fiction.
type expanseShelf struct {
	Items, SeriesBooks, AuthorBooks, NarratorBooks int
	Sequence, Missing                              []string
	SeriesIDs, AuthorIDs                           []string // as library_filters lists them
}

// readExpanse asks every catalogue tool about The Expanse.
func readExpanse(t *testing.T) expanseShelf {
	t.Helper()

	var s expanseShelf
	s.Items = num(t, call(t, "library_get", map[string]any{"library": "Fiction"})["items"], "items")
	for _, row := range rows(t, call(t, "series_list", map[string]any{"library": "Fiction"})["series"], "series") {
		if row["name"] == "The Expanse" {
			s.SeriesBooks = num(t, row["books"], "books")
			s.Sequence = listOrNone(t, row["sequence"], "sequence")
			slices.Sort(s.Sequence)
		}
	}
	for _, gap := range rows(t, call(t, "audit_series", map[string]any{"library": "Fiction"})["gaps"], "gaps") {
		if gap["name"] == "The Expanse" {
			s.Missing = listOrNone(t, gap["missing"], "missing")
		}
	}
	s.AuthorBooks = len(rows(t, call(t, "author_get", map[string]any{"library": "Fiction", "author": "James S. A. Corey"})["books"], "books"))
	for _, n := range rows(t, call(t, "narrator_list", map[string]any{"library": "Fiction"})["narrators"], "narrators") {
		if n["name"] == "Jefferson Mays" {
			s.NarratorBooks = num(t, n["books"], "books")
		}
	}
	filters := call(t, "library_filters", map[string]any{"library": "Fiction"})
	for _, r := range rows(t, filters["series"], "series") {
		if r["name"] == "The Expanse" {
			s.SeriesIDs = append(s.SeriesIDs, text(r["id"]))
		}
	}
	for _, r := range rows(t, filters["authors"], "authors") {
		if r["name"] == "James S. A. Corey" {
			s.AuthorIDs = append(s.AuthorIDs, text(r["id"]))
		}
	}
	return s
}

// The Expanse in Fiction skips its second book. The missing book is copied
// into the library folder, scanned in, linked into its series, and then
// deleted with its files: at each step every tool that counts the catalogue
// - the library, the series, the gap audit, the author, the narrator, the
// filter vocabulary - must agree, and after the delete each must be back
// where it began. It catches a count one tool updates and another does not,
// a scan that makes a second author or series record for a name it already
// has, and a delete that leaves the folder, or the book, behind.
func TestJourneyAMissingBookJoinsItsSeries(t *testing.T) {
	data := dataDir()
	if data == "" {
		t.Skip("ABS_TEST_DATA is not set")
	}

	begin := readExpanse(t)
	if begin.Items != 7 || begin.SeriesBooks != 2 || !slices.Equal(begin.Sequence, []string{"1", "3"}) || !slices.Equal(begin.Missing, []string{"2"}) ||
		begin.AuthorBooks != 2 || begin.NarratorBooks != 2 || len(begin.SeriesIDs) != 1 || len(begin.AuthorIDs) != 1 {
		t.Fatalf("The Expanse is not as seeded: %+v", begin)
	}

	dir := filepath.Join(data, "fiction", "James S. A. Corey", "Caliban's War")
	audio, err := os.ReadFile(filepath.Join(data, "fiction", "Isaac Asimov", "Foundation", "01.mp3"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "01.mp3"), audio, 0o666); err != nil {
		t.Fatal(err)
	}
	var id string
	t.Cleanup(func() {
		// only what a failure left: the journey itself deletes both
		if id != "" {
			if _, err := invoke("item_get", map[string]any{"item": id}); err == nil {
				call(t, "item_delete", map[string]any{"item": id, "confirm": true, "delete_files": true})
			}
		}
		if err := os.RemoveAll(dir); err != nil {
			t.Errorf("removing %s: %v", dir, err)
		}
		if err := waitForItems("Fiction", 7); err != nil {
			t.Error(err)
		}
	})

	call(t, "library_scan", map[string]any{"library": "Fiction"})
	if err := waitForItems("Fiction", 8); err != nil {
		t.Fatal(err)
	}
	waitIdle(t)
	id = itemID(t, "Fiction", "Caliban's War")

	// the newest thing on the server, in every library and in its own
	if got := valuesIn(t, call(t, "library_recent", map[string]any{"limit": 1})["items"], "items", "id"); !slices.Equal(got, []string{id}) {
		t.Errorf("library_recent's newest = %v, want Caliban's War", got)
	}
	if got := valuesIn(t, call(t, "library_items", map[string]any{"library": "Fiction", "sort": "added", "desc": true, "limit": 1})["items"], "items", "id"); !slices.Equal(got, []string{id}) {
		t.Errorf("Fiction's newest = %v, want Caliban's War", got)
	}

	call(t, "item_edit", map[string]any{"library": "Fiction", "item": "Caliban's War", "add_series": []any{"The Expanse #2"}, "narrators": []any{"Jefferson Mays"}})
	linked := readExpanse(t)
	if linked.Items != 8 || linked.SeriesBooks != 3 || !slices.Equal(linked.Sequence, []string{"1", "2", "3"}) || linked.Missing != nil ||
		linked.AuthorBooks != 3 || linked.NarratorBooks != 3 || !slices.Equal(linked.SeriesIDs, begin.SeriesIDs) || !slices.Equal(linked.AuthorIDs, begin.AuthorIDs) {
		t.Errorf("with the book linked: %+v, want 8 items, the series whole at 3 books with no gap, 3 each for the author and the narrator, and the same records", linked)
	}
	if got := titlesIn(t, call(t, "series_get", map[string]any{"library": "Fiction", "series": "The Expanse"})["books"], "books"); !slices.Equal(got, []string{"Leviathan Wakes", "Caliban's War", "Abaddon's Gate"}) {
		t.Errorf("The Expanse in order = %v", got)
	}

	// deleted with its folder: asked first, which changes nothing
	preview := call(t, "item_delete", map[string]any{"library": "Fiction", "item": "Caliban's War", "delete_files": true})
	if preview["would_delete"] != "Caliban's War" || !strings.Contains(text(preview["files"]), "Caliban's War") || preview["deleted"] != nil {
		t.Errorf("the unconfirmed delete = %v, want what it would remove", preview)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("the unconfirmed delete touched the folder: %v", err)
	}
	gone := call(t, "item_delete", map[string]any{"library": "Fiction", "item": "Caliban's War", "delete_files": true, "confirm": true})
	if gone["deleted"] != "Caliban's War" || gone["files_removed"] != true {
		t.Errorf("the delete = %v", gone)
	}
	eventually(t, "the folder going", func() error {
		if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("still there: %v", err)
		}
		return nil
	})
	if err := waitForItems("Fiction", 7); err != nil {
		t.Fatal(err)
	}
	if end := readExpanse(t); fmt.Sprintf("%+v", end) != fmt.Sprintf("%+v", begin) {
		t.Errorf("after the delete:\n  %+v\nwant it as it began:\n  %+v", end, begin)
	}
}

// The reads a caller takes the first row of, or reads a column from, asked
// the question they answer: a title searched for comes first, the newest
// additions come first across every library, and a series says which of its
// books the reader has finished as they finish them. It catches an answer
// that holds the right rows in the wrong order, which every count-only test
// passes.
func TestJourneyReadsAnswerWhatWasAsked(t *testing.T) {
	if !ready {
		t.Skip("ABS_SERVER and ABS_TOKEN are not set")
	}

	t.Run("a title searched for comes first", func(t *testing.T) {
		for _, c := range []struct {
			library, query, want string
		}{
			{"", "Second Foundation", "Second Foundation"},
			{"Fiction", "Foundation", "Foundation"},
			{"", "Foundation and Empire", "Foundation and Empire"},
			{"Messy", "The Way of Kings", "The Way of Kings"},
			{"Messy", "Reaper Man", "Reaper Man"},
		} {
			args := map[string]any{"query": c.query}
			if c.library != "" {
				args["library"] = c.library
			}
			if got := titlesIn(t, call(t, "library_search", args)["items"], "items"); len(got) == 0 || got[0] != c.want {
				t.Errorf("library_search %q in %q = %v, want %s first", c.query, c.library, got, c.want)
			}
		}
	})

	t.Run("the newest additions first", func(t *testing.T) {
		// when each was added, from the server's own listing
		addedAt := map[string]float64{}
		for _, l := range libraries {
			listing, _ := adminGet(t, "/api/libraries/"+libraryID(t, l.Name)+"/items?limit=500").(map[string]any)
			results, _ := listing["results"].([]any)
			for _, r := range results {
				row, _ := r.(map[string]any)
				addedAt[text(row["id"])], _ = row["addedAt"].(float64)
			}
		}
		recent := valuesIn(t, call(t, "library_recent", map[string]any{"limit": 1000})["items"], "items", "id")
		if len(recent) != len(addedAt) {
			t.Errorf("library_recent listed %d items, the server has %d", len(recent), len(addedAt))
		}
		for i := 1; i < len(recent); i++ {
			if addedAt[recent[i]] > addedAt[recent[i-1]] {
				t.Errorf("%s (added %.0f) comes after %s (added %.0f)", recent[i], addedAt[recent[i]], recent[i-1], addedAt[recent[i-1]])
			}
		}
		if top := valuesIn(t, call(t, "library_recent", map[string]any{"limit": 3})["items"], "items", "id"); !slices.Equal(top, recent[:3]) {
			t.Errorf("limit 3 = %v, want the first three of %v", top, recent[:3])
		}
		// one library is library_items newest first
		for _, lib := range []string{"Fiction", "Messy"} {
			got := valuesIn(t, call(t, "library_recent", map[string]any{"library": lib, "limit": 100})["items"], "items", "id")
			want := valuesIn(t, call(t, "library_items", map[string]any{"library": lib, "sort": "added", "desc": true, "limit": 100})["items"], "items", "id")
			if !slices.Equal(got, want) {
				t.Errorf("library_recent in %s = %v, want library_items newest first %v", lib, got, want)
			}
		}
	})

	t.Run("a series says which books are finished", func(t *testing.T) {
		titles := []string{"Foundation", "Foundation and Empire", "Second Foundation"}
		t.Cleanup(func() {
			for _, title := range titles {
				_, _ = invoke("user_progress_remove", map[string]any{"library": "Fiction", "item": title})
			}
		})
		finished := func(t *testing.T) (map[string]bool, int, bool) {
			t.Helper()
			out := call(t, "series_get", map[string]any{"library": "Fiction", "series": "Foundation"})
			books := map[string]bool{}
			for _, b := range rows(t, out["books"], "books") {
				books[text(b["title"])] = b["finished"] == true
			}
			complete, _ := out["complete"].(bool)
			return books, num(t, out["finished"], "finished"), complete
		}
		check := func(t *testing.T, when string, done ...string) {
			t.Helper()
			books, n, complete := finished(t)
			for _, title := range titles {
				if books[title] != slices.Contains(done, title) {
					t.Errorf("%s: %s finished = %v", when, title, books[title])
				}
			}
			if n != len(done) || complete != (len(done) == len(titles)) {
				t.Errorf("%s: finished %d, complete %v, want %d and %v", when, n, complete, len(done), len(done) == len(titles))
			}
		}

		check(t, "before")
		call(t, "user_progress_set", map[string]any{"library": "Fiction", "item": "Foundation and Empire", "finished": true})
		check(t, "one finished", "Foundation and Empire")
		for _, title := range []string{"Foundation", "Second Foundation"} {
			call(t, "user_progress_set", map[string]any{"library": "Fiction", "item": title, "finished": true})
		}
		check(t, "all finished", titles...)
		call(t, "user_progress_set", map[string]any{"library": "Fiction", "item": "Foundation and Empire", "finished": false})
		check(t, "one unfinished again", "Foundation", "Second Foundation")
		for _, title := range titles {
			call(t, "user_progress_remove", map[string]any{"library": "Fiction", "item": title})
		}
		check(t, "progress removed")
	})
}
