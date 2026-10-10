package tools

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/katbyte/abs-mcp/sdk/abs"
)

// Every row is a folder seen on a real library on 2026-09-13, with what the
// metadata said. The first block was reported by the audit and was fine; the
// second block is what the audit is for.
func TestCheckPathStyles(t *testing.T) {
	t.Parallel()

	type row struct {
		rel, title, author string
		series, genres     []string
	}
	fine := []row{
		{rel: "Warhammer 40k - The Horus Heresy/The Horus Heresy - 27 - Unremembered Empire", title: "The Unremembered Empire", author: "Dan Abnett", series: []string{"The Horus Heresy"}},
		{rel: "Warhammer 40k - The Beast Arises/The Beast Arises - 01 - I Am Slaughter", title: "I Am Slaughter: Warhammer 40,000", author: "Dan Abnett", series: []string{"The Beast Arises"}},
		{rel: "Jussi Adler-Olsen/Department Q - 02 - The Absent One", title: "The Absent One [Disc 1]", author: "Jussi Adler-Olsen"},
		{rel: "Jussi Adler-Olsen/Department Q - 01 - The Keeper of Lost Causes (Erik Davies) v2", title: "Jussi Adler-Olsen - The Keeper of Lost Causes", author: "Jussi Adler-Olsen"},
		{rel: "Robin Alexander/White Oak - 2 - The Magic of White Oak Lake", title: "The Magic of White Oak Lake: White Oak Series, Book 2", author: "Robin Alexander"},
		{rel: "Piers Anthony/Xanth - 08 - Crewel Lye", title: "Crewel Lye, A Caustic Yarn", author: "Piers Anthony"},
		{rel: "Isaac Asimov/R. Daneel Olivaw - 01 - Caves Of Steel", title: "The Caves of Steel Disc 1", author: "Isaac Asimov"},
		{rel: "Iain M. Banks/Cultre - 04- The State of the Art", title: "The State of the Art: Culture Series, Book 4", author: "Iain M. Banks"},
		{rel: "Iain M. Banks/Cultre - 02 -The Player of Games", title: "The Player of Games: Culture Series, Book 2", author: "Iain M. Banks"},
		{rel: "Remixed Classics/Remixed Classics - 06 - My Dear Henry (Kalynn Bayron)", title: "My Dear Henry: A Jekyll & Hyde Remix", author: "Kalynn Bayron"},
		{rel: "Travis Beacham/Impact Winter - 3", title: "Impact Winter Season 3", author: "Travis Beacham"},
		{rel: "Melissa Blair/The Halfling Saga - 2 - A Shadow Crown", title: "A Shadow Crown (Unabridged)", author: "Melissa Blair"},
		{rel: "Melissa Blair/The Halfling Saga - 1 - A Broken Blade", title: "01 A Broken Blade", author: "Melissa Blair"},
		{rel: "A Time Odyssey 3 - Firstborn", title: "Firstborn: A Time Odyssey, Book 3", author: "Arthur C. Clarke, Stephen Baxter"},
		{rel: "Matt Dinniman/Dungeon Crawler Carl - 03 - The Dungeon Anarchist's Cookbook", title: "Dungeon Crawler Carl, Book 3 - The Dungeon Anarchist's Cookbook", author: "Matt Dinniman"},
		{rel: "Warhammer 40k - The Horus Heresy/The Horus Heresy - 52 - Heralds of the Siege", title: "Heralds of the Siege (Anthology)", author: "John French, Guy Haley", series: []string{"The Horus Heresy"}},
		{rel: "Nicole Galland/D.O.D.O. - 02 - Master of the Revels", title: "Master of the Revels - A Return to Neal Stephenson's D.O.D.O.", author: "Nicole Galland"},
		{rel: "William Gibson/Bridge - 2 - Idoru", title: "Bridge, Book 2 - Idoru", author: "William Gibson"},
		{rel: "Spice and Wolf/Spice and Wolf, Vol. 08 - Town of Strife I", title: "Spice and Wolf, Vol. 08: The Town of Strife I", author: "Isuna Hasekura"},
		{rel: "Spice and Wolf/Spice and Wolf, Vol. 04", title: "Spice and Wolf, Vol. 4", author: "Isuna Hasekura"},
		{rel: "Millennium/Millennium - 04 - The Girl In The Spider's Web", title: "M4-The Girl in The Spiders Web", author: "Stieg Larsson"},
		{rel: "DragonLance/DragonLance Preludes 5 - Flint, The King", title: "Dragonlance Preludes II Volume 2 - Flint, the King", author: "Mary Kirchoff, Douglas Niles"},
		{rel: "Warhammer 40k - Siege of Terra/Siege of Terra, Book 8 - The End and the Death", title: "The End and the Death: Volume 1", author: "Dan Abnett", series: []string{"The Horus Heresy: Siege of Terra"}},
		{rel: "Warhammer 40k - Siege of Terra/Siege of Terra, Book 4 -  Saturnine", title: "Saturnine", author: "Dan Abnett"},
		{rel: "Foreworld Saga/Foreworld Saga - 02 - The Mongoliad Book Two", title: "The Mongoliad: The Foreworld Saga, Book 2", author: "Neal Stephenson, Greg Bear"},
		{rel: "Warhammer 40k - Dawn of Fire/Dawn of Fire - 2 - The Gate of Bones", title: "The Gate of Bones", author: "Andy Clark", series: []string{"Dawn of Fire: Warhammer 40,000"}},
		{rel: "DragonLance/DragonLance Chronicles 1 - Dragons of Autumn Twilight", title: "Dragons of Autumn Twilight", author: "Margaret Weis, Tracy Hickman"},
		{rel: "psychology/Radical Belonging", title: "Radical Belonging: How to Survive and Thrive in an Unjust World", author: "Lindo Bacon", genres: []string{"Psychology"}},
		{rel: "Personal/The Highly Sensitive Person", title: "The Highly Sensitive Person", author: "Elaine N. Aron", genres: []string{"Personal Development"}},
		{rel: "leadership/Rise", title: "Rise: 3 Practical Steps", author: "Patty Azzarello"},
		{rel: "Herbert, Frank/Dune (1965)", title: "Dune", author: "Frank Herbert"},
		{rel: "Arthur C. Clarke/2001", title: "2001: A Space Odyssey", author: "Arthur C. Clarke"},
		{rel: "Brandon Sanderson/Alcatraz - 04 - Alcatraz vs the Shattered Lens", title: "Alcatraz Versus the Shattered Lens", author: "Brandon Sanderson"},
		{rel: "Kim Stanley Robinson/2312 (2012)", title: "2312", author: "Kim Stanley Robinson"},
		{rel: "George Orwell/1984 - Original Adaptation", title: "George Orwell’s 1984", author: "George Orwell, Joe White"},
	}
	bad := []row{
		{rel: "Piers Anthony/Xanth - 17 - Harpy Time", title: "Harpy Thyme", author: "Piers Anthony"},
		{rel: "Jeffrey Archer/As the Crow Flies", title: "As the Crow Files", author: "Jeffrey Archer"},
		{rel: "Steven Gould/Jumper - 4 - Exo", title: "Jumper Series Bk 4", author: "Steven Gould"},
		{rel: "Stephen King/Dark Tower 5 - Wolves of the Calla", title: "3 - Prologue - Roont", author: "Stephen King"},
		{rel: "Molly J. Bragg/Heart of Heroes - 2 - Transistor", title: "Heart of Heroes, Book 2", author: "Molly J. Bragg"},
		{rel: "Stephen King/Thinner", title: "Thinner", author: "Richard Bachman"},
		{rel: "Philip K. Dick/The Saints of Salvation", title: "The Saints of Salvation: Salvation Sequence Series, Book 3", author: "Peter F. Hamilton", series: []string{"Salvation Sequence"}},
		{rel: "Kate Scelsa/In Bloom", title: "In Bloom", author: "Allie Keane"},
		{rel: "Someone Else/Unrelated Folder", title: "Dune", author: "Frank Herbert"},
		{rel: "Leadership/Rise", title: "Rise: 3 Practical Steps", author: "Patty Azzarello", genres: []string{"Business"}},
	}
	item := func(r row) *abs.Item {
		it := &abs.Item{MediaType: "book", RelPath: r.rel, Media: abs.Media{Metadata: abs.Metadata{Title: r.title, AuthorName: r.author, Genres: r.genres}}}
		for _, s := range r.series {
			it.Media.Metadata.Series = append(it.Media.Metadata.Series, abs.SeriesRef{Name: s, Sequence: "1"})
		}
		return it
	}
	for _, r := range fine {
		if detail, flagged := checkPath(item(r)); flagged {
			t.Errorf("%s: flagged: %s", r.rel, detail)
		}
	}
	for _, r := range bad {
		if _, flagged := checkPath(item(r)); !flagged {
			t.Errorf("%s: title %q by %q not flagged", r.rel, r.title, r.author)
		}
	}
}

