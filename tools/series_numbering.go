package tools

import (
	"context"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/katbyte/abs-mcp/lib/abs"
)

// The numbering section of audit_series: does each book's series number agree
// with the number in its own folder, and is it in the series its folder says?
// A collector's folders are the ground truth they typed by hand: "The
// Hitchhiker's Guide to the Galaxy - 1 - The Hitchhiker's Guide to the Galaxy"
// sat at #4 in the series, and "The Executioner and Her Way of Life, Vol. 02"
// sat in no series at all, and the gaps audit, which only looks for numbers
// that are absent, saw a gap at #2 and nothing else.

type numberingFinding struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	Path         string `json:"path"`
	Series       string `json:"series,omitempty"        jsonschema:"the series the item is in, or that its folder names"`
	Sequence     string `json:"sequence,omitempty"      jsonschema:"the number the item has"`
	FolderNumber string `json:"folder_number,omitempty" jsonschema:"the number the folder carries"`
	Problem      string `json:"problem"                 jsonschema:"folder_disagrees: the folder's number is not the item's; unnumbered: the item is in the folder's series with no number; unlinked: the folder names a series the item is not in; duplicate_sequence: another title in the series has the same number (editions of one title sharing a number are fine and not reported); padding: the number is not zero-padded to the width of the series' highest number, a fixed convention rather than a reading of how the rest are written (one digit under ten, two from ten, three from a hundred: #2 in a twelve-book series is #02, #02 in a nine-book one is #2); folder_style: the book's folder is written unlike the series' other folders beside it - another style (Pandora's Star beside Commonwealth Saga - 02 - Judas Unchained), another spelling of the series, a double space round a dash, or a number padded unlike the rest; a series written one way throughout is not reported, whatever the way"`
	Suggest      string `json:"suggest,omitempty"       jsonschema:"the series value to set with item_edit; for duplicate_sequence the other title; for folder_style the path the folder would have in the style the library's series mostly use. Folders are renamed on disk, not by a tool here"`
	Detail       string `json:"detail,omitempty"        jsonschema:"folder_style: how the folder differs"`
}

var (
	folderMarker = regexp.MustCompile(`^(.*?)\s+[-–]\s+(\d+(?:\.\d+)?)\s+[-–]\s+`) // "Series - 03 - Title"
	folderVolume = regexp.MustCompile(`(?i)\b(?:vol(?:ume)?|book|part|#)\.?\s*(\d+(?:\.\d+)?)\b`)
	folderLead   = regexp.MustCompile(`^(\d{1,3})\s+[-–.]\s+`) // "03 - Title" inside a series folder
)

// folderNumber reads the sequence a folder name carries, and the series name
// when the folder spells it out before the number.
func folderNumber(relPath string) (series, number string) {
	folder := relPath
	if i := strings.LastIndex(folder, "/"); i >= 0 {
		folder = folder[i+1:]
	}
	if m := folderMarker.FindStringSubmatch(folder); m != nil {
		return strings.TrimSpace(m[1]), m[2]
	}
	if m := folderLead.FindStringSubmatch(folder); m != nil {
		return "", m[1]
	}
	if m := folderVolume.FindStringSubmatch(folder); m != nil {
		return "", m[1]
	}
	return "", ""
}

// parentFolder is the name of the folder an item sits in, which in a
// series-organised library is the series: "Mistborn" for "Brandon
// Sanderson/Mistborn/01 - The Final Empire". The whole parent path would be
// "Brandon Sanderson/Mistborn", which names no series at all.
func parentFolder(relPath string) string {
	if dir := path.Dir(strings.Trim(relPath, "/")); dir != "." {
		return path.Base(dir)
	}
	return ""
}

// sameNumber is "01" == "1" == "1.0".
func sameNumber(a, b string) bool {
	fa, oka := sequenceNumber(a)
	fb, okb := sequenceNumber(b)
	if !oka || !okb {
		return strings.TrimSpace(a) == strings.TrimSpace(b)
	}
	return fa == fb
}

// seriesKey is how two series names are the same series: spelling aside and
// the article set aside, as audit_series names does.
func seriesKey(name string) string {
	return vocabKey("series", name)
}

type numberedItem struct {
	ID, Title, RelPath string
	LibraryID, Author  string
	Series             []abs.SeriesRef
}

// numberingCollector gathers what the numbering checks need from a sweep.
type numberingCollector struct {
	items   []numberedItem
	names   map[string]string           // series key -> a display name, from the items' own series
	seqs    map[string]map[float64]bool // series key -> every number present, across every spelling of the key
	widths  map[string]int              // series key -> how many digits it pads to, worked out once
	pending []string                    // items whose series the listing cannot tell apart, for resolve
}

func newNumberingCollector() *numberingCollector {
	return &numberingCollector{names: map[string]string{}, seqs: map[string]map[float64]bool{}, widths: map[string]int{}}
}

