//go:build acceptance

package abs_test

import (
	"slices"
	"testing"

	"github.com/katbyte/abs-mcp/sdk/abs"
)

// User administration: the whole account lifecycle against a real server,
// which is the only way to see the permissions payload as it really arrives.
func TestUserAdmin(t *testing.T) {
	ctx := skipUnlessLive(t)

	created := must(client.UserCreate(ctx, abs.UserCreateRequest{Username: "sdk-user", Password: "sdk-password", Type: "user", Permissions: &abs.UserPermissions{Update: new(true), Download: new(true)}})).User
	if created == nil || created.Id == "" || created.Username != "sdk-user" {
		t.Fatalf("created user = %+v", created)
	}
	t.Cleanup(func() { _ = errorOf(client.UserDelete(afterwards(t), created.Id)) })
	if created.Type != "user" {
		t.Errorf("type = %q, want user", created.Type)
	}

	email := "sdk@example.invalid"
	if updated := must(client.UserUpdate(ctx, created.Id, abs.UserUpdateRequest{Email: email})).User; updated == nil || updated.Email != email {
		t.Errorf("the updated user = %+v, want email %q", updated, email)
	}

	listed := func(u abs.UserForBrowser) bool { return u.Id == created.Id }
	if !slices.ContainsFunc(must(client.UserFindAll(ctx)).Users, listed) {
		t.Error("the new user is not listed")
	}

	if err := errorOf(client.UserDelete(ctx, created.Id)); err != nil {
		t.Fatal(err)
	}
	if _, err := client.UserFindOne(ctx, created.Id); !abs.IsNotFound(err) {
		t.Errorf("the deleted user is still there: %v", err)
	}
}

// Backups: create one, list it, delete it. Applying a backup is deliberately
// not exercised, since it would replace the database the suite is running on.
func TestBackupAdmin(t *testing.T) {
	ctx := skipUnlessLive(t)

	before := must(client.BackupGetAll(ctx))
	if before.BackupLocation == "" {
		t.Error("no backup location")
	}
	made := must(client.BackupCreate(ctx)).Backups
	if len(made) != len(before.Backups)+1 {
		t.Fatalf("backups after making one = %d, want %d", len(made), len(before.Backups)+1)
	}
	newest := made[len(made)-1]

	remaining := must(client.BackupDelete(ctx, newest.Id)).Backups
	gone := func(b abs.Backup) bool { return b.Id == newest.Id }
	if slices.ContainsFunc(remaining, gone) {
		t.Error("the deleted backup is still listed")
	}

	// setting the path back to where it already is, so nothing moves
	if err := errorOf(client.BackupUpdatePath(ctx, abs.BackupUpdatePathRequest{Path: before.BackupLocation})); err != nil {
		t.Errorf("setting the backup path: %v", err)
	}
}

// Deleting a tag drops it entirely, which is what a placeholder value needs
// rather than the merge a rename does.
func TestDeleteTagAndGenre(t *testing.T) {
	ctx := skipUnlessLive(t)
	id := library(t)

	item := firstItems(ctx, id, 1)[0]
	if err := errorOf(client.LibraryItemUpdateMedia(ctx, item.Id, abs.MediaUpdate{Tags: []string{"sdk-doomed-tag"}, Metadata: &abs.MediaUpdateMetadata{Genres: []string{"SDK Doomed Genre"}}})); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = errorOf(client.LibraryItemUpdateMedia(afterwards(t), item.Id, abs.MediaUpdate{Tags: []string{"sdk"}, Metadata: &abs.MediaUpdateMetadata{Genres: []string{"Science Fiction"}}}))
	})

	deleted, err := record(client.MiscDeleteTag(ctx, abs.PathName("sdk-doomed-tag")))
	if err != nil {
		t.Fatal(err)
	}
	if deleted.NumItemsUpdated == 0 {
		t.Error("deleting the tag reported no items updated")
	}
	if slices.Contains(must(client.MiscGetAllTags(ctx)).Tags, "sdk-doomed-tag") {
		t.Error("the tag survived deletion")
	}

	if err := errorOf(client.MiscDeleteGenre(ctx, abs.PathName("SDK Doomed Genre"))); err != nil {
		t.Errorf("deleting the genre: %v", err)
	}
	if slices.Contains(must(client.MiscGetAllGenres(ctx)).Genres, "SDK Doomed Genre") {
		t.Error("the genre survived deletion")
	}
}

// RSS feeds: open one for an item, a collection and a series, then close them.
func TestFeeds(t *testing.T) {
	ctx := skipUnlessLive(t)
	id := library(t)

	const address = "http://localhost:13379"
	item := firstItems(ctx, id, 1)[0]

	feed := must(client.RSSFeedOpenRSSFeedForItem(ctx, item.Id, abs.RSSFeedOpenRSSFeedForItemRequest{Slug: "sdk-item-feed", ServerAddress: address})).Feed
	if feed == nil || feed.Id == "" {
		t.Fatalf("opening a feed for an item answered %+v", feed)
	}
	t.Cleanup(func() { _ = errorOf(client.RSSFeedCloseRSSFeed(afterwards(t), string(feed.Id))) })
	if feed.EntityType != "libraryItem" && feed.EntityType != "item" {
		t.Errorf("entityType = %q", feed.EntityType)
	}

	col := must(client.CollectionCreate(ctx, abs.CollectionCreateRequest{LibraryId: id, Name: "SDK Feed Collection", Books: bookIDs(item)}))
	t.Cleanup(func() { _ = errorOf(client.CollectionDelete(afterwards(t), col.Id)) })
	colFeed := must(client.RSSFeedOpenRSSFeedForCollection(ctx, col.Id, abs.RSSFeedOpenRSSFeedForCollectionRequest{Slug: "sdk-collection-feed", ServerAddress: address})).Feed
	if colFeed == nil {
		t.Fatal("opening a feed for a collection answered no feed")
	}
	t.Cleanup(func() { _ = errorOf(client.RSSFeedCloseRSSFeed(afterwards(t), string(colFeed.Id))) })

	if series := must(client.LibraryGetAllSeriesForLibrary(ctx, id, abs.LibraryGetAllSeriesForLibraryOptions{Limit: new(1)})).Results; len(series) > 0 {
		seriesFeed := must(client.RSSFeedOpenRSSFeedForSeries(ctx, series[0].Id, abs.RSSFeedOpenRSSFeedForSeriesRequest{Slug: "sdk-series-feed", ServerAddress: address})).Feed
		if seriesFeed == nil {
			t.Fatal("opening a feed for a series answered no feed")
		}
		t.Cleanup(func() { _ = errorOf(client.RSSFeedCloseRSSFeed(afterwards(t), string(seriesFeed.Id))) })
	}

	open := must(client.RSSFeedGetAll(ctx)).Feeds
	if len(open) < 2 {
		t.Errorf("%d feeds listed, want at least the two just opened", len(open))
	}
	// the list answers each feed whole where opening one answers it short, and an earlier decode lost the address from one of them
	for _, f := range open {
		if f.Id == feed.Id && (f.FeedUrl != feed.FeedUrl || f.FeedUrl == "" || f.Slug != "sdk-item-feed") {
			t.Errorf("listed as %+v, opened as %+v", f, feed)
		}
	}

	if err := errorOf(client.RSSFeedCloseRSSFeed(ctx, string(feed.Id))); err != nil {
		t.Errorf("closing the feed: %v", err)
	}
}