// A folder's year is a first printing's or a recording's, and the book's year
// is never earlier than either; later is what a first printing's folder
// looks like.
func TestCheckPathYear(t *testing.T) {
	t.Parallel()

	type row struct{ rel, title, author, year string }
	fine := []row{
		{"Herbert, Frank/Dune (1965)", "Dune", "Frank Herbert", "2007"},
		{"Herbert, Frank/Dune (1965)", "Dune", "Frank Herbert", "1965"},
		{"Herbert, Frank/Dune (1965)", "Dune", "Frank Herbert", "2007-06-19"},
		{"Herbert, Frank/Dune (1965)", "Dune", "Frank Herbert", ""},
		{"Kim Stanley Robinson/2312 (2012)", "2312", "Kim Stanley Robinson", "2012"},
		{"George Orwell/1984 - Original Adaptation", "George Orwell’s 1984", "George Orwell", "1949"},
		{"Arthur C. Clarke/2001", "2001: A Space Odyssey", "Arthur C. Clarke", "1968"},
		{"Arthur C. Clarke/2001 - A Space Odyssey (1968)", "2001: A Space Odyssey", "Arthur C. Clarke", "1968"},
		{"Frank Herbert/Dune (1965) (2007 recording)", "Dune", "Frank Herbert", "1999"},
		{"Frank Herbert/Dune [Unabridged]", "Dune", "Frank Herbert", "1959"},
		{"Jussi Adler-Olsen/Department Q - 01 - The Keeper of Lost Causes (Erik Davies) v2", "The Keeper of Lost Causes", "Jussi Adler-Olsen", "2011"},
		{"Someone Else/The Year 2000 Problem (1999)", "The Year 2000 Problem", "Someone Else", "1999"},
	}
	bad := []row{
		{"Herbert, Frank/Dune (1965)", "Dune", "Frank Herbert", "1959"},
		{"Frank Herbert/Dune [Unabridged, 2007]", "Dune", "Frank Herbert", "1978"},
		{"Frank Herbert/2007 - Dune", "Dune", "Frank Herbert", "1978"},
		{"Frank Herbert/Dune - 2007", "Dune", "Frank Herbert", "1978-01-01"},
	}
	item := func(r row) *abs.Item {
		it := &abs.Item{MediaType: "book", RelPath: r.rel}
		it.Media.Metadata.Title, it.Media.Metadata.AuthorName = r.title, r.author
		it.Media.Metadata.PublishedYear = abs.FlexString(r.year)
		return it
	}
	for _, r := range fine {
		if detail, flagged := checkPath(item(r)); flagged {
			t.Errorf("%s dated %q: flagged: %s", r.rel, r.year, detail)
		}
	}
	for _, r := range bad {
		detail, flagged := checkPath(item(r))
		if !flagged {
			t.Errorf("%s dated %q: not flagged", r.rel, r.year)
			continue
		}
		if !strings.Contains(detail, "earlier than the folder allows") {
			t.Errorf("%s dated %q: flagged for something else: %s", r.rel, r.year, detail)
		}
	}
}