// sequences is every number present under any of the series keys, sorted:
// what a series holds once its spellings are read as one.
func (c *numberingCollector) sequences(keys ...string) []float64 {
	seen := map[float64]bool{}
	for _, k := range keys {
		for n := range c.seqs[k] {
			seen[n] = true
		}
	}
	out := make([]float64, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	slices.Sort(out)
	return out
}

// padWidth is how many digits the series writes its whole numbers in: one
// under ten, two from ten, three from a hundred, so "#2" sits beside "#9"
// and "#02" beside "#12". That is the collector's convention, fixed rather
// than read from how the series happens to be written: every series past
// nine books is zero-padded. The highest number decides, not the count, so a
// series with gaps pads the same as a complete one; a number far from the
// rest (#2019 beside #1 to #12, see splitOutliers) does not decide it.
func (c *numberingCollector) padWidth(seriesKey string) int {
	if w, ok := c.widths[seriesKey]; ok {
		return w
	}
	kept, _ := splitOutliers(c.sequences(seriesKey))
	w := 1
	if len(kept) > 0 {
		switch top := kept[len(kept)-1]; {
		case top >= 100:
			w = 3
		case top >= 10:
			w = 2
		}
	}
	c.widths[seriesKey] = w
	return w
}

// styleNumber writes a number the way the series writes its numbers: "2"
// becomes "02" beside "12", and "02" becomes "2" beside "9". A fraction keeps
// its fraction, "2.5" beside "12" is "02.5", and a number that is not a
// number is left alone.
func (c *numberingCollector) styleNumber(seriesKey, number string) string {
	number = strings.TrimSpace(number)
	f, ok := sequenceNumber(number)
	if !ok || f < 0 {
		return number
	}
	whole := strconv.FormatInt(int64(f), 10)
	if w := c.padWidth(seriesKey); len(whole) < w {
		whole = strings.Repeat("0", w-len(whole)) + whole
	}
	if i := strings.IndexByte(number, '.'); i >= 0 {
		return whole + number[i:]
	}
	return whole
}

// seriesRefsOf is the item's series with numbers, from the structured refs
// when the item is expanded and from the joined "Name #4, Other #2" string
// the minified listing carries otherwise. The joined string cannot tell
// "Love, Death & Robots #2" from two series; seriesAmbiguous says when to
// fetch the item instead.
func seriesRefsOf(it *abs.Item) []abs.SeriesRef {
	m := &it.Media.Metadata
	if len(m.Series) > 0 {
		return m.Series
	}
	var refs []abs.SeriesRef
	for part := range strings.SplitSeq(m.SeriesName, ", ") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		ref := abs.SeriesRef{Name: part}
		if i := strings.LastIndex(part, " #"); i > 0 {
			ref.Name, ref.Sequence = strings.TrimSpace(part[:i]), strings.TrimSpace(part[i+2:])
		}
		refs = append(refs, ref)
	}
	return refs
}

// seriesAmbiguous reports whether the listing's joined series string could
// be read more than one way: a comma is both what joins two series and a
// character a series name may hold. "Foundation #2, Robot #9" is two
// numbered series; "Love, Death & Robots #2" has a part with no number,
// which is either an unnumbered series or the front of a name.
func seriesAmbiguous(it *abs.Item) bool {
	m := &it.Media.Metadata
	if len(m.Series) > 0 || !strings.Contains(m.SeriesName, ", ") {
		return false
	}
	for part := range strings.SplitSeq(m.SeriesName, ", ") {
		if !strings.Contains(part, " #") {
			return true
		}
	}
	return false
}

// add gathers one item from the sweep. One whose series string is
// ambiguous is held back for resolve, which fetches it.
func (c *numberingCollector) add(it *abs.Item) {
	if it.IsPodcast() {
		return
	}
	if seriesAmbiguous(it) {
		c.pending = append(c.pending, it.ID)
		return
	}
	c.addRefs(it, seriesRefsOf(it))
}

func (c *numberingCollector) addRefs(it *abs.Item, refs []abs.SeriesRef) {
	n := numberedItem{ID: it.ID, Title: it.Title(), RelPath: it.RelPath, LibraryID: it.LibraryID, Author: it.Media.Metadata.AuthorDisplay(), Series: refs}
	for _, s := range n.Series {
		if k := seriesKey(s.Name); k != "" {
			if _, ok := c.names[k]; !ok {
				c.names[k] = s.Name
			}
			if f, ok := sequenceNumber(s.Sequence); ok {
				if c.seqs[k] == nil {
					c.seqs[k] = map[float64]bool{}
				}
				c.seqs[k][f] = true
				delete(c.widths, k)
			}
		}
	}
	c.items = append(c.items, n)
}

// resolve fetches the items add held back, a batch at a time, and reads
// their series from the record, where each is its own entry. Most books are
// in one series, or in several with a number in each, so this is a handful
// of requests.
func (c *numberingCollector) resolve(ctx context.Context, client *abs.Client) error {
	pending := c.pending
	c.pending = nil
	for chunk := range slices.Chunk(pending, embedBatchSize) {
		items, err := client.ItemsBatch(ctx, chunk)
		if err != nil {
			return err
		}
		for j := range items {
			it := &items[j]
			refs := it.Media.Metadata.Series
			if len(refs) == 0 { // a record with no structured series: the string is all there is
				refs = seriesRefsOf(it)
			}
			c.addRefs(it, refs)
		}
	}
	return nil
}

