//go:build acceptance

// The server-administration surface: notifications, email, API keys, custom
// providers, settings, sharing, sessions and the filesystem browser. None of
// this is wrapped as an MCP tool, but sdk/abs is a general Audiobookshelf
// client, so it is covered here like everything else.
package abs_test

import (
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/katbyte/abs-mcp/sdk/abs"
	pclient "github.com/katbyte/pandorest/client"
)

func TestNotifications(t *testing.T) {
	ctx := skipUnlessLive(t)

	created := must(client.NotificationCreateNotification(ctx, abs.NotificationCreateNotificationRequest{EventName: "onTest", Urls: []string{"json://localhost/sdk"}, TitleTemplate: "SDK", BodyTemplate: "SDK", Enabled: new(false), Type: "apprise"}))
	var id string
	for _, n := range created.Notifications {
		if n.EventName == "onTest" {
			id = n.Id
		}
	}
	if id == "" {
		t.Fatalf("the new notification is not in the settings: %+v", created)
	}
	t.Cleanup(func() { _ = errorOf(client.NotificationDeleteNotification(afterwards(t), id)) })

	if err := errorOf(client.NotificationUpdateNotification(ctx, id, abs.NotificationUpdateNotificationRequest{Id: id, Enabled: new(false), EventName: "onTest"})); err != nil {
		t.Errorf("updating the notification: %v", err)
	}
	// firing one notification, as opposed to all of them
	if err := errorOf(client.NotificationSendNotificationTest(ctx, id)); err != nil && pclient.StatusCode(err) == 0 {
		t.Errorf("testing the notification did not reach the server: %v", err)
	}
	if err := errorOf(client.NotificationDeleteNotification(ctx, id)); err != nil {
		t.Errorf("deleting the notification: %v", err)
	}
}

func TestEmailSettings(t *testing.T) {
	ctx := skipUnlessLive(t)

	if err := errorOf(client.EmailUpdateSettings(ctx, abs.EmailUpdateSettingsRequest{FromAddress: "sdk@example.invalid"})); err != nil {
		t.Errorf("updating the email settings: %v", err)
	}

	devices := []abs.EreaderDevice{{Name: "SDK Reader", Email: "sdk@example.invalid", AvailabilityOption: "adminOrUp"}}
	got, err := record(client.EmailUpdateEReaderDevices(ctx, abs.EmailUpdateEReaderDevicesRequest{EreaderDevices: devices}))
	if err != nil {
		t.Fatalf("setting the e-reader devices: %v", err)
	}
	t.Cleanup(func() {
		_ = errorOf(client.EmailUpdateEReaderDevices(afterwards(t), abs.EmailUpdateEReaderDevicesRequest{EreaderDevices: []abs.EreaderDevice{}}))
	})
	if len(got.EreaderDevices) != 1 || got.EreaderDevices[0].Name != "SDK Reader" {
		t.Errorf("devices = %+v", got.EreaderDevices)
	}

	// testing the email needs a working SMTP server, which a throwaway container has not got: it fails cleanly rather than hang or panic
	if err := errorOf(client.EmailSendTest(ctx)); err == nil {
		t.Log("the test email went out, which means SMTP is configured")
	}
}

