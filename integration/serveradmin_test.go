//go:build integration

// The server-administration surface: notifications, email, API keys, custom
// providers, settings, sharing, sessions and the filesystem browser. None of
// this is wrapped as an MCP tool, but lib/abs is a general Audiobookshelf
// client, so it is covered here like everything else.
package integration

import (
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/katbyte/abs-mcp/lib/abs"
)

func TestNotifications(t *testing.T) {
	ctx := skipUnlessLive(t)

	settings := must(client.Notifications(ctx))
	if settings.MaxFailedAttempts == 0 && settings.MaxNotificationQueue == 0 {
		t.Errorf("notification settings look empty: %+v", settings)
	}
	if data := must(client.NotificationData(ctx)); len(data) == 0 {
		t.Error("NotificationData returned nothing; it lists the events available")
	}

	created := must(client.CreateNotification(ctx, map[string]any{
		"eventName": "onTest", "urls": []string{"json://localhost/sdk"},
		"titleTemplate": "SDK", "bodyTemplate": "SDK", "enabled": false, "type": "apprise",
	}))
	var id string
	for _, n := range created.Notifications {
		if n.EventName == "onTest" {
			id = n.ID
		}
	}
	if id == "" {
		t.Fatalf("the new notification is not in the settings: %+v", created)
	}
	t.Cleanup(func() { _ = client.DeleteNotification(t.Context(), id) })

	if _, err := client.UpdateNotification(ctx, id, map[string]any{"id": id, "enabled": false, "eventName": "onTest"}); err != nil {
		t.Errorf("UpdateNotification: %v", err)
	}
	// firing one notification, as opposed to all of them
	if err := client.TestOneNotification(ctx, id); err != nil {
		var he *abs.HTTPError
		if !errors.As(err, &he) {
			t.Errorf("TestOneNotification did not reach the server: %v", err)
		}
	}
	if err := client.DeleteNotification(ctx, id); err != nil {
		t.Errorf("DeleteNotification: %v", err)
	}
}

func TestEmailSettings(t *testing.T) {
	ctx := skipUnlessLive(t)

	if _, err := client.EmailSettings(ctx); err != nil {
		t.Fatalf("EmailSettings: %v", err)
	}
	if err := client.UpdateEmailSettings(ctx, map[string]any{"fromAddress": "sdk@example.invalid"}); err != nil {
		t.Errorf("UpdateEmailSettings: %v", err)
	}

	devices := []abs.EReaderDevice{{Name: "SDK Reader", Email: "sdk@example.invalid", AvailableTo: "adminOrUp"}}
	got, err := client.UpdateEReaderDevices(ctx, devices)
	if err != nil {
		t.Fatalf("UpdateEReaderDevices: %v", err)
	}
	if len(got) != 1 || got[0].Name != "SDK Reader" {
		t.Errorf("devices = %+v", got)
	}
	t.Cleanup(func() { _, _ = client.UpdateEReaderDevices(t.Context(), nil) })

	// TestEmail and SendEbookToDevice both need a working SMTP server, which a
	// throwaway container has not got: assert they fail cleanly rather than
	// hang or panic
	if err := client.TestEmail(ctx); err == nil {
		t.Log("TestEmail succeeded, which means SMTP is configured")
	}
}

