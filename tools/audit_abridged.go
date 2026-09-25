package tools

import (
	"cmp"
	"context"
	"fmt"
	"math"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/katbyte/abs-mcp/lib/abs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// audit_abridged: books that are probably abridged and not marked so. An
// abridged recording filed as the book gives half the story, and nothing in
// the library says so: the flag is false, the folder and title are silent,
// and a match by title carries the unabridged edition's description. Two
// checks find them. The store's editions of the book, searched by title and
// author: a book the length of an edition the store marks abridged, or far
// shorter than every unabridged one. And two readings of one book in the
// library, compared chapter by chapter: two unabridged readings keep a steady
// ratio, the readers' pace, where an abridgement cuts some chapters far more
// than others. The store check is a provider request per book, so it works a
// window at a time and audit_all runs it only with deep.

const (
	// abridgedTolerance is how close a book must be to an abridged edition's
	// length to be that edition: wider than a match's 3%, since Enchantment
	// read by Alyssa Bresnahan is 3.2% off Audible's abridged 377 minutes.
	// An unabridged edition inside it clears the book whatever else is there.
	abridgedTolerance = 0.05
	// abridgedShare is the share of the shortest unabridged edition under
	// which a book is too short to be a whole reading. The abridged copies
	// found came in at 37 to 49% (Enchantment 6.5h of 17.4h, Heartfire 5.95h
	// of 12.1h, The Shock Doctrine 9h of 22.3h); a fast reader against a
	// slow one at 77% (Destroyer of Worlds, 10.95h against 14.2h).
	abridgedShare = 0.6
	// abridgedMinSeconds: an item this short is a sample or a stub, not a
	// reading of a book, and asking the store about it only says it is
	// shorter than the book
	abridgedMinSeconds = 600
	// abridgedSpread is the chapter spread over which the shorter of two
	// readings was cut: 1.05 to 1.19 on about 45 unabridged pairs, 1.46 for
	// The Gods Themselves read abridged by Morgan against Brick.
	abridgedSpread = 1.35
	// a spread needs this many chapters in common, each over a minute on
	// both sides, covering at least this share of each reading: a handful
	// of them says nothing about the rest
	spreadMinChapters = 5
	spreadMinSeconds  = 60.0
	spreadMinCover    = 0.5
	// spreadMinCut is how much shorter one reading must be than the other to
	// be read as cut rather than read faster: Morgan's abridged Gods
	// Themselves is 32% shorter than Brick's, while two copies a few percent
	// apart are one recording, or two paces, split into sections two ways
	spreadMinCut = 0.10
	// spreadMaxDrift is how far the middle chapter's ratio may sit from the
	// ratio of the two whole readings before the chapters are taken not to
	// line up: two releases that cut the book at different points, under
	// the same names, compare different stretches of text. World of Ptavvs,
	// 6.83h against 5.92h (1.15), gave a middle ratio of 2.60; the real cut,
	// Gods Themselves, 1.38 against 1.44
	spreadMaxDrift = 1.3
)

var (
	// abridgedWord is abridged and not unabridged, which has no word
	// boundary before the a
	abridgedWord = regexp.MustCompile(`(?i)\babridged\b`)
	// a dramatisation is shorter than the book by design; it is not an
	// abridgement to flag
	dramatisedWord = regexp.MustCompile(`(?i)\bdramati[sz]`)
	// chapterNumber is the "12: " or "03 - " a chapter title opens with
	chapterNumber = regexp.MustCompile(`^\d+\s*[:.-]?\s*`)
	// readingNote is a parenthetical naming no number, "(Nana Visitor)" or
	// "(Unabridged)", which a second reading's folder adds to the title; one
	// with a number, "(Part 2)", is another book
	readingNote = regexp.MustCompile(`\s*[(\[{][^)\]}0-9]*[)\]}]`)
)

// splitWords name a piece of a release rather than of the book. Two readings
// split into discs share "Disc 3" without sharing what is on it; "Chapter 3"
// is the book's own.
var splitWords = map[string]bool{"track": true, "disc": true, "disk": true, "cd": true, "part": true, "side": true, "tape": true, "file": true, "of": true}

var abridgedFixes = map[string]string{
	"abridged_length": "item_edit abridged=true; the edition's asin is this recording, for item_match_apply",
	"far_shorter":     "item_edit abridged=true once it is confirmed cut: a book split into parts, or a copy with files missing, is far shorter too",
	"uneven_chapters": "item_edit abridged=true",
}

// abridgedProblemOrder is the order the rows come in, surest first.
var abridgedProblemOrder = []string{"abridged_length", "uneven_chapters", "far_shorter"}

type storeEdition struct {
	ASIN     string `json:"asin,omitempty"`
	Title    string `json:"title"`
	Narrator string `json:"narrator,omitempty"`
	Duration int    `json:"duration_s"`
	Abridged bool   `json:"abridged"           jsonschema:"whether the store calls it abridged, by its format or its title"`
	Provider string `json:"provider"`
}

type abridgedReading struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Path     string `json:"path,omitempty"`
	Duration int    `json:"duration_s"`
}

