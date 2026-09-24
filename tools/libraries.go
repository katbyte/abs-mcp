package tools

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/katbyte/abs-mcp/lib/abs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// filterGroups documents the filter groups accepted by library_items. Values
// for the named groups come from library_filters.
const filterGroups = "genres, tags, narrators, languages, publishers, publishedDecades (value: the name from library_filters); " +
	"authors, series (a name or an id; series:no-series is the books in none); " +
	"progress (finished, not-started, in-progress, not-finished, audio-in-progress, ebook-in-progress, ebook-finished); " +
	"ebooks (ebook, no-ebook, supplementary, no-supplementary); tracks (none, single, multi); " +
	"and with no value: abridged, explicit, feed-open, share-open, recent (added in the last 60 days). " +
	"A podcast library takes only genres, tags, languages, explicit, feed-open, recent and issues. " +
	"The server also takes missing:<field> (" + missingValues + ") and issues, but prefer audit_missing and audit_issues for those: " +
	"they sweep the whole library into a worklist and say how to fix what they find"

const missingValues = "asin, isbn, subtitle, authors, narrators, series, genres, tags, publishedYear, publisher, language, description, chapters, cover"

// The filter groups as the server spells them (libraryFilters.js). The server
// ignores a group it does not know, and a value it does not know for the
// groups below with a fixed set, and answers with the whole library as if it
// were the filtered answer: a mistyped filter would list every book, and
// item_match_tag would tag every one. So nothing else is sent.
var (
	// groups that take a free value: a name from library_filters, or an id
	namedGroups = []string{"genres", "tags", "series", "authors", "narrators", "languages", "publishers", "publishedDecades"}
	// groups that take one of a fixed set
	fixedGroups = map[string][]string{
		"progress": {"finished", "not-started", "in-progress", "not-finished", "audio-in-progress", "ebook-in-progress", "ebook-finished"},
		"ebooks":   {"ebook", "no-ebook", "supplementary", "no-supplementary"},
		"tracks":   {"none", "single", "multi"},
		"missing":  strings.Split(missingValues, ", "),
	}
	// groups sent bare: the server reads "issues.xyz" as a group named that,
	// which it does not know
	bareGroups = []string{"abridged", "explicit", "feed-open", "share-open", "recent", "issues"}
	// the only groups a podcast library filters on
	podcastGroups = []string{"genres", "tags", "languages", "explicit", "feed-open", "recent", "issues"}
	// the web client's singular spellings
	groupAliases = map[string]string{
		"genre": "genres", "tag": "tags", "author": "authors", "narrator": "narrators", "language": "languages",
		"publisher": "publishers", "decade": "publishedDecades", "decades": "publishedDecades", "publisheddecade": "publishedDecades",
		"ebook": "ebooks", "track": "tracks",
	}
)

// allGroups lists every filter group, for a refusal to name them.
func allGroups() []string {
	return slices.Concat(namedGroups, []string{"progress", "ebooks", "tracks", "missing"}, bareGroups)
}

// maxLimit caps a page: a limit of a million was one call reading a whole
// library into one answer.
const maxLimit = 1000

// previewCap is how many titles a preview of a change lists.
const previewCap = 50

// pageArgs settles a limit and offset the way every paged listing takes them:
// the default when no limit is given, never more than maxLimit, never before
// the start.
func pageArgs(asked, from, def int) (limit, offset int) {
	return min(limitOr(asked, def), maxLimit), max(from, 0)
}

// window reads limit rows from offset out of a listing the server pages by
// page number. An offset that is not a whole number of pages falls inside one,
// so the page it falls in and the next are read and cut to the rows asked for:
// dividing it by the limit would round it down and answer rows before it.
func window[T any](offset, limit int, fetch func(page int) ([]T, int, error)) (rows []T, total int, err error) {
	page := offset / limit
	if rows, total, err = fetch(page); err != nil {
		return nil, 0, err
	}
	skip := offset - page*limit
	if skip == 0 {
		return rows, total, nil
	}
	if (page+1)*limit < total {
		more, _, ferr := fetch(page + 1)
		if ferr != nil {
			return nil, 0, ferr
		}
		rows = append(rows, more...)
	}
	rows = rows[min(skip, len(rows)):]

	return rows[:min(limit, len(rows))], total, nil
}

// nextOffset is where the page after one of n rows from offset starts, or 0
// when that was the last.
func nextOffset(offset, n, total int) int {
	if n == 0 || offset+n >= total {
		return 0
	}
	return offset + n
}

