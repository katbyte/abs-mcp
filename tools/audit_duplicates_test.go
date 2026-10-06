package tools

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/katbyte/abs-mcp/sdk/abs"
)

// A matched copy and the unmatched copy scanned in beside it share a title
// and author and nothing else; two recordings with their own asins are
// editions and stay apart, whichever copy their title reaches.
func TestDuplicatesJoinACopyByAnyKey(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/libraries/"+libID+"/items", page(
		item("a", "Dune", `"authorName":"Frank Herbert","asin":"B002V1OF70"`, ""),
		item("b", "Dune", `"authorName":"Frank Herbert"`, ""),
		item("c", "Small Gods", `"authorName":"Terry Pratchett","asin":"B0FULLCAST"`, ""),
		item("d", "Small Gods", `"authorName":"Terry Pratchett","asin":"B0NIGEL"`, ""),
		item("g", "Small Gods", `"authorName":"Terry Pratchett"`, ""),
		item("e", "Solo", `"authorName":"X","isbn":"978-1"`, ""),
		// retitled, filed as a copy of the same book: the isbn joins them
		`{"id":"h","libraryId":"`+libID+`","mediaType":"book","relPath":"A/Solo - Copy","media":{"metadata":{"title":"Solo Again","authorName":"Y","isbn":"9781"}}}`,
		// one print isbn on two books: an isbn is the print edition's, and
		// joins only copies nothing tells apart
		item("i1", "Mistborn", `"authorName":"Brandon Sanderson","isbn":"9780765350381"`, ""),
		item("i2", "The Well of Ascension", `"authorName":"Brandon Sanderson","isbn":"9780765350381"`, ""),
	))
	call := toolCaller(t, f)

	out, err := call("audit_duplicates", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := num(t, out["items_scanned"]); got != 9 {
		t.Errorf("items_scanned = %d, want 9", got)
	}
	groups := map[string][]string{}
	for _, g := range list(t, out["groups"]) {
		var ids []string
		for _, it := range list(t, g["items"]) {
			ids = append(ids, str(t, it["id"]))
		}
		groups[str(t, g["key"])] = ids
	}
	want := map[string][]string{
		"title:dune|frank herbert":         {"a", "b"},
		"title:small gods|terry pratchett": {"c", "g"},
		"isbn:9781":                        {"e", "h"},
	}
	if !reflect.DeepEqual(groups, want) || num(t, out["total_findings"]) != 3 {
		t.Errorf("groups = %v, want %v", groups, want)
	}
	split := map[string]bool{}
	for _, row := range list(t, out["split"]) {
		split[str(t, row["key"])] = true
	}
	if !split["isbn:9780765350381"] {
		t.Errorf("split = %v, want the two books one isbn joins kept apart and shown", split)
	}
}

// A title alone does not join two readings: both name their readers and
// share none, so they are two recordings of one book - a full-cast and a
// single-narrator set kept on purpose - not one held twice. The same reader
// written two ways is one reader, and a copy naming none still joins.
func TestDuplicatesKeepTwoReadingsApart(t *testing.T) {
	t.Parallel()

	book := func(id, narrator string) *abs.Item {
		it := &abs.Item{ID: id, MediaType: "book"}
		it.Media.Metadata.Title = "Wyrd Sisters"
		it.Media.Metadata.AuthorName = "Terry Pratchett"
		it.Media.Metadata.NarratorName = narrator
		return it
	}
	groupsOf := func(items ...*abs.Item) [][]string {
		d := newDupCollector()
		for _, it := range items {
			d.add(it)
		}
		groups := d.groups()
		out := make([][]string, 0, len(groups))
		for _, g := range groups {
			var ids []string
			for _, it := range g.Items {
				ids = append(ids, it.ID)
			}
			out = append(out, ids)
		}
		return out
	}

	if got := groupsOf(book("full", "Peter Serafinowicz, Bill Nighy"), book("single", "Nigel Planer")); len(got) != 0 {
		t.Errorf("two readings were grouped: %v", got)
	}
	if got := groupsOf(book("a", "Nigel Planer"), book("b", "Planer, Nigel")); len(got) != 1 {
		t.Errorf("one reader written two ways was not grouped: %v", got)
	}
	if got := groupsOf(book("a", "Nigel Planer"), book("b", "")); len(got) != 1 {
		t.Errorf("a copy naming no reader was not grouped: %v", got)
	}
}

