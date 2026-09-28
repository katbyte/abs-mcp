package tools

import (
	"fmt"
	"math"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/katbyte/abs-mcp/lib/abs"
)

// The comparator behind item_match_batch and audit_matched: does a provider's
// record describe the recording on disk, or only the same book? Five Douglas
// Adams titles each found their book on the first search and each was a
// different edition, ten to nineteen percent shorter than Audible's; applied
// blind, every one would have carried the wrong narrator and asin. So the
// title says "same book" and the duration and narrator say "same recording",
// and the two are reported apart.

// Confidence levels, best first.
const (
	confExact   = "exact"   // same book, same recording
	confLikely  = "likely"  // same book; duration unknown on one side, or title only close
	confEdition = "edition" // same book, different recording: duration or narrator disagree
	confUnsure  = "unsure"  // candidates exist but none fit the title and author
	confNone    = "none"    // the provider returned nothing
)

var confidenceRank = map[string]int{confExact: 0, confLikely: 1, confEdition: 2, confUnsure: 3, confNone: 4}

// defaultDurationTolerance is how far apart two durations of the same
// recording may be: encoder padding and a trimmed credit, not a chapter.
const defaultDurationTolerance = 0.03

// maxDurationTolerance is the most a caller may loosen it to. The editions it
// is there to tell apart are ten to nineteen percent shorter, and a tolerance
// wide enough to cover them calls every one of them exact.
const maxDurationTolerance = 0.1

type matchScore struct {
	Confidence string
	Reason     string
	// the parts, for audit_matched to name what is wrong
	TitleLevel    int     // 2 same, 1 close, 0 different
	AuthorOK      bool    // false only when both sides name an author and they differ
	Duration      string  // same, off, unknown
	DurationDelta float64 // candidate relative to the item, when both are known
	Narrator      string  // same, differs, unknown
}

var (
	titleNoise   = regexp.MustCompile(`(?i)\s*[\(\[](unabridged|abridged|dramati[sz]ed|dramatization|a novel|audiobook|audio book|(read|narrated|performed) by [^\)\]]*)[\)\]]`)
	titleSplit   = regexp.MustCompile(`\s*:\s+|\s+-\s+|\s+–\s+|\s+—\s+`)
	nameSplitter = regexp.MustCompile(`\s*[,;/&]\s*|\s+and\s+`)
)

// cleanTitle is a title reduced to what identifies the book: no edition
// parenthetical, no punctuation, no leading article.
func cleanTitle(s string) string {
	return strings.TrimPrefix(norm(titleNoise.ReplaceAllString(s, " ")), "the ")
}

// titleForms are the ways a title can be written down: whole, and either side
// of a colon or dash, so "Reckoners #2: Firefight" meets "Firefight" and
// "Killingly" meets "Killingly: A Novel".
func titleForms(s string) []string {
	forms := []string{cleanTitle(s)}
	for _, part := range titleSplit.Split(titleNoise.ReplaceAllString(s, " "), -1) {
		if p := cleanTitle(part); len(p) >= 4 && !slices.Contains(forms, p) {
			forms = append(forms, p)
		}
	}
	return forms
}

// titleLevel is 2 when the titles are the same, 1 when one is the other with
// a subtitle removed or a typo away, 0 otherwise.
func titleLevel(item, candidate string) int {
	a, b := cleanTitle(item), cleanTitle(candidate)
	if a == "" || b == "" {
		return 0
	}
	if a == b {
		return 2
	}
	for _, fa := range titleForms(item) {
		for _, fb := range titleForms(candidate) {
			if fa == fb || typoApart(fa, fb) {
				return 1
			}
		}
	}
	return 0
}

// splitNames breaks "A, B & C" into names.
func splitNames(s string) []string {
	var out []string
	for _, n := range nameSplitter.Split(s, -1) {
		if n = strings.TrimSpace(n); n != "" {
			out = append(out, n)
		}
	}
	return out
}

