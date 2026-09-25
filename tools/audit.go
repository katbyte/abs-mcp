package tools

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/katbyte/abs-mcp/lib/abs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// auditIn is what every audit takes: which library, and how much to return.
// There is no check parameter - the tool name is the check.
type auditIn struct {
	Library string `json:"library,omitempty" jsonschema:"library name or id; default every library"`
	Limit   int    `json:"limit,omitempty"   jsonschema:"maximum findings to return, default 100, at most 1000"`
}

// auditLimitMax caps every audit's limit: a caller asking for a million rows
// gets a thousand, and total_findings still says how many there are.
const auditLimitMax = 1000

// auditLimit is limitOr capped at auditLimitMax.
func auditLimit(limit, def int) int {
	return min(limitOr(limit, def), auditLimitMax)
}

type auditFinding struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Author string `json:"author,omitempty"`
	Path   string `json:"path,omitempty"`
	Detail string `json:"detail,omitempty"`
}

type auditOut struct {
	Check    string         `json:"check"`
	Scanned  int            `json:"items_scanned"`
	Found    int            `json:"total_findings"`
	Findings []auditFinding `json:"findings"       jsonschema:"capped at limit; total_findings is the real count"`
}

// auditSpec names one audit tool and the predicate behind it. Splitting the
// checks this way costs tools but buys tool selection: the name says what it
// finds, and the description says what fixes it.
type auditSpec struct {
	Tool, Check, Description string
}

var auditSpecs = []auditSpec{
	{
		"audit_unmatched", "unmatched",
		"Find books never matched to a metadata provider: no asin and no isbn, so nothing else can be filled in automatically. Fix with item_match to see the candidates, then item_match_apply on the one that is actually the right book. A book that has been looked at and has nothing to match, a recording no provider sells, is given the provider tag's none value, zz-provider:none unless the server's --provider-tag changes the prefix (item_batch_edit add_tags), and is not reported again.",
	},
	{
		"audit_issues", "issues",
		"Find items whose folder is missing from disk or holds no playable media. These are broken records rather than metadata gaps: remove them with library_issues_remove, or item_delete one at a time.",
	},
	{
		"audit_no_audio", "no_audio",
		"Find items with no audio tracks at all, usually an ebook-only folder that landed in an audiobook library.",
	},
	{
		"audit_path", "path",
		"Find items whose folder name disagrees with their title or author: a wrong match, a chapter tag or series placeholder left as the title, a pen name under the real name's folder, or a book filed under another author. Subtitles, series prefixes, disc and edition markers are set aside first, and series, genre and lowercase shelf folders are not taken for author folders. Compare item_get with the path before fixing: the folder is usually right, and item_edit sets the title.",
	},
	{
		"audit_podcast_stale_feed", "stale_feed",
		"Find podcasts whose newest episode came out more than 90 days ago, whose feed was never checked, or that have no feed url. The show may have ended, or the feed url may be dead. Check with podcast_feed_episodes.",
	},
	{
		"audit_podcast_no_episodes", "no_episodes",
		"Find podcasts with nothing downloaded. Fill them with podcast_feed_episodes then podcast_episode_download, or podcast_check_new.",
	},
}

// missingFields are the checks that are all the same question - is this field
// empty - and so are one tool with a parameter rather than nine tools. The
// audits above each have logic of their own, which is why they are separate.
//
// chapters is here because "no chapters" is a gap like any other, but it is
// the one that applies judgement: only multi-hour books are worth flagging.
var missingFields = []string{
	"cover", "description", "narrator", "series", "author",
	"genres", "year", "publisher", "language", "chapters",
}

// auditCheck returns (detail, true) when the item is suspect.
type auditCheck func(it *abs.Item) (string, bool)

