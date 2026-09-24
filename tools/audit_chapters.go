package tools

import (
	"cmp"
	"context"
	"fmt"
	"math"
	"slices"
	"strconv"

	"github.com/katbyte/abs-mcp/lib/abs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// audit_chapters is everything wrong with a book's chapter list, in four
// parts. The server builds a multi-file book's chapters one per file and keeps
// whatever list was last set, so moving files changes the audio under the
// chapters without changing them: a file taken out leaves chapters starting
// after the audio ends (past_end), and a track added leaves audio after the
// last chapter ends (short). A list out of order, and one chapter over a long
// book (single), are the other two.
//
// The item listing carries a chapter count and not the chapters, whatever it
// is asked for (the server answers every listing minified), so each book with
// chapters is fetched whole, a batch at a time.

const (
	// chapterEndSlack is how far past the audio the last chapter may run: a
	// store's list is measured on its own copy of the recording, which runs a
	// second or two either side of a file made from it.
	chapterEndSlack = 5.0
	// the chapters must stop short of the audio by more than a minute and
	// more than 1% of the book to be short: a duration read from a VBR mp3
	// without a header is an estimate that can be a percent out, and a
	// store's list may leave off an outro of tens of seconds. A track added
	// is minutes, and a larger share.
	chapterGapMin   = 60.0
	chapterGapShare = 0.01
	// chapterOverlapSlack is how far a chapter may run past the next one's
	// start before it overlaps: less is two timestamps rounded differently.
	chapterOverlapSlack = 1.0
)

// chapterProblemOrder is the order of the sections, worst first: a chapter
// nobody can reach, a list the player cannot seek by, audio no chapter
// covers, and a book with nothing to seek to.
var chapterProblemOrder = []string{"past_end", "out_of_order", "short", "single"}

var chapterFixes = map[string]string{
	"past_end":     "item_chapters_set fit=true: drops the chapters that start past the end and ends the last at the end of the audio",
	"out_of_order": "item_chapters_set with the chapters in order, or from_asin for the store's",
	"short":        "item_chapters_set fit=true: ends the last chapter at the end of the audio; from_asin or a list when the added audio needs chapters of its own",
	"single":       "item_chapters_set from_asin for the store's chapters, or chapters with a list",
}

type chapterRow struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Path    string `json:"path,omitempty"`
	Problem string `json:"problem"        jsonschema:"past_end: a chapter starts at or past the end of the audio, or the last runs more than 5s past it, as a file taken out of the book leaves them; out_of_order: a chapter starts no later than the one before it, or runs more than 1s past the next one's start; short: the chapters end more than a minute and more than 1% of the book before the audio does, as a track added leaves them; single: one chapter over a book of two hours or more"`
	Detail  string `json:"detail"         jsonschema:"the chapter at fault and the times, in seconds"`
	Fix     string `json:"fix"`
}

type chapterCounts struct {
	PastEnd    int `json:"past_end"`
	OutOfOrder int `json:"out_of_order"`
	Short      int `json:"short"`
	Single     int `json:"single"`
}

type chaptersOut struct {
	Scanned  int           `json:"items_scanned"`
	Read     int           `json:"chapters_read"  jsonschema:"books whose chapter lists were fetched: every book with chapters, fifty to a request"`
	Found    int           `json:"total_findings" jsonschema:"rows before limit: a book with two problems is two rows"`
	Counts   chapterCounts `json:"counts"`
	Findings []chapterRow  `json:"findings"       jsonschema:"by problem, worst first: past_end, out_of_order, short, single"`
}

// chapterSweep gathers the books with chapters from a listing and judges
// their chapter lists once fetched.
type chapterSweep struct {
	scanned, read int
	pending       []string // books with chapters, still to fetch
	rows          []chapterRow
	// singleOnly judges from the listing alone, which carries each book's
	// chapter count and length but not its chapters: one chapter over a
	// long book can be told from that, the rest cannot. audit_all sweeps
	// this way unless deep, as reading every chaptered book whole, fifty to
	// a request with all its files, is the cost deep is for
	singleOnly bool
}

// add takes one item from a listing: a book with chapters is held to fetch.
func (c *chapterSweep) add(it *abs.Item) {
	if it.IsPodcast() {
		return
	}
	c.scanned++
	switch {
	case c.singleOnly:
		if it.Media.NumChapters == 1 && it.Media.Duration >= chapterlessMinHours*3600 {
			c.rows = append(c.rows, chapterRow{
				ID: it.ID, Title: it.Title(), Path: it.RelPath, Problem: "single",
				Detail: "one chapter over the whole " + fmtDuration(it.Media.Duration), Fix: chapterFixes["single"],
			})
		}
	case it.Media.NumChapters > 0:
		c.pending = append(c.pending, it.ID)
	}
}