func registerLibraryTools(r *registry) {
	client := r.client

	type listOut struct {
		Libraries []libraryRow `json:"libraries"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "library_list",
		Description: "List the libraries visible to the API key with their type (book or podcast), folders and default metadata provider.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, listOut, error) {
		libs, err := client.Libraries(ctx)
		if err != nil {
			return nil, listOut{}, err
		}
		out := listOut{Libraries: []libraryRow{}}
		for i := range libs {
			out.Libraries = append(out.Libraries, libraryRowOf(&libs[i]))
		}

		return nil, out, nil
	})

	type getIn struct {
		Library string `json:"library,omitempty" jsonschema:"library name (case-insensitive) or id; optional when the server has one library"`
	}
	type countRow struct {
		Name  string `json:"name"`
		ID    string `json:"id,omitempty"`
		Count int    `json:"count"`
	}
	type statRow struct {
		ID       string `json:"id"`
		Title    string `json:"title"`
		Duration int    `json:"duration_s,omitempty" jsonschema:"length in seconds"`
		Size     int64  `json:"size,omitempty"       jsonschema:"bytes on disk"`
	}
	type getOut struct {
		libraryRow
		Items      int        `json:"items"`
		Authors    int        `json:"authors,omitempty"`
		Series     int        `json:"series,omitempty"`
		Genres     int        `json:"genres"`
		Tags       int        `json:"tags"`
		Narrators  int        `json:"narrators,omitempty"`
		Languages  []string   `json:"languages,omitempty"`
		Issues     int        `json:"issues"                     jsonschema:"items whose folder is missing or has no playable media; see audit_issues"`
		Duration   int        `json:"total_duration_s,omitempty" jsonschema:"every item's length added together, in seconds"`
		Size       int64      `json:"total_size,omitempty"       jsonschema:"bytes on disk"`
		AudioFiles int        `json:"audio_files,omitempty"`
		TopAuthors []countRow `json:"top_authors,omitempty"      jsonschema:"most-published authors in this library"`
		TopGenres  []countRow `json:"top_genres,omitempty"`
		Longest    []statRow  `json:"longest,omitempty"`
		Largest    []statRow  `json:"largest,omitempty"`
		Settings   struct {
			SkipMatchingWithASIN bool     `json:"skip_matching_with_asin"`
			SkipMatchingWithISBN bool     `json:"skip_matching_with_isbn"`
			AudiobooksOnly       bool     `json:"audiobooks_only"`
			MetadataPrecedence   []string `json:"metadata_precedence,omitempty"`
			PodcastSearchRegion  string   `json:"podcast_search_region,omitempty"`
		} `json:"settings"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "library_get",
		Description: "One library in depth: counts of items, authors, series, genres, tags and narrators, total duration and size, issue count, the scan/match settings, and its top authors and genres with the longest and largest items.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in getIn) (*mcp.CallToolResult, getOut, error) {
		lib, err := resolveLibrary(ctx, client, in.Library)
		if err != nil {
			return nil, getOut{}, err
		}
		restricted, err := restrictedKey(ctx, client)
		if err != nil {
			return nil, getOut{}, err
		}
		settings := func(out *getOut) {
			out.Settings.SkipMatchingWithASIN = lib.Settings.SkipMatchingMediaWithASIN
			out.Settings.SkipMatchingWithISBN = lib.Settings.SkipMatchingMediaWithISBN
			out.Settings.AudiobooksOnly = lib.Settings.AudiobooksOnly
			out.Settings.MetadataPrecedence = lib.Settings.MetadataPrecedence
			out.Settings.PodcastSearchRegion = lib.Settings.PodcastSearchRegion
		}
		if restricted {
			// the stats and filter data count books this key may not see
			v, verr := sweepVisible(ctx, client, lib)
			if verr != nil {
				return nil, getOut{}, verr
			}
			out := getOut{
				libraryRow: libraryRowOf(lib),
				Items:      v.Items,
				Authors:    len(v.Authors),
				Series:     len(v.Series),
				Genres:     len(v.Genres),
				Tags:       len(v.Tags),
				Narrators:  len(v.Narrators),
				Languages:  keysOf(v.Languages),
				Issues:     v.Issues,
				Duration:   wholeSec(v.Duration),
				Size:       v.Size,
				AudioFiles: v.AudioFiles,
			}
			settings(&out)
			for _, name := range topCounts(v.AuthorBooks) {
				row := countRow{Name: name, Count: v.AuthorBooks[name]}
				for _, a := range v.Authors {
					if a.Name == name {
						row.ID = a.ID
					}
				}
				out.TopAuthors = append(out.TopAuthors, row)
			}
			for _, name := range topCounts(v.Genres) {
				out.TopGenres = append(out.TopGenres, countRow{Name: name, Count: v.Genres[name]})
			}
			for i := range v.Longest {
				out.Longest = append(out.Longest, statRow{ID: v.Longest[i].ID, Title: v.Longest[i].Title(), Duration: wholeSec(v.Longest[i].Media.Duration)})
			}
			for i := range v.Largest {
				out.Largest = append(out.Largest, statRow{ID: v.Largest[i].ID, Title: v.Largest[i].Title(), Size: v.Largest[i].Size})
			}
			return nil, out, nil
		}
		_, fd, err := client.LibraryWithFilterData(ctx, lib.ID)
		if err != nil {
			return nil, getOut{}, err
		}

		out := getOut{
			libraryRow: libraryRowOf(lib),
			Authors:    len(fd.Authors),
			Series:     len(fd.Series),
			Genres:     len(fd.Genres),
			Tags:       len(fd.Tags),
			Narrators:  len(fd.Narrators),
			Languages:  fd.Languages,
			Issues:     fd.NumIssues,
		}
		settings(&out)

		if st, err := client.LibraryStats(ctx, lib.ID); err == nil {
			out.Items = st.TotalItems
			out.Duration = wholeSec(st.TotalDuration)
			out.Size = st.TotalSize
			out.AudioFiles = st.NumAudioTracks
			for _, a := range st.AuthorsWithCount {
				out.TopAuthors = append(out.TopAuthors, countRow{Name: a.Name, ID: a.ID, Count: a.Count})
			}
			for _, g := range st.GenresWithCount {
				out.TopGenres = append(out.TopGenres, countRow{Name: g.Genre, Count: g.Count})
			}
			for _, it := range st.LongestItems {
				out.Longest = append(out.Longest, statRow{ID: it.ID, Title: it.Title, Duration: wholeSec(it.Duration)})
			}
			for _, it := range st.LargestItems {
				out.Largest = append(out.Largest, statRow{ID: it.ID, Title: it.Title, Size: it.Size})
			}
		} else {
			out.Items = fd.BookCount + fd.PodcastCount
		}

		return nil, out, nil
	})

	type searchIn struct {
		Query   string `json:"query"             jsonschema:"title, author, narrator, series, genre or tag text; partial matches allowed"`
		Library string `json:"library,omitempty" jsonschema:"restrict to one library by name or id; default all libraries"`
		Limit   int    `json:"limit,omitempty"   jsonschema:"maximum items per library, default 10, at most 1000"`
	}
	type nameCount struct {
		Name  string `json:"name"`
		ID    string `json:"id,omitempty"`
		Count int    `json:"count,omitempty"`
	}
	type searchOut struct {
		Items     []itemSummary `json:"items"               jsonschema:"books/podcasts whose title, author, narrator, series, isbn or asin matched"`
		Authors   []nameCount   `json:"authors,omitempty"   jsonschema:"use author_get for their books"`
		Series    []nameCount   `json:"series,omitempty"    jsonschema:"use series_get for the books in order"`
		Narrators []nameCount   `json:"narrators,omitempty"`
		Tags      []nameCount   `json:"tags,omitempty"`
		Genres    []nameCount   `json:"genres,omitempty"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "library_search",
		Description: "Find something by name: one free-text query, matched against titles, authors, narrators, series, tags, genres and isbn/asin. The quickest way to turn a title a user typed into an item id. Returns the matching books and podcasts plus the authors, series, narrators and tags that matched, grouped. For a filtered or sorted listing rather than a name lookup, use library_items.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in searchIn) (*mcp.CallToolResult, searchOut, error) {
		if strings.TrimSpace(in.Query) == "" {
			return nil, searchOut{}, errors.New("query is required")
		}
		libs, err := resolveLibraries(ctx, client, in.Library)
		if err != nil {
			return nil, searchOut{}, err
		}

		restricted, err := restrictedKey(ctx, client)
		if err != nil {
			return nil, searchOut{}, err
		}

		out := searchOut{Items: []itemSummary{}}
		for i := range libs {
			res, err := client.Search(ctx, libs[i].ID, in.Query, min(limitOr(in.Limit, 10), maxLimit))
			if err != nil {
				return nil, searchOut{}, err
			}
			matches := res.Items()
			for j := range matches {
				out.Items = append(out.Items, summarize(&matches[j].LibraryItem))
			}
			// the server filters the items to what the key may see but not the
			// name groups, which are matched over the whole library
			var v *visibleLibrary
			if restricted && len(res.Authors)+len(res.Series)+len(res.Narrators)+len(res.Tags)+len(res.Genres) > 0 {
				if v, err = sweepVisible(ctx, client, &libs[i]); err != nil {
					return nil, searchOut{}, err
				}
			}
			// and counted over it: for such a key the names are those of the
			// books it can see, and so are the counts
			var authorBooks, narrators, tags, genres map[string]int
			if v != nil {
				authorBooks, narrators, tags, genres = v.AuthorBooks, v.Narrators, v.Tags, v.Genres
			}
			visibleCount := func(counts map[string]int, name string, all int) (int, bool) {
				if v == nil {
					return all, true
				}
				return counts[name], counts[name] > 0
			}
			for _, a := range res.Authors {
				if v == nil || hasRef(v.Authors, a.ID) {
					n, _ := visibleCount(authorBooks, a.Name, a.NumBooks)
					out.Authors = append(out.Authors, nameCount{Name: a.Name, ID: a.ID, Count: n})
				}
			}
			for _, s := range res.Series {
				if v == nil || hasRef(v.Series, s.Series.ID) {
					out.Series = append(out.Series, nameCount{Name: s.Series.Name, ID: s.Series.ID, Count: len(s.Books)})
				}
			}
			for _, n := range res.Narrators {
				if count, ok := visibleCount(narrators, n.Name, n.N()); ok {
					out.Narrators = append(out.Narrators, nameCount{Name: n.Name, Count: count})
				}
			}
			for _, t := range res.Tags {
				if count, ok := visibleCount(tags, t.Name, t.N()); ok {
					out.Tags = append(out.Tags, nameCount{Name: t.Name, Count: count})
				}
			}
			for _, g := range res.Genres {
				if count, ok := visibleCount(genres, g.Name, g.N()); ok {
					out.Genres = append(out.Genres, nameCount{Name: g.Name, Count: count})
				}
			}
		}
		// the first row is taken as the book named, and the server lists its
		// hits in its own order (Second Foundation before Foundation), so the
		// title that is the query goes first and the rest keep that order
		query := strings.TrimSpace(in.Query)
		asked := func(s itemSummary) int {
			if strings.EqualFold(strings.TrimSpace(s.Title), query) {
				return 0
			}
			return 1
		}
		slices.SortStableFunc(out.Items, func(a, b itemSummary) int { return asked(a) - asked(b) })

		return nil, out, nil
	})

	type itemsIn struct {
		Library string `json:"library,omitempty" jsonschema:"library name or id; optional when the server has one library"`
		Filter  string `json:"filter,omitempty"  jsonschema:"group or group:value, e.g. genres:Fantasy, authors:Brandon Sanderson, series:Mistborn, progress:in-progress; the tool description lists every group"`
		Sort    string `json:"sort,omitempty"    jsonschema:"title (default), author, author_last_first, year, duration, size, added, modified, created, sequence (with a series filter), progress, random; a podcast library takes title, author, size, added, modified, created, episodes, random"`
		Desc    bool   `json:"desc,omitempty"    jsonschema:"sort descending"`
		Limit   int    `json:"limit,omitempty"   jsonschema:"page size, default 25, at most 1000"`
		Offset  int    `json:"offset,omitempty"  jsonschema:"skip this many items: a previous page's next_offset"`
	}
	type itemsOut struct {
		Total      int           `json:"total"                 jsonschema:"total matches across all pages"`
		Offset     int           `json:"offset"`
		NextOffset int           `json:"next_offset,omitempty" jsonschema:"pass back as offset for the next page; absent on the last"`
		Items      []itemSummary `json:"items"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "library_items",
		Description: "Browse or count a library by structured filter, sorted and paged - 'every Sanderson book, longest first', 'what is in progress'. Takes no text query: use library_search to find something by name. Filter groups: " + filterGroups + ". Authors and series accept a name or an id from library_filters.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in itemsIn) (*mcp.CallToolResult, itemsOut, error) {
		lib, err := resolveLibrary(ctx, client, in.Library)
		if err != nil {
			return nil, itemsOut{}, err
		}
		sort := sortKey(in.Sort, lib.IsPodcast())
		if sort == "" {
			return nil, itemsOut{}, fmt.Errorf("unknown sort %q; choose one of: %s", in.Sort, strings.Join(sortNames(lib.IsPodcast()), ", "))
		}

		filter, err := buildFilter(ctx, client, lib, in.Filter)
		if err != nil {
			return nil, itemsOut{}, err
		}

		limit, offset := pageArgs(in.Limit, in.Offset, defaultLimit)
		items, total, err := window(offset, limit, func(page int) ([]abs.Item, int, error) {
			res, ierr := client.Items(ctx, lib.ID, abs.ItemsOptions{Limit: limit, Page: page, Sort: sort, Desc: in.Desc, Filter: filter, Minified: true})
			if ierr != nil {
				return nil, 0, ierr
			}
			return res.Results, res.Total, nil
		})
		if err != nil {
			return nil, itemsOut{}, err
		}
		if strings.HasPrefix(filter, "series.") { // the listing collapses each book's series to the one filtered on
			if err := fullSeriesLists(ctx, client, items); err != nil {
				return nil, itemsOut{}, err
			}
		}

		return nil, itemsOut{Total: total, Offset: offset, NextOffset: nextOffset(offset, len(items), total), Items: summarizeAll(items)}, nil
	})

	type recentIn struct {
		Library string `json:"library,omitempty" jsonschema:"library name or id; default all libraries"`
		Limit   int    `json:"limit,omitempty"   jsonschema:"maximum items, default 25, at most 1000"`
	}
	type recentOut struct {
		Items []itemSummary `json:"items" jsonschema:"newest additions first"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "library_recent",
		Description: "Recently added books and podcasts across every library at once, newest first - 'what turned up lately'. This is the only item listing that spans libraries; for one library with the full filters, sorts and paging use library_items with sort=added and desc=true.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in recentIn) (*mcp.CallToolResult, recentOut, error) {
		libs, err := resolveLibraries(ctx, client, in.Library)
		if err != nil {
			return nil, recentOut{}, err
		}
		limit := min(limitOr(in.Limit, defaultLimit), maxLimit)

		var all []abs.Item
		for i := range libs {
			res, err := client.Items(ctx, libs[i].ID, abs.ItemsOptions{Limit: limit, Sort: "addedAt", Desc: true, Minified: true})
			if err != nil {
				return nil, recentOut{}, err
			}
			all = append(all, res.Results...)
		}
		slices.SortStableFunc(all, func(a, b abs.Item) int { return cmp.Compare(b.AddedAt, a.AddedAt) })
		if len(all) > limit {
			all = all[:limit]
		}

		return nil, recentOut{Items: summarizeAll(all)}, nil
	})

	type filtersIn struct {
		Library string `json:"library,omitempty" jsonschema:"library name or id; optional when the server has one library"`
	}
	type filtersOut struct {
		Genres           []string      `json:"genres"`
		Tags             []string      `json:"tags"`
		Narrators        []string      `json:"narrators,omitempty"`
		Languages        []string      `json:"languages,omitempty"`
		Publishers       []string      `json:"publishers,omitempty"`
		PublishedDecades []string      `json:"published_decades,omitempty"`
		Authors          []abs.NameRef `json:"authors,omitempty"           jsonschema:"pass the id as authors:<id> to library_items"`
		Series           []abs.NameRef `json:"series,omitempty"            jsonschema:"pass the id as series:<id> to library_items"`
	}
	add(r, readTool, &mcp.Tool{
		Name: "library_filters",
		Description: "The distinct genres, tags, narrators, languages, publishers, authors and series in a library: the valid values for library_items filters, and the vocabulary to normalize against. " +
			"Authors and series are read live; the rest come from the server's filter cache, which can keep a value removed from its last book for up to half an hour. For an account kept from some books by tag or by the explicit flag, everything is read from the books it can see.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in filtersIn) (*mcp.CallToolResult, filtersOut, error) {
		lib, err := resolveLibrary(ctx, client, in.Library)
		if err != nil {
			return nil, filtersOut{}, err
		}
		restricted, err := restrictedKey(ctx, client)
		if err != nil {
			return nil, filtersOut{}, err
		}
		if restricted {
			// the filter data lists the whole library's vocabulary, including
			// what this key may not see
			v, verr := sweepVisible(ctx, client, lib)
			if verr != nil {
				return nil, filtersOut{}, verr
			}
			return nil, filtersOut{
				Genres: keysOf(v.Genres), Tags: keysOf(v.Tags), Narrators: keysOf(v.Narrators),
				Languages: keysOf(v.Languages), Publishers: keysOf(v.Publishers), PublishedDecades: keysOf(v.Decades),
				Authors: v.Authors, Series: v.Series,
			}, nil
		}
		fd, err := client.FilterData(ctx, lib.ID)
		if err != nil {
			return nil, filtersOut{}, err
		}
		out := filtersOut{
			Genres:           fd.Genres,
			Tags:             fd.Tags,
			Narrators:        fd.Narrators,
			Languages:        fd.Languages,
			Publishers:       fd.Publishers,
			PublishedDecades: fd.PublishedDecades,
			Authors:          fd.Authors,
			Series:           fd.Series,
		}
		// a library with none has them left out of its filter data, and the
		// answer says none rather than null
		if out.Genres == nil {
			out.Genres = []string{}
		}
		if out.Tags == nil {
			out.Tags = []string{}
		}
		// the filter data is cached for half an hour and a rename does not
		// reach it, so the names library_items resolves are read live
		if !lib.IsPodcast() {
			authors, err := allAuthors(ctx, client, lib.ID, abs.ListOptions{Sort: "name"})
			if err != nil {
				return nil, filtersOut{}, err
			}
			series, err := allSeries(ctx, client, lib.ID)
			if err != nil {
				return nil, filtersOut{}, err
			}
			out.Authors, out.Series = make([]abs.NameRef, 0, len(authors)), make([]abs.NameRef, 0, len(series))
			for i := range authors {
				out.Authors = append(out.Authors, abs.NameRef{ID: authors[i].ID, Name: authors[i].Name})
			}
			for i := range series {
				out.Series = append(out.Series, abs.NameRef{ID: series[i].ID, Name: series[i].Name})
			}
		}

		return nil, out, nil
	})

	type scanIn struct {
		Library string `json:"library,omitempty" jsonschema:"library name or id; optional when the server has one library"`
		Force   bool   `json:"force,omitempty"   jsonschema:"rescan every item even if its files look unchanged"`
	}
	type scanOut struct {
		Started string `json:"started" jsonschema:"the scan runs in the background; poll server_tasks for completion"`
	}
	type createIn struct {
		Name      string   `json:"name"`
		Folders   []string `json:"folders"              jsonschema:"absolute paths on the Audiobookshelf server, not on this machine"`
		MediaType string   `json:"media_type,omitempty" jsonschema:"book (default) or podcast"`
		Provider  string   `json:"provider,omitempty"   jsonschema:"default metadata provider, one server_info lists for the media type: audible, audible.ca, google, openlibrary... for books, itunes for podcasts"`
		Icon      string   `json:"icon,omitempty"`
	}
	type createOut struct {
		Library libraryRow `json:"library"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "library_create",
		Description: "Create a library over folders that already exist on the Audiobookshelf server (the paths are the server's, not the caller's); a folder that is not there, or a name another library has, is refused. The library starts empty; library_scan populates it. Admin only. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in createIn) (*mcp.CallToolResult, createOut, error) {
		name := strings.TrimSpace(in.Name)
		if name == "" {
			return nil, createOut{}, errors.New("name is required")
		}

		mediaType := strings.ToLower(strings.TrimSpace(in.MediaType))
		if mediaType == "" {
			mediaType = "book"
		}
		if mediaType != "book" && mediaType != "podcast" {
			return nil, createOut{}, fmt.Errorf("media_type %q must be book or podcast", in.MediaType)
		}
		provider, perr := checkProvider(ctx, client, in.Provider, mediaType)
		if perr != nil {
			return nil, createOut{}, perr
		}

		folders := make([]abs.Folder, 0, len(in.Folders))
		for _, f := range in.Folders {
			if f = strings.TrimSpace(f); f != "" {
				folders = append(folders, abs.Folder{FullPath: f})
			}
		}
		if len(folders) == 0 {
			return nil, createOut{}, errors.New("at least one folder path on the server is required")
		}
		// the server creates a folder it is given that does not exist, so a
		// mistyped path would make an empty library over a new empty folder
		for _, f := range folders {
			if !strings.HasPrefix(f.FullPath, "/") && !windowsPath.MatchString(f.FullPath) {
				return nil, createOut{}, fmt.Errorf("folder %q must be an absolute path on the server", f.FullPath)
			}
			exists, err := client.ServerPathExists(ctx, f.FullPath)
			if err != nil {
				return nil, createOut{}, err
			}
			if !exists {
				return nil, createOut{}, fmt.Errorf("no folder %q on the server: Audiobookshelf would create it empty; check the path as the server sees it (inside its container, if it runs in one)", f.FullPath)
			}
		}
		libs, err := client.Libraries(ctx)
		if err != nil {
			return nil, createOut{}, err
		}
		for i := range libs {
			if strings.EqualFold(strings.TrimSpace(libs[i].Name), name) {
				return nil, createOut{}, fmt.Errorf("a library named %q already exists (%s): two libraries with one name cannot be told apart by name", libs[i].Name, libs[i].ID)
			}
		}

		lib, err := client.CreateLibrary(ctx, abs.LibraryCreate{
			Name: name, Folders: folders, MediaType: mediaType, Provider: provider, Icon: in.Icon,
		})
		if err != nil {
			return nil, createOut{}, err
		}

		return nil, createOut{Library: libraryRowOf(lib)}, nil
	})

	type libEditIn struct {
		Library  string `json:"library"            jsonschema:"library name or id"`
		Name     string `json:"name,omitempty"     jsonschema:"new name"`
		Provider string `json:"provider,omitempty" jsonschema:"default metadata provider, one server_info lists for the library's media type: audible, audible.ca, google, openlibrary... for books, itunes for podcasts"`
		Icon     string `json:"icon,omitempty"`
	}
	type libEditOut struct {
		Library libraryRow `json:"library"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "library_edit",
		Description: "Rename a library, or change its default metadata provider or icon. Folders and scan settings are not touched. Admin only. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in libEditIn) (*mcp.CallToolResult, libEditOut, error) {
		lib, err := resolveLibrary(ctx, client, in.Library)
		if err != nil {
			return nil, libEditOut{}, err
		}

		name := strings.TrimSpace(in.Name)
		if in.Name != "" && name == "" {
			return nil, libEditOut{}, errors.New("name cannot be blank")
		}
		provider, err := checkProvider(ctx, client, in.Provider, lib.MediaType)
		if err != nil {
			return nil, libEditOut{}, err
		}
		upd := abs.LibraryUpdate{Name: strPtr(name), Provider: strPtr(provider), Icon: strPtr(in.Icon)}
		if upd.Name == nil && upd.Provider == nil && upd.Icon == nil {
			return nil, libEditOut{}, errors.New("nothing to change: pass name, provider or icon")
		}
		if upd.Name != nil {
			libs, lerr := client.Libraries(ctx)
			if lerr != nil {
				return nil, libEditOut{}, lerr
			}
			for i := range libs {
				if libs[i].ID != lib.ID && strings.EqualFold(strings.TrimSpace(libs[i].Name), name) {
					return nil, libEditOut{}, fmt.Errorf("a library named %q already exists (%s): two libraries with one name cannot be told apart by name", libs[i].Name, libs[i].ID)
				}
			}
		}

		updated, err := client.UpdateLibrary(ctx, lib.ID, upd)
		if err != nil {
			return nil, libEditOut{}, err
		}

		return nil, libEditOut{Library: libraryRowOf(updated)}, nil
	})

	add(r, writeTool, &mcp.Tool{
		Name:        "library_scan",
		Description: "Scan a library's folders so new, changed and removed files are picked up. Admin only. Changes server state; runs in the background.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in scanIn) (*mcp.CallToolResult, scanOut, error) {
		lib, err := resolveLibrary(ctx, client, in.Library)
		if err != nil {
			return nil, scanOut{}, err
		}
		if err := client.ScanLibrary(ctx, lib.ID, in.Force); err != nil {
			return nil, scanOut{}, err
		}

		return nil, scanOut{Started: lib.Name}, nil
	})

	type removeIssuesIn struct {
		Library string `json:"library,omitempty" jsonschema:"library name or id; optional when the server has one library"`
		Confirm bool   `json:"confirm,omitempty" jsonschema:"true to delete; without it the call only reports what it would delete"`
	}
	type issueRow struct {
		ID       string `json:"id"`
		Title    string `json:"title"`
		Path     string `json:"path"      jsonschema:"inside the library, as the audits give it"`
		FullPath string `json:"full_path" jsonschema:"on the server"`
	}
	type removeIssuesOut struct {
		Found     int        `json:"found"     jsonschema:"items whose folder is missing or has no playable media, before this call"`
		Items     []issueRow `json:"items"     jsonschema:"the first 50, with the path each was scanned from: a title alone can be two books, as a copied file carries its tags"`
		Removed   int        `json:"removed"   jsonschema:"item records deleted, counted by reading the library back; 0 without confirm. Files on disk are untouched"`
		Remaining int        `json:"remaining" jsonschema:"items still flagged after this call"`
	}
	add(r, deleteTool, &mcp.Tool{
		Name: "library_issues_remove",
		Description: "Delete the library records of every item whose folder is missing or has no playable media. Without confirm=true it only says how many there are and which, and changes nothing: " +
			"a share that was not mounted during a scan marks every book on it missing, and deleting those records loses everyone's listening progress on them. Files on disk are untouched. Admin only.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in removeIssuesIn) (*mcp.CallToolResult, removeIssuesOut, error) {
		lib, err := resolveLibrary(ctx, client, in.Library)
		if err != nil {
			return nil, removeIssuesOut{}, err
		}
		before, err := client.Items(ctx, lib.ID, abs.ItemsOptions{Limit: previewCap, Sort: "media.metadata.title", Filter: "issues", Minified: true})
		if err != nil {
			return nil, removeIssuesOut{}, err
		}
		out := removeIssuesOut{Found: before.Total, Items: make([]issueRow, 0, len(before.Results)), Remaining: before.Total}
		for i := range before.Results {
			it := &before.Results[i]
			out.Items = append(out.Items, issueRow{ID: it.ID, Title: it.Title(), Path: it.RelPath, FullPath: it.Path})
		}
		if !in.Confirm || before.Total == 0 {
			return nil, out, nil
		}
		if err := client.RemoveIssues(ctx, lib.ID); err != nil {
			return nil, removeIssuesOut{}, err
		}
		// the count is read back rather than assumed from before
		after, err := client.Items(ctx, lib.ID, abs.ItemsOptions{Limit: 1, Filter: "issues", Minified: true})
		if err != nil {
			return nil, removeIssuesOut{}, fmt.Errorf("the records were deleted, but reading the library back failed: %w", err)
		}
		out.Remaining = after.Total
		out.Removed = max(before.Total-after.Total, 0)

		return nil, out, nil
	})
}

// checkProvider takes a default metadata provider only if the server offers
// it for the media type, custom ones included, and returns it as the server
// spells it. The server stores any name it is given (it checks custom- ones
// alone), so a mistyped provider would stick.
func checkProvider(ctx context.Context, client *abs.Client, provider, mediaType string) (string, error) {
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return "", nil
	}
	book, podcast, err := client.Providers(ctx)
	if err != nil {
		return "", err
	}
	have := book
	if mediaType == "podcast" {
		have = podcast
	}
	for _, p := range have {
		if strings.EqualFold(p, provider) {
			return p, nil
		}
	}

	return "", fmt.Errorf("the server has no %s metadata provider %q; choose one of: %s", mediaType, provider, strings.Join(have, ", "))
}

// bookSorts and podcastSorts map the friendly sort names to the server's sort
// fields. The server orders by nothing at all for a field it does not know,
// and a podcast library knows fewer, so a name that is not here is refused.
var (
	bookSorts = map[string]string{
		"title": "media.metadata.title", "author": "media.metadata.authorName", "author_last_first": "media.metadata.authorNameLF",
		"year": "media.metadata.publishedYear", "duration": "media.duration", "size": "size", "added": "addedAt",
		"modified": "mtimeMs", "created": "birthtimeMs", "sequence": "sequence", "progress": "progress", "random": "random",
	}
	podcastSorts = map[string]string{
		"title": "media.metadata.title", "author": "media.metadata.author", "size": "size", "added": "addedAt",
		"modified": "mtimeMs", "created": "birthtimeMs", "episodes": "media.numTracks", "random": "random",
	}
	sortAliases = map[string]string{"authorlf": "author_last_first", "published": "year", "birthtime": "created"}
)

// sortKey maps a friendly sort name, or the server's own field, to the
// server's field; "" means the library cannot be sorted that way.
func sortKey(sortBy string, podcast bool) string {
	sorts := bookSorts
	if podcast {
		sorts = podcastSorts
	}
	name := strings.ToLower(strings.TrimSpace(sortBy))
	if name == "" {
		name = "title"
	}
	if alias, ok := sortAliases[name]; ok {
		name = alias
	}
	if key, ok := sorts[name]; ok {
		return key
	}
	for _, key := range sorts {
		if key == strings.TrimSpace(sortBy) {
			return key
		}
	}

	return ""
}

// sortNames lists the friendly sort names a library takes.
func sortNames(podcast bool) []string {
	sorts := bookSorts
	if podcast {
		sorts = podcastSorts
	}
	return slices.Sorted(maps.Keys(sorts))
}

// windowsPath is a drive-letter path, which a server on Windows takes as
// absolute.
var windowsPath = regexp.MustCompile(`^[A-Za-z]:[\\/]`)

// buildFilter turns "group:value" into the server's encoded filter, resolving
// author and series names to ids when needed.
func buildFilter(ctx context.Context, client *abs.Client, lib *abs.Library, filter string) (string, error) {
	filter = strings.TrimSpace(filter)
	if filter == "" {
		return "", nil
	}

	asked, value, _ := strings.Cut(filter, ":")
	asked = strings.TrimSpace(asked)
	value = strings.TrimSpace(value)

	group := groupAliases[strings.ToLower(asked)]
	for _, g := range allGroups() {
		if strings.EqualFold(g, asked) {
			group = g
		}
	}
	switch {
	case group == "":
		return "", fmt.Errorf("unknown filter group %q; the groups are: %s", asked, strings.Join(allGroups(), ", "))
	case lib.IsPodcast() && !slices.Contains(podcastGroups, group):
		return "", fmt.Errorf("a podcast library cannot be filtered by %s; it takes: %s", group, strings.Join(podcastGroups, ", "))
	case slices.Contains(bareGroups, group) && value != "":
		return "", fmt.Errorf("filter %s takes no value", group)
	case slices.Contains(bareGroups, group):
		return group, nil
	case value == "":
		return "", fmt.Errorf("filter %s needs a value, e.g. %s:<name>", group, group)
	}
	if known, fixed := fixedGroups[group]; fixed {
		i := slices.IndexFunc(known, func(k string) bool { return strings.EqualFold(k, value) })
		if i < 0 {
			return "", fmt.Errorf("%s:%s is not a filter the server knows; choose one of: %s", group, value, strings.Join(known, ", "))
		}
		return abs.EncodeFilter(group, known[i]), nil
	}
	if group == "series" && strings.EqualFold(value, "no-series") {
		return abs.EncodeFilter(group, "no-series"), nil
	}

	// authors and series filter by id; accept names too, looked up on the
	// live routes rather than the cached filter data, which keeps a renamed
	// series or author under its old name for half an hour
	if (group == "authors" || group == "series") && !looksLikeID(value) {
		var refs []abs.NameRef
		if group == "series" {
			series, err := allSeries(ctx, client, lib.ID)
			if err != nil {
				return "", err
			}
			for i := range series {
				refs = append(refs, abs.NameRef{ID: series[i].ID, Name: series[i].Name})
			}
		} else {
			authors, err := allAuthors(ctx, client, lib.ID, abs.ListOptions{})
			if err != nil {
				return "", err
			}
			for i := range authors {
				refs = append(refs, abs.NameRef{ID: authors[i].ID, Name: authors[i].Name})
			}
		}
		// two records can share a name ("jim dale" beside "Jim Dale"), and
		// the first listed is not the one meant any more than the second
		var ids []string
		for _, ref := range refs {
			if ref.ID == value {
				ids = []string{ref.ID}
				break
			}
			if strings.EqualFold(ref.Name, value) {
				ids = append(ids, ref.ID)
			}
		}
		switch len(ids) {
		case 0:
			return "", fmt.Errorf("no %s named %q in %s (library_filters lists them)", group, value, lib.Name)
		case 1:
			value = ids[0]
		default:
			return "", fmt.Errorf("%d %s are named %q in %s; pass an id: %s", len(ids), group, value, lib.Name, strings.Join(ids, ", "))
		}
	}

	return abs.EncodeFilter(group, value), nil
}