func registerAuditTools(r *registry) {
	client := r.client
	prov := r.providerConfig()

	// One tool per audit rather than one tool with eighteen switches: a model
	// picks "find the books with no cover" far more reliably than it picks
	// one audit tool with check="cover", and --allow-tools audit_* still loads
	// the family in one go.
	for _, spec := range auditSpecs {
		if _, ok := auditChecksByName[spec.Check]; !ok {
			panic("audit spec references unknown check " + spec.Check) // a build-time mistake
		}
		if spec.Tool == "audit_path" {
			continue // registerPathAudit, for its files option
		}

		add(r, readTool, &mcp.Tool{
			Name:        spec.Tool,
			Description: spec.Description,
		}, func(ctx context.Context, _ *mcp.CallToolRequest, in auditIn) (*mcp.CallToolResult, auditOut, error) {
			libs, err := resolveLibraries(ctx, client, in.Library)
			if err != nil {
				return nil, auditOut{}, err
			}

			out := auditOut{Check: spec.Check, Findings: []auditFinding{}}
			limit := auditLimit(in.Limit, 100)
			for i := range libs {
				if err := runCheck(ctx, client, prov, &libs[i], spec.Check, limit, &out); err != nil {
					return nil, auditOut{}, err
				}
			}

			return nil, out, nil
		})
	}

	type missingIn struct {
		auditIn
		Field string `json:"field" jsonschema:"cover, description, narrator, series, author, genres, year, publisher, language or chapters. description also reports a stub: under 120 characters, a short credit line such as 'Read by Paul Heck', or only a url"`
	}
	add(r, readTool, &mcp.Tool{
		Name: "audit_missing",
		Description: "Find items with a metadata field left empty: " + strings.Join(missingFields, ", ") + "; for description, also one that says nothing (under 120 characters, a short credit line such as 'Read by Paul Heck', or a bare url). " +
			"chapters only reports books over two hours, where the absence actually hurts. " +
			"Fix most of them with item_match_apply, covers with item_cover_search then item_cover_edit, chapters with item_chapters_set, " +
			"and anything the providers cannot supply with item_edit or item_batch_edit.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in missingIn) (*mcp.CallToolResult, auditOut, error) {
		field := strings.ToLower(strings.TrimSpace(in.Field))
		if !slices.Contains(missingFields, field) {
			return nil, auditOut{}, fmt.Errorf("unknown field %q; choose one of: %s", in.Field, strings.Join(missingFields, ", "))
		}

		libs, err := resolveLibraries(ctx, client, in.Library)
		if err != nil {
			return nil, auditOut{}, err
		}

		out := auditOut{Check: field, Findings: []auditFinding{}}
		limit := auditLimit(in.Limit, 100)
		for i := range libs {
			if err := runCheck(ctx, client, prov, &libs[i], field, limit, &out); err != nil {
				return nil, auditOut{}, err
			}
		}

		return nil, out, nil
	})

	type allRow struct {
		Audit string `json:"audit"`
		Field string `json:"field,omitempty" jsonschema:"pass this to audit_missing"`
		Found int    `json:"found"`
	}
	type allNotRun struct {
		Audit  string `json:"audit"`
		Field  string `json:"field,omitempty"`
		Reason string `json:"reason"`
	}
	type allIn struct {
		Library string `json:"library,omitempty" jsonschema:"library name or id; default every library"`
		Deep    bool   `json:"deep,omitempty"    jsonschema:"also run audit_covers, audit_unembedded and audit_matched, which fetch something for every item and can take minutes on a large library"`
	}
	type allOut struct {
		Scanned       int         `json:"items_scanned"`
		Total         int         `json:"total_findings"`
		Audits        []allRow    `json:"audits"                   jsonschema:"every audit with something to report, worst first; call that audit for the worklist"`
		Clean         []string    `json:"clean"                    jsonschema:"audits that ran and found nothing"`
		Skipped       []string    `json:"skipped,omitempty"        jsonschema:"audits not run: the three that fetch something for every item, unless deep is set; with deep, audit_matched when a library's provider cannot look up an asin and --providers is not set"`
		NotApplicable []string    `json:"not_applicable,omitempty" jsonschema:"audits that cannot find anything in the libraries asked about: the book audits when every one holds podcasts, the podcast audits when every one holds books. Neither run nor clean"`
		NotRun        []allNotRun `json:"not_run,omitempty"        jsonschema:"every audit in skipped and not_applicable, with why"`
		Partial       []allNotRun `json:"partial,omitempty"        jsonschema:"audits run in part, their count covering only some of their problems, with what was left out and why"`
	}
	add(r, readTool, &mcp.Tool{
		Name: "audit_all",
		Description: "Run every audit and return only the counts, so one call says where a library needs work; call the individual audit for the worklist. Start here after a scan. " +
			"The per-item checks, audit_missing for every field, audit_chapters, audit_duplicates, audit_spelling, audit_authors, audit_narrators, audit_series and audit_genres all run. " +
			"audit_covers, audit_unembedded and audit_matched fetch something for every item, so they run only with deep and are reported as skipped otherwise; audit_chapters without deep counts only one chapter over a long book, as the rest needs every chaptered book read whole, and says so under partial. " +
			"An audit that cannot find anything in the libraries asked about, a book audit over podcasts or a podcast audit over books, is listed as not applicable rather than clean; not_run says why each audit left out was left out.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in allIn) (*mcp.CallToolResult, allOut, error) {
		libs, err := resolveLibraries(ctx, client, in.Library)
		if err != nil {
			return nil, allOut{}, err
		}

		found := map[string]int{}
		dups := newDupCollector()
		spellings := newSpellingCounts(spellingFields)
		roles := newRoleCounts()
		narrators := newSpellingCounts([]string{"narrators"})
		var gaps gapsOut
		var series seriesOut
		seriesNamesCount := newSpellingCounts([]string{"series"})
		seriesAuthors := map[string]string{}
		listedSeries := map[string]bool{}
		numbering := newNumberingCollector()
		var authors authorsOut
		authorNames := newSpellingCounts([]string{"authors"})
		genres := newGenresCollector()
		genres.prov = prov
		chapters := chapterSweep{singleOnly: !in.Deep}
		var people joinedNames // roles and narrators read names, which the listing joins
		authorAsTitle := auditChecksByName["author_as_title"]
		staleFeed := auditChecksByName["stale_feed"]
		var covers coversOut
		var embedded auditOut
		var matched matchedOut
		// audit_matched refuses a library whose asins would all come back not
		// found, and deep skips it for the same reason rather than count them
		var noLookup error
		for i := 0; i < len(libs) && noLookup == nil; i++ {
			noLookup = prov.lookupRefusal(nil, &libs[i])
		}
		out := allOut{Audits: []allRow{}, Clean: []string{}}
		for i := range libs {
			lib := &libs[i]
			// one pass over the library evaluating every predicate and
			// feeding every collector, rather than one pass per audit
			var feeds []string // podcasts stale_feed has to see whole
			if err := client.ItemsAll(ctx, lib.ID, abs.ItemsOptions{}, func(items []abs.Item) bool {
				for j := range items {
					it := &items[j]
					out.Scanned++
					for _, spec := range auditSpecs {
						if spec.Check == "stale_feed" && staleFeedNeedsRecord(it) {
							feeds = append(feeds, it.ID)
							continue
						}
						if _, suspect := prov.auditCheck(spec.Check)(it); suspect {
							found[spec.Tool]++
						}
					}
					for _, field := range missingFields {
						if _, suspect := auditChecksByName[field](it); suspect {
							found["audit_missing/"+field]++
						}
					}
					if _, suspect := authorAsTitle(it); suspect {
						authors.Counts.AuthorAsTitle++
					}
					dups.add(it)
					spellings.add(it)
					if !people.hold(it) {
						roles.add(it)
						narrators.add(it)
					}
					genres.add(it)
					numbering.add(it)
					chapters.add(it)
				}
				return true
			}); err != nil {
				return nil, allOut{}, err
			}
			if err := podcastRecords(ctx, client, feeds, func(it *abs.Item) {
				if _, suspect := staleFeed(it); suspect {
					found["audit_podcast_stale_feed"]++
				}
			}); err != nil {
				return nil, allOut{}, err
			}
			if err := chapters.resolve(ctx, client); err != nil {
				return nil, allOut{}, err
			}
			if err := people.resolve(ctx, client, lib.ID, func(it *abs.Item) {
				roles.add(it)
				narrators.add(it)
			}); err != nil {
				return nil, allOut{}, err
			}

			// the audits that need more than the listing: one query per
			// multi-book series, and the author records, both small next
			// to the sweep. A limit of 0 keeps the counts and no rows.
			if err := sweepSeriesGaps(ctx, client, lib, 0, &gaps); err != nil {
				return nil, allOut{}, err
			}
			if err := seriesNames(ctx, client, lib, &series, seriesNamesCount, seriesAuthors, listedSeries, false); err != nil {
				return nil, allOut{}, err
			}
			if err := authorRecords(ctx, client, lib, &authors, authorNames); err != nil {
				return nil, allOut{}, err
			}
			if in.Deep {
				if err := sweepCoverLocal(ctx, client, lib.ID, coverLocalScope{Tolerance: defaultCoverTolerance, MinPixels: defaultCoverMinPixels}, &covers); err != nil {
					return nil, allOut{}, err
				}
				if err := sweepUnembedded(ctx, client, lib, 0, &embedded); err != nil {
					return nil, allOut{}, err
				}
				if noLookup == nil {
					if _, err := sweepMatched(ctx, client, prov, lib, matchedScope{}, &matched); err != nil {
						return nil, allOut{}, err
					}
				}
			}
		}
		if err := numbering.resolve(ctx, client); err != nil {
			return nil, allOut{}, err
		}
		if err := unlistedSeries(ctx, client, numbering, listedSeries, &series, seriesNamesCount, seriesAuthors, false); err != nil {
			return nil, allOut{}, err
		}
		spellings.dropMarkers(prov)
		found["audit_duplicates"] = len(dups.groups())
		found["audit_spelling"] = spellings.findingCount() + len(oddLanguages(spellings))
		found["audit_narrators"] = len(roles.findings()) + narrators.findingCount()
		found["audit_series"] = gaps.Found + len(withSeriesAuthors(seriesNamesCount.report("series"), seriesAuthors)) + len(series.Odd) + len(numbering.findings()) + len(numbering.titleFindings())
		found["audit_authors"] = authors.Counts.AuthorAsTitle + len(authors.Records) + authorNames.findingCount()
		found["audit_genres"] = genres.findings(5, 0).Found
		found["audit_chapters"] = len(chapters.rows)
		if in.Deep {
			found["audit_covers"] = covers.Found
			found["audit_unembedded"] = embedded.Found
			found["audit_matched"] = matched.Found
		}

		// an audit that can only fire for a book says nothing about a
		// library of podcasts: it is not applicable there, not clean
		hasBooks := slices.ContainsFunc(libs, func(l abs.Library) bool { return !l.IsPodcast() })
		hasPodcasts := slices.ContainsFunc(libs, func(l abs.Library) bool { return l.IsPodcast() })
		notRun := func(tool, field, reason string) {
			out.NotRun = append(out.NotRun, allNotRun{Audit: tool, Field: field, Reason: reason})
		}
		report := func(tool, field string) {
			name := strings.TrimSpace(tool + " " + field)
			switch scope := allScope(tool, field); {
			case scope == "book" && !hasBooks:
				out.NotApplicable = append(out.NotApplicable, name)
				notRun(tool, field, "only a book can have this, and every library asked about holds podcasts")
				return
			case scope == "podcast" && !hasPodcasts:
				out.NotApplicable = append(out.NotApplicable, name)
				notRun(tool, field, "only a podcast can have this, and every library asked about holds books")
				return
			}
			key := tool
			if field != "" {
				key += "/" + field
			}
			if n := found[key]; n > 0 {
				out.Audits = append(out.Audits, allRow{Audit: tool, Field: field, Found: n})
				out.Total += n
			} else {
				out.Clean = append(out.Clean, name)
			}
		}
		for _, spec := range auditSpecs {
			report(spec.Tool, "")
		}
		for _, field := range missingFields {
			report("audit_missing", field)
		}
		for _, tool := range []string{"audit_chapters", "audit_duplicates", "audit_spelling", "audit_authors", "audit_narrators", "audit_series", "audit_genres"} {
			report(tool, "")
		}
		if !in.Deep && hasBooks {
			out.Partial = append(out.Partial, allNotRun{Audit: "audit_chapters", Reason: "counted one chapter over a long book only: chapters past the end, out of order or short need every chaptered book read whole, fifty to a request: pass deep, or run audit_chapters"})
		}
		for _, deep := range []struct{ tool, why string }{
			{"audit_covers", "reads every cover file, one request per book: pass deep to run it"},
			{"audit_unembedded", "fetches every audio file's tags: pass deep to run it"},
			{"audit_matched", "asks the provider about every matched book: pass deep to run it"},
		} {
			if deep.tool == "audit_matched" && in.Deep && noLookup != nil {
				deep.why = noLookup.Error()
			} else if in.Deep || !hasBooks {
				report(deep.tool, "")
				continue
			}
			out.Skipped = append(out.Skipped, deep.tool)
			notRun(deep.tool, "", deep.why)
		}
		slices.SortFunc(out.Audits, func(a, b allRow) int {
			if a.Found != b.Found {
				return b.Found - a.Found
			}
			if a.Audit != b.Audit {
				return strings.Compare(a.Audit, b.Audit)
			}
			return strings.Compare(a.Field, b.Field)
		})

		return nil, out, nil
	})

	type dupIn struct {
		Library string `json:"library,omitempty" jsonschema:"library name or id; default all libraries"`
		Limit   int    `json:"limit,omitempty"   jsonschema:"maximum groups to return, default 50, at most 1000"`
	}
	add(r, readTool, &mcp.Tool{
		Name: "audit_duplicates",
		Description: "Find items that appear to be the same work: sharing an asin, an isbn, or a title and author, any one of them, so a matched copy and an unmatched copy of one book are found together. Two copies with different asins are never grouped: those are editions (a full-cast and a single-narrator recording), not duplicates. " +
			"Each group lists every copy with size, duration and path so you can pick which to keep.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in dupIn) (*mcp.CallToolResult, dupOut, error) {
		libs, err := resolveLibraries(ctx, client, in.Library)
		if err != nil {
			return nil, dupOut{}, err
		}

		collector := newDupCollector()
		for i := range libs {
			if err := client.ItemsAll(ctx, libs[i].ID, abs.ItemsOptions{}, func(items []abs.Item) bool {
				for i := range items {
					collector.add(&items[i])
				}
				return true
			}); err != nil {
				return nil, dupOut{}, err
			}
		}

		groups := collector.groups()
		out := dupOut{Scanned: len(collector.items), Found: len(groups), Groups: []dupGroup{}}
		limit := auditLimit(in.Limit, 50)
		out.Groups = append(out.Groups, groups[:min(len(groups), limit)]...)

		return nil, out, nil
	})
}