// resolve fetches the held books whole, a batch at a time, and judges each.
func (c *chapterSweep) resolve(ctx context.Context, client *abs.Client) error {
	pending := c.pending
	c.pending = nil
	for chunk := range slices.Chunk(pending, embedBatchSize) {
		items, err := client.ItemsBatch(ctx, chunk)
		if err != nil {
			return err
		}
		for j := range items {
			c.read++
			c.rows = append(c.rows, chapterProblems(&items[j])...)
		}
	}
	return nil
}

// library sweeps one library's listing and judges its books.
func (c *chapterSweep) library(ctx context.Context, client *abs.Client, lib *abs.Library) error {
	if lib.IsPodcast() {
		return nil
	}
	if err := client.ItemsAll(ctx, lib.ID, abs.ItemsOptions{Minified: true}, func(items []abs.Item) bool {
		for j := range items {
			c.add(&items[j])
		}
		return true
	}); err != nil {
		return err
	}
	return c.resolve(ctx, client)
}

// report is what was found, by problem and then by title, keeping at most
// limit rows.
func (c *chapterSweep) report(limit int) chaptersOut {
	out := chaptersOut{Scanned: c.scanned, Read: c.read, Found: len(c.rows), Findings: []chapterRow{}}
	rows := slices.Clone(c.rows)
	slices.SortStableFunc(rows, func(a, b chapterRow) int {
		return cmp.Or(
			cmp.Compare(slices.Index(chapterProblemOrder, a.Problem), slices.Index(chapterProblemOrder, b.Problem)),
			cmp.Compare(a.Title, b.Title),
			cmp.Compare(a.ID, b.ID),
		)
	})
	for _, row := range rows {
		switch row.Problem {
		case "past_end":
			out.Counts.PastEnd++
		case "out_of_order":
			out.Counts.OutOfOrder++
		case "short":
			out.Counts.Short++
		case "single":
			out.Counts.Single++
		}
	}
	out.Findings = append(out.Findings, rows[:min(len(rows), limit)]...)
	return out
}

// secs writes a chapter time as seconds, to the millisecond the server keeps.
func secs(v float64) string {
	return strconv.FormatFloat(math.Round(v*1000)/1000, 'f', -1, 64) + "s"
}

// chapterProblems is everything wrong with a whole book's chapter list,
// one row per problem.
func chapterProblems(it *abs.Item) []chapterRow {
	chapters, end := it.Media.Chapters, it.Media.Duration
	if it.IsPodcast() || len(chapters) == 0 || end <= 0 {
		return nil
	}
	var out []chapterRow
	row := func(problem, detail string) {
		out = append(out, chapterRow{ID: it.ID, Title: it.Title(), Path: it.RelPath, Problem: problem, Detail: detail, Fix: chapterFixes[problem]})
	}

	// the chapter that ends last, which is the last one unless the list is
	// out of order
	last := chapters[0]
	for _, ch := range chapters[1:] {
		if ch.End >= last.End {
			last = ch
		}
	}

	if first := slices.IndexFunc(chapters, func(ch abs.Chapter) bool { return ch.Start >= end }); first >= 0 {
		past := 0
		for _, ch := range chapters {
			if ch.Start >= end {
				past++
			}
		}
		row("past_end", fmt.Sprintf("%d of %d chapters start at or past the end of the audio at %s, from chapter %d, %q, at %s",
			past, len(chapters), secs(end), first+1, chapters[first].Title, secs(chapters[first].Start)))
	} else if last.End > end+chapterEndSlack {
		row("past_end", fmt.Sprintf("the last chapter, %q, ends at %s, %s past the end of the audio at %s",
			last.Title, secs(last.End), secs(last.End-end), secs(end)))
	}

	var disorder []string
	for i := 1; i < len(chapters); i++ {
		prev, ch := chapters[i-1], chapters[i]
		switch {
		case ch.Start <= prev.Start:
			disorder = append(disorder, fmt.Sprintf("chapter %d, %q, starts at %s, not after chapter %d, %q, at %s", i+1, ch.Title, secs(ch.Start), i, prev.Title, secs(prev.Start)))
		case prev.End > ch.Start+chapterOverlapSlack:
			disorder = append(disorder, fmt.Sprintf("chapter %d, %q, runs to %s, past the start of chapter %d, %q, at %s", i, prev.Title, secs(prev.End), i+1, ch.Title, secs(ch.Start)))
		}
	}
	if len(disorder) > 0 {
		detail := disorder[0]
		if len(disorder) > 1 {
			detail += fmt.Sprintf(", and %d more out of place", len(disorder)-1)
		}
		row("out_of_order", detail)
	}

	if gap := end - last.End; gap > chapterGapMin && gap > end*chapterGapShare {
		row("short", fmt.Sprintf("the last chapter, %q, ends at %s, %s (%.0f%%) before the audio does at %s",
			last.Title, secs(last.End), secs(gap), 100*gap/end, secs(end)))
	}

	// one chapter over a whole book is the same problem as none: there is
	// nothing to seek to, and audit_missing counts any chapter as some
	if len(chapters) == 1 && end >= chapterlessMinHours*3600 {
		row("single", fmt.Sprintf("one chapter, %q, over the whole %s", chapters[0].Title, fmtDuration(end)))
	}

	return out
}

