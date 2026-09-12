package abs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/gif"  // registered for image.DecodeConfig
	_ "image/jpeg" // registered for image.DecodeConfig
	_ "image/png"  // registered for image.DecodeConfig
	"net/http"
	"net/url"
)

// Item fetches one library item in the expanded shape (audio files, chapters,
// tracks, episodes) with the API key user's progress attached.
func (c *Client) Item(ctx context.Context, id string) (*Item, error) {
	q := url.Values{"expanded": {"1"}, "include": {"progress"}}
	var it Item
	if err := c.get(ctx, "/api/items/"+url.PathEscape(id), q, &it); err != nil {
		return nil, err
	}
	return &it, nil
}

// ItemsBatch fetches several items by id in one request (expanded shape).
func (c *Client) ItemsBatch(ctx context.Context, ids []string) ([]Item, error) {
	var resp struct {
		LibraryItems []Item `json:"libraryItems"`
	}
	if err := c.post(ctx, "/api/items/batch/get", nil, map[string]any{"libraryItemIds": ids}, &resp); err != nil {
		return nil, err
	}
	return resp.LibraryItems, nil
}

// MetadataUpdate is the editable metadata for a book (or podcast: Title,
// Author, Description, Genres, Language, Explicit, FeedURL, ...). Nil pointers
// are left unchanged; a pointer to an empty string clears the field.
type MetadataUpdate struct {
	Title         *string     `json:"title,omitempty"`
	Subtitle      *string     `json:"subtitle,omitempty"`
	Authors       []NameRef   `json:"authors,omitempty"`   // books; {name} entries are created if new
	Narrators     []string    `json:"narrators,omitempty"` // books
	Series        []SeriesRef `json:"series,omitempty"`    // books; {name, sequence}
	Genres        []string    `json:"genres,omitempty"`
	PublishedYear *string     `json:"publishedYear,omitempty"`
	PublishedDate *string     `json:"publishedDate,omitempty"`
	Publisher     *string     `json:"publisher,omitempty"`
	Description   *string     `json:"description,omitempty"`
	ISBN          *string     `json:"isbn,omitempty"`
	ASIN          *string     `json:"asin,omitempty"`
	Language      *string     `json:"language,omitempty"`
	Explicit      *bool       `json:"explicit,omitempty"`
	Abridged      *bool       `json:"abridged,omitempty"`
	// podcast
	Author      *string `json:"author,omitempty"`
	FeedURL     *string `json:"feedUrl,omitempty"`
	ImageURL    *string `json:"imageUrl,omitempty"`
	ItunesID    *string `json:"itunesId,omitempty"`
	ReleaseDate *string `json:"releaseDate,omitempty"`
	PodcastType *string `json:"type,omitempty"`
}

// MediaUpdate is the PATCH /api/items/:id/media payload.
type MediaUpdate struct {
	Metadata *MetadataUpdate `json:"metadata,omitempty"`
	Tags     []string        `json:"tags,omitempty"`
	// podcast settings
	AutoDownloadEpisodes     *bool   `json:"autoDownloadEpisodes,omitempty"`
	AutoDownloadSchedule     *string `json:"autoDownloadSchedule,omitempty"`
	MaxEpisodesToKeep        *int    `json:"maxEpisodesToKeep,omitempty"`
	MaxNewEpisodesToDownload *int    `json:"maxNewEpisodesToDownload,omitempty"`
}

// UpdateMedia edits an item's metadata, tags or podcast settings. Requires
// the update permission.
func (c *Client) UpdateMedia(ctx context.Context, id string, upd MediaUpdate) (updated bool, err error) {
	var resp struct {
		Updated bool `json:"updated"`
	}
	if err := c.patch(ctx, "/api/items/"+url.PathEscape(id)+"/media", upd, &resp); err != nil {
		return false, err
	}
	return resp.Updated, nil
}

// BatchUpdate applies media updates to several items; each entry needs the
// item id. Returns the number of items changed.
func (c *Client) BatchUpdate(ctx context.Context, updates []BatchMediaUpdate) (int, error) {
	var resp struct {
		Updates int `json:"updates"`
	}
	if err := c.post(ctx, "/api/items/batch/update", nil, updates, &resp); err != nil {
		return 0, err
	}
	return resp.Updates, nil
}

