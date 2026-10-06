package tools

import (
	"cmp"
	"context"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/katbyte/abs-mcp/sdk/abs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// audit_whitespace: spaces where a name should not have them. A double space,
// a space at either end or before the extension, a space before a colon and
// a tab or a non-breaking space all look like one ordinary space on screen,
// and none of them is found by a search for the name as it reads. The zbooks
// sort (2026-09-24) found 36 folders with a double space, 929 audio files,
// 114 names ending in a space, most of them just before the extension, and
// one title.
//
// A double space in a folder or file name is often not a typo but the mark
// of a character a renamer dropped: a colon ("The Mandalorian and Grogu  A
// Special Look"), a censored word, a bar. So every row carries the item's
// own title beside the path, to say what the gap was.

var whitespaceProblemOrder = []string{"odd_space", "double_space", "edge_space", "space_before_extension", "space_before_colon"}

var whitespaceWhereOrder = []string{"title", "subtitle", "author", "narrator", "series", "folder", "file"}

// renamedOnDisk is the fix for a folder or a file: no tool here renames one.
const renamedOnDisk = "rename it on disk and rescan the library; no tool here renames files or folders. A double space often marks a character a renamer dropped, a colon or a censored word: check the name against the title before collapsing it"

var whitespaceFixes = map[string]string{
	"title":          "item_edit title= the suggested value",
	"subtitle":       "item_edit subtitle= the suggested value",
	"author":         "author_edit author= the record_id, name= the suggested name; renaming onto a name that already exists merges the two authors",
	"podcast_author": "item_edit podcast_author= the suggested name, on each podcast carrying it",
	"narrator":       "metadata_rename field=narrators from= the value exactly as given, spaces and all, to= the suggested name; renaming onto a name that already exists merges the two",
	"series":         "series_edit series= the record_id, name= the suggested name; where a series of that name already exists, series_merge from= the record_id into= that series moves the books instead",
	"folder":         renamedOnDisk,
	"file":           renamedOnDisk,
}

const (
	// byHand is added to a fix whose name comes to nothing once the spaces go
	byHand = "; nothing is left of the name once the spaces go but perhaps its extension, or what is left starts with a dot, which the server skips as a hidden file: name it by hand"
	// takenFix is added to a folder or file row whose suggestion is taken
	takenFix = "; where a suggested name is taken (see taken), a plain rename would overwrite that file or move the folder inside the other: merge the two by hand"
	// takenShow is how many taken names a row lists
	takenShow = 3
)

var (
	// wsRun is two or more spaces in a row
	wsRun = regexp.MustCompile(` {2,}`)
	// wsColon is a space before a colon or the look-alike U+A789 a filename
	// uses for one, "Part Two ꞉ Six" where the library writes "Title꞉ Subtitle"
	wsColon = regexp.MustCompile(` +([:꞉])`)
)

// oddSpace is a space that is not the ordinary one: a tab, a line break, a
// non-breaking or typographic space. The ideographic space, U+3000, is not
// one: Japanese titles use it as written.
func oddSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\r', '\v', '\f', 0x85, 0xA0, 0x1680, 0x2028, 0x2029, 0x202F, 0x205F:
		return true
	}
	return r >= 0x2000 && r <= 0x200A
}

// plainSpaces is a name with every odd space made an ordinary one: an odd
// space doubles, ends a name or stands before a colon as an ordinary one
// does, and is looked for that way.
func plainSpaces(name string) string {
	return strings.Map(func(r rune) rune {
		if oddSpace(r) {
			return ' '
		}
		return r
	}, name)
}

// splitExt parts a file name into its stem and its extension; a name whose
// last dot starts no plausible extension is all stem. The name is read
// without the spaces after it: "Chapter 1 .mp3 " is "Chapter 1 " and ".mp3".
func splitExt(name string) (stem, ext string) {
	name = strings.TrimRight(name, " ")
	ext = path.Ext(name)
	if ext == "" || ext == name || len(ext) > 6 || strings.Contains(ext, " ") {
		return name, ""
	}
	return strings.TrimSuffix(name, ext), ext
}