// fitChapters is a book's own chapters fitted to its audio: the ones that
// start at or past the end dropped, and the last ended at the end, which also
// covers audio a track added left after it. A list out of order is refused
// rather than fitted: fitting keeps the order it finds, and the order is what
// is wrong.
func fitChapters(it *abs.Item) (kept []abs.Chapter, dropped int, err error) {
	end := it.Media.Duration
	switch {
	case len(it.Media.Chapters) == 0:
		return nil, 0, fmt.Errorf("%s has no chapters to fit: set them with chapters or from_asin", it.Title())
	case end <= 0:
		return nil, 0, fmt.Errorf("%s has no audio to fit its chapters to", it.Title())
	}
	var at []int // each kept chapter's place in the book's list, for the errors
	for i, ch := range it.Media.Chapters {
		if ch.Start < end {
			kept = append(kept, ch)
			at = append(at, i+1)
		}
	}
	if len(kept) == 0 {
		return nil, 0, fmt.Errorf("every chapter of %s starts at or past the end of the audio at %s: pass the chapters, or from_asin", it.Title(), secs(end))
	}
	for i := 1; i < len(kept); i++ {
		if prev, ch := kept[i-1], kept[i]; ch.Start <= prev.Start || prev.End > ch.Start+chapterOverlapSlack {
			return nil, 0, fmt.Errorf("chapter %d, %q, at %s is out of order with chapter %d, %q, at %s: fit keeps the order it finds, so pass the chapters in order instead",
				at[i], ch.Title, secs(ch.Start), at[i-1], prev.Title, secs(prev.Start))
		}
	}
	for i := range kept {
		kept[i].ID = i
	}
	kept[len(kept)-1].End = end
	return kept, len(it.Media.Chapters) - len(kept), nil
}

func registerChaptersAudit(r *registry) {
	client := r.client

	add(r, readTool, &mcp.Tool{
		Name: "audit_chapters",
		Description: "Everything wrong with books' chapter lists, one row per problem. past_end: a chapter starts at or past the end of the audio, or the last runs more than 5s past it; the server does not rebuild a book's chapters when a file is taken out of it, so a book chaptered one per file keeps the chapters of the file that went. " +
			"short: the chapters end more than a minute, and more than 1% of the book, before the audio does, as a track added to a chaptered book leaves them. " +
			"out_of_order: a chapter starts no later than the one before it, or runs past the next one's start. " +
			"single: one chapter over a book of two hours or more, which is as unnavigable as none but not reported by audit_missing chapters. " +
			"Each row names the chapter and the times in seconds, and the fix: item_chapters_set fit=true for past_end and short, which keeps the book's chapters, drops those past the end and ends the last at the end of the audio; from_asin or a list for the others. " +
			"The item listing carries only a chapter count, so every book with chapters is fetched, fifty to a request.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in auditIn) (*mcp.CallToolResult, chaptersOut, error) {
		libs, err := resolveLibraries(ctx, client, in.Library)
		if err != nil {
			return nil, chaptersOut{}, err
		}
		var sweep chapterSweep
		for i := range libs {
			if err := sweep.library(ctx, client, &libs[i]); err != nil {
				return nil, chaptersOut{}, err
			}
		}

		return nil, sweep.report(auditLimit(in.Limit, 100)), nil
	})
}