func TestAPIKeyAdmin(t *testing.T) {
	ctx := skipUnlessLive(t)

	me := must(client.MeGetCurrentUser(ctx))
	created := must(client.ApiKeyCreate(ctx, abs.ApiKeyCreateRequest{Name: "sdk-key", UserId: me.Id, IsActive: new(true)})).ApiKey
	if created == nil || created.ApiKey == "" {
		t.Fatalf("the plaintext key is only answered on creation and was empty: %+v", created)
	}
	t.Cleanup(func() { _ = errorOf(client.ApiKeyDelete(afterwards(t), string(created.Id))) })

	// the new key actually works, for the same client acting as its account
	other, err := client.As(created.ApiKey)
	if err != nil {
		t.Fatal(err)
	}
	if as, err := record(other.MeGetCurrentUser(ctx)); err != nil || as.Id != me.Id {
		t.Errorf("the created key does not authenticate as its account: %+v, %v", as, err)
	}
	if other.Client.BaseURL != client.Client.BaseURL {
		t.Errorf("the client acting as the key's account is at %s, want the same server", other.Client.BaseURL)
	}
	if _, err := client.As(""); err == nil {
		t.Error("a client was made to act as an account with no key")
	}

	// what an update can change is whether the key works; a key's name is part of the key, and the update takes none
	updated := must(client.ApiKeyUpdate(ctx, string(created.Id), abs.ApiKeyUpdateRequest{IsActive: new(false)})).ApiKey
	if updated == nil || updated.IsActive {
		t.Errorf("the answer to making the key inactive = %+v, want it inactive", updated)
	}
	if _, err := other.MeGetCurrentUser(ctx); err == nil {
		t.Error("the key still authenticates after it was made inactive")
	}

	var listed bool
	for _, k := range must(client.ApiKeyGetAll(ctx)).ApiKeys {
		if k.Id != created.Id {
			continue
		}
		listed = true
		if k.Name != "sdk-key" || k.IsActive {
			t.Errorf("listed as %q, active %v, want sdk-key, inactive", k.Name, k.IsActive)
		}
		if k.ApiKey != "" {
			t.Error("the listing exposed a plaintext key, which it should never do")
		}
	}
	if !listed {
		t.Error("the new key is not listed")
	}
}

func TestCustomMetadataProviders(t *testing.T) {
	ctx := skipUnlessLive(t)

	created := must(client.CustomMetadataProviderCreate(ctx, abs.CustomMetadataProviderCreateRequest{Name: "SDK Provider", Url: "http://localhost:9/sdk", MediaType: "book"})).Provider
	if created == nil || created.Id == "" {
		t.Fatalf("created provider = %+v", created)
	}
	t.Cleanup(func() { _ = errorOf(client.CustomMetadataProviderDelete(afterwards(t), string(created.Id))) })

	listed := func(p abs.CustomMetadataProviderRecord) bool { return p.Id == created.Id }
	if !slices.ContainsFunc(must(client.CustomMetadataProviderGetAll(ctx)).Providers, listed) {
		t.Error("the new provider is not listed")
	}
}

func TestSettingsAndMaintenance(t *testing.T) {
	ctx := skipUnlessLive(t)
	library(t)

	if err := errorOf(client.MiscValidateCronExpression(ctx, abs.MiscValidateCronExpressionRequest{Expression: "0 0 * * *"})); err != nil {
		t.Errorf("checking a valid cron expression: %v", err)
	}
	if err := errorOf(client.MiscValidateCronExpression(ctx, abs.MiscValidateCronExpressionRequest{Expression: "not a cron"})); err == nil {
		t.Error("a cron expression that is nonsense was taken")
	}
	if err := errorOf(client.CachePurgeItemsCache(ctx)); err != nil {
		t.Errorf("purging the items cache: %v", err)
	}
	if err := errorOf(client.CachePurgeCache(ctx)); err != nil {
		t.Errorf("purging the cache: %v", err)
	}
	if err := errorOf(client.MiscUpdateServerSettings(ctx, abs.MiscUpdateServerSettingsRequest{ScannerFindCovers: new(false)})); err != nil {
		t.Errorf("updating the server settings: %v", err)
	}
	if err := errorOf(client.MiscUpdateSortingPrefixes(ctx, abs.MiscUpdateSortingPrefixesRequest{SortingPrefixes: []string{"the", "a"}})); err != nil {
		t.Errorf("updating the sorting prefixes: %v", err)
	}
	// a library the watcher does not know, so the call is checked and answered without the watcher acting on it
	if err := errorOf(client.MiscUpdateWatchedPath(ctx, abs.MiscUpdateWatchedPathRequest{LibraryId: "no-such-library", Path: "/fiction/sdk-watched", Type: "add"})); err != nil {
		t.Errorf("telling the watcher of a path: %v", err)
	}
}