type dupGroup struct {
	Key   string        `json:"key"   jsonschema:"what the copies share: asin, isbn, title+author, or several of them joined by a comma"`
	Items []itemSummary `json:"items"`
}

type dupOut struct {
	Scanned int        `json:"items_scanned"`
	Found   int        `json:"total_findings" jsonschema:"groups, before limit"`
	Groups  []dupGroup `json:"groups"         jsonschema:"each group is one work with several copies"`
}

// dupCollector files each item under everything that would make two items
// the same work - its asin, its isbn, and its title and author - and joins
// the items that share any of them. A priority chain (the asin, else the
// isbn, else the title) put a matched copy and the unmatched copy scanned in
// beside it under different keys, and that is the duplicate a library
// collects most. It holds the summary rather than the item: a whole library
// is here until the sweep ends, and the summary is what the answer carries.
type dupCollector struct {
	items []itemSummary
	asins []string         // each item's asin, "" when it has none
	byKey map[string][]int // key -> the items filed under it, in sweep order
}

func newDupCollector() *dupCollector {
	return &dupCollector{byKey: map[string][]int{}}
}

func (d *dupCollector) add(it *abs.Item) {
	m := &it.Media.Metadata
	i := len(d.items)
	asin := strings.ToUpper(strings.TrimSpace(m.ASIN))
	d.items = append(d.items, summarize(it))
	d.asins = append(d.asins, asin)
	var keys []string
	if asin != "" {
		keys = append(keys, "asin:"+asin)
	}
	if isbn := strings.ReplaceAll(strings.TrimSpace(m.ISBN), "-", ""); isbn != "" {
		keys = append(keys, "isbn:"+isbn)
	}
	// an item with no title shares nothing with another untitled one
	if title := strings.ToLower(strings.TrimSpace(m.Title)); title != "" {
		keys = append(keys, "title:"+title+"|"+strings.ToLower(strings.TrimSpace(m.AuthorDisplay())))
	}
	for _, k := range keys {
		d.byKey[k] = append(d.byKey[k], i)
	}
}

