package tools

import (
	"context"
	"regexp"
	"slices"
	"strings"

	"github.com/katbyte/abs-mcp/lib/abs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// audit_series is everything wrong with series, in three parts, the way
// audit_authors and audit_narrators cover people.
//
// Gaps: a series missing a book between the lowest and highest number it has.
//
// Names: two series that are one series spelled two ways. A library of ninety
// series held "The Chronicles of Amber" with nine books beside "Chronicles of
// Amber" with one, and the same for The Murderbot Diaries, because one book's
// metadata dropped the article. The detectors that find variant spellings
// elsewhere run here with the leading "The" set aside.
//
// Numbering: the folder is the collector's own record of where a book goes.
// A book whose folder says " - 1 - " and whose series says #4, a book whose
// folder sits in a series it is not linked to, and two different titles at
// the same number.
//
// Odd: a name that is not really a series name. One that ends in the word
// Series, one that is the author's name, one carrying a book number, one with
// a trademark sign, a space before its colon or doubled spaces. Whether
// "Siege of Terra: The Horus Heresy" should be "Siege of Terra" is taste, not
// an error, and is left to whoever reads the list.

type oddSeries struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Author   string   `json:"author,omitempty"`
	Books    int      `json:"books"`
	Problems []string `json:"problems"          jsonschema:"redundant_word: ends in 'Series'; named_after_author: the series is called what its author is called; sequence_in_name: carries 'Book 3', 'Vol. 2' or '#4'; stray_characters: a trademark sign, a space before a colon, doubled spaces or trailing punctuation. Fix with series_edit name="`
	Suggest  string   `json:"suggest,omitempty" jsonschema:"the name with the redundant word or stray characters removed, when that is all that is wrong"`
}

// articleSeries is a series name that opens with an article, reported only
// when asked: whether "The Expanse" should be "Expanse" is the collector's
// taste, and the name of a book standing in for a series ("The Shining") is
// not touched either way.
type articleSeries struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Author  string `json:"author,omitempty"`
	Books   int    `json:"books"`
	Suggest string `json:"suggest"          jsonschema:"the name without its article, for series_edit name="`
}

type seriesCounts struct {
	Gaps      int `json:"gaps"`
	Names     int `json:"names"`
	Odd       int `json:"odd"`
	Numbering int `json:"numbering"`
	Titles    int `json:"titles"`
	Articles  int `json:"articles,omitempty" jsonschema:"only with articles=true"`
}

type seriesOut struct {
	Scanned   int                `json:"series_scanned"`
	Found     int                `json:"total_findings"     jsonschema:"gaps, names, odd and numbering together, before limit; articles too when asked for"`
	Counts    seriesCounts       `json:"counts"`
	Gaps      []seriesGap        `json:"gaps"               jsonschema:"series missing a book: whole numbers absent between the lowest and highest present, interior gaps only; unlinked names the books on the shelf that fill a gap once linked, and merged what is left once the spellings in names are one series"`
	Names     []vocabGroup       `json:"names"              jsonschema:"series that are one series spelled two ways, the one with more books first: series_merge from= into= moves the other's books over, and the empty series goes away. Each spelling carries its author"`
	Odd       []oddSeries        `json:"odd"                jsonschema:"names that are not series names, most books first"`
	Numbering []numberingFinding `json:"numbering"          jsonschema:"books whose folder disagrees with their series number, books their folder puts in a series they are not in, and two titles sharing a number"`
	Titles    []titleFinding     `json:"titles"             jsonschema:"books whose title is their series name, or carries it with a number, where the folder says what the title is: suggest is the value for item_edit title="`
	Articles  []articleSeries    `json:"articles,omitempty" jsonschema:"with articles=true: series names opening with The, A or An that are not a book's title, most books first"`
}

