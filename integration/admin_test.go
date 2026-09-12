//go:build integration

package integration

import (
	"testing"
	"time"

	"github.com/katbyte/abs-mcp/lib/abs"
)

// User administration: the whole account lifecycle against a real server,
// which is the only way to see the permissions payload as it really arrives.
func TestUserAdmin(t *testing.T) {
	ctx := skipUnlessLive(t)

	created := must(client.CreateUser(ctx, abs.UserCreate{
		Username: "sdk-user", Password: "sdk-password", Type: "user",
		Permissions: map[string]bool{"update": true, "download": true},
	}))
	if created.ID == "" || created.Username != "sdk-user" {
		t.Fatalf("created user = %+v", created)
	}
	t.Cleanup(func() { _ = client.DeleteUser(t.Context(), created.ID) })
	if created.Type != "user" {
		t.Errorf("type = %q, want user", created.Type)
	}

	email := "sdk@example.invalid"
	updated := must(client.UpdateUser(ctx, created.ID, abs.UserUpdate{Email: &email}))
	if updated.Email != email {
		t.Errorf("email = %q, want %q", updated.Email, email)
	}

	// the new account shows up in the listing
	var found bool
	for _, u := range must(client.Users(ctx, false)) {
		if u.ID == created.ID {
			found = true
		}
	}
	if !found {
		t.Error("the new user is not in Users")
	}

	if err := client.DeleteUser(ctx, created.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := client.User(ctx, created.ID); !abs.IsNotFound(err) {
		t.Errorf("the deleted user is still there: %v", err)
	}
}

// Backups: create one, list it, delete it. Applying a backup is deliberately
// not exercised, since it would replace the database the suite is running on.
func TestBackupAdmin(t *testing.T) {
	ctx := skipUnlessLive(t)

	made := must(client.CreateBackup(ctx))
	if len(made) == 0 {
		t.Fatal("CreateBackup returned nothing")
	}
	newest := made[len(made)-1]

	remaining, err := client.DeleteBackup(ctx, newest.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range remaining {
		if b.ID == newest.ID {
			t.Error("the deleted backup is still listed")
		}
	}

	// setting the path back to where it already is, so nothing moves
	_, location, err := client.Backups(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.SetBackupPath(ctx, location); err != nil {
		t.Errorf("SetBackupPath: %v", err)
	}
}

// The server-wide year in review, as opposed to the API key user's own.
func TestServerYearStats(t *testing.T) {
	ctx := skipUnlessLive(t)
	library(t)

	if _, err := client.ServerYearStats(ctx, time.Now().Year()); err != nil {
		t.Errorf("ServerYearStats: %v", err)
	}
}

// Deleting a tag drops it entirely, which is what a placeholder value needs
// rather than the merge RenameTag does.
func TestDeleteTagAndGenre(t *testing.T) {
	ctx := skipUnlessLive(t)
	id := library(t)

	item := must(client.Items(ctx, id, abs.ItemsOptions{Limit: 1})).Results[0]
	if _, err := client.UpdateMedia(ctx, item.ID, abs.MediaUpdate{
		Tags:     []string{"sdk-doomed-tag"},
		Metadata: &abs.MetadataUpdate{Genres: []string{"SDK Doomed Genre"}},
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = client.UpdateMedia(t.Context(), item.ID, abs.MediaUpdate{
			Tags: []string{"sdk"}, Metadata: &abs.MetadataUpdate{Genres: []string{"Science Fiction"}},
		})
	})

	n, err := client.DeleteTag(ctx, "sdk-doomed-tag")
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Error("DeleteTag reported no items updated")
	}
	for _, tag := range must(client.Tags(ctx)) {
		if tag == "sdk-doomed-tag" {
			t.Error("the tag survived deletion")
		}
	}

	if _, err := client.DeleteGenre(ctx, "SDK Doomed Genre"); err != nil {
		t.Errorf("DeleteGenre: %v", err)
	}
}

// RSS feeds: open one for an item, a collection and a series, then close them.
func TestFeeds(t *testing.T) {
	ctx := skipUnlessLive(t)
	id := library(t)

	const address = "http://localhost:13379"
	item := must(client.Items(ctx, id, abs.ItemsOptions{Limit: 2})).Results[0]

	feed := must(client.OpenItemFeed(ctx, item.ID, "sdk-item-feed", address))
	if feed.ID == "" {
		t.Fatalf("OpenItemFeed returned %+v", feed)
	}
	t.Cleanup(func() { _ = client.CloseFeed(t.Context(), feed.ID) })
	if feed.EntityType != "libraryItem" && feed.EntityType != "item" {
		t.Errorf("entityType = %q", feed.EntityType)
	}

	col := must(client.CreateCollection(ctx, id, "SDK Feed Collection", "", []string{item.ID}))
	t.Cleanup(func() { _ = client.DeleteCollection(t.Context(), col.ID) })
	colFeed := must(client.OpenCollectionFeed(ctx, col.ID, "sdk-collection-feed", address))
	t.Cleanup(func() { _ = client.CloseFeed(t.Context(), colFeed.ID) })

	series, _, err := client.SeriesList(ctx, id, abs.ListOptions{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(series) > 0 {
		seriesFeed := must(client.OpenSeriesFeed(ctx, series[0].ID, "sdk-series-feed", address))
		t.Cleanup(func() { _ = client.CloseFeed(t.Context(), seriesFeed.ID) })
	}

	open := must(client.Feeds(ctx))
	if len(open) < 2 {
		t.Errorf("Feeds returned %d, want at least the two just opened", len(open))
	}

	if err := client.CloseFeed(ctx, feed.ID); err != nil {
		t.Errorf("CloseFeed: %v", err)
	}
}
