package tools

import (
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
)

// One series named in two styles on disk. A collector's folders follow a
// pattern - "Commonwealth Saga - 02 - Judas Unchained" - until an import
// drops a book in under its bare title beside them, "Pandora's Star", or one
// with a double space or an unpadded number. The path audit reads a name
// through its style, on purpose, and the rest of this audit reads metadata,
// so nothing saw it. The books of a series that share a folder are compared
// with each other; where they are written in more than one way, each book
// not in the library's own style for a series is reported with its name in
// that style.

// folderStyle is how one book folder is written.
type folderStyle struct {
	kind   string // see styleWords
	series string // the series as the folder writes it
	number string // the number as written
	title  string // the book's title as the folder writes it, if it does
	loose  bool   // a separator spaced other than " - "
}

var styleWords = map[string]string{
	"numbered":      `"Series - NN - Title"`,
	"series_number": `"Series - NN"`,
	"lead":          `"NN - Title"`,
	"volume":        `"Title N"`,
	"bare":          "the title alone",
}

var (
	// any spacing round the dashes, so a double space is still read as the
	// style it was meant to be
	styleNumbered     = regexp.MustCompile(`^(.*?\S)(\s+)[-–](\s+)(\d+(?:\.\d+)?)(\s+)[-–](\s+)(.+)$`)
	styleSeriesNumber = regexp.MustCompile(`^(.*?\S)(\s+)[-–](\s+)(\d+(?:\.\d+)?)$`)
	styleLead         = regexp.MustCompile(`^(\d+(?:\.\d+)?)(\s*)[-–.](\s+)(.+)$`)
	// "Laddertop 2": a title closing on a small number
	styleVolume = regexp.MustCompile(`^(.*\S)\s+(\d{1,2})$`)
	// "(Unabridged)" names no book, so a new folder name drops it
	editionMarker = regexp.MustCompile(`(?i)\s*[(\[](un)?abridged[)\]]`)
)

// readFolderStyle sorts a folder name into its style.
func readFolderStyle(folder string) folderStyle {
	if m := styleNumbered.FindStringSubmatch(folder); m != nil {
		return folderStyle{kind: "numbered", series: m[1], number: m[4], title: m[7], loose: m[2] != " " || m[3] != " " || m[5] != " " || m[6] != " "}
	}
	if m := styleSeriesNumber.FindStringSubmatch(folder); m != nil {
		return folderStyle{kind: "series_number", series: m[1], number: m[4], loose: m[2] != " " || m[3] != " "}
	}
	if m := styleLead.FindStringSubmatch(folder); m != nil {
		return folderStyle{kind: "lead", number: m[1], title: m[4], loose: m[2] != " " || m[3] != " "}
	}
	if m := styleVolume.FindStringSubmatch(folder); m != nil {
		return folderStyle{kind: "volume", number: m[2], title: m[1]}
	}
	return folderStyle{kind: "bare", title: folder}
}

// folderMember is one book of a series in a shared folder.
type folderMember struct {
	it     numberedItem
	folder string
	style  folderStyle
	key    string // the series key
	series string // the series as the book's metadata names it
	seq    string
}