// whitespaceProblems is what is wrong with the spaces in one name, in
// whitespaceProblemOrder. A file's name is read as a stem and an extension.
func whitespaceProblems(name string, file bool) []string {
	var out []string
	plain := plainSpaces(name)
	if plain != name {
		out = append(out, "odd_space")
	}
	if strings.Contains(plain, "  ") {
		out = append(out, "double_space")
	}
	if strings.HasPrefix(plain, " ") || strings.HasSuffix(plain, " ") {
		out = append(out, "edge_space")
	}
	if file {
		if stem, ext := splitExt(plain); ext != "" && strings.HasSuffix(stem, " ") {
			out = append(out, "space_before_extension")
		}
	}
	if wsColon.MatchString(plain) {
		out = append(out, "space_before_colon")
	}
	return out
}

// whitespaceVisible writes a name with the offending spaces made visible: ␣
// for an ordinary space in a run, at an end, before a colon or before the
// extension, and [U+00A0] for a space that is not the ordinary one, wherever
// it is.
func whitespaceVisible(name string, file bool) string {
	runes := []rune(name)
	plain := []rune(plainSpaces(name))
	extAt := -1
	if file {
		if stem, ext := splitExt(string(plain)); ext != "" {
			extAt = len([]rune(stem))
		}
	}
	var b strings.Builder
	for i := 0; i < len(runes); {
		if plain[i] != ' ' {
			b.WriteRune(runes[i])
			i++
			continue
		}
		end := i
		for end < len(plain) && plain[end] == ' ' {
			end++
		}
		mark := end-i > 1 || i == 0 || end == len(plain) || end == extAt || plain[end] == ':' || plain[end] == '꞉'
		for ; i < end; i++ {
			switch {
			case oddSpace(runes[i]):
				fmt.Fprintf(&b, "[U+%04X]", runes[i])
			case mark:
				b.WriteRune('␣')
			default:
				b.WriteRune(' ')
			}
		}
	}
	return b.String()
}

// whitespaceFixed is the name with its spaces put right: an odd space made
// an ordinary one, runs made one, the ends and the space before a colon or
// the extension dropped. "" when nothing but spaces, or an extension, is
// left.
func whitespaceFixed(name string, file bool) string {
	stem, ext := plainSpaces(name), ""
	if file {
		stem, ext = splitExt(stem)
	}
	stem = strings.Trim(wsColon.ReplaceAllString(wsRun.ReplaceAllString(stem, " "), "$1"), " ")
	if stem == "" {
		return ""
	}
	return stem + ext
}

// diskFixed is whitespaceFixed for a folder or file name, "" too when what is
// left starts with a dot: the server skips a name that does, and " .hack"
// put right to ".hack" would hide the book. A title may start with one.
func diskFixed(name string, file bool) string {
	if fixed := whitespaceFixed(name, file); !strings.HasPrefix(fixed, ".") {
		return fixed
	}
	return ""
}

type whitespaceRow struct {
	Where    string   `json:"where"               jsonschema:"title, subtitle, author, narrator, series, folder or file"`
	Problem  string   `json:"problem"             jsonschema:"odd_space: a tab, line break or non-breaking or typographic space, the ideographic space aside; double_space: two or more spaces in a row; edge_space: a space at the start or the end; space_before_extension: a file name's stem ends in a space ('End Credits .m4b'); space_before_colon: a space before a colon or the look-alike ꞉ ('Part Two ꞉ Six'). An odd space counts as a space for the other four"`
	Text     string   `json:"text"                jsonschema:"the name or value with the offending spaces made visible: ␣ for each ordinary one, [U+00A0] for a space that is not the ordinary one. For a file row, the first file, by its path in the book's folder"`
	Suggest  string   `json:"suggest"             jsonschema:"the name with its spaces put right; empty when nothing is left of it but spaces and perhaps an extension, to name by hand. A double space in a folder or file name may mark a dropped character: check it against title"`
	Value    string   `json:"value,omitempty"     jsonschema:"author, narrator and series rows: the name exactly as stored, spaces and all. A narrator is fixed by it, from= as it is; an author or series by its record_id, as a one-book series a library hides is found by no name"`
	Record   string   `json:"record_id,omitempty" jsonschema:"author and series rows: the record's id, to pass to author_edit or series_edit; a name with a space at an end is best named by its id"`
	Items    int      `json:"items,omitempty"     jsonschema:"author, narrator and series rows: how many books carry the name; folder rows: how many items sit in the folder, reported once, with id, title and path the first"`
	Files    int      `json:"files,omitempty"     jsonschema:"file rows: how many of the book's files have this problem"`
	Examples []string `json:"examples,omitempty"  jsonschema:"file rows with more than one file: up to three of them, by their path in the book's folder, spaces made visible"`
	Taken    []string `json:"taken,omitempty"     jsonschema:"folder and file rows: names whose suggestion is already taken in the same folder, by a file or folder there or by another name's suggestion, as 'name → suggestion: why', up to three. A plain rename would overwrite or nest: merge by hand"`
	ID       string   `json:"id,omitempty"        jsonschema:"the item's id; on a folder row the first item in it; absent on author, narrator and series rows, which are records"`
	Title    string   `json:"title,omitempty"     jsonschema:"the item's own title, to set a folder or file name beside"`
	Path     string   `json:"path,omitempty"`
	Fix      string   `json:"fix"`

	// for the taken check at the end, not answered: the folder the name is
	// in and its own entry there, each keyed by the library folder, and the
	// name itself
	parent, entry, name string
}

