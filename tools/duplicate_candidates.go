package tools

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/katbyte/abs-mcp/lib/abs"
)

// Duplicate candidates: pairs audit_duplicates' keys do not join that are
// probably one recording, found in the zbooks sort (2026-09-24). The keys
// need an asin, an isbn or the same title and author, and a copy scanned in
// beside another rarely has them: the 20th anniversary copy of Ender's Game
// carries its edition in the title, The Saints of Salvation was filed under
// Philip K. Dick, A Planet Called Treason is Treason under the title of its
// later edition with "Treason" left in its album tag. Each is only a lead:
// item_compare_audio says whether the two are one recording.
//
// Other readings of one book are what must stay out, and the evidence is
// the narrator (the field, or the name in brackets on a folder, "Red Prophet
// (Polk)") and the length: two readings of a novel differ by 5-50%, one
// recording re-encoded or split by under 1%, and a copy with an anniversary's
// extra material by 6%.
//
// Two books of one series must stay out too, and so must the groups' title
// joins of them. "Mushoku Tensei: Jobless Reincarnation, Vol. 18" and "Vol.
// 17" are one title once the subtitle goes, and their lengths agree as often
// as not: 76 of the first 78 candidates on the real library were volumes of
// light-novel series. Nine of Radclyffe's Honor books all carried the title
// "Honor" from their album tag. Three things tell two books apart
// (bookConflict): a number that is part of the name ("Vol. 17", "Part 1
// Volume 2", "CD1", "Book One"), read from the title and the folder alike;
// a different place in one series (#1 and #2, "01 Honor" and "02 Honor"),
// whatever the folders say; and, for the title joins, folders that name
// different books ("Honor - 01 - Above All", "Honor Bound"). One recording
// filed as Discworld 4 and as Death 1, or numbered in two orders (Narnia), is
// kept apart as well, and shown in split: a false group costs more than a
// false split.
//
// The author's folder counts as the author: an unmatched copy's author
// comes from its tags, and "Enders Game 1" was the author tag of an Ender's
// Game filed under Orson Scott Card beside its anniversary copy. Only a
// folder that names one of the two authors counts, not a genre shelf.

const (
	// lengths within this share of each other, the same title and author
	// (Protector 2.5%, Saints of Salvation 3%)
	candidateSameLength = 0.04
	// with an edition named in the title or folder, which can add material
	// (Ender's Game, 20th anniversary: 6%)
	candidateEditionLength = 0.10
	// an album tag naming the other's title is a weaker clue than the title
	// itself, so the lengths must agree closely (Treason: 0.7%)
	candidateAlbumLength = 0.01
)

var (
	// a parenthetical or bracketed part of a title or folder
	candidateBrackets = regexp.MustCompile(`[(\[]([^)\]]*)[)\]]`)
	// what names an edition or a format rather than a reader
	candidateLabel = regexp.MustCompile(`(?i)\b(anniversary|full[- ]?cast|cast|dramati[sz](ed|ation)|unabridged|abridged|remaster(ed)?|single[- ]?file|one[- ]file|files?|edition|version|bbc|radio|graphic ?audio|audio ?drama|drama|extended|uncut|complete|retail|mp3|m4b|m4a|aac|aax|aaxc|flac|alac|opus|ogg|wav|wma|epub|pdf|vbr|cbr|kbps)\b`)
	// what names neither a reader nor an edition: "(A Novel)", "Various",
	// a note on the copy itself, "(copy)", "(old rip)", and where it came
	// from, "(Audible)", "(Libation)", "(Libby)"
	candidateNoName = regexp.MustCompile(`(?i)\b(novel|book|books|series|saga|trilogy|volume|vol|part|collection|stories|omnibus|various|multiple|unknown|anonymous|copy|rip|ripped|old|new|backup|alt|alternate|duplicate|dupe|audible|audiobooks?|libation|libby|overdrive|itunes|chirp|kobo|storytel|downpour|spotify|bookbeat|nextory|librofm|hoopla|chaptered|explicit|proper|hq|nlb|na|english|german|deutsch|french|francais|spanish|espanol|italian|dutch|swedish|polish|russian|japanese|chinese|korean)\b`)
	// a note on the copy at the end of a folder name, "Dune - Copy", "Dune -
	// old rip": not the book it names
	bookCopyNote = regexp.MustCompile(`(?i)^\s*((old|new|alt|alternate|backup|duplicate|dupe)\s*)*(copy|rip|ripped|backup|duplicate|dupe)\s*(\d+)?\s*$`)
	// braces, which the server reads a narrator from, "The Hobbit {Andy
	// Serkis}", name no book
	bookBraces = regexp.MustCompile(`\s*\{[^}]*\}`)
)

