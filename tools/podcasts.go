package tools

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/katbyte/abs-mcp/lib/abs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var errNotPodcast = errors.New("this item is a book, not a podcast")

// resolvePodcast resolves an item and checks it is a podcast.
func resolvePodcast(ctx context.Context, client *abs.Client, library, idOrTitle string) (*abs.Item, error) {
	it, err := resolveItem(ctx, client, library, idOrTitle)
	if err != nil {
		return nil, err
	}
	if !it.IsPodcast() {
		return nil, errNotPodcast
	}
	return it, nil
}

// findEpisode locates an episode of a podcast by id or exact title.
func findEpisode(it *abs.Item, idOrTitle string) (*abs.Episode, error) {
	idOrTitle = strings.TrimSpace(idOrTitle)
	for i := range it.Media.Episodes {
		e := &it.Media.Episodes[i]
		if e.ID == idOrTitle || strings.EqualFold(e.Title, idOrTitle) {
			return e, nil
		}
	}
	return nil, fmt.Errorf("no episode %q in %q (podcast_episodes lists them)", idOrTitle, it.Title())
}

type feedEpisodeRow struct {
	Index       int    `json:"index"                 jsonschema:"pass to podcast_episode_download"`
	Title       string `json:"title"`
	Season      string `json:"season,omitempty"`
	Episode     string `json:"episode,omitempty"`
	Type        string `json:"type,omitempty"`
	Published   string `json:"published,omitempty"`
	Duration    string `json:"duration,omitempty"`
	Description string `json:"description,omitempty"`
}

func feedEpisodeRows(eps []abs.FeedEpisode, withDescription bool) []feedEpisodeRow {
	rows := make([]feedEpisodeRow, 0, len(eps))
	for i, e := range eps {
		row := feedEpisodeRow{Index: i, Title: e.Title, Season: e.Season.String(), Episode: e.Episode.String(), Type: e.EpisodeType, Published: fmtDate(e.PublishedAt)}
		if row.Published == "" {
			row.Published = e.PubDate
		}
		if e.DurationSeconds != nil {
			row.Duration = fmtDuration(*e.DurationSeconds)
		} else {
			row.Duration = e.Duration.String()
		}
		if withDescription {
			row.Description = clip(plain(e.Description), 200)
		}
		rows = append(rows, row)
	}
	return rows
}

