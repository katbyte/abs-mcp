// Package text is small helpers for wording that more than one tool needs: the first sentence of a description, its first line, the article a
// word takes.
package text

import "strings"

// FirstSentence is s cut to its opening sentence on its first line, ending as a sentence ends: for a list one line each, or a comment drawn from an
// API's description. The full stop of an abbreviation does not end it: "a url (e.g. from item_cover_search)" was once cut at "(e.g.".
func FirstSentence(s string) string {
	s = FirstLine(s)

	// a sentence ends at a full stop, a question mark or an exclamation mark with a space after it, but not at an abbreviation's full stop
	for from := 0; ; {
		i := strings.IndexAny(s[from:], ".?!")
		if i < 0 {
			break
		}

		end := from + i
		if end+1 >= len(s) {
			break
		}

		if s[end+1] == ' ' && (s[end] != '.' || !abbreviated(s[:end+1])) {
			s = s[:end+1]
			break
		}

		from = end + 1
	}

	if s != "" && !strings.HasSuffix(s, ".") && !strings.HasSuffix(s, "?") && !strings.HasSuffix(s, "!") {
		s += "."
	}

	return s
}

// abbreviated says whether s ends in a dotted abbreviation rather than a sentence.
func abbreviated(s string) bool {
	lower := strings.ToLower(s)
	for _, abbr := range []string{"e.g.", "i.e.", "etc.", "vs.", "ph.d."} {
		if strings.HasSuffix(lower, abbr) {
			return true
		}
	}

	return false
}

// FirstLine is the first line of s that has anything on it, without the spaces around it.
func FirstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")

	return strings.TrimSpace(line)
}

// Article is the article a word takes, "an" or "a", by its first letter: an indexer, a notification. It knows nothing of how a word sounds, so "an
// user" is for the caller to word around.
func Article(word string) string {
	if word != "" && strings.ContainsRune("AEIOUaeiou", rune(word[0])) {
		return "an"
	}

	return "a"
}