// sharesName reports whether any name on one side is any name on the other,
// spelling and initials aside.
func sharesName(as, bs []string) bool {
	for _, a := range as {
		na := norm(a)
		for _, b := range bs {
			nb := norm(b)
			if na == nb || initialsOf(na, nb) || initialsOf(nb, na) {
				return true
			}
		}
	}
	return false
}

// readerSource is one place the library says who reads an item, and the
// readers it names there, as name words (readerWords) and as written. A
// folder's brackets name a reader by surname as often as not, "(Tipton)",
// and only there does a surname alone stand for a reader.
type readerSource struct {
	from     string
	readers  [][]string
	as       []string
	surnames bool
	turned   []string // the field read "Last, First" whole, when it can be
}

func (r *readerSource) add(name string) {
	if words := readerWords(name); len(words) > 0 {
		r.readers, r.as = append(r.readers, words), append(r.as, strings.TrimSpace(name))
	}
}

// production is a label that says the recording is a production with a
// cast, not a reading: "(Full Cast)", "[Dramatized]", "Graphic Audio".
var production = regexp.MustCompile(`(?i)\b(full[- ]?cast|cast|dramati[sz](ed|ation)|graphic ?audio|audio ?drama|radio (play|drama)|bbc radio)\b`)

// creditLine finds a credit that says who reads the book, "Read by Paul
// Heck.": a description that is only that is what an importer leaves when
// there is no description, and a title bracket saying so is a collector's
// mark. An introduction or a foreword credits someone else.
var creditLine = regexp.MustCompile(`(?i)^(?:read|narrated|performed|narration)\s+by\s+(.+)$`)

// creditEnd is where the names in a credit stop: a clause after them, "with
// an introduction by", or the end of the sentence.
var creditEnd = regexp.MustCompile(`(?i)\s+(?:with|featuring|and an?|plus)\s+(?:an?\s+)?(?:introduction|foreword|afterword|interview|music)\b|\s+with\s+|[;:\n(]`)

// creditNames is the names a credit's words after "by" hold: up to a clause
// or the end of the sentence, a full stop after an initial or a title (J.,
// Dr.) not ending it.
func creditNames(s string) string {
	if loc := creditEnd.FindStringIndex(s); loc != nil {
		s = s[:loc[0]]
	}
	words := strings.Fields(s)
	for i, w := range words {
		if !strings.HasSuffix(w, ".") {
			continue
		}
		bare := strings.TrimSuffix(w, ".")
		if len([]rune(bare)) <= 2 || creditTitles[strings.ToLower(bare)] || strings.Contains(bare, ".") {
			continue // an initial, "Dr.", "J.K."
		}
		words, words[i] = words[:i+1], bare
		break
	}
	return strings.Join(words, " ")
}

// creditTitles are the titles past two letters a credit writes with a full
// stop.
var creditTitles = map[string]bool{"mrs": true, "prof": true, "rev": true, "sir": true}

// readerWords is a name's words as a comparison reads them: lower-case,
// punctuation gone, initials kept, "Dr" and role words ("read by") dropped.
// "J. K. Simmons" is [j k simmons].
func readerWords(name string) []string {
	var out []string
	for w := range strings.FieldsSeq(norm(name)) {
		if !candidateRoles[w] && !nameTitles[w] && !nameSuffixes[w] {
			out = append(out, w)
		}
	}
	return out
}

// nameSuffixes are the words after a name that the store may leave off:
// "Robert Downey Jr." is Robert Downey.
var nameSuffixes = map[string]bool{"jr": true, "sr": true, "ii": true, "iii": true, "iv": true}

// nameTitles are the words before a name that are not part of it.
var nameTitles = map[string]bool{"dr": true, "mr": true, "mrs": true, "ms": true, "prof": true, "sir": true, "dame": true}