type whitespaceCounts struct {
	OddSpace             int `json:"odd_space"`
	DoubleSpace          int `json:"double_space"`
	EdgeSpace            int `json:"edge_space"`
	SpaceBeforeExtension int `json:"space_before_extension"`
	SpaceBeforeColon     int `json:"space_before_colon"`
}

type whitespaceWhere struct {
	Title    int `json:"title"`
	Subtitle int `json:"subtitle"`
	Author   int `json:"author"`
	Narrator int `json:"narrator"`
	Series   int `json:"series"`
	Folder   int `json:"folder"`
	File     int `json:"file"`
}

type whitespaceOut struct {
	Scanned  int              `json:"items_scanned"`
	Read     int              `json:"files_read"     jsonschema:"books whose file lists were fetched, fifty to a request"`
	Found    int              `json:"total_findings" jsonschema:"rows before limit: a name or folder shared by many items is one row, and a book's files one row per problem"`
	Counts   whitespaceCounts `json:"counts"         jsonschema:"rows by problem"`
	ByWhere  whitespaceWhere  `json:"by_where"       jsonschema:"rows by where the name is"`
	Findings []whitespaceRow  `json:"findings"       jsonschema:"by where (title, subtitle, author, narrator, series, folder, file), then by problem"`
}

// whitespaceSweep gathers the names from a listing and the libraries' own
// author, series and narrator lists and, once fetched, the books' file
// names.
type whitespaceSweep struct {
	scanned, read int
	rows          []whitespaceRow
	shared        map[string]int             // a name or folder already reported -> its row
	entries       map[string]map[string]bool // a folder, keyed by its library folder -> the names in it
	pending       []string                   // books whose files are still to read
	libs          []string                   // book libraries seen since the last resolve, for their names
	seenLib       map[string]bool
	narrators     map[string]int  // a narrator already reported -> its first row, across libraries
	bookDirs      map[string]bool // folders inside books already checked
	// the series the listing names on each book, by library: a library
	// that hides one-book series leaves them out of its series list, and
	// the books are how they are found
	bookSeries map[string][]bookSeries
	// single-file books by the folder they sit in, and the name each is put
	// right to: two put right to one name would overwrite each other
	fileBecomes map[string]map[string]int
	// namesOnly reads the listing and the name lists alone: file names need
	// every book fetched whole, which audit_all does only with deep
	namesOnly bool
}

// seriesTextSpaced reports whether an item's series, as the listing gives
// them, show any space out of place.
func seriesTextSpaced(it *abs.Item) bool {
	m := &it.Media.Metadata
	text := m.SeriesName
	if len(m.Series) > 0 {
		parts := make([]string, 0, len(m.Series))
		for _, ref := range m.Series {
			parts = append(parts, ref.Name)
		}
		text = strings.Join(parts, ", ")
	}
	plain := plainSpaces(text)
	return plain != text || strings.Contains(plain, "  ") || strings.Contains(plain, " ,") ||
		strings.HasPrefix(plain, " ") || strings.HasSuffix(plain, " ") || wsColon.MatchString(plain)
}