// candidateRoles are words in an author or narrator field that are not part
// of the name: "Ken Liu - Translator Baoshu".
var candidateRoles = map[string]bool{
	"translator": true, "translated": true, "narrator": true, "narrated": true, "editor": true, "edited": true,
	"foreword": true, "introduction": true, "read": true, "by": true, "and": true, "with": true,
}

// dupCandidate is two copies that are probably one recording.
type dupCandidate struct {
	Why   string        `json:"why"   jsonschema:"what joins them; confirm with item_compare_audio"`
	Items []itemSummary `json:"items" jsonschema:"the two copies"`
}

// candidateFacts is what the rules read of one item.
type candidateFacts struct {
	title     string     // the title as it names the book: no brackets, no subtitle
	core      string     // title reduced to comparable words, articles and punctuation gone
	author    []string   // the author's name words
	narrators [][]string // each narrator's name words, from the field and the folder's brackets
	bracketed [][]string // the narrators named only in brackets, on the title or the folder
	bracketAs []string   // each of those as the brackets write it
	fieldAs   []string   // the narrators the field names, as it writes them
	labels    []string   // editions and formats named in brackets
	parent    string     // the path the book's folder sits in, "" at the library's root
	names     []string   // the book its folder names, as comparable words; none when the folder says nothing
	numbers   map[string]string
	positions map[string]string
}

func candidateFactsOf(s *itemSummary) candidateFacts {
	f := candidateFacts{title: candidateTitle(s.Title), author: candidateWords(s.Author)}
	f.core = candidateCore(s.Title)
	rel := strings.Trim(s.Path, "/")
	base := path.Base(rel)
	if audioExt[strings.ToLower(path.Ext(base))] {
		base = strings.TrimSuffix(base, path.Ext(base)) // a book that is one file
	}
	if dir := path.Dir(rel); dir != "." && dir != "/" {
		f.parent = dir
	}
	if rel != "" {
		f.names = bookNames(base, f.author)
	}
	f.numbers = nameNumbers(s.Title, base)
	f.positions = seriesPositions(s, base)
	for _, name := range readerNames(s.Narrator) {
		if words := candidateWords(name); len(words) > 0 && !candidateLabel.MatchString(name) && !candidateNoName.MatchString(name) {
			f.narrators = append(f.narrators, words)
			f.fieldAs = append(f.fieldAs, strings.TrimSpace(name))
		}
	}
	folder := s.Path
	if i := strings.LastIndex(folder, "/"); i >= 0 {
		folder = folder[i+1:]
	}
	br := bracketsOf(s.Title, folder, f.author)
	f.labels = br.labels
	f.narrators = append(f.narrators, br.readers...)
	f.bracketed, f.bracketAs = br.readers, br.as
	return f
}

// brackets is what the brackets on a title and a book folder say: the
// readers they name, each as name words and as written, and the editions
// and formats they name.
type brackets struct {
	readers [][]string
	as      []string
	labels  []string
}