// A title from a bad album tag is shared by books that are not one work: all
// nine of Radclyffe's Honor books carried the title "Honor". The folders'
// numbers keep them apart, and so do lengths too far apart for one recording.
func TestDuplicatesKeepBooksOfASeriesApart(t *testing.T) {
	t.Parallel()

	honor := func(id, path string, seconds float64) *abs.Item {
		it := &abs.Item{ID: id, MediaType: "book", RelPath: "Radclyffe/" + path}
		it.Media.Metadata.Title = "Honor"
		it.Media.Metadata.AuthorName = "Radclyffe"
		it.Media.Duration = seconds
		return it
	}
	d := newDupCollector()
	for _, it := range []*abs.Item{
		honor("1", "Honor - 01 - Above All", 30000),
		honor("2", "Honor - 02 - Honor Bound", 31000),
		honor("2b", "Honor - 02 - Honor Bound (single file)", 31020),
		honor("3", "Honor - 03 - Love & Honor", 30500),
		// no numbers: the length alone says two books, and not one
		honor("x", "Innocent Hearts", 20000),
		honor("y", "Passion's Bright Fury", 40000),
		honor("y2", "Passion's Bright Fury (mp3)", 39000),
	} {
		d.add(it)
	}
	groups := d.groups()
	got := make([][]string, 0, len(groups))
	for _, g := range groups {
		var ids []string
		for _, it := range g.Items {
			ids = append(ids, it.ID)
		}
		got = append(got, ids)
	}
	slices.SortFunc(got, func(a, b []string) int { return strings.Compare(a[0], b[0]) })
	if want := [][]string{{"2", "2b"}, {"y", "y2"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("groups = %v, want %v: book 2's two copies, and nothing joining two numbers or lengths far apart", got, want)
	}
}

// One recording filed under two numberings is one book held twice, and two
// books under one bad title are not, whatever order the listing gives them
// in.
func TestDuplicatesGroupByTheBookNotItsNumber(t *testing.T) {
	t.Parallel()

	book := func(id, path, title string, seconds float64, series ...string) *abs.Item {
		it := &abs.Item{ID: id, MediaType: "book", RelPath: path}
		it.Media.Metadata.Title = title
		it.Media.Metadata.AuthorName = "Terry Pratchett"
		it.Media.Metadata.SeriesName = strings.Join(series, ", ")
		it.Media.Duration = seconds
		return it
	}
	groupsOf := func(items ...*abs.Item) [][]string {
		d := newDupCollector()
		for _, it := range items {
			d.add(it)
		}
		groups := d.groups()
		out := make([][]string, 0, len(groups))
		for _, g := range groups {
			var ids []string
			for _, it := range g.Items {
				ids = append(ids, it.ID)
			}
			slices.Sort(ids)
			out = append(out, ids)
		}
		slices.SortFunc(out, func(a, b []string) int { return strings.Compare(a[0], b[0]) })
		return out
	}
	if got := groupsOf(book("dw", "Terry Pratchett/Discworld/04 - Mort", "Mort", 30000), book("de", "Terry Pratchett/Death/01 - Mort", "Mort", 30000)); !reflect.DeepEqual(got, [][]string{{"de", "dw"}}) {
		t.Errorf("Mort filed as Discworld 4 and Death 1 = %v, want one group", got)
	}
	// numbered in two orders in one series: kept apart, as a false split
	// costs less than a false group, and shown in split
	if got := groupsOf(book("n6", "C. S. Lewis/The Chronicles of Narnia - 06 - The Magician's Nephew", "The Magician's Nephew", 20000, "The Chronicles of Narnia #6"),
		book("n1", "C. S. Lewis/The Chronicles of Narnia - 01 - The Magician's Nephew", "The Magician's Nephew", 20000, "The Chronicles of Narnia #1")); len(got) != 0 {
		t.Errorf("Narnia numbered in two orders = %v, want no group", got)
	}
	if got := groupsOf(book("h1", "Radclyffe/Honor - 01 - Above All", "Honor", 30000), book("h2", "Radclyffe/Honor Bound", "Honor", 30900, "Honor #2")); len(got) != 0 {
		t.Errorf("Honor 1 by its folder and 2 by its series = %v, want no group", got)
	}
	// copies joined by an isbn, two with their own asins: the same group
	// whichever the listing gives first
	isbn := func(id, asin string) *abs.Item {
		it := book(id, "Terry Pratchett/Small Gods "+id, "Small Gods "+id, 30000)
		it.Media.Metadata.ISBN, it.Media.Metadata.ASIN = "9780552152976", asin
		return it
	}
	if one, other := groupsOf(isbn("x", "B01"), isbn("y", "B02"), isbn("z", "")), groupsOf(isbn("y", "B02"), isbn("x", "B01"), isbn("z", "")); !reflect.DeepEqual(one, other) {
		t.Errorf("an isbn's groups depend on the listing's order: %v against %v", one, other)
	}
	// three copies, the ends 18% apart: the same groups in either order
	a, b, c := book("a", "Terry Pratchett/Eric", "Eric", 10000), book("b", "Terry Pratchett/Eric (2)", "Eric", 11500), book("c", "Terry Pratchett/Eric (3)", "Eric", 9700)
	if one, other := groupsOf(a, b, c), groupsOf(c, a, b); !reflect.DeepEqual(one, other) {
		t.Errorf("the groups depend on the listing's order: %v against %v", one, other)
	}
}

// A reader named only in a folder's brackets is a reader: two readings of one
// book, each naming its own, are not one held twice, and a note on the copy
// names nobody.
func TestDuplicatesReadTheReaderInBrackets(t *testing.T) {
	t.Parallel()

	book := func(id, path string, seconds float64, narrator string) *abs.Item {
		it := &abs.Item{ID: id, MediaType: "book", RelPath: path}
		it.Media.Metadata.Title = "Starship Troopers"
		it.Media.Metadata.AuthorName = "Robert A. Heinlein"
		it.Media.Metadata.NarratorName = narrator
		it.Media.Duration = seconds
		return it
	}
	groupsOf := func(items ...*abs.Item) int {
		d := newDupCollector()
		for _, it := range items {
			d.add(it)
		}
		return len(d.groups())
	}
	if n := groupsOf(book("bray", "Robert A. Heinlein/Starship Troopers", 29772, "R.C. Bray"), book("wilson", "Robert A. Heinlein/Starship Troopers (Wilson)", 34776, ""),
		book("james", "Robert A. Heinlein/Starship Troopers (James)", 35676, "Lloyd James")); n != 0 {
		t.Errorf("three readers, two named only in brackets = %d groups, want none", n)
	}
	if n := groupsOf(book("a", "Robert A. Heinlein/Starship Troopers (James)", 35676, ""), book("b", "Robert A. Heinlein/Starship Troopers", 35690, "Lloyd James")); n != 1 {
		t.Errorf("one reader, in brackets on one and the field on the other = %d groups, want one", n)
	}
	if n := groupsOf(book("a", "Robert A. Heinlein/Starship Troopers (copy)", 35676, ""), book("b", "Robert A. Heinlein/Starship Troopers (old rip)", 35690, "")); n != 1 {
		t.Errorf("two notes on copies = %d groups, want one", n)
	}
}

// A title many books share, a tag's placeholder, joins none of them: one
// split row says so, counting them all and listing the first parts, and
// each copy is read against at most dupApartMax others for incomplete.
func TestDuplicatesBoundAPlaceholderTitle(t *testing.T) {
	t.Parallel()

	const n = 200
	d := newDupCollector()
	for i := range n {
		name := string(rune('a'+i%26)) + string(rune('a'+i/26%26)) + string(rune('a'+i/676))
		it := &abs.Item{ID: fmt.Sprintf("i%04d", i), MediaType: "book", RelPath: "Unknown/The Tale of " + name}
		it.Media.Metadata.Title, it.Media.Metadata.AuthorName = "Audiobook", "Unknown"
		it.Media.Duration = float64(3600 + (i*7919)%(40*3600))
		d.add(it)
	}
	if groups := d.groups(); len(groups) != 0 {
		t.Errorf("groups %d, want none: a placeholder title joins nothing", len(groups))
	}
	if len(d.splits) != 1 || d.splits[0].Copies != n || len(d.splits[0].Parts) != dupSplitParts || !strings.Contains(fmt.Sprint(d.splits[0].Why), "placeholder") {
		t.Errorf("split = %d rows, want one counting %d copies with %d parts listed and the placeholder said: %+v", len(d.splits), n, dupSplitParts, d.splits)
	}
	if len(d.apart) > n*dupApartMax || len(d.close) > dupClosePerKey {
		t.Errorf("%d pairs and %d close pairs, want at most %d and %d", len(d.apart), len(d.close), n*dupApartMax, dupClosePerKey)
	}

	// copies nothing else tells apart, one length, one folder name: a
	// placeholder title still joins none of them
	d = newDupCollector()
	for i := range dupPlaceholderMax + 1 {
		it := &abs.Item{ID: fmt.Sprintf("j%04d", i), MediaType: "book", RelPath: fmt.Sprintf("Unknown/Audiobook - Copy %d", i)}
		it.Media.Metadata.Title, it.Media.Metadata.AuthorName = "Audiobook", "Unknown"
		it.Media.Duration = 36000
		d.add(it)
	}
	if groups := d.groups(); len(groups) != 0 {
		t.Errorf("%d groups of a placeholder title's copies, want none", len(groups))
	}

	// an isbn is no placeholder, however many copies carry it: its parts
	// are split by what tells them apart, not by the count
	d = newDupCollector()
	for i := range dupPlaceholderMax + 1 {
		it := &abs.Item{ID: fmt.Sprintf("k%04d", i), MediaType: "book", RelPath: fmt.Sprintf("Author %d/Book %d", i, i)}
		it.Media.Metadata.Title, it.Media.Metadata.AuthorName = fmt.Sprintf("Book %d", i), fmt.Sprintf("Author %d", i)
		it.Media.Metadata.ISBN = "9780765350381"
		it.Media.Duration = float64(36000 + 36000*(i%2))
		d.add(it)
	}
	d.groups()
	if len(d.splits) != 1 || strings.Contains(fmt.Sprint(d.splits[0].Why), "placeholder") {
		t.Errorf("split = %+v, want the isbn's row with no placeholder said", d.splits)
	}
	// and joins the copies nothing tells apart, as many as there are
	d = newDupCollector()
	for i := range dupPlaceholderMax + 1 {
		it := &abs.Item{ID: fmt.Sprintf("m%04d", i), MediaType: "book", RelPath: fmt.Sprintf("Shelf %d/Dune", i)}
		it.Media.Metadata.Title, it.Media.Metadata.AuthorName = "Dune", "Frank Herbert"
		it.Media.Metadata.ISBN = "9780441172719"
		it.Media.Duration = 36000
		d.add(it)
	}
	if groups := d.groups(); len(groups) != 1 || len(groups[0].Items) != dupPlaceholderMax+1 {
		t.Errorf("%d groups, want the isbn's %d copies in one", len(groups), dupPlaceholderMax+1)
	}
}

// An ebook and the recording of its book share a title and an isbn but are
// not two copies of one thing: kept apart, and split says why.
func TestDuplicatesKeepAnEbookApart(t *testing.T) {
	t.Parallel()

	d := newDupCollector()
	for _, c := range []struct {
		id, path string
		seconds  float64
	}{{"a", "Frank Herbert/Dune", 75000}, {"b", "Frank Herbert/Dune (ebook)", 0}} {
		it := &abs.Item{ID: c.id, MediaType: "book", RelPath: c.path}
		it.Media.Metadata.Title, it.Media.Metadata.AuthorName = "Dune", "Frank Herbert"
		it.Media.Metadata.ISBN = "9780441172719"
		it.Media.Duration = c.seconds
		d.add(it)
	}
	if groups := d.groups(); len(groups) != 0 {
		t.Errorf("%d groups, want none", len(groups))
	}
	if len(d.splits) != 2 || !strings.Contains(fmt.Sprint(d.splits), "no audio") {
		t.Errorf("split = %+v, want the title's and the isbn's rows saying one holds no audio", d.splits)
	}
}

// A year beside a reader in a folder's brackets is not part of the name the
// split row gives.
func TestDuplicatesNameAReaderWithoutTheYear(t *testing.T) {
	t.Parallel()

	d := newDupCollector()
	for _, c := range []struct{ id, path string }{{"a", "Frank Herbert/Dune [Scott Brick 2007]"}, {"b", "Frank Herbert/Dune [George Guidall 1993]"}} {
		it := &abs.Item{ID: c.id, MediaType: "book", RelPath: c.path}
		it.Media.Metadata.Title, it.Media.Metadata.AuthorName = "Dune", "Frank Herbert"
		it.Media.Duration = 72000
		d.add(it)
	}
	if groups := d.groups(); len(groups) != 0 {
		t.Errorf("%d groups, want none", len(groups))
	}
	if why := fmt.Sprint(d.splits); len(d.splits) != 1 || !strings.Contains(why, `"George Guidall" and "Scott Brick"`) {
		t.Errorf("split = %+v, want the two readers named without their years", d.splits)
	}
}

// Of two editions in reach, a copy with no asin joins the one it matches,
// not the longest.
func TestUnmatchedCopyJoinsTheEditionItMatches(t *testing.T) {
	t.Parallel()

	groups, _, _ := dupShape(t, dupFixture(t, []string{
		shelfBook("e1", "Frank Herbert/Dune (Full Cast)", "Dune", "Frank Herbert", "Full Cast", 79200, 1, "B0FULLCAST"),
		shelfBook("e2", "Frank Herbert/Dune", "Dune", "Frank Herbert", "Scott Brick", 72000, 1, "B002V1OF70"),
		shelfBook("u", "Frank Herbert/Dune (2)", "Dune", "Frank Herbert", "", 72000, 1, ""),
	}, nil))
	if !slices.Equal(groups, []string{"e2+u"}) {
		t.Errorf("groups %v, want the unmatched copy with the edition its length matches", groups)
	}
}

// Every copy a title joins is somewhere: grouped, or in a split row saying
// what kept the parts apart. Two readers the library knows are two readings;
// a note on the copy, "(Audible)", names no reader; one bad title on two
// books splits on its folders.
func TestEveryCopyATitleJoinsIsAccountedFor(t *testing.T) {
	t.Parallel()

	out := dupFixture(t, []string{
		// two readers the library knows: two readings
		shelfBook("k1", "Frank Herbert/Children of Dune (Scott Brick)", "Children of Dune", "Frank Herbert", "", 72000, 1, ""),
		shelfBook("k2", "Frank Herbert/Children of Dune (Simon Vance)", "Children of Dune", "Frank Herbert", "", 72010, 1, ""),
		shelfBook("n1", "Frank Herbert/God Emperor of Dune", "God Emperor of Dune", "Frank Herbert", "Scott Brick", 50000, 1, ""),
		shelfBook("n2", "Frank Herbert/Heretics of Dune", "Heretics of Dune", "Frank Herbert", "Simon Vance", 60000, 1, ""),
		// a reader beside a note on the copy: one recording
		shelfBook("a1", "Frank Herbert/Dune (Scott Brick)", "Dune", "Frank Herbert", "", 72000, 1, ""),
		shelfBook("a2", "Frank Herbert/Dune (Audible)", "Dune", "Frank Herbert", "", 72010, 1, ""),
		// one bad title on two books
		shelfBook("f1", "Isaac Asimov/Foundation", "Foundation", "Isaac Asimov", "", 31200, 1, ""),
		shelfBook("f2", "Isaac Asimov/Foundation and Empire", "Foundation", "Isaac Asimov", "", 34000, 1, ""),
	}, nil)
	groups, _, candidates := dupShape(t, out)
	if !slices.Equal(groups, []string{"a1+a2"}) || len(candidates) != 0 {
		t.Errorf("groups %v, candidates %v; want the reader and the note one group, and no candidate for a title's copies", groups, candidates)
	}
	why := map[string]string{}
	for _, row := range list(t, out["split"]) {
		why[str(t, row["key"])] = fmt.Sprint(row["why"])
		if parts, ok := row["parts"].([]any); !ok || len(parts) != 2 {
			t.Errorf("%v split into %v, want 2 parts", row["key"], row["parts"])
		}
	}
	if !strings.Contains(why["title:children of dune|frank herbert"], "different readers") || !strings.Contains(why["title:foundation|isaac asimov"], "folders name different books") || len(why) != 2 || num(t, out["total_split"]) != 2 {
		t.Errorf("split why = %v, want the two readers and the bad title, and nothing else", why)
	}
}

// One book is one group however its folders are spelled - an article, "&",
// the server's narrator braces, a note on the copy. Numbered in two orders,
// it is kept apart, and shown in split saying so.
func TestOneBookUnderFoldersSpelledTwoWays(t *testing.T) {
	t.Parallel()

	groups, _, _ := dupShape(t, dupFixture(t, []string{
		shelfBook("h1", "J.R.R. Tolkien/The Hobbit {Andy Serkis}", "The Hobbit", "J.R.R. Tolkien", "", 36000, 1, ""),
		shelfBook("h2", "J.R.R. Tolkien/Hobbit - Copy", "The Hobbit", "J.R.R. Tolkien", "", 36010, 1, ""),
		shelfBook("l1", "Radclyffe/Love & Honor", "Love & Honor", "Radclyffe", "", 30000, 1, ""),
		shelfBook("l2", "Radclyffe/Love and Honor", "Love & Honor", "Radclyffe", "", 30010, 1, ""),
		`{"id":"n6","libraryId":"` + libID + `","mediaType":"book","relPath":"C. S. Lewis/The Magician's Nephew","media":{"metadata":{"title":"The Magician's Nephew","authorName":"C. S. Lewis","seriesName":"The Chronicles of Narnia #6"},"duration":20000,"numTracks":1}}`,
		`{"id":"n1","libraryId":"` + libID + `","mediaType":"book","relPath":"C. S. Lewis/Magician's Nephew","media":{"metadata":{"title":"The Magician's Nephew","authorName":"C. S. Lewis","seriesName":"The Chronicles of Narnia #1"},"duration":20000,"numTracks":1}}`,
	}, nil))
	if want := []string{"h1+h2", "l1+l2"}; !slices.Equal(groups, want) {
		t.Errorf("groups %v, want %v", groups, want)
	}
}

// Readers are judged group against group: a copy read by two joins copies
// read by either. A placeholder in the narrator field names no reader.
func TestReadersPooledAndPlaceholdersNone(t *testing.T) {
	t.Parallel()

	groups, _, _ := dupShape(t, dupFixture(t, []string{
		shelfBook("p", "Frank Herbert/Dune", "Dune", "Frank Herbert", "Scott Brick", 72000, 1, ""),
		shelfBook("q", "Frank Herbert/Dune - Copy", "Dune", "Frank Herbert", "Scott Brick, Simon Vance", 72000, 1, ""),
		shelfBook("r", "Frank Herbert/Dune - Copy 2", "Dune", "Frank Herbert", "Simon Vance", 72000, 1, ""),
		shelfBook("u", "Frank Herbert/Children of Dune", "Children of Dune", "Frank Herbert", "Scott Brick", 70000, 1, ""),
		shelfBook("v", "Frank Herbert/Children of Dune - Copy", "Children of Dune", "Frank Herbert", "Unknown", 76000, 1, ""),
		shelfBook("w", "Frank Herbert/Children of Dune - Copy 2", "Children of Dune", "Frank Herbert", "Full Cast", 73000, 1, ""),
	}, nil))
	if want := []string{"p+q+r", "u+v+w"}; !slices.Equal(groups, want) {
		t.Errorf("groups %v, want %v", groups, want)
	}
}

// A narrator field's "Last, First" pair of single words is one reader: two
// copies read by "Barrett, Sean" and "Connery, Sean" share no reader.
func TestReaderNamesReadLastFirstWhole(t *testing.T) {
	t.Parallel()

	for field, want := range map[string][]string{
		"Barrett, Sean":                {"Sean Barrett"},
		"Brick, Scott, Vance, Simon":   {"Scott Brick", "Simon Vance"},
		"Kate Reading, Michael Kramer": {"Kate Reading", "Michael Kramer"},
		"Scott Brick":                  {"Scott Brick"},
	} {
		if got := readerNames(field); !slices.Equal(got, want) {
			t.Errorf("readerNames(%q) = %v, want %v", field, got, want)
		}
	}
	groups, _, _ := dupShape(t, dupFixture(t, []string{
		shelfBook("a", "Frank Herbert/Dune", "Dune", "Frank Herbert", "Barrett, Sean", 30000, 1, ""),
		shelfBook("b", "Frank Herbert/Dune - Copy", "Dune", "Frank Herbert", "Connery, Sean", 30000, 1, ""),
	}, nil))
	if len(groups) != 0 {
		t.Errorf("groups %v, want none: Sean Barrett and Sean Connery are two readers", groups)
	}
}

// An abridged copy an asin joins to the whole one does not stretch the
// title's length rule: a copy of the whole one's length still joins it.
func TestAsinGroupDoesNotStretchTheTitleRule(t *testing.T) {
	t.Parallel()

	groups, _, _ := dupShape(t, dupFixture(t, []string{
		shelfBook("a", "Frank Herbert/Dune", "Dune", "Frank Herbert", "", 80000, 1, "B0DUNE0001"),
		shelfBook("x", "Frank Herbert/Dune (Abridged)", "Dune (Abridged)", "Frank Herbert", "", 30000, 1, "B0DUNE0001"),
		shelfBook("b", "Frank Herbert/Dune - Copy", "Dune", "Frank Herbert", "", 80000, 1, ""),
	}, nil))
	if !slices.Equal(groups, []string{"a+b+x"}) {
		t.Errorf("groups %v, want the copy of the whole one's length with it", groups)
	}
}

// A split row's reasons are what keeps its final parts apart: three
// readings the pooled readers join say nothing about the fourth copy's
// folder.
func TestSplitReasonsAreTheFinalParts(t *testing.T) {
	t.Parallel()

	out := dupFixture(t, []string{
		shelfBook("a", "Frank Herbert/Dune", "Dune", "Frank Herbert", "Scott Brick", 80000, 1, ""),
		shelfBook("b", "Frank Herbert/Dune - Copy", "Dune", "Frank Herbert", "Simon Vance", 80000, 1, ""),
		shelfBook("c", "Frank Herbert/Dune - Copy 2", "Dune", "Frank Herbert", "Scott Brick, Simon Vance", 80000, 1, ""),
		shelfBook("d", "Frank Herbert/Dune 1965 Edition", "Dune", "Frank Herbert", "", 75000, 1, ""),
	}, nil)
	rows := list(t, out["split"])
	if len(rows) != 1 {
		t.Fatalf("split = %v, want one row", rows)
	}
	why := fmt.Sprint(rows[0]["why"])
	if strings.Contains(why, "readers") || !strings.Contains(why, "folders name different books") {
		t.Errorf("why = %s, want the folder reason alone", why)
	}
}

// What a folder or a narrator field says beside the reader does not make two
// books one: a narrator field's wrapper around a name, a format or source
// beside a reader in brackets, a reader beside a year, numbers a folder gives
// in words, a disc or a leading number, and an ebook beside the recording.
func TestDuplicatesReadPastNotesBesideTheReader(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name       string
		a, b       string // folder and narrator field, "folder|narrator"
		secA, secB int
		tracksB    int
	}{
		{"a narrator field's wrapper", "Dune|Narrator..........Jim Dale", "Dune - Copy|Stephen Fry", 33900, 32580, 1},
		{"a format beside the reader", "Dune [Scott Brick] [FLAC]|", "Dune [George Guidall] [FLAC]|", 72000, 72010, 1},
		{"a source beside the reader", "Dune [Scott Brick] (Chirp)|", "Dune [George Guidall] (Chirp)|", 72000, 72010, 1},
		{"a language beside the reader", "Dune [Scott Brick] [English]|", "Dune [George Guidall] [English]|", 72000, 72010, 1},
		{"a reader beside a year", "Dune [Scott Brick 2007]|", "Dune [George Guidall 1993]|", 72000, 72010, 1},
		{"a leading number", "Honor/01 Honor|", "Honor/02 Honor|", 30000, 30100, 1},
		{"a number alone", "Honor/01|", "Honor/02|", 30000, 30100, 1},
		{"discs", "The Stand CD1|", "The Stand CD2|", 30000, 30100, 1},
		{"discs spelled out", "The Stand - Disc 1|", "The Stand - Disc 2|", 30000, 30100, 1},
		{"books in words", "Honor - Book One|", "Honor - Book Two|", 30000, 30100, 1},
		{"an ebook beside the recording", "Dune|", "Dune (ebook)|", 75000, 0, 0},
	} {
		fa, na, _ := strings.Cut(c.a, "|")
		fb, nb, _ := strings.Cut(c.b, "|")
		groups, _, _ := dupShape(t, dupFixture(t, []string{
			shelfBook("a", "Frank Herbert/"+fa, "Dune", "Frank Herbert", na, c.secA, 1, ""),
			shelfBook("b", "Frank Herbert/"+fb, "Dune", "Frank Herbert", nb, c.secB, c.tracksB, ""),
		}, nil))
		if len(groups) != 0 {
			t.Errorf("%s: groups %v, want none", c.name, groups)
		}
	}
	// and the ones that stay one book
	for _, c := range []struct{ name, a, b string }{
		{"one reader, one wrapped", "Dune|Narrator..........Jim Dale", "Dune - Copy|Jim Dale"},
		{"one reader beside a format", "Dune [Scott Brick] [FLAC]|", "Dune [Scott Brick]|"},
		{"one reader beside a year", "Dune [Scott Brick 2007]|", "Dune - Copy|Scott Brick"},
		{"one number, in words and in digits", "Dune - Book One|", "Dune - Book 1|"},
	} {
		fa, na, _ := strings.Cut(c.a, "|")
		fb, nb, _ := strings.Cut(c.b, "|")
		groups, _, _ := dupShape(t, dupFixture(t, []string{
			shelfBook("a", "Frank Herbert/"+fa, "Dune", "Frank Herbert", na, 72000, 1, ""),
			shelfBook("b", "Frank Herbert/"+fb, "Dune", "Frank Herbert", nb, 72010, 1, ""),
		}, nil))
		if !slices.Equal(groups, []string{"a+b"}) {
			t.Errorf("%s: groups %v, want one", c.name, groups)
		}
	}
}

