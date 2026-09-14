package tools

import (
	"fmt"
	"math"
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
	titleNoise   = regexp.MustCompile(`(?i)\s*[\(\[](unabridged|abridged|dramati[sz]ed|dramatization|a novel|audiobook|audio book)[\)\]]`)
	titleSplit   = regexp.MustCompile(`\s*:\s+|\s+-\s+|\s+–\s+|\s+—\s+`)
	bracketName  = regexp.MustCompile(`\[([^\]]+)\]`)
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

// itemNarrators is who the library says reads the item: the narrator field,
// and a name in square brackets in the folder ("Harry Hole - 01 - The Bat
// [John Lee]"), which is how a collector marks the edition.
func itemNarrators(it *abs.Item) []string {
	names := splitNames(it.Media.Metadata.NarratorDisplay())
	for _, m := range bracketName.FindAllStringSubmatch(it.RelPath, -1) {
		if n := strings.TrimSpace(m[1]); n != "" && !regexp.MustCompile(`^\d`).MatchString(n) {
			names = append(names, n)
		}
	}
	return names
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

	itemNarr, candNarr := itemNarrators(it), splitNames(c.Narrator)
	switch {
	case len(itemNarr) == 0 || len(candNarr) == 0:
		if len(candNarr) > 0 {
			reasons = append(reasons, "read by "+c.Narrator)
		}
	case sharesName(itemNarr, candNarr):
		s.Narrator = "same"
		reasons = append(reasons, "narrator same")
	default:
		s.Narrator = "differs"
		reasons = append(reasons, fmt.Sprintf("narrator %s vs %s", c.Narrator, strings.Join(itemNarr, ", ")))
	}

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
