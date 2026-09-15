package tools

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/katbyte/abs-mcp/lib/abs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// filterGroups documents the filter groups accepted by library_items. Values
// for the named groups come from library_filters.
const filterGroups = "genres, tags, series, authors, narrators, languages, publishers, publishedDecades (value: the name from library_filters); " +
	"progress (finished, not-started, in-progress, not-finished); " +
	"ebooks (ebook, no-ebook, supplementary, no-supplementary); tracks (none, single, multi); " +
	"abridged (abridged, not-abridged); explicit; feed-open; recent (added in the last 60 days). " +
	"The server also takes missing:<field> and issues, but prefer audit_missing and audit_issues for those: " +
	"they sweep the whole library into a worklist and say how to fix what they find"

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
		Duration string `json:"duration,omitempty"`
		SizeMB   int64  `json:"size_mb,omitempty"`
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
		Issues     int        `json:"issues"                   jsonschema:"items whose folder is missing or has no playable media; see audit_issues"`
		Duration   string     `json:"total_duration,omitempty"`
		SizeGB     int64      `json:"total_size_gb,omitempty"`
		AudioFiles int        `json:"audio_files,omitempty"`
		TopAuthors []countRow `json:"top_authors,omitempty"    jsonschema:"most-published authors in this library"`
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
				Duration:   fmtDuration(v.Duration),
				SizeGB:     v.Size >> 30,
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
				out.Longest = append(out.Longest, statRow{ID: v.Longest[i].ID, Title: v.Longest[i].Title(), Duration: fmtDuration(v.Longest[i].Media.Duration)})
			}
			for i := range v.Largest {
				out.Largest = append(out.Largest, statRow{ID: v.Largest[i].ID, Title: v.Largest[i].Title(), SizeMB: mb(v.Largest[i].Size)})
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
			out.Duration = fmtDuration(st.TotalDuration)
			out.SizeGB = st.TotalSize >> 30
			out.AudioFiles = st.NumAudioTracks
			for _, a := range st.AuthorsWithCount {
				out.TopAuthors = append(out.TopAuthors, countRow{Name: a.Name, ID: a.ID, Count: a.Count})
			}
			for _, g := range st.GenresWithCount {
				out.TopGenres = append(out.TopGenres, countRow{Name: g.Genre, Count: g.Count})
			}
			for _, it := range st.LongestItems {
				out.Longest = append(out.Longest, statRow{ID: it.ID, Title: it.Title, Duration: fmtDuration(it.Duration)})
			}
			for _, it := range st.LargestItems {
				out.Largest = append(out.Largest, statRow{ID: it.ID, Title: it.Title, SizeMB: mb(it.Size)})
			}
		} else {
			out.Items = fd.BookCount + fd.PodcastCount
		}

		return nil, out, nil
	})

	type searchIn struct {
		Query   string `json:"query"             jsonschema:"title, author, narrator, series, genre or tag text; partial matches allowed"`
		Library string `json:"library,omitempty" jsonschema:"restrict to one library by name or id; default all libraries"`
		Limit   int    `json:"limit,omitempty"   jsonschema:"maximum items per library, default 10"`
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
			res, err := client.Search(ctx, libs[i].ID, in.Query, limitOr(in.Limit, 10))
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
			for _, a := range res.Authors {
				if v == nil || hasRef(v.Authors, a.ID) {
					out.Authors = append(out.Authors, nameCount{Name: a.Name, ID: a.ID, Count: a.NumBooks})
				}
			}
			for _, s := range res.Series {
				if v == nil || hasRef(v.Series, s.Series.ID) {
					out.Series = append(out.Series, nameCount{Name: s.Series.Name, ID: s.Series.ID, Count: len(s.Books)})
				}
			}
			for _, n := range res.Narrators {
				if v == nil || v.Narrators[n.Name] > 0 {
					out.Narrators = append(out.Narrators, nameCount{Name: n.Name, Count: n.N()})
				}
			}
			for _, t := range res.Tags {
				if v == nil || v.Tags[t.Name] > 0 {
					out.Tags = append(out.Tags, nameCount{Name: t.Name, Count: t.N()})
				}
			}
			for _, g := range res.Genres {
				if v == nil || v.Genres[g.Name] > 0 {
					out.Genres = append(out.Genres, nameCount{Name: g.Name, Count: g.N()})
				}
			}
		}

		return nil, out, nil
	})

	type itemsIn struct {
		Library string `json:"library,omitempty" jsonschema:"library name or id; optional when the server has one library"`
		Filter  string `json:"filter,omitempty"  jsonschema:"group or group:value, e.g. genres:Fantasy, authors:Brandon Sanderson, series:Mistborn, progress:in-progress; the tool description lists every group"`
		Sort    string `json:"sort,omitempty"    jsonschema:"title (default), author, year, duration, size, added, modified, sequence (with a series filter), progress, random"`
		Desc    bool   `json:"desc,omitempty"    jsonschema:"sort descending"`
		Limit   int    `json:"limit,omitempty"   jsonschema:"page size, default 25"`
		Offset  int    `json:"offset,omitempty"  jsonschema:"skip this many items (paging)"`
	}
	type itemsOut struct {
		Total  int           `json:"total"  jsonschema:"total matches across all pages"`
		Offset int           `json:"offset"`
		Items  []itemSummary `json:"items"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "library_items",
		Description: "Browse or count a library by structured filter, sorted and paged - 'every Sanderson book, longest first', 'what is in progress'. Takes no text query: use library_search to find something by name. Filter groups: " + filterGroups + ". Authors and series accept a name or an id from library_filters.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in itemsIn) (*mcp.CallToolResult, itemsOut, error) {
		lib, err := resolveLibrary(ctx, client, in.Library)
		if err != nil {
			return nil, itemsOut{}, err
		}

		filter, err := buildFilter(ctx, client, lib, in.Filter)
		if err != nil {
			return nil, itemsOut{}, err
		}

		limit := limitOr(in.Limit, defaultLimit)
		page := 0
		if in.Offset > 0 {
			page = in.Offset / limit
		}

		res, err := client.Items(ctx, lib.ID, abs.ItemsOptions{
			Limit:    limit,
			Page:     page,
			Sort:     sortKey(in.Sort, lib.IsPodcast()),
			Desc:     in.Desc,
			Filter:   filter,
			Minified: true,
		})
		if err != nil {
			return nil, itemsOut{}, err
		}
		if strings.HasPrefix(filter, "series.") { // the listing collapses each book's series to the one filtered on
			if err := fullSeriesLists(ctx, client, res.Results); err != nil {
				return nil, itemsOut{}, err
			}
		}

		return nil, itemsOut{Total: res.Total, Offset: page * limit, Items: summarizeAll(res.Results)}, nil
	})

	type recentIn struct {
		Library string `json:"library,omitempty" jsonschema:"library name or id; default all libraries"`
		Limit   int    `json:"limit,omitempty"   jsonschema:"maximum items, default 25"`
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
		limit := limitOr(in.Limit, defaultLimit)

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
		Provider  string   `json:"provider,omitempty"   jsonschema:"default metadata provider: audible, google, openlibrary, itunes..."`
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
			Name: name, Folders: folders, MediaType: mediaType, Provider: in.Provider, Icon: in.Icon,
		})
		if err != nil {
			return nil, createOut{}, err
		}

		return nil, createOut{Library: libraryRowOf(lib)}, nil
	})

	type libEditIn struct {
		Library  string `json:"library"            jsonschema:"library name or id"`
		Name     string `json:"name,omitempty"     jsonschema:"new name"`
		Provider string `json:"provider,omitempty" jsonschema:"default metadata provider: audible, google, openlibrary, itunes..."`
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

		upd := abs.LibraryUpdate{Name: strPtr(in.Name), Provider: strPtr(in.Provider), Icon: strPtr(in.Icon)}
		if upd.Name == nil && upd.Provider == nil && upd.Icon == nil {
			return nil, libEditOut{}, errors.New("nothing to change: pass name, provider or icon")
		}
		if upd.Name != nil {
			libs, lerr := client.Libraries(ctx)
			if lerr != nil {
				return nil, libEditOut{}, lerr
			}
			for i := range libs {
				if libs[i].ID != lib.ID && strings.EqualFold(strings.TrimSpace(libs[i].Name), strings.TrimSpace(in.Name)) {
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
	}
	type removeIssuesOut struct {
		Removed int `json:"removed" jsonschema:"item records deleted; files on disk are untouched"`
	}
	add(r, deleteTool, &mcp.Tool{
		Name:        "library_issues_remove",
		Description: "Delete the library records of every item whose folder is missing or has no playable media (audit_issues lists them first). Files on disk are untouched, but listening progress for those items is lost. Admin only.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in removeIssuesIn) (*mcp.CallToolResult, removeIssuesOut, error) {
		lib, err := resolveLibrary(ctx, client, in.Library)
		if err != nil {
			return nil, removeIssuesOut{}, err
		}
		before, err := client.Items(ctx, lib.ID, abs.ItemsOptions{Limit: 1, Filter: "issues", Minified: true})
		if err != nil {
			return nil, removeIssuesOut{}, err
		}
		if before.Total == 0 {
			return nil, removeIssuesOut{}, nil
		}
		if err := client.RemoveIssues(ctx, lib.ID); err != nil {
			return nil, removeIssuesOut{}, err
		}

		return nil, removeIssuesOut{Removed: before.Total}, nil
	})
}

// sortKey maps the friendly sort names to the server's sort fields.
func sortKey(sortBy string, podcast bool) string {
	switch strings.ToLower(strings.TrimSpace(sortBy)) {
	case "", "title":
		return "media.metadata.title"
	case "author":
		if podcast {
			return "media.metadata.author"
		}
		return "media.metadata.authorName"
	case "author_last_first", "authorlf":
		return "media.metadata.authorNameLF"
	case "year", "published":
		return "media.metadata.publishedYear"
	case "duration":
		return "media.duration"
	case "size":
		return "size"
	case "added":
		return "addedAt"
	case "modified":
		return "mtimeMs"
	case "created", "birthtime":
		return "birthtimeMs"
	case "episodes":
		return "media.numTracks"
	default:
		// sequence, progress, random, or a raw server key
		return sortBy
	}
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

	group, value, _ := strings.Cut(filter, ":")
	group = strings.TrimSpace(group)
	value = strings.TrimSpace(value)

	// the web client uses these exact group names
	switch strings.ToLower(group) {
	case "genre":
		group = "genres"
	case "tag":
		group = "tags"
	case "author":
		group = "authors"
	case "narrator":
		group = "narrators"
	case "language":
		group = "languages"
	case "publisher":
		group = "publishers"
	case "decade", "decades", "publisheddecade":
		group = "publishedDecades"
	case "ebook":
		group = "ebooks"
	case "track":
		group = "tracks"
	}

	if value == "" {
		switch group {
		case "issues", "explicit", "feed-open", "recent", "share-open":
			return group, nil
		case "abridged":
			return abs.EncodeFilter(group, "abridged"), nil
		default:
			return "", fmt.Errorf("filter %s needs a value, e.g. %s:<name>", group, group)
		}
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
		found := ""
		for _, ref := range refs {
			if strings.EqualFold(ref.Name, value) || ref.ID == value {
				found = ref.ID
				break
			}
		}
		if found == "" {
			return "", fmt.Errorf("no %s named %q in %s (library_filters lists them)", group, value, lib.Name)
		}
		value = found
	}

	return abs.EncodeFilter(group, value), nil
}