func TestFilesystemBrowser(t *testing.T) {
	ctx := skipUnlessLive(t)

	// the directory is checked relative to a library folder
	if got := must(client.FileSystemCheckPathExists(ctx, abs.FileSystemCheckPathExistsRequest{FolderPath: "/fiction", Directory: "Isaac Asimov"})); !got.Exists {
		t.Error("/fiction/Isaac Asimov is on disk but the check says otherwise")
	}
	if got := must(client.FileSystemCheckPathExists(ctx, abs.FileSystemCheckPathExistsRequest{FolderPath: "/fiction", Directory: "No Such Author"})); got.Exists {
		t.Error("the check claims a directory that is not there exists")
	}

	// any absolute path, which is what a new library's folder is checked with: the folder picker answers a 400 for one that is not there, where
	// the server would otherwise create it
	if err := errorOf(client.FileSystemGetPaths(ctx, abs.FileSystemGetPathsOptions{Path: "/fiction/Isaac Asimov", Level: new(0)})); err != nil {
		t.Errorf("listing a folder that is there: %v", err)
	}
	if err := errorOf(client.FileSystemGetPaths(ctx, abs.FileSystemGetPathsOptions{Path: "/no-such-folder", Level: new(0)})); pclient.StatusCode(err) != http.StatusBadRequest {
		t.Errorf("listing a folder that is not there = %v, want a 400", err)
	}
}

func TestSessionsAndAuth(t *testing.T) {
	ctx := skipUnlessLive(t)
	id := library(t)

	if err := errorOf(client.MiscAuthorize(ctx)); err != nil {
		t.Errorf("authorizing: %v", err)
	}

	// starting playback creates a session we can then read, sync and close
	item := firstItems(ctx, id, 1)[0]
	play := abs.LibraryItemStartPlaybackSessionRequest{DeviceInfo: &abs.LibraryItemStartPlaybackSessionRequestDeviceInfo{ClientName: "abs-mcp-sdk-test"}, SupportedMimeTypes: []string{"audio/mpeg"}, ForceDirectPlay: new(true)}
	session, err := record(client.LibraryItemStartPlaybackSession(ctx, item.Id, play))
	if err != nil {
		t.Skipf("starting playback: %v", err) // needs a playable stream
	}
	if session.Id == "" {
		t.Fatalf("starting playback answered no session: %+v", session)
	}

	if err := errorOf(client.SessionGetOpenSession(ctx, session.Id)); err != nil {
		t.Errorf("reading the open session: %v", err)
	}
	if err := errorOf(client.SessionSync(ctx, session.Id, abs.SessionSyncRequest{CurrentTime: 0.5, TimeListened: 0.5})); err != nil {
		t.Errorf("syncing the session: %v", err)
	}
	if err := errorOf(client.SessionClose(ctx, session.Id, abs.SessionCloseRequest{})); err != nil && pclient.StatusCode(err) == 0 {
		t.Errorf("closing the session did not reach the server: %v", err)
	}
	if err := errorOf(client.SessionDelete(ctx, session.Id)); err != nil {
		t.Errorf("deleting the session: %v", err)
	}

	// the batch delete against a second session, so it is asserted rather than tolerated
	second, err := record(client.LibraryItemStartPlaybackSession(ctx, item.Id, play))
	if err != nil {
		t.Skipf("starting playback again: %v", err)
	}
	if err := errorOf(client.SessionBatchDelete(ctx, abs.SessionBatchDeleteRequest{Sessions: []string{second.Id}})); err != nil {
		t.Errorf("deleting a batch of sessions: %v", err)
	}
	if _, err := client.SessionGetOpenSession(ctx, second.Id); !abs.IsNotFound(err) {
		t.Errorf("the batch-deleted session is still there: %v", err)
	}
}

