package abs

import (
	"context"
	"net/http"
	"net/url"
)

// Author fetches one author; withItems attaches their library items.
func (c *Client) Author(ctx context.Context, id string, withItems bool) (*Author, error) {
	q := url.Values{}
	if withItems {
		q.Set("include", "items")
	}
	var a Author
	if err := c.get(ctx, "/api/authors/"+url.PathEscape(id), q, &a); err != nil {
		return nil, err
	}
	return &a, nil
}

// AuthorUpdate holds editable author fields; nil pointers are unchanged.
type AuthorUpdate struct {
	Name        *string `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
	ASIN        *string `json:"asin,omitempty"`
}

// UpdateAuthor edits an author. Renaming to an existing author's name merges
// the two; merged reports that.
func (c *Client) UpdateAuthor(ctx context.Context, id string, upd AuthorUpdate) (author *Author, merged bool, err error) {
	var resp struct {
		Author Author `json:"author"`
		Merged bool   `json:"merged"`
	}
	if err := c.patch(ctx, "/api/authors/"+url.PathEscape(id), upd, &resp); err != nil {
		return nil, false, err
	}
	return &resp.Author, resp.Merged, nil
}

// SetAuthorImage downloads imageURL on the server and makes it the author's
// photo. Requires the upload permission.
func (c *Client) SetAuthorImage(ctx context.Context, id, imageURL string) (*Author, error) {
	var resp struct {
		Author Author `json:"author"`
	}
	if err := c.post(ctx, "/api/authors/"+url.PathEscape(id)+"/image", nil, map[string]string{"url": imageURL}, &resp); err != nil {
		return nil, err
	}
	return &resp.Author, nil
}

// MatchAuthor looks the author up on Audnexus by name (or ASIN) and fills in
// their ASIN, description and image.
func (c *Client) MatchAuthor(ctx context.Context, id, name, asin, region string) (author *Author, updated bool, err error) {
	body := map[string]string{}
	if asin != "" {
		body["asin"] = asin
	} else {
		body["q"] = name
	}
	if region != "" {
		body["region"] = region
	}
	var resp struct {
		Updated bool   `json:"updated"`
		Author  Author `json:"author"`
	}
	if err := c.post(ctx, "/api/authors/"+url.PathEscape(id)+"/match", nil, body, &resp); err != nil {
		return nil, false, err
	}
	return &resp.Author, resp.Updated, nil
}

// DeleteAuthor removes an author record and unlinks them from their books
// (the books stay). Requires the delete permission.
func (c *Client) DeleteAuthor(ctx context.Context, id string) error {
	return c.del(ctx, "/api/authors/"+url.PathEscape(id), nil)
}

// Series fetches one series with the API key user's progress through it.
func (c *Client) Series(ctx context.Context, id string) (*Series, error) {
	q := url.Values{"include": {"progress"}}
	var s Series
	if err := c.get(ctx, "/api/series/"+url.PathEscape(id), q, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// SeriesUpdate holds editable series fields; nil pointers are unchanged.
type SeriesUpdate struct {
	Name        *string `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
}

