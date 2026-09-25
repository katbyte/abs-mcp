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
// book, so it works a window at a time, and audit_all runs it only with deep.

type matchedFinding struct {
	ID       string          `json:"id"`
	Title    string          `json:"title"`
	ASIN     string          `json:"asin"`
	Duration int             `json:"duration_s,omitempty" jsonschema:"the book's length in seconds, to compare with provider.duration_s"`
	Problems []string        `json:"problems"             jsonschema:"duration_off: the recording is a different length; narrator_differs: read by someone else (or the item's narrator field holds something else, such as a publisher); title_differs: the asin is another book; not_found: none of the providers has a record for the asin, usually a store region that was not tried"`
	Reason   string          `json:"reason,omitempty"`
	FoundIn  string          `json:"found_in,omitempty"   jsonschema:"the provider that had the asin"`
	Provider *batchCandidate `json:"provider,omitempty"   jsonschema:"what the asin points at"`
	Fields   []fieldDiff     `json:"fields,omitempty"     jsonschema:"with fields: each field the provider's record would write differently, and both values"`
}

type matchedCounts struct {
	DurationOff     int `json:"duration_off"`
	NarratorDiffers int `json:"narrator_differs"`
	TitleDiffers    int `json:"title_differs"`
	NotFound        int `json:"not_found"`
	FieldsDiffer    int `json:"fields_differ,omitempty" jsonschema:"with fields: books where at least one field differs"`
}

type matchedOut struct {
	Scanned     int              `json:"items_scanned"          jsonschema:"books with an asin that were checked: this call's window"`
	Found       int              `json:"total_findings"         jsonschema:"before limit"`
	Counts      matchedCounts    `json:"counts"`
	FieldCounts map[string]int   `json:"field_counts,omitempty" jsonschema:"with fields: how many books differ on each field"`
	Findings    []matchedFinding `json:"findings"`
	NextOffset  int              `json:"next_offset,omitempty"  jsonschema:"pass back as offset to check the next matched books; absent when every one has been checked"`
}

// hasASIN is a matched book as the audits that look an asin up see it.
func hasASIN(it *abs.Item) bool { return strings.TrimSpace(it.Media.Metadata.ASIN) != "" }

// hasASINOrISBN is a matched book as the provider tag sees it: an isbn names
// a store's record too.
func hasASINOrISBN(it *abs.Item) bool {
	return hasASIN(it) || strings.TrimSpace(it.Media.Metadata.ISBN) != ""
}

// bookWindow is which matched books matchedWindow hands on.
type bookWindow struct {
	Filter  string               // a built library_items filter; empty is every book
	Matched func(*abs.Item) bool // what counts as matched
	Offset  int                  // matched books to pass over first
	Limit   int                  // matched books to hand on; 0 is every one

	// seen counts the matched books walked so far, carried on from one
	// library into the next when a call walks several
	seen *int
}

// matchedWindow walks a library's books in the order they were added (the
// ones the filter selects, when there is one) and hands fn a window of the
// matched ones: the first Offset are passed over and the next Limit handed
// on. Only matched books are counted, so a window is Limit books to look up
// however many unmatched ones lie between them. No fix changes the order
// added: by title, a book retitled between two calls moved across the
// offset and another was skipped. more says a matched book lies past the
// window.
func matchedWindow(ctx context.Context, client *abs.Client, libraryID string, w bookWindow, fn func(*abs.Item) error) (more bool, err error) {
	seen := w.seen
	if seen == nil {
		seen = new(int)
	}
	lerr := client.ItemsAll(ctx, libraryID, abs.ItemsOptions{Sort: "addedAt", Filter: w.Filter}, func(items []abs.Item) bool {
		for j := range items {
			it := &items[j]
			if it.IsPodcast() || !w.Matched(it) {
				continue
			}
			*seen++
			if *seen <= w.Offset {
				continue
			}
			if w.Limit > 0 && *seen > w.Offset+w.Limit {
				more = true
				return false
			}
			if err = fn(it); err != nil {
				return false
			}
		}
		return true
	})
	if err != nil {
		return false, err
	}
	return more, lerr
}

