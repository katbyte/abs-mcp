package tools

import (
	"cmp"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	api "github.com/katbyte/abs-mcp/sdk/abs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Feeds and share links let someone listen from outside the app: an RSS
// feed of a book, a series or a collection for a podcast player, or a public
// web page that plays one book. The server keeps a list of its feeds but none
// of its share links; a book's record carries its own.

// feedSlug is what a url's last part may be: the web app refuses anything
// else, and so does this.
var feedSlug = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// shareSlugLetters are what a share link's generated name is made of: like
// the web app's, random, because anyone holding the url can listen.
const shareSlugLetters = "abcdefghijklmnopqrstuvwxyz0123456789"

func randomSlug() string {
	b := make([]byte, 10)
	_, _ = rand.Read(b) // never fails; crypto/rand panics rather than return an error
	for i := range b {
		b[i] = shareSlugLetters[int(b[i])%len(shareSlugLetters)]
	}
	return string(b)
}

type feedRow struct {
	ID    string `json:"id"`
	For   string `json:"for"   jsonschema:"item, series or collection"`
	Title string `json:"title"`
	URL   string `json:"url"`
}

func feedRowOf(f *api.Feed, address string) feedRow {
	kind := f.EntityType
	if kind == "libraryItem" {
		kind = "item"
	}
	title := ""
	if f.Meta != nil {
		title = f.Meta.Title
	}
	return feedRow{ID: string(f.Id), For: kind, Title: title, URL: feedURL(address, feedPath(f))}
}

// feedPath is the url a feed keeps, which an older server wrote only in the feed's meta.
func feedPath(f *api.Feed) string {
	if f.Meta == nil {
		return f.FeedUrl
	}
	return cmp.Or(f.FeedUrl, f.Meta.FeedUrl)
}

// feeds are the RSS feeds the server is publishing.
func feeds(ctx context.Context, gen *api.Client) ([]api.Feed, error) {
	all, err := record(gen.RSSFeedGetAll(ctx))
	if err != nil {
		return nil, err
	}
	return all.Feeds, nil
}

// feedURL is a feed's url as a listener reaches it. The server keeps a path,
// /feed/<slug>, and builds each answer's links from the address the request
// came in on, so the address is put in front of it here.
func feedURL(address, u string) string {
	if strings.HasPrefix(u, "/") {
		return strings.TrimRight(address, "/") + u
	}
	return u
}

func registerFeedTools(r *registry) {
	gen := r.gen

	type listOut struct {
		Total      int       `json:"total"`
		Offset     int       `json:"offset"`
		NextOffset int       `json:"next_offset,omitempty" jsonschema:"pass back as offset for the next page; absent on the last"`
		Feeds      []feedRow `json:"feeds"                 jsonschema:"the RSS feeds the server is publishing"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "feed_list",
		Description: "The RSS feeds the server is publishing, each with what it is for and its url. Share links to single books are not listed, as the server keeps no list of them: feed_edit on the book says whether it has one. Admin only.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in pageIn) (*mcp.CallToolResult, listOut, error) {
		all, err := feeds(ctx, gen)
		if err != nil {
			return nil, listOut{}, err
		}
		limit, offset := pageArgs(in.Limit, in.Offset, 50)
		page, next := pageOf(all, limit, offset)
		out := listOut{Total: len(all), Offset: offset, NextOffset: next, Feeds: []feedRow{}}
		for i := range page {
			out.Feeds = append(out.Feeds, feedRowOf(&page[i], gen.Client.BaseURL))
		}
		return nil, out, nil
	})

	type editIn struct {
		Item         string `json:"item,omitempty"         jsonschema:"a book or podcast by id or whole title"`
		Series       string `json:"series,omitempty"       jsonschema:"a series by name or id: a feed of every book in it"`
		Collection   string `json:"collection,omitempty"   jsonschema:"a collection by name or id: a feed of every book in it"`
		Library      string `json:"library,omitempty"      jsonschema:"narrow a title or series lookup to one library by name or id"`
		Link         bool   `json:"link,omitempty"         jsonschema:"a public web page that plays one book in a browser, instead of an RSS feed; item only"`
		Close        bool   `json:"close,omitempty"        jsonschema:"close the feed, or with link the link, instead of opening one"`
		Slug         string `json:"slug,omitempty"         jsonschema:"the url's last part, lowercase letters, digits and dashes; default the feed's id, or for a link ten random characters, so the url cannot be guessed"`
		Address      string `json:"address,omitempty"      jsonschema:"the server's url as listeners reach it, which the url answered is built on; default the url abs-mcp reaches it by"`
		Downloadable bool   `json:"downloadable,omitempty" jsonschema:"with link: let whoever holds the link download the files too"`
		ExpiresDays  int    `json:"expires_days,omitempty" jsonschema:"with link: close it on its own after this many days; default never"`
	}
	type editOut struct {
		For     string `json:"for"               jsonschema:"the book, series or collection"`
		URL     string `json:"url,omitempty"     jsonschema:"what to give a podcast player, or for a link a browser"`
		Opened  bool   `json:"opened,omitempty"`
		Already bool   `json:"already,omitempty" jsonschema:"it was open, or closed, before this call; nothing changed"`
		Closed  bool   `json:"closed,omitempty"`
		Expires string `json:"expires,omitempty" jsonschema:"with link: when it closes on its own"`
	}
	add(r, writeTool, &mcp.Tool{
		Name: "feed_edit",
		Description: "Open or close an RSS feed of a book, a series or a collection, for listening in a podcast player; or with link, a public web page that plays one book in a browser. Anyone who has the url can listen, no account needed. " +
			"Opening one already open hands back its url. Admin only. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in editIn) (*mcp.CallToolResult, editOut, error) {
		named := 0
		for _, v := range []string{in.Item, in.Series, in.Collection} {
			if v != "" {
				named++
			}
		}
		switch {
		case named != 1:
			return nil, editOut{}, errors.New("name one of item, series or collection")
		case in.Link && in.Item == "":
			return nil, editOut{}, errors.New("a link plays one book: pass item")
		case !in.Link && (in.Downloadable || in.ExpiresDays != 0):
			return nil, editOut{}, errors.New("downloadable and expires_days are for a link; pass link")
		case in.Close && (in.Slug != "" || in.Address != "" || in.Downloadable || in.ExpiresDays != 0):
			return nil, editOut{}, errors.New("close takes only what to close")
		case in.ExpiresDays < 0:
			return nil, editOut{}, fmt.Errorf("expires_days %d is in the past", in.ExpiresDays)
		case in.Slug != "" && !feedSlug.MatchString(in.Slug):
			return nil, editOut{}, fmt.Errorf("slug %q: use lowercase letters, digits and dashes, starting with a letter or digit", in.Slug)
		}
		address := strings.TrimRight(cmp.Or(in.Address, gen.Client.BaseURL), "/")

		if in.Link {
			it, err := resolveItemToChange(ctx, gen, in.Library, in.Item)
			if err != nil {
				return nil, editOut{}, err
			}
			if it.IsPodcast() {
				return nil, editOut{}, fmt.Errorf("%q is a podcast: a link plays one book; open an RSS feed of the podcast instead", it.Title())
			}
			out := editOut{For: it.Title()}
			shared, err := record(gen.LibraryItemFindOne(ctx, it.Id, api.LibraryItemFindOneOptions{Expanded: "1", Include: "share"}))
			if err != nil {
				return nil, editOut{}, err
			}
			share := shared.MediaItemShare
			switch {
			case in.Close && share == nil:
				out.Already, out.Closed = true, true
				return nil, out, nil
			case in.Close:
				if _, err := gen.ShareDeleteMediaItemShare(ctx, share.Id); err != nil {
					return nil, editOut{}, err
				}
				out.Closed = true
				return nil, out, nil
			case share != nil:
				out.Already, out.URL, out.Expires = true, address+"/share/"+share.Slug, share.ExpiresAt
				return nil, out, nil
			}
			var expiresMs int64
			if in.ExpiresDays > 0 {
				expiresMs = time.Now().Add(time.Duration(in.ExpiresDays) * 24 * time.Hour).UnixMilli()
			}
			slug := cmp.Or(in.Slug, randomSlug())
			made, err := record(gen.ShareCreateMediaItemShare(ctx, api.ShareCreateMediaItemShareRequest{MediaItemId: it.Media.Id, MediaItemType: api.MediaItemTypeBook, Slug: slug, ExpiresAt: int(expiresMs), IsDownloadable: &in.Downloadable}))
			if err != nil {
				return nil, editOut{}, err
			}
			out.Opened, out.URL, out.Expires = true, address+"/share/"+made.Slug, made.ExpiresAt
			return nil, out, nil
		}

		// the feed is found by what it is for, so a second call does not
		// open a second feed of the same thing
		var kind, id, title string
		switch {
		case in.Item != "":
			it, err := resolveItemToChange(ctx, gen, in.Library, in.Item)
			if err != nil {
				return nil, editOut{}, err
			}
			kind, id, title = "item", string(it.Id), it.Title()
		case in.Series != "":
			s, err := resolveSeries(ctx, gen, in.Library, in.Series)
			if err != nil {
				return nil, editOut{}, err
			}
			kind, id, title = "series", string(s.Id), s.Name
		default:
			c, err := resolveCollection(ctx, gen, in.Collection)
			if err != nil {
				return nil, editOut{}, err
			}
			kind, id, title = "collection", string(c.Id), c.Name
		}
		out := editOut{For: title}
		all, err := feeds(ctx, gen)
		if err != nil {
			return nil, editOut{}, err
		}
		var open []api.Feed
		for _, f := range all {
			if f.EntityId == id {
				open = append(open, f)
			}
		}

		if in.Close {
			if len(open) == 0 {
				out.Already, out.Closed = true, true
				return nil, out, nil
			}
			for _, f := range open {
				if _, err := gen.RSSFeedCloseRSSFeed(ctx, string(f.Id)); err != nil {
					return nil, editOut{}, err
				}
			}
			out.Closed = true
			return nil, out, nil
		}
		if len(open) > 0 {
			out.Already, out.URL = true, feedURL(address, feedPath(&open[0]))
			return nil, out, nil
		}
		slug := cmp.Or(in.Slug, id)
		if slices.ContainsFunc(all, func(f api.Feed) bool { return string(f.Id) == slug || f.Slug == slug }) {
			return nil, editOut{}, fmt.Errorf("another feed already has the slug %q: pass another", slug)
		}
		var feed *api.FeedMinified
		switch kind {
		case "item":
			var got api.RSSFeedOpenRSSFeedForItemAnswer
			got, err = record(gen.RSSFeedOpenRSSFeedForItem(ctx, api.LibraryItemID(id), api.RSSFeedOpenRSSFeedForItemRequest{Slug: slug, ServerAddress: address}))
			feed = got.Feed
		case "series":
			var got api.RSSFeedOpenRSSFeedForSeriesAnswer
			got, err = record(gen.RSSFeedOpenRSSFeedForSeries(ctx, api.SeriesID(id), api.RSSFeedOpenRSSFeedForSeriesRequest{Slug: slug, ServerAddress: address}))
			feed = got.Feed
		default:
			var got api.RSSFeedOpenRSSFeedForCollectionAnswer
			got, err = record(gen.RSSFeedOpenRSSFeedForCollection(ctx, api.CollectionID(id), api.RSSFeedOpenRSSFeedForCollectionRequest{Slug: slug, ServerAddress: address}))
			feed = got.Feed
		}
		if err != nil {
			return nil, editOut{}, err
		}
		if feed == nil {
			return nil, editOut{}, fmt.Errorf("the server opened a feed of %q but answered without it: feed_list shows its url", title)
		}
		out.Opened, out.URL = true, feedURL(address, feed.FeedUrl)
		return nil, out, nil
	})
}
