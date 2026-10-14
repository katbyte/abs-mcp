package abs

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// The endpoints that move bytes rather than JSON. Each returns the body
// unread, so a caller can stream a multi-gigabyte audiobook to disk without
// holding it in memory. Every one of these must be closed.

// DownloadItem streams an item's files as a zip, or the single file itself
// when the item has only one.
func (c *Client) DownloadItem(ctx context.Context, itemID string) (io.ReadCloser, error) {
	return c.stream(ctx, "/api/items/"+url.PathEscape(itemID)+"/download", nil)
}

// DownloadLibrary streams the named items from a library as a zip. The server
// requires the ids: there is no download-everything form.
func (c *Client) DownloadLibrary(ctx context.Context, libraryID string, itemIDs []string) (io.ReadCloser, error) {
	q := url.Values{"ids": {strings.Join(itemIDs, ",")}}
	return c.stream(ctx, "/api/libraries/"+url.PathEscape(libraryID)+"/download", q)
}

// ItemFile streams one file from an item, by the file id the item reports in
// LibraryFiles.
func (c *Client) ItemFile(ctx context.Context, itemID, fileID string) (io.ReadCloser, error) {
	return c.stream(ctx, "/api/items/"+url.PathEscape(itemID)+"/file/"+url.PathEscape(fileID), nil)
}

// ItemFileRange opens one file of an item at a byte range, the Range header
// passed through as given ("bytes=1000-"), so a reader that seeks - ffmpeg
// reading a file over http - fetches only the part it plays. The whole
// response comes back, since a reader of a range needs the 206's
// Content-Range; the caller closes the body. An empty rng reads the file.
func (c *Client) ItemFileRange(ctx context.Context, itemID, fileID, rng string) (*http.Response, error) {
	return c.open(ctx, "/api/items/"+url.PathEscape(itemID)+"/file/"+url.PathEscape(fileID), nil, rng)
}

// DownloadItemFile streams one file as an attachment rather than inline.
func (c *Client) DownloadItemFile(ctx context.Context, itemID, fileID string) (io.ReadCloser, error) {
	return c.stream(ctx, "/api/items/"+url.PathEscape(itemID)+"/file/"+url.PathEscape(fileID)+"/download", nil)
}

// Ebook streams an item's ebook file. An empty fileID takes the primary one.
func (c *Client) Ebook(ctx context.Context, itemID, fileID string) (io.ReadCloser, error) {
	path := "/api/items/" + url.PathEscape(itemID) + "/ebook"
	if fileID != "" {
		path += "/" + url.PathEscape(fileID)
	}
	return c.stream(ctx, path, nil)
}

// Cover streams an item's cover image. CoverSize reads only the header when
// all that is wanted is the dimensions.
func (c *Client) Cover(ctx context.Context, itemID string, width, height int, format string) (io.ReadCloser, error) {
	q := url.Values{}
	intQuery(q, "width", width)
	intQuery(q, "height", height)
	if format != "" {
		q.Set("format", format)
	}
	return c.stream(ctx, "/api/items/"+url.PathEscape(itemID)+"/cover", q)
}

// AuthorImage streams an author's photo.
func (c *Client) AuthorImage(ctx context.Context, authorID string, width, height int) (io.ReadCloser, error) {
	q := url.Values{}
	intQuery(q, "width", width)
	intQuery(q, "height", height)
	return c.stream(ctx, "/api/authors/"+url.PathEscape(authorID)+"/image", q)
}

// DeleteAuthorImage removes an author's photo and returns the author as they
// now stand. The server wraps the record in an author key, the way
// SetAuthorImage and MatchAuthor do.
func (c *Client) DeleteAuthorImage(ctx context.Context, authorID string) (*Author, error) {
	var resp struct {
		Author Author `json:"author"`
	}
	// not c.del: that discards the body, and the author is in it
	path := "/api/authors/" + url.PathEscape(authorID) + "/image"
	if err := c.do(ctx, http.MethodDelete, path, nil, nil, &resp); err != nil {
		return nil, err
	}
	return &resp.Author, nil
}

// DownloadBackup streams a backup file.
func (c *Client) DownloadBackup(ctx context.Context, id string) (io.ReadCloser, error) {
	return c.stream(ctx, "/api/backups/"+url.PathEscape(id)+"/download", nil)
}

// LibraryOPML streams a library's podcast subscriptions as an OPML document,
// which is what another podcast client imports.
func (c *Client) LibraryOPML(ctx context.Context, libraryID string) (io.ReadCloser, error) {
	return c.stream(ctx, "/api/libraries/"+url.PathEscape(libraryID)+"/opml", nil)
}

// FFProbe returns the raw ffprobe output for one audio file, which is what the
// scanner read when it decided the file's duration and codec.
func (c *Client) FFProbe(ctx context.Context, itemID, fileID string) (map[string]any, error) {
	var out map[string]any
	path := "/api/items/" + url.PathEscape(itemID) + "/ffprobe/" + url.PathEscape(fileID)
	if err := c.get(ctx, path, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Upload adds files to a library, creating the item folder. Each file is sent
// as its own part; the server places them under <library folder>/<path>.
func (c *Client) Upload(ctx context.Context, libraryID, folderID, title, author, series, filename string, content io.Reader) error {
	fields := map[string]string{
		"library": libraryID, "folder": folderID,
		"title": title, "author": author, "series": series,
	}
	return c.uploadMultipart(ctx, "/api/upload", "0", filename, content, fields)
}

// UploadBackup restores a backup file onto the server without applying it.
func (c *Client) UploadBackup(ctx context.Context, filename string, content io.Reader) error {
	return c.uploadMultipart(ctx, "/api/backups/upload", "file", filename, content, nil)
}