type abridgedFinding struct {
	ID       string           `json:"id"`
	Title    string           `json:"title"`
	Author   string           `json:"author,omitempty"`
	Path     string           `json:"path,omitempty"`
	Duration int              `json:"duration_s"`
	Problem  string           `json:"problem"                     jsonschema:"abridged_length: within 5% of an edition the store marks abridged, and of no unabridged one; far_shorter: under 60% of the shortest unabridged edition, further than a reader's pace goes; uneven_chapters: against another reading of the book, its chapters are cut unevenly"`
	Detail   string           `json:"detail"`
	Edition  *storeEdition    `json:"edition,omitempty"           jsonschema:"abridged_length and far_shorter: the store's edition the book was measured against"`
	Reading  *abridgedReading `json:"other_reading,omitempty"     jsonschema:"uneven_chapters: the longer reading it was compared with, the one it differs from most"`
	Spread   float64          `json:"spread,omitempty"            jsonschema:"uneven_chapters: the 90th percentile of the per-chapter length ratios over the 10th; two unabridged readings kept 1.05 to 1.19, and over 1.35 is reported"`
	Chapters int              `json:"chapters_compared,omitempty" jsonschema:"uneven_chapters: chapters matched by title, each over a minute in both readings"`
	Fix      string           `json:"fix"`
}

type abridgedCounts struct {
	AbridgedLength int `json:"abridged_length"`
	FarShorter     int `json:"far_shorter"`
	UnevenChapters int `json:"uneven_chapters"`
}

type abridgedOut struct {
	Scanned    int               `json:"items_scanned"         jsonschema:"books in this call's window, abridged or not"`
	Searched   int               `json:"store_checked"         jsonschema:"books in the window searched at the store; the rest are marked or say abridged or dramatised, are under ten minutes, or carry the provider tag's none"`
	Compared   int               `json:"readings_compared"     jsonschema:"pairs of readings of one book whose chapters could be compared, at offset 0 only"`
	Found      int               `json:"total_findings"`
	Counts     abridgedCounts    `json:"counts"`
	Findings   []abridgedFinding `json:"findings"              jsonschema:"surest first: abridged_length, uneven_chapters, far_shorter. A book can be a row under two problems"`
	NextOffset int               `json:"next_offset,omitempty" jsonschema:"pass back as offset to search the next books; absent when every one has been searched"`
}

// add records a finding.
func (o *abridgedOut) add(f abridgedFinding) {
	f.Fix = abridgedFixes[f.Problem]
	switch f.Problem {
	case "abridged_length":
		o.Counts.AbridgedLength++
	case "far_shorter":
		o.Counts.FarShorter++
	case "uneven_chapters":
		o.Counts.UnevenChapters++
	}
	o.Found++
	o.Findings = append(o.Findings, f)
}