var (
	seriesWord     = regexp.MustCompile(`(?i)\s+series$`)
	leadingArticle = regexp.MustCompile(`(?i)^(the|an?)\s+`)
	sequenceInName = regexp.MustCompile(`(?i)(\b(book|vol\.?|volume|part|no\.?)\s*\d+|#\s*\d+)\s*$`)
	spaceColon     = regexp.MustCompile(`\s+([:;,])`)
	doubleSpace    = regexp.MustCompile(`\s{2,}`)
	trailingPunct  = regexp.MustCompile(`[\s:;,\-]+$`)
)

// oddSeriesName lists what is odd about a series name and, when the oddity is
// something to strip, the name without it.
func oddSeriesName(name, author string) (problems []string, suggest string) {
	fixed := strings.TrimSpace(name)
	if seriesWord.MatchString(fixed) {
		problems = append(problems, "redundant_word")
		fixed = seriesWord.ReplaceAllString(fixed, "")
	}
	if author != "" && norm(name) == norm(author) {
		problems = append(problems, "named_after_author")
	}
	if sequenceInName.MatchString(fixed) {
		problems = append(problems, "sequence_in_name")
	}
	if strings.ContainsAny(name, "™®©") || spaceColon.MatchString(name) || doubleSpace.MatchString(name) ||
		trailingPunct.MatchString(name) || strings.TrimSpace(name) != name {
		problems = append(problems, "stray_characters")
		fixed = strings.NewReplacer("™", "", "®", "", "©", "").Replace(fixed)
		fixed = spaceColon.ReplaceAllString(fixed, "$1")
		fixed = doubleSpace.ReplaceAllString(fixed, " ")
		fixed = strings.TrimSpace(trailingPunct.ReplaceAllString(fixed, ""))
	}
	if len(problems) == 0 {
		return nil, ""
	}
	if fixed != name && !slices.Contains(problems, "named_after_author") && !slices.Contains(problems, "sequence_in_name") {
		suggest = fixed
	}
	return problems, suggest
}

// articleSeriesName is the name without its leading article, or "" when the
// name has none or is one of the series' own book titles: "The Shining", and
// "The Executioner and Her Way of Life" whose books are "..., Vol. 01".
func articleSeriesName(name string, titles []string) string {
	m := leadingArticle.FindStringIndex(name)
	if m == nil {
		return ""
	}
	n := norm(name)
	for _, t := range titles {
		if strings.HasPrefix(norm(t), n) {
			return ""
		}
	}
	return strings.TrimSpace(name[m[1]:])
}

// seriesNames feeds one library's series into the name detectors, the odd
// list and, when asked, the articles list, from the listing alone, and
// records whose series each name is. Podcast libraries have no series.
func seriesNames(ctx context.Context, client *abs.Client, lib *abs.Library, out *seriesOut, names spellingCounts, authors map[string]string, articles bool) error {
	if lib.IsPodcast() {
		return nil
	}
	for page := 0; ; page++ {
		series, total, err := client.SeriesList(ctx, lib.ID, abs.ListOptions{Limit: seriesPageSize, Page: page, Sort: "name"})
		if err != nil {
			return err
		}
		for j := range series {
			s := &series[j]
			names.addValue("series", s.Name, max(len(s.Books), 1))
			author := ""
			if len(s.Books) > 0 {
				author = s.Books[0].Media.Metadata.AuthorDisplay()
			}
			authors[s.Name] = author
			if problems, suggest := oddSeriesName(s.Name, author); len(problems) > 0 {
				out.Odd = append(out.Odd, oddSeries{ID: s.ID, Name: s.Name, Author: author, Books: len(s.Books), Problems: problems, Suggest: suggest})
			}
			if articles {
				titles := make([]string, 0, len(s.Books))
				for k := range s.Books {
					titles = append(titles, s.Books[k].Title())
				}
				if suggest := articleSeriesName(s.Name, titles); suggest != "" {
					out.Articles = append(out.Articles, articleSeries{ID: s.ID, Name: s.Name, Author: author, Books: len(s.Books), Suggest: suggest})
				}
			}
		}
		if len(series) == 0 || (page+1)*seriesPageSize >= total {
			return nil
		}
	}
}