// bracketsOf reads the brackets on a title and a book folder. A bracket that
// is not a year, an edition, a format, a source, a language, a note on the
// copy or the author is its reader in a collector's library ("(Weiner)",
// "[John Lee]").
func bracketsOf(title, folder string, author []string) brackets {
	var b brackets
	seen := map[string]bool{}
	for _, m := range slices.Concat(candidateBrackets.FindAllStringSubmatch(title, -1), candidateBrackets.FindAllStringSubmatch(folder, -1)) {
		for part := range strings.SplitSeq(m[1], ",") {
			part = strings.TrimSpace(part)
			words := candidateWords(part)
			switch {
			case len(words) == 0 || seen[part]:
			case candidateLabel.MatchString(part):
				b.labels = append(b.labels, part)
			// a year, a book number: neither a reader nor an edition, but a
			// reader beside one is still a reader, "Scott Brick 2007"
			case strings.ContainsAny(part, "0123456789") && len(candidateWords(withoutNumbers(part))) == 0:
			case candidateNoName.MatchString(part):
			// the author's own name disambiguates the author, "(Baoshu)",
			// as often as it says the author reads it
			case candidateSubset(words, author):
			case len(words) <= 4:
				if strings.ContainsAny(part, "0123456789") {
					part = withoutNumbers(part)
					words = candidateWords(part)
				}
				b.readers = append(b.readers, words)
				b.as = append(b.as, part)
			}
			seen[part] = true
		}
	}
	return b
}

// candidateTitle is a title as it names the book: brackets, disc and part
// markers, a "Series - 01 - " in front and a subtitle set aside.
func candidateTitle(title string) string {
	t := pathStrip(title)
	if m := folderMarker.FindStringIndex(t); len(m) == 2 && m[1] < len(t) {
		t = t[m[1]:]
	}
	if i := strings.Index(t, ":"); i > 0 {
		t = t[:i]
	}
	return strings.TrimSpace(t)
}

// candidateCore reduces a title to the words compared: "Ender's Game (20th
// Anniversary full cast)" and "Ender's Game" are both "enders game", "The
// Redemption of Time: The Three-Body Problem, Book 4" is "redemption of
// time". A leading article goes too: folders drop it as often as not.
func candidateCore(title string) string {
	core := pathWords(candidateTitle(title))
	for _, article := range []string{"the ", "a ", "an "} {
		if rest, ok := strings.CutPrefix(core, article); ok && rest != "" {
			return rest
		}
	}
	return core
}

// candidateWords is the name words of an author or narrator: lower-case, no
// punctuation, no initials or role words.
func candidateWords(name string) []string {
	var out []string
	for w := range strings.FieldsSeq(norm(name)) {
		if len(w) > 1 && !candidateRoles[w] {
			out = append(out, w)
		}
	}
	return out
}

// candidateSubset reports whether every word of part is in whole, and part
// has some: "O'Brien" is Connor O'Brien, and Baoshu is "Ken Liu - Translator
// Baoshu".
func candidateSubset(part, whole []string) bool {
	if len(part) == 0 {
		return false
	}
	for _, w := range part {
		if !slices.Contains(whole, w) {
			return false
		}
	}
	return true
}

// sameAuthor reports whether one author's name is within the other's.
func (f *candidateFacts) sameAuthor(o *candidateFacts) bool {
	return candidateSubset(f.author, o.author) || candidateSubset(o.author, f.author)
}

// sameShelf reports whether both are filed in one folder that names the
// author of one of them: "Orson Scott Card/", not "Fiction/" or "psychology/".
func (f *candidateFacts) sameShelf(o *candidateFacts) bool {
	if f.parent == "" || f.parent != o.parent {
		return false
	}
	for seg := range strings.SplitSeq(f.parent, "/") {
		words := candidateWords(seg)
		if (len(f.author) > 0 && candidateSubset(f.author, words)) || (len(o.author) > 0 && candidateSubset(o.author, words)) {
			return true
		}
	}
	return false
}

// audioExt are the extensions of a book that is one file rather than a
// folder, set aside to read its name.
var audioExt = map[string]bool{".m4b": true, ".m4a": true, ".mp3": true, ".mp4": true, ".aac": true, ".flac": true, ".ogg": true, ".opus": true, ".wma": true, ".wav": true}

