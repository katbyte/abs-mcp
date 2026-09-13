package tools

import (
	"slices"
	"strings"
	"unicode"
)

// The detectors behind audit_spelling beyond "same key, different spelling".
// Every one of them was found by hand first, reading a narrator list of four
// hundred names: "Narrator..........Sean Barrett" beside "Sean Barrett", a
// "Fajer Al" cut off from "Fajer Al-Kaisi", "Peter Whickam" a letter away from
// "Peter Wickham", two names in one field, and a "Ph.D." left behind by a
// split on the comma. Detection is code; these make it so.

// nameFields hold people or organisations, where a value is a name and the
// truncation and initials checks make sense (they do not for a genre).
var nameFields = map[string]bool{"narrators": true, "authors": true, "publishers": true}

// personFields hold people, where importers leave "Read by" and "Ph.D." behind.
var personFields = map[string]bool{"narrators": true, "authors": true}

// nameAffixes are the leading phrases an importer glues onto a person's name,
// matched against the normalized value. Longer phrases come first so that
// "narrators" is tried before "narrator".
var nameAffixes = []string{
	"read by", "narrated by", "performed by", "performance by", "narration by",
	"introduction by", "intro by", "foreword by", "afterword by", "translated by",
	"narrators", "narrator", "translator", "featuring", "feat", "with",
}

// fragments are whole values that are not a name: a credential or a leftover
// from splitting "Jane Doe, Ph.D." on the comma.
var fragments = map[string]bool{
	"ph d": true, "phd": true, "md": true, "m d": true, "dc": true, "jr": true, "sr": true,
	"ii": true, "iii": true, "iv": true, "esq": true, "et al": true, "and": true, "others": true,
	"various": true, "unknown": true, "n a": true, "none": true, "tbd": true,
}

// nameCore strips the affixes from a normalized name and reports whether any
// were there: "narrator sean barrett" -> "sean barrett", true. A value that is
// nothing but an affix is left alone, so it surfaces as itself.
func nameCore(field, n string) (string, bool) {
	if !personFields[field] {
		return n, false
	}
	stripped := false
	for {
		again := false
		for _, a := range nameAffixes {
			if rest, ok := strings.CutPrefix(n, a+" "); ok {
				n = rest
				stripped, again = true, true
				break
			}
		}
		if !again {
			return n, stripped
		}
	}
}

// cleanName is the original spelling with its first words removed, counting
// words the way norm does, so "Narrator..........Sean Barrett" less one word
// is "Sean Barrett" with its capitals intact.
func cleanName(original string, words int) string {
	if words <= 0 {
		return strings.TrimSpace(original)
	}
	inWord, seen := false, 0
	for i, r := range original {
		lr := unicode.ToLower(r)
		switch {
		case (lr >= 'a' && lr <= 'z') || (r >= '0' && r <= '9'):
			if !inWord {
				inWord = true
				seen++
				if seen > words {
					return strings.TrimSpace(original[i:])
				}
			}
		case r == '_' || r == '-' || r == '.' || unicode.IsSpace(r):
			inWord = false
		}
		// anything else norm drops without breaking the word (O'Brien)
	}
	return strings.TrimSpace(original)
}

// truncationOf reports whether short is long cut off ("fajer al" in "fajer al
// kaisi", "full cast" in "full cast recording") or the same name without its
// middle initials ("jack evans" in "jack r b evans"). Both are normalized.
func truncationOf(short, long string) bool {
	if short == long {
		return false
	}
	ss, ls := strings.ReplaceAll(short, " ", ""), strings.ReplaceAll(long, " ", "")
	if len(ss) >= 6 && strings.HasPrefix(ls, ss) {
		return true
	}
	sw, lw := strings.Fields(short), strings.Fields(long)
	if len(sw) < 2 || len(sw) >= len(lw) || sw[0] != lw[0] || sw[len(sw)-1] != lw[len(lw)-1] {
		return false
	}
	i := 0
	for _, w := range lw {
		if i < len(sw) && w == sw[i] {
			i++
		}
	}
	return i == len(sw)
}

// initialsOf reports whether short is long with a first name reduced to its
// initial, middle names or initials aside: "c z dunn" is "christian dunn",
// "j k rowling" is "joanne rowling". The surname has to be the same and at
// least three letters, and the short form a single letter: "joe hill" is not
// "joey w hill", those are two people. Both are normalized.
func initialsOf(short, long string) bool {
	sw, lw := strings.Fields(short), strings.Fields(long)
	if len(sw) < 2 || len(lw) < 2 || short == long {
		return false
	}
	sur := sw[len(sw)-1]
	if len(sur) < 3 || sur != lw[len(lw)-1] {
		return false
	}
	a, b := sw[0], lw[0]
	if len(a) > len(b) {
		a, b = b, a
	}
	return len(a) == 1 && len(b) > 1 && strings.HasPrefix(b, a)
}

// typoApart reports whether two normalized values differ by a slip of the
// keyboard: one edit for anything six letters or longer, two for twelve or
// longer when they start with the same word (peter whickam / peter wickham).
func typoApart(a, b string) bool {
	if a == b || len(a) < 6 || len(b) < 6 {
		return false
	}
	switch typoDistance(a, b, 2) {
	case 0, 1:
		return true
	case 2:
		if len(a) < 12 || len(b) < 12 {
			return false
		}
		return strings.Fields(a)[0] == strings.Fields(b)[0]
	}
	return false
}

// typoDistance is the Damerau-Levenshtein distance (optimal string alignment,
// so a transposition is one edit), capped: anything past limit comes back as
// limit+1, and strings whose lengths differ by more than limit are not walked.
func typoDistance(a, b string, limit int) int {
	ra, rb := []rune(a), []rune(b)
	if d := len(ra) - len(rb); d > limit || -d > limit {
		return limit + 1
	}
	prev2 := make([]int, len(rb)+1)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		best := cur[0]
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
			if i > 1 && j > 1 && ra[i-1] == rb[j-2] && ra[i-2] == rb[j-1] {
				cur[j] = min(cur[j], prev2[j-2]+1)
			}
			best = min(best, cur[j])
		}
		if best > limit {
			return limit + 1
		}
		prev2, prev, cur = prev, cur, prev2
	}
	if prev[len(rb)] > limit {
		return limit + 1
	}
	return prev[len(rb)]
}

// splitValue reports whether a person field holds more than one name: "Etienne
// Mailloux/Paul Ablaze", "A & B", "A and B", "A; B". norm drops the slash and
// the ampersand, so the original is what is looked at.
func splitValue(field, original, n string) bool {
	if !personFields[field] {
		return false
	}
	return strings.ContainsAny(original, "/;&") || slices.Contains(strings.Fields(n), "and")
}

// splitParts is the names inside a split value, in order.
func splitParts(original string) []string {
	s := strings.NewReplacer("/", ";", "&", ";", " and ", ";", " And ", ";", " AND ", ";").Replace(original)
	var parts []string
	for p := range strings.SplitSeq(s, ";") {
		if p = strings.TrimSpace(p); p != "" {
			parts = append(parts, p)
		}
	}
	return parts
}
