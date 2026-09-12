package abs

import (
	"context"
	"net/url"
	"strconv"
)

// Progress returns the API key user's progress on an item (or a podcast
// episode). Nil when they have none.
func (c *Client) Progress(ctx context.Context, itemID, episodeID string) (*MediaProgress, error) {
	path := "/api/me/progress/" + url.PathEscape(itemID)
	if episodeID != "" {
		path += "/" + url.PathEscape(episodeID)
	}
	var p MediaProgress
	if err := c.get(ctx, path, nil, &p); err != nil {
		if IsNotFound(err) {
			return nil, nil //nolint:nilnil // no progress record is a normal state
		}
		return nil, err
	}
	return &p, nil
}

// ProgressUpdate sets progress fields; nil pointers are unchanged. Setting
// IsFinished true marks the item finished; CurrentTime is in seconds.
type ProgressUpdate struct {
	CurrentTime               *float64 `json:"currentTime,omitempty"`
	Duration                  *float64 `json:"duration,omitempty"`
	Progress                  *float64 `json:"progress,omitempty"` // 0..1
	IsFinished                *bool    `json:"isFinished,omitempty"`
	HideFromContinueListening *bool    `json:"hideFromContinueListening,omitempty"`
	EbookLocation             *string  `json:"ebookLocation,omitempty"`
	EbookProgress             *float64 `json:"ebookProgress,omitempty"`
}

// SetProgress creates or updates the API key user's progress on an item or
// episode.
func (c *Client) SetProgress(ctx context.Context, itemID, episodeID string, upd ProgressUpdate) error {
	path := "/api/me/progress/" + url.PathEscape(itemID)
	if episodeID != "" {
		path += "/" + url.PathEscape(episodeID)
	}
	return c.patch(ctx, path, upd, nil)
}

// RemoveProgress deletes a progress record by its progress id (from Progress
// or Me), resetting the item to not started.
func (c *Client) RemoveProgress(ctx context.Context, progressID string) error {
	return c.del(ctx, "/api/me/progress/"+url.PathEscape(progressID), nil)
}

// HideFromContinueListening removes an in-progress item from the continue
// listening shelf without changing its progress.
func (c *Client) HideFromContinueListening(ctx context.Context, progressID string) error {
	return c.get(ctx, "/api/me/progress/"+url.PathEscape(progressID)+"/remove-from-continue-listening", nil, nil)
}

// ItemsInProgress lists the items (and, for podcasts, the episode) the API
// key user is partway through, most recently updated first.
func (c *Client) ItemsInProgress(ctx context.Context, limit int) ([]Item, error) {
	q := url.Values{}
	intQuery(q, "limit", limit)
	var resp struct {
		LibraryItems []Item `json:"libraryItems"`
	}
	if err := c.get(ctx, "/api/me/items-in-progress", q, &resp); err != nil {
		return nil, err
	}
	return resp.LibraryItems, nil
}

// Bookmarks lists the API key user's bookmarks across all items.
func (c *Client) Bookmarks(ctx context.Context) ([]Bookmark, error) {
	// the server wraps these in an object, unlike most of the /api/me routes
	var resp struct {
		Bookmarks []Bookmark `json:"bookmarks"`
	}
	if err := c.get(ctx, "/api/me/bookmarks", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Bookmarks, nil
}

// CreateBookmark adds a bookmark at time (seconds) on an item.
func (c *Client) CreateBookmark(ctx context.Context, itemID string, seconds float64, title string) (*Bookmark, error) {
	var b Bookmark
	if err := c.post(ctx, "/api/me/item/"+url.PathEscape(itemID)+"/bookmark", nil, map[string]any{"time": seconds, "title": title}, &b); err != nil {
		return nil, err
	}
	return &b, nil
}

// UpdateBookmark renames the bookmark at time on an item.
func (c *Client) UpdateBookmark(ctx context.Context, itemID string, seconds float64, title string) (*Bookmark, error) {
	var b Bookmark
	if err := c.patch(ctx, "/api/me/item/"+url.PathEscape(itemID)+"/bookmark", map[string]any{"time": seconds, "title": title}, &b); err != nil {
		return nil, err
	}
	return &b, nil
}

// DeleteBookmark removes the bookmark at time (seconds) on an item.
func (c *Client) DeleteBookmark(ctx context.Context, itemID string, seconds float64) error {
	return c.del(ctx, "/api/me/item/"+url.PathEscape(itemID)+"/bookmark/"+strconv.FormatFloat(seconds, 'f', -1, 64), nil)
}

// ListeningSessions lists the API key user's listening sessions, newest
// first.
func (c *Client) ListeningSessions(ctx context.Context, itemsPerPage, page int) ([]Session, int, error) {
	q := url.Values{}
	intQuery(q, "itemsPerPage", itemsPerPage)
	q.Set("page", strconv.Itoa(page))
	var resp struct {
		Total    int       `json:"total"`
		Sessions []Session `json:"sessions"`
	}
	if err := c.get(ctx, "/api/me/listening-sessions", q, &resp); err != nil {
		return nil, 0, err
	}
	return resp.Sessions, resp.Total, nil
}

// ItemListeningSessions lists the API key user's sessions for one item or
// episode.
func (c *Client) ItemListeningSessions(ctx context.Context, itemID, episodeID string, itemsPerPage, page int) ([]Session, int, error) {
	path := "/api/me/item/listening-sessions/" + url.PathEscape(itemID)
	if episodeID != "" {
		path += "/" + url.PathEscape(episodeID)
	}
	q := url.Values{}
	intQuery(q, "itemsPerPage", itemsPerPage)
	q.Set("page", strconv.Itoa(page))
	var resp struct {
		Total    int       `json:"total"`
		Sessions []Session `json:"sessions"`
	}
	if err := c.get(ctx, path, q, &resp); err != nil {
		return nil, 0, err
	}
	return resp.Sessions, resp.Total, nil
}

// ListeningStats returns the API key user's total listening time, per-day
// and per-item breakdowns.
func (c *Client) ListeningStats(ctx context.Context) (*ListeningStats, error) {
	var s ListeningStats
	if err := c.get(ctx, "/api/me/listening-stats", nil, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// YearStats returns the API key user's year-in-review stats.
func (c *Client) YearStats(ctx context.Context, year int) (*YearStats, error) {
	var s YearStats
	if err := c.get(ctx, "/api/me/stats/year/"+strconv.Itoa(year), nil, &s); err != nil {
		return nil, err
	}
	return &s, nil
}