// bookSeries is a book and the series names the listing gives it.
type bookSeries struct {
	id    string
	names []string
}

// entryIn records a name as present in a folder, for the taken check.
func (w *whitespaceSweep) entryIn(parent, name string) {
	if w.entries == nil {
		w.entries = map[string]map[string]bool{}
	}
	if w.entries[parent] == nil {
		w.entries[parent] = map[string]bool{}
	}
	w.entries[parent][name] = true
}

// check reports each problem with one name. A shared name, key set, is
// reported once however many items carry it.
func (w *whitespaceSweep) check(row whitespaceRow, name, key string) {
	for _, problem := range whitespaceProblems(name, false) {
		if key != "" {
			k := row.Where + "|" + problem + "|" + key
			if at, ok := w.shared[k]; ok {
				w.rows[at].Items++
				continue
			}
			if w.shared == nil {
				w.shared = map[string]int{}
			}
			w.shared[k] = len(w.rows)
		}
		r := row
		r.Problem, r.Text, r.Suggest = problem, whitespaceVisible(name, false), whitespaceFixed(name, false)
		if r.Where == "folder" {
			r.Suggest = diskFixed(name, false)
		}
		if r.Suggest == "" {
			r.Fix += byHand
		}
		if key != "" && r.Items == 0 {
			r.Items = 1
		}
		w.rows = append(w.rows, r)
	}
}

// add reads one item from a listing: its title and subtitle, a podcast's
// author and its folders; a book is held to read its files, and its library
// to read its names.
func (w *whitespaceSweep) add(it *abs.Item) {
	w.scanned++
	m := &it.Media.Metadata
	item := whitespaceRow{ID: it.ID, Title: it.Title(), Path: it.RelPath}
	for _, field := range [][2]string{{"title", m.Title}, {"subtitle", m.Subtitle}} {
		row := item
		row.Where, row.Fix = field[0], whitespaceFixes[field[0]]
		w.check(row, field[1], "")
	}
	if it.IsPodcast() {
		// a podcast's author is text on the item, with no record behind it
		row := item
		row.Where, row.Fix = "author", whitespaceFixes["podcast_author"]
		w.check(row, m.Author, "podcast|"+it.LibraryID+"|"+m.Author)
	} else {
		if !w.seenLib[it.LibraryID] {
			if w.seenLib == nil {
				w.seenLib = map[string]bool{}
			}
			w.seenLib[it.LibraryID] = true
			w.libs = append(w.libs, it.LibraryID)
		}
		// only a book whose series, as the listing joins them, show a space
		// out of place can be in a hidden series with one: a double space,
		// a space at an end or before a comma or colon, an odd space all
		// show in the joined text, and the rest need no fetch
		if refs := seriesRefsOf(it); len(refs) > 0 && seriesTextSpaced(it) {
			names := make([]string, 0, len(refs))
			for _, ref := range refs {
				names = append(names, ref.Name)
			}
			if w.bookSeries == nil {
				w.bookSeries = map[string][]bookSeries{}
			}
			w.bookSeries[it.LibraryID] = append(w.bookSeries[it.LibraryID], bookSeries{it.ID, names})
		}
	}
	// a record whose folder is gone names nothing on disk
	if it.IsMissing {
		return
	}
	segments := strings.Split(strings.Trim(it.RelPath, "/"), "/")
	if it.IsFile {
		// the last is the book's one file, beside the folders
		parent, name := it.FolderID+"|"+strings.Join(segments[:len(segments)-1], "/"), segments[len(segments)-1]
		w.entryIn(parent, name)
		if fixed := diskFixed(name, true); fixed != "" && fixed != name {
			if w.fileBecomes == nil {
				w.fileBecomes = map[string]map[string]int{}
			}
			if w.fileBecomes[parent] == nil {
				w.fileBecomes[parent] = map[string]int{}
			}
			w.fileBecomes[parent][fixed]++
		}
		segments = segments[:len(segments)-1]
	}
	for i, folder := range segments {
		parent := it.FolderID + "|" + strings.Join(segments[:i], "/")
		entry := it.FolderID + "|" + strings.Join(segments[:i+1], "/")
		w.entryIn(parent, folder)
		row := item
		row.Where, row.Fix, row.parent, row.entry, row.name = "folder", whitespaceFixes["folder"], parent, entry, folder
		row.Path = strings.Join(segments[:i+1], "/")
		w.check(row, folder, entry)
	}
	if !w.namesOnly && !it.IsPodcast() {
		w.pending = append(w.pending, it.ID)
	}
}