// findings runs the three checks over what was gathered.
func (c *numberingCollector) findings() []numberingFinding {
	var out []numberingFinding
	type slot struct{ title, id string }
	bySeriesNumber := map[string]map[string][]slot{} // series key -> number -> titles

	for _, it := range c.items {
		folderSeries, number := folderNumber(it.RelPath)
		if folderSeries == "" {
			folderSeries = parentFolder(it.RelPath)
		}
		folderKey := seriesKey(folderSeries)

		for _, s := range it.Series {
			if s.Sequence == "" {
				continue
			}
			k := seriesKey(s.Name)
			if bySeriesNumber[k] == nil {
				bySeriesNumber[k] = map[string][]slot{}
			}
			num := strings.TrimSpace(s.Sequence)
			if f, ok := sequenceNumber(num); ok {
				num = strconv.FormatFloat(f, 'f', -1, 64)
			}
			bySeriesNumber[k][num] = append(bySeriesNumber[k][num], slot{it.Title, it.ID}) // raw: titleLevel reads the subtitle
			if styled := c.styleNumber(k, s.Sequence); styled != strings.TrimSpace(s.Sequence) {
				out = append(out, numberingFinding{ID: it.ID, Title: it.Title, Path: it.RelPath, Series: s.Name, Sequence: s.Sequence, Problem: "padding", Suggest: s.Name + " #" + styled})
			}
		}

		if number == "" {
			continue
		}
		// the series the folder points at, if the item is in it
		var inFolderSeries *abs.SeriesRef
		for i := range it.Series {
			if seriesKey(it.Series[i].Name) == folderKey {
				inFolderSeries = &it.Series[i]
			}
		}
		switch {
		case inFolderSeries != nil:
			suggest := inFolderSeries.Name + " #" + c.styleNumber(folderKey, number)
			if inFolderSeries.Sequence == "" {
				out = append(out, numberingFinding{ID: it.ID, Title: it.Title, Path: it.RelPath, Series: inFolderSeries.Name, FolderNumber: number, Problem: "unnumbered", Suggest: suggest})
			} else if !sameNumber(inFolderSeries.Sequence, number) {
				out = append(out, numberingFinding{ID: it.ID, Title: it.Title, Path: it.RelPath, Series: inFolderSeries.Name, Sequence: inFolderSeries.Sequence, FolderNumber: number, Problem: "folder_disagrees", Suggest: suggest})
			}
		case len(it.Series) > 0 && folderKey == "":
			// the folder has a number but names no series: check it against
			// whatever series the item has
			if s := it.Series[0]; s.Sequence != "" && !sameNumber(s.Sequence, number) {
				out = append(out, numberingFinding{ID: it.ID, Title: it.Title, Path: it.RelPath, Series: s.Name, Sequence: s.Sequence, FolderNumber: number, Problem: "folder_disagrees", Suggest: s.Name + " #" + c.styleNumber(seriesKey(s.Name), number)})
			}
		case folderKey != "":
			if name, known := c.names[folderKey]; known {
				out = append(out, numberingFinding{ID: it.ID, Title: it.Title, Path: it.RelPath, Series: name, FolderNumber: number, Problem: "unlinked", Suggest: name + " #" + c.styleNumber(folderKey, number)})
			}
		}
	}

	for k, numbers := range bySeriesNumber {
		for num, slots := range numbers {
			for i := 1; i < len(slots); i++ {
				// another edition of the same book, subtitle or "(Dramatized)" aside
				if slices.ContainsFunc(slots[:i], func(s slot) bool { return titleLevel(s.title, slots[i].title) > 0 }) {
					continue
				}
				out = append(out, numberingFinding{ID: slots[i].id, Title: c.titleOf(slots[i].id), Path: c.pathOf(slots[i].id), Series: c.names[k], Sequence: num, Problem: "duplicate_sequence", Suggest: "same number as " + c.titleOf(slots[0].id)})
			}
		}
	}

	out = append(out, c.folderStyleFindings()...)

	slices.SortFunc(out, func(a, b numberingFinding) int {
		if a.Problem != b.Problem {
			return strings.Compare(a.Problem, b.Problem)
		}
		if a.Path != b.Path {
			return strings.Compare(a.Path, b.Path)
		}
		return strings.Compare(a.ID, b.ID) // the same path in two libraries
	})
	return out
}

func (c *numberingCollector) titleOf(id string) string {
	for _, it := range c.items {
		if it.ID == id {
			return it.Title
		}
	}
	return ""
}

func (c *numberingCollector) pathOf(id string) string {
	for _, it := range c.items {
		if it.ID == id {
			return it.RelPath
		}
	}
	return ""
}
