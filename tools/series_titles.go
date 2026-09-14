package tools

import (
	"regexp"
	"slices"
	"strings"
)

// The titles section of audit_series: a title that is really the series.
// Twenty-four Harry Hole books came in as "Harry Hole 1 (Sean Barrett)" and
// "The Bat - Harry Hole Series, Book 1", Ann Bannon's as "Beebo Brinker" five
// times over, and audit_path let every one through, because a folder called
// "Harry Hole - 01 - The Bat" does name "Harry Hole 1" when the whole folder
// is allowed as a name (which "Spice and Wolf, Vol. 4" needs). The series a
// book is linked to is the tell: a title that is the series name with a
// number, or carries the series name and a number inside it, is not the
// title. The folder's own title segment, "The Bat", is the collector's record
// of what is, so only the "Series - 03 - Title" layout is judged, and the
// segment is the suggestion. No provider is asked.

type titleFinding struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Path    string `json:"path"`
	Series  string `json:"series"`
	Problem string `json:"problem" jsonschema:"series_as_title: the title is the series name, with or without a number ('Harry Hole 1', 'Beebo Brinker' on Odd Girl Out); series_in_title: the title carries the series name and a number beside the real one ('The Bat - Harry Hole Series, Book 1', 'Bright Falls 03 - Iris Kelly Doesn't Date')"`
	Suggest string `json:"suggest" jsonschema:"the title the folder carries, for item_edit title="`
}

// seriesMarkerWords are what sits between a series name and its number in
// a title: "Harry Hole Series, Book 1", "Jumper Series Bk 1", "Vol. 4", "#2".
// Applied to normalized text, where punctuation is already gone.
const seriesMarkerWords = `(?:\s+series)?\s*(?:book|bk|vol|volume|no|part|number)?\s*\d+(?:\s\d+)?`

// seriesShapedTitle says how a title is shaped like its series, or "" when
// it is not. Both are compared normalized, the title without its
// parentheticals (and with them, for a series written in brackets), and the
// series with and without a leading "The".
func seriesShapedTitle(title string, seriesNames []string) string {
	t, raw := norm(pathStrip(title)), norm(title) // "[Jumper, #2.5]" is a bracket the strip would take
	if t == "" {
		return ""
	}
	for _, s := range seriesNames {
		ns := strings.TrimSuffix(norm(s), " series")
		if ns == "" {
			continue
		}
		for _, name := range []string{ns, strings.TrimPrefix(ns, "the ")} {
			if name == "" {
				continue
			}
			q := regexp.QuoteMeta(name)
			if regexp.MustCompile(`^` + q + `(?:` + seriesMarkerWords + `)?$`).MatchString(t) {
				return "series_as_title"
			}
			if in := regexp.MustCompile(`(?:^|\s)` + q + seriesMarkerWords + `(?:\s|$)`); in.MatchString(t) || in.MatchString(raw) {
				return "series_in_title"
			}
		}
	}
	return ""
}

// folderTitle is the title segment of a "Series - 03 - Title" folder, or "".
func folderTitle(relPath string) string {
	folder := relPath
	if i := strings.LastIndex(folder, "/"); i >= 0 {
		folder = folder[i+1:]
	}
	m := folderMarker.FindStringIndex(folder)
	if m == nil {
		return ""
	}
	return strings.TrimSpace(pathStrip(folder[m[1]:]))
}

// titleFindings runs the title check over what the numbering sweep gathered.
func (c *numberingCollector) titleFindings() []titleFinding {
	var out []titleFinding
	for _, it := range c.items {
		suggest := folderTitle(it.RelPath)
		if suggest == "" || pathWords(suggest) == pathWords(it.Title) {
			continue
		}
		names := make([]string, 0, len(it.Series)+1)
		for _, s := range it.Series {
			names = append(names, s.Name)
		}
		if folderSeries, _ := folderNumber(it.RelPath); folderSeries != "" {
			names = append(names, folderSeries)
		}
		problem := seriesShapedTitle(it.Title, names)
		if problem == "" || len(names) == 0 {
			continue
		}
		series := names[len(names)-1] // the folder's, when the book is in no series
		if len(it.Series) > 0 {
			series = it.Series[0].Name
		}
		out = append(out, titleFinding{ID: it.ID, Title: it.Title, Path: it.RelPath, Series: series, Problem: problem, Suggest: suggest})
	}
	slices.SortFunc(out, func(a, b titleFinding) int {
		if a.Problem != b.Problem {
			return strings.Compare(a.Problem, b.Problem)
		}
		return strings.Compare(a.Path, b.Path)
	})
	return out
}