// BatchMediaUpdate is one entry of BatchUpdate. The update must be nested
// under mediaPayload: the server reads up.mediaPayload.metadata without
// checking, so a flattened entry crashes it with an unhandled rejection
// rather than returning an error.
type BatchMediaUpdate struct {
	ID           string      `json:"id"`
	MediaPayload MediaUpdate `json:"mediaPayload"`
}

// MatchOptions steers a quick match against a metadata provider.
type MatchOptions struct {
	Provider        string `json:"provider,omitempty"` // audible, audible.uk, google, itunes, openlibrary, fantlab, audiobookcovers, custom-<id>...; default is the library's provider
	Title           string `json:"title,omitempty"`
	Author          string `json:"author,omitempty"`
	ISBN            string `json:"isbn,omitempty"`
	ASIN            string `json:"asin,omitempty"`
	OverrideCover   bool   `json:"overrideCover"`
	OverrideDetails bool   `json:"overrideDetails"`
}

// Match quick-matches one item: searches the provider (by asin/isbn if given,
// otherwise title/author) and applies the best hit. Requires the update
// permission.
func (c *Client) Match(ctx context.Context, id string, opts MatchOptions) (*MatchResult, error) {
	var r MatchResult
	if err := c.post(ctx, "/api/items/"+url.PathEscape(id)+"/match", nil, opts, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// BatchQuickMatch queues a quick match of several items (admin only, runs in
// the background).
func (c *Client) BatchQuickMatch(ctx context.Context, ids []string, provider string, overrideCover, overrideDetails bool) error {
	options := map[string]any{
		"overrideCover":   overrideCover,
		"overrideDetails": overrideDetails,
	}
	if provider != "" {
		options["provider"] = provider
	}
	body := map[string]any{"libraryItemIds": ids, "options": options}
	return c.post(ctx, "/api/items/batch/quickmatch", nil, body, nil)
}

// ScanItem rescans one item's folder (admin only). Result is NOTHING, ADDED,
// UPDATED, REMOVED or UPTODATE.
func (c *Client) ScanItem(ctx context.Context, id string) (string, error) {
	var resp struct {
		Result string `json:"result"`
	}
	if err := c.post(ctx, "/api/items/"+url.PathEscape(id)+"/scan", nil, nil, &resp); err != nil {
		return "", err
	}
	return resp.Result, nil
}

// DeleteItem removes an item from the library; hard also deletes its files
// from disk. Requires the delete permission.
func (c *Client) DeleteItem(ctx context.Context, id string, hard bool) error {
	q := url.Values{}
	if hard {
		q.Set("hard", "1")
	}
	return c.del(ctx, "/api/items/"+url.PathEscape(id), q)
}

// CoverSize returns the pixel dimensions of an item's cover without holding
// the image: only the header is decoded, and the bytes are discarded. Returns
// ErrNoCover when the item has none, and an unsupported-format error for
// anything the standard library cannot read (webp, avif).
func (c *Client) CoverSize(ctx context.Context, itemID string) (width, height int, err error) {
	raw, err := c.doRaw(ctx, http.MethodGet, "/api/items/"+url.PathEscape(itemID)+"/cover", nil, nil)
	if err != nil {
		if IsNotFound(err) {
			return 0, 0, ErrNoCover
		}
		return 0, 0, err
	}
	if len(raw) == 0 {
		return 0, 0, ErrNoCover
	}

	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return 0, 0, fmt.Errorf("decoding cover for %s: %w", itemID, err)
	}

	return cfg.Width, cfg.Height, nil
}

// ErrNoCover reports that an item has no cover image at all.
var ErrNoCover = errors.New("item has no cover")

// SetCoverFromURL downloads an image and sets it as the item's cover.
func (c *Client) SetCoverFromURL(ctx context.Context, id, imageURL string) error {
	return c.post(ctx, "/api/items/"+url.PathEscape(id)+"/cover", nil, map[string]string{"url": imageURL}, nil)
}

// SetCoverFromFile picks a file already inside the item's folder as its
// cover (path as reported in the item's library files).
func (c *Client) SetCoverFromFile(ctx context.Context, id, path string) error {
	return c.patch(ctx, "/api/items/"+url.PathEscape(id)+"/cover", map[string]string{"cover": path}, nil)
}

// RemoveCover deletes the item's cover image.
func (c *Client) RemoveCover(ctx context.Context, id string) error {
	return c.del(ctx, "/api/items/"+url.PathEscape(id)+"/cover", nil)
}

// SetChapters replaces a book's chapter list. Requires the update permission.
func (c *Client) SetChapters(ctx context.Context, id string, chapters []Chapter) (updated bool, err error) {
	var resp struct {
		Success bool `json:"success"`
		Updated bool `json:"updated"`
	}
	if err := c.post(ctx, "/api/items/"+url.PathEscape(id)+"/chapters", nil, map[string]any{"chapters": chapters}, &resp); err != nil {
		return false, err
	}
	return resp.Updated, nil
}

// EmbedMetadata queues writing the item's metadata (and optionally chapters)
// into its audio file tags (admin only). backup keeps a copy of the original
// files.
func (c *Client) EmbedMetadata(ctx context.Context, id string, forceEmbedChapters, backup bool) error {
	q := url.Values{}
	if forceEmbedChapters {
		q.Set("forceEmbedChapters", "1")
	}
	if backup {
		q.Set("backup", "1")
	}
	return c.post(ctx, "/api/tools/item/"+url.PathEscape(id)+"/embed-metadata", q, nil, nil)
}

// SearchBooks queries a metadata provider without changing anything. itemID
// (optional) lets the server use the item's existing identifiers to rank
// results.
func (c *Client) SearchBooks(ctx context.Context, provider, title, author, itemID string) ([]BookSearchResult, error) {
	q := url.Values{}
	if provider != "" {
		q.Set("provider", provider)
	}
	q.Set("title", title)
	if author != "" {
		q.Set("author", author)
	}
	if itemID != "" {
		q.Set("id", itemID)
	}
	var results []BookSearchResult
	if err := c.get(ctx, "/api/search/books", q, &results); err != nil {
		return nil, err
	}
	return results, nil
}

// SearchCovers returns candidate cover image URLs for a title/author.
func (c *Client) SearchCovers(ctx context.Context, provider, title, author string, podcast bool) ([]string, error) {
	q := url.Values{"title": {title}}
	if provider != "" {
		q.Set("provider", provider)
	}
	if author != "" {
		q.Set("author", author)
	}
	if podcast {
		q.Set("podcast", "1")
	}
	var resp struct {
		Results []string `json:"results"`
	}
	if err := c.get(ctx, "/api/search/covers", q, &resp); err != nil {
		return nil, err
	}
	return resp.Results, nil
}

// SearchChapters fetches chapter data for an ASIN from Audible (region: us,
// ca, uk, au, fr, de, jp, it, in, es).
func (c *Client) SearchChapters(ctx context.Context, asin, region string) ([]Chapter, error) {
	q := url.Values{"asin": {asin}}
	if region != "" {
		q.Set("region", region)
	}
	var resp struct {
		Error    string `json:"error"`
		Chapters []struct {
			StartOffsetMs int64  `json:"startOffsetMs"`
			LengthMs      int64  `json:"lengthMs"`
			Title         string `json:"title"`
		} `json:"chapters"`
	}
	if err := c.get(ctx, "/api/search/chapters", q, &resp); err != nil {
		return nil, err
	}
	if resp.Error != "" {
		return nil, &HTTPError{Method: "GET", Path: "/api/search/chapters", Status: 404, Body: resp.Error}
	}
	out := make([]Chapter, 0, len(resp.Chapters))
	for i, ch := range resp.Chapters {
		start := float64(ch.StartOffsetMs) / 1000
		out = append(out, Chapter{ID: i, Start: start, End: start + float64(ch.LengthMs)/1000, Title: ch.Title})
	}
	return out, nil
}