// groups joins the items that share a key into groups of more than one,
// sorted by what they share. The asins are joined first, then the isbns,
// then the titles, and two items with different asins are never joined: an
// unmatched copy goes with the first matched edition its title reaches.
func (d *dupCollector) groups() []dupGroup {
	parent := make([]int, len(d.items))
	asin := slices.Clone(d.asins) // for each root, the asin its group carries
	for i := range parent {
		parent[i] = i
	}
	find := func(i int) int {
		for parent[i] != i {
			parent[i] = parent[parent[i]]
			i = parent[i]
		}
		return i
	}
	keys := make([]string, 0, len(d.byKey))
	for k := range d.byKey {
		keys = append(keys, k)
	}
	slices.Sort(keys) // asin: before isbn: before title:
	for _, k := range keys {
		members := d.byKey[k]
		for _, m := range members[1:] {
			ra, rb := find(members[0]), find(m)
			if ra == rb || (asin[ra] != "" && asin[rb] != "" && asin[ra] != asin[rb]) {
				continue
			}
			parent[rb] = ra
			if asin[ra] == "" {
				asin[ra] = asin[rb]
			}
		}
	}

	byRoot := map[int][]int{}
	for i := range d.items {
		r := find(i)
		byRoot[r] = append(byRoot[r], i)
	}
	// what each group shares: every key two of its items are filed under
	shared := map[int][]string{}
	for _, k := range keys {
		counted := map[int]int{}
		for _, m := range d.byKey[k] {
			counted[find(m)]++
		}
		for r, n := range counted {
			if n > 1 {
				shared[r] = append(shared[r], k)
			}
		}
	}
	var out []dupGroup
	for r, members := range byRoot {
		if len(members) < 2 {
			continue
		}
		g := dupGroup{Key: strings.Join(shared[r], ", ")}
		for _, m := range members {
			g.Items = append(g.Items, d.items[m])
		}
		out = append(out, g)
	}
	slices.SortFunc(out, func(a, b dupGroup) int {
		if a.Key != b.Key {
			return strings.Compare(a.Key, b.Key)
		}
		return strings.Compare(a.Items[0].ID, b.Items[0].ID)
	})
	return out
}