// resolve reads the names of the libraries seen since the last call from
// their own lists, then fetches the held books whole, a batch at a time,
// and reads their file names: one row per book per problem.
func (w *whitespaceSweep) resolve(ctx context.Context, client *abs.Client) error {
	// the books first: fetched whole for their files, they carry their
	// series as the record has them, which the names then need no second
	// fetch for
	fetched := map[string][]abs.SeriesRef{}
	pending := w.pending
	w.pending = nil
	for chunk := range slices.Chunk(pending, embedBatchSize) {
		items, err := client.ItemsBatch(ctx, chunk)
		if err != nil {
			return err
		}
		for j := range items {
			w.read++
			w.files(&items[j])
			fetched[items[j].ID] = items[j].Media.Metadata.Series
		}
	}
	libs := w.libs
	w.libs = nil
	for _, lib := range libs {
		if err := w.names(ctx, client, lib, fetched); err != nil {
			return err
		}
	}
	return nil
}

// names reads a book library's authors, series and narrators from its own
// lists: the listing joins a book's names with ", ", and cannot tell the
// comma a name holds ("Chronicles of Amber, The", "Jane Doe, Ph.D.") from the
// one between two names. An author or series is its record, reported with
// its id; a narrator is only a name, and is reported once across libraries.
func (w *whitespaceSweep) names(ctx context.Context, client *abs.Client, libraryID string, fetched map[string][]abs.SeriesRef) error {
	authors, err := allAuthors(ctx, client, libraryID, abs.ListOptions{})
	if err != nil {
		return fmt.Errorf("reading the authors: %w", err)
	}
	for _, a := range authors {
		w.check(whitespaceRow{Where: "author", Value: a.Name, Record: a.ID, Items: a.NumBooks, Fix: whitespaceFixes["author"]}, a.Name, "")
	}
	series, err := allSeries(ctx, client, libraryID)
	if err != nil {
		return fmt.Errorf("reading the series: %w", err)
	}
	listed := map[string]bool{}  // by series id
	inListed := map[string]int{} // book id -> how many listed series hold it
	for _, s := range series {
		listed[s.ID] = true
		for i := range s.Books {
			inListed[s.Books[i].ID]++
		}
		w.check(whitespaceRow{Where: "series", Value: s.Name, Record: s.ID, Items: len(s.Books), Fix: whitespaceFixes["series"]}, s.Name, "")
	}
	// a series the list left out - every one-book series, in a library that
	// hides them - is read from its books: a book the listing gives more
	// series than listed series hold it is read whole, for the series as
	// the record has it. By the book, not the name: the listing trims its
	// names, so a hidden "Mistborn " beside a listed "Mistborn" reads as it,
	// and its joined names cannot be trusted with a comma
	var unlisted []string
	for _, b := range w.bookSeries[libraryID] {
		if len(b.names) > inListed[b.id] {
			unlisted = append(unlisted, b.id)
		}
	}
	delete(w.bookSeries, libraryID)
	refsOf := map[string][]abs.SeriesRef{}
	var toFetch []string
	for _, id := range unlisted {
		if refs, ok := fetched[id]; ok {
			refsOf[id] = refs
		} else {
			toFetch = append(toFetch, id)
		}
	}
	for chunk := range slices.Chunk(toFetch, embedBatchSize) {
		items, ferr := client.ItemsBatch(ctx, chunk)
		if ferr != nil {
			return fmt.Errorf("reading the series of books the series list leaves out: %w", ferr)
		}
		for j := range items {
			refsOf[items[j].ID] = items[j].Media.Metadata.Series
		}
	}
	for _, id := range unlisted {
		for _, ref := range refsOf[id] {
			if ref.ID == "" || listed[ref.ID] {
				continue
			}
			w.check(whitespaceRow{Where: "series", Value: ref.Name, Record: ref.ID, Fix: whitespaceFixes["series"]}, ref.Name, "series|"+ref.ID)
		}
	}
	narrators, err := client.Narrators(ctx, libraryID)
	if err != nil {
		return fmt.Errorf("reading the narrators: %w", err)
	}
	for _, n := range narrators {
		if len(whitespaceProblems(n.Name, false)) == 0 {
			continue
		}
		// the same name in another library is the same fix: metadata_rename
		// without a library renames it in every one
		if at, ok := w.narrators[n.Name]; ok {
			for i := at; i < len(w.rows) && w.rows[i].Where == "narrator" && w.rows[i].Value == n.Name; i++ {
				w.rows[i].Items += n.NumBooks
			}
			continue
		}
		if w.narrators == nil {
			w.narrators = map[string]int{}
		}
		w.narrators[n.Name] = len(w.rows)
		w.check(whitespaceRow{Where: "narrator", Value: n.Name, Items: n.NumBooks, Fix: whitespaceFixes["narrator"]}, n.Name, "")
	}
	return nil
}