func TestSharing(t *testing.T) {
	ctx := skipUnlessLive(t)
	id := library(t)

	full := whole(ctx, firstItems(ctx, id, 1)[0].Id)
	if len(full.Media.AudioFiles) == 0 {
		t.Skip("sharing needs a media item")
	}

	// expiresAt is epoch milliseconds, not a duration
	share, err := record(client.ShareCreateMediaItemShare(ctx, abs.ShareCreateMediaItemShareRequest{MediaItemId: full.Media.Id, MediaItemType: abs.MediaItemTypeBook, Slug: "sdk-share", ExpiresAt: int(time.Now().Add(time.Hour).UnixMilli()), IsDownloadable: new(false)}))
	if err != nil {
		t.Fatalf("sharing the book: %v", err)
	}
	t.Cleanup(func() { _ = errorOf(client.ShareDeleteMediaItemShare(afterwards(t), share.Id)) })
	if share.Slug != "sdk-share" {
		t.Errorf("slug = %q", share.Slug)
	}

	// the server lists no links: the book's record carries its own
	if got := sharedAs(t, full.Id); got == nil || got.Id != share.Id || got.Slug != "sdk-share" {
		t.Errorf("the book's link = %+v, want the one just opened", got)
	}
	// someone opening the link plays it in a session of the link's own, which a cookie carries, and that session's place is kept
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	visitor, err := connect(os.Getenv("ABS_TOKEN"), pclient.WithCookies(jar))
	if err != nil {
		t.Fatal(err)
	}
	if err := errorOf(visitor.ShareGetMediaItemShareBySlug(ctx, share.Slug)); err != nil {
		t.Fatalf("opening the link: %v", err)
	}
	if err := errorOf(visitor.ShareUpdateMediaItemShareProgress(ctx, share.Slug, abs.ShareUpdateMediaItemShareProgressRequest{CurrentTime: 1})); err != nil {
		t.Errorf("keeping the place in the link's session: %v", err)
	}

	if err := errorOf(client.ShareDeleteMediaItemShare(ctx, share.Id)); err != nil {
		t.Fatalf("closing the link: %v", err)
	}
	if got := sharedAs(t, full.Id); got != nil {
		t.Errorf("the book's link after closing = %+v; want none", got)
	}
}

// sharedAs is the public link an item's record carries, which it carries only when asked.
func sharedAs(t *testing.T, id abs.LibraryItemID) *abs.MediaItemShareForClient {
	t.Helper()

	return must(client.LibraryItemFindOne(t.Context(), id, abs.LibraryItemFindOneOptions{Expanded: "1", Include: "share"})).MediaItemShare
}

// Setting up a server that has its root account already is refused, and changes nothing.
func TestSetupRefused(t *testing.T) {
	ctx := skipUnlessLive(t)

	err := errorOf(client.ServerPostInit(ctx, abs.ServerPostInitRequest{NewRoot: map[string]string{"username": "sdk-root"}}))
	if pclient.StatusCode(err) != http.StatusInternalServerError {
		t.Errorf("setting up a server already set up = %v, want the 500 it refuses with", err)
	}
	if me := must(client.MeGetCurrentUser(ctx)); me.Username != "root" {
		t.Errorf("the account after a refused setup is %q, want root", me.Username)
	}
}

