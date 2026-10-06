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

	"github.com/katbyte/abs-mcp/sdk/abs"
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
		Path     string `json:"path,omitempty"         jsonschema:"its path in the item's folder, when it is in a folder of its own (a disc folder); item_edit tracks takes it where two files share a name"`
		Duration int    `json:"duration_s,omitempty"   jsonschema:"length in seconds"`
		Size     int64  `json:"size"                   jsonschema:"bytes"`
		Codec    string `json:"codec,omitempty"`
		Bitrate  int64  `json:"bitrate_kbps,omitempty"`
		Channels int    `json:"channels,omitempty"`
		Excluded bool   `json:"excluded,omitempty"     jsonschema:"not part of the playable tracks"`
		Error    string `json:"error,omitempty"`
		Type     string `json:"type,omitempty"         jsonschema:"for non-audio files: ebook, image, text, metadata"`
		Main     bool   `json:"main,omitempty"         jsonschema:"the ebook readers open; the book's other ebooks are supplementary (item_edit ebook changes it)"`
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
		Downloads     *downloadSettings `json:"downloads,omitempty"      jsonschema:"podcasts: the automatic download settings podcast_edit changes"`
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
			out.OtherFiles = append(out.OtherFiles, fileRow{
				Filename: f.Metadata.Filename, Path: subfolderPath(f.Metadata), Size: f.Metadata.Size, Type: f.FileType,
				Main: f.FileType == "ebook" && it.Media.EbookFile != nil && it.Media.EbookFile.Ino == f.Ino,
			})
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
					Path:     subfolderPath(af.Metadata),
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
		Item          string   `json:"item,omitempty"           jsonschema:"library item id, or its whole title; or items, for the same change on many"`
		Library       string   `json:"library,omitempty"        jsonschema:"narrow a title lookup to one library by name or id"`
		Items         []string `json:"items,omitempty"          jsonschema:"instead of item: the items to make the same change on, by id or whole title - a genre on forty titles, a publisher on a series. Every field given is set on each of them; add_tags, remove_tags, add_series and remove_series edit each item's own list. title, subtitle, description, isbn, asin, podcast_author, feed_url, tracks and ebook are one item's own, and take item"`
		Title         string   `json:"title,omitempty"`
		Subtitle      string   `json:"subtitle,omitempty"`
		Authors       []string `json:"authors,omitempty"        jsonschema:"replacement author list (books); new names are created"`
		Narrators     []string `json:"narrators,omitempty"      jsonschema:"replacement narrator list (books)"`
		Series        []string `json:"series,omitempty"         jsonschema:"replacement series list as 'Name' or 'Name #2' (books); every series not listed is dropped, so use add_series to link one more"`
		AddSeries     []string `json:"add_series,omitempty"     jsonschema:"series to add to the item's own as 'Name' or 'Name #2', keeping the rest; a series it is already in takes the number given"`
		RemoveSeries  []string `json:"remove_series,omitempty"  jsonschema:"series to take the item out of, by name, keeping the rest"`
		Genres        []string `json:"genres,omitempty"         jsonschema:"replacement genre list"`
		Tags          []string `json:"tags,omitempty"           jsonschema:"replacement tag list"`
		AddTags       []string `json:"add_tags,omitempty"       jsonschema:"tags to add to the item's own, keeping the rest; the provider tag set to none (zz-provider:none by default) marks a book as checked with nothing to match"`
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
		Tracks        []string `json:"tracks,omitempty"         jsonschema:"books: the play order, as every audio file of the book by the filename item_get files=true lists (by its path in the folder where two share a name). The list must be whole: the server drops a file it is not sent from the book. Excluded files stay excluded. Chapters that each sit inside one track move with it. The order holds until the book's audio files change on disk, when the scan that finds it sorts them by track number again"`
		Ebook         string   `json:"ebook,omitempty"          jsonschema:"books: the ebook file readers open, by filename; the book's other ebooks become supplementary. none leaves it with no main ebook"`
	}
	type editOut struct {
		Updated      bool     `json:"updated"`
		Fields       []string `json:"fields_sent"`
		ItemsUpdated *int     `json:"items_updated,omitempty" jsonschema:"with items: how many the server changed; one already as asked is not counted"`
		Items        []string `json:"items,omitempty"         jsonschema:"with items: the titles that were sent"`
		Tracks       []string `json:"tracks,omitempty"        jsonschema:"with tracks: the play order now, read back from the server"`
		Chapters     string   `json:"chapters,omitempty"      jsonschema:"with tracks: moved (each chapter went with its track), unchanged (the order was already so, or the book has none), or left (a chapter runs across two tracks, so they could not follow the files; audit_chapters and item_chapters_set)"`
		Ebook        string   `json:"ebook,omitempty"         jsonschema:"with ebook: the main ebook now, read back from the server, or none"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "item_edit",
		Description: "Edit an item's metadata: title, authors, narrators, series, genres, tags, year, publisher, description, isbn, asin, language, explicit/abridged flags; and a book's track order and main ebook. Or with items, make the same change on many in one call: a genre on forty titles, a publisher on a series. Only provided fields change; list a field in clear to blank it. series and tags replace the list, add_series, remove_series, add_tags and remove_tags edit it. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in editIn) (*mcp.CallToolResult, editOut, error) {
		many := len(in.Items) > 0
		if many {
			if strings.TrimSpace(in.Item) != "" {
				return nil, editOut{}, errors.New("item is one item and items is many; pass one or the other")
			}
			own := []struct {
				name  string
				given bool
			}{
				{"title", in.Title != ""},
				{"subtitle", in.Subtitle != ""},
				{"description", in.Description != ""},
				{"isbn", in.ISBN != ""},
				{"asin", in.ASIN != ""},
				{"podcast_author", in.PodcastAuthor != ""},
				{"feed_url", in.FeedURL != ""},
				{"tracks", len(in.Tracks) > 0},
				{"ebook", in.Ebook != ""},
			}
			for _, f := range own {
				if f.given {
					return nil, editOut{}, fmt.Errorf("%s is one item's own: set it with item, not items", f.name)
				}
			}
		}
		refs := in.Items
		if !many {
			refs = []string{in.Item}
		}
		var (
			items []*abs.Item
			ids   []string
		)
		for _, ref := range refs {
			found, err := resolveItemToChange(ctx, client, in.Library, ref)
			if err != nil {
				return nil, editOut{}, err
			}
			// named twice, it is changed once
			if !slices.Contains(ids, found.ID) {
				items, ids = append(items, found), append(ids, found.ID)
			}
		}
		it := items[0] // the item, when there is one
		if (len(in.Tracks) > 0 || in.Ebook != "") && it.IsPodcast() {
			return nil, editOut{}, errors.New("tracks and ebook are a book's; this item is a podcast")
		}
		defer r.locks.hold(itemKeys(ids...)...)()
		// the lists are edited from the items as they are once held, not as
		// the lookup found them: a call running alongside may have changed them
		editSeries := len(in.AddSeries) > 0 || len(in.RemoveSeries) > 0
		editTags := len(in.AddTags) > 0 || len(in.RemoveTags) > 0
		var err error
		switch {
		case many && (editSeries || editTags):
			var fresh []abs.Item
			if fresh, err = client.ItemsBatch(ctx, ids); err != nil {
				return nil, editOut{}, err
			}
			for i := range items {
				j := slices.IndexFunc(fresh, func(f abs.Item) bool { return f.ID == items[i].ID })
				if j < 0 {
					// the batch leaves out what is gone: its tags and series
					// from before the hold are not the ones to edit
					return nil, editOut{}, fmt.Errorf("%q (%s) was not in the server's reply when read again to edit: deleted meanwhile? nothing was changed", items[i].Title(), items[i].ID)
				}
				items[i] = &fresh[j]
			}
		case !many && (editSeries || editTags || len(in.Tracks) > 0 || in.Ebook != ""):
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
		if editSeries {
			if len(in.Series) > 0 || slices.ContainsFunc(in.Clear, func(c string) bool { return strings.EqualFold(c, "series") }) {
				return nil, editOut{}, errors.New("series replaces the list; add_series and remove_series edit it. One or the other")
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
		if editTags {
			if len(in.Tags) > 0 || slices.ContainsFunc(in.Clear, func(c string) bool { return strings.EqualFold(c, "tags") }) {
				return nil, editOut{}, errors.New("tags replaces the list; add_tags and remove_tags edit it. One or the other")
			}
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
		metadata := len(fields) > 0

		// one item's update: the change given, with the lists that are edited
		// worked out from the item's own
		tagsOnly := !slices.ContainsFunc(fields, func(f string) bool { return f != "tags" && f != "clear:tags" })
		updateFor := func(it *abs.Item) (abs.MediaUpdate, error) {
			u, m := upd, md
			if editSeries {
				refs, serr := editSeriesList(it.Media.Metadata.Series, in.AddSeries, in.RemoveSeries)
				if serr != nil {
					return u, serr
				}
				m.Series = refs
			}
			if editTags {
				u.Tags = editTagList(it.Media.Tags, in.AddTags, in.RemoveTags)
			}
			// empty slices must survive JSON encoding to clear server-side
			if !many || !tagsOnly {
				u.Metadata = &m
			}
			return u, nil
		}
		if many {
			if !metadata {
				return nil, editOut{}, errors.New("nothing to change: pass at least one field")
			}
			out := editOut{Fields: fields, Items: make([]string, 0, len(items))}
			updates := make([]abs.BatchMediaUpdate, 0, len(items))
			shows := map[string]bool{}
			for _, it := range items {
				var u abs.MediaUpdate
				if u, err = updateFor(it); err != nil {
					return nil, editOut{}, err
				}
				updates = append(updates, abs.BatchMediaUpdate{ID: it.ID, MediaPayload: u})
				out.Items = append(out.Items, it.Title())
				shows[it.ID] = it.IsPodcast()
			}
			var n int
			if n, err = updateMany(ctx, client, updates, shows); err != nil {
				return nil, editOut{}, err
			}
			out.ItemsUpdated, out.Updated = &n, n > 0
			return nil, out, nil
		}
		one, err := updateFor(it)
		if err != nil {
			return nil, editOut{}, err
		}

		// the files are planned before anything is sent, so a list that names
		// a file wrongly changes nothing at all
		var (
			order          []abs.TrackOrder
			before, played []abs.AudioFile
			chapters       []abs.Chapter
			flip           *abs.LibraryFile
			wantEbook      string
		)
		out := editOut{}
		if len(in.Tracks) > 0 {
			var listed []abs.AudioFile
			if order, listed, err = trackOrder(it, in.Tracks); err != nil {
				return nil, editOut{}, err
			}
			before = playOrder(it.Media.AudioFiles)
			played = slices.DeleteFunc(listed, func(af abs.AudioFile) bool { return af.Exclude })
			switch moved, ok := chaptersMoved(it.Media.Chapters, before, played); {
			case sameTracks(before, played):
				order, out.Chapters = nil, "unchanged"
			case len(it.Media.Chapters) == 0:
				out.Chapters = "unchanged"
			case ok:
				chapters, out.Chapters = moved, "moved"
			default:
				out.Chapters = "left"
			}
			if order != nil {
				fields = append(fields, "tracks")
			}
		}
		if in.Ebook != "" {
			if flip, wantEbook, err = ebookFlip(it, in.Ebook); err != nil {
				return nil, editOut{}, err
			}
			if flip != nil {
				fields = append(fields, "ebook")
			}
		}
		// tracks already in that order, or an ebook already the main one, is
		// an answer and not a mistake
		if len(fields) == 0 && len(in.Tracks) == 0 && in.Ebook == "" {
			return nil, editOut{}, errors.New("nothing to change: pass at least one field")
		}
		out.Fields = fields

		// each change after the first is said to have followed it when it
		// fails, so a partial edit is never reported as none
		var done []string
		failed := func(what string, err error) error {
			if len(done) == 0 {
				return fmt.Errorf("%s: %w", what, err)
			}
			return fmt.Errorf("%s changed, but %s failed: %w", strings.Join(done, " and "), what, err)
		}
		if metadata {
			if out.Updated, err = client.UpdateMedia(ctx, it.ID, one); err != nil {
				return nil, editOut{}, err
			}
			done = append(done, "the metadata")
		}
		if order != nil {
			got, err := client.UpdateTracks(ctx, it.ID, order)
			if err != nil {
				return nil, editOut{}, failed("reordering the tracks", err)
			}
			done = append(done, "the track order")
			out.Updated = true
			if now := playOrder(got.Media.AudioFiles); !sameTracks(now, played) {
				return nil, editOut{}, failed("checking the order", fmt.Errorf("the server was sent %s but plays %s", strings.Join(trackNames(played), ", "), strings.Join(trackNames(now), ", ")))
			}
			if chapters != nil {
				if _, err := client.SetChapters(ctx, it.ID, chapters); err != nil {
					return nil, editOut{}, failed("moving the chapters with their tracks (they still follow the old order: item_chapters_set)", err)
				}
				done = append(done, "the chapters")
			}
			out.Tracks = trackNames(played)
		} else if len(in.Tracks) > 0 {
			out.Tracks = trackNames(before)
		}
		if in.Ebook != "" {
			out.Ebook = wantEbook
			if flip != nil {
				if err := client.SetEbookPrimary(ctx, it.ID, flip.Ino, wantEbook != "none"); err != nil {
					return nil, editOut{}, failed("setting the main ebook", err)
				}
				done = append(done, "the main ebook")
				out.Updated = true
				got, err := client.Item(ctx, it.ID)
				if err != nil {
					return nil, editOut{}, failed("reading the main ebook back", err)
				}
				if out.Ebook = mainEbook(got); out.Ebook != wantEbook {
					return nil, editOut{}, failed("checking the main ebook", fmt.Errorf("it is %s, not %s", out.Ebook, wantEbook))
				}
			}
		}

		return nil, out, nil
	})

	type matchIn struct {
		itemRef
		Providers []string `json:"providers,omitempty" jsonschema:"metadata providers to try in order (server_info lists them), the first with any candidates answering; default the server's --providers, else the library's"`
		Title     string   `json:"title,omitempty"     jsonschema:"override the search title; default the item's"`
		Author    string   `json:"author,omitempty"    jsonschema:"override the search author; default the item's"`
		Limit     int      `json:"limit,omitempty"     jsonschema:"maximum candidates, default 8"`
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
		Provider     string      `json:"provider"                  jsonschema:"the provider the candidates came from; the last one asked when none had any"`
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

		if err := prov.checkNamed(ctx, client, in.Providers); err != nil {
			return nil, matchOut{}, err
		}
		providers, err := prov.itemProviders(ctx, client, it, in.Providers)
		if err != nil {
			return nil, matchOut{}, err
		}
		title, author := matchSearch(it, in.Title, in.Author)
		provider, results, err := searchProviders(ctx, client, it, providers, title, author)
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
		Providers       []string `json:"providers,omitempty"        jsonschema:"metadata providers to try in order, as item_match takes them and with the same default: for a candidate the first with any candidates, so pass what item_match was passed; for an asin or isbn the first that holds it"`
		Candidate       *int     `json:"candidate,omitempty"        jsonschema:"index from item_match; the candidate's asin/isbn is used to match exactly"`
		ASIN            string   `json:"asin,omitempty"             jsonschema:"match this asin directly instead of a candidate"`
		ISBN            string   `json:"isbn,omitempty"             jsonschema:"match this isbn directly instead of a candidate"`
		Title           string   `json:"title,omitempty"            jsonschema:"must match the title override used in item_match"`
		Author          string   `json:"author,omitempty"           jsonschema:"must match the author override used in item_match"`
		OverrideDetails bool     `json:"override_details,omitempty" jsonschema:"replace existing metadata fields instead of only filling empty ones"`
		OverrideCover   bool     `json:"override_cover,omitempty"   jsonschema:"replace the existing cover"`
		Keep            []string `json:"keep,omitempty"             jsonschema:"with override_details: fields to put back as they were after the match: title, subtitle, authors, narrators, series, genres, tags, publisher, year, language, description"`
		Smart           bool     `json:"smart,omitempty"            jsonschema:"fill the empty fields, then decide each remaining difference by rule: a file-tag title, a company in the narrator field or a timestamp year is written; a curated series, a plain year or an honorific-only difference is kept; anything else is reported for review with both values. Cannot combine with override_details"`
		Confirm         bool     `json:"confirm,omitempty"          jsonschema:"true to apply. Without it nothing changes and the answer says what would be applied, and with smart every decision"`
	}
	type appliedRef struct {
		Title    string `json:"title,omitempty"`
		Author   string `json:"author,omitempty"`
		ASIN     string `json:"asin,omitempty"`
		ISBN     string `json:"isbn,omitempty"`
		Provider string `json:"provider,omitempty" jsonschema:"the provider it comes from"`
	}
	type applyOut struct {
		WouldApply *appliedRef     `json:"would_apply,omitempty" jsonschema:"without confirm: the candidate a confirmed call applies; nothing has changed"`
		Updated    bool            `json:"updated"`
		Kept       []string        `json:"kept,omitempty"        jsonschema:"fields restored after the match"`
		Fields     []fieldDecision `json:"fields,omitempty"      jsonschema:"with smart: every field the provider would write differently and what was done about it"`
		Counts     map[string]int  `json:"counts,omitempty"      jsonschema:"with smart: decisions by action"`
		Warning    string          `json:"warning,omitempty"`
		Applied    *appliedRef     `json:"applied,omitempty"     jsonschema:"the candidate that was applied; check it against item"`
		Item       *itemSummary    `json:"item,omitempty"        jsonschema:"the item after matching"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "item_match_apply",
		Description: "Apply a match: pull metadata and cover from the provider into the item, for a candidate from item_match or an asin/isbn you already know (one of candidate, asin or isbn is required: the match is never left to the provider's first guess). By default only empty fields are filled; set override_details to replace. Without confirm nothing changes and the answer says what would be applied. Changes server state.",
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

		if err := prov.checkNamed(ctx, client, in.Providers); err != nil {
			return nil, applyOut{}, err
		}
		providers, err := prov.itemProviders(ctx, client, it, in.Providers)
		if err != nil {
			return nil, applyOut{}, err
		}
		title, author := matchSearch(it, in.Title, in.Author)
		opts := abs.MatchOptions{ASIN: strings.TrimSpace(in.ASIN), ISBN: strings.TrimSpace(in.ISBN), OverrideCover: in.OverrideCover, OverrideDetails: in.OverrideDetails}
		var applied appliedRef
		var provider string

		// a candidate comes from the first store with any, as item_match's
		// did; an asin or isbn from the first store that holds it
		if byCandidate := in.Candidate != nil && opts.ASIN == "" && opts.ISBN == ""; byCandidate {
			var results []abs.BookSearchResult
			var serr error
			if provider, results, serr = searchProviders(ctx, client, it, providers, title, author); serr != nil {
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
		} else if provider, err = providerHolding(ctx, client, it, providers, opts.ASIN, opts.ISBN); err != nil {
			// no store named holds the asin or isbn
			return nil, applyOut{}, err
		}
		opts.Provider = provider
		applied.ASIN, applied.ISBN, applied.Provider = opts.ASIN, opts.ISBN, provider

		if in.Smart {
			if opts.ASIN == "" && opts.ISBN == "" {
				return nil, applyOut{}, errors.New("smart needs an asin or isbn to fetch the provider's record")
			}
			decisions, res, serr := smartApply(ctx, client, it, provider, opts.ASIN, opts.ISBN, !in.Confirm)
			if serr != nil {
				return nil, applyOut{}, serr
			}
			if !in.Confirm {
				return nil, applyOut{WouldApply: &applied, Fields: decisions, Counts: smartCounts(decisions)}, nil
			}
			out := applyOut{Applied: &applied, Fields: decisions, Counts: smartCounts(decisions)}
			out.Updated, out.Warning = res.Updated, res.Warning
			// a match that found nothing has no store to record
			if res.LibraryItem != nil {
				if err := prov.tagProvider(ctx, client, it.ID, tagsAfter(it, res, nil), provider); err != nil {
					out.Warning = joinWarnings(out.Warning, "recording the provider tag failed: "+err.Error())
				}
			}
			after, aerr := client.Item(ctx, it.ID)
			if aerr != nil {
				return nil, applyOut{}, fmt.Errorf("matched, but reading the item back failed: %w", aerr)
			}
			s := summarize(after)
			out.Item = &s
			return nil, out, nil
		}

		if !in.Confirm {
			return nil, applyOut{WouldApply: &applied}, nil
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
		after, err := client.Item(ctx, it.ID)
		if err != nil {
			return nil, applyOut{}, fmt.Errorf("matched, but reading the item back failed: %w", err)
		}
		res.LibraryItem = after
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
		Providers []string `json:"providers,omitempty" jsonschema:"cover providers to try in order, the first with any covers answering; default the server's --providers, else the library's; audiobookcovers searches audiobookcovers.com"`
		Title     string   `json:"title,omitempty"     jsonschema:"override the search title"`
		Author    string   `json:"author,omitempty"    jsonschema:"override the search author"`
	}
	type coverSearchOut struct {
		Item     string   `json:"item"`
		Provider string   `json:"provider" jsonschema:"the provider the covers came from; the last one asked when none had any"`
		Covers   []string `json:"covers"   jsonschema:"image urls; pass one to item_cover_edit"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "item_cover_search",
		Description: "Find candidate cover images for an item from the metadata providers. Returns urls for item_cover_edit.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in coverSearchIn) (*mcp.CallToolResult, coverSearchOut, error) {
		it, err := resolveItem(ctx, client, in.Library, in.Item)
		if err != nil {
			return nil, coverSearchOut{}, err
		}
		if err := prov.checkNamed(ctx, client, in.Providers); err != nil {
			return nil, coverSearchOut{}, err
		}
		providers, err := prov.itemProviders(ctx, client, it, in.Providers)
		if err != nil {
			return nil, coverSearchOut{}, err
		}
		title, author := matchSearch(it, in.Title, in.Author)
		if it.IsPodcast() {
			author = ""
		}
		out := coverSearchOut{Item: it.Title(), Covers: []string{}}
		for _, out.Provider = range providers {
			covers, err := client.SearchCovers(ctx, out.Provider, title, author, it.IsPodcast())
			if err != nil {
				return nil, coverSearchOut{}, err
			}
			if len(covers) > 0 {
				out.Covers = covers
				break
			}
		}

		return nil, out, nil
	})

	type coverEditOut struct {
		Item  string `json:"item"`
		Cover string `json:"cover" jsonschema:"the cover file the server now has, read back after the change; empty once removed"`
	}
	type coverEditIn struct {
		itemRef
		URL    string `json:"url,omitempty"    jsonschema:"image url to download as the cover"`
		File   string `json:"file,omitempty"   jsonschema:"instead of a url: an image in the item's folder, by its filename, its path in the folder or its full path (item_get files=true lists them); one put there since the last scan is scanned in first"`
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
			var f *abs.LibraryFile
			if f, err = coverFile(ctx, client, it, in.File); err == nil {
				err = client.SetCoverFromFile(ctx, it.ID, f.Metadata.Path)
			}
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
		Backup   bool `json:"backup,omitempty"       jsonschema:"keep a copy of the original audio files"`
		M4B      bool `json:"m4b,omitempty"          jsonschema:"instead of tagging the files: merge the audio that plays into one m4b named after the book's folder, carrying its metadata, chapters and cover. The server moves the files merged out of the folder into its cache for the book, which a cache purge empties. Without confirm nothing changes and the answer says what would happen"`
		Bitrate  int  `json:"bitrate_kbps,omitempty" jsonschema:"with m4b: the bitrate to encode at, 16 to 320; default the book's own, the highest of its files'"`
		Channels int  `json:"channels,omitempty"     jsonschema:"with m4b: 1 or 2; default the book's own"`
		Confirm  bool `json:"confirm,omitempty"      jsonschema:"with m4b: true to merge"`
		Cancel   bool `json:"cancel,omitempty"       jsonschema:"with m4b: stop the book's merge while it is still running; the files stay as they were"`
	}
	type embedOut struct {
		Item       string   `json:"item"`
		Embedded   *bool    `json:"embedded,omitempty"    jsonschema:"tagging the files: they carry the item's metadata, read back after a rescan"`
		Running    bool     `json:"running,omitempty"     jsonschema:"the embed or merge was still running or queued when this stopped waiting: item_rescan the item once server_tasks no longer lists it, or audit_unembedded keeps reading the old tags; a merge's result is then item_get files=true"`
		Rescan     string   `json:"rescan,omitempty"      jsonschema:"the rescan result once the embed or merge finished"`
		Differs    string   `json:"differs,omitempty"     jsonschema:"what the files' tags still disagree on after the embed: it failed, or wrote something else"`
		WouldMerge *m4bPlan `json:"would_merge,omitempty" jsonschema:"m4b without confirm: what a confirmed call does; nothing has changed"`
		Merged     *m4bPlan `json:"merged,omitempty"      jsonschema:"m4b: what was merged, checked after a rescan: the book plays the one m4b, at the length the files had"`
		Cancelled  bool     `json:"cancelled,omitempty"`
	}
	// mergeM4B merges a book into one m4b, or says what that would do, or
	// stops a merge running. The server lists the merge only while it runs,
	// failed and finished alike leaving the list, so the book is scanned and
	// read back to learn which it was
	mergeM4B := func(ctx context.Context, it *abs.Item, kbps, channels int, confirm, cancel bool) (embedOut, error) {
		tasks, err := client.Tasks(ctx)
		if err != nil {
			return embedOut{}, err
		}
		switch running := mergeRunning(tasks, it.ID); {
		case cancel && !running:
			return embedOut{}, fmt.Errorf("no merge of %q is running", it.Title())
		case cancel:
			if err := client.CancelEncodeM4B(ctx, it.ID); err != nil {
				return embedOut{}, err
			}
			return embedOut{Cancelled: true}, nil
		case running:
			return embedOut{Running: true}, nil
		}
		if confirm {
			release := r.locks.hold(itemKeys(it.ID)...)
			defer release()
			if it, err = client.Item(ctx, it.ID); err != nil { // as it is once held
				return embedOut{}, err
			}
		}
		plan, err := planM4B(it, kbps, channels)
		if err != nil {
			return embedOut{}, err
		}
		if !confirm {
			return embedOut{WouldMerge: plan}, nil
		}

		before := 0.0
		for _, af := range playOrder(it.Media.AudioFiles) {
			before += af.Duration
		}
		if err := client.EncodeM4B(ctx, it.ID, strconv.Itoa(plan.Bitrate)+"k", strconv.Itoa(plan.Channels), ""); err != nil {
			return embedOut{}, err
		}
		deadline := time.Now().Add(embedWait)
		for {
			tasks, err = client.Tasks(ctx)
			if err != nil {
				return embedOut{}, fmt.Errorf("the merge started, but reading the server's tasks failed: %w", err)
			}
			if !mergeRunning(tasks, it.ID) {
				break
			}
			if time.Now().After(deadline) {
				return embedOut{Running: true}, nil
			}
			select {
			case <-ctx.Done():
				return embedOut{}, ctx.Err()
			case <-time.After(embedPoll):
			}
		}
		// the server's file watcher sees the merge's new file too, and until
		// its own scan runs a rescan skips a file it holds: for some seconds
		// the book reads as holding no audio. The files merged still playing
		// is a merge that failed; anything else is waited out
		out := embedOut{}
		merged := playOrder(it.Media.AudioFiles)
		settle := time.Now().Add(mergeSettle)
		for {
			if out.Rescan, err = client.ScanItem(ctx, it.ID); err != nil {
				return embedOut{}, fmt.Errorf("the merge ended, but the rescan that reads the book back failed: %w", err)
			}
			after, err := client.Item(ctx, it.ID)
			if err != nil {
				return embedOut{}, fmt.Errorf("the merge ended, but reading the book back failed: %w", err)
			}
			merr := mergedInto(after, plan.Into, before)
			if merr == nil {
				break
			}
			if sameTracks(playOrder(after.Media.AudioFiles), merged) || time.Now().After(settle) {
				return embedOut{}, fmt.Errorf("the merge of %q ended without making the book one m4b: %w. server_tasks log=true match=AbMergeManager says why", it.Title(), merr)
			}
			select {
			case <-ctx.Done():
				return embedOut{}, ctx.Err()
			case <-time.After(embedPoll):
			}
		}
		out.Merged = plan
		return out, nil
	}

	add(r, writeTool, &mcp.Tool{
		Name:        "item_embed_metadata",
		Description: "Write the item's metadata and chapters into its audio files' tags so they travel with the files, then rescan the item and check the tags: embedded says they now match. audit_unembedded lists the books where this is due. With m4b, merge the book's audio into one m4b instead, previewed until confirm. Waits for the embed or merge, up to two minutes; a longer one comes back running. Admin only. Changes the files on disk.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in embedIn) (*mcp.CallToolResult, embedOut, error) {
		if !in.M4B && (in.Bitrate != 0 || in.Channels != 0 || in.Confirm || in.Cancel) {
			return nil, embedOut{}, errors.New("bitrate_kbps, channels, confirm and cancel are for m4b; tagging the files takes none of them")
		}
		if in.M4B && in.Backup {
			return nil, embedOut{}, errors.New("m4b keeps the files it merges in the server's cache already; backup is for tagging the files")
		}
		it, err := resolveItemToChange(ctx, client, in.Library, in.Item)
		if err != nil {
			return nil, embedOut{}, err
		}
		if it.IsPodcast() {
			return nil, embedOut{}, errNotBook
		}
		if in.M4B {
			out, merr := mergeM4B(ctx, it, in.Bitrate, in.Channels, in.Confirm, in.Cancel)
			out.Item = it.Title()
			return nil, out, merr
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
		out.Embedded, out.Differs = new(!stale), detail

		return nil, out, nil
	})

	type deleteIn struct {
		itemRef
		DeleteFiles bool   `json:"delete_files,omitempty" jsonschema:"also delete the item's folder from disk (irreversible); default keeps the files"`
		Confirm     bool   `json:"confirm,omitempty"      jsonschema:"true to delete; without it nothing changes and the answer says what a confirmed call would remove"`
		File        string `json:"file,omitempty"         jsonschema:"delete only this file of the book, by the filename item_get files=true lists (its path in the folder where two share a name), from the book and from disk; the book stays. Not an audio file, whose removal the server does not take off the book's length: take one out on disk and item_rescan. Not the cover: item_cover_edit first"`
	}
	type deleteOut struct {
		Deleted          string   `json:"deleted,omitempty"           jsonschema:"the item deleted, or with file the file"`
		WouldDelete      string   `json:"would_delete,omitempty"      jsonschema:"without confirm: the item a confirmed call deletes, or with file the file; nothing has changed"`
		Files            string   `json:"files,omitempty"             jsonschema:"with delete_files or file: what is erased from disk, the item's folder and everything in it, or the one file"`
		FilesRemoved     bool     `json:"files_removed"               jsonschema:"with file: checked on disk and gone"`
		Bookmarks        []string `json:"bookmarks,omitempty"         jsonschema:"the API key user's bookmarks on it, which go before the item does"`
		BookmarksRemoved int      `json:"bookmarks_removed,omitempty" jsonschema:"the API key user's bookmarks on it, removed before the delete"`
		Note             string   `json:"note,omitempty"              jsonschema:"with file: what else changes, or what could not be checked"`
	}
	// deleteFile takes one file out of a book and off the disk, then looks at
	// the disk: the server takes the file out of the book even when it cannot
	// remove it, and the next scan would put it back
	deleteFile := func(ctx context.Context, it *abs.Item, name string, confirm bool) (deleteOut, error) {
		release := r.locks.hold(itemKeys(it.ID)...)
		defer release()
		it, err := client.Item(ctx, it.ID) // as it is once held
		if err != nil {
			return deleteOut{}, err
		}
		me, err := client.Me(ctx)
		if err != nil {
			return deleteOut{}, err
		}
		if !me.Permissions.Delete {
			return deleteOut{}, fmt.Errorf("%s may not delete files: the account lacks the delete permission", me.Username)
		}
		f, err := fileToDelete(it, name)
		if err != nil {
			return deleteOut{}, err
		}

		label := fmt.Sprintf("%q from %q", cmp.Or(f.Metadata.RelPath, f.Metadata.Filename), it.Title())
		out := deleteOut{Files: "the file " + f.Metadata.Path}
		if ef := it.Media.EbookFile; ef != nil && ef.Ino == f.Ino {
			out.Note = "it is the main ebook, and the book is left with none"
			if slices.ContainsFunc(it.LibraryFiles, func(o abs.LibraryFile) bool { return o.FileType == "ebook" && o.Ino != f.Ino }) {
				out.Note += ": item_edit ebook makes one of the others the main one"
			}
		}
		if !confirm {
			out.WouldDelete = label
			return out, nil
		}

		if err := client.DeleteItemFile(ctx, it.ID, f.Ino); err != nil {
			return deleteOut{}, err
		}
		out.Deleted = label
		if !me.Permissions.Upload {
			out.Note = joinWarnings(out.Note, "not checked on disk: the check needs the upload permission, which "+me.Username+" lacks")
			return out, nil
		}
		lib, err := client.Library(ctx, it.LibraryID)
		if err != nil {
			return deleteOut{}, fmt.Errorf("%s was taken out of the book, but reading its library to look at the disk failed: %w", label, err)
		}
		gone, err := fileGone(ctx, client, lib, it, f)
		switch {
		case err != nil:
			return deleteOut{}, fmt.Errorf("%s was taken out of the book, but looking for it on disk failed: %w", label, err)
		case !gone:
			return deleteOut{}, fmt.Errorf("%s was taken out of the book but is still on disk at %s: the server could not remove it, and the next scan puts it back", label, f.Metadata.Path)
		}
		out.FilesRemoved = true
		return out, nil
	}
	add(r, deleteTool, &mcp.Tool{
		Name: "item_delete",
		Description: "Remove an item from the library, and with delete_files also erase its folder from disk; or with file, erase one file of a book and keep the book. Listening progress for it is lost. The API key user's bookmarks on it are removed first: Audiobookshelf keeps a bookmark on a deleted item and will not remove it afterwards, though other accounts' bookmarks stay. " +
			"Without confirm=true nothing changes and the answer says what would go: the record, the folder or file on disk with delete_files, and the bookmarks. Requires the delete permission.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in deleteIn) (*mcp.CallToolResult, deleteOut, error) {
		if in.File != "" && in.DeleteFiles {
			return nil, deleteOut{}, errors.New("file erases that one file and keeps the book; delete_files erases the book's whole folder. One or the other")
		}
		it, err := resolveItemToChange(ctx, client, in.Library, in.Item)
		if err != nil {
			return nil, deleteOut{}, err
		}
		if in.File != "" {
			out, derr := deleteFile(ctx, it, in.File, in.Confirm)
			return nil, out, derr
		}
		// the account's bookmarks are one list, which user_bookmark_edit
		// reads and saves whole: held from reading them to the delete
		defer r.locks.hold(append(itemKeys(it.ID), "bookmarks")...)()
		me, err := client.Me(ctx)
		if err != nil {
			return nil, deleteOut{}, err
		}
		// asked before the bookmarks go, so a refused delete costs nothing;
		// the server grants it by the permission alone, an admin's too
		if !me.Permissions.Delete {
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
// handing back a running one, and embedPoll how often it looks. mergeSettle
// is how long a merged book is read back for while the server's file watcher
// holds its new file: the watcher's own scan runs ten seconds after the last
// change it saw.
var (
	embedWait   = 2 * time.Minute
	embedPoll   = 500 * time.Millisecond
	mergeSettle = 30 * time.Second
)

// itemProviders is the stores a call about one item asks, in order: the ones
// it named, as it named them; else the configured default, else the item's
// library's own, with the store the item's provider tag records first, as the
// store that had the book last time is the one to ask first. Stores a call
// names are checked by checkNamed before this, once a call.
func (p providerConfig) itemProviders(ctx context.Context, client *abs.Client, it *abs.Item, named []string) ([]string, error) {
	if len(named) > 0 {
		return named, nil
	}
	if len(p.providers) > 0 {
		return p.providerOrder(it, slices.Clone(p.providers)), nil
	}
	lib, err := client.Library(ctx, it.LibraryID)
	if err != nil {
		return nil, fmt.Errorf("reading the library's provider, as none was named: %w", err)
	}
	return p.providerOrder(it, []string{lib.Provider}), nil
}

// checkNamed refuses a store a call named that the server does not have. The
// server answers a name it does not know by searching Google, so a misspelt
// audible.ca comes back as Google's guesses with nothing to say so.
func (p providerConfig) checkNamed(ctx context.Context, client *abs.Client, named []string) error {
	if len(named) == 0 {
		return nil
	}
	return p.checkProviders(ctx, client, named, false)
}

// matchSearch is the title and author a provider search asks for: the ones
// given, else the item's own.
func matchSearch(it *abs.Item, title, author string) (searchTitle, searchAuthor string) {
	return cmp.Or(title, it.Title()), cmp.Or(author, it.Media.Metadata.AuthorDisplay())
}

// searchProviders asks each store in turn for a title and author and answers
// with the first that has any candidates, and which store that was; the last
// one asked when none has.
func searchProviders(ctx context.Context, client *abs.Client, it *abs.Item, providers []string, title, author string) (string, []abs.BookSearchResult, error) {
	var provider string
	for _, provider = range providers {
		results, err := client.SearchBooks(ctx, provider, title, author, it.ID)
		if err != nil {
			return provider, nil, err
		}
		if len(results) > 0 {
			return provider, results, nil
		}
	}
	return provider, nil, nil
}

// providerHolding is the first of the stores that has a record for an asin
// or isbn. One store alone is taken at its word, without asking it first.
func providerHolding(ctx context.Context, client *abs.Client, it *abs.Item, providers []string, asin, isbn string) (string, error) {
	if len(providers) == 1 {
		return providers[0], nil
	}
	for _, provider := range providers {
		_, err := providerRecord(ctx, client, it, provider, asin, isbn)
		var none *noRecordError
		switch {
		case err == nil:
			return provider, nil
		case !errors.As(err, &none):
			return "", err
		}
	}
	return "", fmt.Errorf("none of %s has a record for %s", strings.Join(providers, ", "), cmp.Or(asin, isbn))
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
// sendError is a change to many items that failed part way: the requests
// before the one that failed stay made, and a caller told only of the error
// would take the whole change as not made.
type sendError struct {
	from, to, of, updated int
	err                   error
}

func (e *sendError) Error() string {
	return fmt.Sprintf("the batch of items %d to %d of %d failed and may have landed in part; %d items before it were updated, and the %d after it were not sent: %v",
		e.from, e.to, e.of, e.updated, e.of-e.to, e.err)
}

func (e *sendError) Unwrap() error { return e.err }

// updateMany sends media updates for many items and answers how many the
// server changed. Books go through the server's batch route, a hundred at a
// time: one request carrying hundreds of items is what a reverse proxy times
// out on, and a timeout after part of it landed would read as nothing done.
// Podcasts, those shows names, go one at a time through the route item_edit
// uses: the batch route answered 502 for two podcasts of a real library
// where that route took the same change. No test server has reproduced it,
// so they go the way that is known to work.
func updateMany(ctx context.Context, client *abs.Client, updates []abs.BatchMediaUpdate, shows map[string]bool) (int, error) {
	var books, podcasts []abs.BatchMediaUpdate
	for _, u := range updates {
		if shows[u.ID] {
			podcasts = append(podcasts, u)
		} else {
			books = append(books, u)
		}
	}
	updated, sent := 0, 0
	failed := func(n int, err error) error {
		// the only request there was: nothing came before it, nothing after
		if sent == 0 && n == len(updates) {
			return err
		}
		return &sendError{from: sent + 1, to: sent + n, of: len(updates), updated: updated, err: err}
	}
	for chunk := range slices.Chunk(books, sweepBatchSize) {
		n, err := client.BatchUpdate(ctx, chunk)
		if err != nil {
			return updated, failed(len(chunk), err)
		}
		updated, sent = updated+n, sent+len(chunk)
	}
	for _, u := range podcasts {
		changed, err := client.UpdateMedia(ctx, u.ID, u.MediaPayload)
		if err != nil {
			return updated, failed(1, err)
		}
		if changed {
			updated++
		}
		sent++
	}
	return updated, nil
}

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