// files reads one whole book's file names, the audio and the rest, each by
// its path in the book's folder: a folder inside the book, a disc's, is
// checked as a folder, and a file's suggestion is taken when another file
// or folder beside it already has that name or another file there would be
// put right to it too.
func (w *whitespaceSweep) files(it *abs.Item) {
	var rels []string
	for _, f := range it.LibraryFiles {
		rels = append(rels, cmp.Or(f.Metadata.RelPath, f.Metadata.Filename))
	}
	if len(rels) == 0 {
		for _, f := range it.Media.AudioFiles {
			rels = append(rels, cmp.Or(f.Metadata.RelPath, f.Metadata.Filename))
		}
	}
	slices.Sort(rels)
	rels = slices.Compact(rels) // a path listed twice is one file
	// a single-file book's one file is the item itself, beside the folders
	base := it.RelPath
	if it.IsFile {
		base = path.Dir(it.RelPath)
		if base == "." {
			base = ""
		}
	}
	var inDir map[string]map[string]bool // a folder in the book -> the names in it
	put := func(dir, name string) {
		if inDir == nil {
			inDir = map[string]map[string]bool{}
		}
		if inDir[dir] == nil {
			inDir[dir] = map[string]bool{}
		}
		inDir[dir][name] = true
	}
	// what already sits beside a name, the book's own folder or, for a
	// single-file book, the library folder it is in
	beside := func(dir, name string) bool {
		if it.IsFile && dir == "." {
			return w.entries[it.FolderID+"|"+base][name]
		}
		return inDir[dir][name]
	}
	for _, rel := range rels {
		dir := path.Dir(rel)
		for d := dir; d != "." && d != "/"; d = path.Dir(d) {
			put(path.Dir(d), path.Base(d))
			// a folder inside the book, a disc's: checked as a folder, once
			entry := it.FolderID + "|" + path.Join(base, d)
			if w.bookDirs[entry] {
				continue
			}
			if w.bookDirs == nil {
				w.bookDirs = map[string]bool{}
			}
			w.bookDirs[entry] = true
			parent := it.FolderID + "|" + path.Join(base, path.Dir(d))
			w.entryIn(parent, path.Base(d))
			row := whitespaceRow{
				Where: "folder", ID: it.ID, Title: it.Title(), Path: path.Join(base, d), Fix: whitespaceFixes["folder"],
				parent: parent, entry: entry, name: path.Base(d),
			}
			w.check(row, path.Base(d), entry)
		}
		put(dir, path.Base(rel))
	}
	// the suggestions each folder in the book would take, for two names
	// put right to the same one
	becomes := map[string]map[string][]string{} // dir -> suggestion -> the names
	for _, rel := range rels {
		dir, name := path.Dir(rel), path.Base(rel)
		if fixed := diskFixed(name, true); fixed != "" && fixed != name {
			if becomes[dir] == nil {
				becomes[dir] = map[string][]string{}
			}
			becomes[dir][fixed] = append(becomes[dir][fixed], name)
		}
	}
	shown := func(rel string) string {
		dir, name := path.Dir(rel), path.Base(rel)
		if dir == "." {
			return whitespaceVisible(name, true)
		}
		parts := strings.Split(dir, "/")
		for i := range parts {
			parts[i] = whitespaceVisible(parts[i], false)
		}
		return strings.Join(parts, "/") + "/" + whitespaceVisible(name, true)
	}
	byProblem := map[string]*whitespaceRow{}
	for _, rel := range rels {
		dir, name := path.Dir(rel), path.Base(rel)
		problems := whitespaceProblems(name, true)
		if len(problems) == 0 {
			continue
		}
		fixed := diskFixed(name, true)
		var taken string
		switch {
		case fixed == "":
		case beside(dir, fixed):
			taken = fmt.Sprintf("%s → %s: a file or folder of that name is already there", shown(rel), fixed)
		case len(becomes[dir][fixed]) > 1:
			taken = fmt.Sprintf("%s → %s: another file there is put right to that name too", shown(rel), fixed)
		case it.IsFile && dir == "." && w.fileBecomes[it.FolderID+"|"+base][fixed] > 1:
			taken = fmt.Sprintf("%s → %s: another book beside it is put right to that name too", shown(rel), fixed)
		}
		for _, problem := range problems {
			row := byProblem[problem]
			if row == nil {
				row = &whitespaceRow{
					Where: "file", Problem: problem, Text: shown(rel), Suggest: fixed,
					ID: it.ID, Title: it.Title(), Path: it.RelPath, Fix: whitespaceFixes["file"],
				}
				if fixed == "" {
					row.Fix += byHand
				}
				byProblem[problem] = row
			}
			row.Files++
			if len(row.Examples) < 3 {
				row.Examples = append(row.Examples, shown(rel))
			}
			if taken != "" && len(row.Taken) < takenShow {
				if len(row.Taken) == 0 {
					row.Fix += takenFix
				}
				row.Taken = append(row.Taken, taken)
			}
		}
	}
	for _, problem := range whitespaceProblemOrder {
		if row := byProblem[problem]; row != nil {
			if row.Files == 1 {
				row.Examples = nil
			}
			w.rows = append(w.rows, *row)
		}
	}
}