// checkMatched compares one matched book with the provider's record for its
// asin, trying each provider in turn until one has it, and adds a finding when
// they disagree. A book bought from another store is not in the library's
// region: the same recording has a different asin in each, and a Canadian
// library set to the US store had none of its Black Library asins found there.
func checkMatched(ctx context.Context, client *abs.Client, it *abs.Item, providers []string, tolerance float64, fields bool, out *matchedOut) error {
	asin := strings.TrimSpace(it.Media.Metadata.ASIN)
	out.Scanned++
	f := matchedFinding{ID: it.ID, Title: it.Title(), ASIN: asin, Duration: wholeSec(it.Media.Duration)}
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
// duration comparison is, and which window of which books.
type matchedScope struct {
	Providers []string
	Tolerance float64
	Limit     int    // matched books to check; 0: every one, as audit_all does
	Offset    int    // matched books to pass over first
	Filter    string // a library_items filter; empty means every matched book
	Fields    bool   // compare every field, not only the ones that identify the recording

	// seen counts the matched books walked so far, across every library a
	// call sweeps, so a window runs on from one library into the next
	seen *int
}

// sweepMatched checks the matched books a scope selects and says whether
// more lie past the window.
func sweepMatched(ctx context.Context, client *abs.Client, prov providerConfig, lib *abs.Library, scope matchedScope, out *matchedOut) (more bool, err error) {
	if lib.IsPodcast() {
		return false, nil
	}
	if err := prov.checkProviders(ctx, client, scope.Providers, true); err != nil {
		return false, err
	}
	providers := prov.providersFor(scope.Providers, lib)
	var filter string
	if scope.Filter != "" {
		if filter, err = buildFilter(ctx, client, lib, scope.Filter); err != nil {
			return false, err
		}
	}
	return matchedWindow(ctx, client, lib.ID, bookWindow{Filter: filter, Matched: hasASIN, Offset: scope.Offset, Limit: scope.Limit, seen: scope.seen}, func(it *abs.Item) error {
		return checkMatched(ctx, client, it, prov.providerOrder(it, providers), scope.Tolerance, scope.Fields, out)
	})
}

func registerMatchedAudit(r *registry) {
	client := r.client
	prov := r.providerConfig()

	type matchedIn struct {
		Library   string   `json:"library,omitempty"   jsonschema:"library name or id; default every book library"`
		Filter    string   `json:"filter,omitempty"    jsonschema:"which books, as library_items takes it: authors:Douglas Adams, series:Discworld; default every matched book (needs library)"`
		Providers []string `json:"providers,omitempty" jsonschema:"where to look the asins up, in order, default the server's --providers, else the library's provider alone, which must then be an Audible store; the same recording has a different asin in each region, so put the store the books were bought from first: [audible.ca, audible]"`
		Limit     int      `json:"limit,omitempty"     jsonschema:"matched books to check per call, default 50, at most 100: each is a provider request"`
		Offset    int      `json:"offset,omitempty"    jsonschema:"skip this many matched books: a previous call's next_offset. Only books with an asin are counted (the filter's, when one is given), in the order they were added; with no library the count runs on from one book library into the next"`
		Tolerance float64  `json:"tolerance,omitempty" jsonschema:"how far apart two durations of the same recording may be, default 0.03, at most 0.1"`
		Fields    bool     `json:"fields,omitempty"    jsonschema:"also compare title, subtitle, authors, narrators, series, genres, publisher, year, language and description with the provider's record, and report each field that differs with both values: what item_match_apply with override_details would change. Off by default"`
	}
	add(r, readTool, &mcp.Tool{
		Name: "audit_matched",
		Description: "Check that each matched book's asin is the recording on disk, not just the same title: the provider's record for the asin is compared on title, duration and narrator. A match applied by title alone carries the right description and the wrong narrator, and this is what says so. " +
			"One provider request per matched book (more when providers lists several), so it checks limit matched books per call, over the whole library or a filter such as authors:Douglas Adams; audit_all runs it only with deep. " +
			"offset and next_offset count only books with an asin, in the order they were added, so every call checks limit books however many unmatched ones the filter also selects, and a retitle or a re-match between calls moves no book across the offset; clearing a book's asin takes it out of the count, so take as many off next_offset as were cleared. " +
			"not_found usually means the book came from another store's region, where the same recording has another asin: pass providers=[audible, audible.ca] or whichever store the books were bought from. Fix a wrong edition with item_match (another region via provider) then item_match_apply with override_details, or accept it; a narrator field holding a publisher is an item_edit. " +
			"With fields, every field is compared with the provider's record and the differences are listed with both values: a match applied the default way fills only empty fields, so this is what says which of the kept values the provider would have written differently. Accept the provider's by re-applying that book with override_details, or keep yours.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in matchedIn) (*mcp.CallToolResult, matchedOut, error) {
		// a wide enough tolerance calls every shorter edition the same
		// recording, and duration_off could then never be reported
		if in.Tolerance < 0 || in.Tolerance > maxDurationTolerance {
			return nil, matchedOut{}, fmt.Errorf("tolerance %g is outside 0 to %g: the editions it tells apart are ten to nineteen percent shorter, and past a tenth they count as the same recording", in.Tolerance, maxDurationTolerance)
		}
		libs, err := resolveLibraries(ctx, client, in.Library)
		if err != nil {
			return nil, matchedOut{}, err
		}
		if in.Filter != "" && in.Library == "" && len(libs) > 1 {
			return nil, matchedOut{}, fmt.Errorf("a filter needs one library (have: %s)", libraryNames(libs))
		}
		// every library is refused before any is swept: a later one on a
		// store that cannot look an asin up would have wasted the first's
		for i := range libs {
			if err := prov.lookupRefusal(in.Providers, &libs[i]); err != nil {
				return nil, matchedOut{}, err
			}
		}
		out := matchedOut{Findings: []matchedFinding{}}
		limit, offset := min(limitOr(in.Limit, 50), 100), max(in.Offset, 0)
		// with no library the matched books of every book library are one
		// run, in library order, and a window is limit books of that run: the
		// count of books walked carries on from one library to the next
		walked := new(int)
		for i := range libs {
			more, err := sweepMatched(ctx, client, prov, &libs[i], matchedScope{Providers: in.Providers, Tolerance: in.Tolerance, Limit: limit, Offset: offset, Filter: strings.TrimSpace(in.Filter), Fields: in.Fields, seen: walked}, &out)
			if err != nil {
				return nil, matchedOut{}, err
			}
			if more {
				out.NextOffset = offset + out.Scanned
				break
			}
		}

		return nil, out, nil
	})
}
