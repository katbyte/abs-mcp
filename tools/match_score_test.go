package tools

import (
	"slices"
	"strings"
	"testing"

	"github.com/katbyte/abs-mcp/sdk/abs"
)

// The comparator on the cases that motivated it: the Adams editions that
// match the book and not the recording, a subtitle on one side only, a
// narrator marked in the folder, and a candidate that is another book.
func TestScoreMatch(t *testing.T) {
	t.Parallel()

	book := func(title, author, narrator, relPath string, seconds float64) *abs.Item {
		it := &abs.Item{RelPath: relPath}
		it.Media.Metadata.Title, it.Media.Metadata.AuthorName, it.Media.Metadata.NarratorName = title, author, narrator
		it.Media.Duration = seconds
		return it
	}
	hit := func(title, author, narrator string, minutes float64) *abs.BookSearchResult {
		return &abs.BookSearchResult{Title: title, Author: author, Narrator: narrator, Duration: minutes}
	}
	for _, tc := range []struct {
		name string
		item *abs.Item
		hit  *abs.BookSearchResult
		want string
	}{
		{"same recording", book("Killingly", "Katharine Beutner", "Rachel Botchan", "", 12*3600+23*60), hit("Killingly", "Katharine Beutner", "Rachel Botchan", 12*60+23), confExact},
		{"same book, shorter recording", book("The Hitchhiker's Guide To The Galaxy", "Douglas Adams", "", "", 4*3600+56*60), hit("The Hitchhiker's Guide to the Galaxy", "Douglas Adams", "Stephen Fry", 5*60+51), confEdition},
		{"same length, other narrator in the folder", book("The Bat", "Jo Nesbø", "", "Jo Nesbø/Harry Hole - 01 - The Bat [John Lee]", 9*3600+39*60), hit("The Bat", "Jo Nesbo", "Robin Sachs", 9*60+39), confEdition},
		{"folder narrator agrees", book("The Bat", "Jo Nesbø", "", "Jo Nesbø/Harry Hole - 01 - The Bat [John Lee]", 9*3600+39*60), hit("The Bat", "Jo Nesbo", "John Lee", 9*60+39), confExact},
		{"subtitle on one side", book("Reckoners #2: Firefight", "Brandon Sanderson", "", "", 11*3600+37*60), hit("Firefight", "Brandon Sanderson", "MacLeod Andrews", 11*60+37), confLikely},
		{"unabridged tag", book("System Collapse (Unabridged)", "Martha Wells", "", "", 6*3600+36*60), hit("System Collapse", "Martha Wells", "Kevin R. Free", 6*60+36), confExact},
		{"duration unknown", book("Dune", "Frank Herbert", "", "", 0), hit("Dune", "Frank Herbert", "Scott Brick", 21*60), confLikely},
		{"other book", book("Dune", "Frank Herbert", "", "", 21*3600), hit("Dune Messiah", "Frank Herbert", "Scott Brick", 21*60), confUnsure},
		{"other author", book("Dune", "Frank Herbert", "", "", 21*3600), hit("Dune", "Someone Else", "", 21*60), confUnsure},
		{"initials in the author", book("Dune", "Frank Herbert", "", "", 21*3600), hit("Dune", "F. Herbert", "", 21*60), confExact},
	} {
		if got := scoreMatch(tc.item, tc.hit, 0); got.Confidence != tc.want {
			t.Errorf("%s: confidence = %s (%s), want %s", tc.name, got.Confidence, got.Reason, tc.want)
		}
	}

	// who reads it, each place the library says on its own: the cases held
	// back from the 2026-09-13 pass because the scorer could not see them
	inSeries := func(it *abs.Item, series string) *abs.Item {
		it.Media.Metadata.SeriesName = series
		return it
	}
	described := func(it *abs.Item, desc string) *abs.Item {
		it.Media.Metadata.Description = desc
		return it
	}
	for _, tc := range []struct {
		name string
		item *abs.Item
		hit  *abs.BookSearchResult
		want string
	}{
		// a second copy matched to the first copy's reading: the field
		// carries the wrong name, the folder still says otherwise
		{"field agrees, folder surname differs", book("Citizen of the Galaxy", "Robert A. Heinlein", "Lloyd James", "Robert A. Heinlein/Citizen of the Galaxy (Tipton)", 9*3600), hit("Citizen of the Galaxy", "Robert A. Heinlein", "Lloyd James", 9*60), confEdition},
		{"folder surname within the store's name", book("Citizen of the Galaxy", "Robert A. Heinlein", "", "Robert A. Heinlein/Citizen of the Galaxy (James)", 9*3600), hit("Citizen of the Galaxy", "Robert A. Heinlein", "Lloyd James", 9*60), confExact},
		{"folder surname, no field, other reader", book("Double Star", "Robert A. Heinlein", "", "Robert A. Heinlein/Double Star (James)", 7*3600), hit("Double Star", "Robert A. Heinlein", "Paul Michael Garcia", 7*60), confEdition},
		{"a credit line names another reader", described(book("The Eleventh Commandment", "Jeffrey Archer", "", "Jeffrey Archer/The Eleventh Commandment", 10*3600), "Read by Paul Heck."), hit("The Eleventh Commandment", "Jeffrey Archer", "Michael Brandon", 10*60), confEdition},
		{"a credit line names the store's reader", described(book("The Eleventh Commandment", "Jeffrey Archer", "", "Jeffrey Archer/The Eleventh Commandment", 10*3600), "<p>Narrated by Paul Heck</p>"), hit("The Eleventh Commandment", "Jeffrey Archer", "Paul Heck", 10*60), confExact},
		{"read by the author", described(book("On Writing", "Stephen King", "", "Stephen King/On Writing", 5*3600), "Read by the author."), hit("On Writing", "Stephen King", "Stephen King", 5*60), confExact},
		{"a real description naming a reader is no credit line", described(book("Dune", "Frank Herbert", "", "", 21*3600), "Set on the desert planet Arrakis, Dune is the story of Paul Atreides. "+strings.Repeat("A long description. ", 20)+"Read by someone in another edition."), hit("Dune", "Frank Herbert", "Scott Brick", 21*60), confExact},
		{"a title bracket saying who reads it", book("The Dead Zone (read by Lorelei King)", "Stephen King", "", "Stephen King/The Dead Zone", 16*3600), hit("The Dead Zone", "Stephen King", "James Franco", 16*60), confEdition},
		{"a title bracket naming a series is no reader", book("Pleasure Activism (Emergent Strategy)", "adrienne maree brown", "", "feminism/Pleasure Activism", 10*3600), hit("Pleasure Activism (Emergent Strategy)", "adrienne maree brown", "adrienne maree brown", 10*60), confExact},
		{"a folder part note is no reader", book("86: Run Through the Battlefront", "Asato", "Todd Haberkorn", "86/86—EIGHTY-SIX, Vol. 02 - Run Through the Battlefront (Start)", 6*3600), hit("86: Run Through the Battlefront", "Asato", "Todd Haberkorn", 6*60), confExact},
		{"a folder title fragment is no reader", book("Surrounded by Bad Bosses", "Thomas Erikson", "", "business/Surrounded by Bad Bosses (and Lazy Employees)", 8*3600), hit("Surrounded by Bad Bosses", "Thomas Erikson", "Tim Campbell", 8*60), confExact},
		// a production with a cast, and one reader's recording
		{"folder says full cast, the store's has one reader", book("Between Planets", "Robert A. Heinlein", "", "Robert A. Heinlein/Between Planets (Full Cast)", 6*3600), hit("Between Planets", "Robert A. Heinlein", "Paul Michael Garcia", 6*60), confEdition},
		{"folder says full cast, the store's is the production", book("Between Planets", "Robert A. Heinlein", "", "Robert A. Heinlein/Between Planets (Full Cast)", 6*3600), hit("Between Planets", "Robert A. Heinlein", "Full Cast", 6*60), confExact},
		{"folder says full cast, the store's lists the cast", book("Red Planet", "Robert A. Heinlein", "", "Robert A. Heinlein/Red Planet (Full Cast)", 6*3600), hit("Red Planet", "Robert A. Heinlein", "Bruce Coville, Jean Brassard, Lance Roger Axt", 6*60), confExact},
		{"field names a company's production", book("Network Effect", "Martha Wells", "Graphic Audio LLC.", "Martha Wells/The Murderbot Diaries - 5 - Network Effect", 12*3600+48*60), hit("Network Effect", "Martha Wells", "Kevin R. Free", 12*60+48), confEdition},
		// initials either side
		{"initials in the reader", book("The Bat", "Jo Nesbø", "J. Lee", "", 9*3600+39*60), hit("The Bat", "Jo Nesbo", "John Lee", 9*60+39), confExact},
		{"Last, First in the field", book("Stardust", "Neil Gaiman", "Gaiman, Neil", "", 6*3600), hit("Stardust", "Neil Gaiman", "Neil Gaiman", 6*60), confExact},
		// what an independent review found: an initial is a name, not a
		// wildcard, and a surname alone is a reader only in a folder
		{"a wrong initial in the field", book("The Bat", "Jo Nesbø", "J. Lee", "", 9*3600+39*60), hit("The Bat", "Jo Nesbo", "Christopher Lee", 9*60+39), confEdition},
		{"a wrong initial in the folder", book("The Bat", "Jo Nesbø", "", "Jo Nesbø/The Bat [J. Lee]", 9*3600+39*60), hit("The Bat", "Jo Nesbo", "Christopher Lee", 9*60+39), confEdition},
		{"a surname alone in the field", book("Stardust", "Neil Gaiman", "Fry", "", 6*3600), hit("Stardust", "Neil Gaiman", "Stephen Fry", 6*60), confEdition},
		{"a two-word surname in the folder", book("Tunnel in the Sky", "Robert A. Heinlein", "", "Robert A. Heinlein/Tunnel in the Sky (Van Horn)", 8*3600), hit("Tunnel in the Sky", "Robert A. Heinlein", "Kevin Van Horn", 8*60), confExact},
		{"a middle name on one side", book("Podkayne of Mars", "Robert A. Heinlein", "Emily Card", "", 5*3600), hit("Podkayne of Mars", "Robert A. Heinlein", "Emily Janice Card", 5*60), confExact},
		{"initials run together", book("Dune", "Frank Herbert", "J. K. Simmons", "", 21*3600), hit("Dune", "Frank Herbert", "JK Simmons", 21*60), confExact},
		{"initials in a folder bracket", book("Dune", "Frank Herbert", "", "Frank Herbert/Dune (A.J. Smith)", 21*3600), hit("Dune", "Frank Herbert", "Scott Brick", 21*60), confEdition},
		{"a reader in a folder above the book", book("Dune", "Frank Herbert", "", "Frank Herbert/Dune Chronicles [Scott Brick]/Dune", 21*3600), hit("Dune", "Frank Herbert", "Simon Vance", 21*60), confEdition},
		{"the same reader in a folder above", book("Dune", "Frank Herbert", "", "Frank Herbert/Dune Chronicles [Scott Brick]/Dune", 21*3600), hit("Dune", "Frank Herbert", "Scott Brick", 21*60), confExact},
		{"a series in the folder's brackets", inSeries(book("Leviathan Wakes", "James S. A. Corey", "", "James S. A. Corey/Leviathan Wakes (Expanse 1)", 20*3600), "The Expanse #1"), hit("Leviathan Wakes", "James S. A. Corey", "Jefferson Mays", 20*60), confExact},
		{"where the copy is from", book("Dune", "Frank Herbert", "", "Frank Herbert/Dune (UK)", 21*3600), hit("Dune", "Frank Herbert", "Simon Vance", 21*60), confExact},
		{"how the copy was made", book("Dune", "Frank Herbert", "", "Frank Herbert/Dune (Illustrated)", 21*3600), hit("Dune", "Frank Herbert", "Simon Vance", 21*60), confExact},
		{"a title before the name in a credit", described(book("Dune", "Frank Herbert", "", "", 21*3600), "Narrated by Dr. Jane Smith."), hit("Dune", "Frank Herbert", "Jane Smith", 21*60), confExact},
		{"an introduction after the reader", described(book("Dune", "Frank Herbert", "", "", 21*3600), "Read by Simon Vance with an introduction by the author."), hit("Dune", "Frank Herbert", "Simon Vance", 21*60), confExact},
		// and the second pass: the safe direction, but exact matches lost
		{"an article in a folder bracket", book("Educated", "Tara Westover", "", "Tara Westover/Educated (A Memoir)", 12*3600), hit("Educated", "Tara Westover", "Julia Whelan", 12*60), confExact},
		{"Last, First with a two-word surname", book("The Dispossessed", "Ursula K. Le Guin", "Le Guin, Ursula K.", "", 12*3600), hit("The Dispossessed", "Ursula K. Le Guin", "Ursula K. Le Guin", 12*60), confExact},
		{"two readers in the field, one the store's", book("Dune", "Frank Herbert", "Scott Brick, Simon Vance", "", 21*3600), hit("Dune", "Frank Herbert", "Simon Vance", 21*60), confExact},
		{"two readers in the field, neither the store's", book("Dune", "Frank Herbert", "Scott Brick, Simon Vance", "", 21*3600), hit("Dune", "Frank Herbert", "Euan Morton", 21*60), confEdition},
		{"a suffix the store leaves off", book("Dune", "Frank Herbert", "Robert Downey Jr.", "", 21*3600), hit("Dune", "Frank Herbert", "Robert Downey", 21*60), confExact},
		{"a genre folder above the book", book("Dune", "Frank Herbert", "", "[Sci-Fi]/Frank Herbert/Dune", 21*3600), hit("Dune", "Frank Herbert", "Simon Vance", 21*60), confExact},
		{"initials in a credit", described(book("Dune", "Frank Herbert", "", "", 21*3600), "Read by J. K. Simmons."), hit("Dune", "Frank Herbert", "J.K. Simmons", 21*60), confExact},
	} {
		if got := scoreMatch(tc.item, tc.hit, 0); got.Confidence != tc.want {
			t.Errorf("%s: confidence = %s (%s), want %s", tc.name, got.Confidence, got.Reason, tc.want)
		}
	}

	// the reason names where the disagreeing reader was said
	got := scoreMatch(book("Citizen of the Galaxy", "Robert A. Heinlein", "Lloyd James", "Robert A. Heinlein/Citizen of the Galaxy (Tipton)", 9*3600), hit("Citizen of the Galaxy", "Robert A. Heinlein", "Lloyd James", 9*60), 0)
	if got.Narrator != "differs" || !strings.Contains(got.Reason, "Tipton in the folder") {
		t.Errorf("reason = %q, want the folder's Tipton named", got.Reason)
	}

	// ranking puts the recording before the book
	it := book("The Hitchhiker's Guide To The Galaxy", "Douglas Adams", "", "", 4*3600+56*60)
	ranked := rankCandidates(it, []abs.BookSearchResult{
		*hit("The Hitchhiker's Guide to the Galaxy", "Douglas Adams", "Stephen Fry", 5*60+51),
		*hit("So Long, and Thanks for All the Fish", "Douglas Adams", "Martin Freeman", 4*60+39),
		*hit("The Hitchhiker's Guide to the Galaxy", "Douglas Adams", "Stephen Moore", 4*60+56),
	}, 0)
	if ranked[0].Result.Narrator != "Stephen Moore" || ranked[0].Score.Confidence != confExact || ranked[1].Score.Confidence != confEdition || ranked[2].Score.Confidence != confUnsure {
		t.Errorf("ranking = %v", []string{ranked[0].Score.Confidence, ranked[1].Score.Confidence, ranked[2].Score.Confidence})
	}

	for _, tc := range []struct{ in, want string }{
		{"The Hitchhiker's Guide To The Galaxy", "hitchhiker's guide to the galaxy"},
		{"System Collapse (Unabridged)", "system collapse"},
		{"Killingly: A Novel", "killingly a novel"},
	} {
		if got := cleanTitle(tc.in); got != strings.TrimPrefix(norm(tc.want), "the ") {
			t.Errorf("cleanTitle(%q) = %q", tc.in, got)
		}
	}
	if got := titleForms("Reckoners #2: Firefight"); !slices.Contains(got, "firefight") {
		t.Errorf("titleForms = %v, want the part after the colon", got)
	}
}