// bookNames is the book a folder names, as comparable words: the title after
// "Series - 03 - " or "03 - ", or the whole name and its last worded segment
// ("Frank Herbert - Dune" names Dune). Not the first segment: that is the
// series as often as the title, "Mushoku Tensei - Jobless Reincarnation,
// Vol. 17". A leading article, braces, a note on the copy ("Dune - Copy")
// and "&" for "and" are set aside, so "The Hobbit {Andy Serkis}" and
// "Hobbit" name one book. Brackets, disc markers and a lone number say
// nothing.
func bookNames(folder string, author []string) []string {
	folder = strings.ReplaceAll(bookBraces.ReplaceAllString(folder, ""), "&", " and ")
	s := pathStrip(folder)
	var names []string
	add := func(v string) {
		v = pathWords(v)
		for _, article := range []string{"the ", "a ", "an "} {
			if rest, ok := strings.CutPrefix(v, article); ok && rest != "" {
				v = rest
				break
			}
		}
		if v == "" || slices.Contains(names, v) || (!strings.ContainsFunc(v, unicode.IsLetter) && !pathYearLike.MatchString(v)) {
			return
		}
		names = append(names, v)
	}
	if m := folderMarker.FindStringIndex(s); len(m) == 2 && m[1] < len(s) {
		add(s[m[1]:])
		return names
	}
	if m := folderLead.FindStringIndex(folder); len(m) == 2 && m[1] < len(folder) {
		add(pathStrip(folder[m[1]:]))
		return names
	}
	// the segments, a note on the copy left off the end
	segs := pathSegment.Split(s, -1)
	for len(segs) > 1 && strings.TrimSpace(segs[len(segs)-1]) != "" && bookCopyNote.MatchString(segs[len(segs)-1]) {
		segs = segs[:len(segs)-1]
	}
	add(strings.Join(segs, " - "))
	// the last worded segment that names neither the author nor anything
	// but the copy: "Above All - Radclyffe" names Above All, and
	// "Honor Bound - Unabridged" Honor Bound
	for _, seg := range slices.Backward(segs[1:]) {
		// a number alone says which book: "Honor - 1" is not "Honor"
		if strings.ContainsFunc(seg, unicode.IsDigit) && !strings.ContainsFunc(seg, unicode.IsLetter) {
			return names
		}
		words := candidateWords(seg)
		if len(words) == 0 || candidateSubset(words, author) || candidateLabel.MatchString(seg) || candidateNoName.MatchString(seg) {
			continue
		}
		add(seg)
		return names
	}
	add(segs[0])
	return names
}

// nameNumberRe finds a number that is part of a book's name: "Vol. 17", "Part
// 1", "Book 3", "#2".
var nameNumberRe = regexp.MustCompile(`(?i)(?:\b(vol(?:ume)?|book|part|dis[ck]|cd)|(#))\.?\s*(\d+(?:\.\d+)?|one|two|three|four|five|six|seven|eight|nine|ten|eleven|twelve|thirteen|fourteen|fifteen|sixteen|seventeen|eighteen|nineteen|twenty)\b`)

// numberWords are the numbers a name spells out, "Book One", "Part Two".
var numberWords = map[string]string{
	"one": "1", "two": "2", "three": "3", "four": "4", "five": "5", "six": "6", "seven": "7", "eight": "8", "nine": "9", "ten": "10",
	"eleven": "11", "twelve": "12", "thirteen": "13", "fourteen": "14", "fifteen": "15", "sixteen": "16", "seventeen": "17", "eighteen": "18", "nineteen": "19", "twenty": "20",
}

// withoutNumbers is a name with every word holding a digit left out.
func withoutNumbers(s string) string {
	words := strings.Fields(s)
	return strings.Join(slices.DeleteFunc(words, func(w string) bool { return strings.ContainsAny(w, "0123456789") }), " ")
}