// withSeriesAuthors puts each spelling's author on the name groups and drops
// the near matches that are two authors' series: Witchery and The Witcher
// are a letter apart and nothing to do with each other. A spelling group
// (the same key) stays whoever the authors are, since one of them is a
// record with the wrong author on it.
func withSeriesAuthors(groups []vocabGroup, authors map[string]string) []vocabGroup {
	out := make([]vocabGroup, 0, len(groups))
	for _, g := range groups {
		shared, known := false, 0
		seen := map[string]bool{}
		for i := range g.Spellings {
			a := authors[g.Spellings[i].Value]
			g.Spellings[i].Author = a
			if a == "" {
				continue
			}
			known++
			if seen[norm(a)] {
				shared = true
			}
			seen[norm(a)] = true
		}
		if g.Kind == "near" && known == len(g.Spellings) && !shared {
			continue
		}
		out = append(out, g)
	}
	return out
}

// crossReferenceGaps reads each gap beside the other sections. A missing
// number whose book sits unlinked in the right folder is not missing, it is
// unlinked, and the numbering suggestion is carried onto the gap. A series
// the names section reports as one of several spellings has its gap read
// across all of them: Wheel of Time 0-4 and 10-14 beside The Wheel of Time
// 5-9 and 13 is a complete run, not two broken ones.
func crossReferenceGaps(gaps []seriesGap, names []vocabGroup, numbering []numberingFinding, coll *numberingCollector) {
	for i := range gaps {
		g := &gaps[i]
		key := seriesKey(g.Name)
		for _, m := range g.Missing {
			for _, f := range numbering {
				if f.Problem == "unlinked" && seriesKey(f.Series) == key && sameNumber(f.FolderNumber, m) {
					g.Unlinked = append(g.Unlinked, gapUnlinked{Missing: m, ID: f.ID, Path: f.Path, Suggest: f.Suggest})
				}
			}
		}
		for _, grp := range names {
			if grp.Field != "series" || !slices.ContainsFunc(grp.Spellings, func(s spelling) bool { return s.Value == g.Name }) {
				continue
			}
			keys := make([]string, 0, len(grp.Spellings))
			with := make([]string, 0, len(grp.Spellings)-1)
			for _, s := range grp.Spellings {
				keys = append(keys, seriesKey(s.Value))
				if s.Value != g.Name {
					with = append(with, s.Value)
				}
			}
			missing := missingBetween(coll.sequences(keys...))
			if missing == nil {
				missing = []string{}
			}
			g.Merged = &gapMerged{With: with, Missing: missing}
			break
		}
	}
}