func registerPodcastTools(r *registry) {
	client := r.client

	type episodesIn struct {
		Item    string `json:"item,omitempty"    jsonschema:"podcast id or exact title; omit for the newest episodes across every podcast"`
		Library string `json:"library,omitempty" jsonschema:"narrow to one library by name or id"`
		Limit   int    `json:"limit,omitempty"   jsonschema:"page size, default 50"`
		Offset  int    `json:"offset,omitempty"  jsonschema:"only when a podcast is named"`
	}
	type episodesOut struct {
		Podcast  string           `json:"podcast"`
		Total    int              `json:"total"`
		Episodes []episodeSummary `json:"episodes" jsonschema:"newest first, with the API key user's progress"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "podcast_episodes",
		Description: "Downloaded podcast episodes, newest first. Name a podcast for its own episodes with the API key user's progress on each; omit it for the newest episodes across every podcast in the library - 'what is new to listen to'. For episodes not downloaded yet, see podcast_feed_episodes.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in episodesIn) (*mcp.CallToolResult, episodesOut, error) {
		// no podcast named: the library-wide recent-episodes endpoint, which is
		// the same question asked of everything rather than of one show
		if strings.TrimSpace(in.Item) == "" {
			libs, err := resolveLibraries(ctx, client, in.Library)
			if err != nil {
				return nil, episodesOut{}, err
			}
			out := episodesOut{Episodes: []episodeSummary{}}
			podcastLibs := 0
			for i := range libs {
				if !libs[i].IsPodcast() {
					continue
				}
				podcastLibs++
				eps, err := client.RecentEpisodes(ctx, libs[i].ID, limitOr(in.Limit, defaultLimit), 0)
				if err != nil {
					return nil, episodesOut{}, err
				}
				for j := range eps {
					out.Episodes = append(out.Episodes, summarizeEpisode(&eps[j], "", false))
				}
			}
			if podcastLibs == 0 {
				return nil, episodesOut{}, errors.New("no podcast library found")
			}
			out.Total = len(out.Episodes)

			return nil, out, nil
		}

		it, err := resolvePodcast(ctx, client, in.Library, in.Item)
		if err != nil {
			return nil, episodesOut{}, err
		}
		progress := map[string]*abs.MediaProgress{}
		if me, err := client.Me(ctx); err == nil {
			for i := range me.MediaProgress {
				p := &me.MediaProgress[i]
				if p.LibraryItemID == it.ID && p.EpisodeID != "" {
					progress[p.EpisodeID] = p
				}
			}
		}

		eps := newestEpisodes(it.Media.Episodes)
		limit := limitOr(in.Limit, 50)
		// a negative offset would index past the end of the list; treat it as
		// the start, like an offset past the end yields nothing
		offset := max(in.Offset, 0)
		out := episodesOut{Podcast: it.Title(), Total: len(eps), Episodes: []episodeSummary{}}
		for i := offset; i < len(eps) && len(out.Episodes) < limit; i++ {
			e := &eps[i]
			e.Progress = progress[e.ID]
			out.Episodes = append(out.Episodes, summarizeEpisode(e, "", false))
		}

		return nil, out, nil
	})

	type episodeIn struct {
		itemRef
		Episode string `json:"episode" jsonschema:"episode id or exact title"`
	}
	type chapterRow struct {
		Title string `json:"title"`
		Start string `json:"start"`
	}
	type episodeGetOut struct {
		episodeSummary
		Subtitle string       `json:"subtitle,omitempty"`
		Audio    string       `json:"audio,omitempty"`
		File     string       `json:"file,omitempty"`
		URL      string       `json:"source_url,omitempty"`
		Chapters []chapterRow `json:"chapters,omitempty"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "podcast_episode_get",
		Description: "One podcast episode in full: description, audio file, chapters and progress.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in episodeIn) (*mcp.CallToolResult, episodeGetOut, error) {
		it, err := resolvePodcast(ctx, client, in.Library, in.Item)
		if err != nil {
			return nil, episodeGetOut{}, err
		}
		e, err := findEpisode(it, in.Episode)
		if err != nil {
			return nil, episodeGetOut{}, err
		}
		e.Progress, _ = client.Progress(ctx, it.ID, e.ID)

		summary := summarizeEpisode(e, it.Title(), true)
		summary.Description = clip(plain(e.Description), 1500)
		out := episodeGetOut{episodeSummary: summary, Subtitle: e.Subtitle}
		if e.AudioFile != nil {
			out.Audio = fmt.Sprintf("%s %d kbps", e.AudioFile.Codec, e.AudioFile.BitRate/1000)
			out.File = e.AudioFile.Metadata.Filename
		}
		if e.Enclosure != nil {
			out.URL = e.Enclosure.URL
		}
		for _, ch := range e.Chapters {
			out.Chapters = append(out.Chapters, chapterRow{Title: ch.Title, Start: fmtDuration(ch.Start)})
		}

		return nil, out, nil
	})

	type episodeEditIn struct {
		episodeIn
		Title       string `json:"title,omitempty"`
		Subtitle    string `json:"subtitle,omitempty"`
		Description string `json:"description,omitempty"`
		Season      string `json:"season,omitempty"`
		Number      string `json:"number,omitempty"      jsonschema:"episode number"`
		Type        string `json:"type,omitempty"        jsonschema:"full, trailer or bonus"`
		PubDate     string `json:"pub_date,omitempty"`
	}
	type doneOut struct {
		Done bool `json:"done"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "podcast_episode_edit",
		Description: "Edit an episode's title, subtitle, description, season/episode number, type or publish date. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in episodeEditIn) (*mcp.CallToolResult, doneOut, error) {
		it, err := resolvePodcast(ctx, client, in.Library, in.Item)
		if err != nil {
			return nil, doneOut{}, err
		}
		e, err := findEpisode(it, in.Episode)
		if err != nil {
			return nil, doneOut{}, err
		}
		upd := abs.EpisodeUpdate{
			Title:       strPtr(in.Title),
			Subtitle:    strPtr(in.Subtitle),
			Description: strPtr(in.Description),
			Season:      strPtr(in.Season),
			Episode:     strPtr(in.Number),
			EpisodeType: strPtr(in.Type),
			PubDate:     strPtr(in.PubDate),
		}
		if upd == (abs.EpisodeUpdate{}) {
			return nil, doneOut{}, errors.New("nothing to change")
		}
		if _, err := client.UpdateEpisode(ctx, it.ID, e.ID, upd); err != nil {
			return nil, doneOut{}, err
		}

		return nil, doneOut{Done: true}, nil
	})

	type episodeDeleteIn struct {
		episodeIn
		DeleteFile bool `json:"delete_file,omitempty" jsonschema:"also delete the audio file from disk"`
	}
	add(r, deleteTool, &mcp.Tool{
		Name:        "podcast_episode_delete",
		Description: "Remove an episode from a podcast, and with delete_file erase its audio from disk. Requires the delete permission.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in episodeDeleteIn) (*mcp.CallToolResult, doneOut, error) {
		it, err := resolvePodcast(ctx, client, in.Library, in.Item)
		if err != nil {
			return nil, doneOut{}, err
		}
		e, err := findEpisode(it, in.Episode)
		if err != nil {
			return nil, doneOut{}, err
		}
		if err := client.DeleteEpisode(ctx, it.ID, e.ID, in.DeleteFile); err != nil {
			return nil, doneOut{}, err
		}

		return nil, doneOut{Done: true}, nil
	})

	type checkNewIn struct {
		itemRef
		Limit int `json:"limit,omitempty" jsonschema:"maximum new episodes to download, default 3"`
	}
	type checkNewOut struct {
		Podcast string           `json:"podcast"`
		Queued  []feedEpisodeRow `json:"queued"  jsonschema:"new episodes found and queued for download"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "podcast_check_new",
		Description: "Fetch a podcast's feed and download any new episodes (up to limit). Admin only. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in checkNewIn) (*mcp.CallToolResult, checkNewOut, error) {
		it, err := resolvePodcast(ctx, client, in.Library, in.Item)
		if err != nil {
			return nil, checkNewOut{}, err
		}
		eps, err := client.CheckNewEpisodes(ctx, it.ID, in.Limit)
		if err != nil {
			return nil, checkNewOut{}, err
		}

		return nil, checkNewOut{Podcast: it.Title(), Queued: feedEpisodeRows(eps, false)}, nil
	})

	type feedSearchIn struct {
		itemRef
		Title string `json:"title,omitempty" jsonschema:"episode title text to look for; empty lists the whole feed"`
		Limit int    `json:"limit,omitempty" jsonschema:"maximum episodes, default 25"`
	}
	type feedSearchOut struct {
		Podcast  string           `json:"podcast"`
		Total    int              `json:"total"`
		Episodes []feedEpisodeRow `json:"episodes" jsonschema:"episodes available in the feed (not necessarily downloaded); newest first"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "podcast_feed_episodes",
		Description: "Episodes available in a podcast's RSS feed, newest first, optionally filtered by title text. Use the index with podcast_episode_download to fetch back-catalogue episodes.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in feedSearchIn) (*mcp.CallToolResult, feedSearchOut, error) {
		it, err := resolvePodcast(ctx, client, in.Library, in.Item)
		if err != nil {
			return nil, feedSearchOut{}, err
		}
		eps, err := feedEpisodes(ctx, client, it, in.Title)
		if err != nil {
			return nil, feedSearchOut{}, err
		}
		limit := limitOr(in.Limit, defaultLimit)
		rows := feedEpisodeRows(eps, true)
		if len(rows) > limit {
			rows = rows[:limit]
		}

		return nil, feedSearchOut{Podcast: it.Title(), Total: len(eps), Episodes: rows}, nil
	})

	type downloadIn struct {
		itemRef
		Title   string `json:"title,omitempty" jsonschema:"the same title filter passed to podcast_feed_episodes, so indexes line up"`
		Indexes []int  `json:"indexes"         jsonschema:"episode indexes from podcast_feed_episodes"`
	}
	type downloadOut struct {
		Podcast       string   `json:"podcast"`
		Queued        []string `json:"queued,omitempty"         jsonschema:"accepted by the download queue; podcast_episodes shows them once they have downloaded"`
		AlreadyHeld   []string `json:"already_held,omitempty"   jsonschema:"feed episodes the podcast already has, by guid or audio url, whatever they are titled now; not downloaded again"`
		AlreadyQueued []string `json:"already_queued,omitempty" jsonschema:"feed episodes already downloading or waiting in the queue"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "podcast_episode_download",
		Description: "Queue episodes from a podcast's feed for download, chosen by index from podcast_feed_episodes, and say which the podcast already holds or is already fetching (those are not downloaded twice). Admin only. Changes server state; downloads run in the background (podcast_downloads shows the queue, podcast_episodes what arrived).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in downloadIn) (*mcp.CallToolResult, downloadOut, error) {
		it, err := resolvePodcast(ctx, client, in.Library, in.Item)
		if err != nil {
			return nil, downloadOut{}, err
		}
		if len(in.Indexes) == 0 {
			return nil, downloadOut{}, errors.New("indexes is required")
		}
		eps, err := feedEpisodes(ctx, client, it, in.Title)
		if err != nil {
			return nil, downloadOut{}, err
		}
		queued, err := queuedURLs(ctx, client, it)
		if err != nil {
			return nil, downloadOut{}, err
		}

		// the server downloads an episode it already has a second time, under
		// a filename with a random suffix, and adds it as another episode
		var pick []abs.FeedEpisode
		out := downloadOut{Podcast: it.Title()}
		picked := map[int]bool{}
		for _, i := range in.Indexes {
			if i < 0 || i >= len(eps) {
				return nil, downloadOut{}, fmt.Errorf("index %d out of range (feed has %d episodes)", i, len(eps))
			}
			if picked[i] {
				continue
			}
			picked[i] = true
			switch {
			case heldEpisode(it, &eps[i]) != nil:
				out.AlreadyHeld = append(out.AlreadyHeld, eps[i].Title)
			case eps[i].Enclosure != nil && queued[strings.TrimSpace(eps[i].Enclosure.URL)]:
				out.AlreadyQueued = append(out.AlreadyQueued, eps[i].Title)
			default:
				pick = append(pick, eps[i])
				out.Queued = append(out.Queued, eps[i].Title)
			}
		}
		if len(pick) == 0 {
			return nil, out, nil
		}
		if err := client.DownloadEpisodes(ctx, it.ID, pick); err != nil {
			return nil, downloadOut{}, err
		}

		return nil, out, nil
	})

	type downloadsIn struct {
		Library string `json:"library,omitempty" jsonschema:"podcast library name or id; optional when there is one"`
	}
	type downloadRow struct {
		Podcast string `json:"podcast"`
		Episode string `json:"episode"`
		Status  string `json:"status"            jsonschema:"downloading, queued, finished or failed"`
		Started string `json:"started,omitempty"`
	}
	type downloadsOut struct {
		Downloads []downloadRow `json:"downloads"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "podcast_downloads",
		Description: "The episode download queue for a podcast library: what is downloading now and what is waiting.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in downloadsIn) (*mcp.CallToolResult, downloadsOut, error) {
		libs, err := resolveLibraries(ctx, client, in.Library)
		if err != nil {
			return nil, downloadsOut{}, err
		}
		out := downloadsOut{Downloads: []downloadRow{}}
		rowOf := func(d *abs.EpisodeDownload, status string) downloadRow {
			switch {
			case d.Failed:
				status = "failed"
			case d.IsFinished:
				status = "finished"
			}
			return downloadRow{Podcast: d.PodcastTitle, Episode: d.EpisodeDisplayTitle, Status: status, Started: fmtTime(d.StartedAt)}
		}
		for i := range libs {
			if !libs[i].IsPodcast() {
				continue
			}
			current, queue, err := client.EpisodeDownloads(ctx, libs[i].ID)
			if err != nil {
				return nil, downloadsOut{}, err
			}
			if current != nil {
				out.Downloads = append(out.Downloads, rowOf(current, "downloading"))
			}
			for j := range queue {
				out.Downloads = append(out.Downloads, rowOf(&queue[j], "queued"))
			}
		}

		return nil, out, nil
	})

	type searchIn struct {
		Query   string `json:"query"             jsonschema:"podcast name or keywords"`
		Country string `json:"country,omitempty" jsonschema:"iTunes store country, default us"`
		Limit   int    `json:"limit,omitempty"   jsonschema:"maximum results, default 10"`
	}
	type podcastHit struct {
		Title       string   `json:"title"`
		Author      string   `json:"author,omitempty"`
		FeedURL     string   `json:"feed_url"              jsonschema:"pass to podcast_add"`
		Episodes    int      `json:"episodes,omitempty"`
		Genres      []string `json:"genres,omitempty"`
		Explicit    bool     `json:"explicit,omitempty"`
		Description string   `json:"description,omitempty"`
	}
	type searchOut struct {
		Results []podcastHit `json:"results"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "podcast_search",
		Description: "Search iTunes for podcasts to add, returning their feed urls for podcast_add.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in searchIn) (*mcp.CallToolResult, searchOut, error) {
		if strings.TrimSpace(in.Query) == "" {
			return nil, searchOut{}, errors.New("query is required")
		}
		results, err := client.SearchPodcasts(ctx, in.Query, in.Country)
		if err != nil {
			return nil, searchOut{}, err
		}
		out := searchOut{Results: []podcastHit{}}
		for i, p := range results {
			if i >= limitOr(in.Limit, 10) {
				break
			}
			out.Results = append(out.Results, podcastHit{
				Title:       p.Title,
				Author:      p.ArtistName,
				FeedURL:     p.FeedURL,
				Episodes:    p.TrackCount,
				Genres:      p.Genres,
				Explicit:    p.Explicit,
				Description: clip(plain(p.DescriptionPlain+p.Description), 200),
			})
		}

		return nil, out, nil
	})

	type addIn struct {
		FeedURL      string `json:"feed_url"                  jsonschema:"RSS feed url (from podcast_search or known)"`
		Library      string `json:"library,omitempty"         jsonschema:"podcast library name or id; optional when there is one"`
		Folder       string `json:"folder,omitempty"          jsonschema:"folder name to create under the library folder; default the podcast title"`
		AutoDownload bool   `json:"auto_download,omitempty"   jsonschema:"download new episodes automatically"`
		Download     int    `json:"download_latest,omitempty" jsonschema:"queue this many of the newest episodes now"`
	}
	type addOut struct {
		itemSummary
		FeedEpisodes int      `json:"feed_episodes"     jsonschema:"episodes available in the feed"`
		Queued       []string `json:"queued,omitempty"  jsonschema:"the newest episodes queued for download with download_latest"`
		Warning      string   `json:"warning,omitempty"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "podcast_add",
		Description: "Subscribe to a podcast: parse its feed, create it in the podcast library, and optionally queue the newest episodes. A feed the library already subscribes to is refused. Admin only. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in addIn) (*mcp.CallToolResult, addOut, error) {
		libs, err := resolveLibraries(ctx, client, in.Library)
		if err != nil {
			return nil, addOut{}, err
		}
		var lib *abs.Library
		for i := range libs {
			if libs[i].IsPodcast() {
				if lib != nil {
					return nil, addOut{}, fmt.Errorf("several podcast libraries; pass one (have: %s)", libraryNames(libs))
				}
				lib = &libs[i]
			}
		}
		if lib == nil {
			return nil, addOut{}, errors.New("no podcast library found")
		}
		if len(lib.Folders) == 0 {
			return nil, addOut{}, fmt.Errorf("library %q has no folders", lib.Name)
		}

		feed, err := client.ParseFeed(ctx, in.FeedURL)
		if err != nil {
			return nil, addOut{}, err
		}
		// the server refuses only a folder it already has a podcast in; the
		// same feed under another folder would be a second copy of the show
		var dup error
		if err := client.ItemsAll(ctx, lib.ID, abs.ItemsOptions{}, func(items []abs.Item) bool {
			for i := range items {
				have := strings.TrimSpace(items[i].Media.Metadata.FeedURL)
				if have != "" && (strings.EqualFold(have, strings.TrimSpace(in.FeedURL)) || strings.EqualFold(have, strings.TrimSpace(feed.Metadata.FeedURL))) {
					dup = fmt.Errorf("%s already subscribes to this feed as %q (%s): podcast_check_new fetches its new episodes", lib.Name, items[i].Title(), items[i].ID)
					return false
				}
			}
			return true
		}); err != nil {
			return nil, addOut{}, err
		}
		if dup != nil {
			return nil, addOut{}, dup
		}
		folder := in.Folder
		if folder == "" {
			folder = safeFolderName(feed.Metadata.Title)
		}

		np := abs.NewPodcast{LibraryID: lib.ID, FolderID: lib.Folders[0].ID, Path: path.Join(lib.Folders[0].FullPath, folder)}
		np.Media.AutoDownloadEpisodes = in.AutoDownload
		np.Media.Metadata = map[string]any{
			"title":         feed.Metadata.Title,
			"author":        feed.Metadata.Author,
			"description":   feed.Metadata.Description,
			"releaseDate":   feed.Metadata.PubDate,
			"genres":        feed.Metadata.Categories,
			"feedUrl":       feed.Metadata.FeedURL,
			"imageUrl":      feed.Metadata.Image,
			"itunesPageUrl": feed.Metadata.Link,
			"language":      feed.Metadata.Language,
			"explicit":      strings.EqualFold(feed.Metadata.Explicit, "yes") || feed.Metadata.Explicit == "true",
			"type":          feed.Metadata.Type,
		}
		if np.Media.Metadata["feedUrl"] == "" {
			np.Media.Metadata["feedUrl"] = in.FeedURL
		}

		it, err := client.CreatePodcast(ctx, np)
		if err != nil {
			return nil, addOut{}, err
		}
		out := addOut{itemSummary: summarize(it), FeedEpisodes: len(feed.Episodes)}

		// queued once the podcast exists: the server takes no episodes with
		// the create, and dropped the ones sent there without a word
		if in.Download > 0 && len(feed.Episodes) > 0 {
			newest := slices.Clone(feed.Episodes)
			slices.SortStableFunc(newest, func(a, b abs.FeedEpisode) int { return cmp.Compare(b.PublishedAt, a.PublishedAt) })
			newest = newest[:min(in.Download, len(newest))]
			if derr := client.DownloadEpisodes(ctx, it.ID, newest); derr != nil {
				out.Warning = "the podcast was added, but queueing its newest episodes failed: " + derr.Error()
			} else {
				for i := range newest {
					out.Queued = append(out.Queued, newest[i].Title)
				}
			}
		}

		return nil, out, nil
	})

	type settingsIn struct {
		itemRef
		AutoDownload *bool  `json:"auto_download,omitempty"`
		Schedule     string `json:"schedule,omitempty"      jsonschema:"cron expression for the automatic check, e.g. '0 * * * *'"`
		KeepEpisodes *int   `json:"keep_episodes,omitempty" jsonschema:"maximum episodes to keep, 0 for all"`
		NewPerCheck  *int   `json:"new_per_check,omitempty" jsonschema:"maximum new episodes to download per check"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "podcast_settings",
		Description: "Change a podcast's automatic download settings: on/off, schedule, how many episodes to keep and to fetch per check. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in settingsIn) (*mcp.CallToolResult, doneOut, error) {
		it, err := resolvePodcast(ctx, client, in.Library, in.Item)
		if err != nil {
			return nil, doneOut{}, err
		}
		upd := abs.MediaUpdate{
			AutoDownloadEpisodes:     in.AutoDownload,
			AutoDownloadSchedule:     strPtr(in.Schedule),
			MaxEpisodesToKeep:        in.KeepEpisodes,
			MaxNewEpisodesToDownload: in.NewPerCheck,
		}
		if upd.AutoDownloadEpisodes == nil && upd.AutoDownloadSchedule == nil && upd.MaxEpisodesToKeep == nil && upd.MaxNewEpisodesToDownload == nil {
			return nil, doneOut{}, errors.New("nothing to change")
		}
		updated, err := client.UpdateMedia(ctx, it.ID, upd)
		if err != nil {
			return nil, doneOut{}, err
		}

		return nil, doneOut{Done: updated}, nil
	})
}