// leadNumber is a number a folder opens with and no dash follows, "01
// Honor", or that is the whole name, "01": a place in the series folder
// it sits in
var leadNumber = regexp.MustCompile(`^(\d{1,3})(?:\s+|$)`)

// nameNumbers reads the numbers that are part of the name, from the title and
// the folder alike, each keyed by the words before it and its own word, so
// "Mushoku Tensei: Jobless Reincarnation, Vol. 17" and the folder "Mushoku
// Tensei - Jobless Reincarnation, Vol. 17" give one key, and "Part 1 Volume
// 2" gives two. "Mort: Discworld, Book 4" and "Mort: Death, Book 1" key
// apart, as the series before the number differs.
func nameNumbers(names ...string) map[string]string {
	out := map[string]string{}
	for _, name := range names {
		for _, m := range nameNumberRe.FindAllStringSubmatchIndex(name, -1) {
			word := "#"
			if m[2] >= 0 {
				word = strings.ToLower(name[m[2]:m[3]])
			}
			switch {
			case strings.HasPrefix(word, "vol"):
				word = "vol"
			case word == "disk" || word == "cd":
				word = "disc"
			}
			number := strings.ToLower(name[m[6]:m[7]])
			number = cmp.Or(numberWords[number], number)
			before := pathWords(pathParen.ReplaceAllString(name[:m[0]], " "))
			out[before+"|"+word] = number
		}
	}
	return out
}

// seriesPositions is where an item says it sits in a numbered series, keyed
// by the series: the folder's "Series - 03 -", a "03 - Title" folder inside
// a series folder, and the series field's "#3".
func seriesPositions(s *itemSummary, folder string) map[string]string {
	out := map[string]string{}
	if m := folderMarker.FindStringSubmatch(folder); m != nil {
		out[seriesKey(m[1])] = m[2]
	} else if m := folderLead.FindStringSubmatch(folder); m != nil {
		if series := parentFolder(s.Path); series != "" {
			out[seriesKey(series)] = m[1]
		}
	} else if m := leadNumber.FindStringSubmatch(folder); m != nil {
		if series := parentFolder(s.Path); series != "" {
			out[seriesKey(series)] = m[1]
		}
	}
	for _, ref := range s.Series {
		if i := strings.LastIndex(ref, " #"); i > 0 {
			out[seriesKey(ref[:i])] = strings.TrimSpace(ref[i+2:])
		}
	}
	return out
}

// nameNumbersDiffer reports whether two sets of name numbers give one number
// differently: the same word ("vol") after words one of which runs within
// the other, so "Mushoku Tensei: Vol. 18" and "Mushoku Tensei: Jobless
// Reincarnation, Vol. 17" differ, and "Mort: Discworld, Book 4" and "Mort:
// Death, Book 1" are not compared. A number with nothing before it is
// compared only with another with nothing before it: "Book 4 - Mort" says
// nothing against "Mort: Discworld, Book 1".
func nameNumbersDiffer(a, b map[string]string) bool {
	for ka, na := range a {
		beforeA, wordA, _ := strings.Cut(ka, "|")
		for kb, nb := range b {
			beforeB, wordB, _ := strings.Cut(kb, "|")
			nested := beforeA == beforeB || (beforeA != "" && beforeB != "" && (containsWords(beforeA, beforeB) || containsWords(beforeB, beforeA)))
			if wordA == wordB && nested && !sameNumber(na, nb) {
				return true
			}
		}
	}
	return false
}

// numbersDiffer reports the first key two sets of numbers give differently,
// or "".
func numbersDiffer(a, b map[string]string) string {
	keys := slices.Sorted(maps.Keys(a))
	for _, k := range keys {
		if n, ok := b[k]; ok && !sameNumber(a[k], n) {
			return k
		}
	}
	return ""
}

// differentBooks says why two items are two books however alike their
// titles, or "": the strong reason bookConflict finds, or with byFolder its
// weak one.
func (f *candidateFacts) differentBooks(o *candidateFacts, byFolder bool) string {
	strong, weak := f.bookConflict(o, byFolder)
	return cmp.Or(strong, weak)
}

