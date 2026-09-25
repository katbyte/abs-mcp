package tools

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/katbyte/abs-mcp/lib/abs"
)

// A match is fill-only or override, and neither is what a collector wants
// for a library that is half curated and half file tags: fill-only keeps
// "Mrs. S .mp3", override loses the series numbering. smart is the third
// way. The match fills the empty fields as before; then each field the
// provider would write differently is decided by a rule - written when the
// library's value is plainly a file tag, a company in the narrator field, a
// timestamp for a year; kept when it is curated or the difference is only
// an honorific; and left for review, with both values, when no rule can
// say. Every decision is reported, so what the rules could not call is what
// the reader looks at.

// fieldDecision is one field's fate under smart matching.
type fieldDecision struct {
	Field    string `json:"field"`
	Local    string `json:"local,omitempty"`
	Provider string `json:"provider"`
	Action   string `json:"action"          jsonschema:"filled: was empty and the match wrote it; written: replaced by rule; kept: left as it was by rule; review: no rule applies, both values shown, nothing changed"`
	Rule     string `json:"rule"`
}

const (
	actFilled  = "filled"
	actWritten = "written"
	actKept    = "kept"
	actReview  = "review"
)

var (
	// a title that came from a file name or a chapter tag
	titleJunk = regexp.MustCompile(`(?i)\.(mp3|m4b|m4a|flac|ogg|wav)\s*$|\(uab\)|\(unabridged\)|^\d{1,3}\s+\S|^chapter\b|^\d+\s*-\s*|\bdisc\s*\d`)
	// a subtitle that is only a place in a series
	subtitleNumber = regexp.MustCompile(`(?i)\b(book|vol\.?|volume|novel|#)\s*\d`)
	// a company or imprint where a person's name belongs
	companyName = regexp.MustCompile(`(?i)\b(audio|library|entertainment|studios?|books?|media|recorded|publishing|publishers?|productions?|llc|inc|ltd|digital|group|press|courses|theat(re|er)|records)\b`)
	// "Larry Page - introduction": a credit, not an author
	creditSuffix = regexp.MustCompile(`(?i)\s*[-–]\s*(translator|foreword|introduction|illustrator|editor|adaptation|adaption|afterword|contributor|narrator|reader|übersetzer|traducteur)\b.*$`)
	// honorifics that providers add and collectors leave off
	honorific = regexp.MustCompile(`(?i)(^|\s)(dr\.?|md|m\.d\.?|ph\.?\s?d\.?|phd|esq\.?)(?:\s|$|,)`)
	// a year stored as a timestamp
	timestampYear = regexp.MustCompile(`^\d{4}-\d{2}`)
	// a genre that says nothing
	placeholderGenre = regexp.MustCompile(`(?i)^(audio ?books?.*|sci-?fi|fiction|non-?fiction|books?|unknown|other|general)$`)
)

// cleanPerson strips honorifics and credits from a name, and reports
// whether the entry was a credit rather than an author.
func cleanPerson(name string) (clean string, credit bool) {
	if creditSuffix.MatchString(name) {
		return "", true
	}
	clean = honorific.ReplaceAllString(name, " ")
	clean = strings.Trim(strings.Join(strings.Fields(clean), " "), " ,")
	return clean, false
}

// cleanPeople applies cleanPerson to a list, dropping credits and companies.
func cleanPeople(names []string) []string {
	var out []string
	for _, n := range names {
		c, credit := cleanPerson(n)
		if credit || c == "" || companyName.MatchString(c) {
			continue
		}
		out = append(out, c)
	}
	return out
}