// A book the record places in a series is in a path that says so, the book
// the series is named after included. Most rows are folders seen on a real
// library on 2026-10-09 with the series its records gave, written the way a
// listing writes them.
func TestCheckPathSeries(t *testing.T) {
	t.Parallel()

	type row struct{ rel, title, author, series string }
	fine := []row{
		// a place between dashes, whatever the series is called there
		{"Peter F. Hamilton/Salvation Sequence - 03 - The Saints of Salvation", "The Saints of Salvation", "Peter F. Hamilton", "The Salvation Sequence #3"},
		{"Peter F. Hamilton/Void Trilogy - 02 - The Temporal Void", "The Temporal Void", "Peter F. Hamilton", "Void Trilogy #2"},
		{"Britney Jackson/Lesbians, Pirates, and Dragons - 01 - Pirates of Aletharia", "Pirates of Aletharia", "Britney Jackson", "Lesbians, Pirates, and Dragons #1"},
		// a place closing the series' short name
		{"Terry Pratchett/Bromeliad 1 - Truckers", "Truckers", "Terry Pratchett", "The Bromeliad Trilogy #1"},
		{"Aliette de Bodard/Dominion of Fallen 2 - The House of Binding Thorns", "The House of Binding Thorns", "Aliette de Bodard", "Dominion of the Fallen #2"},
		{"Neal Stephenson/Baroque 2 - The Confusion (V)", "The Confusion", "Neal Stephenson", "The Baroque Cycle #4"},
		{"Orson Scott Card/Laddertop 2", "Laddertop 2", "Orson Scott Card", "The Laddertop Series #2"},
		// the series' name and a number, in brackets
		{"Terry Pratchett/Sourcery (Discworld 5)", "Sourcery", "Terry Pratchett", "Discworld #5"},
		// a folder above the book that is the series'
		{"Konosuba/Konosuba, Vol. 01 - Oh! My Useless Goddess!", "Konosuba: God's Blessing on This Wonderful World!, Vol. 01: Oh! My Useless Goddess!", "Natsume Akatsuki", "Konosuba: God's Blessing on This Wonderful World! #1"},
		{"Terry Pratchett/Discworld/Mort", "Mort", "Terry Pratchett", "Discworld #4"},
		{"Terry Pratchett/The Discworld/Mort", "Mort", "Terry Pratchett", "Discworld #4"},
		{"Larry Niven/Ringworld - 01 - Ringworld", "Ringworld", "Larry Niven", "Ringworld #1"},
		// no place on the record: a collection, not a shelf order
		{"Jane Austen/Pride and Prejudice", "Pride and Prejudice", "Jane Austen", "Jane Austen's Novels"},
		{"Jane Austen/Pride and Prejudice", "Pride and Prejudice", "Jane Austen", ""},
	}
	bad := []row{
		{"Peter F. Hamilton/Salvation Lost", "Salvation Lost", "Peter F. Hamilton", "The Salvation Sequence #2"},
		{"Peter F. Hamilton/Salvation", "Salvation", "Peter F. Hamilton", "The Salvation Sequence #1"},
		{"Peter F. Hamilton/The Dreaming Void", "The Dreaming Void", "Peter F. Hamilton", "Void Trilogy #1"},
		{"Margaret Atwood/MaddAddam", "MaddAddam", "Margaret Atwood", "The MaddAddam Trilogy #3"},
		// the book a series is named after does not sort beside the rest of it
		{"Alastair Reynolds/Revelation Space", "Revelation Space", "Alastair Reynolds", "Revelation Space #1"},
		{"Stephen King/The Shining", "The Shining", "Stephen King", "The Shining #1"},
		{"Your Name", "your name.", "Makoto Shinkai", "your name. #01"},
		// the series' name in the title is not a place in it
		{"J. K. Rowling/Harry Potter and the Deathly Hallows", "Harry Potter and the Deathly Hallows", "J.K. Rowling", "Harry Potter (Narrated by Stephen Fry) #7"},
		// a number that is the title is not one either
		{"Arthur C. Clarke/2001", "2001: A Space Odyssey", "Arthur C. Clarke", "Space Odyssey Series #1"},
		// nor is a disc
		{"Isaac Asimov/Foundation and Empire Disc 1", "Foundation and Empire", "Isaac Asimov", "Foundation #2"},
		// a shelf is not a series' folder
		{"feminism/Pleasure Activism", "Pleasure Activism", "adrienne maree brown", "Emergent Strategy #2"},
		// nor is the author's, though the series carries the author's name
		{"Terry Pratchett/Mort", "Mort", "Terry Pratchett", "Terry Pratchett Discworld #4"},
		// every series the record places it in is named, a comma in a name kept
		{"Orson Scott Card/Children of the Mind (Unabridged)", "Children of the Mind", "Orson Scott Card", "The Ender Saga #4, The Enderverse #14"},
		{"Britney Jackson/Pirates of Aletharia", "Pirates of Aletharia", "Britney Jackson", "Lesbians, Pirates, and Dragons #1"},
	}
	item := func(r row) *abs.Item {
		return &abs.Item{MediaType: "book", RelPath: r.rel, Media: abs.Media{Metadata: abs.Metadata{Title: r.title, AuthorName: r.author, SeriesName: r.series}}}
	}
	for _, r := range fine {
		if detail, flagged := checkPath(item(r)); flagged {
			t.Errorf("%s in %q: flagged: %s", r.rel, r.series, detail)
		}
	}
	for _, r := range bad {
		detail, flagged := checkPath(item(r))
		if !flagged {
			t.Errorf("%s in %q: not flagged", r.rel, r.series)
			continue
		}
		for _, s := range placedSeries(item(r).Media.Metadata) {
			if !strings.Contains(detail, "does not say it is") || !strings.Contains(detail, `"`+s+`"`) {
				t.Errorf("%s in %q: flagged without %q: %s", r.rel, r.series, s, detail)
			}
		}
	}

	// one item read whole carries its series apart, and is judged the same
	whole := item(bad[0])
	whole.Media.Metadata.SeriesName = ""
	whole.Media.Metadata.Series = abs.SeriesRefs{{Name: "The Salvation Sequence", Sequence: "2"}, {Name: "Tantor Collection"}}
	if detail, flagged := checkPath(whole); !flagged || !strings.HasSuffix(detail, `does not say it is "The Salvation Sequence #2"`) {
		t.Errorf("read whole: %v %q, want the one series it has a place in", flagged, detail)
	}
}