// bookConflict says what tells two items apart as two books. Strong: a number
// in their names that differs, or another place in one series, whatever the
// folders say - one recording numbered two ways, Discworld 4 and Death 1, is
// kept apart as well, and shown in split, as a false group costs more than a
// false split. Weak, with byFolder: folders that name different books with
// nothing more, "Dune" and "Dune 1965 Edition" as much as "Foundation" and
// "Foundation and Empire", which a listen settles.
func (f *candidateFacts) bookConflict(o *candidateFacts, byFolder bool) (strong, weak string) {
	known := len(f.names) > 0 && len(o.names) > 0
	agree := known && slices.ContainsFunc(f.names, func(n string) bool { return slices.Contains(o.names, n) })
	switch {
	case nameNumbersDiffer(f.numbers, o.numbers):
		return "numbered apart in their names", ""
	case numbersDiffer(f.positions, o.positions) != "":
		return "at different places in one series", ""
	case byFolder && known && !agree:
		a, b := f.names[0], o.names[0]
		return "", fmt.Sprintf("their folders name different books, %q and %q", min(a, b), max(a, b))
	}
	return "", ""
}

// readerNames is the readers a narrator field names, a "Last, First" pair of
// single words read as one name: "Barrett, Sean" is Sean Barrett, not Barrett
// and a Sean who might be Sean Connery.
func readerNames(field string) []string {
	parts := splitNames(field)
	out := make([]string, 0, len(parts))
	for i := 0; i < len(parts); i++ {
		if i+1 < len(parts) && len(candidateWords(parts[i])) == 1 && len(candidateWords(parts[i+1])) == 1 && strings.Contains(field, parts[i]+", "+parts[i+1]) {
			out = append(out, parts[i+1]+" "+parts[i])
			i++
			continue
		}
		out = append(out, parts[i])
	}
	return out
}

// readersApart judges two groups' readers, each group's pooled: apart when
// both name readers and no name of one is within a name of the other, so a
// copy naming two readers joins copies naming either; and why. A bracket on
// a book folder that is not a year, an edition, a format, a source, a
// language, a note on the copy or the author is its reader in a collector's
// library ("(Weiner)", "[John Lee]"), whether or not a narrator field names
// them.
func readersApart(xs, ys []*candidateFacts) (apart bool, why string) {
	pool := func(cs []*candidateFacts) (names [][]string, as []string) {
		for _, c := range cs {
			names = append(names, c.narrators...)
			as = append(as, strings.Trim(readersOf(c), `"`))
		}
		return names, as
	}
	xn, xa := pool(xs)
	yn, ya := pool(ys)
	if len(xn) == 0 || len(yn) == 0 {
		return false, ""
	}
	for _, a := range xn {
		for _, b := range yn {
			if candidateSubset(a, b) || candidateSubset(b, a) {
				return false, ""
			}
		}
	}
	a, b := strconv.Quote(joinNonEmpty(xa)), strconv.Quote(joinNonEmpty(ya))
	return true, fmt.Sprintf("they name different readers, %s and %s", min(a, b), max(a, b))
}

