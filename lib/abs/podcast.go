package abs

import (
	"context"
	"net/url"
)

// Episode fetches one podcast episode.
func (c *Client) Episode(ctx context.Context, itemID, episodeID string) (*Episode, error) {
	var e Episode
	if err := c.get(ctx, "/api/podcasts/"+url.PathEscape(itemID)+"/episode/"+url.PathEscape(episodeID), nil, &e); err != nil {
		return nil, err
	}
	return &e, nil
}

// EpisodeUpdate holds editable episode fields; nil pointers are unchanged.
type EpisodeUpdate struct {
	Title       *string `json:"title,omitempty"`
	Subtitle    *string `json:"subtitle,omitempty"`
	Description *string `json:"description,omitempty"`
	PubDate     *string `json:"pubDate,omitempty"`
	Episode     *string `json:"episode,omitempty"`
	Season      *string `json:"season,omitempty"`
	EpisodeType *string `json:"episodeType,omitempty"` // full, trailer, bonus
}

// UpdateEpisode edits an episode's metadata.
func (c *Client) UpdateEpisode(ctx context.Context, itemID, episodeID string, upd EpisodeUpdate) (*Item, error) {
	var it Item
	if err := c.patch(ctx, "/api/podcasts/"+url.PathEscape(itemID)+"/episode/"+url.PathEscape(episodeID), upd, &it); err != nil {
		return nil, err
	}
	return &it, nil
}

// DeleteEpisode removes an episode; hard also deletes its audio file.
func (c *Client) DeleteEpisode(ctx context.Context, itemID, episodeID string, hard bool) error {
	q := url.Values{}
	if hard {
		q.Set("hard", "1")
	}
	return c.del(ctx, "/api/podcasts/"+url.PathEscape(itemID)+"/episode/"+url.PathEscape(episodeID), q)
}

// CheckNewEpisodes fetches the podcast's feed and downloads up to limit new
// episodes (admin only). Returns the episodes queued.
func (c *Client) CheckNewEpisodes(ctx context.Context, itemID string, limit int) ([]FeedEpisode, error) {
	q := url.Values{}
	intQuery(q, "limit", limit)
	var resp struct {
		Episodes []FeedEpisode `json:"episodes"`
	}
	if err := c.get(ctx, "/api/podcasts/"+url.PathEscape(itemID)+"/checknew", q, &resp); err != nil {
		return nil, err
	}
	return resp.Episodes, nil
}

// SearchFeedEpisodes finds episodes in the podcast's feed whose title
// matches, without downloading.
func (c *Client) SearchFeedEpisodes(ctx context.Context, itemID, title string) ([]FeedEpisode, error) {
	// each match is wrapped: {"episodes":[{"episode":{...}}]}. Decoding
	// straight into []FeedEpisode collides with the episode-number field,
	// which is a string, not the episode object.
	var resp struct {
		Episodes []struct {
			Episode FeedEpisode `json:"episode"`
		} `json:"episodes"`
	}
	if err := c.get(ctx, "/api/podcasts/"+url.PathEscape(itemID)+"/search-episode", url.Values{"title": {title}}, &resp); err != nil {
		return nil, err
	}

	out := make([]FeedEpisode, 0, len(resp.Episodes))
	for _, m := range resp.Episodes {
		out = append(out, m.Episode)
	}
	return out, nil
}

// DownloadEpisodes queues feed episodes (from SearchFeedEpisodes /
// ParseFeed) for download (admin only).
func (c *Client) DownloadEpisodes(ctx context.Context, itemID string, episodes []FeedEpisode) error {
	return c.post(ctx, "/api/podcasts/"+url.PathEscape(itemID)+"/download-episodes", nil, episodes, nil)
}

// PodcastDownloads lists downloads queued for one podcast.
func (c *Client) PodcastDownloads(ctx context.Context, itemID string) ([]EpisodeDownload, error) {
	var resp struct {
		Downloads []EpisodeDownload `json:"downloads"`
	}
	if err := c.get(ctx, "/api/podcasts/"+url.PathEscape(itemID)+"/downloads", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Downloads, nil
}

// ClearDownloadQueue drops every queued download for a podcast (admin only).
func (c *Client) ClearDownloadQueue(ctx context.Context, itemID string) error {
	return c.get(ctx, "/api/podcasts/"+url.PathEscape(itemID)+"/clear-queue", nil, nil)
}

// MatchEpisodes quick-matches every episode of a podcast against its feed to
// fill in missing metadata (admin only).
func (c *Client) MatchEpisodes(ctx context.Context, itemID string, override bool) (int, error) {
	q := url.Values{}
	if override {
		q.Set("override", "1")
	}
	var resp struct {
		NumEpisodesUpdated int `json:"numEpisodesUpdated"`
	}
	if err := c.post(ctx, "/api/podcasts/"+url.PathEscape(itemID)+"/match-episodes", q, nil, &resp); err != nil {
		return 0, err
	}
	return resp.NumEpisodesUpdated, nil
}

// SearchPodcasts searches iTunes for podcasts by name (country: us, gb, ...).
func (c *Client) SearchPodcasts(ctx context.Context, term, country string) ([]PodcastSearchResult, error) {
	q := url.Values{"term": {term}}
	if country != "" {
		q.Set("country", country)
	}
	var results []PodcastSearchResult
	if err := c.get(ctx, "/api/search/podcast", q, &results); err != nil {
		return nil, err
	}
	return results, nil
}

// ParseFeed fetches and parses a podcast RSS feed (admin only).
func (c *Client) ParseFeed(ctx context.Context, feedURL string) (*FeedPodcast, error) {
	var resp struct {
		Podcast FeedPodcast `json:"podcast"`
	}
	if err := c.post(ctx, "/api/podcasts/feed", nil, map[string]string{"rssFeed": feedURL}, &resp); err != nil {
		return nil, err
	}
	return &resp.Podcast, nil
}

// NewPodcast is the payload to add a podcast to a library.
type NewPodcast struct {
	LibraryID string `json:"libraryId"`
	FolderID  string `json:"folderId"`
	Path      string `json:"path"` // folder to create, inside the library folder
	Media     struct {
		Metadata             map[string]any `json:"metadata"`
		AutoDownloadEpisodes bool           `json:"autoDownloadEpisodes"`
	} `json:"media"`
}

// CreatePodcast adds a podcast from a parsed feed (admin only). It downloads
// nothing: older servers took episodesToDownload with the create, current
// ones ignore it, so queue episodes with DownloadEpisodes once it exists.
func (c *Client) CreatePodcast(ctx context.Context, p NewPodcast) (*Item, error) {
	var it Item
	if err := c.post(ctx, "/api/podcasts", nil, p, &it); err != nil {
		return nil, err
	}
	return &it, nil
}