// shelfBook is a book as the listing returns it, at a folder of its own, with
// the fields the duplicate rules read.
func shelfBook(id, path, title, author, narrator string, seconds, tracks int, asin string) string {
	return fmt.Sprintf(`{"id":%q,"libraryId":%q,"mediaType":"book","relPath":%q,"media":{"metadata":{"title":%q,"authorName":%q,"narratorName":%q,"asin":%q},"duration":%d,"numTracks":%d,"numAudioFiles":%d}}`,
		id, libID, path, title, author, narrator, asin, seconds, tracks, tracks)
}

// dupFixture serves a listing and, for the batch reads, each book's audio
// files; files maps an id to its tracks' lengths.
func dupFixture(t *testing.T, listing []string, files map[string][]float64) map[string]any {
	t.Helper()

	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/libraries/"+libID+"/items", page(listing...))
	f.mux.HandleFunc("POST /api/items/batch/get", func(w http.ResponseWriter, r *http.Request) {
		var asked struct {
			IDs []string `json:"libraryItemIds"`
		}
		if err := json.NewDecoder(r.Body).Decode(&asked); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		out := make([]string, 0, len(asked.IDs))
		for _, id := range asked.IDs {
			tracks := make([]string, 0, len(files[id]))
			for i, l := range files[id] {
				tracks = append(tracks, fmt.Sprintf(`{"index":%d,"duration":%g,"metadata":{"filename":"%02d.mp3"}}`, i+1, l, i+1))
			}
			out = append(out, item(id, id, "", `"audioFiles":[`+strings.Join(tracks, ",")+`]`))
		}
		if _, err := fmt.Fprintf(w, `{"libraryItems":[%s]}`, strings.Join(out, ",")); err != nil {
			t.Error(err)
		}
	})
	out, err := toolCaller(t, f)("audit_duplicates", nil)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// dupShape is the groups, incomplete rows and candidate pairs of an answer,
// each as sorted ids.
func dupShape(t *testing.T, out map[string]any) (groups, incomplete, candidates []string) {
	t.Helper()

	ids := func(items []map[string]any, sep string) string {
		got := make([]string, 0, len(items))
		for _, it := range items {
			got = append(got, str(t, it["id"]))
		}
		if sep == "+" {
			slices.Sort(got)
		}
		return strings.Join(got, sep)
	}
	for _, g := range list(t, out["groups"]) {
		groups = append(groups, ids(list(t, g["items"]), "+"))
	}
	for _, r := range list(t, out["incomplete"]) {
		incomplete = append(incomplete, ids(list(t, r["items"]), " of "))
	}
	for _, c := range list(t, out["candidates"]) {
		candidates = append(candidates, ids(list(t, c["items"]), "+"))
	}
	slices.Sort(groups)
	slices.Sort(incomplete)
	slices.Sort(candidates)
	return groups, incomplete, candidates
}