// smartDecide decides every field the provider would write differently.
// hit is the provider's record for the item's asin; it is the item before
// the match.
func smartDecide(it *abs.Item, hit *abs.BookSearchResult) []fieldDecision {
	m := &it.Media.Metadata
	var out []fieldDecision
	for _, d := range fieldDiffs(it, hit) {
		dec := fieldDecision{Field: d.Field, Local: d.Local, Provider: d.Provider}
		if strings.TrimSpace(d.Local) == "" {
			dec.Action, dec.Rule = actFilled, "was empty"
			out = append(out, dec)
			continue
		}
		switch d.Field {
		case "title":
			switch {
			case titleJunk.MatchString(d.Local):
				dec.Action, dec.Rule = actWritten, "the title came from a file name or chapter tag"
			case slices.ContainsFunc(m.SeriesDisplay(), func(s string) bool { return norm(strings.Split(s, " #")[0]) == norm(d.Local) }):
				dec.Action, dec.Rule = actWritten, "the series name stood in for the title"
			case titleLevel(d.Local, d.Provider) == 2:
				dec.Action, dec.Rule = actKept, "the same title, differently punctuated"
			case titleLevel(d.Local, d.Provider) == 1:
				dec.Action, dec.Rule = actWritten, "the same title in the provider's form"
			default:
				dec.Action, dec.Rule = actReview, "a different title; the folder says which is right"
			}
		case "subtitle":
			if subtitleNumber.MatchString(d.Local) && !subtitleNumber.MatchString(d.Provider) {
				dec.Action, dec.Rule = actWritten, "the subtitle was only a place in the series"
			} else {
				dec.Action, dec.Rule = actReview, "subtitles are taste"
			}
		case "authors":
			local, provider := cleanPeople(splitNames(d.Local)), cleanPeople(splitNames(d.Provider))
			missing := slices.ContainsFunc(local, func(l string) bool {
				return !slices.ContainsFunc(provider, func(p string) bool { return norm(l) == norm(p) })
			})
			switch {
			case sameNames(local, provider):
				dec.Action, dec.Rule = actKept, "only credits or honorifics differ"
			case len(provider) > 0 && !missing:
				// Audible lists illustrators, translators and pen names as bare
				// co-authors as often as real ones (Ben McSweeney on The
				// Rithmatist, Paul Starr on Spice and Wolf, Jill Emerson on
				// Shadows), so the extra name is shown, never written
				dec.Action, dec.Rule = actReview, "the provider adds a name; an illustrator, translator or pen name arrives this way as often as a co-author"
				dec.Provider = strings.Join(provider, ", ")
			default:
				dec.Action, dec.Rule = actReview, "a different author; check the match"
			}
		case "narrators":
			local, provider := splitNames(d.Local), splitNames(d.Provider)
			authors := splitNames(m.AuthorDisplay())
			switch {
			case slices.ContainsFunc(local, func(n string) bool { return companyName.MatchString(n) }):
				dec.Action, dec.Rule = actWritten, "the narrator field held a company"
			case sharesName(local, authors) && !sharesName(provider, authors):
				dec.Action, dec.Rule = actWritten, "the narrator field held the author"
			case sharesName(local, provider):
				dec.Action, dec.Rule = actKept, "the same reader, spelled differently"
			default:
				dec.Action, dec.Rule = actReview, "a different reader; the recording may be another edition"
			}
		case "series":
			// the name is the collector's; the number is a fact, and a
			// disagreement there is worth a look
			if a, b, ok := seriesNumberClash(m.Series, hit.Series); ok {
				dec.Action, dec.Rule = actReview, fmt.Sprintf("same series, numbered %s here and %s by the provider", a, b)
			} else {
				dec.Action, dec.Rule = actKept, "series are curated"
			}
		case "genres":
			if slices.ContainsFunc(m.Genres, func(g string) bool { return !placeholderGenre.MatchString(strings.TrimSpace(g)) }) {
				dec.Action, dec.Rule = actKept, "genres are curated"
			} else {
				dec.Action, dec.Rule = actWritten, "the genres were placeholders"
			}
		case "publisher":
			dec.Action, dec.Rule = actWritten, "the publisher follows the recording"
		case "year":
			if timestampYear.MatchString(d.Local) {
				dec.Action, dec.Rule = actWritten, "the year was a timestamp"
			} else {
				dec.Action, dec.Rule = actKept, "a plain year is kept"
			}
		case "language":
			dec.Action, dec.Rule = actWritten, "the language follows the provider's spelling"
		case "description":
			if len(strings.TrimSpace(d.Local)) < descriptionStub {
				dec.Action, dec.Rule = actWritten, "the description was a stub"
			} else {
				dec.Action, dec.Rule = actKept, "the description is written"
			}
		default:
			dec.Action, dec.Rule = actReview, "no rule for this field"
		}
		out = append(out, dec)
	}
	return out
}