// UpdateSeries renames or describes a series. Requires the update permission.
func (c *Client) UpdateSeries(ctx context.Context, id string, upd SeriesUpdate) (*Series, error) {
	var s Series
	if err := c.patch(ctx, "/api/series/"+url.PathEscape(id), upd, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// Collection fetches one collection with its books.
func (c *Client) Collection(ctx context.Context, id string) (*Collection, error) {
	var col Collection
	if err := c.get(ctx, "/api/collections/"+url.PathEscape(id), nil, &col); err != nil {
		return nil, err
	}
	return &col, nil
}

// CreateCollection creates a collection in a library, optionally with
// initial books.
func (c *Client) CreateCollection(ctx context.Context, libraryID, name, description string, bookIDs []string) (*Collection, error) {
	body := map[string]any{"libraryId": libraryID, "name": name}
	if description != "" {
		body["description"] = description
	}
	if len(bookIDs) > 0 {
		body["books"] = bookIDs
	}
	var col Collection
	if err := c.post(ctx, "/api/collections", nil, body, &col); err != nil {
		return nil, err
	}
	return &col, nil
}

// UpdateCollection renames or describes a collection; nil pointers are
// unchanged.
func (c *Client) UpdateCollection(ctx context.Context, id string, name, description *string) (*Collection, error) {
	body := map[string]any{}
	if name != nil {
		body["name"] = *name
	}
	if description != nil {
		body["description"] = *description
	}
	var col Collection
	if err := c.patch(ctx, "/api/collections/"+url.PathEscape(id), body, &col); err != nil {
		return nil, err
	}
	return &col, nil
}

// DeleteCollection deletes a collection (its books stay in the library).
func (c *Client) DeleteCollection(ctx context.Context, id string) error {
	return c.del(ctx, "/api/collections/"+url.PathEscape(id), nil)
}

// AddToCollection appends books to a collection.
func (c *Client) AddToCollection(ctx context.Context, id string, bookIDs []string) (*Collection, error) {
	var col Collection
	if err := c.post(ctx, "/api/collections/"+url.PathEscape(id)+"/batch/add", nil, map[string]any{"books": bookIDs}, &col); err != nil {
		return nil, err
	}
	return &col, nil
}

// RemoveFromCollection removes books from a collection.
func (c *Client) RemoveFromCollection(ctx context.Context, id string, bookIDs []string) (*Collection, error) {
	var col Collection
	if err := c.post(ctx, "/api/collections/"+url.PathEscape(id)+"/batch/remove", nil, map[string]any{"books": bookIDs}, &col); err != nil {
		return nil, err
	}
	return &col, nil
}

// Playlist fetches one of the API key user's playlists with its entries.
func (c *Client) Playlist(ctx context.Context, id string) (*Playlist, error) {
	var p Playlist
	if err := c.get(ctx, "/api/playlists/"+url.PathEscape(id), nil, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// PlaylistEntry identifies a book or a podcast episode for playlist calls.
type PlaylistEntry struct {
	LibraryItemID string `json:"libraryItemId"`
	EpisodeID     string `json:"episodeId,omitempty"`
}

// CreatePlaylist creates a playlist for the API key's user.
func (c *Client) CreatePlaylist(ctx context.Context, libraryID, name, description string, items []PlaylistEntry) (*Playlist, error) {
	body := map[string]any{"libraryId": libraryID, "name": name}
	if description != "" {
		body["description"] = description
	}
	if len(items) > 0 {
		body["items"] = items
	}
	var p Playlist
	if err := c.post(ctx, "/api/playlists", nil, body, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// CreatePlaylistFromCollection copies a collection's books into a new
// playlist.
func (c *Client) CreatePlaylistFromCollection(ctx context.Context, collectionID string) (*Playlist, error) {
	var p Playlist
	if err := c.post(ctx, "/api/playlists/collection/"+url.PathEscape(collectionID), nil, nil, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// UpdatePlaylist renames or describes a playlist; nil pointers are unchanged.
func (c *Client) UpdatePlaylist(ctx context.Context, id string, name, description *string) (*Playlist, error) {
	body := map[string]any{}
	if name != nil {
		body["name"] = *name
	}
	if description != nil {
		body["description"] = *description
	}
	var p Playlist
	if err := c.patch(ctx, "/api/playlists/"+url.PathEscape(id), body, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// DeletePlaylist deletes a playlist.
func (c *Client) DeletePlaylist(ctx context.Context, id string) error {
	return c.del(ctx, "/api/playlists/"+url.PathEscape(id), nil)
}

// AddToPlaylist appends entries to a playlist.
func (c *Client) AddToPlaylist(ctx context.Context, id string, items []PlaylistEntry) (*Playlist, error) {
	var p Playlist
	if err := c.post(ctx, "/api/playlists/"+url.PathEscape(id)+"/batch/add", nil, map[string]any{"items": items}, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// RemoveFromPlaylist removes entries from a playlist.
func (c *Client) RemoveFromPlaylist(ctx context.Context, id string, items []PlaylistEntry) (*Playlist, error) {
	var p Playlist
	if err := c.post(ctx, "/api/playlists/"+url.PathEscape(id)+"/batch/remove", nil, map[string]any{"items": items}, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// Feed is an RSS feed the server is publishing for an item, collection or
// series.
type Feed struct {
	ID         string `json:"id"`
	Slug       string `json:"slug"`
	EntityType string `json:"entityType"`
	EntityID   string `json:"entityId"`
	FeedURL    string `json:"feedUrl"`
	ItemID     string `json:"itemId"`
	Meta       struct {
		Title       string `json:"title"`
		Description string `json:"description"`
		Author      string `json:"author"`
		ImageURL    string `json:"imageUrl"`
	} `json:"meta"`
}

// Feeds lists the RSS feeds the server is currently publishing (admin only).
func (c *Client) Feeds(ctx context.Context) ([]Feed, error) {
	var resp struct {
		Feeds []Feed `json:"feeds"`
	}
	if err := c.get(ctx, "/api/feeds", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Feeds, nil
}

// OpenItemFeed starts publishing an RSS feed for one item. slug is the feed's
// path segment; serverAddress is the url the feed advertises, which has to be
// reachable by whatever reads it.
func (c *Client) OpenItemFeed(ctx context.Context, itemID, slug, serverAddress string) (*Feed, error) {
	return c.openFeed(ctx, "/api/feeds/item/"+url.PathEscape(itemID)+"/open", slug, serverAddress)
}

// OpenCollectionFeed publishes a feed for a whole collection.
func (c *Client) OpenCollectionFeed(ctx context.Context, collectionID, slug, serverAddress string) (*Feed, error) {
	return c.openFeed(ctx, "/api/feeds/collection/"+url.PathEscape(collectionID)+"/open", slug, serverAddress)
}

// OpenSeriesFeed publishes a feed for a whole series.
func (c *Client) OpenSeriesFeed(ctx context.Context, seriesID, slug, serverAddress string) (*Feed, error) {
	return c.openFeed(ctx, "/api/feeds/series/"+url.PathEscape(seriesID)+"/open", slug, serverAddress)
}

func (c *Client) openFeed(ctx context.Context, path, slug, serverAddress string) (*Feed, error) {
	var resp struct {
		Feed Feed `json:"feed"`
	}
	body := map[string]string{"slug": slug, "serverAddress": serverAddress}
	if err := c.post(ctx, path, nil, body, &resp); err != nil {
		return nil, err
	}
	return &resp.Feed, nil
}

// CloseFeed stops publishing a feed.
func (c *Client) CloseFeed(ctx context.Context, feedID string) error {
	return c.post(ctx, "/api/feeds/"+url.PathEscape(feedID)+"/close", nil, nil, nil)
}

// AddBookToCollection adds a single book. AddToCollection is the batch form.
func (c *Client) AddBookToCollection(ctx context.Context, id, bookID string) (*Collection, error) {
	var col Collection
	if err := c.post(ctx, "/api/collections/"+url.PathEscape(id)+"/book", nil, map[string]string{"id": bookID}, &col); err != nil {
		return nil, err
	}
	return &col, nil
}

// RemoveBookFromCollection removes a single book. RemoveFromCollection is the
// batch form.
func (c *Client) RemoveBookFromCollection(ctx context.Context, id, bookID string) (*Collection, error) {
	var col Collection
	path := "/api/collections/" + url.PathEscape(id) + "/book/" + url.PathEscape(bookID)
	if err := c.do(ctx, http.MethodDelete, path, nil, nil, &col); err != nil {
		return nil, err
	}
	return &col, nil
}

// AddItemToPlaylist appends a single entry. AddToPlaylist is the batch form.
func (c *Client) AddItemToPlaylist(ctx context.Context, id string, entry PlaylistEntry) (*Playlist, error) {
	var pl Playlist
	if err := c.post(ctx, "/api/playlists/"+url.PathEscape(id)+"/item", nil, entry, &pl); err != nil {
		return nil, err
	}
	return &pl, nil
}

// RemoveItemFromPlaylist removes a single entry. RemoveFromPlaylist is the
// batch form. episodeID may be empty for a book.
func (c *Client) RemoveItemFromPlaylist(ctx context.Context, id, itemID, episodeID string) (*Playlist, error) {
	path := "/api/playlists/" + url.PathEscape(id) + "/item/" + url.PathEscape(itemID)
	if episodeID != "" {
		path += "/" + url.PathEscape(episodeID)
	}
	var pl Playlist
	if err := c.do(ctx, http.MethodDelete, path, nil, nil, &pl); err != nil {
		return nil, err
	}
	return &pl, nil
}

// AuthorCandidate is what the provider knows about an author: the record a
// name lookup returns, before anything is applied to a library record.
type AuthorCandidate struct {
	ASIN        string `json:"asin"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Image       string `json:"image"`
}

// SearchAuthor looks a name up on the provider without applying anything,
// and returns nil when nobody close enough exists (the server answers null).
// The lookup is tolerant of small spelling differences, so the name that
// comes back is not necessarily the one asked for. MatchAuthor is the form
// that writes a result to a record.
func (c *Client) SearchAuthor(ctx context.Context, query string) (*AuthorCandidate, error) {
	q := url.Values{"q": {query}}
	var cand *AuthorCandidate
	if err := c.get(ctx, "/api/search/authors", q, &cand); err != nil {
		return nil, err
	}
	return cand, nil
}