// folderStyleFindings compares the folders of each series that share a
// parent folder. A series written one way throughout is left alone, whatever
// the way; one written in more than one is brought to the style the
// library's other series use most, and within that to a single spacing, one
// spelling of the series and numbers padded to its widest.
func (c *numberingCollector) folderStyleFindings() []numberingFinding {
	groups := map[string][]folderMember{} // series key + parent folder -> its books there
	var order []string
	for _, it := range c.items {
		dir := path.Dir(strings.Trim(it.RelPath, "/"))
		folder := path.Base(it.RelPath)
		// a single-file book is a file, not a folder of its own
		if dir == "." || path.Ext(folder) != "" && readFolderStyle(strings.TrimSuffix(folder, path.Ext(folder))).kind == "bare" {
			continue
		}
		for _, s := range it.Series {
			if strings.TrimSpace(s.Sequence) == "" {
				continue
			}
			k := seriesKey(s.Name)
			gk := k + "\x00" + dir
			if _, ok := groups[gk]; !ok {
				order = append(order, gk)
			}
			groups[gk] = append(groups[gk], folderMember{it: it, folder: folder, style: readFolderStyle(folder), key: k, series: s.Name, seq: strings.TrimSpace(s.Sequence)})
		}
	}

	// the library's style for a series: the one most books in a series of
	// two or more are written in, numbered winning a tie as the convention
	counts := map[string]int{}
	for _, gk := range order {
		if members := groups[gk]; len(members) > 1 {
			for _, m := range members {
				counts[m.style.kind]++
			}
		}
	}
	house := "numbered"
	for _, kind := range []string{"numbered", "series_number", "lead", "volume", "bare"} {
		if counts[kind] > counts[house] {
			house = kind
		}
	}
	// folders pad their numbers by habit, not by the metadata's rule: "- 01 -"
	// in a four-book series is the collector's own style, so a series' folders
	// are held to the width they mostly use, and one that shows none to the
	// width the library's folders mostly use
	houseWidth := commonWidth(order, groups, 2)

	var out []numberingFinding
	for _, gk := range order {
		members := groups[gk]
		if len(members) < 2 {
			continue
		}
		width := commonWidth([]string{gk}, groups, houseWidth)
		names := map[string]int{}
		kinds := map[string]bool{}
		for _, m := range members {
			kinds[m.style.kind] = true
			if m.style.series != "" {
				names[m.style.series]++
			}
		}
		// the spelling of the series most of its folders use, else the
		// metadata's
		name := members[0].series
		best := 0
		for n, count := range names {
			if count > best || count == best && n < name {
				name, best = n, count
			}
		}
		mixed := len(kinds) > 1 || len(names) > 1
		for _, m := range members {
			var problem string
			switch {
			case mixed && m.style.kind != house:
				problem = fmt.Sprintf("written as %s beside folders of the series written otherwise; the library's series are mostly %s", styleWords[m.style.kind], styleWords[house])
			case mixed && m.style.series != "" && m.style.series != name:
				problem = fmt.Sprintf("names the series %q where its other folders say %q", m.style.series, name)
			case m.style.loose:
				problem = "spaced unlike " + styleWords[m.style.kind] + " round its dashes"
			case m.style.number != "" && m.style.number != paddedNumber(m.style.number, width):
				problem = fmt.Sprintf("numbered %s where the series' other folders use %d digits", m.style.number, width)
			default:
				continue
			}
			style := m.style.kind
			if mixed {
				style = house
			}
			want := styledFolder(style, name, m.seq, width, bookTitle(&m))
			if want == "" || want == m.folder {
				continue
			}
			out = append(out, numberingFinding{
				ID: m.it.ID, Title: m.it.Title, Path: m.it.RelPath, Series: m.series, Sequence: m.seq,
				FolderNumber: m.style.number, Problem: "folder_style", Detail: problem,
				Suggest: path.Join(path.Dir(m.it.RelPath), want),
			})
		}
	}
	slices.SortFunc(out, func(a, b numberingFinding) int { return strings.Compare(a.Path, b.Path) })

	return out
}

// commonWidth is the digit count the numbers of the named groups' folders
// are most often written with, the wider on a tie, or fallback when none is.
func commonWidth(keys []string, groups map[string][]folderMember, fallback int) int {
	widths := map[int]int{}
	for _, k := range keys {
		for _, m := range groups[k] {
			if n := m.style.number; n != "" && !strings.Contains(n, ".") {
				widths[len(n)]++
			}
		}
	}
	best, count := fallback, 0
	for w, n := range widths {
		if n > count || n == count && w > best {
			best, count = w, n
		}
	}
	return best
}

// bookTitle is the book's own title for a new folder name: what follows the
// number where the folder writes one, else the title the book carries.
func bookTitle(m *folderMember) string {
	if (m.style.kind == "numbered" || m.style.kind == "lead") && strings.TrimSpace(m.style.title) != "" {
		return strings.TrimSpace(m.style.title)
	}
	t := strings.TrimSpace(m.it.Title)
	if t == "" {
		t = m.folder
	}
	return strings.TrimSpace(editionMarker.ReplaceAllString(t, ""))
}

// styledFolder writes a folder name in a style.
func styledFolder(style, series, seq string, width int, title string) string {
	number := paddedNumber(seq, width)
	switch style {
	case "numbered":
		return series + " - " + number + " - " + title
	case "series_number":
		return series + " - " + number
	case "lead":
		return number + " - " + title
	case "volume":
		return title + " " + strings.TrimLeft(number, "0")
	case "bare":
		return title
	}
	return ""
}

// paddedNumber zero-pads a whole number to width; a half number or one that
// is not a number is left as written.
func paddedNumber(number string, width int) string {
	f, ok := sequenceNumber(number)
	if !ok || f != float64(int(f)) {
		return strings.TrimSpace(number)
	}
	return fmt.Sprintf("%0*d", width, int(f))
}