// saysAbridged reports whether a book already says what this audit would:
// the flag, or abridged in its title or its own folder or file name. Only the
// book's own name counts: a shelf folder called "Abridged" says nothing about
// a book filed under it by mistake. A dramatisation is left out with them.
func saysAbridged(it *abs.Item) bool {
	m := &it.Media.Metadata
	text := m.Title + " " + m.Subtitle + " " + path.Base(strings.Trim(it.RelPath, "/"))
	return m.Abridged || abridgedWord.MatchString(text) || dramatisedWord.MatchString(text)
}

// abridgedLimitMax is the most books one call searches the store for.
const abridgedLimitMax = 50

// abridgedRefusal refuses a library whose stores cannot say how long an
// edition is or whether it is abridged: only the Audible stores do, and a new
// library is on google.
func abridgedRefusal(prov providerConfig, named []string, lib *abs.Library) error {
	if lib.IsPodcast() {
		return nil
	}
	if len(named) == 0 && len(prov.providers) == 0 {
		if isAudible(lib.Provider) {
			return nil
		}
		on := "the " + lib.Provider + " provider"
		if lib.Provider == "" {
			on = "the server's default provider"
		}
		return fmt.Errorf("library %q is on %s, which gives no edition lengths and does not say which are abridged: pass providers, or start the server with --providers (ABS_PROVIDERS), e.g. audible.ca,audible", lib.Name, on)
	}
	for _, p := range prov.providersFor(named, lib) {
		if !isAudible(p) {
			return fmt.Errorf("%s gives no edition lengths and does not say which are abridged; only an Audible store does, e.g. audible.ca,audible", p)
		}
	}
	return nil
}

// storeTitled is the book as the store would title it: a second reading's
// folder names its reader, "Heartfire (Nana Visitor)", and the store knows
// the book as Heartfire.
func storeTitled(it *abs.Item) *abs.Item {
	probe := *it
	if title := strings.TrimSpace(readingNote.ReplaceAllString(it.Title(), " ")); title != "" {
		probe.Media.Metadata.Title = title
	}
	return &probe
}

// judgeEditions compares a book with the store's editions of it: the hits
// with the same title and author and a length. It says how many there were,
// so a store with none can be passed over for the next.
func judgeEditions(it *abs.Item, results []abs.BookSearchResult, provider string) (row *abridgedFinding, editions int) {
	book, probe := it.Media.Duration, storeTitled(it)
	var near, shortest *abs.BookSearchResult // the closest abridged edition in tolerance; the shortest unabridged
	nearDelta, unabridgedNear := math.Inf(1), false
	for i := range results {
		r := &results[i]
		if r.Duration <= 0 {
			continue
		}
		s := scoreMatch(probe, r, abridgedTolerance)
		if s.TitleLevel == 0 || !s.AuthorOK {
			continue
		}
		editions++
		abridged := abridgedEdition(r)
		switch delta := math.Abs(s.DurationDelta); {
		case delta <= abridgedTolerance && !abridged:
			unabridgedNear = true
		case delta <= abridgedTolerance && delta < nearDelta:
			near, nearDelta = r, delta
		}
		if !abridged && (shortest == nil || r.Duration < shortest.Duration) {
			shortest = r
		}
	}
	edition := func(r *abs.BookSearchResult) *storeEdition {
		return &storeEdition{ASIN: r.ASIN, Title: r.Title, Narrator: r.Narrator, Duration: wholeSec(r.Duration * 60), Abridged: abridgedEdition(r), Provider: provider}
	}
	f := abridgedFinding{ID: it.ID, Title: it.Title(), Author: it.Media.Metadata.AuthorDisplay(), Path: it.RelPath, Duration: wholeSec(book)}
	switch {
	case unabridgedNear:
		return nil, editions // the length of an unabridged edition: it is that one
	case near != nil:
		f.Problem, f.Edition = "abridged_length", edition(near)
		f.Detail = fmt.Sprintf("%s, the length of the abridged edition %s at %s (%s%s)", fmtDuration(book), near.ASIN, provider, fmtDuration(near.Duration*60), readBy(near.Narrator))
		if shortest != nil {
			f.Detail += "; the unabridged runs " + fmtDuration(shortest.Duration*60)
		}
	case shortest != nil && book < abridgedShare*shortest.Duration*60:
		f.Problem, f.Edition = "far_shorter", edition(shortest)
		f.Detail = fmt.Sprintf("%s, %d%% of the shortest unabridged edition %s at %s (%s%s): under %d%%, further than a reader's pace goes",
			fmtDuration(book), percent(book/(shortest.Duration*60)), shortest.ASIN, provider, fmtDuration(shortest.Duration*60), readBy(shortest.Narrator), percent(abridgedShare))
	default:
		return nil, editions
	}
	return &f, editions
}

