package tools

import (
	"context"
	"errors"
	"fmt"
	"math"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/katbyte/abs-mcp/lib/abs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// audit checks. Detection is code, correction is judgment: each check is a
// cheap deterministic sweep that produces a worklist for the AI to act on
// with item_match, item_edit or item_cover_set.
const auditChecks = "unmatched (no asin and no isbn: never matched to a provider), cover, description, narrator, series, author, genres, year, publisher, language, " +
	"chapters (multi-hour audio with no chapters), " +
	"issues (folder missing or no playable media), no_audio (ebook-only), path (folder name disagrees with the metadata title/author), " +
	"stale_feed (podcasts: no new episodes in 90 days or the feed was never checked), no_episodes (podcasts with nothing downloaded), " +
	"author_as_title (the author field holds the title, a common import mistake)"

// auditIn is what every audit takes: which library, and how much to return.
// There is no check parameter - the tool name is the check.
type auditIn struct {
	Library string `json:"library,omitempty" jsonschema:"library name or id; default every library"`
	Limit   int    `json:"limit,omitempty"   jsonschema:"maximum findings to return, default 100"`
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
	{"audit_unmatched", "unmatched",
		"Find books never matched to a metadata provider: no asin and no isbn, so nothing else can be filled in automatically. Fix with item_match then item_match_apply, or library_match_all for a whole library."},
	{"audit_issues", "issues",
		"Find items whose folder is missing from disk or holds no playable media. These are broken records rather than metadata gaps: remove them with library_remove_issues, or item_delete one at a time."},
	{"audit_no_audio", "no_audio",
		"Find items with no audio tracks at all, usually an ebook-only folder that landed in an audiobook library."},
	{"audit_path", "path",
		"Find items whose folder name disagrees with their title or author, which usually means the metadata was matched to the wrong book. Compare item_get with the path before fixing."},
	{"audit_author_as_title", "author_as_title",
		"Find items whose author field holds the title, a common import mistake that also creates a bogus author record. Fix with item_edit, then author_delete the stray author."},
	{"audit_single_chapter", "single_chapter",
		"Find long books carrying exactly one chapter that spans the whole recording, which is as unnavigable as having none but is not reported by audit_missing chapters. Fix with item_chapters_set, which can pull real chapters from Audible by asin."},
	{"audit_stale_feed", "stale_feed",
		"Find podcasts with no new episodes in 90 days, or whose feed was never checked. The show may have ended, or the feed url may be dead. Check with podcast_feed_episodes."},
	{"audit_no_episodes", "no_episodes",
		"Find podcasts with nothing downloaded. Fill them with podcast_feed_episodes then podcast_episode_download, or podcast_check_new."},
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

	// One tool per audit rather than one tool with eighteen switches: a model
	// picks "find the books with no cover" far more reliably than it picks
	// one audit tool with check="cover", and --allow-tools audit_* still loads
	// the family in one go.
	for _, spec := range auditSpecs {
		check, ok := auditChecksByName[spec.Check]
		if !ok {
			panic("audit spec references unknown check " + spec.Check) // a build-time mistake
		}
		filter := nativeFilter[spec.Check]

		add(r, readTool, &mcp.Tool{
			Name:        spec.Tool,
			Description: spec.Description,
		}, func(ctx context.Context, _ *mcp.CallToolRequest, in auditIn) (*mcp.CallToolResult, auditOut, error) {
			libs, err := resolveLibraries(ctx, client, in.Library)
			if err != nil {
				return nil, auditOut{}, err
			}

			out := auditOut{Check: spec.Check, Findings: []auditFinding{}}
			limit := limitOr(in.Limit, 100)
			for i := range libs {
				if err := runCheck(ctx, client, libs[i].ID, check, filter, limit, &out); err != nil {
					return nil, auditOut{}, err
				}
			}

			return nil, out, nil
		})
	}

	type missingIn struct {
		auditIn
		Field string `json:"field" jsonschema:"cover, description, narrator, series, author, genres, year, publisher, language or chapters"`
	}
	add(r, readTool, &mcp.Tool{
		Name: "audit_missing",
		Description: "Find items with a metadata field left empty: " + strings.Join(missingFields, ", ") + ". " +
			"chapters only reports books over two hours, where the absence actually hurts. " +
			"Fix most of them with item_match_apply, covers with item_cover_search then item_cover_set, chapters with item_chapters_set, " +
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
		limit := limitOr(in.Limit, 100)
		for i := range libs {
			if err := runCheck(ctx, client, libs[i].ID, auditChecksByName[field], nativeFilter[field], limit, &out); err != nil {
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
	type allIn struct {
		Library string `json:"library,omitempty" jsonschema:"library name or id; default every library"`
	}
	type allOut struct {
		Scanned int      `json:"items_scanned"`
		Total   int      `json:"total_findings"`
		Audits  []allRow `json:"audits" jsonschema:"every audit with something to report, worst first; call that audit for the worklist"`
		Clean   []string `json:"clean"  jsonschema:"audits that found nothing"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "audit_all",
		Description: "Run every per-item audit in a single sweep and return only the counts, so one call says where a library needs work. Call the individual audit for the worklist. Start here after a scan. Does not include audit_duplicates, audit_series_gaps or audit_terminology, which sweep differently; run those separately.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in allIn) (*mcp.CallToolResult, allOut, error) {
		libs, err := resolveLibraries(ctx, client, in.Library)
		if err != nil {
			return nil, allOut{}, err
		}

		found := map[string]int{}
		out := allOut{Audits: []allRow{}, Clean: []string{}}
		for i := range libs {
			// one pass over the library evaluating every predicate, rather
			// than one pass per audit
			if err := client.ItemsAll(ctx, libs[i].ID, abs.ItemsOptions{}, func(items []abs.Item) bool {
				for j := range items {
					out.Scanned++
					for _, spec := range auditSpecs {
						if _, suspect := auditChecksByName[spec.Check](&items[j]); suspect {
							found[spec.Tool]++
						}
					}
					for _, field := range missingFields {
						if _, suspect := auditChecksByName[field](&items[j]); suspect {
							found["audit_missing/"+field]++
						}
					}
				}
				return true
			}); err != nil {
				return nil, allOut{}, err
			}
		}

		for _, spec := range auditSpecs {
			if n := found[spec.Tool]; n > 0 {
				out.Audits = append(out.Audits, allRow{Audit: spec.Tool, Found: n})
				out.Total += n
			} else {
				out.Clean = append(out.Clean, spec.Tool)
			}
		}
		for _, field := range missingFields {
			if n := found["audit_missing/"+field]; n > 0 {
				out.Audits = append(out.Audits, allRow{Audit: "audit_missing", Field: field, Found: n})
				out.Total += n
			} else {
				out.Clean = append(out.Clean, "audit_missing "+field)
			}
		}
		slices.SortFunc(out.Audits, func(a, b allRow) int {
			if a.Found != b.Found {
				return b.Found - a.Found
			}
			return strings.Compare(a.Audit, b.Audit)
		})

		return nil, out, nil
	})

	type coverRatioIn struct {
		Library   string  `json:"library,omitempty"   jsonschema:"library name or id; default every library"`
		Tolerance float64 `json:"tolerance,omitempty" jsonschema:"how far from square counts as square, default 0.1 (a 10% deviation)"`
		MinPixels int     `json:"min_pixels,omitempty" jsonschema:"also report covers narrower than this, default 400"`
		Limit     int     `json:"limit,omitempty"     jsonschema:"maximum findings, default 50"`
	}
	type coverRow struct {
		ID     string `json:"id"`
		Title  string `json:"title"`
		Width  int    `json:"width"`
		Height int    `json:"height"`
		Ratio  string `json:"ratio"`
		Why    string `json:"why"`
	}
	type coverRatioOut struct {
		Checked  int        `json:"covers_checked"`
		Skipped  int        `json:"skipped,omitempty" jsonschema:"items with no cover, or in a format Go cannot read (webp)"`
		Found    int        `json:"total_findings"`
		Findings []coverRow `json:"findings"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "audit_cover_ratio",
		Description: "Find covers that are not square or are too small to look right in a client. Audiobook art is square by convention, so a tall book-jacket scan or a thumbnail stands out. This reads every cover's header, so it is far slower than the other audits: narrow it with library, or raise limit knowing the cost. Fix with item_cover_search then item_cover_set.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in coverRatioIn) (*mcp.CallToolResult, coverRatioOut, error) {
		tolerance := in.Tolerance
		if tolerance <= 0 {
			tolerance = 0.1
		}
		minPixels := in.MinPixels
		if minPixels <= 0 {
			minPixels = 400
		}
		limit := limitOr(in.Limit, 50)

		libs, err := resolveLibraries(ctx, client, in.Library)
		if err != nil {
			return nil, coverRatioOut{}, err
		}

		out := coverRatioOut{Findings: []coverRow{}}
		for i := range libs {
			if err := client.ItemsAll(ctx, libs[i].ID, abs.ItemsOptions{Minified: true}, func(items []abs.Item) bool {
				for j := range items {
					it := &items[j]
					w, h, err := client.CoverSize(ctx, it.ID)
					if err != nil {
						out.Skipped++ // no cover, or a format we cannot read
						continue
					}
					out.Checked++

					var why string
					switch {
					case h == 0 || w == 0:
						why = "cover has no dimensions"
					case math.Abs(float64(w)/float64(h)-1) > tolerance:
						why = fmt.Sprintf("not square (%dx%d)", w, h)
					case w < minPixels:
						why = fmt.Sprintf("only %dpx wide", w)
					}
					if why == "" {
						continue
					}

					out.Found++
					if len(out.Findings) >= limit {
						continue
					}
					out.Findings = append(out.Findings, coverRow{
						ID: it.ID, Title: it.Title(), Width: w, Height: h,
						Ratio: fmt.Sprintf("%.2f", float64(w)/float64(h)), Why: why,
					})
				}
				return true
			}); err != nil {
				return nil, coverRatioOut{}, err
			}
		}

		return nil, out, nil
	})

	type dupIn struct {
		Library string `json:"library,omitempty" jsonschema:"library name or id; default all libraries"`
		Limit   int    `json:"limit,omitempty"   jsonschema:"maximum groups to return, default 50"`
	}
	type dupGroup struct {
		Key   string        `json:"key"   jsonschema:"what matched: asin, isbn, or title+author"`
		Items []itemSummary `json:"items"`
	}
	type dupOut struct {
		Groups []dupGroup `json:"groups" jsonschema:"each group is one work with several copies"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "audit_duplicates",
		Description: "Find items that appear to be the same work: identical asin, isbn, or title+author. Each group lists every copy with size, duration and path so you can pick which to keep.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in dupIn) (*mcp.CallToolResult, dupOut, error) {
		libs, err := resolveLibraries(ctx, client, in.Library)
		if err != nil {
			return nil, dupOut{}, err
		}

		groups := map[string][]abs.Item{}
		for i := range libs {
			if err := client.ItemsAll(ctx, libs[i].ID, abs.ItemsOptions{}, func(items []abs.Item) bool {
				for i := range items {
					it := &items[i]
					m := &it.Media.Metadata
					var key string
					switch {
					case m.ASIN != "":
						key = "asin:" + strings.ToUpper(m.ASIN)
					case m.ISBN != "":
						key = "isbn:" + strings.ReplaceAll(m.ISBN, "-", "")
					default:
						key = "title:" + strings.ToLower(strings.TrimSpace(m.Title)) + "|" + strings.ToLower(strings.TrimSpace(m.AuthorDisplay()))
					}
					groups[key] = append(groups[key], *it)
				}
				return true
			}); err != nil {
				return nil, dupOut{}, err
			}
		}

		out := dupOut{Groups: []dupGroup{}}
		keys := make([]string, 0, len(groups))
		for k, items := range groups {
			if len(items) > 1 {
				keys = append(keys, k)
			}
		}
		slices.Sort(keys)
		limit := limitOr(in.Limit, 50)
		for _, k := range keys {
			if len(out.Groups) >= limit {
				break
			}
			out.Groups = append(out.Groups, dupGroup{Key: k, Items: summarizeAll(groups[k])})
		}

		return nil, out, nil
	})

	type gapsIn struct {
		Library string `json:"library,omitempty" jsonschema:"library name or id; default every book library"`
		Limit   int    `json:"limit,omitempty"   jsonschema:"maximum series to return, default 50"`
	}
	type seriesGap struct {
		ID      string   `json:"id"`
		Name    string   `json:"name"`
		Author  string   `json:"author,omitempty"`
		Books   int      `json:"books"`
		Have    []string `json:"have"    jsonschema:"sequence numbers present, in order"`
		Missing []string `json:"missing" jsonschema:"whole numbers absent between the lowest and the highest present"`
	}
	type gapsOut struct {
		Scanned int         `json:"series_scanned"`
		Found   int         `json:"found"                   jsonschema:"series with gaps, before limit"`
		Series  []seriesGap `json:"series"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "audit_series_gaps",
		Description: "Find series missing a book: sequence numbers absent between the lowest and the highest the library has. Only interior gaps are reported, so a series whose first book is #3 is not flagged for #1-2, and novella numbering (4.5) never creates one. series_get shows what is present.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in gapsIn) (*mcp.CallToolResult, gapsOut, error) {
		libs, err := resolveLibraries(ctx, client, in.Library)
		if err != nil {
			return nil, gapsOut{}, err
		}

		out := gapsOut{Series: []seriesGap{}}
		limit := limitOr(in.Limit, 50)
		for i := range libs {
			if libs[i].MediaType == "podcast" {
				continue
			}
			for page := 0; ; page++ {
				series, total, err := client.SeriesList(ctx, libs[i].ID, abs.ListOptions{Limit: seriesPageSize, Page: page, Sort: "name"})
				if err != nil {
					return nil, gapsOut{}, err
				}
				for j := range series {
					s := &series[j]
					out.Scanned++
					// an interior gap needs a book on either side of it
					if len(s.Books) < 2 {
						continue
					}

					// the series listing carries no sequence numbers, and
					// neither does a plain item listing: only an item query
					// filtered by the series does (see docs/README.md)
					res, err := client.Items(ctx, libs[i].ID, abs.ItemsOptions{
						Limit:  len(s.Books),
						Filter: abs.EncodeFilter("series", s.ID),
					})
					if err != nil {
						return nil, gapsOut{}, err
					}

					have, missing := seriesSequences(res.Results, s.ID)
					if len(missing) == 0 {
						continue
					}
					out.Found++
					if len(out.Series) >= limit {
						continue
					}
					row := seriesGap{ID: s.ID, Name: s.Name, Books: len(s.Books), Have: have, Missing: missing}
					if len(res.Results) > 0 {
						row.Author = res.Results[0].Media.Metadata.AuthorDisplay()
					}
					out.Series = append(out.Series, row)
				}
				if len(series) == 0 || (page+1)*seriesPageSize >= total {
					break
				}
			}
		}

		return nil, out, nil
	})
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
	seen := map[float64]string{}
	for i := range items {
		for _, ref := range items[i].Media.Metadata.Series {
			if ref.ID != seriesID || ref.Sequence == "" {
				continue
			}
			n, err := strconv.ParseFloat(strings.TrimSpace(ref.Sequence), 64)
			if err != nil {
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

	nums := make([]float64, 0, len(seen))
	for n := range seen {
		nums = append(nums, n)
	}
	slices.Sort(nums)
	for _, n := range nums {
		have = append(have, seen[n])
	}

	for n := math.Ceil(nums[0]); n < nums[len(nums)-1]; n++ {
		if _, ok := seen[n]; !ok {
			missing = append(missing, strconv.FormatFloat(n, 'f', -1, 64))
		}
	}

	return have, missing
}

// runCheck evaluates one predicate over a library, using the server's own
// filter when it has one (far cheaper than a sweep) and paging otherwise.
func runCheck(ctx context.Context, client *abs.Client, libraryID string, check auditCheck, filter string, limit int, out *auditOut) error {
	if filter != "" {
		remaining := limit - len(out.Findings)
		if remaining < 0 {
			remaining = 0
		}
		res, err := client.Items(ctx, libraryID, abs.ItemsOptions{Limit: remaining, Filter: filter, Minified: true})
		if err != nil {
			return err
		}
		out.Scanned += res.Total
		out.Found += res.Total
		for j := range res.Results {
			detail, _ := check(&res.Results[j])
			out.Findings = append(out.Findings, finding(&res.Results[j], detail))
		}
		return nil
	}

	return client.ItemsAll(ctx, libraryID, abs.ItemsOptions{}, func(items []abs.Item) bool {
		for j := range items {
			out.Scanned++
			detail, suspect := check(&items[j])
			if !suspect {
				continue
			}
			out.Found++
			if len(out.Findings) < limit {
				out.Findings = append(out.Findings, finding(&items[j], detail))
			}
		}
		return true
	})
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
	"issues":      "issues",
	"cover":       abs.EncodeFilter("missing", "cover"),
	"description": abs.EncodeFilter("missing", "description"),
	"narrator":    abs.EncodeFilter("missing", "narrators"),
	"series":      abs.EncodeFilter("missing", "series"),
	"author":      abs.EncodeFilter("missing", "authors"),
	"genres":      abs.EncodeFilter("missing", "genres"),
	"year":        abs.EncodeFilter("missing", "publishedYear"),
	"publisher":   abs.EncodeFilter("missing", "publisher"),
	"language":    abs.EncodeFilter("missing", "language"),
	"no_audio":    abs.EncodeFilter("tracks", "none"),
}

const (
	chapterlessMinHours = 2
	staleFeedDays       = 90
	seriesPageSize      = 100
)

var auditChecksByName = map[string]auditCheck{
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
		if strings.TrimSpace(it.Media.Metadata.Description) == "" {
			return "no description", true
		}
		return "", false
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
	"single_chapter": func(it *abs.Item) (string, bool) {
		// exactly one chapter over a whole book is the same problem as none:
		// there is nothing to seek to. The chapters check treats any chapter
		// count above zero as fine, so this would otherwise go unseen.
		if it.IsPodcast() || it.Media.NumChapters != 1 || it.Media.Duration < chapterlessMinHours*3600 {
			return "", false
		}
		return fmt.Sprintf("one chapter covering all %s", fmtDuration(it.Media.Duration)), true
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
		last := abs.Millis(it.Media.LastEpisodeCheck)
		if last.IsZero() {
			return "feed never checked", true
		}
		if age := time.Since(last); age > staleFeedDays*24*time.Hour {
			return fmt.Sprintf("feed last checked %d days ago", int(age.Hours()/24)), true
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

// checkPath flags items whose folder name does not contain the title and
// whose parent folder does not name an author: a sign of a wrong match or a
// misfiled folder in an Author/Title or Author/Series/Title layout.
func checkPath(it *abs.Item) (string, bool) {
	if it.IsPodcast() || it.RelPath == "" || it.IsFile {
		return "", false
	}
	m := it.Media.Metadata
	rel := strings.Trim(it.RelPath, "/")
	folder := norm(path.Base(rel))
	title := norm(m.Title)
	if title == "" {
		return "", false
	}

	titleOK := strings.Contains(folder, title) || strings.Contains(title, folder)
	authorOK := true
	if parent := path.Dir(rel); parent != "." && parent != "/" {
		authorOK = false
		parts := strings.SplitSeq(parent, "/")
		for p := range parts {
			np := norm(p)
			if np == "" {
				continue
			}
			for a := range strings.SplitSeq(m.AuthorDisplay(), ",") {
				na := norm(a)
				if na != "" && (strings.Contains(np, na) || strings.Contains(na, np) || lastFirstMatch(np, na)) {
					authorOK = true
				}
			}
			for _, s := range m.SeriesDisplay() {
				if ns := norm(strings.Split(s, " #")[0]); ns != "" && strings.Contains(np, ns) {
					authorOK = true
				}
			}
		}
	}

	switch {
	case !titleOK && !authorOK:
		return fmt.Sprintf("folder %q does not match title %q or author %q", rel, m.Title, m.AuthorDisplay()), true
	case !titleOK:
		return fmt.Sprintf("folder %q does not contain title %q", path.Base(rel), m.Title), true
	case !authorOK:
		return fmt.Sprintf("parent folder %q does not name author %q", path.Dir(rel), m.AuthorDisplay()), true
	}

	return "", false
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
// compare loosely.
func norm(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == ' ':
			b.WriteRune(r)
		case r == '_' || r == '-' || r == '.':
			b.WriteRune(' ')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// errNotBook is returned by tools that only make sense for books.
var errNotBook = errors.New("this item is a podcast, not a book")