type seriesGap struct {
	ID       string        `json:"id"`
	Name     string        `json:"name"`
	Author   string        `json:"author,omitempty"`
	Books    int           `json:"books"`
	Have     []string      `json:"have"               jsonschema:"sequence numbers present, in order"`
	Missing  []string      `json:"missing"            jsonschema:"whole numbers absent between the lowest and the highest present, outliers aside"`
	Outliers []string      `json:"outliers,omitempty" jsonschema:"numbers far from the rest of the series, a year or a date typed as the sequence (#2019 beside #1 and #2): fix the book's number with item_edit. The gaps are read without them"`
	Unlinked []gapUnlinked `json:"unlinked,omitempty" jsonschema:"audit_series only: books already in the library whose folder carries a missing number but which are not in the series; suggest is the series value for item_edit add_series"`
	Merged   *gapMerged    `json:"merged,omitempty"   jsonschema:"audit_series only: when names reports this series as one of several spellings, what is still missing once they are one series"`
}

// gapUnlinked is a missing number that is not missing at all: the book is on
// the shelf under the right folder and simply not linked to the series.
type gapUnlinked struct {
	Missing string `json:"missing"`
	ID      string `json:"id"`
	Path    string `json:"path"`
	Suggest string `json:"suggest" jsonschema:"the series value for item_edit add_series"`
}

// gapMerged is a gap read across every spelling of the series at once.
type gapMerged struct {
	With    []string `json:"with"    jsonschema:"the other spellings the names section joins this series with"`
	Missing []string `json:"missing" jsonschema:"numbers still absent once the spellings are one series; empty when the other spelling fills every gap"`
}

type gapsOut struct {
	Scanned int         `json:"series_scanned"`
	Found   int         `json:"total_findings" jsonschema:"series with gaps, before limit"`
	Series  []seriesGap `json:"series"`
}

// sweepSeriesGaps checks every multi-book series in a book library for
// interior gaps and numbers far from the rest, counting them all and keeping
// at most limit as rows. A podcast library has no series and is skipped.
func sweepSeriesGaps(ctx context.Context, client *abs.Client, lib *abs.Library, limit int, out *gapsOut) error {
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
			out.Scanned++
			// an interior gap needs a book on either side of it
			if len(s.Books) < 2 {
				continue
			}

			// the series listing carries no sequence numbers, and neither
			// does a plain item listing: only an item query filtered by the
			// series does (see docs/README.md)
			res, err := client.Items(ctx, lib.ID, abs.ItemsOptions{
				Limit:  len(s.Books),
				Filter: abs.EncodeFilter("series", s.ID),
			})
			if err != nil {
				return err
			}

			have, nums := seriesNumbers(res.Results, s.ID)
			missing := missingBetween(nums)
			_, outliers := splitOutliers(nums)
			if len(missing) == 0 && len(outliers) == 0 {
				continue
			}
			out.Found++
			if len(out.Series) >= limit {
				continue
			}
			row := seriesGap{ID: s.ID, Name: s.Name, Books: len(s.Books), Have: have, Missing: missing}
			if row.Missing == nil {
				row.Missing = []string{}
			}
			for _, n := range outliers {
				row.Outliers = append(row.Outliers, strconv.FormatFloat(n, 'f', -1, 64))
			}
			if len(res.Results) > 0 {
				row.Author = res.Results[0].Media.Metadata.AuthorDisplay()
			}
			out.Series = append(out.Series, row)
		}
		if len(series) == 0 || (page+1)*seriesPageSize >= total {
			return nil
		}
	}
}

// seriesSequences returns the numeric sequence numbers items carry for the
// series seriesID, in order, and the whole numbers missing between the lowest
// and the highest. Only interior gaps count: a series whose first book is #3
// is not assumed to be missing #1 and #2, and decimal sequences (novellas such
// as 4.5) never create a gap.
//
// items must come from a series-filtered query; the series listing and plain
// item listings both return metadata with no sequence at all.
func seriesSequences(items []abs.Item, seriesID string) (have, missing []string) {
	have, nums := seriesNumbers(items, seriesID)
	return have, missingBetween(nums)
}

// seriesNumbers is the sequence numbers items carry for the series seriesID,
// as written and as numbers, both in order and each number once.
func seriesNumbers(items []abs.Item, seriesID string) (have []string, nums []float64) {
	seen := map[float64]string{}
	for i := range items {
		for _, ref := range items[i].Media.Metadata.Series {
			if ref.ID != seriesID {
				continue
			}
			n, ok := sequenceNumber(ref.Sequence)
			if !ok {
				continue
			}
			if _, dup := seen[n]; !dup {
				seen[n] = ref.Sequence
			}
		}
	}
	if len(seen) == 0 {
		return nil, nil
	}

	nums = make([]float64, 0, len(seen))
	for n := range seen {
		nums = append(nums, n)
	}
	slices.Sort(nums)
	for _, n := range nums {
		have = append(have, seen[n])
	}
	return have, nums
}