// itemReaders is where the library says who reads the item, each place on
// its own: the narrator field; the brackets on the book's folder, which is
// how a collector marks a reading ("Citizen of the Galaxy (Tipton)", "The
// Bat [John Lee]"), and square brackets on a folder above it; a title
// bracket saying so, "(read by Lorelei King)"; and a description that is
// only a credit line, "Read by Paul Heck". A place that names a production
// rather than a reader, "(Full Cast)", or a narrator field reading "Graphic
// Audio LLC.", says so in productionSaid.
func itemReaders(it *abs.Item) (sources []readerSource, productionSaid string) {
	m := &it.Media.Metadata
	author := candidateWords(m.AuthorDisplay())

	field := readerSource{from: "the narrator field"}
	// "Le Guin, Ursula K.": a surname of two words before its comma, which
	// readerNames leaves as two names; read turned round as well, so either
	// reading can agree with the candidate
	if last, first, ok := strings.Cut(m.NarratorDisplay(), ","); ok && !strings.ContainsAny(first, ",;&/") && !nameSplitter.MatchString(strings.TrimSpace(first)) {
		field.turned = readerWords(strings.TrimSpace(first) + " " + strings.TrimSpace(last))
	}
	for _, name := range readerNames(m.NarratorDisplay()) {
		switch {
		case production.MatchString(name):
			productionSaid = "the narrator field says " + strings.TrimSpace(name)
		case len(candidateWords(name)) > 0 && !candidateLabel.MatchString(name) && !candidateNoName.MatchString(name):
			field.add(name)
		}
	}
	if len(field.readers) > 0 {
		sources = append(sources, field)
	}

	// the folder's brackets, read as the duplicates audit reads them, less
	// the ones that name the series or say where or how the copy was made;
	// the title's hold a series or a subtitle as often as a reader,
	// "Pleasure Activism (Emergent Strategy)", and count only when they say
	// so. Square brackets higher up mark a reading too, "Author/Series [John
	// Lee]/Book"; parentheses there are the series' or the shelf's
	parents := strings.Join(squareBrackets.FindAllString(path.Dir(it.RelPath), -1), " ")
	br := bracketsOf("", parents+" "+path.Base(it.RelPath), author)
	series := candidateWords(strings.Join(seriesOf(m), " "))
	folder := readerSource{from: "the folder", surnames: true}
	for i, words := range br.readers {
		if plausibleReader(br.as[i]) && !candidateSubset(words, series) {
			folder.add(br.as[i])
		}
	}
	if len(folder.readers) > 0 {
		sources = append(sources, folder)
	}
	for _, label := range br.labels {
		if production.MatchString(label) && productionSaid == "" {
			productionSaid = "the folder says " + label
		}
	}

	title := readerSource{from: "the title"}
	for _, bm := range candidateBrackets.FindAllStringSubmatch(m.Title, -1) {
		if cm := creditLine.FindStringSubmatch(strings.TrimSpace(bm[1])); cm != nil {
			addCredit(&title, creditNames(cm[1]), m)
		}
	}
	if len(title.readers) > 0 {
		sources = append(sources, title)
	}

	if _, stub := stubDescription(m.Description); stub {
		if cm := creditLine.FindStringSubmatch(strings.Join(strings.Fields(descriptionTags.ReplaceAllString(m.Description, " ")), " ")); cm != nil {
			credit := readerSource{from: "the description"}
			addCredit(&credit, creditNames(cm[1]), m)
			if len(credit.readers) > 0 {
				sources = append(sources, credit)
			}
		}
	}
	return sources, productionSaid
}

// addCredit adds the readers a credit names; "the author" is the book's.
func addCredit(src *readerSource, names string, m *abs.Metadata) {
	for _, name := range readerNames(names) {
		switch n := norm(name); {
		case n == "the author" || n == "author" || n == "the authors":
			for _, a := range readerNames(m.AuthorDisplay()) {
				src.add(a)
			}
		case len(readerWords(name)) <= 4 && !candidateLabel.MatchString(name) && !candidateNoName.MatchString(name) && plausibleReader(name):
			src.add(name)
		}
	}
}

// squareBrackets finds the square brackets in a path.
var squareBrackets = regexp.MustCompile(`\[[^\]]+\]`)

// seriesOf is the series an item is in, by name, whichever shape it came
// in.
func seriesOf(m *abs.Metadata) []string {
	var out []string
	for _, s := range m.SeriesDisplay() {
		out = append(out, strings.Split(s, " #")[0])
	}
	if len(out) == 0 && m.SeriesName != "" {
		for s := range strings.SplitSeq(m.SeriesName, ",") {
			out = append(out, strings.Split(s, " #")[0])
		}
	}
	return out
}

