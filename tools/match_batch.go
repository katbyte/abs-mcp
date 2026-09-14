package tools

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/katbyte/abs-mcp/lib/abs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// item_match_batch and item_match_apply_batch: matching at the scale of a
// library rather than a book. Nine hundred unmatched books is eighteen hundred
// calls and nine hundred judgements the one-at-a-time way. The batch searches
// the provider for a page of books, scores every hit with the comparator in
// match_score.go, and returns one row per book with the best candidate and a
// confidence, so the reader looks at the rows that need a decision. Nothing is
// applied here: item_match_apply_batch takes an explicit list of item and asin
// pairs, however they were chosen.

// batchCandidate is a provider hit as the batch reports it.
type batchCandidate struct {
	ASIN       string   `json:"asin,omitempty"`
	ISBN       string   `json:"isbn,omitempty"`
	Title      string   `json:"title"`
	Author     string   `json:"author,omitempty"`
	Narrator   string   `json:"narrator,omitempty"`
	Duration   string   `json:"duration,omitempty"`
	Year       string   `json:"year,omitempty"`
	Series     []string `json:"series,omitempty"`
	Confidence string   `json:"confidence"`
	Reason     string   `json:"reason"`
}

type batchRow struct {
	ID         string           `json:"id"`
	Title      string           `json:"title"`
	Author     string           `json:"author,omitempty"`
	Path       string           `json:"path,omitempty"`
	Duration   string           `json:"duration,omitempty"`
	Confidence string           `json:"confidence"         jsonschema:"of the best candidate: exact (same book, same recording), likely (same book, recording unconfirmed), edition (same book, different recording), unsure (nothing fits), none (no hits)"`
	Provider   string           `json:"provider,omitempty" jsonschema:"the provider the best candidate came from; pass it to item_match_apply_batch"`
	Best       *batchCandidate  `json:"best,omitempty"`
	Others     []batchCandidate `json:"others,omitempty"   jsonschema:"the next candidates, when more than one was asked for"`
}

type batchCounts struct {
	Exact   int `json:"exact"`
	Likely  int `json:"likely"`
	Edition int `json:"edition"`
	Unsure  int `json:"unsure"`
	None    int `json:"none"`
}

func candidateOf(s scored) batchCandidate {
	c := batchCandidate{
		ASIN: s.Result.ASIN, ISBN: s.Result.ISBN, Title: s.Result.Title, Author: s.Result.Author, Narrator: s.Result.Narrator,
		Year: s.Result.PublishedYear.String(), Confidence: s.Score.Confidence, Reason: s.Score.Reason,
	}
	if s.Result.Duration > 0 {
		c.Duration = fmtDuration(s.Result.Duration * 60)
	}
	for _, sr := range s.Result.Series {
		if sr.Sequence != "" {
			c.Series = append(c.Series, sr.Title()+" #"+sr.Sequence)
		} else {
			c.Series = append(c.Series, sr.Title())
		}
	}
	return c
}

// searchForItem asks the provider about one item, once by its title and, when
// that finds nothing and the title has a subtitle, once more by the main part.
func searchForItem(ctx context.Context, client *abs.Client, it *abs.Item, provider string) ([]abs.BookSearchResult, error) {
	title, author := strings.TrimSpace(titleNoise.ReplaceAllString(it.Title(), " ")), it.Media.Metadata.AuthorDisplay()
	results, err := client.SearchBooks(ctx, provider, title, author, it.ID)
	if err != nil {
		return nil, err
	}
	if len(results) == 0 {
		if parts := titleSplit.Split(title, 2); len(parts) == 2 && len(parts[0]) >= 4 {
			results, err = client.SearchBooks(ctx, provider, strings.TrimSpace(parts[0]), author, it.ID)
			if err != nil {
				return nil, err
			}
		}
	}
	return results, nil
}

