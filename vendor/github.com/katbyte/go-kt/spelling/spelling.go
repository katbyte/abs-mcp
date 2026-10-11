// Package spelling finds one name spelled two ways, as genres, tags and studios arrive typed by hand: "Sci-Fi" and "sci fi", "Amélie" and "Amelie",
// "Noir" and "Nior". Key folds spellings that mean the same onto one string; TypoApart, TruncationOf and InitialsOf say when two keys that still
// differ are a slip, a name cut short or an initial. Which spelling to keep is the caller's to decide. Each rule says what real library it was found
// in.
package spelling

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Key lowercases, folds accents to plain letters, turns _ - . / into spaces and drops the rest of the punctuation: "Sci-Fi" and "sci fi" meet at "sci
// fi". Letters of other scripts are kept, so two Japanese titles stay two values.
func Key(s string) string {
	var b strings.Builder
	latin := false // the last rune written was plain ASCII
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == ' ':
			b.WriteRune(r)
			latin = r != ' '
		case r == '_' || r == '-' || r == '.' || r == '/':
			b.WriteRune(' ')
			latin = false
		case r > unicode.MaxASCII && unicode.IsSpace(r):
			// an ideographic or a non-breaking space is still a space
			b.WriteRune(' ')
			latin = false
		case r > unicode.MaxASCII:
			// Amélie is Amelie, not Amlie; 進撃の巨人 is itself, not nothing
			if spelling, word := WordRune(r, latin); word && spelling != "" {
				b.WriteString(spelling)
				latin = spelling[0] < utf8.RuneSelf
			}
		}
	}

	return strings.Join(strings.Fields(b.String()), " ")
}

// WordRune is how a name fold reads a rune past ASCII: an accented Latin letter as its plain spelling (FoldLetter), any other letter or digit as
// itself. A combining mark after a plain Latin letter (afterLatin) is an accent written apart and folds away; in a script that writes vowels as marks
// it is kept. word is false for punctuation, symbols and spaces.
func WordRune(r rune, afterLatin bool) (spelling string, word bool) {
	if folded := FoldLetter(r); folded != "" {
		return folded, true
	}
	switch {
	case unicode.IsLetter(r), unicode.IsDigit(r):
		return string(r), true
	case unicode.IsMark(r) && afterLatin:
		return "", true
	case unicode.IsMark(r):
		return string(r), true
	}

	return "", false
}

// FoldLetter is the plain-ASCII spelling of an accented Latin letter, or "" for a rune that is not one: é is e, ß is ss, þ is th.
func FoldLetter(r rune) string {
	for ascii, accented := range foldTable {
		if strings.ContainsRune(accented, r) {
			return ascii
		}
	}

	return ""
}

// foldTable is the accented Latin letters by their plain spelling, lowercase; a value is lowercased before it is folded.
var foldTable = map[string]string{
	"a": "àáâãäåāąă", "ae": "æ", "c": "çćčċ", "d": "ďđð", "e": "èéêëēęěė", "g": "ğģ",
	"i": "ìíîïīıį", "l": "łļľ", "n": "ñńňņ", "o": "òóôõöøōőœ", "r": "řŗ", "s": "šşśș", "ss": "ß",
	"t": "ťţț", "th": "þ", "u": "ùúûüūůűų", "y": "ýÿ", "z": "žźż",
}

// Marks are the marks in a value that make another name of it, in order: "discovery+" is a brand and "Discovery" a channel. Case, spacing, accents
// and separators make no other name and Key folds them; two values of one Key and different Marks are two names.
func Marks(v string) string {
	var b strings.Builder
	for _, r := range v {
		if strings.ContainsRune("+!()[]{}#@*$%=~|<>^", r) {
			b.WriteRune(r)
		}
	}

	return b.String()
}

// Letters is a value's length in letters, not bytes: counted in bytes a two-ideograph name passed for a six-letter one.
func Letters(s string) int { return utf8.RuneCountInString(s) }

