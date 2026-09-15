package tools

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/katbyte/abs-mcp/lib/abs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// audit_matched: is the asin on each matched book the recording on disk? A
// match applied by title alone carries the right description and the wrong
// narrator, and nothing later says so. This fetches the provider's record for
// every asin and runs the same comparator as item_match_batch, so the two
// agree on what "same recording" means. One provider request per matched
// book, so it pages, and audit_all runs it only with deep.

type matchedFinding struct {
	ID       string          `json:"id"`
	Title    string          `json:"title"`
	ASIN     string          `json:"asin"`
	Problems []string        `json:"problems"           jsonschema:"duration_off: the recording is a different length; narrator_differs: read by someone else (or the item's narrator field holds something else, such as a publisher); title_differs: the asin is another book; not_found: none of the providers has a record for the asin, usually a store region that was not tried"`
	Reason   string          `json:"reason,omitempty"`
	FoundIn  string          `json:"found_in,omitempty" jsonschema:"the provider that had the asin"`
	Provider *batchCandidate `json:"provider,omitempty" jsonschema:"what the asin points at"`
	Fields   []fieldDiff     `json:"fields,omitempty"   jsonschema:"with fields: each field the provider's record would write differently, and both values"`
}

type matchedCounts struct {
	DurationOff     int `json:"duration_off"`
	NarratorDiffers int `json:"narrator_differs"`
	TitleDiffers    int `json:"title_differs"`
	NotFound        int `json:"not_found"`
	FieldsDiffer    int `json:"fields_differ,omitempty" jsonschema:"with fields: books where at least one field differs"`
}

type matchedOut struct {
	Scanned     int              `json:"items_scanned"          jsonschema:"books with an asin that were checked"`
	Found       int              `json:"total_findings"         jsonschema:"before limit"`
	Counts      matchedCounts    `json:"counts"`
	FieldCounts map[string]int   `json:"field_counts,omitempty" jsonschema:"with fields: how many books differ on each field"`
	Findings    []matchedFinding `json:"findings"`
	NextPage    *int             `json:"next_page,omitempty"    jsonschema:"pass as page to check the next books; absent when every matched book has been checked"`
}

// checkMatched compares one matched book with the provider's record for its
// asin, trying each provider in turn until one has it, and adds a finding when
// they disagree. A book bought from another store is not in the library's
// region: the same recording has a different asin in each, and a Canadian
// library set to the US store had none of its Black Library asins found there.
func checkMatched(ctx context.Context, client *abs.Client, it *abs.Item, providers []string, tolerance float64, fields bool, out *matchedOut) error {
	asin := strings.TrimSpace(it.Media.Metadata.ASIN)
	out.Scanned++
	f := matchedFinding{ID: it.ID, Title: it.Title(), ASIN: asin}
	var hit *abs.BookSearchResult
	for _, provider := range providers {
		results, err := client.SearchBooks(ctx, provider, asin, "", it.ID)
		if err != nil {
			return err
		}
		if idx := slices.IndexFunc(results, func(r abs.BookSearchResult) bool { return strings.EqualFold(r.ASIN, asin) }); idx >= 0 {
			hit, f.FoundIn = &results[idx], provider
			break
		}
	}
	if hit == nil {
		f.Problems = []string{"not_found"}
		f.Reason = "no record for the asin in " + strings.Join(providers, ", ")
		out.Counts.NotFound++
		out.Found++
		out.Findings = append(out.Findings, f)
		return nil
	}
	s := scoreMatch(it, hit, tolerance)
	if s.TitleLevel == 0 {
		f.Problems = append(f.Problems, "title_differs")
		out.Counts.TitleDiffers++
	}
	if s.Duration == "off" {
		f.Problems = append(f.Problems, "duration_off")
		out.Counts.DurationOff++
	}
	if s.Narrator == "differs" {
		f.Problems = append(f.Problems, "narrator_differs")
		out.Counts.NarratorDiffers++
	}
	if fields {
		if f.Fields = fieldDiffs(it, hit); len(f.Fields) > 0 {
			f.Problems = append(f.Problems, "fields_differ")
			out.Counts.FieldsDiffer++
			if out.FieldCounts == nil {
				out.FieldCounts = map[string]int{}
			}
			for _, d := range f.Fields {
				out.FieldCounts[d.Field]++
			}
		}
	}
	if len(f.Problems) == 0 {
		return nil
	}
	c := candidateOf(scored{Result: *hit, Score: s})
	f.Reason, f.Provider = s.Reason, &c
	out.Found++
	out.Findings = append(out.Findings, f)
	return nil
}

// matchedScope is what a sweep covers: which providers to ask, how strict the
// duration comparison is, and which page of which books.
type matchedScope struct {
	Providers []string
	Tolerance float64
	PageSize  int    // 0: everything in one go, as audit_all does
	Page      int    // 0-based
	Filter    string // a library_items filter; empty means every matched book
	Fields    bool   // compare every field, not only the ones that identify the recording

	// seen counts the matched books walked so far, across every library a
	// call sweeps, so a page runs on from one library into the next
	seen *int
}