// A server told to leave the series rule out reports none of it, and still
// reports what is a mistake under any way of filing.
func TestPathRulesLeaveSeriesOut(t *testing.T) {
	t.Parallel()

	off := pathRules{}
	book := func(rel, title, year string) *abs.Item {
		it := &abs.Item{MediaType: "book", RelPath: rel, Media: abs.Media{Metadata: abs.Metadata{Title: title, AuthorName: "Peter F. Hamilton", SeriesName: "The Salvation Sequence #2"}}}
		it.Media.Metadata.PublishedYear = abs.FlexString(year)
		return it
	}
	lost := book("Peter F. Hamilton/Salvation Lost", "Salvation Lost", "")
	if _, flagged := checkPath(lost); !flagged {
		t.Fatal("with every rule on, the folder that does not say its series is not flagged")
	}
	if detail, flagged := off.check(lost); flagged {
		t.Errorf("with the series rule left out: %s", detail)
	}
	for what, it := range map[string]*abs.Item{
		"another title":   book("Peter F. Hamilton/Salvation Lost", "Pandora's Star", ""),
		"another author":  book("Philip K. Dick/Salvation Lost", "Salvation Lost", ""),
		"an earlier year": book("Peter F. Hamilton/Salvation Lost (2019)", "Salvation Lost", "2001"),
	} {
		if detail, flagged := off.check(it); !flagged || strings.Contains(detail, "does not say it is") {
			t.Errorf("%s with the series rule left out: %v %q, want it flagged for itself", what, flagged, detail)
		}
	}
}