// smartUpdate is the update that writes the decisions marked written, with
// the provider's values.
func smartUpdate(decisions []fieldDecision, hit *abs.BookSearchResult) (abs.MediaUpdate, bool) {
	md := abs.MetadataUpdate{}
	changed := false
	str := func(v string) *string { return &v }
	for _, d := range decisions {
		if d.Action != actWritten {
			continue
		}
		changed = true
		switch d.Field {
		case "title":
			md.Title = str(hit.Title)
		case "subtitle":
			md.Subtitle = str(hit.Subtitle)
		case "authors":
			md.Authors = []abs.NameRef{}
			for _, a := range cleanPeople(splitNames(hit.Author)) {
				md.Authors = append(md.Authors, abs.NameRef{Name: a})
			}
		case "narrators":
			md.Narrators = splitNames(hit.Narrator)
		case "genres":
			md.Genres = slices.Clone(hit.Genres)
		case "publisher":
			md.Publisher = str(hit.Publisher)
		case "year":
			md.PublishedYear = str(hit.PublishedYear.String())
		case "language":
			md.Language = str(hit.Language)
		case "description":
			md.Description = str(hit.Description)
		}
	}
	return abs.MediaUpdate{Metadata: &md}, changed
}

// providerRecord fetches the provider's record for an asin or isbn.
func providerRecord(ctx context.Context, client *abs.Client, it *abs.Item, provider, asin, isbn string) (*abs.BookSearchResult, error) {
	query := asin
	if query == "" {
		query = isbn
	}
	results, err := client.SearchBooks(ctx, provider, query, "", it.ID)
	if err != nil {
		return nil, err
	}
	for i := range results {
		r := &results[i]
		if (asin != "" && strings.EqualFold(r.ASIN, asin)) || (isbn != "" && r.ISBN == isbn) {
			return r, nil
		}
	}
	return nil, fmt.Errorf("%s has no record for %s", provider, query)
}

// smartApply decides the fields, and unless preview is set applies the
// fill-only match and then writes the decided fields. The match result is
// nil in preview.
func smartApply(ctx context.Context, client *abs.Client, it *abs.Item, provider, asin, isbn string, preview bool) ([]fieldDecision, *abs.MatchResult, error) {
	hit, err := providerRecord(ctx, client, it, provider, asin, isbn)
	if err != nil {
		return nil, nil, err
	}
	decisions := smartDecide(it, hit)
	if preview {
		return decisions, nil, nil
	}
	res, err := client.Match(ctx, it.ID, abs.MatchOptions{Provider: provider, ASIN: asin, ISBN: isbn})
	if err != nil {
		return decisions, nil, err
	}
	if upd, changed := smartUpdate(decisions, hit); changed {
		updated, err := client.UpdateMedia(ctx, it.ID, upd)
		if err != nil {
			return decisions, res, fmt.Errorf("matched, but writing the decided fields failed: %w", err)
		}
		// the book changed when either write changed it: a match with
		// nothing empty to fill can still have fields decided in its favour
		res.Updated = res.Updated || updated
	}
	return decisions, res, nil
}

// smartCounts tallies decisions by action.
func smartCounts(decisions []fieldDecision) map[string]int {
	if len(decisions) == 0 {
		return nil
	}
	c := map[string]int{}
	for _, d := range decisions {
		c[d.Action]++
	}
	return c
}

// seriesCore is a series name reduced to what identifies it: no leading
// article, no trailing "series" or "saga", so "Foundation" meets "The
// Foundation Series".
func seriesCore(name string) string {
	k := seriesKey(name)
	for _, suffix := range []string{" series", " saga", " novels", " books"} {
		k = strings.TrimSuffix(k, suffix)
	}
	return k
}

// seriesNumberClash finds a series both sides name, spelling aside, at
// different numbers, and returns the two numbers.
func seriesNumberClash(local abs.SeriesRefs, provider []abs.SearchSeries) (mine, theirs string, clash bool) {
	for _, l := range local {
		for _, p := range provider {
			if l.Sequence == "" || p.Sequence == "" || seriesCore(l.Name) != seriesCore(p.Title()) {
				continue
			}
			if !sameNumber(l.Sequence, p.Sequence) {
				return l.Sequence, p.Sequence, true
			}
		}
	}
	return "", "", false
}