// sequenceNumber reads a series sequence as a number. "inf" and "NaN" parse
// as floats and are not a place in a series; walking up to infinity one book
// at a time never ends.
func sequenceNumber(s string) (float64, bool) {
	n, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || math.IsInf(n, 0) || math.IsNaN(n) {
		return 0, false
	}
	return n, true
}

// outlierSpan is how far past its count a series' numbers may spread before
// the ones far from the rest are set aside: twenty numbers a book, and never
// less than two hundred, so a collector holding five books of a forty-book
// series still has its gaps read.
const (
	outlierSpanPerBook = 20
	outlierSpanMin     = 200
)

// splitOutliers sets aside the numbers far from the rest of a sorted list: a
// year or a date typed as the sequence (#2019 beside #1 and #2) would
// otherwise read as two thousand missing books, and a date as twenty million.
// While the numbers spread further than the count allows, they are cut at
// their widest jump and the smaller side is set aside.
func splitOutliers(nums []float64) (kept, outliers []float64) {
	kept = nums
	for len(kept) > 1 && kept[len(kept)-1]-kept[0] > float64(max(outlierSpanMin, outlierSpanPerBook*len(kept))) {
		cut := 1
		for i := 2; i < len(kept); i++ {
			if kept[i]-kept[i-1] > kept[cut]-kept[cut-1] {
				cut = i
			}
		}
		if cut < len(kept)-cut { // the low side is smaller; on a tie the high one goes, a year being the usual stray
			outliers = append(outliers, kept[:cut]...)
			kept = kept[cut:]
		} else {
			outliers = append(outliers, kept[cut:]...)
			kept = kept[:cut]
		}
	}
	slices.Sort(outliers)
	return kept, outliers
}

// missingBetween is the whole numbers absent between the lowest and the
// highest of a sorted list: interior gaps only, a fraction such as 4.5
// never opens one, and the outliers are left out.
func missingBetween(nums []float64) []string {
	nums, _ = splitOutliers(nums)
	if len(nums) == 0 {
		return nil
	}
	seen := make(map[float64]bool, len(nums))
	for _, n := range nums {
		seen[n] = true
	}
	var missing []string
	for n := math.Ceil(nums[0]); n < nums[len(nums)-1]; n++ {
		if !seen[n] {
			missing = append(missing, strconv.FormatFloat(n, 'f', -1, 64))
		}
	}
	return missing
}

// bookOnlyChecks never fire for a podcast, so a podcast library is neither
// swept for them nor asked with a book filter its own filters do not know.
var bookOnlyChecks = map[string]bool{
	"unmatched": true, "author_as_title": true, "no_audio": true,
	"narrator": true, "series": true, "year": true, "publisher": true, "chapters": true,
	"path": true,
}

// podcastOnlyChecks never fire for a book.
var podcastOnlyChecks = map[string]bool{"stale_feed": true, "no_episodes": true}

// bookOnlyAudits are the audits beyond the per-item checks that look at
// books alone.
var bookOnlyAudits = map[string]bool{
	"audit_authors": true, "audit_narrators": true, "audit_series": true, "audit_genres": true, "audit_chapters": true,
	"audit_covers": true, "audit_unembedded": true, "audit_matched": true,
}

// allScope is the kind of library an audit_all row can find anything in:
// "book", "podcast", or "" for either.
func allScope(tool, field string) string {
	check := field
	if field == "" {
		if i := slices.IndexFunc(auditSpecs, func(s auditSpec) bool { return s.Tool == tool }); i >= 0 {
			check = auditSpecs[i].Check
		}
	}
	switch {
	case bookOnlyChecks[check], bookOnlyAudits[tool]:
		return "book"
	case podcastOnlyChecks[check]:
		return "podcast"
	}
	return ""
}

// runCheck evaluates one named predicate over a library, using the server's
// own filter when it has one (far cheaper than a sweep) and paging otherwise.
func runCheck(ctx context.Context, client *abs.Client, prov providerConfig, lib *abs.Library, name string, limit int, out *auditOut) error {
	if lib.IsPodcast() && bookOnlyChecks[name] {
		return nil
	}
	check := prov.auditCheck(name)

	// a podcast library's listing ignores the missing filters and answers
	// with every podcast, so only issues, which it does know, is asked
	// there; everything else is swept
	if filter := nativeFilter[name]; filter != "" && (!lib.IsPodcast() || name == "issues") {
		// the filtered query says how many match, not how many were looked
		// at; the library's size is one more request, for one row
		all, err := client.Items(ctx, lib.ID, abs.ItemsOptions{Limit: 1, Minified: true})
		if err != nil {
			return err
		}
		out.Scanned += all.Total

		// once the worklist is full only the count is wanted, and a limit of 0
		// would mean "no limit" to the server: ask for one row and keep none
		remaining := max(limit-len(out.Findings), 1)
		res, err := client.Items(ctx, lib.ID, abs.ItemsOptions{Limit: remaining, Filter: filter, Minified: true})
		if err != nil {
			return err
		}
		out.Found += res.Total
		for j := range res.Results {
			// the predicate has the last word, as it has in audit_all: a row
			// the filter let through that the predicate clears is not a finding
			detail, suspect := check(&res.Results[j])
			if !suspect {
				out.Found--
				continue
			}
			if len(out.Findings) < limit {
				out.Findings = append(out.Findings, finding(&res.Results[j], detail))
			}
		}
		return nil
	}

	judge := func(it *abs.Item) {
		detail, suspect := check(it)
		if !suspect {
			return
		}
		out.Found++
		if len(out.Findings) < limit {
			out.Findings = append(out.Findings, finding(it, detail))
		}
	}
	var feeds []string // podcasts stale_feed has to see whole
	if err := client.ItemsAll(ctx, lib.ID, abs.ItemsOptions{}, func(items []abs.Item) bool {
		for j := range items {
			out.Scanned++
			if name == "stale_feed" && staleFeedNeedsRecord(&items[j]) {
				feeds = append(feeds, items[j].ID)
				continue
			}
			judge(&items[j])
		}
		return true
	}); err != nil {
		return err
	}
	return podcastRecords(ctx, client, feeds, judge)
}