// library sweeps one library's listing, then reads its names and its books'
// files.
func (w *whitespaceSweep) library(ctx context.Context, client *abs.Client, lib *abs.Library) error {
	if err := client.ItemsAll(ctx, lib.ID, abs.ItemsOptions{}, func(items []abs.Item) bool {
		for j := range items {
			w.add(&items[j])
		}
		return true
	}); err != nil {
		return err
	}
	return w.resolve(ctx, client)
}

// takenFolders marks each folder row whose suggestion is taken: another
// folder or a file of that name already beside it, or another folder beside
// it put right to the same name.
func (w *whitespaceSweep) takenFolders(rows []whitespaceRow) {
	becomes := map[string]map[string]bool{} // parent + suggestion -> the entries put right to it
	for _, row := range rows {
		if row.Where != "folder" || row.Suggest == "" {
			continue
		}
		k := row.parent + "\x00" + row.Suggest
		if becomes[k] == nil {
			becomes[k] = map[string]bool{}
		}
		becomes[k][row.entry] = true
	}
	for i := range rows {
		row := &rows[i]
		if row.Where != "folder" || row.Suggest == "" {
			continue
		}
		var why string
		switch {
		case row.Suggest != row.name && w.entries[row.parent][row.Suggest]:
			why = "a folder or file of that name is already there"
		case len(becomes[row.parent+"\x00"+row.Suggest]) > 1:
			why = "another folder there is put right to that name too"
		default:
			continue
		}
		row.Taken = []string{fmt.Sprintf("%s → %s: %s", row.Text, row.Suggest, why)}
		row.Fix += takenFix
	}
}