// The setting reaches both tools that run the check, and audit_path's own
// account of itself.
func TestAuditPathSeriesRuleIsTheServers(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/libraries/"+libID+"/items", page(
		shelved("b1", "Peter F. Hamilton/Salvation Lost", "Salvation Lost", "Peter F. Hamilton", 3600, `"seriesName":"The Salvation Sequence #2"`, ""),
		shelved("b2", "Peter F. Hamilton/Salvation Sequence - 03 - The Saints of Salvation", "The Saints of Salvation", "Peter F. Hamilton", 3600, `"seriesName":"The Salvation Sequence #3"`, ""),
		shelved("b3", "Larry Niven/Ringworld", "Ringworld", "Larry Niven", 3600, `"seriesName":"Ringworld #1"`, ""),
	))
	f.json("GET /api/libraries/"+libID+"/series", `{"results":[],"total":0}`)
	f.json("GET /api/libraries/"+libID+"/authors", `{"results":[],"total":0}`)

	pathCount := func(all map[string]any) int {
		for _, row := range list(t, all["audits"]) {
			if str(t, row["audit"]) == "audit_path" {
				return num(t, row["found"])
			}
		}
		return 0
	}
	for _, tc := range []struct {
		opts Options
		want map[string]string
	}{
		{Options{}, map[string]string{
			"b1": `folder "Salvation Lost" does not say it is "The Salvation Sequence #2"`,
			"b3": `folder "Ringworld" does not say it is "Ringworld #1"`,
		}},
		{Options{AuditSkip: []string{" Path-Series "}}, map[string]string{}},
	} {
		call := callerWith(t, f, tc.opts)
		out, err := call("audit_path", nil)
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]string{}
		for _, row := range list(t, out["findings"]) {
			got[str(t, row["id"])] = str(t, row["detail"])
		}
		if !maps.Equal(got, tc.want) || num(t, out["total_findings"]) != len(tc.want) {
			t.Errorf("skipping %q: audit_path = %v, want %v", tc.opts.AuditSkip, got, tc.want)
		}
		all, err := call("audit_all", nil)
		if err != nil {
			t.Fatal(err)
		}
		if n := pathCount(all); n != len(tc.want) {
			t.Errorf("skipping %q: audit_all counts %d for audit_path, want %d", tc.opts.AuditSkip, n, len(tc.want))
		}

		r := &registry{opts: tc.opts}
		r.opts.AuditSkip, _ = auditSkips(tc.opts.AuditSkip)
		queueTools(r)
		for _, p := range queuedTools(t, r) {
			if p.Name != "audit_path" {
				continue
			}
			if says := strings.Contains(p.Description, "--audit-skip "+rulePathSeries); says != (len(tc.want) > 0) {
				t.Errorf("skipping %q: audit_path's description says the series rule: %v", tc.opts.AuditSkip, says)
			}
		}
	}
}