// The OPML lists a show by its feed, which the fixture shows have none of, so one is made with one and removed, files and all, after.
func TestOPML(t *testing.T) {
	ctx := skipUnlessLive(t)
	podcasts := podcastLibrary(t)

	show := abs.PodcastCreateRequest{LibraryId: podcasts, FolderId: folderOf(t, podcasts), Path: "/podcasts/SDK OPML Podcast", Media: &abs.PodcastCreateRequestMedia{Metadata: &abs.PodcastCreateRequestMediaMetadata{Title: "SDK OPML Podcast", FeedUrl: feedURL}}}
	created, files := must(client.PodcastCreate(ctx, show)), abs.LibraryItemDeleteOptions{Hard: "1"}
	t.Cleanup(func() { _ = errorOf(client.LibraryItemDelete(afterwards(t), created.Id, files)) })

	got, err := client.LibraryGetOPMLFile(ctx, podcasts)
	if err != nil {
		t.Fatalf("reading the OPML: %v", err)
	}
	if got.Answer == nil || !strings.Contains(*got.Answer, "<opml") {
		t.Fatalf("the OPML is not an OPML document: %.80v", got.Answer)
	}

	feeds, err := record(client.PodcastGetFeedsFromOPMLText(ctx, abs.PodcastGetFeedsFromOPMLTextRequest{OpmlText: *got.Answer}))
	if err != nil {
		t.Errorf("reading the feeds out of the server's own OPML: %v", err)
	} else if len(feeds.Feeds) == 0 {
		t.Error("no feeds found in an OPML listing a show with one")
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
	item := firstItems(ctx, id, 1)[0]

	reaches := func(name string, err error) {
		t.Helper()

		if err != nil && pclient.StatusCode(err) == 0 {
			t.Errorf("%s did not reach the server: %v", name, err)
		}
	}

	// settings changes are asserted by reading them back
	if err := errorOf(client.NotificationUpdate(ctx, abs.NotificationUpdateRequest{MaxFailedAttempts: 7})); err != nil {
		t.Errorf("updating the notification settings: %v", err)
	} else if got := must(client.NotificationGet(ctx)).Settings; got == nil || got.MaxFailedAttempts != 7 {
		t.Errorf("the notification settings read back = %+v, want 7 failed attempts", got)
	}

	if err := errorOf(client.MiscUpdateAuthSettings(ctx, abs.MiscUpdateAuthSettingsRequest{AuthActiveAuthMethods: []string{"local"}})); err != nil {
		t.Errorf("updating the sign-in settings: %v", err)
	} else if after := must(client.MiscGetAuthSettings(ctx)); !slices.Equal(after.AuthActiveAuthMethods, []string{"local"}) {
		t.Errorf("the sign-in methods read back = %v, want local", after.AuthActiveAuthMethods)
	}

	// an account's own device must be scoped to that account alone
	me := must(client.MeGetCurrentUser(ctx))
	devices := []abs.EreaderDevice{{Name: "SDK Me Reader", Email: "me@example.invalid", AvailabilityOption: "specificUsers", Users: []string{string(me.Id)}}}
	named := func(d abs.EreaderDevice) bool { return d.Name == "SDK Me Reader" }
	// the server answers with every device it knows, not just the caller's
	if got, err := record(client.MeUpdateUserEReaderDevices(ctx, abs.MeUpdateUserEReaderDevicesRequest{EreaderDevices: devices})); err != nil {
		t.Errorf("setting the account's own devices: %v", err)
	} else if !slices.ContainsFunc(got.EreaderDevices, named) {
		t.Errorf("devices = %+v, want the new one among them", got.EreaderDevices)
	}
	t.Cleanup(func() {
		_ = errorOf(client.MeUpdateUserEReaderDevices(afterwards(t), abs.MeUpdateUserEReaderDevicesRequest{EreaderDevices: []abs.EreaderDevice{}}))
	})

	// what a sign-in answers: the devices this account may send to, its own among them
	if mine, err := record(client.MiscAuthorize(ctx)); err != nil {
		t.Errorf("authorizing: %v", err)
	} else if !slices.ContainsFunc(mine.EreaderDevices, named) {
		t.Errorf("the devices on sign-in = %+v, want the account's own device among them", mine.EreaderDevices)
	}

	// the password is not what this suite authenticates with - it uses an API
	// key - so changing it and changing it back is safe
	suite, changed := "abs-mcp-integration", "sdk-new-password"
	if err := errorOf(client.MeUpdatePassword(ctx, abs.MeUpdatePasswordRequest{Password: suite, NewPassword: changed})); err != nil {
		t.Errorf("changing the password: %v", err)
	} else if err := errorOf(client.MeUpdatePassword(ctx, abs.MeUpdatePasswordRequest{Password: changed, NewPassword: suite})); err != nil {
		t.Errorf("changing the password back: %v", err)
	}

	if err := errorOf(client.SessionSyncLocalSessions(ctx, abs.SessionSyncLocalSessionsRequest{})); err != nil {
		t.Errorf("syncing no local sessions: %v", err)
	}

	// these need infrastructure a throwaway container has not got - an SMTP
	// server, an apprise endpoint, an OpenID provider - so the assertion is
	// that they reach the server and answer, not that they succeed
	reaches("firing the test event", errorOf(client.NotificationFireTestEvent(ctx)))
	reaches("sending an ebook to a device", errorOf(client.EmailSendEBookToDevice(ctx, abs.EmailSendEBookToDeviceRequest{LibraryItemId: item.Id, DeviceName: "SDK Reader"})))
	reaches("unlinking OpenID", errorOf(client.UserUnlinkFromOpenID(ctx, me.Id)))
	reaches("closing a session of the account's", errorOf(client.MeDeleteSession(ctx, "no-such-session")))
	reaches("syncing a local session", errorOf(client.SessionSyncLocal(ctx, abs.SessionSyncLocalRequest{Id: "sdk-local", LibraryItemId: string(item.Id)})))

	if authors := firstAuthors(ctx, id, 1); len(authors) > 0 {
		reaches("deleting the author's image", errorOf(client.AuthorDeleteImage(ctx, authors[0].Id)))
	}
}

// Backups sent up and put back: an upload of something that is not a backup, and putting back one that does not exist. Applying a real one
// would replace the database this suite is running against, so the assertion is that each reaches the server.
func TestBackupUploadAndApply(t *testing.T) {
	ctx := skipUnlessLive(t)

	body, kind, err := form("file", "sdk.audiobookshelf", []byte("not a backup"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := errorOf(client.BackupUpload(ctx, body, kind)); err != nil && pclient.StatusCode(err) == 0 {
		t.Errorf("uploading a backup did not reach the server: %v", err)
	}
	if err := errorOf(client.BackupApply(ctx, "00000000-0000-0000-0000-000000000000")); err == nil {
		t.Error("putting back a backup that does not exist went through")
	} else if pclient.StatusCode(err) == 0 {
		t.Errorf("putting back a backup did not reach the server: %v", err)
	}
}

// Foundation carries an epub beside its audio, so the ebook endpoints have
// something real to work on.
func TestEbookEndpoints(t *testing.T) {
	ctx := skipUnlessLive(t)
	id := library(t)

	var item *abs.LibraryItemExpanded
	var ebookFile string
	for _, candidate := range firstItems(ctx, id, 100) {
		full := whole(ctx, candidate.Id)
		for _, f := range full.LibraryFiles {
			if f.FileType == "ebook" {
				item, ebookFile = full, f.Ino
			}
		}
		if item != nil {
			break
		}
	}
	if item == nil {
		t.Skip("no ebook was scanned; the epub fixture may not have been picked up")
	}

	got, err := client.LibraryItemGetEBookFile2(ctx, item.Id)
	if err != nil {
		t.Fatalf("reading the ebook: %v", err)
	}
	book, err := got.Bytes(64 << 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(book) < 2 || book[0] != 'P' || book[1] != 'K' {
		t.Errorf("the ebook does not start with a zip header: %q", book[:min(len(book), 4)])
	}

	// the status flips the file between the book's ebook and a supplementary file, so it is flipped and flipped back
	if err := errorOf(client.LibraryItemUpdateEbookFileStatus(ctx, item.Id, ebookFile)); err != nil {
		t.Errorf("making the ebook supplementary: %v", err)
	}
	if err := errorOf(client.LibraryItemUpdateEbookFileStatus(ctx, item.Id, ebookFile)); err != nil {
		t.Errorf("making the ebook primary again: %v", err)
	}
}

// Subscribing to every feed in an OPML document. It runs against the feed the proxy has recorded.
func TestCreatePodcastsFromOPML(t *testing.T) {
	ctx := skipUnlessLive(t)
	id := podcastLibrary(t)

	count := func() (int, error) {
		res, err := record(client.LibraryGetLibraryItems(ctx, id, abs.LibraryGetLibraryItemsOptions{Limit: new(100), Minified: "1"}))
		return res.Total, err
	}
	before, err := count()
	if err != nil {
		t.Fatal(err)
	}
	if err := errorOf(client.PodcastBulkCreatePodcastsFromOpmlFeedUrls(ctx, abs.PodcastBulkCreatePodcastsFromOpmlFeedUrlsRequest{LibraryId: id, FolderId: folderOf(t, id), Feeds: []string{feedURL}, AutoDownloadEpisodes: new(false)})); err != nil {
		t.Fatalf("subscribing to the feeds: %v", err)
	}

	// the podcast the OPML names is new; take it away again
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(2 * time.Second) {
		if n, err := count(); err != nil || n <= before {
			continue
		}
		for _, it := range firstItems(ctx, id, 100) {
			if title := it.Title(); title != "Well There's Your Problem" && title != "Behind the Bastards" {
				t.Cleanup(func() { _ = errorOf(client.LibraryItemDelete(afterwards(t), it.Id)) })
			}
		}
		return
	}
	t.Errorf("the OPML feed never became a podcast: still %d items", before)
}

// An upload streams a file up and the server makes the item's folder from it.
func TestUploadCreatesAnItem(t *testing.T) {
	ctx := skipUnlessLive(t)

	data := os.Getenv("ABS_TEST_DATA")
	if data == "" {
		t.Skip("ABS_TEST_DATA is not set")
	}
	audio, err := os.ReadFile(filepath.Join(data, "fiction", "Isaac Asimov", "Foundation", "01.mp3"))
	if err != nil {
		t.Fatal(err)
	}

	scratch := must(client.LibraryCreate(ctx, abs.LibraryCreateRequest{Name: "SDK Upload Scratch", MediaType: "book", Folders: []abs.LibraryFolder{{FullPath: "/nonfiction"}}}))
	t.Cleanup(func() {
		_ = errorOf(client.LibraryDelete(afterwards(t), scratch.Id))
		_ = os.RemoveAll(filepath.Join(data, "nonfiction", "SDK Uploader"))
	})

	fields := map[string]string{"library": string(scratch.Id), "folder": string(scratch.Folders[0].Id), "title": "SDK Uploaded", "author": "SDK Uploader"}
	body, kind, err := form("0", "01.mp3", audio, fields)
	if err != nil {
		t.Fatal(err)
	}
	if err := errorOf(client.MiscHandleUpload(ctx, body, kind)); err != nil {
		t.Fatalf("uploading: %v", err)
	}

	// the upload writes the folder; prove that much before waiting on a scan
	uploaded := filepath.Join(data, "nonfiction", "SDK Uploader", "SDK Uploaded")
	if _, err := os.Stat(uploaded); err != nil {
		t.Fatalf("the upload reported success but wrote nothing to %s: %v", uploaded, err)
	}

	if err := errorOf(client.LibraryScan(ctx, scratch.Id)); err != nil {
		t.Fatal(err)
	}
	scannedItems(t, scratch.Id, 1, func(title string) bool { return title == "SDK Uploaded" })
}