// notReader are words a bracket on a folder holds that no reader's name
// does: the small words of a title ("(and Lazy Employees)", "(1 of 5)"),
// the notes that mark a part or a cut ("(Start)", "(Disc)", "(Unabr)"), and
// where or how the copy was made ("(UK)", "(Illustrated)").
var notReader = map[string]bool{
	"a": true, "of": true, "the": true, "an": true, "in": true, "and": true, "to": true, "for": true,
	"with": true, "on": true, "at": true, "from": true, "is": true, "or": true, "my": true, "your": true,
	"start": true, "finish": true, "end": true, "beginning": true, "disc": true, "disk": true, "cd": true,
	"unabr": true, "abr": true, "anthology": true, "prologue": true, "epilogue": true, "sample": true,
	"preview": true, "excerpt": true, "bonus": true,
	"uk": true, "us": true, "usa": true, "au": true, "illustrated": true, "annotated": true, "revised": true, "expanded": true,
	"sci": true, "scifi": true, "fi": true, "fantasy": true, "horror": true, "mystery": true, "thriller": true, "romance": true,
	"fiction": true, "nonfiction": true, "classics": true, "classic": true, "ya": true, "kids": true, "memoir": true, "biography": true,
}

// plausibleReader reports whether a name as a bracket writes it can be a
// reader's: every word, not only the ones kept as the name. A letter
// written with a full stop is an initial, "(A.J. Smith)"; one without is
// the article, "(A Memoir)".
func plausibleReader(name string) bool {
	named := false
	for raw := range strings.FieldsSeq(name) {
		initials := strings.Contains(raw, ".")
		for w := range strings.FieldsSeq(norm(raw)) {
			named = true
			if notReader[w] && (!initials || len(w) != 1) {
				return false
			}
		}
	}
	return named
}

// readsSame reports whether any reader one place names is one of the
// candidate's readers.
func readsSame(src readerSource, cand [][]string) bool {
	for _, b := range cand {
		if len(src.turned) > 1 && sameReader(src.turned, b, false) {
			return true
		}
	}
	for _, a := range src.readers {
		for _, b := range cand {
			if sameReader(a, b, src.surnames) {
				return true
			}
		}
	}
	return false
}

// sameReader reports whether two names, as readerWords, are one reader. The
// surnames must be the same, and where both give more than a surname, the
// first names must agree, or one be the other's initial: "J. Lee" is John
// Lee and not Christopher Lee, "Emily Card" is Emily Janice Card. A surname
// alone, or the end of a name, "(Van Horn)", is one only where surnames
// stands for readers, a folder's brackets; a narrator field reading "Fry"
// is not every Fry.
func sameReader(a, b []string, surnames bool) bool {
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	if slices.Equal(a, b) {
		return true
	}
	if a[len(a)-1] != b[len(b)-1] {
		return false
	}
	if len(a) > len(b) {
		a, b = b, a
	}
	if surnames && slices.Equal(a, b[len(b)-len(a):]) {
		return true
	}
	if len(a) == 1 {
		return false // a surname alone, and not where one stands for a reader
	}
	x, y := a[0], b[0]
	if len(x) > len(y) {
		x, y = y, x
	}
	return x == y || (len(x) == 1 && strings.HasPrefix(y, x))
}