// feedEpisodes lists a podcast's feed, filtered by title when given. The
// server only exposes a title search, so an empty title uses a feed parse.
func feedEpisodes(ctx context.Context, client *abs.Client, it *abs.Item, title string) ([]abs.FeedEpisode, error) {
	if strings.TrimSpace(title) != "" {
		return client.SearchFeedEpisodes(ctx, it.ID, title)
	}
	if it.Media.Metadata.FeedURL == "" {
		return nil, errors.New("podcast has no feed url")
	}
	feed, err := client.ParseFeed(ctx, it.Media.Metadata.FeedURL)
	if err != nil {
		return nil, err
	}
	return feed.Episodes, nil
}

// newestEpisodes is a podcast's episodes by publication, newest first. The
// server lists them in the order they were downloaded, so an episode fetched
// from the back catalogue would otherwise come ahead of this week's. One with
// no publish date goes by when it was added.
func newestEpisodes(eps []abs.Episode) []abs.Episode {
	out := slices.Clone(eps)
	when := func(e *abs.Episode) int64 {
		if e.PublishedAt > 0 {
			return e.PublishedAt
		}
		return e.AddedAt
	}
	slices.SortStableFunc(out, func(a, b abs.Episode) int { return cmp.Compare(when(&b), when(&a)) })
	return out
}

