package tools

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/katbyte/abs-mcp/lib/abs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// itemRef is the common way tools name an item: by id, or by title when it
// is unambiguous.
type itemRef struct {
	Item    string `json:"item"              jsonschema:"library item id, or its title: the whole title of one item (a tool that only reads also takes a part of one title)"`
	Library string `json:"library,omitempty" jsonschema:"narrow a title lookup to one library by name or id"`
}

func registerItemTools(r *registry) {
	client := r.client
	prov := r.providerConfig()

	type chapterRow struct {
		Index int     `json:"index"`
		Title string  `json:"title"`
		Start float64 `json:"start_s" jsonschema:"seconds into the audio, as item_chapters_set's start_s takes it"`
		End   float64 `json:"end_s"   jsonschema:"seconds into the audio"`
	}
	type fileRow struct {
		Index    int    `json:"index,omitempty"`
		Filename string `json:"filename"`
		Duration int    `json:"duration_s,omitempty"   jsonschema:"length in seconds"`
		Size     int64  `json:"size"                   jsonschema:"bytes"`
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
	type downloadSettings struct {
		AutoDownload bool   `json:"auto_download"`
		Schedule     string `json:"schedule,omitempty"   jsonschema:"cron expression of the automatic check"`
		KeepEpisodes int    `json:"keep_episodes"        jsonschema:"0 keeps them all"`
		NewPerCheck  int    `json:"new_per_check"        jsonschema:"0 downloads every new one"`
		LastCheck    string `json:"last_check,omitempty" jsonschema:"when the feed was last checked for new episodes"`
	}
	type getOut struct {
		itemSummary
		Downloads     *downloadSettings `json:"downloads,omitempty"      jsonschema:"podcasts: the automatic download settings podcast_settings changes"`
		Description   string            `json:"description,omitempty"`
		PublishedDate string            `json:"published_date,omitempty"`
		Authors       []abs.NameRef     `json:"authors,omitempty"        jsonschema:"with ids for author_get"`
		SeriesRefs    []abs.SeriesRef   `json:"series_refs,omitempty"    jsonschema:"with ids for series_get"`
		LibraryID     string            `json:"library_id"`
		FullPath      string            `json:"full_path,omitempty"`
		LastScan      string            `json:"last_scan,omitempty"`
		Audio         string            `json:"audio,omitempty"          jsonschema:"codec, bitrate and channels of the first track"`
		OtherFiles    []fileRow         `json:"other_files,omitempty"    jsonschema:"non-audio files in the folder"`
		Episodes      []episodeSummary  `json:"episodes,omitempty"       jsonschema:"podcasts: newest 25 episodes; podcast_episodes lists all"`
		EpisodeTotal  int               `json:"episode_total,omitempty"`
		ChapterList   *[]chapterRow     `json:"chapter_list,omitempty"   jsonschema:"only when chapters is true; an empty list means the book genuinely has none"`
		TrackList     *[]fileRow        `json:"track_list,omitempty"     jsonschema:"only when files is true: audio files in play order"`
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
			out.OtherFiles = append(out.OtherFiles, fileRow{Filename: f.Metadata.Filename, Size: f.Metadata.Size, Type: f.FileType})
		}
		if it.IsPodcast() {
			out.Downloads = &downloadSettings{
				AutoDownload: it.Media.AutoDownloadEpisodes,
				Schedule:     it.Media.AutoDownloadSchedule,
				KeepEpisodes: it.Media.MaxEpisodesToKeep,
				NewPerCheck:  it.Media.MaxNewEpisodesToDownload,
				LastCheck:    fmtTime(it.Media.LastEpisodeCheck),
			}
			eps := newestEpisodes(it.Media.Episodes)
			out.EpisodeTotal = len(eps)
			for i := 0; i < len(eps) && len(out.Episodes) < defaultLimit; i++ {
				out.Episodes = append(out.Episodes, summarizeEpisode(&eps[i], "", false))
			}
		}

		// the unbounded parts, only when asked for: a 300-chapter book is around
		// 19 times the rest of this answer
		if in.Chapters {
			list := []chapterRow{}
			for _, ch := range it.Media.Chapters {
				list = append(list, chapterRow{Index: ch.ID, Title: ch.Title, Start: ch.Start, End: ch.End})
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
					Duration: wholeSec(af.Duration),
					Size:     af.Metadata.Size,
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
		Series        []string `json:"series,omitempty"         jsonschema:"replacement series list as 'Name' or 'Name #2' (books); every series not listed is dropped, so use add_series to link one more"`
		AddSeries     []string `json:"add_series,omitempty"     jsonschema:"series to add to the item's own as 'Name' or 'Name #2', keeping the rest; a series it is already in takes the number given"`
		RemoveSeries  []string `json:"remove_series,omitempty"  jsonschema:"series to take the item out of, by name, keeping the rest"`
		Genres        []string `json:"genres,omitempty"         jsonschema:"replacement genre list"`
		Tags          []string `json:"tags,omitempty"           jsonschema:"replacement tag list"`
		AddTags       []string `json:"add_tags,omitempty"       jsonschema:"tags to add to the item's own, keeping the rest"`
		RemoveTags    []string `json:"remove_tags,omitempty"    jsonschema:"tags to take off the item, keeping the rest"`
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
		Description: "Edit an item's metadata: title, authors, narrators, series, genres, tags, year, publisher, description, isbn, asin, language, explicit/abridged flags. Only provided fields change; list a field in clear to blank it. series and tags replace the list, add_series, remove_series, add_tags and remove_tags edit it. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in editIn) (*mcp.CallToolResult, editOut, error) {
		it, err := resolveItemToChange(ctx, client, in.Library, in.Item)
		if err != nil {
			return nil, editOut{}, err
		}
		defer r.locks.hold(itemKeys(it.ID)...)()
		// the lists are edited from the item as it is once held, not as the
		// lookup found it: a call running alongside may have changed them
		if len(in.AddSeries)+len(in.RemoveSeries)+len(in.AddTags)+len(in.RemoveTags) > 0 {
			if it, err = client.Item(ctx, it.ID); err != nil {
				return nil, editOut{}, err
			}
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
				md.Series = append(md.Series, parseSeriesRef(s))
			}
			fields = append(fields, "series")
		}
		if len(in.AddSeries) > 0 || len(in.RemoveSeries) > 0 {
			if len(in.Series) > 0 || slices.ContainsFunc(in.Clear, func(c string) bool { return strings.EqualFold(c, "series") }) {
				return nil, editOut{}, errors.New("series replaces the list; add_series and remove_series edit it. One or the other")
			}
			refs, serr := editSeriesList(it.Media.Metadata.Series, in.AddSeries, in.RemoveSeries)
			if serr != nil {
				return nil, editOut{}, serr
			}
			md.Series = refs
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
		if len(in.AddTags) > 0 || len(in.RemoveTags) > 0 {
			if len(in.Tags) > 0 || slices.ContainsFunc(in.Clear, func(c string) bool { return strings.EqualFold(c, "tags") }) {
				return nil, editOut{}, errors.New("tags replaces the list; add_tags and remove_tags edit it. One or the other")
			}
			upd.Tags = editTagList(it.Media.Tags, in.AddTags, in.RemoveTags)
			fields = append(fields, "tags")
		}

		// a field both given and cleared would be cleared, the clear being
		// applied last, and the value given lost without a word
		given := map[string]bool{
			"subtitle": in.Subtitle != "", "narrators": len(in.Narrators) > 0, "narrator": len(in.Narrators) > 0,
			"series": len(in.Series) > 0, "genres": len(in.Genres) > 0, "tags": len(in.Tags) > 0,
			"year": in.Year != "", "publisher": in.Publisher != "", "description": in.Description != "",
			"isbn": in.ISBN != "", "asin": in.ASIN != "", "language": in.Language != "",
		}
		empty := ""
		for _, c := range in.Clear {
			if given[strings.ToLower(c)] {
				return nil, editOut{}, fmt.Errorf("%s is both given and in clear; one or the other", c)
			}
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
		Duration    int      `json:"duration_s,omitempty"  jsonschema:"length in seconds; compare with item_duration_s to confirm the edition"`
		ASIN        string   `json:"asin,omitempty"`
		ISBN        string   `json:"isbn,omitempty"`
		Language    string   `json:"language,omitempty"`
		Abridged    bool     `json:"abridged,omitempty"`
		HasCover    bool     `json:"has_cover"`
		Description string   `json:"description,omitempty"`
	}
	type matchOut struct {
		Item         string      `json:"item"`
		ItemDuration int         `json:"item_duration_s,omitempty" jsonschema:"the book's length in seconds"`
		Provider     string      `json:"provider"`
		Candidates   []candidate `json:"candidates"                jsonschema:"suggestions only; nothing changes until item_match_apply"`
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

		if err := prov.checkNamed(ctx, client, in.Provider); err != nil {
			return nil, matchOut{}, err
		}
		provider, title, author := prov.matchQuery(ctx, client, it, in.Provider, in.Title, in.Author)
		results, err := client.SearchBooks(ctx, provider, title, author, it.ID)
		if err != nil {
			return nil, matchOut{}, err
		}

		out := matchOut{Item: it.Title(), ItemDuration: wholeSec(it.Media.Duration), Provider: provider, Candidates: []candidate{}}
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
				Duration:    wholeSec(res.Duration * 60),
				ASIN:        res.ASIN,
				ISBN:        res.ISBN,
				Language:    res.Language,
				Abridged:    res.Abridged,
				HasCover:    res.Cover != "",
				Description: clip(plain(res.Description), 200),
			}
			for _, s := range res.Series {
				if s.Sequence != "" {
					c.Series = append(c.Series, s.Title()+" #"+s.Sequence)
				} else {
					c.Series = append(c.Series, s.Title())
				}
			}
			out.Candidates = append(out.Candidates, c)
		}

		return nil, out, nil
	})

	type applyIn struct {
		itemRef
		Provider        string   `json:"provider,omitempty"         jsonschema:"must match the provider used in item_match"`
		Candidate       *int     `json:"candidate,omitempty"        jsonschema:"index from item_match; the candidate's asin/isbn is used to match exactly"`
		ASIN            string   `json:"asin,omitempty"             jsonschema:"match this asin directly instead of a candidate"`
		ISBN            string   `json:"isbn,omitempty"             jsonschema:"match this isbn directly instead of a candidate"`
		Title           string   `json:"title,omitempty"            jsonschema:"must match the title override used in item_match"`
		Author          string   `json:"author,omitempty"           jsonschema:"must match the author override used in item_match"`
		OverrideDetails bool     `json:"override_details,omitempty" jsonschema:"replace existing metadata fields instead of only filling empty ones"`
		OverrideCover   bool     `json:"override_cover,omitempty"   jsonschema:"replace the existing cover"`
		Keep            []string `json:"keep,omitempty"             jsonschema:"with override_details: fields to put back as they were after the match: title, subtitle, authors, narrators, series, genres, tags, publisher, year, language, description"`
		Smart           bool     `json:"smart,omitempty"            jsonschema:"fill the empty fields, then decide each remaining difference by rule: a file-tag title, a company in the narrator field or a timestamp year is written; a curated series, a plain year or an honorific-only difference is kept; anything else is reported for review with both values. Cannot combine with override_details"`
		Preview         bool     `json:"preview,omitempty"          jsonschema:"with smart: report the decisions and change nothing"`
	}
	type appliedRef struct {
		Title  string `json:"title,omitempty"`
		Author string `json:"author,omitempty"`
		ASIN   string `json:"asin,omitempty"`
		ISBN   string `json:"isbn,omitempty"`
	}
	type applyOut struct {
		Updated bool            `json:"updated"`
		Kept    []string        `json:"kept,omitempty"    jsonschema:"fields restored after the match"`
		Fields  []fieldDecision `json:"fields,omitempty"  jsonschema:"with smart: every field the provider would write differently and what was done about it"`
		Counts  map[string]int  `json:"counts,omitempty"  jsonschema:"with smart: decisions by action"`
		Warning string          `json:"warning,omitempty"`
		Applied *appliedRef     `json:"applied,omitempty" jsonschema:"the candidate that was applied; check it against item"`
		Item    *itemSummary    `json:"item,omitempty"    jsonschema:"the item after matching"`
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

		it, err := resolveItemToChange(ctx, client, in.Library, in.Item)
		if err != nil {
			return nil, applyOut{}, err
		}
		if it.IsPodcast() {
			return nil, applyOut{}, errNotBook
		}
		// held from the read the kept fields and the provider tag are
		// restored from, to the last write
		defer r.locks.hold(itemKeys(it.ID)...)()
		if it, err = client.Item(ctx, it.ID); err != nil {
			return nil, applyOut{}, err
		}

		keep, err := parseKeep(in.Keep)
		if err != nil {
			return nil, applyOut{}, err
		}
		if len(keep) > 0 && !in.OverrideDetails {
			return nil, applyOut{}, errors.New("keep only means something with override_details: without it nothing already set is replaced")
		}
		if in.Smart && in.OverrideDetails {
			return nil, applyOut{}, errors.New("smart and override_details are two answers to the same question; pick one")
		}
		if in.Preview && !in.Smart {
			return nil, applyOut{}, errors.New("preview only means something with smart")
		}

		if err := prov.checkNamed(ctx, client, in.Provider); err != nil {
			return nil, applyOut{}, err
		}
		provider, title, author := prov.matchQuery(ctx, client, it, in.Provider, in.Title, in.Author)
		opts := abs.MatchOptions{Provider: provider, ASIN: in.ASIN, ISBN: in.ISBN, OverrideCover: in.OverrideCover, OverrideDetails: in.OverrideDetails}
		var applied appliedRef

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
			applied.Title, applied.Author = c.Title, c.Author
			if opts.ASIN == "" && opts.ISBN == "" {
				// providers without ids: the server searches again by the
				// candidate's exact title and author and takes its first
				// hit, which is the one place a match is not pinned to an id
				opts.Title, opts.Author = c.Title, c.Author
			}
		}
		applied.ASIN, applied.ISBN = opts.ASIN, opts.ISBN

		if in.Smart {
			if opts.ASIN == "" && opts.ISBN == "" {
				return nil, applyOut{}, errors.New("smart needs an asin or isbn to fetch the provider's record")
			}
			decisions, res, serr := smartApply(ctx, client, it, provider, opts.ASIN, opts.ISBN, in.Preview)
			if serr != nil {
				return nil, applyOut{}, serr
			}
			out := applyOut{Applied: &applied, Fields: decisions, Counts: smartCounts(decisions)}
			if in.Preview {
				return nil, out, nil
			}
			out.Updated, out.Warning = res.Updated, res.Warning
			// a match that found nothing has no store to record
			if res.LibraryItem != nil {
				if err := prov.tagProvider(ctx, client, it.ID, tagsAfter(it, res, nil), provider); err != nil {
					out.Warning = joinWarnings(out.Warning, "recording the provider tag failed: "+err.Error())
				}
			}
			if after, aerr := client.Item(ctx, it.ID); aerr == nil {
				s := summarize(after)
				out.Item = &s
			}
			return nil, out, nil
		}

		res, err := client.Match(ctx, it.ID, opts)
		if err != nil {
			return nil, applyOut{}, err
		}
		out := applyOut{Updated: res.Updated, Warning: res.Warning, Applied: &applied}
		// a match that found nothing changed nothing: there is no field to
		// put back, no store to record, and no id the item failed to take
		if res.LibraryItem == nil {
			s := summarize(it)
			out.Item = &s
			return nil, out, nil
		}
		tags := tagsAfter(it, res, keep)
		if err := restoreKept(ctx, client, it, keep); err != nil {
			return nil, applyOut{}, fmt.Errorf("matched, but restoring %s failed: %w", strings.Join(keep, ", "), err)
		}
		out.Kept = keep
		if err := prov.tagProvider(ctx, client, it.ID, tags, provider); err != nil {
			out.Warning = joinWarnings(out.Warning, "recording the provider tag failed: "+err.Error())
		}
		// the match result predates the restored fields and the provider
		// tag; the item as it is now is one more read
		if after, aerr := client.Item(ctx, it.ID); aerr == nil {
			res.LibraryItem = after
		}
		s := summarize(res.LibraryItem)
		out.Item = &s
		// say when the id asked for is not the one the item ended up with:
		// without override_details a field already set is kept
		got := res.LibraryItem.Media.Metadata
		switch {
		case applied.ASIN != "" && !strings.EqualFold(got.ASIN, applied.ASIN):
			out.Warning = joinWarnings(out.Warning, fmt.Sprintf("the item's asin is %q, not the applied %s; set override_details to replace it", got.ASIN, applied.ASIN))
		case applied.ISBN != "" && got.ISBN != applied.ISBN:
			out.Warning = joinWarnings(out.Warning, fmt.Sprintf("the item's isbn is %q, not the applied %s; set override_details to replace it", got.ISBN, applied.ISBN))
		}

		return nil, out, nil
	})

	type batchEditIn struct {
		Library string   `json:"library,omitempty"       jsonschema:"library name or id, for resolving titles"`
		Items   []string `json:"items"                   jsonschema:"the books to change, by id or exact title"`
		Genres  []string `json:"genres,omitempty"        jsonschema:"replacement genre list, applied to every item"`
		Tags    []string `json:"tags,omitempty"          jsonschema:"replacement tag list, applied to every item"`
		AddTags []string `json:"add_tags,omitempty"      jsonschema:"tags to add to each item's own, keeping the rest; the provider tag set to none (zz-provider:none by default) marks a book as checked with nothing to match"`
		DropTag []string `json:"remove_tags,omitempty"   jsonschema:"tags to remove from each item, keeping the rest"`
		AddSer  []string `json:"add_series,omitempty"    jsonschema:"series to add to each item's own as 'Name' or 'Name #2', keeping the rest: 'Cosmere' on every Sanderson book"`
		DropSer []string `json:"remove_series,omitempty" jsonschema:"series to take each item out of, by name, keeping the rest"`
		Authors []string `json:"authors,omitempty"       jsonschema:"replacement author list"`
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
		Description: "Apply the same metadata to many books in one call - a genre on forty titles, a publisher on a series. Every field given replaces that field on every item listed, except add_tags, remove_tags, add_series and remove_series, which edit each item's own list; fields left out are untouched. Use item_edit for one item, or for fields that differ per item. Changes server state.",
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
		editSeries := len(in.AddSer) > 0 || len(in.DropSer) > 0
		if len(in.Tags) == 0 && len(in.AddTags) == 0 && len(in.DropTag) == 0 && !hasMeta && !editSeries {
			return nil, batchEditOut{}, errors.New("nothing to change: pass genres, tags, add_tags, remove_tags, add_series, remove_series, authors, year, publisher or language")
		}
		if len(in.Tags) > 0 && (len(in.AddTags) > 0 || len(in.DropTag) > 0) {
			return nil, batchEditOut{}, errors.New("tags replaces the list; add_tags and remove_tags edit it. One or the other")
		}

		items := make([]*abs.Item, 0, len(in.Items))
		ids := make([]string, 0, len(in.Items))
		for _, ref := range in.Items {
			it, err := resolveItemToChange(ctx, client, in.Library, ref)
			if err != nil {
				return nil, batchEditOut{}, err
			}
			if it.IsPodcast() {
				return nil, batchEditOut{}, fmt.Errorf("%s is a podcast; item_batch_edit is for books", it.Title())
			}
			items = append(items, it)
			ids = append(ids, it.ID)
		}
		defer r.locks.hold(itemKeys(ids...)...)()
		// the lists are edited from the items as they are once held
		if len(in.AddTags) > 0 || len(in.DropTag) > 0 || editSeries {
			fresh, err := client.ItemsBatch(ctx, ids)
			if err != nil {
				return nil, batchEditOut{}, err
			}
			for i := range items {
				for j := range fresh {
					if fresh[j].ID == items[i].ID {
						items[i] = &fresh[j]
					}
				}
			}
		}

		out := batchEditOut{Items: make([]string, 0, len(in.Items))}
		updates := make([]abs.BatchMediaUpdate, 0, len(in.Items))
		for _, it := range items {
			upd := abs.MediaUpdate{}
			if len(in.Tags) > 0 {
				upd.Tags = in.Tags // an explicit empty list would clear them
			}
			if len(in.AddTags) > 0 || len(in.DropTag) > 0 {
				upd.Tags = editTagList(it.Media.Tags, in.AddTags, in.DropTag)
			}
			if hasMeta || editSeries {
				upd.Metadata = new(md)
			}
			if editSeries {
				refs, err := editSeriesList(it.Media.Metadata.Series, in.AddSer, in.DropSer)
				if err != nil {
					return nil, batchEditOut{}, err
				}
				upd.Metadata.Series = refs
			}
			updates = append(updates, abs.BatchMediaUpdate{ID: it.ID, MediaPayload: upd})
			out.Items = append(out.Items, it.Title())
		}

		// in pages: one request carrying hundreds of items is what a reverse
		// proxy times out on, and a timeout after part of it landed would
		// read as nothing done
		for start := 0; start < len(updates); start += sweepBatchSize {
			end := min(start+sweepBatchSize, len(updates))
			n, err := client.BatchUpdate(ctx, updates[start:end])
			if err != nil && len(updates) <= sweepBatchSize {
				return nil, batchEditOut{}, err
			}
			if err != nil {
				return nil, batchEditOut{}, fmt.Errorf("the batch of items %d to %d of %d failed and may have landed in part; %d items before it were updated, and the %d after it were not sent: %w",
					start+1, end, len(updates), out.Updated, len(updates)-end, err)
			}
			out.Updated += n
		}

		return nil, out, nil
	})

	type rescanOut struct {
		Result string `json:"result" jsonschema:"NOTHING, ADDED, UPDATED, REMOVED or UPTODATE"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "item_rescan",
		Description: "Rescan one item's folder for changed files (new tracks, replaced cover, edited metadata.json). Admin only. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in itemRef) (*mcp.CallToolResult, rescanOut, error) {
		it, err := resolveItemToChange(ctx, client, in.Library, in.Item)
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
		if err := prov.checkNamed(ctx, client, in.Provider); err != nil {
			return nil, coverSearchOut{}, err
		}
		provider, title, author := prov.matchQuery(ctx, client, it, in.Provider, in.Title, in.Author)
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

	type coverEditOut struct {
		Item  string `json:"item"`
		Cover string `json:"cover" jsonschema:"the cover file the server now has, read back after the change; empty once removed"`
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
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in coverEditIn) (*mcp.CallToolResult, coverEditOut, error) {
		asked := 0
		for _, given := range []bool{in.URL != "", in.File != "", in.Remove} {
			if given {
				asked++
			}
		}
		switch {
		case asked == 0:
			// removal has to be asked for: a call that forgot its url must
			// not take the cover away
			return nil, coverEditOut{}, errors.New("pass url or file to set the cover, or remove to delete it")
		case asked > 1:
			// only the first would be done: a remove that sets a cover instead
			return nil, coverEditOut{}, errors.New("url, file and remove each say what to do with the cover; pass one")
		}
		it, err := resolveItemToChange(ctx, client, in.Library, in.Item)
		if err != nil {
			return nil, coverEditOut{}, err
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
			return nil, coverEditOut{}, err
		}
		// the cover the server now has, not the request's word for it: a url
		// it could not fetch answers as a set that left nothing behind
		after, err := client.Item(ctx, it.ID)
		switch {
		case err != nil:
			return nil, coverEditOut{}, fmt.Errorf("the cover was sent, but reading %q back failed: %w", it.Title(), err)
		case in.Remove && after.HasCover():
			return nil, coverEditOut{}, fmt.Errorf("the server accepted the removal but %q still has a cover (%s)", it.Title(), after.Media.CoverPath)
		case !in.Remove && !after.HasCover():
			return nil, coverEditOut{}, fmt.Errorf("the server accepted the cover but %q has none afterwards", it.Title())
		}

		return nil, coverEditOut{Item: after.Title(), Cover: after.Media.CoverPath}, nil
	})

	type chapterIn struct {
		Title string  `json:"title"`
		Start float64 `json:"start_s" jsonschema:"start time in seconds"`
	}
	type chaptersSetIn struct {
		itemRef
		Chapters []chapterIn `json:"chapters,omitempty"  jsonschema:"replacement chapter list in order, each starting after the one before and inside the audio; each ends where the next starts"`
		FromASIN string      `json:"from_asin,omitempty" jsonschema:"instead of a list: fetch Audible's chapters for this asin (default the item's asin)"`
		Region   string      `json:"region,omitempty"    jsonschema:"Audible region for from_asin: us, uk, ca, au, de, fr, it, es, jp, in; default the store the book's provider tag records (audible.ca is ca), else us"`
		Fit      bool        `json:"fit,omitempty"       jsonschema:"instead of a list or an asin: keep the book's own chapters, drop those that start at or past the end of the audio, and end the last at the end of the audio. The fix audit_chapters gives for past_end and short, after a file is taken out of a book or a track added"`
	}
	type chaptersSetOut struct {
		Updated  bool    `json:"updated"`
		Chapters int     `json:"chapters"`
		Region   string  `json:"region,omitempty"  jsonschema:"the Audible region the chapters came from"`
		Dropped  int     `json:"dropped,omitempty" jsonschema:"fit only: chapters dropped for starting at or past the end of the audio"`
		EndS     float64 `json:"end_s,omitempty"   jsonschema:"fit only: where the last chapter now ends, the end of the audio, in seconds"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "item_chapters_set",
		Description: "Replace a book's chapters, either with an explicit list or with the chapters Audible has for its asin, or fit the ones it has to its audio (fit: drop those starting past the end, end the last at the end). The last chapter of a list runs to the end of the audio. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in chaptersSetIn) (*mcp.CallToolResult, chaptersSetOut, error) {
		// a list given alongside an asin would be set and the asin ignored
		if len(in.Chapters) > 0 && strings.TrimSpace(in.FromASIN) != "" {
			return nil, chaptersSetOut{}, errors.New("chapters is the list to set and from_asin fetches one; pass one or the other")
		}
		// and fit would throw away either
		if in.Fit && (len(in.Chapters) > 0 || strings.TrimSpace(in.FromASIN) != "") {
			return nil, chaptersSetOut{}, errors.New("fit keeps the book's own chapters, and chapters or from_asin replaces them; pass one of the three")
		}
		it, err := resolveItemToChange(ctx, client, in.Library, in.Item)
		if err != nil {
			return nil, chaptersSetOut{}, err
		}
		if it.IsPodcast() {
			return nil, chaptersSetOut{}, errNotBook
		}

		var chapters []abs.Chapter
		out := chaptersSetOut{}
		switch {
		case in.Fit:
			if chapters, out.Dropped, err = fitChapters(it); err != nil {
				return nil, chaptersSetOut{}, err
			}
			out.EndS = it.Media.Duration
		case len(in.Chapters) > 0:
			// the server takes any list and the player then seeks by it: a
			// chapter out of order, or past the end, is one nobody can reach
			for i, ch := range in.Chapters {
				switch {
				case ch.Start < 0:
					return nil, chaptersSetOut{}, fmt.Errorf("chapter %d (%q) starts at %gs, before the audio does", i+1, ch.Title, ch.Start)
				case i > 0 && ch.Start <= in.Chapters[i-1].Start:
					return nil, chaptersSetOut{}, fmt.Errorf("chapter %d (%q) starts at %gs, not after chapter %d at %gs: list them in order", i+1, ch.Title, ch.Start, i, in.Chapters[i-1].Start)
				case it.Media.Duration > 0 && ch.Start >= it.Media.Duration:
					return nil, chaptersSetOut{}, fmt.Errorf("chapter %d (%q) starts at %gs, at or past the end of the audio at %gs", i+1, ch.Title, ch.Start, it.Media.Duration)
				}
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
			// the store the book was matched from: an asin from the Canadian
			// store may be sold nowhere else, and the lookup defaults to the US
			region := strings.ToLower(strings.TrimSpace(in.Region))
			if region == "" {
				region = audibleRegion(prov.providerTag(it))
			}
			chapters, err = client.SearchChapters(ctx, asin, region)
			if err != nil {
				return nil, chaptersSetOut{}, err
			}
			if len(chapters) == 0 {
				return nil, chaptersSetOut{}, fmt.Errorf("no chapters found for asin %s", asin)
			}
			// the store's list is for the recording the asin names, which is
			// not always the one on disk: a chapter starting past the end of
			// this book's audio is another edition's, and no player can reach it
			if last := chapters[len(chapters)-1]; it.Media.Duration > 0 && last.Start >= it.Media.Duration {
				return nil, chaptersSetOut{}, fmt.Errorf("asin %s's %d chapters run to %s, but this book's audio ends at %s: they are another recording's (another edition or narrator); check the asin, or pass the chapters",
					asin, len(chapters), fmtDuration(last.Start), fmtDuration(it.Media.Duration))
			}
			// the store's last chapter ends where its recording does, a few
			// seconds either side of this file's: the last runs to the end
			// of this audio, as a list given here does
			if it.Media.Duration > 0 {
				chapters[len(chapters)-1].End = it.Media.Duration
			}
			out.Region = region
			if out.Region == "" {
				out.Region = "us"
			}
		}

		updated, err := client.SetChapters(ctx, it.ID, chapters)
		if err != nil {
			return nil, chaptersSetOut{}, err
		}
		out.Updated, out.Chapters = updated, len(chapters)

		return nil, out, nil
	})

	type embedIn struct {
		itemRef
		Backup bool `json:"backup,omitempty" jsonschema:"keep a copy of the original audio files"`
	}
	type embedOut struct {
		Item     string `json:"item"`
		Embedded bool   `json:"embedded"          jsonschema:"the files' tags carry the item's metadata, read back after a rescan"`
		Running  bool   `json:"running,omitempty" jsonschema:"the embed was still running or queued when this stopped waiting: item_rescan the item once server_tasks no longer lists it, or audit_unembedded keeps reading the old tags"`
		Rescan   string `json:"rescan,omitempty"  jsonschema:"the rescan result once the embed finished"`
		Differs  string `json:"differs,omitempty" jsonschema:"what the files' tags still disagree on after the embed: it failed, or wrote something else"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "item_embed_metadata",
		Description: "Write the item's metadata and chapters into its audio files' tags so they travel with the files, then rescan the item and check the tags: embedded says they now match. audit_unembedded lists the books where this is due. Waits for the embed, up to two minutes; a longer one comes back running. Admin only. Changes the files on disk.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in embedIn) (*mcp.CallToolResult, embedOut, error) {
		it, err := resolveItemToChange(ctx, client, in.Library, in.Item)
		if err != nil {
			return nil, embedOut{}, err
		}
		if it.IsPodcast() {
			return nil, embedOut{}, errNotBook
		}
		if err := client.EmbedMetadata(ctx, it.ID, true, in.Backup); err != nil {
			return nil, embedOut{}, err
		}
		out := embedOut{Item: it.Title()}

		// the server tags the files in the background and never reads them
		// back: what it holds of their tags, which is what audit_unembedded
		// judges, stays as it was until the item is scanned again
		deadline := time.Now().Add(embedWait)
		for {
			pending, perr := client.EmbedPending(ctx, it.ID)
			if perr != nil {
				return nil, embedOut{}, perr
			}
			if !pending {
				break
			}
			if time.Now().After(deadline) {
				out.Running = true
				return nil, out, nil
			}
			select {
			case <-ctx.Done():
				return nil, embedOut{}, ctx.Err()
			case <-time.After(embedPoll):
			}
		}
		if out.Rescan, err = client.ScanItem(ctx, it.ID); err != nil {
			return nil, embedOut{}, fmt.Errorf("embedded, but the rescan that reads the tags back failed: %w", err)
		}
		after, err := client.Item(ctx, it.ID)
		if err != nil {
			return nil, embedOut{}, err
		}
		detail, stale := checkEmbedded(after)
		out.Embedded, out.Differs = !stale, detail

		return nil, out, nil
	})

	type deleteIn struct {
		itemRef
		DeleteFiles bool `json:"delete_files,omitempty" jsonschema:"also delete the item's folder from disk (irreversible); default keeps the files"`
		Confirm     bool `json:"confirm,omitempty"      jsonschema:"true to delete; without it nothing changes and the answer says what a confirmed call would remove"`
	}
	type deleteOut struct {
		Deleted          string   `json:"deleted,omitempty"           jsonschema:"the item deleted"`
		WouldDelete      string   `json:"would_delete,omitempty"      jsonschema:"without confirm: the item a confirmed call deletes; nothing has changed"`
		Files            string   `json:"files,omitempty"             jsonschema:"with delete_files: what is erased from disk, the item's folder and everything in it, or the one file of a book that is a single file at the library root"`
		FilesRemoved     bool     `json:"files_removed"`
		Bookmarks        []string `json:"bookmarks,omitempty"         jsonschema:"the API key user's bookmarks on it, which go before the item does"`
		BookmarksRemoved int      `json:"bookmarks_removed,omitempty" jsonschema:"the API key user's bookmarks on it, removed before the delete"`
	}
	add(r, deleteTool, &mcp.Tool{
		Name: "item_delete",
		Description: "Remove an item from the library, and with delete_files also erase its folder from disk. Listening progress for it is lost. The API key user's bookmarks on it are removed first: Audiobookshelf keeps a bookmark on a deleted item and will not remove it afterwards, though other accounts' bookmarks stay. " +
			"Without confirm=true nothing changes and the answer says what would go: the record, the folder or file on disk with delete_files, and the bookmarks. Requires the delete permission.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in deleteIn) (*mcp.CallToolResult, deleteOut, error) {
		it, err := resolveItemToChange(ctx, client, in.Library, in.Item)
		if err != nil {
			return nil, deleteOut{}, err
		}
		// the account's bookmarks are one list, which user_bookmark_edit
		// reads and saves whole: held from reading them to the delete
		defer r.locks.hold(append(itemKeys(it.ID), "bookmarks")...)()
		me, err := client.Me(ctx)
		if err != nil {
			return nil, deleteOut{}, err
		}
		// asked before the bookmarks go, so a refused delete costs nothing
		if !me.IsAdmin() && !me.Permissions.Delete {
			return nil, deleteOut{}, fmt.Errorf("%s may not delete items: the account lacks the delete permission", me.Username)
		}
		out := deleteOut{}
		if in.DeleteFiles {
			out.Files = "the folder " + it.Path + " and everything in it"
			if it.IsFile {
				out.Files = "the file " + it.Path
			}
		}
		var marks []abs.Bookmark
		for _, b := range me.Bookmarks {
			if b.LibraryItemID == it.ID {
				marks = append(marks, b)
				out.Bookmarks = append(out.Bookmarks, fmt.Sprintf("%q at %s", b.Title, cmp.Or(fmtDuration(b.Time), "0s")))
			}
		}
		if !in.Confirm {
			out.WouldDelete = it.Title()
			return nil, out, nil
		}

		for _, b := range marks {
			if err := client.DeleteBookmark(ctx, it.ID, b.Time); err != nil {
				if out.BookmarksRemoved == 0 {
					return nil, deleteOut{}, fmt.Errorf("removing the bookmark %q before deleting %q failed, and nothing was deleted: %w", b.Title, it.Title(), err)
				}
				return nil, deleteOut{}, fmt.Errorf("removing the bookmark %q before deleting %q failed once %d of its %d bookmarks were removed; the item was not deleted: %w", b.Title, it.Title(), out.BookmarksRemoved, len(marks), err)
			}
			out.BookmarksRemoved++
		}
		if err := client.DeleteItem(ctx, it.ID, in.DeleteFiles); err != nil {
			if out.BookmarksRemoved > 0 {
				return nil, deleteOut{}, fmt.Errorf("%d of its bookmarks were removed, but deleting %q then failed: %w", out.BookmarksRemoved, it.Title(), err)
			}
			return nil, deleteOut{}, err
		}
		out.Deleted, out.FilesRemoved = it.Title(), in.DeleteFiles

		return nil, out, nil
	})
}

// embedWait is how long item_embed_metadata waits for its embed before
// handing back a running one, and embedPoll how often it looks.
var (
	embedWait = 2 * time.Minute
	embedPoll = 500 * time.Millisecond
)

// checkNamed refuses a provider the caller named that the server does not
// have. The server answers a name it does not know by searching Google, so a
// misspelt audible.ca comes back as Google's guesses with nothing to say so.
func (p providerConfig) checkNamed(ctx context.Context, client *abs.Client, provider string) error {
	if provider == "" {
		return nil
	}

	return p.checkProviders(ctx, client, []string{provider}, false)
}

// matchQuery fills in the provider, title and author for a provider search
// from the item, the configured default and its library when not overridden.
func (p providerConfig) matchQuery(ctx context.Context, client *abs.Client, it *abs.Item, provider, title, author string) (resolvedProvider, resolvedTitle, resolvedAuthor string) {
	if title == "" {
		title = it.Title()
	}
	if author == "" {
		author = it.Media.Metadata.AuthorDisplay()
	}
	if provider == "" && len(p.providers) > 0 {
		provider = p.providers[0]
	}
	if provider == "" {
		if lib, err := client.Library(ctx, it.LibraryID); err == nil {
			provider = lib.Provider
		}
	}
	return provider, title, author
}

// joinWarnings adds a warning to whatever the server already said.
func joinWarnings(have, add string) string {
	if have == "" {
		return add
	}
	return have + "; " + add
}

// parseSeriesRef reads "Name #2" or "Name" as a series entry.
func parseSeriesRef(s string) abs.SeriesRef {
	name, seq, _ := strings.Cut(s, " #")
	return abs.SeriesRef{Name: strings.TrimSpace(name), Sequence: strings.TrimSpace(seq)}
}

// editSeriesList is a book's series list with some added and some removed,
// the rest untouched. The whole list is what the server takes, so an edit
// that only meant to add one series has to send the others back: four
// Stormlight books lost their Cosmere link to a replacement built from a
// listing that showed one series per book. An added series the book is
// already in takes the number given, or keeps its own when none is.
// editTagList adds and removes tags on a list, keeping the rest: a tag already
// there, in any case, is not added twice. The result is never nil, so an edit
// that removes the last tag reaches the server as an empty list.
func editTagList(have, add, remove []string) []string {
	tags := slices.Clone(have)
	for _, t := range add {
		if t = strings.TrimSpace(t); t != "" && !slices.ContainsFunc(tags, func(x string) bool { return strings.EqualFold(x, t) }) {
			tags = append(tags, t)
		}
	}
	tags = slices.DeleteFunc(tags, func(x string) bool {
		return slices.ContainsFunc(remove, func(d string) bool { return strings.EqualFold(strings.TrimSpace(d), x) })
	})
	if tags == nil {
		tags = []string{}
	}
	return tags
}

func editSeriesList(have []abs.SeriesRef, add, remove []string) ([]abs.SeriesRef, error) {
	out := slices.Clone(have)
	if out == nil {
		out = []abs.SeriesRef{}
	}
	same := func(a, b string) bool { return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b)) }
	for _, s := range add {
		ref := parseSeriesRef(s)
		if ref.Name == "" {
			return nil, fmt.Errorf("add_series %q names no series", s)
		}
		if i := slices.IndexFunc(out, func(r abs.SeriesRef) bool { return same(r.Name, ref.Name) }); i >= 0 {
			if ref.Sequence != "" {
				out[i].Sequence = ref.Sequence
			}
			continue
		}
		out = append(out, ref)
	}
	for _, s := range remove {
		name := parseSeriesRef(s).Name
		if name == "" {
			return nil, fmt.Errorf("remove_series %q names no series", s)
		}
		if !slices.ContainsFunc(out, func(r abs.SeriesRef) bool { return same(r.Name, name) }) {
			return nil, fmt.Errorf("remove_series: the item is not in %q (it is in %s)", name, seriesNamesOf(have))
		}
		out = slices.DeleteFunc(out, func(r abs.SeriesRef) bool { return same(r.Name, name) })
	}
	return out, nil
}

func seriesNamesOf(refs []abs.SeriesRef) string {
	if len(refs) == 0 {
		return "no series"
	}
	names := make([]string, 0, len(refs))
	for _, r := range refs {
		names = append(names, strconv.Quote(r.Name))
	}
	return strings.Join(names, ", ")
}