// podcastBatchSize is how many podcasts one batch request asks for: a whole
// podcast carries every episode it holds, which runs to hundreds.
const podcastBatchSize = 10

// podcastRecords fetches podcasts whole, a batch at a time, and hands each to
// fn.
func podcastRecords(ctx context.Context, client *abs.Client, ids []string, fn func(it *abs.Item)) error {
	for chunk := range slices.Chunk(ids, podcastBatchSize) {
		items, err := client.ItemsBatch(ctx, chunk)
		if err != nil {
			return err
		}
		for j := range items {
			fn(&items[j])
		}
	}
	return nil
}

// staleFeedNeedsRecord reports whether stale_feed has to see the whole
// podcast: the listing says how many episodes a podcast holds, not when the
// newest came out.
func staleFeedNeedsRecord(it *abs.Item) bool {
	return it.IsPodcast() && it.Media.Metadata.FeedURL != "" && it.Media.LastEpisodeCheck != 0 &&
		len(it.Media.Episodes) == 0 && it.Media.NumEpisodes > 0
}

func finding(it *abs.Item, detail string) auditFinding {
	return auditFinding{
		ID:     it.ID,
		Title:  it.Title(),
		Author: it.Media.Metadata.AuthorDisplay(),
		Path:   it.RelPath,
		Detail: detail,
	}
}

// nativeFilter maps checks the server can filter itself to the encoded
// filter value.
var nativeFilter = map[string]string{
	"issues":    "issues",
	"cover":     abs.EncodeFilter("missing", "cover"),
	"narrator":  abs.EncodeFilter("missing", "narrators"),
	"series":    abs.EncodeFilter("missing", "series"),
	"author":    abs.EncodeFilter("missing", "authors"),
	"genres":    abs.EncodeFilter("missing", "genres"),
	"year":      abs.EncodeFilter("missing", "publishedYear"),
	"publisher": abs.EncodeFilter("missing", "publisher"),
	"language":  abs.EncodeFilter("missing", "language"),
	"no_audio":  abs.EncodeFilter("tracks", "none"),
}

const (
	chapterlessMinHours = 2
	staleFeedDays       = 90
	seriesPageSize      = 100
)

var auditChecksByName = map[string]auditCheck{
	// a book the provider tag marks as having nothing to match is left out
	// by providerConfig.auditCheck, which knows the server's tag prefix
	"unmatched": func(it *abs.Item) (string, bool) {
		if it.IsPodcast() {
			return "", false
		}
		m := it.Media.Metadata
		if m.ASIN == "" && m.ISBN == "" {
			return "no asin or isbn", true
		}
		return "", false
	},
	"author_as_title": func(it *abs.Item) (string, bool) {
		if it.IsPodcast() {
			return "", false
		}
		m := it.Media.Metadata
		author := m.AuthorDisplay()
		if author == "" || m.Title == "" {
			return "", false
		}
		// an import that could not find an author sometimes copies the title
		// into the field, which then creates a bogus author record too
		if norm(author) == norm(m.Title) {
			return fmt.Sprintf("author %q is the title", author), true
		}
		return "", false
	},
	"cover": func(it *abs.Item) (string, bool) {
		if !it.HasCover() {
			return "no cover", true
		}
		return "", false
	},
	"description": func(it *abs.Item) (string, bool) {
		return stubDescription(it.Media.Metadata.Description)
	},
	"narrator": func(it *abs.Item) (string, bool) {
		if !it.IsPodcast() && it.Media.Metadata.NarratorDisplay() == "" {
			return "no narrator", true
		}
		return "", false
	},
	"series": func(it *abs.Item) (string, bool) {
		if !it.IsPodcast() && len(it.Media.Metadata.SeriesDisplay()) == 0 {
			return "no series", true
		}
		return "", false
	},
	"author": func(it *abs.Item) (string, bool) {
		if it.Media.Metadata.AuthorDisplay() == "" {
			return "no author", true
		}
		return "", false
	},
	"genres": func(it *abs.Item) (string, bool) {
		if len(it.Media.Metadata.Genres) == 0 {
			return "no genres", true
		}
		return "", false
	},
	"year": func(it *abs.Item) (string, bool) {
		if !it.IsPodcast() && it.Media.Metadata.PublishedYear == "" {
			return "no published year", true
		}
		return "", false
	},
	"publisher": func(it *abs.Item) (string, bool) {
		if !it.IsPodcast() && it.Media.Metadata.Publisher == "" {
			return "no publisher", true
		}
		return "", false
	},
	"language": func(it *abs.Item) (string, bool) {
		if it.Media.Metadata.Language == "" {
			return "no language", true
		}
		return "", false
	},
	"chapters": func(it *abs.Item) (string, bool) {
		if it.IsPodcast() || it.Media.NumChapters > 0 || it.Media.Duration < chapterlessMinHours*3600 {
			return "", false
		}
		// one unchaptered file cannot be navigated at all; several at least
		// break at track boundaries, so say which this is
		if it.Media.NumTracks == 1 {
			return fmt.Sprintf("one unchaptered %s file: no way to navigate it", fmtDuration(it.Media.Duration)), true
		}
		return fmt.Sprintf("%s of audio in %d files with no chapters", fmtDuration(it.Media.Duration), it.Media.NumTracks), true
	},
	"issues": func(it *abs.Item) (string, bool) {
		switch {
		case it.IsMissing:
			return "folder missing from disk", true
		case it.IsInvalid:
			return "no playable media in folder", true
		}
		return "", false
	},
	"no_audio": func(it *abs.Item) (string, bool) {
		if !it.IsPodcast() && it.Media.NumTracks == 0 && it.Media.NumAudioFiles == 0 {
			if it.Media.EbookFormat != "" {
				return "ebook only (" + it.Media.EbookFormat + ")", true
			}
			return "no audio files", true
		}
		return "", false
	},
	"path": checkPath,
	"stale_feed": func(it *abs.Item) (string, bool) {
		if !it.IsPodcast() {
			return "", false
		}
		if it.Media.Metadata.FeedURL == "" {
			return "no feed url", true
		}
		if abs.Millis(it.Media.LastEpisodeCheck).IsZero() {
			return "feed never checked", true
		}
		// the server stamps lastEpisodeCheck on every check, whatever it
		// finds, so it says the feed was read, not that anything came of it:
		// the newest episode's date is what says the show is still going. The
		// listing does not carry episodes; the sweeps fetch the podcasts
		// that need it (staleFeedNeedsRecord)
		var newest int64
		for i := range it.Media.Episodes {
			newest = max(newest, it.Media.Episodes[i].PublishedAt)
		}
		if newest == 0 {
			return "", false // nothing held, or nothing dated: no_episodes is the audit for the first
		}
		if age := time.Since(abs.Millis(newest)); age > staleFeedDays*24*time.Hour {
			return fmt.Sprintf("no new episode in %d days: the newest came out %s", int(age.Hours()/24), fmtDate(newest)), true
		}
		return "", false
	},
	"no_episodes": func(it *abs.Item) (string, bool) {
		if it.IsPodcast() && it.Media.NumEpisodes == 0 && len(it.Media.Episodes) == 0 {
			return "no episodes downloaded", true
		}
		return "", false
	},
}