// TruncationOf reports whether short is long cut off ("warner bros" in "warner bros pictures"): at least six letters, and whole words. Both are keys
// (Key).
func TruncationOf(short, long string) bool {
	if short == long || Letters(strings.ReplaceAll(short, " ", "")) < 6 {
		return false
	}

	return strings.HasPrefix(long, short+" ")
}

// InitialsOf reports whether two keys are one person's name, one with the first name reduced to its initial: "c z dunn" is "christian dunn". The last
// name must match and be three letters or more, and the initial one letter: "joe hill" and "joey w hill" are two people.
func InitialsOf(short, long string) bool {
	sw, lw := strings.Fields(short), strings.Fields(long)
	if len(sw) < 2 || len(lw) < 2 || short == long {
		return false
	}

	last := sw[len(sw)-1]
	if Letters(last) < 3 || last != lw[len(lw)-1] {
		return false
	}

	a, b := sw[0], lw[0]
	if Letters(a) > Letters(b) {
		a, b = b, a
	}

	return Letters(a) == 1 && Letters(b) > 1 && strings.HasPrefix(b, a)
}

// TypoApart reports whether two keys differ by a slip: one edit at six letters or more, two at twelve when they start with the same word, and every
// differing word a slip itself (SlipsInWords).
func TypoApart(a, b string) bool {
	if a == b || Letters(a) < 6 || Letters(b) < 6 {
		return false
	}
	switch Distance(a, b, 2) {
	case 0, 1:
	case 2:
		if Letters(a) < 12 || Letters(b) < 12 || strings.Fields(a)[0] != strings.Fields(b)[0] {
			return false
		}
	default:
		return false
	}

	return SlipsInWords(a, b)
}

// SlipsInWords says whether two values differ only by slips within words: a word of five letters or more one edit away, or a shorter one with two
// letters swapped ("Nior") or dropped from its middle ("Nr"). A short word otherwise an edit apart is as often another word: "teenage life" and
// "teenage love". A different word count is a space slip.
func SlipsInWords(a, b string) bool {
	wa, wb := strings.Fields(a), strings.Fields(b)
	if len(wa) != len(wb) {
		return true
	}
	for i := range wa {
		switch {
		case wa[i] == wb[i]:
		case Letters(wa[i]) >= 5 && Letters(wb[i]) >= 5:
			if Distance(wa[i], wb[i], 1) > 1 {
				return false
			}
		case !Swapped(wa[i], wb[i]) && !DroppedFromMiddle(wa[i], wb[i]) && !DroppedFromMiddle(wb[i], wa[i]):
			return false
		}
	}

	return true
}

// Swapped says whether two words are one with two letters next to each other swapped: "nior" and "noir".
func Swapped(a, b string) bool {
	ra, rb := []rune(a), []rune(b)
	if len(ra) != len(rb) || len(ra) < 3 {
		return false
	}
	at := -1
	for i := range ra {
		if ra[i] != rb[i] {
			at = i

			break
		}
	}

	return at >= 0 && at+1 < len(ra) && ra[at] == rb[at+1] && ra[at+1] == rb[at] && string(ra[at+2:]) == string(rb[at+2:])
}

// DroppedFromMiddle says whether short is long with letters dropped from between its first and last: "nr" from "noir". A letter lost at an end
// ("age", "rage") makes another word as often as not.
func DroppedFromMiddle(short, long string) bool {
	s, l := []rune(short), []rune(long)
	if len(s) < 2 || len(s) >= len(l) || s[0] != l[0] || s[len(s)-1] != l[len(l)-1] {
		return false
	}
	at := 0
	for _, r := range l {
		if at < len(s) && r == s[at] {
			at++
		}
	}

	return at == len(s)
}

// Distance is the edit distance with a swap counting as one edit, capped: anything past limit comes back as limit+1.
func Distance(a, b string, limit int) int {
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