// abridgedEdition reports whether the store says an edition is abridged: its
// format, or its title.
func abridgedEdition(r *abs.BookSearchResult) bool {
	return r.Abridged || abridgedWord.MatchString(r.Title+" "+r.Subtitle)
}

// readBy is ", read by X", or nothing when the narrator is not known.
func readBy(narrator string) string {
	if narrator = strings.TrimSpace(narrator); narrator == "" {
		return ""
	}
	return ", read by " + narrator
}

// checkAbridgedStore searches the stores for one book, the one it was
// matched from first, until one has editions of it with a length, and adds a
// finding when the book's length gives it away.
func checkAbridgedStore(ctx context.Context, client *abs.Client, it *abs.Item, providers []string, out *abridgedOut) error {
	for _, provider := range providers {
		if !isAudible(provider) {
			continue // a provider tag can name a store with no lengths
		}
		results, err := searchForItem(ctx, client, storeTitled(it), provider)
		if err != nil {
			return err
		}
		row, editions := judgeEditions(it, results, provider)
		if editions == 0 {
			continue
		}
		if row != nil {
			out.add(*row)
		}
		return nil
	}
	return nil
}

// abridgedScope is what a store sweep covers.
type abridgedScope struct {
	Providers []string
	Limit     int    // books to search; 0: every one, as audit_all does
	Offset    int    // books to pass over first
	Filter    string // a built library_items filter; empty is every book

	// seen counts the books walked so far, across every library a call
	// sweeps, so a window runs on from one library into the next
	seen *int
}

// sweepAbridged runs both checks over the whole of one library, as audit_all
// does.
func sweepAbridged(ctx context.Context, client *abs.Client, prov providerConfig, lib *abs.Library, out *abridgedOut) error {
	if err := sweepReadings(ctx, client, lib, "", out); err != nil {
		return err
	}
	_, err := sweepAbridgedStore(ctx, client, prov, lib, abridgedScope{}, out)
	return err
}

// sweepAbridgedStore searches a window of a library's books at the store.
// more says a book lies past the window.
func sweepAbridgedStore(ctx context.Context, client *abs.Client, prov providerConfig, lib *abs.Library, scope abridgedScope, out *abridgedOut) (more bool, err error) {
	if lib.IsPodcast() {
		return false, nil
	}
	if err := prov.checkProviders(ctx, client, scope.Providers, false); err != nil {
		return false, err
	}
	providers := prov.providersFor(scope.Providers, lib)
	// every book counts toward the window, searched or not, so marking one
	// abridged between calls moves no book across the offset
	return matchedWindow(ctx, client, lib.ID, bookWindow{Filter: scope.Filter, Matched: func(*abs.Item) bool { return true }, Offset: scope.Offset, Limit: scope.Limit, seen: scope.seen}, func(it *abs.Item) error {
		out.Scanned++
		if saysAbridged(it) || it.Media.Duration < abridgedMinSeconds || prov.markedUnmatchable(it) {
			return nil
		}
		out.Searched++
		return checkAbridgedStore(ctx, client, it, prov.providerOrder(it, providers), out)
	})
}