// heldEpisode is the episode a podcast already has for a feed entry, matched
// the way the server's own new-episode check matches: by guid, or by the
// audio url. Not by title, which an edit changes.
func heldEpisode(it *abs.Item, fe *abs.FeedEpisode) *abs.Episode {
	for i := range it.Media.Episodes {
		e := &it.Media.Episodes[i]
		if fe.GUID != "" && e.GUID == fe.GUID {
			return e
		}
		if fe.Enclosure != nil && e.Enclosure != nil && fe.Enclosure.URL != "" && strings.TrimSpace(e.Enclosure.URL) == strings.TrimSpace(fe.Enclosure.URL) {
			return e
		}
	}
	return nil
}

// queuedURLs are the audio urls of a podcast's episodes downloading or
// waiting to: the server drops a second request for one without saying so.
func queuedURLs(ctx context.Context, client *abs.Client, it *abs.Item) (map[string]bool, error) {
	current, queue, err := client.EpisodeDownloads(ctx, it.LibraryID)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, d := range append(queue, ptrValues(current)...) {
		if d.LibraryItemID == it.ID {
			out[strings.TrimSpace(d.URL)] = true
		}
	}
	return out, nil
}

// ptrValues is a pointer's value as a list of one, or none for nil.
func ptrValues[T any](p *T) []T {
	if p == nil {
		return nil
	}
	return []T{*p}
}

// safeFolderName strips characters that are unsafe in folder names.
func safeFolderName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|':
			b.WriteRune(' ')
		default:
			b.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}
