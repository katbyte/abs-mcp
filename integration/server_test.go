//go:build integration

package integration

import (
	"strings"
	"testing"
	"time"

	"github.com/katbyte/abs-mcp/sdk/abs"
)

// --- connectivity and identity ------------------------------------------

func TestStatusAndMe(t *testing.T) {
	ctx := skipUnlessLive(t)

	status := must(client.Status(ctx))
	if status.ServerVersion == "" {
		t.Errorf("no serverVersion: %+v", status)
	}
	if !status.IsInit {
		t.Error("server reports itself uninitialized")
	}

	me := must(client.Me(ctx))
	if me.Username != "root" || me.Type != "root" {
		t.Errorf("me = %s/%s, want root/root", me.Username, me.Type)
	}
	if !me.Permissions.Update || !me.Permissions.Delete {
		t.Errorf("root permissions did not decode: %+v", me.Permissions)
	}
}

// Providers decoded to nothing before this suite existed: the server wraps the
// lists in {"providers":{"books":[{value,text}]}}, not {"book":[...]}.
func TestProviders(t *testing.T) {
	ctx := skipUnlessLive(t)

	book, podcast, err := client.Providers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(book) == 0 {
		t.Error("no book providers decoded")
	}
	if len(podcast) == 0 {
		t.Error("no podcast providers decoded")
	}
	var audible bool
	for _, p := range book {
		if p == "audible" {
			audible = true
		}
	}
	if !audible {
		t.Errorf("book providers = %v, want audible among them", book)
	}
}

// --- server-wide --------------------------------------------------------

func TestServerReads(t *testing.T) {
	ctx := skipUnlessLive(t)
	library(t)

	stats := must(client.ServerStats(ctx))
	if stats.Books.NumItems == 0 {
		t.Errorf("server stats book count = %d", stats.Books.NumItems)
	}
	if stats.Total.TotalSize == 0 {
		t.Error("server stats reported zero total size")
	}

	users := must(client.Users(ctx, true))
	if len(users) == 0 {
		t.Error("Users returned nothing")
	}

	// UsersOnline and Sessions had no caller before this suite
	if _, _, err := client.UsersOnline(ctx); err != nil {
		t.Errorf("UsersOnline: %v", err)
	}
	if _, _, err := client.Sessions(ctx, abs.SessionsOptions{}); err != nil {
		t.Errorf("Sessions: %v", err)
	}
	if _, err := client.OpenSessions(ctx); err != nil {
		t.Errorf("OpenSessions: %v", err)
	}
	if _, err := client.Tasks(ctx); err != nil {
		t.Errorf("Tasks: %v", err)
	}

	backups, location, err := client.Backups(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if location == "" {
		t.Error("no backup location")
	}
	before := len(backups)
	after := must(client.CreateBackup(ctx))
	if len(after) != before+1 {
		t.Errorf("backups after create = %d, want %d", len(after), before+1)
	}
}

func TestSearch(t *testing.T) {
	ctx := skipUnlessLive(t)
	id := library(t)

	res := must(client.Search(ctx, id, "foundation", 10))
	if len(res.Book) == 0 {
		t.Errorf("search for foundation found nothing: %+v", res)
	}
}

// --- error handling -----------------------------------------------------

func TestNotFound(t *testing.T) {
	ctx := skipUnlessLive(t)

	_, err := client.Item(ctx, "00000000-0000-0000-0000-000000000000")
	if err == nil {
		t.Fatal("expected an error for a missing item")
	}
	if !abs.IsNotFound(err) {
		t.Errorf("IsNotFound = false for %v", err)
	}
}

func TestBadToken(t *testing.T) {
	if client == nil {
		t.Skip("not live")
	}

	bad, err := abs.New(client.BaseURL(), "not-a-real-key")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bad.Me(t.Context()); err == nil {
		t.Fatal("expected an error with a bad key")
	} else if !strings.Contains(err.Error(), "401") && !strings.Contains(err.Error(), "403") {
		t.Errorf("error for a bad key = %v, want a 401 or 403", err)
	}
}

func TestStatsAndSessionMethods(t *testing.T) {
	ctx := skipUnlessLive(t)
	id := library(t)

	if _, err := client.LibraryStats(ctx, id); err != nil {
		t.Errorf("LibraryStats: %v", err)
	}
	if _, _, err := client.LibraryWithFilterData(ctx, id); err != nil {
		t.Errorf("LibraryWithFilterData: %v", err)
	}
	if _, err := client.ListeningStats(ctx); err != nil {
		t.Errorf("ListeningStats: %v", err)
	}
	if _, err := client.YearStats(ctx, time.Now().Year()); err != nil {
		t.Errorf("YearStats: %v", err)
	}
	if _, _, err := client.ListeningSessions(ctx, 10, 0); err != nil {
		t.Errorf("ListeningSessions: %v", err)
	}

	item := must(client.Items(ctx, id, abs.ItemsOptions{Limit: 1})).Results[0]
	if _, _, err := client.ItemListeningSessions(ctx, item.ID, "", 10, 0); err != nil {
		t.Errorf("ItemListeningSessions: %v", err)
	}

	me := must(client.Me(ctx))
	if _, err := client.User(ctx, me.ID); err != nil {
		t.Errorf("User: %v", err)
	}
	if _, _, err := client.UserSessions(ctx, me.ID, 10, 0); err != nil {
		t.Errorf("UserSessions: %v", err)
	}
	if _, err := client.UserListeningStats(ctx, me.ID); err != nil {
		t.Errorf("UserListeningStats: %v", err)
	}
	if _, err := client.Personalized(ctx, id, 5); err != nil {
		t.Errorf("Personalized: %v", err)
	}
}

// EmbedPending reads the task list with the queue beside it, which the
// server sends only when asked: an embed waiting its turn is in the queue and
// not among the tasks. An embed of a one-second file is over too soon to be
// sure of seeing it pending, so what is asserted is that the answer decodes,
// and that an embed that was started comes to an end.
func TestEmbedPending(t *testing.T) {
	ctx := skipUnlessLive(t)
	id := library(t)

	item := must(client.Items(ctx, id, abs.ItemsOptions{Limit: 1})).Results[0]
	settled := func(what string) {
		t.Helper()

		deadline := time.Now().Add(30 * time.Second)
		for {
			pending, err := client.EmbedPending(ctx, item.ID)
			if err != nil {
				t.Fatalf("EmbedPending %s: %v", what, err)
			}
			if !pending {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("EmbedPending %s: still pending after 30s", what)
			}
			time.Sleep(200 * time.Millisecond)
		}
	}

	settled("before an embed")
	if err := client.EmbedMetadata(ctx, item.ID, true, false); err != nil {
		t.Fatalf("EmbedMetadata: %v", err)
	}
	settled("after an embed")

	if pending, err := client.EmbedPending(ctx, "no-such-item"); err != nil || pending {
		t.Errorf("an item that does not exist = %v, %v; want not pending", pending, err)
	}
}
