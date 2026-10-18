//go:build acceptance

// The client calls that make Audiobookshelf reach outside itself: Audible,
// Audnexus, iTunes and RSS feeds. Audiobookshelf makes those requests, not us,
// so they are intercepted at the container's edge by go-kt's record/replay
// proxy (test/replayproxy) and replayed from testdata/cassettes.
//
//	make record-sdk   # re-record against the real providers
package abs_test

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/katbyte/abs-mcp/sdk/abs"
)

const feedURL = "https://feed.podbean.com/wtyppod/feed.xml"

func TestProviderMatch(t *testing.T) {
	ctx := skipUnlessLive(t)
	id := library(t)

	item := firstItems(ctx, id, 1)[0]

	if res := must(client.LibraryItemMatch(ctx, item.Id, abs.LibraryItemMatchRequest{Provider: "audible", Title: "Foundation", Author: "Isaac Asimov"})); res.LibraryItem == nil {
		t.Fatalf("the match answered no item: %+v", res)
	}

	// the batch forms of the same thing
	quick := abs.LibraryItemBatchQuickMatchRequest{LibraryItemIds: []abs.LibraryItemID{item.Id}, Options: &abs.LibraryItemBatchQuickMatchRequestOptions{Provider: "audible", OverrideCover: new(false), OverrideDetails: new(false)}}
	if err := errorOf(client.LibraryItemBatchQuickMatch(ctx, quick)); err != nil {
		t.Errorf("quick matching a batch: %v", err)
	}
	if err := errorOf(client.LibraryMatchAll(ctx, id)); err != nil {
		t.Errorf("matching the whole library: %v", err)
	}
}

func TestProviderAuthor(t *testing.T) {
	ctx := skipUnlessLive(t)
	id := library(t)

	authors := firstAuthors(ctx, id, 10)
	if len(authors) == 0 {
		t.Skip("no authors")
	}

	if err := errorOf(client.AuthorMatch(ctx, authors[0].Id, abs.AuthorMatchRequest{Q: "Isaac Asimov"})); err != nil {
		t.Errorf("matching the author: %v", err)
	}

	// an image url the server fetches for itself
	if err := errorOf(client.AuthorUploadImage(ctx, authors[0].Id, abs.AuthorUploadImageRequest{Url: "https://m.media-amazon.com/images/I/81Sr4Ms8SaL.jpg"})); err != nil {
		t.Errorf("setting the author's image from a url: %v", err)
	}
}

// The cover route takes an upload or a url, and the url goes as JSON.
func TestProviderCoverFromURL(t *testing.T) {
	ctx := skipUnlessLive(t)
	id := library(t)

	item := firstItems(ctx, id, 1)[0]
	body, err := json.Marshal(map[string]string{"url": "https://m.media-amazon.com/images/I/81Sr4Ms8SaL.jpg"})
	if err != nil {
		t.Fatal(err)
	}

	if err := errorOf(client.LibraryItemUploadCover(ctx, item.Id, bytes.NewReader(body), "application/json")); err != nil {
		t.Fatalf("setting the cover from a url: %v", err)
	}
	t.Cleanup(func() { _ = errorOf(client.LibraryItemRemoveCover(afterwards(t), item.Id)) })

	if _, _, err := coverSize(ctx, item.Id); err != nil {
		t.Errorf("measuring the cover after setting one from a url: %v", err)
	}
}

// The feed calls take any url, so pointing them at a real feed through the proxy is ordinary use rather than a stub.
func TestProviderFeeds(t *testing.T) {
	ctx := skipUnlessLive(t)
	id := podcastLibrary(t)

	feed := must(client.PodcastGetPodcastFeed(ctx, abs.PodcastGetPodcastFeedRequest{RssFeed: feedURL})).Podcast
	if feed == nil || len(feed.Episodes) == 0 {
		t.Fatal("the feed was read with no episodes")
	}
	author := ""
	if feed.Metadata != nil {
		author = feed.Metadata.Author
	}

	show := abs.PodcastCreateRequest{LibraryId: id, FolderId: folderOf(t, id), Path: "/podcasts/SDK Feed Podcast", Media: &abs.PodcastCreateRequestMedia{Metadata: &abs.PodcastCreateRequestMediaMetadata{Title: "SDK Feed Podcast", Author: author, FeedUrl: feedURL}}}
	created := must(client.PodcastCreate(ctx, show))
	t.Cleanup(func() { _ = errorOf(client.LibraryItemDelete(afterwards(t), created.Id)) })

	// an empty title is a 500 from the server, not an empty result
	if err := errorOf(client.PodcastFindEpisode(ctx, created.Id, abs.PodcastFindEpisodeOptions{Title: feed.Episodes[0].Title})); err != nil {
		t.Errorf("searching the feed's episodes: %v", err)
	}
	if err := errorOf(client.PodcastDownloadEpisodes(ctx, created.Id, feed.Episodes[:1])); err != nil {
		t.Errorf("downloading an episode: %v", err)
	}
	if err := errorOf(client.PodcastCheckNewEpisodes(ctx, created.Id, abs.PodcastCheckNewEpisodesOptions{Limit: new(1)})); err != nil {
		t.Errorf("checking for new episodes: %v", err)
	}
	if err := errorOf(client.PodcastQuickMatchEpisodes(ctx, created.Id)); err != nil {
		t.Errorf("matching the episodes: %v", err)
	}
}

func folderOf(t *testing.T, lib abs.LibraryID) abs.FolderID {
	t.Helper()

	folders := must(client.LibraryFindOne(t.Context(), lib)).Folders
	if len(folders) == 0 {
		t.Fatalf("library %s has no folders", lib)
	}

	return folders[0].Id
}
