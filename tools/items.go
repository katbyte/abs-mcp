package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/katbyte/abs-mcp/lib/abs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// itemRef is the common way tools name an item: by id, or by title when it
// is unambiguous.
type itemRef struct {
	Item    string `json:"item"              jsonschema:"library item id, or an exact title (must match one item)"`
	Library string `json:"library,omitempty" jsonschema:"narrow a title lookup to one library by name or id"`
}

func registerItemTools(r *registry) {
	client := r.client

	type chapterRow struct {
		Index int     `json:"index"`
		Title string  `json:"title"`
		Start string  `json:"start"`
		End   string  `json:"end"`
		Sec   float64 `json:"start_seconds"`
	}
	type fileRow struct {
		Index    int    `json:"index,omitempty"`
		Filename string `json:"filename"`
		Duration string `json:"duration,omitempty"`
		SizeMB   int64  `json:"size_mb"`
		Codec    string `json:"codec,omitempty"`
		Bitrate  int64  `json:"bitrate_kbps,omitempty"`
		Channels int    `json:"channels,omitempty"`
		Excluded bool   `json:"excluded,omitempty"     jsonschema:"not part of the playable tracks"`
		Error    string `json:"error,omitempty"`
		Type     string `json:"type,omitempty"         jsonschema:"for non-audio files: ebook, image, text, metadata"`
	}
	type getIn struct {
		itemRef
		Chapters bool `json:"chapters,omitempty" jsonschema:"include the full chapter list; a long book can run to thousands of tokens, and chapter_count is always reported, so check that first. Books only: a podcast's chapters belong to each episode, see podcast_episode_get"`
		Files    bool `json:"files,omitempty"    jsonschema:"include every audio track with codec, bitrate, duration and any probe error, plus the non-audio files; for a podcast the tracks are its downloaded episodes"`
	}
	type getOut struct {
		itemSummary
		Description   string           `json:"description,omitempty"`
		PublishedDate string           `json:"published_date,omitempty"`
		Authors       []abs.NameRef    `json:"authors,omitempty"        jsonschema:"with ids for author_get"`
		SeriesRefs    []abs.SeriesRef  `json:"series_refs,omitempty"    jsonschema:"with ids for series_get"`
		LibraryID     string           `json:"library_id"`
		FullPath      string           `json:"full_path,omitempty"`
		LastScan      string           `json:"last_scan,omitempty"`
		Audio         string           `json:"audio,omitempty"          jsonschema:"codec, bitrate and channels of the first track"`
		OtherFiles    []fileRow        `json:"other_files,omitempty"    jsonschema:"non-audio files in the folder"`
		Episodes      []episodeSummary `json:"episodes,omitempty"       jsonschema:"podcasts: newest 25 episodes; podcast_episodes lists all"`
		EpisodeTotal  int              `json:"episode_total,omitempty"`
		ChapterList   *[]chapterRow    `json:"chapter_list,omitempty"   jsonschema:"only when chapters is true; an empty list means the book genuinely has none"`
		TrackList     *[]fileRow       `json:"track_list,omitempty"     jsonschema:"only when files is true: audio files in play order"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "item_get",
		Description: "One book or podcast: metadata with provider ids, description, listening progress, how many chapters and tracks it has, and for podcasts the newest episodes. Set chapters or files to pull in the full lists - both are off by default because they are unbounded, and the counts in the default answer are usually enough to decide.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in getIn) (*mcp.CallToolResult, getOut, error) {
		it, err := resolveItem(ctx, client, in.Library, in.Item)
		if err != nil {
			return nil, getOut{}, err
		}

		m := &it.Media.Metadata
		out := getOut{
			itemSummary:   summarize(it),
			Description:   clip(plain(m.Description), 1500),
			PublishedDate: m.PublishedDate,
			Authors:       m.Authors,
			SeriesRefs:    m.Series,
			LibraryID:     it.LibraryID,
			FullPath:      it.Path,
			LastScan:      fmtTime(it.LastScan),
		}
		if len(it.Media.AudioFiles) > 0 {
			af := it.Media.AudioFiles[0]
			out.Audio = fmt.Sprintf("%s %d kbps %dch", af.Codec, af.BitRate/1000, af.Channels)
		}
		for _, f := range it.LibraryFiles {
			if f.FileType == "audio" {
				continue
			}
			out.OtherFiles = append(out.OtherFiles, fileRow{Filename: f.Metadata.Filename, SizeMB: mb(f.Metadata.Size), Type: f.FileType})
		}
		if it.IsPodcast() {
			eps := it.Media.Episodes
			out.EpisodeTotal = len(eps)
			// newest first
			for i := len(eps) - 1; i >= 0 && len(out.Episodes) < defaultLimit; i-- {
				out.Episodes = append(out.Episodes, summarizeEpisode(&eps[i], "", false))
			}
		}

		// the unbounded parts, only when asked for: a 300-chapter book is around
		// 19 times the rest of this answer
		if in.Chapters {
			list := []chapterRow{}
			for _, ch := range it.Media.Chapters {
				list = append(list, chapterRow{Index: ch.ID, Title: ch.Title, Start: fmtDuration(ch.Start), End: fmtDuration(ch.End), Sec: ch.Start})
			}
			out.ChapterList = &list
		}
		if in.Files {
			audio := it.Media.AudioFiles
			if it.IsPodcast() {
				for _, e := range it.Media.Episodes {
					if e.AudioFile != nil {
						audio = append(audio, *e.AudioFile)
					}
				}
			}
			list := []fileRow{}
			for _, af := range audio {
				list = append(list, fileRow{
					Index:    af.Index,
					Filename: af.Metadata.Filename,
					Duration: fmtDuration(af.Duration),
					SizeMB:   mb(af.Metadata.Size),
					Codec:    af.Codec,
					Bitrate:  af.BitRate / 1000,
					Channels: af.Channels,
					Excluded: af.Exclude,
					Error:    af.Error,
				})
			}
			out.TrackList = &list
		}

		return nil, out, nil
	})

	type editIn struct {
		itemRef
		Title         string   `json:"title,omitempty"`
		Subtitle      string   `json:"subtitle,omitempty"`
		Authors       []string `json:"authors,omitempty"        jsonschema:"replacement author list (books); new names are created"`
		Narrators     []string `json:"narrators,omitempty"      jsonschema:"replacement narrator list (books)"`
		Series        []string `json:"series,omitempty"         jsonschema:"replacement series list as 'Name' or 'Name #2' (books)"`
		Genres        []string `json:"genres,omitempty"         jsonschema:"replacement genre list"`
		Tags          []string `json:"tags,omitempty"           jsonschema:"replacement tag list"`
		Year          string   `json:"year,omitempty"           jsonschema:"published year"`
		Publisher     string   `json:"publisher,omitempty"`
		Description   string   `json:"description,omitempty"`
		ISBN          string   `json:"isbn,omitempty"`
		ASIN          string   `json:"asin,omitempty"`
		Language      string   `json:"language,omitempty"`
		Explicit      *bool    `json:"explicit,omitempty"`
		Abridged      *bool    `json:"abridged,omitempty"`
		PodcastAuthor string   `json:"podcast_author,omitempty" jsonschema:"podcasts only"`
		FeedURL       string   `json:"feed_url,omitempty"       jsonschema:"podcasts only"`
		Clear         []string `json:"clear,omitempty"          jsonschema:"fields to blank: subtitle, narrators, series, genres, tags, year, publisher, description, isbn, asin, language"`
	}
	type editOut struct {
		Updated bool     `json:"updated"`
		Fields  []string `json:"fields_sent"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "item_edit",
		Description: "Edit an item's metadata: title, authors, narrators, series, genres, tags, year, publisher, description, isbn, asin, language, explicit/abridged flags. Only provided fields change; list a field in clear to blank it. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in editIn) (*mcp.CallToolResult, editOut, error) {
		it, err := resolveItem(ctx, client, in.Library, in.Item)
		if err != nil {
			return nil, editOut{}, err
		}

		md := abs.MetadataUpdate{}
		upd := abs.MediaUpdate{}
		var fields []string
		set := func(name string, dst **string, val string) {
			if val != "" {
				*dst = &val
				fields = append(fields, name)
			}
		}
		set("title", &md.Title, in.Title)
		set("subtitle", &md.Subtitle, in.Subtitle)
		set("year", &md.PublishedYear, in.Year)
		set("publisher", &md.Publisher, in.Publisher)
		set("description", &md.Description, in.Description)
		set("isbn", &md.ISBN, in.ISBN)
		set("asin", &md.ASIN, in.ASIN)
		set("language", &md.Language, in.Language)
		set("podcast_author", &md.Author, in.PodcastAuthor)
		set("feed_url", &md.FeedURL, in.FeedURL)
		if in.Explicit != nil {
			md.Explicit = in.Explicit
			fields = append(fields, "explicit")
		}
		if in.Abridged != nil {
			md.Abridged = in.Abridged
			fields = append(fields, "abridged")
		}
		if len(in.Authors) > 0 {
			for _, a := range in.Authors {
				md.Authors = append(md.Authors, abs.NameRef{Name: strings.TrimSpace(a)})
			}
			fields = append(fields, "authors")
		}
		if len(in.Narrators) > 0 {
			md.Narrators = in.Narrators
			fields = append(fields, "narrators")
		}
		if len(in.Series) > 0 {
			for _, s := range in.Series {
				name, seq, _ := strings.Cut(s, " #")
				md.Series = append(md.Series, abs.SeriesRef{Name: strings.TrimSpace(name), Sequence: strings.TrimSpace(seq)})
			}
			fields = append(fields, "series")
		}
		if len(in.Genres) > 0 {
			md.Genres = in.Genres
			fields = append(fields, "genres")
		}
		if len(in.Tags) > 0 {
			upd.Tags = in.Tags
			fields = append(fields, "tags")
		}

		empty := ""
		for _, c := range in.Clear {
			fields = append(fields, "clear:"+c)
			switch strings.ToLower(c) {
			case "subtitle":
				md.Subtitle = &empty
			case "narrators", "narrator":
				md.Narrators = []string{}
			case "series":
				md.Series = []abs.SeriesRef{}
			case "genres":
				md.Genres = []string{}
			case "tags":
				upd.Tags = []string{}
			case "year":
				md.PublishedYear = &empty
			case "publisher":
				md.Publisher = &empty
			case "description":
				md.Description = &empty
			case "isbn":
				md.ISBN = &empty
			case "asin":
				md.ASIN = &empty
			case "language":
				md.Language = &empty
			default:
				return nil, editOut{}, fmt.Errorf("cannot clear %q", c)
			}
		}
		if len(fields) == 0 {
			return nil, editOut{}, errors.New("nothing to change: pass at least one field")
		}

		// empty slices must survive JSON encoding to clear server-side
		upd.Metadata = &md
		updated, err := client.UpdateMedia(ctx, it.ID, upd)
		if err != nil {
			return nil, editOut{}, err
		}

		return nil, editOut{Updated: updated, Fields: fields}, nil
	})

	type matchIn struct {
		itemRef
		Provider string `json:"provider,omitempty" jsonschema:"metadata provider (server_info lists them); default the library's"`
		Title    string `json:"title,omitempty"    jsonschema:"override the search title; default the item's"`
		Author   string `json:"author,omitempty"   jsonschema:"override the search author; default the item's"`
		Limit    int    `json:"limit,omitempty"    jsonschema:"maximum candidates, default 8"`
	}
	type candidate struct {
		Index       int      `json:"index"                 jsonschema:"pass to item_match_apply as candidate"`
		Title       string   `json:"title"`
		Subtitle    string   `json:"subtitle,omitempty"`
		Author      string   `json:"author,omitempty"`
		Narrator    string   `json:"narrator,omitempty"`
		Series      []string `json:"series,omitempty"`
		Year        string   `json:"year,omitempty"`
		Publisher   string   `json:"publisher,omitempty"`
		Duration    string   `json:"duration,omitempty"    jsonschema:"compare with the item's duration to confirm the edition"`
		ASIN        string   `json:"asin,omitempty"`
		ISBN        string   `json:"isbn,omitempty"`
		Language    string   `json:"language,omitempty"`
		Abridged    bool     `json:"abridged,omitempty"`
		HasCover    bool     `json:"has_cover"`
		Description string   `json:"description,omitempty"`
	}
	type matchOut struct {
		Item         string      `json:"item"`
		ItemDuration string      `json:"item_duration,omitempty"`
		Provider     string      `json:"provider"`
		Candidates   []candidate `json:"candidates"              jsonschema:"suggestions only; nothing changes until item_match_apply"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "item_match",
		Description: "Ask a metadata provider for candidate matches for a book (suggestions only). Compare title, author, narrator and duration against the item before applying one with item_match_apply.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in matchIn) (*mcp.CallToolResult, matchOut, error) {
		it, err := resolveItem(ctx, client, in.Library, in.Item)
		if err != nil {
			return nil, matchOut{}, err
		}
		if it.IsPodcast() {
			return nil, matchOut{}, errNotBook
		}

		provider, title, author := matchQuery(ctx, client, it, in.Provider, in.Title, in.Author)
		results, err := client.SearchBooks(ctx, provider, title, author, it.ID)
		if err != nil {
			return nil, matchOut{}, err
		}

		out := matchOut{Item: it.Title(), ItemDuration: fmtDuration(it.Media.Duration), Provider: provider, Candidates: []candidate{}}
		for i, res := range results {
			if i >= limitOr(in.Limit, 8) {
				break
			}
			c := candidate{
				Index:       i,
				Title:       res.Title,
				Subtitle:    res.Subtitle,
				Author:      res.Author,
				Narrator:    res.Narrator,
				Year:        res.PublishedYear.String(),
				Publisher:   res.Publisher,
				Duration:    fmtDuration(res.Duration * 60),
				ASIN:        res.ASIN,
				ISBN:        res.ISBN,
				Language:    res.Language,
				Abridged:    res.Abridged,
				HasCover:    res.Cover != "",
				Description: clip(plain(res.Description), 200),
			}
			for _, s := range res.Series {
				if s.Sequence != "" {
					c.Series = append(c.Series, s.Name+" #"+s.Sequence)
				} else {
					c.Series = append(c.Series, s.Name)
				}
			}
			out.Candidates = append(out.Candidates, c)
		}

		return nil, out, nil
	})

	type applyIn struct {
		itemRef
		Provider        string `json:"provider,omitempty"         jsonschema:"must match the provider used in item_match"`
		Candidate       *int   `json:"candidate,omitempty"        jsonschema:"index from item_match; the candidate's asin/isbn is used to match exactly"`
		ASIN            string `json:"asin,omitempty"             jsonschema:"match this asin directly instead of a candidate"`
		ISBN            string `json:"isbn,omitempty"             jsonschema:"match this isbn directly instead of a candidate"`
		Title           string `json:"title,omitempty"            jsonschema:"must match the title override used in item_match"`
		Author          string `json:"author,omitempty"           jsonschema:"must match the author override used in item_match"`
		OverrideDetails bool   `json:"override_details,omitempty" jsonschema:"replace existing metadata fields instead of only filling empty ones"`
		OverrideCover   bool   `json:"override_cover,omitempty"   jsonschema:"replace the existing cover"`
	}
	type applyOut struct {
		Updated bool         `json:"updated"`
		Warning string       `json:"warning,omitempty"`
		Item    *itemSummary `json:"item,omitempty"    jsonschema:"the item after matching"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "item_match_apply",
		Description: "Apply a match: pull metadata and cover from the provider into the item, for a candidate from item_match or an asin/isbn you already know (one of candidate, asin or isbn is required: the match is never left to the provider's first guess). By default only empty fields are filled; set override_details to replace. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in applyIn) (*mcp.CallToolResult, applyOut, error) {
		// a match with nothing to name the book would take the provider's
		// first hit unseen, and with override_details replace the metadata
		// with it; the omitted argument is an error, not a quick match
		if in.Candidate == nil && strings.TrimSpace(in.ASIN) == "" && strings.TrimSpace(in.ISBN) == "" {
			return nil, applyOut{}, errors.New("pass candidate (an index from item_match), asin or isbn to say which book to apply")
		}

		it, err := resolveItem(ctx, client, in.Library, in.Item)
		if err != nil {
			return nil, applyOut{}, err
		}
		if it.IsPodcast() {
			return nil, applyOut{}, errNotBook
		}

		provider, title, author := matchQuery(ctx, client, it, in.Provider, in.Title, in.Author)
		opts := abs.MatchOptions{Provider: provider, ASIN: in.ASIN, ISBN: in.ISBN, OverrideCover: in.OverrideCover, OverrideDetails: in.OverrideDetails}

		if in.Candidate != nil && opts.ASIN == "" && opts.ISBN == "" {
			results, serr := client.SearchBooks(ctx, provider, title, author, it.ID)
			if serr != nil {
				return nil, applyOut{}, serr
			}
			if *in.Candidate < 0 || *in.Candidate >= len(results) {
				return nil, applyOut{}, fmt.Errorf("candidate %d out of range (item_match returned %d)", *in.Candidate, len(results))
			}
			c := results[*in.Candidate]
			opts.ASIN, opts.ISBN = c.ASIN, c.ISBN
			if opts.ASIN == "" && opts.ISBN == "" {
				// providers without ids: match by the candidate's exact title/author
				opts.Title, opts.Author = c.Title, c.Author
			}
		}

		res, err := client.Match(ctx, it.ID, opts)
		if err != nil {
			return nil, applyOut{}, err
		}
		out := applyOut{Updated: res.Updated, Warning: res.Warning}
		if res.LibraryItem != nil {
			s := summarize(res.LibraryItem)
			out.Item = &s
		}

		return nil, out, nil
	})

	type batchEditIn struct {
		Library string   `json:"library,omitempty"   jsonschema:"library name or id, for resolving titles"`
		Items   []string `json:"items"               jsonschema:"the books to change, by id or exact title"`
		Genres  []string `json:"genres,omitempty"    jsonschema:"replacement genre list, applied to every item"`
		Tags    []string `json:"tags,omitempty"      jsonschema:"replacement tag list, applied to every item"`
		Authors []string `json:"authors,omitempty"   jsonschema:"replacement author list"`
		Year    string   `json:"year,omitempty"`
		Publish string   `json:"publisher,omitempty"`
		Lang    string   `json:"language,omitempty"`
	}
	type batchEditOut struct {
		Updated int      `json:"items_updated"`
		Items   []string `json:"items"         jsonschema:"the titles that were sent"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "item_batch_edit",
		Description: "Apply the same metadata to many books in one call - a genre on forty titles, a publisher on a series. Every field given replaces that field on every item listed; fields left out are untouched. Use item_edit for one item, or for fields that differ per item. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in batchEditIn) (*mcp.CallToolResult, batchEditOut, error) {
		if len(in.Items) == 0 {
			return nil, batchEditOut{}, errors.New("at least one item is required")
		}

		md := abs.MetadataUpdate{}
		var hasMeta bool
		if len(in.Genres) > 0 {
			md.Genres, hasMeta = in.Genres, true
		}
		if len(in.Authors) > 0 {
			for _, a := range in.Authors {
				md.Authors = append(md.Authors, abs.NameRef{Name: a})
			}
			hasMeta = true
		}
		for _, f := range []struct {
			dst **string
			val string
		}{{&md.PublishedYear, in.Year}, {&md.Publisher, in.Publish}, {&md.Language, in.Lang}} {
			if f.val != "" {
				*f.dst = new(f.val)
				hasMeta = true
			}
		}
		if len(in.Tags) == 0 && !hasMeta {
			return nil, batchEditOut{}, errors.New("nothing to change: pass genres, tags, authors, year, publisher or language")
		}

		out := batchEditOut{Items: make([]string, 0, len(in.Items))}
		updates := make([]abs.BatchMediaUpdate, 0, len(in.Items))
		for _, ref := range in.Items {
			it, err := resolveItem(ctx, client, in.Library, ref)
			if err != nil {
				return nil, batchEditOut{}, err
			}
			if it.IsPodcast() {
				return nil, batchEditOut{}, fmt.Errorf("%s is a podcast; item_batch_edit is for books", it.Title())
			}
			upd := abs.MediaUpdate{}
			if len(in.Tags) > 0 {
				upd.Tags = in.Tags // an explicit empty list would clear them
			}
			if hasMeta {
				upd.Metadata = new(md)
			}
			updates = append(updates, abs.BatchMediaUpdate{ID: it.ID, MediaPayload: upd})
			out.Items = append(out.Items, it.Title())
		}

		n, err := client.BatchUpdate(ctx, updates)
		if err != nil {
			return nil, batchEditOut{}, err
		}
		out.Updated = n

		return nil, out, nil
	})

	type rescanOut struct {
		Result string `json:"result" jsonschema:"NOTHING, ADDED, UPDATED, REMOVED or UPTODATE"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "item_rescan",
		Description: "Rescan one item's folder for changed files (new tracks, replaced cover, edited metadata.json). Admin only. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in itemRef) (*mcp.CallToolResult, rescanOut, error) {
		it, err := resolveItem(ctx, client, in.Library, in.Item)
		if err != nil {
			return nil, rescanOut{}, err
		}
		res, err := client.ScanItem(ctx, it.ID)
		if err != nil {
			return nil, rescanOut{}, err
		}

		return nil, rescanOut{Result: res}, nil
	})

	type coverSearchIn struct {
		itemRef
		Provider string `json:"provider,omitempty" jsonschema:"cover provider; default the library's; audiobookcovers searches audiobookcovers.com"`
		Title    string `json:"title,omitempty"    jsonschema:"override the search title"`
		Author   string `json:"author,omitempty"   jsonschema:"override the search author"`
	}
	type coverSearchOut struct {
		Item   string   `json:"item"`
		Covers []string `json:"covers" jsonschema:"image urls; pass one to item_cover_edit"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "item_cover_search",
		Description: "Find candidate cover images for an item from the metadata providers. Returns urls for item_cover_edit.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in coverSearchIn) (*mcp.CallToolResult, coverSearchOut, error) {
		it, err := resolveItem(ctx, client, in.Library, in.Item)
		if err != nil {
			return nil, coverSearchOut{}, err
		}
		provider, title, author := matchQuery(ctx, client, it, in.Provider, in.Title, in.Author)
		if it.IsPodcast() {
			author = ""
		}
		covers, err := client.SearchCovers(ctx, provider, title, author, it.IsPodcast())
		if err != nil {
			return nil, coverSearchOut{}, err
		}
		if covers == nil {
			covers = []string{}
		}

		return nil, coverSearchOut{Item: it.Title(), Covers: covers}, nil
	})

	type doneOut struct {
		Done bool `json:"done"`
	}
	type coverEditIn struct {
		itemRef
		URL    string `json:"url,omitempty"    jsonschema:"image url to download as the cover"`
		File   string `json:"file,omitempty"   jsonschema:"instead of a url: the path of an image already in the item's folder (item_get with files lists them)"`
		Remove bool   `json:"remove,omitempty" jsonschema:"instead of setting one: delete the current cover"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "item_cover_edit",
		Description: "Set an item's cover from a url (e.g. from item_cover_search) or from an image file already in its folder, or with remove delete the current cover. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in coverEditIn) (*mcp.CallToolResult, doneOut, error) {
		// removal has to be asked for: a call that forgot its url must not
		// take the cover away
		if in.URL == "" && in.File == "" && !in.Remove {
			return nil, doneOut{}, errors.New("pass url or file to set the cover, or remove to delete it")
		}
		it, err := resolveItem(ctx, client, in.Library, in.Item)
		if err != nil {
			return nil, doneOut{}, err
		}

		switch {
		case in.URL != "":
			err = client.SetCoverFromURL(ctx, it.ID, in.URL)
		case in.File != "":
			err = client.SetCoverFromFile(ctx, it.ID, in.File)
		default:
			err = client.RemoveCover(ctx, it.ID)
		}
		if err != nil {
			return nil, doneOut{}, err
		}

		return nil, doneOut{Done: true}, nil
	})

	type chapterIn struct {
		Title string  `json:"title"`
		Start float64 `json:"start" jsonschema:"start time in seconds"`
	}
	type chaptersSetIn struct {
		itemRef
		Chapters []chapterIn `json:"chapters,omitempty"  jsonschema:"replacement chapter list in order; each ends where the next starts"`
		FromASIN string      `json:"from_asin,omitempty" jsonschema:"instead of a list: fetch Audible's chapters for this asin (default the item's asin)"`
		Region   string      `json:"region,omitempty"    jsonschema:"Audible region for from_asin: us, uk, ca, au, de, fr, it, es, jp, in"`
	}
	type chaptersSetOut struct {
		Updated  bool `json:"updated"`
		Chapters int  `json:"chapters"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "item_chapters_set",
		Description: "Replace a book's chapters, either with an explicit list or with the chapters Audible has for its asin. The last chapter runs to the end of the audio. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in chaptersSetIn) (*mcp.CallToolResult, chaptersSetOut, error) {
		it, err := resolveItem(ctx, client, in.Library, in.Item)
		if err != nil {
			return nil, chaptersSetOut{}, err
		}
		if it.IsPodcast() {
			return nil, chaptersSetOut{}, errNotBook
		}

		var chapters []abs.Chapter
		switch {
		case len(in.Chapters) > 0:
			for i, ch := range in.Chapters {
				end := it.Media.Duration
				if i+1 < len(in.Chapters) {
					end = in.Chapters[i+1].Start
				}
				chapters = append(chapters, abs.Chapter{ID: i, Title: ch.Title, Start: ch.Start, End: end})
			}
		default:
			asin := in.FromASIN
			if asin == "" {
				asin = it.Media.Metadata.ASIN
			}
			if asin == "" {
				return nil, chaptersSetOut{}, errors.New("pass chapters, or from_asin for an item without an asin")
			}
			chapters, err = client.SearchChapters(ctx, asin, in.Region)
			if err != nil {
				return nil, chaptersSetOut{}, err
			}
			if len(chapters) == 0 {
				return nil, chaptersSetOut{}, fmt.Errorf("no chapters found for asin %s", asin)
			}
		}

		updated, err := client.SetChapters(ctx, it.ID, chapters)
		if err != nil {
			return nil, chaptersSetOut{}, err
		}

		return nil, chaptersSetOut{Updated: updated, Chapters: len(chapters)}, nil
	})

	type embedIn struct {
		itemRef
		Backup bool `json:"backup,omitempty" jsonschema:"keep a copy of the original audio files"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "item_embed_metadata",
		Description: "Write the item's metadata and chapters into its audio files' tags so they travel with the files. audit_unembedded lists the books where this is due. Admin only. Changes the files on disk; runs in the background (see server_tasks).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in embedIn) (*mcp.CallToolResult, scanStartedOut, error) {
		it, err := resolveItem(ctx, client, in.Library, in.Item)
		if err != nil {
			return nil, scanStartedOut{}, err
		}
		if err := client.EmbedMetadata(ctx, it.ID, true, in.Backup); err != nil {
			return nil, scanStartedOut{}, err
		}

		return nil, scanStartedOut{Started: it.Title()}, nil
	})

	type deleteIn struct {
		itemRef
		DeleteFiles bool `json:"delete_files,omitempty" jsonschema:"also delete the item's folder from disk (irreversible); default keeps the files"`
	}
	type deleteOut struct {
		Deleted      string `json:"deleted"`
		FilesRemoved bool   `json:"files_removed"`
	}
	add(r, deleteTool, &mcp.Tool{
		Name:        "item_delete",
		Description: "Remove an item from the library, and with delete_files also erase its folder from disk. Listening progress for it is lost. Requires the delete permission.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in deleteIn) (*mcp.CallToolResult, deleteOut, error) {
		it, err := resolveItem(ctx, client, in.Library, in.Item)
		if err != nil {
			return nil, deleteOut{}, err
		}
		if err := client.DeleteItem(ctx, it.ID, in.DeleteFiles); err != nil {
			return nil, deleteOut{}, err
		}

		return nil, deleteOut{Deleted: it.Title(), FilesRemoved: in.DeleteFiles}, nil
	})
}

type scanStartedOut struct {
	Started string `json:"started" jsonschema:"runs in the background; poll server_tasks for completion"`
}

// matchQuery fills in the provider, title and author for a provider search
// from the item and its library when not overridden.
func matchQuery(ctx context.Context, client *abs.Client, it *abs.Item, provider, title, author string) (resolvedProvider, resolvedTitle, resolvedAuthor string) {
	if title == "" {
		title = it.Title()
	}
	if author == "" {
		author = it.Media.Metadata.AuthorDisplay()
	}
	if provider == "" {
		if lib, err := client.Library(ctx, it.LibraryID); err == nil {
			provider = lib.Provider
		}
	}
	return provider, title, author
}