func registerSeriesAudit(r *registry) {
	client := r.client

	type seriesIn struct {
		Library  string `json:"library,omitempty"  jsonschema:"library name or id; default every book library"`
		Limit    int    `json:"limit,omitempty"    jsonschema:"maximum rows per section, default 50"`
		Articles bool   `json:"articles,omitempty" jsonschema:"also list series names that open with The, A or An, with the bare name suggested; a matter of taste, so off by default"`
	}
	add(r, readTool, &mcp.Tool{
		Name: "audit_series",
		Description: "Everything wrong with series. Gaps: a series missing a book, sequence numbers absent between the lowest and the highest the library has (interior gaps only, so a series whose first book is #3 is not flagged for #1-2, and novella numbering such as 4.5 never creates one; series_get shows what is present). A gap whose book is on the shelf in the right folder but not linked is reported with that book under unlinked, and a series that names reports as one of several spellings has its gap read across all of them under merged. " +
			"Names: two series that are one series spelled two ways ('The Chronicles of Amber', 'Chronicles of Amber', 'Chronicles of Amber Series'), the one with more books first; fix with series_merge. Each spelling carries its author, and two authors' series a letter apart are not reported. " +
			"Numbering: a book whose folder carries a different number from its series entry ('Series - 1 - Title' at #4), a book whose folder puts it in a series it is not linked to, and two different titles at the same number, plus a number padded unlike the rest of its series (one digit under ten, two from ten, three from a hundred: #1 #02 #3 in one series is reported at #02); the folder is taken as the collector's own record, and suggest is the series value for item_edit add_series. " +
			"Odd: names that are not series names - ending in the word Series, called what the author is called, carrying a book number, or with a trademark sign, a space before a colon or doubled spaces; fix with series_edit name=, and suggest is the cleaned name where that is all that is wrong. " +
			"Titles: a book whose title is its series name, with or without a number ('Harry Hole 1', 'Beebo Brinker' on the book Odd Girl Out), or carries the series name and a number beside the real title ('The Bat - Harry Hole Series, Book 1', 'Bright Falls 03 - Iris Kelly Doesn't Date'), judged only where the folder is 'Series - 03 - Title' and so says what the title is; suggest is that title, for item_edit title=. No provider is asked. " +
			"Whether a subtitle belongs in the name ('Siege of Terra: The Horus Heresy' or 'Siege of Terra') is a matter of taste and is not reported, and so is a leading article: articles=true lists 'The Expanse' and 'A Hanne Wilhelmsen Novel' with the bare name suggested, leaving out names that are a book's own title.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in seriesIn) (*mcp.CallToolResult, seriesOut, error) {
		libs, err := resolveLibraries(ctx, client, in.Library)
		if err != nil {
			return nil, seriesOut{}, err
		}
		limit := limitOr(in.Limit, 50)
		var gaps gapsOut
		out := seriesOut{Gaps: []seriesGap{}, Names: []vocabGroup{}, Odd: []oddSeries{}, Numbering: []numberingFinding{}, Titles: []titleFinding{}}
		names := newSpellingCounts([]string{"series"})
		authors := map[string]string{}
		numbering := newNumberingCollector()
		for i := range libs {
			if err := sweepSeriesGaps(ctx, client, &libs[i], limit, &gaps); err != nil {
				return nil, seriesOut{}, err
			}
			if err := seriesNames(ctx, client, &libs[i], &out, names, authors, in.Articles); err != nil {
				return nil, seriesOut{}, err
			}
			if libs[i].IsPodcast() {
				continue
			}
			if err := client.ItemsAll(ctx, libs[i].ID, abs.ItemsOptions{}, func(items []abs.Item) bool {
				for j := range items {
					numbering.add(&items[j])
				}
				return true
			}); err != nil {
				return nil, seriesOut{}, err
			}
		}
		slices.SortFunc(out.Odd, func(x, y oddSeries) int {
			if x.Books != y.Books {
				return y.Books - x.Books
			}
			return strings.Compare(x.Name, y.Name)
		})
		slices.SortFunc(out.Articles, func(x, y articleSeries) int {
			if x.Books != y.Books {
				return y.Books - x.Books
			}
			return strings.Compare(x.Name, y.Name)
		})
		allNames, allNumbering, allTitles := withSeriesAuthors(names.report("series"), authors), numbering.findings(), numbering.titleFindings()
		crossReferenceGaps(gaps.Series, allNames, allNumbering, numbering)
		out.Scanned = gaps.Scanned
		out.Counts = seriesCounts{Gaps: gaps.Found, Names: len(allNames), Odd: len(out.Odd), Numbering: len(allNumbering), Titles: len(allTitles), Articles: len(out.Articles)}
		out.Found = gaps.Found + len(allNames) + len(out.Odd) + len(allNumbering) + len(allTitles) + len(out.Articles)
		out.Gaps = append(out.Gaps, gaps.Series...)
		out.Names = append(out.Names, allNames[:min(len(allNames), limit)]...)
		out.Odd = out.Odd[:min(len(out.Odd), limit)]
		out.Numbering = append(out.Numbering, allNumbering[:min(len(allNumbering), limit)]...)
		out.Titles = append(out.Titles, allTitles[:min(len(allTitles), limit)]...)
		if in.Articles {
			out.Articles = out.Articles[:min(len(out.Articles), limit)]
		}

		return nil, out, nil
	})
}