// A listing writes a book's series on one line, and only the ones with a
// place in them are wanted.
func TestPlacedSeries(t *testing.T) {
	t.Parallel()

	for line, want := range map[string][]string{
		"":                                      nil,
		"Jane Austen's Novels":                  nil,
		"Discworld #17":                         {"Discworld #17"},
		"Discworld #17, Penguin Classics":       {"Discworld #17"},
		"The Ender Saga #4, The Enderverse #14": {"The Ender Saga #4", "The Enderverse #14"},
		"Lesbians, Pirates, and Dragons #1":     {"Lesbians, Pirates, and Dragons #1"},
		// a name with no place cannot be told from the start of the next
		"The Cosmere, The Mistborn Saga #1": {"The Cosmere, The Mistborn Saga #1"},
	} {
		if got := placedSeries(abs.Metadata{SeriesName: line}); !slices.Equal(got, want) {
			t.Errorf("placedSeries(%q) = %q, want %q", line, got, want)
		}
	}
}

func TestFileStem(t *testing.T) {
	t.Parallel()

	cases := []struct {
		names []string
		want  string
	}{
		{[]string{"Dune - 01", "Dune - 02"}, "dune"},
		{[]string{"01 - Arrakis", "02 - Caladan"}, ""},
		{[]string{"Frank Herbert - Dune - Chapter 01", "Frank Herbert - Dune - Chapter 02"}, "frank herbert dune"},
		{[]string{"01 Dune Messiah", "02 Dune Messiah"}, "dune messiah"},
		{[]string{"Part 1 of 20", "Part 2 of 20"}, ""},
		{[]string{"Neuromancer"}, "neuromancer"},
		{[]string{"Neuromancer (Unabridged) - 1", "Neuromancer (Unabridged) - 2"}, "neuromancer"},
		{[]string{"Dune", "Dune"}, "dune"},
		{nil, ""},
	}
	for _, c := range cases {
		if got := fileStem(c.names); got != c.want {
			t.Errorf("fileStem(%q) = %q, want %q", c.names, got, c.want)
		}
	}
}