// readingKey files a book under its title and first author, loosely: a
// second reading's folder often adds the reader, "Heartfire (Nana Visitor)".
func readingKey(it *abs.Item) string {
	title := cleanTitle(readingNote.ReplaceAllString(it.Title(), " "))
	if title == "" {
		return ""
	}
	author := ""
	if names := splitNames(it.Media.Metadata.AuthorDisplay()); len(names) > 0 {
		author = norm(names[0])
	}
	return title + "|" + author
}

// sweepReadings compares every pair of readings of one book in a library
// chapter by chapter and reports the shorter of each pair cut unevenly, once,
// against the reading it differs from most. The listing carries a chapter
// count and not the chapters, so the books held more than once are fetched
// whole, fifty to a request; nothing is asked of a store.
func sweepReadings(ctx context.Context, client *abs.Client, lib *abs.Library, filter string, out *abridgedOut) error {
	if lib.IsPodcast() {
		return nil
	}
	groups := map[string][]string{}
	var keys []string
	if err := client.ItemsAll(ctx, lib.ID, abs.ItemsOptions{Filter: filter}, func(items []abs.Item) bool {
		for j := range items {
			it := &items[j]
			if it.IsPodcast() || it.Media.NumChapters == 0 {
				continue
			}
			k := readingKey(it)
			if k == "" {
				continue
			}
			if _, held := groups[k]; !held {
				keys = append(keys, k)
			}
			groups[k] = append(groups[k], it.ID)
		}
		return true
	}); err != nil {
		return err
	}
	var ids []string
	for _, k := range keys {
		if len(groups[k]) > 1 {
			ids = append(ids, groups[k]...)
		}
	}
	whole := map[string]*abs.Item{}
	for chunk := range slices.Chunk(ids, embedBatchSize) {
		items, err := client.ItemsBatch(ctx, chunk)
		if err != nil {
			return err
		}
		for j := range items {
			whole[items[j].ID] = &items[j]
		}
	}

	var rows []abridgedFinding
	at := map[string]int{} // the shorter reading's row
	for _, k := range keys {
		group := groups[k]
		for i := range group {
			for _, other := range group[i+1:] {
				short, long := whole[group[i]], whole[other]
				if short == nil || long == nil {
					continue
				}
				if short.Media.Duration > long.Media.Duration {
					short, long = long, short
				}
				// two copies near one length are one recording, or two paces,
				// chaptered two ways, not a cut
				if short.Media.Duration <= 0 || short.Media.Duration >= long.Media.Duration*(1-spreadMinCut) {
					continue
				}
				sp, lo, hi, n, ok := chapterSpread(long, short)
				if !ok {
					continue
				}
				out.Compared++
				if sp <= abridgedSpread || saysAbridged(short) {
					continue
				}
				row := abridgedFinding{
					ID: short.ID, Title: short.Title(), Author: short.Media.Metadata.AuthorDisplay(), Path: short.RelPath, Duration: wholeSec(short.Media.Duration),
					Problem: "uneven_chapters", Spread: math.Round(sp*100) / 100, Chapters: n,
					Reading: &abridgedReading{ID: long.ID, Title: long.Title(), Path: long.RelPath, Duration: wholeSec(long.Media.Duration)},
					Detail: fmt.Sprintf("against the reading at %s (%s), whose chapters run %.2f to %.2f times as long as this one's over %d in common, a spread of %.2f: a reader's pace alone keeps under 1.2",
						long.RelPath, fmtDuration(long.Media.Duration), lo, hi, n, sp),
				}
				if j, seen := at[short.ID]; seen {
					if rows[j].Spread < row.Spread {
						rows[j] = row
					}
					continue
				}
				at[short.ID] = len(rows)
				rows = append(rows, row)
			}
		}
	}
	for _, row := range rows {
		out.add(row)
	}
	return nil
}