// scoreMatch compares a provider hit with the item on disk.
func scoreMatch(it *abs.Item, c *abs.BookSearchResult, tolerance float64) matchScore {
	if tolerance <= 0 {
		tolerance = defaultDurationTolerance
	}
	s := matchScore{TitleLevel: titleLevel(it.Title(), c.Title), AuthorOK: true, Duration: "unknown", Narrator: "unknown"}
	var reasons []string

	switch s.TitleLevel {
	case 2:
		reasons = append(reasons, "title same")
	case 1:
		reasons = append(reasons, fmt.Sprintf("title close (%q)", c.Title))
	default:
		reasons = append(reasons, fmt.Sprintf("title differs (%q)", c.Title))
	}

	itemAuthors, candAuthors := splitNames(it.Media.Metadata.AuthorDisplay()), splitNames(c.Author)
	switch {
	case len(itemAuthors) == 0 || len(candAuthors) == 0:
		reasons = append(reasons, "author unknown")
	case sharesName(itemAuthors, candAuthors):
		reasons = append(reasons, "author same")
	default:
		s.AuthorOK = false
		reasons = append(reasons, fmt.Sprintf("author differs (%q)", c.Author))
	}

	itemSec, candSec := it.Media.Duration, c.Duration*60
	if itemSec > 0 && candSec > 0 {
		s.DurationDelta = (candSec - itemSec) / itemSec
		if math.Abs(s.DurationDelta) <= tolerance {
			s.Duration = "same"
			reasons = append(reasons, "duration "+fmtDuration(candSec))
		} else {
			s.Duration = "off"
			reasons = append(reasons, fmt.Sprintf("duration %s vs %s (%+.0f%%)", fmtDuration(candSec), fmtDuration(itemSec), s.DurationDelta*100))
		}
	} else {
		reasons = append(reasons, "duration unknown")
	}

	s.Narrator, reasons = judgeReaders(it, c, reasons)

	switch {
	case s.TitleLevel == 0 || !s.AuthorOK:
		s.Confidence = confUnsure
	case s.Duration == "off" || s.Narrator == "differs":
		s.Confidence = confEdition
	case s.TitleLevel == 2 && s.Duration == "same":
		s.Confidence = confExact
	default: // same book; the recording cannot be confirmed
		s.Confidence = confLikely
	}
	s.Reason = strings.Join(reasons, "; ")
	return s
}

// judgeReaders compares who the library says reads the item with who the
// candidate says reads it: "differs" when any one place the library names a
// reader - the narrator field, the folder, a credit line - names none of the
// candidate's, since a copy matched to the wrong reading carries that
// reading's name in its field and only the folder still says otherwise; and
// when the library says the recording is a production with a cast and the
// candidate is one reader's. "same" when every place that names a reader
// agrees, "unknown" when none does.
func judgeReaders(it *abs.Item, c *abs.BookSearchResult, reasons []string) (verdict string, why []string) {
	var cand [][]string
	for _, name := range readerNames(c.Narrator) {
		if words := readerWords(name); len(words) > 0 {
			cand = append(cand, words)
		}
	}
	if len(cand) == 0 {
		return "unknown", reasons
	}
	sources, productionSaid := itemReaders(it)
	if productionSaid != "" && len(cand) == 1 && !production.MatchString(c.Narrator+" "+c.Title) {
		return "differs", append(reasons, fmt.Sprintf("narrator %s alone, but %s", c.Narrator, productionSaid))
	}
	if len(sources) == 0 {
		return "unknown", append(reasons, "read by "+c.Narrator)
	}
	for _, src := range sources {
		if !readsSame(src, cand) {
			return "differs", append(reasons, fmt.Sprintf("narrator %s vs %s in %s", c.Narrator, strings.Join(src.as, ", "), src.from))
		}
	}
	return "same", append(reasons, "narrator same")
}

// scored is a candidate with its score, for ranking.
type scored struct {
	Result abs.BookSearchResult
	Score  matchScore
}

// rankCandidates scores every hit and orders them best first: by confidence,
// then by how close the duration is.
func rankCandidates(it *abs.Item, results []abs.BookSearchResult, tolerance float64) []scored {
	out := make([]scored, 0, len(results))
	for i := range results {
		out = append(out, scored{Result: results[i], Score: scoreMatch(it, &results[i], tolerance)})
	}
	slices.SortStableFunc(out, func(a, b scored) int {
		if ra, rb := confidenceRank[a.Score.Confidence], confidenceRank[b.Score.Confidence]; ra != rb {
			return ra - rb
		}
		return int(math.Abs(a.Score.DurationDelta)*1000) - int(math.Abs(b.Score.DurationDelta)*1000)
	})
	return out
}