// scoreItem is one batch row: the item searched at each provider in turn
// until one has the recording (an exact), the hits ranked, the best kept and
// the next few listed when asked. The same recording carries a different asin
// in each Audible region and is missing from some, so a book bought from the
// Canadian store is exact on audible.ca and an edition, or nothing, on the US
// store.
func scoreItem(ctx context.Context, client *abs.Client, it *abs.Item, providers []string, tolerance float64, candidates int) (batchRow, error) {
	row := batchRow{ID: it.ID, Title: it.Title(), Author: it.Media.Metadata.AuthorDisplay(), Path: it.RelPath, Confidence: confNone}
	if it.Media.Duration > 0 {
		row.Duration = fmtDuration(it.Media.Duration)
	}
	var best []scored
	for _, provider := range providers {
		results, err := searchForItem(ctx, client, it, provider)
		if err != nil {
			return row, err
		}
		ranked := rankCandidates(it, results, tolerance)
		if len(ranked) == 0 {
			continue
		}
		if row.Best == nil || confidenceRank[ranked[0].Score.Confidence] < confidenceRank[row.Confidence] {
			b := candidateOf(ranked[0])
			row.Best, row.Confidence, row.Provider, best = &b, b.Confidence, provider, ranked
		}
		if row.Confidence == confExact {
			break
		}
	}
	for _, sc := range best[min(len(best), 1):min(len(best), max(candidates, 1))] {
		row.Others = append(row.Others, candidateOf(sc))
	}
	return row, nil
}

func (c *batchCounts) add(confidence string) {
	switch confidence {
	case confExact:
		c.Exact++
	case confLikely:
		c.Likely++
	case confEdition:
		c.Edition++
	case confUnsure:
		c.Unsure++
	default:
		c.None++
	}
}

// applyResult is one row of item_match_apply_batch.
type applyResult struct {
	Item    string          `json:"item"`
	Title   string          `json:"title,omitempty"`
	ASIN    string          `json:"asin,omitempty"`
	ISBN    string          `json:"isbn,omitempty"`
	Updated bool            `json:"updated"`
	Kept    []string        `json:"kept,omitempty"    jsonschema:"fields restored after the match"`
	Fields  []fieldDecision `json:"fields,omitempty"  jsonschema:"with smart: every field the provider would write differently and what was done about it"`
	Warning string          `json:"warning,omitempty"`
	Error   string          `json:"error,omitempty"`
}

// smartRow runs the smart match for one row of a batch: the decisions land on
// the row, and so does what happened.
func smartRow(ctx context.Context, client *abs.Client, it *abs.Item, provider string, res *applyResult, preview bool) {
	decisions, r, err := smartApply(ctx, client, it, provider, res.ASIN, res.ISBN, preview)
	res.Fields = decisions
	switch {
	case err != nil:
		res.Error = err.Error()
	case preview:
	default:
		res.Updated, res.Warning = r.Updated, r.Warning
		if terr := tagProvider(ctx, client, it.ID, it.Media.Tags, provider); terr != nil {
			res.Warning = joinWarnings(res.Warning, "recording the provider tag failed: "+terr.Error())
		}
	}
}

// matchRow runs a plain match for one row of a batch, then restores the kept
// fields and records the store. opts carries the override flags; the
// provider, asin and isbn come from the row.
func matchRow(ctx context.Context, client *abs.Client, it *abs.Item, provider string, keep []string, opts abs.MatchOptions, res *applyResult) {
	opts.Provider, opts.ASIN, opts.ISBN = provider, res.ASIN, res.ISBN
	r, err := client.Match(ctx, it.ID, opts)
	if err != nil {
		res.Error = err.Error()
		return
	}
	res.Updated, res.Warning = r.Updated, r.Warning
	if r.LibraryItem != nil && res.ASIN != "" && !strings.EqualFold(r.LibraryItem.Media.Metadata.ASIN, res.ASIN) {
		res.Warning = joinWarnings(res.Warning, fmt.Sprintf("the item's asin is %q, not %s; set override_details to replace it", r.LibraryItem.Media.Metadata.ASIN, res.ASIN))
	}
	if kerr := restoreKept(ctx, client, it, keep); kerr != nil {
		res.Error = "matched, but restoring " + strings.Join(keep, ", ") + " failed: " + kerr.Error()
	} else {
		res.Kept = keep
	}
	tags := it.Media.Tags
	if r.LibraryItem != nil && !slices.Contains(keep, "tags") {
		tags = r.LibraryItem.Media.Tags
	}
	if terr := tagProvider(ctx, client, it.ID, tags, provider); terr != nil {
		res.Warning = joinWarnings(res.Warning, "recording the provider tag failed: "+terr.Error())
	}
}

// addCounts folds one row's counts into the batch's.
func addCounts(into, add map[string]int) map[string]int {
	for k, v := range add {
		if into == nil {
			into = map[string]int{}
		}
		into[k] += v
	}
	return into
}