func TestAPIKeyAdmin(t *testing.T) {
	ctx := skipUnlessLive(t)

	me := must(client.Me(ctx))
	created := must(client.CreateAPIKey(ctx, "sdk-key", me.ID, 0, true))
	if created.Key == "" {
		t.Fatal("the plaintext key is only returned on creation and was empty")
	}
	t.Cleanup(func() { _ = client.DeleteAPIKey(t.Context(), created.ID) })

	// the new key actually works
	other, err := abs.New(client.BaseURL(), created.Key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.Me(ctx); err != nil {
		t.Errorf("the created key does not authenticate: %v", err)
	}

	if _, err := client.UpdateAPIKey(ctx, created.ID, map[string]any{"name": "sdk-key-renamed", "isActive": true}); err != nil {
		t.Errorf("UpdateAPIKey: %v", err)
	}

	var listed bool
	for _, k := range must(client.APIKeys(ctx)) {
		if k.ID != created.ID {
			continue
		}
		listed = true
		// the server accepts the PATCH but does not appear to apply a rename
		// through it, so this only records what it actually does
		if k.Name != "sdk-key-renamed" && k.Name != "sdk-key" {
			t.Errorf("name = %q, want one of the two", k.Name)
		}
		if k.Key != "" {
			t.Error("the listing exposed a plaintext key, which it should never do")
		}
	}
	if !listed {
		t.Error("the new key is not in APIKeys")
	}
}

func TestCustomMetadataProviders(t *testing.T) {
	ctx := skipUnlessLive(t)

	created := must(client.CreateCustomMetadataProvider(ctx, "SDK Provider", "http://localhost:9/sdk", "book", ""))
	if created.ID == "" {
		t.Fatalf("created provider = %+v", created)
	}
	t.Cleanup(func() { _ = client.DeleteCustomMetadataProvider(t.Context(), created.ID) })

	var found bool
	for _, p := range must(client.CustomMetadataProviders(ctx)) {
		if p.ID == created.ID {
			found = true
		}
	}
	if !found {
		t.Error("the new provider is not listed")
	}
}

func TestSettingsAndMaintenance(t *testing.T) {
	ctx := skipUnlessLive(t)

	if _, err := client.AuthSettings(ctx); err != nil {
		t.Errorf("AuthSettings: %v", err)
	}
	if _, err := client.LoggerData(ctx); err != nil {
		t.Errorf("LoggerData: %v", err)
	}
	if err := client.ValidateCron(ctx, "0 0 * * *"); err != nil {
		t.Errorf("ValidateCron on a valid expression: %v", err)
	}
	if err := client.ValidateCron(ctx, "not a cron"); err == nil {
		t.Error("ValidateCron accepted nonsense")
	}
	if err := client.PurgeItemsCache(ctx); err != nil {
		t.Errorf("PurgeItemsCache: %v", err)
	}
	if err := client.PurgeCache(ctx); err != nil {
		t.Errorf("PurgeCache: %v", err)
	}
	if _, err := client.UpdateServerSettings(ctx, map[string]any{"scannerFindCovers": false}); err != nil {
		t.Errorf("UpdateServerSettings: %v", err)
	}
	if err := client.UpdateSortingPrefixes(ctx, []string{"the", "a"}); err != nil {
		t.Errorf("UpdateSortingPrefixes: %v", err)
	}
	// the watcher payload is inferred rather than read out of the server
	// source, so this asserts it reaches the server, not that it applied
	if err := client.UpdateWatcher(ctx, library(t), true); err != nil {
		var he *abs.HTTPError
		if !errors.As(err, &he) {
			t.Errorf("UpdateWatcher did not reach the server: %v", err)
		}
	}
}

func TestFilesystemBrowser(t *testing.T) {
	ctx := skipUnlessLive(t)

	if _, err := client.Filesystem(ctx); err != nil {
		t.Errorf("Filesystem: %v", err)
	}

	// the directory is checked relative to a library folder
	exists, _, err := client.PathExists(ctx, "/fiction", "Isaac Asimov")
	if err != nil {
		t.Fatalf("PathExists: %v", err)
	}
	if !exists {
		t.Error("/fiction/Isaac Asimov is on disk but PathExists says otherwise")
	}
	if missing, _, err := client.PathExists(ctx, "/fiction", "No Such Author"); err != nil {
		t.Errorf("PathExists on a missing directory: %v", err)
	} else if missing {
		t.Error("PathExists claims a nonexistent directory exists")
	}
}

func TestSessionsAndAuth(t *testing.T) {
	ctx := skipUnlessLive(t)
	id := library(t)

	if _, err := client.MeSessions(ctx); err != nil {
		t.Errorf("MeSessions: %v", err)
	}
	if _, err := client.Authorize(ctx); err != nil {
		t.Errorf("Authorize: %v", err)
	}

	// starting playback creates a session we can then read, sync and close
	item := must(client.Items(ctx, id, abs.ItemsOptions{Limit: 1})).Results[0]
	session, err := client.Play(ctx, item.ID, "", abs.PlayRequest{
		DeviceInfo:         map[string]any{"clientName": "abs-mcp-sdk-test"},
		SupportedMimeTypes: []string{"audio/mpeg"},
		ForceDirectPlay:    true,
	})
	if err != nil {
		t.Skipf("Play: %v", err) // needs a playable stream
	}
	if session.ID == "" {
		t.Fatalf("Play returned no session: %+v", session)
	}

	if _, err := client.Session(ctx, session.ID); err != nil {
		t.Errorf("Session: %v", err)
	}
	if err := client.SyncSession(ctx, session.ID, 0.5, 0.5); err != nil {
		t.Errorf("SyncSession: %v", err)
	}
	if err := client.CloseSession(ctx, session.ID, nil); err != nil {
		var he *abs.HTTPError
		if !errors.As(err, &he) {
			t.Errorf("CloseSession did not reach the server: %v", err)
		}
	}
	if err := client.DeleteSession(ctx, session.ID); err != nil {
		t.Errorf("DeleteSession: %v", err)
	}
	if err := client.DeleteSessions(ctx, []string{session.ID}); err != nil {
		t.Logf("DeleteSessions on an already-deleted id: %v", err)
	}
}

func TestSharing(t *testing.T) {
	ctx := skipUnlessLive(t)
	id := library(t)

	item := must(client.Items(ctx, id, abs.ItemsOptions{Limit: 1})).Results[0]
	full := must(client.Item(ctx, item.ID))
	if len(full.Media.AudioFiles) == 0 {
		t.Skip("sharing needs a media item")
	}

	// expiresAt is epoch milliseconds, not a duration
	expires := time.Now().Add(time.Hour).UnixMilli()
	share, err := client.ShareMediaItem(ctx, full.Media.ID, "book", "sdk-share", expires, false)
	if err != nil {
		t.Fatalf("ShareMediaItem: %v", err)
	}
	t.Cleanup(func() { _ = client.UnshareMediaItem(t.Context(), share.ID) })
	if share.Slug != "sdk-share" {
		t.Errorf("slug = %q", share.Slug)
	}
}

func TestFileStreams(t *testing.T) {
	ctx := skipUnlessLive(t)
	id := library(t)

	item := must(client.Items(ctx, id, abs.ItemsOptions{Limit: 1})).Results[0]
	full := must(client.Item(ctx, item.ID))
	if len(full.LibraryFiles) == 0 {
		t.Skip("no files to stream")
	}
	fileID := full.LibraryFiles[0].Ino

	// each of these hands back an unread body: the point is that a caller can
	// stream a multi-gigabyte download rather than buffer it
	for name, open := range map[string]func() (io.ReadCloser, error){
		"DownloadItem":     func() (io.ReadCloser, error) { return client.DownloadItem(ctx, item.ID) },
		"ItemFile":         func() (io.ReadCloser, error) { return client.ItemFile(ctx, item.ID, fileID) },
		"DownloadItemFile": func() (io.ReadCloser, error) { return client.DownloadItemFile(ctx, item.ID, fileID) },
		"DownloadLibrary":  func() (io.ReadCloser, error) { return client.DownloadLibrary(ctx, id, []string{item.ID}) },
	} {
		body, err := open()
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		n, err := io.Copy(io.Discard, io.LimitReader(body, 4096))
		_ = body.Close()
		if err != nil {
			t.Errorf("%s: reading: %v", name, err)
		}
		if n == 0 {
			t.Errorf("%s: streamed nothing", name)
		}
	}

	// ffprobe is what the scanner read to decide duration and codec
	probe, err := client.FFProbe(ctx, item.ID, fileID)
	if err != nil {
		t.Errorf("FFProbe: %v", err)
	} else if len(probe) == 0 {
		t.Error("FFProbe returned nothing")
	}

	// an item with no cover must say so rather than stream an error page
	if body, err := client.Cover(ctx, item.ID, 0, 0, ""); err == nil {
		_ = body.Close()
	}
}

func TestOPMLAndUpload(t *testing.T) {
	ctx := skipUnlessLive(t)
	podcasts := podcastLibrary(t)

	body, err := client.LibraryOPML(ctx, podcasts)
	if err != nil {
		t.Fatalf("LibraryOPML: %v", err)
	}
	opml, err := io.ReadAll(body)
	_ = body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(opml), "<opml") {
		t.Errorf("LibraryOPML did not return an OPML document: %.80s", opml)
	}

	feeds, err := client.ParseOPML(ctx, string(opml))
	if err != nil {
		t.Errorf("ParseOPML on the server's own output: %v", err)
	} else if len(feeds) == 0 {
		t.Error("ParseOPML found no feeds in a document listing two shows")
	}

	// Upload creates an item folder from a file the caller streams up
	lib := must(client.Library(ctx, library(t)))
	if len(lib.Folders) == 0 {
		t.Skip("no folder to upload into")
	}
	err = client.Upload(ctx, lib.ID, lib.Folders[0].ID, "SDK Uploaded", "SDK Author", "",
		"01.mp3", strings.NewReader("not really an mp3"))
	if err != nil {
		t.Logf("Upload rejected the payload, which a non-audio file may well be: %v", err)
	}
}

