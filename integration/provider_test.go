//go:build integration

// The client calls that make Audiobookshelf reach outside itself: Audible,
// Audnexus, iTunes and RSS feeds. Audiobookshelf makes those requests, not us,
// so they are intercepted at the container's edge by lib/providerproxy and
// replayed from testdata/cassettes.
//
//	make record-sdk   # re-record against the real providers
package integration

import (
	"testing"

	"github.com/katbyte/abs-mcp/lib/abs"
)

const feedURL = "https://feed.podbean.com/wtyppod/feed.xml"

func TestProviderSearch(t *testing.T) {
	ctx := skipUnlessLive(t)
	library(t)

	books := must(client.SearchBooks(ctx, "audible", "Foundation", "Isaac Asimov", ""))
	if len(books) == 0 {
		t.Fatal("SearchBooks found nothing")
	}
	if books[0].Title == "" {
		t.Errorf("the first result did not decode: %+v", books[0])
	}

	covers := must(client.SearchCovers(ctx, "audible", "Foundation", "Isaac Asimov", false))
	if len(covers) == 0 {
		t.Error("SearchCovers found nothing")
	}

	pods := must(client.SearchPodcasts(ctx, "Well There's Your Problem", ""))
	if len(pods) == 0 {
		t.Error("SearchPodcasts found nothing")
	}

	// chapters come from Audnexus, keyed by asin
	if books[0].ASIN != "" {
		if _, err := client.SearchChapters(ctx, books[0].ASIN, ""); err != nil {
			t.Errorf("SearchChapters(%s): %v", books[0].ASIN, err)
		}
	}
}

func TestProviderMatch(t *testing.T) {
	ctx := skipUnlessLive(t)
	id := library(t)

	item := must(client.Items(ctx, id, abs.ItemsOptions{Limit: 1})).Results[0]

	res, err := client.Match(ctx, item.ID, abs.MatchOptions{
		Provider: "audible", Title: "Foundation", Author: "Isaac Asimov",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res == nil {
		t.Fatal("Match returned nothing")
	}

	// the batch forms of the same thing
	if err := client.BatchQuickMatch(ctx, []string{item.ID}, "audible", false, false); err != nil {
		t.Errorf("BatchQuickMatch: %v", err)
	}
	if err := client.MatchAll(ctx, id); err != nil {
		t.Errorf("MatchAll: %v", err)
	}
}

func TestProviderAuthor(t *testing.T) {
	ctx := skipUnlessLive(t)
	id := library(t)

	authors, _, err := client.Authors(ctx, id, abs.ListOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(authors) == 0 {
		t.Skip("no authors")
	}

	if _, _, err := client.MatchAuthor(ctx, authors[0].ID, "Isaac Asimov", "", ""); err != nil {
		t.Errorf("MatchAuthor: %v", err)
	}

	// an image url the server fetches for itself
	const image = "https://m.media-amazon.com/images/I/81Sr4Ms8SaL.jpg"
	if _, err := client.SetAuthorImage(ctx, authors[0].ID, image); err != nil {
		t.Errorf("SetAuthorImage: %v", err)
	}
}

func TestProviderCoverFromURL(t *testing.T) {
	ctx := skipUnlessLive(t)
	id := library(t)

	item := must(client.Items(ctx, id, abs.ItemsOptions{Limit: 1})).Results[0]
	const cover = "https://m.media-amazon.com/images/I/81Sr4Ms8SaL.jpg"

	if err := client.SetCoverFromURL(ctx, item.ID, cover); err != nil {
		t.Fatalf("SetCoverFromURL: %v", err)
	}
	t.Cleanup(func() { _ = client.RemoveCover(t.Context(), item.ID) })

	if _, _, err := client.CoverSize(ctx, item.ID); err != nil {
		t.Errorf("CoverSize after setting one from a url: %v", err)
	}
}

// The feed methods take an arbitrary url, so pointing them at a real feed
// through the proxy is ordinary use rather than a stub.
func TestProviderFeeds(t *testing.T) {
	ctx := skipUnlessLive(t)
	id := podcastLibrary(t)

	feed := must(client.ParseFeed(ctx, feedURL))
	if feed == nil || len(feed.Episodes) == 0 {
		t.Fatal("ParseFeed returned no episodes")
	}

	newPodcast := abs.NewPodcast{
		LibraryID: id, FolderID: folderOf(t, id),
		Path: "/podcasts/SDK Feed Podcast",
	}
	newPodcast.Media.Metadata = map[string]any{
		"title":   "SDK Feed Podcast",
		"author":  feed.Metadata.Author,
		"feedUrl": feedURL,
	}
	created := must(client.CreatePodcast(ctx, newPodcast))
	t.Cleanup(func() { _ = client.DeleteItem(t.Context(), created.ID, false) })

	// an empty title is a 500 from the server, not an empty result
	if _, err := client.SearchFeedEpisodes(ctx, created.ID, feed.Episodes[0].Title); err != nil {
		t.Errorf("SearchFeedEpisodes: %v", err)
	}
	if err := client.DownloadEpisodes(ctx, created.ID, feed.Episodes[:1]); err != nil {
		t.Errorf("DownloadEpisodes: %v", err)
	}
	if _, err := client.CheckNewEpisodes(ctx, created.ID, 1); err != nil {
		t.Errorf("CheckNewEpisodes: %v", err)
	}
	if _, err := client.MatchEpisodes(ctx, created.ID, false); err != nil {
		t.Errorf("MatchEpisodes: %v", err)
	}
}

func folderOf(t *testing.T, libraryID string) string {
	t.Helper()

	lib := must(client.Library(t.Context(), libraryID))
	if len(lib.Folders) == 0 {
		t.Fatalf("library %s has no folders", libraryID)
	}

	return lib.Folders[0].ID
}