// chapterSpread compares two readings of one book chapter by chapter, the
// way the zbooks session's section_ratio.py did: titles with any leading
// number stripped, lengths summed per title, the titles both have at over a
// minute each, and the long reading's length over the short one's for each.
// Two unabridged readings keep the ratio steady, their readers' pace; the
// spread is the 90th percentile of the ratios over the 10th. A title that
// names only a disc, a track or the book itself is left out: two releases
// split differently share it without sharing the text.
func chapterSpread(long, short *abs.Item) (spread, lo, hi float64, n int, ok bool) {
	book := map[string]bool{}
	for _, it := range []*abs.Item{long, short} {
		for w := range strings.FieldsSeq(norm(it.Title() + " " + it.Media.Metadata.AuthorDisplay())) {
			book[w] = true
		}
	}
	lengths := func(it *abs.Item) map[string]float64 {
		out := map[string]float64{}
		for _, ch := range it.Media.Chapters {
			if t := chapterKey(ch.Title, book); t != "" {
				out[t] += ch.End - ch.Start
			}
		}
		return out
	}
	a, b := lengths(long), lengths(short)
	var ratios []float64
	var coverA, coverB float64
	for t, la := range a {
		lb, both := b[t]
		if !both || la <= spreadMinSeconds || lb <= spreadMinSeconds {
			continue
		}
		ratios = append(ratios, la/lb)
		coverA += la
		coverB += lb
	}
	if len(ratios) < spreadMinChapters || coverA < spreadMinCover*long.Media.Duration || coverB < spreadMinCover*short.Media.Duration {
		return 0, 0, 0, len(ratios), false
	}
	slices.Sort(ratios)
	lo, hi = decile(ratios, 1), decile(ratios, 9)
	if lo <= 0 {
		return 0, 0, 0, len(ratios), false
	}
	// sections that line up run, in the middle, about as much longer as the
	// whole reading does; far from it, they are different stretches of text
	if short.Media.Duration > 0 {
		whole := long.Media.Duration / short.Media.Duration
		if mid := decile(ratios, 5); mid > whole*spreadMaxDrift || mid < whole/spreadMaxDrift {
			return 0, 0, 0, len(ratios), false
		}
	}
	return hi / lo, lo, hi, len(ratios), true
}

// chapterKey is a chapter title as the readings are matched on, or "" for one
// that names nothing of the book's own: no word left once the numbers, the
// split words and the book's title and author are taken out.
func chapterKey(title string, book map[string]bool) string {
	t := strings.ToLower(strings.TrimSpace(chapterNumber.ReplaceAllString(strings.TrimSpace(title), "")))
	for w := range strings.FieldsSeq(norm(t)) {
		if !splitWords[w] && !book[w] && strings.Trim(w, "0123456789") != "" {
			return t
		}
	}
	return ""
}

// decile is the i-th of the nine points cutting sorted into tenths, the way
// Python's statistics.quantiles cuts them by default, so a spread here is the
// spread section_ratio.py measured.
func decile(sorted []float64, i int) float64 {
	const n = 10
	ld := len(sorted)
	m := ld + 1
	j := max(1, min(i*m/n, ld-1))
	delta := i*m - j*n
	return (sorted[j-1]*float64(n-delta) + sorted[j]*float64(delta)) / n
}