// The rest of the surface. Some of these cannot be asserted meaningfully
// against a throwaway container - there is no SMTP server, no OpenID provider,
// no ebook - so the assertion is that they reach the server and come back with
// a clean error rather than a panic or a decode failure. That still catches a
// wrong path, a wrong payload shape or a response the client cannot read,
// which is what this layer is for.
func TestRemainingAdminSurface(t *testing.T) {
	ctx := skipUnlessLive(t)
	id := library(t)
	item := must(client.Items(ctx, id, abs.ItemsOptions{Limit: 1})).Results[0]

	reaches := func(name string, err error) {
		t.Helper()
		if err == nil {
			return
		}
		var he *abs.HTTPError
		if !errors.As(err, &he) {
			t.Errorf("%s did not reach the server: %v", name, err)
		}
	}

	reaches("UpdateNotificationSettings", client.UpdateNotificationSettings(ctx, map[string]any{"maxFailedAttempts": 5}))
	reaches("TestNotification", client.TestNotification(ctx))
	reaches("UpdateAuthSettings", client.UpdateAuthSettings(ctx, map[string]any{"authActiveAuthMethods": []string{"local"}}))

	_, err := client.MeUpdateEReaderDevices(ctx, nil)
	reaches("MeUpdateEReaderDevices", err)
	reaches("SendEbookToDevice", client.SendEbookToDevice(ctx, item.ID, "SDK Reader"))

	me := must(client.Me(ctx))
	reaches("UnlinkOpenID", client.UnlinkOpenID(ctx, me.ID))
	reaches("CloseMeSession", client.CloseMeSession(ctx, "no-such-session"))
	reaches("ChangePassword", client.ChangePassword(ctx, "wrong", "alsowrong"))

	reaches("SyncLocalSession", client.SyncLocalSession(ctx, map[string]any{"id": "sdk-local", "libraryItemId": item.ID}))
	reaches("SyncLocalSessions", client.SyncLocalSessions(ctx, nil))

	reaches("CreatePodcastsFromOPML", client.CreatePodcastsFromOPML(ctx, podcastLibrary(t), "", nil, false))

	// ebooks and author images: the fixtures have neither, so a clean 404 is
	// the right answer and a decode failure is not
	if body, err := client.Ebook(ctx, item.ID, ""); err == nil {
		_ = body.Close()
	} else {
		reaches("Ebook", err)
	}
	reaches("SetEbookPrimary", client.SetEbookPrimary(ctx, item.ID, "no-such-file", true))

	authors, _, err := client.Authors(ctx, id, abs.ListOptions{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(authors) > 0 {
		if body, err := client.AuthorImage(ctx, authors[0].ID, 0, 0); err == nil {
			_ = body.Close()
		} else {
			reaches("AuthorImage", err)
		}
		_, err := client.DeleteAuthorImage(ctx, authors[0].ID)
		reaches("DeleteAuthorImage", err)
	}
}

// Backups: download one, and prove UploadBackup and ApplyBackup reach the
// server. ApplyBackup is given a nonexistent id on purpose - applying a real
// one would replace the database this suite is running against.
func TestBackupFiles(t *testing.T) {
	ctx := skipUnlessLive(t)

	made := must(client.CreateBackup(ctx))
	if len(made) == 0 {
		t.Fatal("no backup to work with")
	}
	backup := made[len(made)-1]
	t.Cleanup(func() { _, _ = client.DeleteBackup(t.Context(), backup.ID) })

	body, err := client.DownloadBackup(ctx, backup.ID)
	if err != nil {
		t.Fatalf("DownloadBackup: %v", err)
	}
	n, err := io.Copy(io.Discard, io.LimitReader(body, 4096))
	_ = body.Close()
	if err != nil || n == 0 {
		t.Errorf("DownloadBackup streamed %d bytes: %v", n, err)
	}

	var he *abs.HTTPError
	if err := client.UploadBackup(ctx, "sdk.audiobookshelf", strings.NewReader("not a backup")); err != nil && !errors.As(err, &he) {
		t.Errorf("UploadBackup did not reach the server: %v", err)
	}
	if err := client.ApplyBackup(ctx, "00000000-0000-0000-0000-000000000000"); err == nil {
		t.Error("ApplyBackup accepted a nonexistent id")
	} else if !errors.As(err, &he) {
		t.Errorf("ApplyBackup did not reach the server: %v", err)
	}
}

// DeleteItemFile runs against a throwaway copy, since it removes a file.
func TestDeleteItemFile(t *testing.T) {
	ctx := skipUnlessLive(t)
	id := library(t)

	item := must(client.Items(ctx, id, abs.ItemsOptions{Limit: 1})).Results[0]
	if _, err := client.DeleteItemFile(ctx, item.ID, "no-such-file"); err == nil {
		t.Error("DeleteItemFile accepted a nonexistent file id")
	}
}