// sweepMatched checks the matched books a scope selects and says whether a
// further page exists.
func sweepMatched(ctx context.Context, client *abs.Client, lib *abs.Library, scope matchedScope, out *matchedOut) (more bool, err error) {
	if lib.IsPodcast() {
		return false, nil
	}
	providers := providersFor(scope.Providers, lib)
	if scope.Filter != "" {
		encoded, ferr := buildFilter(ctx, client, lib, scope.Filter)
		if ferr != nil {
			return false, ferr
		}
		res, lerr := client.Items(ctx, lib.ID, abs.ItemsOptions{Limit: scope.PageSize, Page: scope.Page, Sort: "media.metadata.title", Filter: encoded, Minified: true})
		if lerr != nil {
			return false, lerr
		}
		for j := range res.Results {
			it := &res.Results[j]
			if it.IsPodcast() || strings.TrimSpace(it.Media.Metadata.ASIN) == "" {
				continue
			}
			if err := checkMatched(ctx, client, it, providerOrder(it, providers), scope.Tolerance, scope.Fields, out); err != nil {
				return false, err
			}
		}
		return (scope.Page+1)*scope.PageSize < res.Total, nil
	}
	skip := scope.PageSize * scope.Page
	seen := scope.seen
	if seen == nil {
		seen = new(0)
	}
	err = client.ItemsAll(ctx, lib.ID, abs.ItemsOptions{}, func(items []abs.Item) bool {
		for j := range items {
			it := &items[j]
			if it.IsPodcast() || strings.TrimSpace(it.Media.Metadata.ASIN) == "" {
				continue
			}
			*seen++
			if *seen <= skip {
				continue
			}
			if scope.PageSize > 0 && *seen > skip+scope.PageSize {
				more = true
				return false
			}
			if err = checkMatched(ctx, client, it, providerOrder(it, providers), scope.Tolerance, scope.Fields, out); err != nil {
				return false
			}
		}
		return true
	})
	return more, err
}

func registerMatchedAudit(r *registry) {
	client := r.client

	type matchedIn struct {
		Library   string   `json:"library,omitempty"   jsonschema:"library name or id; default every book library"`
		Filter    string   `json:"filter,omitempty"    jsonschema:"which books, as library_items takes it: authors:Douglas Adams, series:Discworld; default every matched book (needs library)"`
		Providers []string `json:"providers,omitempty" jsonschema:"where to look the asins up, in order, default the server's --providers, else the library's provider alone; the same recording has a different asin in each region, so put the store the books were bought from first: [audible.ca, audible]"`
		Limit     int      `json:"limit,omitempty"     jsonschema:"matched books to check per call, default 50, at most 100: each is a provider request"`
		Page      int      `json:"page,omitempty"      jsonschema:"0-based page of matched books, which with no library run on from one book library into the next; next_page says when there is more"`
		Tolerance float64  `json:"tolerance,omitempty" jsonschema:"how far apart two durations of the same recording may be, default 0.03"`
		Fields    bool     `json:"fields,omitempty"    jsonschema:"also compare title, subtitle, authors, narrators, series, genres, publisher, year, language and description with the provider's record, and report each field that differs with both values: what item_match_apply with override_details would change. Off by default"`
	}
	add(r, readTool, &mcp.Tool{
		Name: "audit_matched",
		Description: "Check that each matched book's asin is the recording on disk, not just the same title: the provider's record for the asin is compared on title, duration and narrator. A match applied by title alone carries the right description and the wrong narrator, and this is what says so. " +
			"One provider request per matched book (more when providers lists several), so it works a page at a time, over the whole library or a filter such as authors:Douglas Adams; audit_all runs it only with deep. " +
			"not_found usually means the book came from another store's region, where the same recording has another asin: pass providers=[audible, audible.ca] or whichever store the books were bought from. Fix a wrong edition with item_match (another region via provider) then item_match_apply with override_details, or accept it; a narrator field holding a publisher is an item_edit. " +
			"With fields, every field is compared with the provider's record and the differences are listed with both values: a match applied the default way fills only empty fields, so this is what says which of the kept values the provider would have written differently. Accept the provider's by re-applying that book with override_details, or keep yours.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in matchedIn) (*mcp.CallToolResult, matchedOut, error) {
		libs, err := resolveLibraries(ctx, client, in.Library)
		if err != nil {
			return nil, matchedOut{}, err
		}
		if in.Filter != "" && in.Library == "" && len(libs) > 1 {
			return nil, matchedOut{}, fmt.Errorf("a filter needs one library (have: %s)", libraryNames(libs))
		}
		out := matchedOut{Findings: []matchedFinding{}}
		pageSize := min(limitOr(in.Limit, 50), 100)
		// with no library the matched books of every book library are one
		// run, in library order, and a page is a window over that run: the
		// count of books walked carries on from one library to the next
		walked := new(int)
		for i := range libs {
			more, err := sweepMatched(ctx, client, &libs[i], matchedScope{Providers: in.Providers, Tolerance: in.Tolerance, PageSize: pageSize, Page: max(in.Page, 0), Filter: strings.TrimSpace(in.Filter), Fields: in.Fields, seen: walked}, &out)
			if err != nil {
				return nil, matchedOut{}, err
			}
			if more {
				out.NextPage = new(max(in.Page, 0) + 1)
				break
			}
		}

		return nil, out, nil
	})
}