func registerAbridgedAudit(r *registry) {
	client := r.client
	prov := r.providerConfig()

	type abridgedIn struct {
		Library   string   `json:"library,omitempty"   jsonschema:"library name or id; default every book library"`
		Filter    string   `json:"filter,omitempty"    jsonschema:"which books, as library_items takes it: authors:Orson Scott Card; default every book (needs library)"`
		Providers []string `json:"providers,omitempty" jsonschema:"Audible stores to search, in order, default the server's --providers, else the library's provider alone, which must then be an Audible store; a book's provider tag puts its own store first, and the next is asked only when one has no edition of the book"`
		Limit     int      `json:"limit,omitempty"     jsonschema:"books per call, default 50, at most 50: each not already marked is a store search, which the server fans out to several store requests"`
		Offset    int      `json:"offset,omitempty"    jsonschema:"skip this many books, counted in the order they were added, abridged or not: a previous call's next_offset. The readings check is reported at offset 0 only"`
	}
	add(r, readTool, &mcp.Tool{
		Name: "audit_abridged",
		Description: "Find books that are probably abridged but not marked so: the abridged flag is off and neither the title nor the folder says abridged (or dramatised). Two checks. " +
			"The store: each book is searched by title and author, and its editions there compared by length: abridged_length is within 5% of an edition the store marks abridged and of no unabridged one; far_shorter is under 60% of the shortest unabridged edition, where the fastest reader against the slowest measured came to 77%. " +
			"The library: two readings of one book (same title and author, a parenthetical such as the reader's name aside) are compared chapter by chapter, matched by title; two unabridged readings keep a steady length ratio from chapter to chapter, spread 1.05 to 1.19 in the cases measured, and uneven_chapters is the shorter of a pair spread over 1.35, cut more in some chapters than others. That needs chapter titles naming the book's own chapters in both, and fetches the books held twice whole; it runs at offset 0 only. Two readings within 10% of each other's length are not compared (a pace, not a cut), nor two whose chapters do not line up, where the middle chapter's ratio is far from the whole readings' (two releases cutting the book at different points under the same names). " +
			"The store check is one search per book (more when providers lists several and the first has no edition), so it works through limit books per call in the order they were added, over a library or a filter; audit_all runs it only with deep. Books under ten minutes, and ones whose provider tag says none, are not searched. " +
			"Fix with item_edit abridged=true; a far_shorter book may instead be one part of a split release, or missing files, so check it first.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in abridgedIn) (*mcp.CallToolResult, abridgedOut, error) {
		libs, err := resolveLibraries(ctx, client, in.Library)
		if err != nil {
			return nil, abridgedOut{}, err
		}
		if in.Filter != "" && in.Library == "" && len(libs) > 1 {
			return nil, abridgedOut{}, fmt.Errorf("a filter needs one library (have: %s)", libraryNames(libs))
		}
		// every library is refused before any is swept
		for i := range libs {
			if err := abridgedRefusal(prov, in.Providers, &libs[i]); err != nil {
				return nil, abridgedOut{}, err
			}
		}
		// a filter comes with one library
		var filter string
		if f := strings.TrimSpace(in.Filter); f != "" && len(libs) == 1 && !libs[0].IsPodcast() {
			if filter, err = buildFilter(ctx, client, &libs[0], f); err != nil {
				return nil, abridgedOut{}, err
			}
		}
		out := abridgedOut{Findings: []abridgedFinding{}}
		// each book is a title search the server fans out to as many as ten
		// store requests, as item_match_batch's is, and a store that starts
		// refusing leaves editions out without saying so: so as few per call
		limit, offset := min(auditLimit(in.Limit, 50), abridgedLimitMax), max(in.Offset, 0)
		// the readings answer the same at every offset: the first call
		// reports them for every library, before its window ends in one
		if offset == 0 {
			for i := range libs {
				if err := sweepReadings(ctx, client, &libs[i], filter, &out); err != nil {
					return nil, abridgedOut{}, err
				}
			}
		}
		walked := new(int)
		for i := range libs {
			more, err := sweepAbridgedStore(ctx, client, prov, &libs[i], abridgedScope{Providers: in.Providers, Limit: limit, Offset: offset, Filter: filter, seen: walked}, &out)
			if err != nil {
				return nil, abridgedOut{}, err
			}
			if more {
				out.NextOffset = offset + out.Scanned
				break
			}
		}
		slices.SortStableFunc(out.Findings, func(a, b abridgedFinding) int {
			return cmp.Compare(slices.Index(abridgedProblemOrder, a.Problem), slices.Index(abridgedProblemOrder, b.Problem))
		})

		return nil, out, nil
	})
}