// lastFirstMatch treats "Last, First" folders as matching "First Last".
func lastFirstMatch(folder, author string) bool {
	parts := strings.Fields(author)
	if len(parts) < 2 {
		return false
	}
	return strings.Contains(folder, parts[len(parts)-1]+" "+strings.Join(parts[:len(parts)-1], " "))
}

// norm lowercases and strips punctuation so folder names and metadata
// compare loosely. Accented Latin letters are folded to plain ones; any other
// letter or digit is kept as it is, lower-cased, so a Cyrillic, Greek or
// Japanese title is itself rather than nothing (and SF小説 is not SF映画).
func norm(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '_' || r == '-' || r == '.' || unicode.IsSpace(r):
			b.WriteRune(' ')
		case r > 127:
			if folded := foldLetter(r); folded != "" {
				b.WriteString(folded) // Nesbø is Nesbo, not Nesb
			} else if unicode.IsLetter(r) || unicode.IsDigit(r) {
				b.WriteRune(r)
			}
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// foldLetter is the plain-ASCII spelling of an accented Latin letter, or
// nothing for a rune that is not one. Enough for the names a library holds.
func foldLetter(r rune) string {
	for ascii, accented := range foldTable {
		if strings.ContainsRune(accented, r) {
			return ascii
		}
	}
	return ""
}

var foldTable = map[string]string{
	"a": "àáâãäåāąă", "ae": "æ", "c": "çćčċ", "d": "ďđð", "e": "èéêëēęěė", "g": "ğģ",
	"i": "ìíîïīıį", "l": "łļľ", "n": "ñńňņ", "o": "òóôõöøōőœ", "r": "řŗ", "s": "šşśș", "ss": "ß",
	"t": "ťţț", "th": "þ", "u": "ùúûüūůűų", "y": "ýÿ", "z": "žźż",
}

// errNotBook is returned by tools that only make sense for books.
var errNotBook = errors.New("this item is a podcast, not a book")

// The description field is where importers leave notes: "Read by Paul Heck",
// "Unabridged", the title again, a bare url. None of those is a description,
// and audit_missing description reports them beside the empty ones. HTML is
// stripped first, since the server stores descriptions with markup.
var (
	descriptionTags    = regexp.MustCompile(`<[^>]*>`)
	descriptionCredit  = regexp.MustCompile(`(?i)^(read|narrated|performed|narration|introduction|foreword)\s+by\b`)
	descriptionOnlyURL = regexp.MustCompile(`(?i)^https?://\S+$`)
)

// creditLineMax is the length past which a description that opens with a
// credit is a description with a credit in front ("Introduction by Neil
// Gaiman. In 1666...") rather than a credit line.
const creditLineMax = 240

// stubDescription reports a description that says nothing: empty, shorter
// than descriptionStub, a short credit line, or a bare url.
func stubDescription(raw string) (string, bool) {
	text := strings.TrimSpace(descriptionTags.ReplaceAllString(raw, " "))
	text = strings.Join(strings.Fields(text), " ")
	switch {
	case text == "":
		return "no description", true
	case len(text) < creditLineMax && descriptionCredit.MatchString(text):
		return fmt.Sprintf("description is a credit line: %q", clip(text, 80)), true
	case descriptionOnlyURL.MatchString(text):
		return fmt.Sprintf("description is only a url: %q", clip(text, 80)), true
	case len(text) < descriptionStub:
		return fmt.Sprintf("description is a stub, %d characters: %q", len(text), clip(text, 80)), true
	}
	return "", false
}