func registerMatchBatchTools(r *registry) {
	client := r.client

	type batchIn struct {
		Library    string   `json:"library,omitempty"    jsonschema:"library name or id; optional when the server has one library"`
		Filter     string   `json:"filter,omitempty"     jsonschema:"which books, as library_items takes it: authors:Robert A. Heinlein, series:Discworld, missing:asin (the default: books with no asin)"`
		Providers  []string `json:"providers,omitempty"  jsonschema:"metadata providers to try in order, default the server's --providers, else the library's alone; each book is searched at the next one until an exact match turns up, so [audible.ca, audible] for books bought from the Canadian store"`
		Limit      int      `json:"limit,omitempty"      jsonschema:"books per call, default 20, at most 50: each is a provider search"`
		Page       int      `json:"page,omitempty"       jsonschema:"0-based page of the filtered listing; next_page says when there is more"`
		Candidates int      `json:"candidates,omitempty" jsonschema:"candidates to report per book, default 1 (the best); raise it to drill into a row"`
		Tolerance  float64  `json:"tolerance,omitempty"  jsonschema:"how far apart two durations of the same recording may be, default 0.03"`
	}
	type batchOut struct {
		Library   string      `json:"library"`
		Providers []string    `json:"providers"`
		Filter    string      `json:"filter"`
		Page      int         `json:"page"`
		Total     int         `json:"total"               jsonschema:"books the filter selects, across all pages"`
		Skipped   int         `json:"skipped,omitempty"   jsonschema:"books tagged zz-provider:none, which the collector has said have nothing to match"`
		NextPage  *int        `json:"next_page,omitempty" jsonschema:"pass as page to continue; absent on the last page"`
		Counts    batchCounts `json:"counts"`
		Rows      []batchRow  `json:"rows"`
	}
	add(r, readTool, &mcp.Tool{
		Name: "item_match_batch",
		Description: "Match a page of books against a provider and score every hit, one row per book with the best candidate and a confidence: exact means the same book and the same recording (title, author and duration agree), likely the same book with the recording unconfirmed, edition the same book but a different recording (duration or narrator disagree), unsure nothing fits, none no hits. " +
			"Nothing is applied: pass the rows you accept to item_match_apply_batch. Defaults to the books with no asin; filter narrows to an author, series or folder the way library_items does. " +
			"The same recording has a different asin in each Audible region and is missing from some, so providers is a list tried in order until a book is exact; put the store the books were bought from first. Each row says which provider its best candidate came from. Raise candidates to see more than the best hit for a row.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in batchIn) (*mcp.CallToolResult, batchOut, error) {
		lib, err := resolveLibrary(ctx, client, in.Library)
		if err != nil {
			return nil, batchOut{}, err
		}
		if lib.IsPodcast() {
			return nil, batchOut{}, errors.New("a podcast library has no books to match")
		}
		filter := in.Filter
		if strings.TrimSpace(filter) == "" {
			filter = "missing:asin"
		}
		encoded, err := buildFilter(ctx, client, lib, filter)
		if err != nil {
			return nil, batchOut{}, err
		}
		providers := providersFor(in.Providers, lib)
		limit := min(limitOr(in.Limit, 20), 50)
		page, err := client.Items(ctx, lib.ID, abs.ItemsOptions{Limit: limit, Page: max(in.Page, 0), Sort: "media.metadata.title", Filter: encoded, Minified: true})
		if err != nil {
			return nil, batchOut{}, err
		}

		out := batchOut{Library: lib.Name, Providers: providers, Filter: filter, Page: max(in.Page, 0), Total: page.Total, Rows: []batchRow{}}
		for i := range page.Results {
			it := &page.Results[i]
			if it.IsPodcast() {
				continue
			}
			if markedUnmatchable(it) {
				out.Skipped++
				continue
			}
			row, err := scoreItem(ctx, client, it, providerOrder(it, providers), in.Tolerance, in.Candidates)
			if err != nil {
				return nil, batchOut{}, fmt.Errorf("%s: %w", it.Title(), err)
			}
			out.Counts.add(row.Confidence)
			out.Rows = append(out.Rows, row)
		}
		if (out.Page+1)*limit < page.Total {
			out.NextPage = new(out.Page + 1)
		}

		return nil, out, nil
	})

	type applyPair struct {
		Item     string `json:"item"               jsonschema:"library item id"`
		ASIN     string `json:"asin,omitempty"`
		ISBN     string `json:"isbn,omitempty"`
		Provider string `json:"provider,omitempty" jsonschema:"the provider the asin came from, as the batch row says; default the call's provider, then the library's"`
	}
	type applyBatchIn struct {
		Matches         []applyPair `json:"matches"                    jsonschema:"the books to match and what to match each to, up to 50; from item_match_batch rows you accept"`
		Provider        string      `json:"provider,omitempty"         jsonschema:"provider for matches that do not name their own; default the library's"`
		OverrideDetails bool        `json:"override_details,omitempty" jsonschema:"replace metadata fields already set instead of only filling empty ones"`
		OverrideCover   bool        `json:"override_cover,omitempty"`
		Keep            []string    `json:"keep,omitempty"             jsonschema:"with override_details: fields to put back as they were after the match, so an override can replace everything except what is already curated: title, subtitle, authors, narrators, series, genres, tags, publisher, year, language, description. A kept field that was empty stays empty"`
		Smart           bool        `json:"smart,omitempty"            jsonschema:"fill the empty fields, then decide each remaining difference by rule: a file-tag title, a company in the narrator field or a timestamp year is written; a curated series, a plain year or an honorific-only difference is kept; anything else is reported for review with both values. Cannot combine with override_details"`
		Preview         bool        `json:"preview,omitempty"          jsonschema:"with smart: report every decision and change nothing"`
	}
	type applyBatchOut struct {
		Applied int            `json:"applied"`
		Failed  int            `json:"failed"`
		Counts  map[string]int `json:"counts,omitempty" jsonschema:"with smart: decisions by action over every book"`
		Results []applyResult  `json:"results"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "item_match_apply_batch",
		Description: "Apply matches to many books at once, each pinned to the asin or isbn you name: the rows from item_match_batch you accept, whether that is every exact row or a hand-picked few. One failure does not stop the rest; each result says what happened. By default only empty fields are filled; override_details replaces them, and keep names the fields to put back afterwards. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in applyBatchIn) (*mcp.CallToolResult, applyBatchOut, error) {
		if len(in.Matches) == 0 {
			return nil, applyBatchOut{}, errors.New("matches is required: the items and the asin or isbn to match each to")
		}
		if len(in.Matches) > 50 {
			return nil, applyBatchOut{}, fmt.Errorf("%d matches; at most 50 per call", len(in.Matches))
		}
		keep, err := parseKeep(in.Keep)
		if err != nil {
			return nil, applyBatchOut{}, err
		}
		if len(keep) > 0 && !in.OverrideDetails {
			return nil, applyBatchOut{}, errors.New("keep only means something with override_details: without it nothing already set is replaced")
		}
		if in.Smart && in.OverrideDetails {
			return nil, applyBatchOut{}, errors.New("smart and override_details are two answers to the same question; pick one")
		}
		if in.Preview && !in.Smart {
			return nil, applyBatchOut{}, errors.New("preview only means something with smart")
		}
		out := applyBatchOut{Results: []applyResult{}}
		for _, m := range in.Matches {
			res := applyResult{Item: m.Item, ASIN: strings.TrimSpace(m.ASIN), ISBN: strings.TrimSpace(m.ISBN)}
			if res.ASIN == "" && res.ISBN == "" {
				res.Error = "no asin or isbn"
			} else if it, err := resolveItem(ctx, client, "", m.Item); err != nil {
				res.Error = err.Error()
			} else if it.IsPodcast() {
				res.Error = errNotBook.Error()
			} else {
				res.Title = it.Title()
				provider := m.Provider
				if provider == "" {
					provider = in.Provider
				}
				if provider == "" {
					provider, _, _ = matchQuery(ctx, client, it, "", "", "")
				}
				if in.Smart {
					smartRow(ctx, client, it, provider, &res, in.Preview)
					out.Counts = addCounts(out.Counts, smartCounts(res.Fields))
				} else {
					matchRow(ctx, client, it, provider, keep, abs.MatchOptions{OverrideCover: in.OverrideCover, OverrideDetails: in.OverrideDetails}, &res)
				}
			}
			if res.Error == "" {
				out.Applied++
			} else {
				out.Failed++
			}
			out.Results = append(out.Results, res)
		}

		return nil, out, nil
	})
}
