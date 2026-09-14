package tools

import (
	"testing"

	"github.com/katbyte/abs-mcp/lib/abs"
)

// Every row is a folder seen on a real library on 2026-09-13, with what the
// metadata said. The first block was reported by the audit and was fine; the
// second block is what the audit is for.
func TestCheckPathStyles(t *testing.T) {
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

func TestFileStem(t *testing.T) {
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
		item("Terry Pratchett/Discworld - 26 - Thief of Time", "Thief of Time", false, "Theif of Time - 01.mp3", "Theif of Time - 02.mp3"),
	}
	for _, it := range bad {
		if _, flagged := checkFiles(it); !flagged {
			t.Errorf("%s: not flagged", it.RelPath)
		}
	}
}