// joinNonEmpty joins the names that are not empty, each once.
func joinNonEmpty(names []string) string {
	var out []string
	for _, n := range names {
		if n != "" && !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	return strings.Join(out, "; ")
}

// readersOf is the readers an item names, as its field and brackets write
// them, each once.
func readersOf(c *candidateFacts) string {
	var names []string
	for _, n := range slices.Concat(c.fieldAs, c.bracketAs) {
		if !slices.ContainsFunc(names, func(o string) bool { return strings.EqualFold(o, n) }) {
			names = append(names, n)
		}
	}
	return strconv.Quote(strings.Join(names, ", "))
}

// otherReader reports whether both copies name their reader and no name of
// one is within a name of the other: another reading, not another copy.
func (f *candidateFacts) otherReader(o *candidateFacts) bool {
	if len(f.narrators) == 0 || len(o.narrators) == 0 {
		return false
	}
	for _, a := range f.narrators {
		for _, b := range o.narrators {
			if candidateSubset(a, b) || candidateSubset(b, a) {
				return false
			}
		}
	}
	return true
}

// candidateApart is how far apart two lengths are, as a share of the longer;
// 1 when either is unknown.
func candidateApart(a, b int) float64 {
	if a <= 0 || b <= 0 {
		return 1
	}
	return float64(max(a, b)-min(a, b)) / float64(max(a, b))
}

// candidates finds the pairs the keys did not join that are probably one
// recording, leaving out the copies incomplete already reports. Titles and
// lengths come from the listing already swept; the album rule reads the
// first file's tags, fetched only for the pairs by one author whose lengths
// already agree, a batch at a time.
func (d *dupCollector) candidates(ctx context.Context, client *abs.Client, groups []dupGroup, skip map[string]bool) ([]dupCandidate, error) {
	group := map[string]int{} // item id -> its group, for the pairs already joined
	for g := range groups {
		for _, it := range groups[g].Items {
			group[it.ID] = g + 1
		}
	}
	joined := func(i, j int) bool {
		gi, gj := group[d.items[i].ID], group[d.items[j].ID]
		return gi != 0 && gi == gj
	}
	facts := d.facts
	var books []int
	for i := range d.items {
		if d.items[i].Type == "book" {
			books = append(books, i)
		}
	}

	var out []dupCandidate
	listed := map[[2]int]bool{} // each pair once
	pair := func(i, j int, why string) {
		key := [2]int{min(i, j), max(i, j)}
		if listed[key] {
			return
		}
		listed[key] = true
		out = append(out, dupCandidate{Why: why + "; confirm with item_compare_audio", Items: []itemSummary{d.items[i], d.items[j]}})
	}
	// two copies one title and author join are a group, or in split, with
	// what kept them apart: not a candidate by the title rule as well, but
	// one kept apart on uncertain evidence with lengths near enough for one
	// recording is
	sameKey := func(i, j int) bool { return d.titleKeys[i] != "" && d.titleKeys[i] == d.titleKeys[j] }
	for _, c := range d.close {
		if joined(c.i, c.j) || skip[d.items[c.i].ID] || skip[d.items[c.j].ID] {
			continue
		}
		pair(c.i, c.j, fmt.Sprintf("the same title and author, lengths %.1f%% apart, though %s", 100*candidateApart(d.items[c.i].Duration, d.items[c.j].Duration), c.why))
	}

	// the same title, once brackets and subtitles are set aside
	byCore := map[string][]int{}
	for _, i := range books {
		if facts[i].core != "" {
			byCore[facts[i].core] = append(byCore[facts[i].core], i)
		}
	}
	for _, members := range byCore {
		for x, i := range members {
			for _, j := range members[x+1:] {
				if joined(i, j) || sameKey(i, j) || skip[d.items[i].ID] || skip[d.items[j].ID] {
					continue
				}
				if why := d.titleCandidate(i, j, &facts[i], &facts[j]); why != "" {
					pair(i, j, why)
				}
			}
		}
	}

	// one's album tag the other's title: same author, lengths within 1%
	byLength := slices.Clone(books)
	slices.SortFunc(byLength, func(a, b int) int { return cmp.Compare(d.items[a].Duration, d.items[b].Duration) })
	type albumPair struct {
		i, j   int
		author string
	}
	var pending []albumPair
	need := map[string]bool{}
	for x, i := range byLength {
		for _, j := range byLength[x+1:] {
			if candidateApart(d.items[i].Duration, d.items[j].Duration) > candidateAlbumLength {
				break
			}
			fi, fj := &facts[i], &facts[j]
			author := "same author"
			switch {
			case fi.sameAuthor(fj):
			case fi.sameShelf(fj):
				author = "same author folder"
			default:
				continue
			}
			if fi.core == fj.core || fi.otherReader(fj) || fi.differentBooks(fj, false) != "" || joined(i, j) || skip[d.items[i].ID] || skip[d.items[j].ID] {
				continue
			}
			pending = append(pending, albumPair{min(i, j), max(i, j), author})
			need[d.items[i].ID], need[d.items[j].ID] = true, true
		}
	}
	albums := map[string]string{}
	ids := make([]string, 0, len(need))
	for id := range need {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for chunk := range slices.Chunk(ids, embedBatchSize) {
		items, err := client.ItemsBatch(ctx, chunk)
		if err != nil {
			return nil, err
		}
		for k := range items {
			albums[items[k].ID] = candidateAlbum(&items[k])
		}
	}
	for _, p := range pending {
		ai, aj := albums[d.items[p.i].ID], albums[d.items[p.j].ID]
		apart := fmt.Sprintf("%.1f%%", 100*candidateApart(d.items[p.i].Duration, d.items[p.j].Duration))
		switch {
		case ai != "" && candidateCore(ai) == facts[p.j].core:
			pair(p.i, p.j, fmt.Sprintf("the album tag of the first, %q, is the second's title; %s, lengths %s apart", ai, p.author, apart))
		case aj != "" && candidateCore(aj) == facts[p.i].core:
			pair(p.i, p.j, fmt.Sprintf("the album tag of the second, %q, is the first's title; %s, lengths %s apart", aj, p.author, apart))
		}
	}

	slices.SortFunc(out, func(a, b dupCandidate) int {
		return cmp.Or(
			strings.Compare(strings.ToLower(a.Items[0].Title), strings.ToLower(b.Items[0].Title)),
			strings.Compare(a.Items[0].ID, b.Items[0].ID),
			strings.Compare(a.Items[1].ID, b.Items[1].ID))
	})
	return out, nil
}

// titleCandidate says why two items with the same core title are probably
// one recording, or "" when they are not: another reader named on each,
// another number in a series, or lengths too far apart for the evidence.
func (d *dupCollector) titleCandidate(i, j int, fi, fj *candidateFacts) string {
	if fi.otherReader(fj) || fi.differentBooks(fj, false) != "" {
		return ""
	}
	a, b := d.items[i], d.items[j]
	apart := candidateApart(a.Duration, b.Duration)
	labels := slices.Concat(fi.labels, fj.labels)
	sameAuthor, author := fi.sameAuthor(fj), "author"
	if !sameAuthor && fi.sameShelf(fj) {
		sameAuthor, author = true, "author folder"
	}

	var why string
	title := fi.title
	if len(fj.title) < len(title) {
		title = fj.title
	}
	switch {
	case sameAuthor && apart <= candidateSameLength:
		why = fmt.Sprintf("the same title, %q, and %s; lengths %.1f%% apart", title, author, 100*apart)
	case sameAuthor && len(labels) > 0 && apart <= candidateEditionLength:
		why = fmt.Sprintf("the same title, %q, and %s, one marked %q; lengths %.1f%% apart, as an edition's added material makes them", title, author, labels[0], 100*apart)
	case !sameAuthor && apart <= candidateSameLength:
		why = fmt.Sprintf("the same title, %q, under another author (%s, %s); lengths %.1f%% apart", title, cmp.Or(a.Author, "none"), cmp.Or(b.Author, "none"), 100*apart)
	default:
		return ""
	}
	if a.Tracks > 0 && b.Tracks > 0 && (a.Tracks == 1) != (b.Tracks == 1) {
		why += fmt.Sprintf("; one is a single file, the other %d", max(a.Tracks, b.Tracks))
	}
	return why
}

// candidateAlbum is the album tag of an item's first audio file.
func candidateAlbum(it *abs.Item) string {
	files := slices.Clone(it.Media.AudioFiles)
	slices.SortStableFunc(files, func(a, b abs.AudioFile) int { return cmp.Compare(a.Index, b.Index) })
	for _, f := range files {
		if !f.Exclude {
			return strings.TrimSpace(f.MetaTags["tagAlbum"])
		}
	}
	return ""
}