func TestCheckFiles(t *testing.T) {
	t.Parallel()

	item := func(rel, title string, isFile bool, files ...string) *abs.Item {
		it := &abs.Item{MediaType: "book", RelPath: rel, IsFile: isFile, Media: abs.Media{Metadata: abs.Metadata{Title: title, AuthorName: "Frank Herbert"}}}
		for _, f := range files {
			var af abs.AudioFile
			af.Metadata.Filename = f
			it.Media.AudioFiles = append(it.Media.AudioFiles, af)
		}
		return it
	}
	fine := []*abs.Item{
		item("Frank Herbert/Dune", "Dune", false, "Dune - 01.mp3", "Dune - 02.mp3"),
		item("Frank Herbert/Dune", "Dune", false, "01 - Arrakis.mp3", "02 - Caladan.mp3"),
		item("Frank Herbert/Dune", "Dune", false, "Frank Herbert - 01.mp3", "Frank Herbert - 02.mp3"),
		item("Frank Herbert/Dune (1965)", "Dune: Book One", false, "Dune (1965) - 01.mp3", "Dune (1965) - 02.mp3"),
		item("Frank Herbert/Dune.m4b", "Dune", true, "Dune.m4b"),
		item("Frank Herbert/Dune", "Dune", false),
		item("Horus Heresy/The Horus Heresy - 48 - The Burden of Loyalty", "The Burden of Loyalty", false, "The Burden of Loyalty - 01.mp3", "The Burden of Loyalty - 02.mp3"),
		item("Horus Heresy/The Horus Heresy - 22  Shadows of Treachery.m4b", "Shadows of Treachery", true, "The Horus Heresy - 22  Shadows of Treachery.m4b"),
		item("Spice and Wolf/Spice and Wolf, Vol. 08 - Town of Strife I", "Spice and Wolf, Vol. 08: The Town of Strife I", true, "Spice and Wolf, Vol. 08 [PZG].m4b"),
		item("Robert A. Heinlein/The Long Watch (Oliver).mp3", "The Long Watch", true, "Robert A. Heinlein  - The Long Watch (Oliver).mp3"),
		item("Robert Jordan/The Wheel of Time - 12 - The Gathering Storm", "The Gathering Storm (The Wheel of Time #12)", false, "The Wheel of Time - 12 - The Gathering Storm - 01.mp3", "The Wheel of Time - 12 - The Gathering Storm - 02.mp3"),
	}
	for _, it := range fine {
		if detail, flagged := checkFiles(it); flagged {
			t.Errorf("%s: flagged: %s", it.RelPath, detail)
		}
	}
	bad := []*abs.Item{
		item("Frank Herbert/Dune", "Dune", false, "Neuromancer - 01.mp3", "Neuromancer - 02.mp3"),
		item("Frank Herbert/Neuromancer.m4b", "Dune", true, "Neuromancer.m4b"),
		item("Terry Pratchett/Discworld - 26 - Thief of Time", "Thief of Time", false, "Theif of Time - 01.mp3", "Theif of Time - 02.mp3"), //nolint:misspell // the misspelt filename is the case under test
	}
	for _, it := range bad {
		if _, flagged := checkFiles(it); !flagged {
			t.Errorf("%s: not flagged", it.RelPath)
		}
	}
}

// A book wrongly matched to "It" passed in a folder called "The Institute":
// the letters are there, the word is not.
func TestPathAgreesWordForWord(t *testing.T) {
	t.Parallel()

	it := &abs.Item{MediaType: "book", RelPath: "Stephen King/The Institute"}
	it.Media.Metadata.Title, it.Media.Metadata.AuthorName = "It", "Stephen King"
	if _, suspect := checkPath(it); !suspect {
		t.Error("It in The Institute passed")
	}
	it.RelPath = "Stephen King/It (Unabridged)"
	if detail, suspect := checkPath(it); suspect {
		t.Errorf("It in its own folder: %s", detail)
	}
}
