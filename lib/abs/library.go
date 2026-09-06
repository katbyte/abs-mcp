package abs

import (
	"context"
	"net/url"
	"strconv"
	"strings"
)

// Libraries lists the libraries visible to the API key's user.
func (c *Client) Libraries(ctx context.Context) ([]Library, error) {
	var resp struct {
		Libraries []Library `json:"libraries"`
	}
	if err := c.get(ctx, "/api/libraries", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Libraries, nil
}

// Library fetches one library by id.
func (c *Client) Library(ctx context.Context, id string) (*Library, error) {
	var l Library
	if err := c.get(ctx, "/api/libraries/"+url.PathEscape(id), nil, &l); err != nil {
		return nil, err
	}
	return &l, nil
}

// LibraryWithFilterData fetches a library together with its filter data
// (authors, genres, tags, series, narrators, ...) and issue count.
func (c *Client) LibraryWithFilterData(ctx context.Context, id string) (*Library, *FilterData, error) {
	q := url.Values{"include": {"filterdata"}}
	var resp struct {
		FilterData FilterData `json:"filterdata"`
		Issues     int        `json:"issues"`
		Library    Library    `json:"library"`
	}
	if err := c.get(ctx, "/api/libraries/"+url.PathEscape(id), q, &resp); err != nil {
		return nil, nil, err
	}
	resp.FilterData.NumIssues = resp.Issues
	return &resp.Library, &resp.FilterData, nil
}

// FilterData returns the distinct authors, genres, tags, series, narrators,
// languages and publishers in a library.
func (c *Client) FilterData(ctx context.Context, libraryID string) (*FilterData, error) {
	var fd FilterData
	if err := c.get(ctx, "/api/libraries/"+url.PathEscape(libraryID)+"/filterdata", nil, &fd); err != nil {
		return nil, err
	}
	return &fd, nil
}

// ItemsOptions selects a page of library items. Filter is the raw value for
// the `filter` parameter; build it with EncodeFilter.
type ItemsOptions struct {
	Limit          int
	Page           int
	Sort           string // e.g. media.metadata.title, media.metadata.authorName, media.metadata.publishedYear, media.duration, size, addedAt, birthtimeMs, mtimeMs, sequence, progress, random
	Desc           bool
	Filter         string
	Minified       bool
	CollapseSeries bool
	Include        string // comma-separated: rssfeed, numepisodesincomplete, share
}

// ItemsPage is one page of Items results.
type ItemsPage struct {
	Results []Item
	Total   int
	Limit   int
	Page    int
}

// Items lists items in a library. Total is the count of all matches, not
// just this page.
func (c *Client) Items(ctx context.Context, libraryID string, opts ItemsOptions) (*ItemsPage, error) {
	q := url.Values{}
	intQuery(q, "limit", opts.Limit)
	q.Set("page", strconv.Itoa(opts.Page))
	if opts.Sort != "" {
		q.Set("sort", opts.Sort)
	}
	if opts.Desc {
		q.Set("desc", "1")
	}
	if opts.Filter != "" {
		q.Set("filter", opts.Filter)
	}
	if opts.Minified {
		q.Set("minified", "1")
	}
	if opts.CollapseSeries {
		q.Set("collapseseries", "1")
	}
	if opts.Include != "" {
		q.Set("include", opts.Include)
	}

	var page ItemsPage
	var resp struct {
		Results []Item `json:"results"`
		Total   int    `json:"total"`
		Limit   int    `json:"limit"`
		Page    int    `json:"page"`
	}
	if err := c.get(ctx, "/api/libraries/"+url.PathEscape(libraryID)+"/items", q, &resp); err != nil {
		return nil, err
	}
	page.Results, page.Total, page.Limit, page.Page = resp.Results, resp.Total, resp.Limit, resp.Page
	return &page, nil
}

// sweepPageSize keeps each request bounded while walking a whole library.
const sweepPageSize = 500

// ItemsAll walks every item matching opts (limit/page are managed here),
// calling fn per page until it returns false or the library is exhausted.
func (c *Client) ItemsAll(ctx context.Context, libraryID string, opts ItemsOptions, fn func(items []Item) bool) error {
	opts.Limit = sweepPageSize
	opts.Minified = true
	for page := 0; ; page++ {
		opts.Page = page
		res, err := c.Items(ctx, libraryID, opts)
		if err != nil {
			return err
		}
		if len(res.Results) == 0 {
			return nil
		}
		if !fn(res.Results) {
			return nil
		}
		if (page+1)*sweepPageSize >= res.Total {
			return nil
		}
	}
}

// Search searches one library's titles, authors, series, narrators, tags and
// genres.
func (c *Client) Search(ctx context.Context, libraryID, query string, limit int) (*SearchResult, error) {
	q := url.Values{"q": {query}}
	intQuery(q, "limit", limit)
	var r SearchResult
	if err := c.get(ctx, "/api/libraries/"+url.PathEscape(libraryID)+"/search", q, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// LibraryStats returns totals plus top authors, genres, largest and longest
// items.
func (c *Client) LibraryStats(ctx context.Context, libraryID string) (*LibraryStats, error) {
	var s LibraryStats
	if err := c.get(ctx, "/api/libraries/"+url.PathEscape(libraryID)+"/stats", nil, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// ListOptions pages through authors or series listings.
type ListOptions struct {
	Limit  int
	Page   int
	Sort   string
	Desc   bool
	Filter string
}

// Authors lists a library's authors with their book counts. Sort: name,
// lastFirst, addedAt, updatedAt, numBooks.
func (c *Client) Authors(ctx context.Context, libraryID string, opts ListOptions) ([]Author, int, error) {
	q := url.Values{}
	intQuery(q, "limit", opts.Limit)
	q.Set("page", strconv.Itoa(opts.Page))
	if opts.Sort != "" {
		q.Set("sort", opts.Sort)
	}
	if opts.Desc {
		q.Set("desc", "1")
	}
	var resp struct {
		Results []Author `json:"results"`
		Total   int      `json:"total"`
		Authors []Author `json:"authors"` // pre-2.20 servers
	}
	if err := c.get(ctx, "/api/libraries/"+url.PathEscape(libraryID)+"/authors", q, &resp); err != nil {
		return nil, 0, err
	}
	if resp.Results == nil && resp.Authors != nil {
		return resp.Authors, len(resp.Authors), nil
	}
	return resp.Results, resp.Total, nil
}

// SeriesList lists a library's series, each with its books (minified). Sort:
// name, numBooks, totalDuration, addedAt, lastBookAdded, lastBookUpdated.
func (c *Client) SeriesList(ctx context.Context, libraryID string, opts ListOptions) ([]Series, int, error) {
	q := url.Values{}
	intQuery(q, "limit", opts.Limit)
	q.Set("page", strconv.Itoa(opts.Page))
	if opts.Sort != "" {
		q.Set("sort", opts.Sort)
	}
	if opts.Desc {
		q.Set("desc", "1")
	}
	if opts.Filter != "" {
		q.Set("filter", opts.Filter)
	}
	var resp struct {
		Results []Series `json:"results"`
		Total   int      `json:"total"`
	}
	if err := c.get(ctx, "/api/libraries/"+url.PathEscape(libraryID)+"/series", q, &resp); err != nil {
		return nil, 0, err
	}
	return resp.Results, resp.Total, nil
}

// Narrators lists a library's narrators with book counts.
func (c *Client) Narrators(ctx context.Context, libraryID string) ([]NarratorRow, error) {
	var resp struct {
		Narrators []NarratorRow `json:"narrators"`
	}
	if err := c.get(ctx, "/api/libraries/"+url.PathEscape(libraryID)+"/narrators", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Narrators, nil
}

// Collections lists collections, optionally restricted to one library.
func (c *Client) Collections(ctx context.Context, libraryID string) ([]Collection, error) {
	path := "/api/collections"
	if libraryID != "" {
		path = "/api/libraries/" + url.PathEscape(libraryID) + "/collections"
	}
	var resp struct {
		Collections []Collection `json:"collections"`
		Results     []Collection `json:"results"`
	}
	if err := c.get(ctx, path, nil, &resp); err != nil {
		return nil, err
	}
	if resp.Collections != nil {
		return resp.Collections, nil
	}
	return resp.Results, nil
}

// Playlists lists the API key user's playlists, optionally restricted to one
// library.
func (c *Client) Playlists(ctx context.Context, libraryID string) ([]Playlist, error) {
	path := "/api/playlists"
	if libraryID != "" {
		path = "/api/libraries/" + url.PathEscape(libraryID) + "/playlists"
	}
	var resp struct {
		Playlists []Playlist `json:"playlists"`
		Results   []Playlist `json:"results"`
	}
	if err := c.get(ctx, path, nil, &resp); err != nil {
		return nil, err
	}
	if resp.Playlists != nil {
		return resp.Playlists, nil
	}
	return resp.Results, nil
}

// RecentEpisodes lists the newest episodes across a podcast library.
func (c *Client) RecentEpisodes(ctx context.Context, libraryID string, limit, page int) ([]Episode, error) {
	q := url.Values{}
	intQuery(q, "limit", limit)
	q.Set("page", strconv.Itoa(page))
	var resp struct {
		Episodes []Episode `json:"episodes"`
	}
	if err := c.get(ctx, "/api/libraries/"+url.PathEscape(libraryID)+"/recent-episodes", q, &resp); err != nil {
		return nil, err
	}
	return resp.Episodes, nil
}

// Personalised returns the home-page shelves (continue listening, recently
// added, continue series, ...) for the API key's user.
func (c *Client) Personalised(ctx context.Context, libraryID string, limit int) ([]Shelf, error) {
	q := url.Values{}
	intQuery(q, "limit", limit)
	var shelves []Shelf
	if err := c.get(ctx, "/api/libraries/"+url.PathEscape(libraryID)+"/personalised", q, &shelves); err != nil {
		return nil, err
	}
	return shelves, nil
}

// EpisodeDownloads returns the podcast download queue for a library.
func (c *Client) EpisodeDownloads(ctx context.Context, libraryID string) (current *EpisodeDownload, queue []EpisodeDownload, err error) {
	var resp struct {
		CurrentDownload *EpisodeDownload  `json:"currentDownload"`
		Queue           []EpisodeDownload `json:"queue"`
	}
	if err := c.get(ctx, "/api/libraries/"+url.PathEscape(libraryID)+"/episode-downloads", nil, &resp); err != nil {
		return nil, nil, err
	}
	return resp.CurrentDownload, resp.Queue, nil
}

// ScanLibrary starts a library scan (admin only). The server responds before
// the scan finishes; watch Tasks for completion.
func (c *Client) ScanLibrary(ctx context.Context, libraryID string, force bool) error {
	q := url.Values{}
	if force {
		q.Set("force", "1")
	}
	return c.post(ctx, "/api/libraries/"+url.PathEscape(libraryID)+"/scan", q, nil, nil)
}

// MatchAll quick-matches every item in a library against its metadata
// provider (admin only, runs in the background).
func (c *Client) MatchAll(ctx context.Context, libraryID string) error {
	return c.get(ctx, "/api/libraries/"+url.PathEscape(libraryID)+"/matchall", nil, nil)
}

// RemoveIssues deletes every missing or invalid item record from a library
// (admin only). Files are untouched.
func (c *Client) RemoveIssues(ctx context.Context, libraryID string) error {
	return c.del(ctx, "/api/libraries/"+url.PathEscape(libraryID)+"/issues", nil)
}

// LibraryUpdate holds the editable library fields; nil pointers are left
// unchanged.
type LibraryUpdate struct {
	Name     *string        `json:"name,omitempty"`
	Provider *string        `json:"provider,omitempty"`
	Icon     *string        `json:"icon,omitempty"`
	Settings map[string]any `json:"settings,omitempty"`
}

// UpdateLibrary edits a library's name, provider, icon or settings (admin
// only).
func (c *Client) UpdateLibrary(ctx context.Context, libraryID string, upd LibraryUpdate) (*Library, error) {
	var l Library
	if err := c.patch(ctx, "/api/libraries/"+url.PathEscape(libraryID), upd, &l); err != nil {
		return nil, err
	}
	return &l, nil
}

// SplitCSV splits a comma-separated string into trimmed non-empty parts.
func SplitCSV(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
