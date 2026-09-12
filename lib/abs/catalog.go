package abs

import (
	"context"
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