// report is what was found, by where and problem, keeping at most limit rows.
func (w *whitespaceSweep) report(limit int) whitespaceOut {
	out := whitespaceOut{Scanned: w.scanned, Read: w.read, Found: len(w.rows), Findings: []whitespaceRow{}}
	rows := slices.Clone(w.rows)
	w.takenFolders(rows)
	slices.SortStableFunc(rows, func(a, b whitespaceRow) int {
		return cmp.Or(
			cmp.Compare(slices.Index(whitespaceWhereOrder, a.Where), slices.Index(whitespaceWhereOrder, b.Where)),
			cmp.Compare(slices.Index(whitespaceProblemOrder, a.Problem), slices.Index(whitespaceProblemOrder, b.Problem)),
			cmp.Compare(a.Path, b.Path),
			cmp.Compare(a.Text, b.Text),
		)
	})
	for _, row := range rows {
		switch row.Problem {
		case "odd_space":
			out.Counts.OddSpace++
		case "double_space":
			out.Counts.DoubleSpace++
		case "edge_space":
			out.Counts.EdgeSpace++
		case "space_before_extension":
			out.Counts.SpaceBeforeExtension++
		case "space_before_colon":
			out.Counts.SpaceBeforeColon++
		}
		switch row.Where {
		case "title":
			out.ByWhere.Title++
		case "subtitle":
			out.ByWhere.Subtitle++
		case "author":
			out.ByWhere.Author++
		case "narrator":
			out.ByWhere.Narrator++
		case "series":
			out.ByWhere.Series++
		case "folder":
			out.ByWhere.Folder++
		case "file":
			out.ByWhere.File++
		}
	}
	out.Findings = append(out.Findings, rows[:min(len(rows), limit)]...)
	return out
}

func registerWhitespaceAudit(r *registry) {
	client := r.client

	add(r, readTool, &mcp.Tool{
		Name: "audit_whitespace",
		Description: "Find spaces where a name should not have them, in the title and subtitle, the author, narrator and series names (read from the library's own lists, so a comma in a name is kept whole, and a one-book series such a list hides from its books; a podcast's author from the podcast), every folder of an item's path and every folder and file in a book's folder, disc folders too (the audio, covers, .cue, .nfo). " +
			"double_space: two or more spaces in a row. edge_space: a space at the start or the end. space_before_extension: a file name's stem ends in a space, 'End Credits .m4b'. space_before_colon: a space before a colon or the look-alike ꞉ ('Part Two ꞉ Six', where the library writes 'Title꞉ Subtitle'). odd_space: a tab, a line break, a non-breaking or typographic space, which counts as a space for the other four; the ideographic space U+3000 is left alone, as Japanese titles use it. " +
			"Each row is one problem with one name, so a name with two problems is two rows. It shows the name with the offending spaces made visible (␣, and an odd space as its code point, [U+00A0]) and the name put right, or no suggestion when nothing is left of it but spaces and perhaps an extension, or what is left starts with a dot, which the server skips. A folder many items share is one row a problem with how many sit in it; an author or series is its record, with its id and book count, and a narrator its name, with value holding it exactly as stored; a book's files are one row per problem, with how many files and up to three examples by their path in the book. " +
			"A folder or file row whose suggestion is already taken beside it - a file or folder of that name, or another name put right to the same one - lists it under taken: a plain rename would overwrite or nest, so merge by hand. " +
			"A double space in a folder or file name often marks a character a renamer dropped - a colon ('The Mandalorian and Grogu  A Special Look'), a censored word - so each row carries the item's own title: check the name against it before collapsing the gap. A record whose folder is gone gives no folder or file rows. " +
			"Fix titles and subtitles with item_edit, a podcast's author with item_edit podcast_author, an author with author_edit and a series with series_edit, each by its record_id, and a narrator with metadata_rename from= the value as stored; folders and files are renamed on disk, and no tool here does that. " +
			"The listing carries no file names, so every book is fetched, fifty to a request.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in auditIn) (*mcp.CallToolResult, whitespaceOut, error) {
		libs, err := resolveLibraries(ctx, client, in.Library)
		if err != nil {
			return nil, whitespaceOut{}, err
		}
		var sweep whitespaceSweep
		for i := range libs {
			if err := sweep.library(ctx, client, &libs[i]); err != nil {
				return nil, whitespaceOut{}, err
			}
		}

		return nil, sweep.report(auditLimit(in.Limit, 100)), nil
	})
}
