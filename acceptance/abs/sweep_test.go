//go:build acceptance

package abs_test

// The read sweep: every GET of the API document, called against the running Audiobookshelf through the client generated from it, with
// arguments from this suite's fixtures. The other tests here prove the hand-written client field by field; the sweep proves the generated one
// reads every answer the server gives, and keeps doing so as the document changes: a GET the document gains is swept on the next run with
// nothing to write, and one that cannot answer must say why here.
//
// The sweep is pandorest's (github.com/katbyte/pandorest/sweep); what is here is the fixtures and the cases. A case whose operation starts
// answering fails the sweep, so a stale one is noticed and removed.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	gen "github.com/katbyte/abs-mcp/sdk/abs"
	"github.com/katbyte/abs-mcp/sdk/abs-byhand"
	pclient "github.com/katbyte/pandorest/client"
	"github.com/katbyte/pandorest/definitions"
	"github.com/katbyte/pandorest/sweep"
)

// TestReadSweep calls every GET against the fixture libraries, with one of each thing Audiobookshelf reads by id made for it.
func TestReadSweep(t *testing.T) {
	ctx := skipUnlessLive(t)

	books, shows := library(t), podcastLibrary(t)
	me := must(client.Me(ctx))

	book := must(client.Item(ctx, must(client.Items(ctx, books, abs.ItemsOptions{Limit: 1})).Results[0].ID))
	show := must(client.Item(ctx, must(client.Items(ctx, shows, abs.ItemsOptions{Limit: 1})).Results[0].ID))
	if len(book.Media.AudioFiles) == 0 || len(show.Media.Episodes) == 0 {
		t.Fatalf("the fixtures are incomplete: %d audio files on %s, %d episodes on %s", len(book.Media.AudioFiles), book.ID, len(show.Media.Episodes), show.ID)
	}
	episode := show.Media.Episodes[0].ID

	authors, _, err := client.Authors(ctx, books, abs.ListOptions{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	series, _, err := client.SeriesList(ctx, books, abs.ListOptions{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(authors) == 0 || len(series) == 0 {
		t.Fatalf("the fixtures are incomplete: %d authors, %d series", len(authors), len(series))
	}

	// what is read back by id is made here and removed after; an image the server fetches comes from the recorded provider answers
	if err := client.UploadCover(ctx, book.ID, "cover.jpg", bytes.NewReader(tinyJPEG())); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.RemoveCover(context.WithoutCancel(t.Context()), book.ID) })
	if _, err := client.SetAuthorImage(ctx, authors[0].ID, "https://m.media-amazon.com/images/I/81Sr4Ms8SaL.jpg"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = client.DeleteAuthorImage(context.WithoutCancel(t.Context()), authors[0].ID) })
	if _, err := client.UpdateMedia(ctx, book.ID, abs.MediaUpdate{Metadata: &abs.MetadataUpdate{Genres: []string{"Sweep"}}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = client.UpdateMedia(context.WithoutCancel(t.Context()), book.ID, abs.MediaUpdate{Metadata: &abs.MetadataUpdate{Genres: append([]string{}, book.Media.Metadata.Genres...)}})
	})
	provider := must(client.CreateCustomMetadataProvider(ctx, "Sweep", "http://localhost:1", "book", ""))
	t.Cleanup(func() { _ = client.DeleteCustomMetadataProvider(context.WithoutCancel(t.Context()), provider.ID) })
	collection := must(client.CreateCollection(ctx, books, "Sweep", "", []string{book.ID}))
	t.Cleanup(func() { _ = client.DeleteCollection(context.WithoutCancel(t.Context()), collection.ID) })
	playlist := must(client.CreatePlaylist(ctx, books, "Sweep", "", []abs.PlaylistEntry{{LibraryItemID: book.ID}}))
	t.Cleanup(func() { _ = client.DeletePlaylist(context.WithoutCancel(t.Context()), playlist.ID) })
	// an opened feed's answer leaves its slug out
	const slug = "sweep-feed"
	feed := must(client.OpenItemFeed(ctx, book.ID, slug, client.BaseURL()))
	t.Cleanup(func() { _ = client.CloseFeed(context.WithoutCancel(t.Context()), feed.ID) })
	share := must(client.ShareMediaItem(ctx, book.Media.ID, "book", "sweep-share", time.Now().Add(time.Hour).UnixMilli(), true))
	t.Cleanup(func() { _ = client.UnshareMediaItem(context.WithoutCancel(t.Context()), share.ID) })
	backups := must(client.CreateBackup(ctx))
	backup := backups[len(backups)-1].ID
	t.Cleanup(func() { _, _ = client.DeleteBackup(context.WithoutCancel(t.Context()), backup) })
	bookmark := must(client.CreateBookmark(ctx, book.ID, 5, "sweep"))
	t.Cleanup(func() { _ = client.DeleteBookmark(context.WithoutCancel(t.Context()), book.ID, bookmark.Time) })
	session := must(client.Play(ctx, book.ID, "", abs.PlayRequest{ForceDirectPlay: true, MediaPlayer: "sweep"}))
	t.Cleanup(func() { _ = client.CloseSession(context.WithoutCancel(t.Context()), session.ID, nil) })
	// time listened is what the listening stats count
	if err := client.SyncSession(ctx, session.ID, 40, 10); err != nil {
		t.Fatal(err)
	}

	// the fixture recordings last seconds, and the server counts anything within ten seconds of the end as finished, so the progress is set after the
	// sync, against an hour, to leave the book and the episode in progress
	for _, at := range []struct{ item, episode string }{{book.ID, ""}, {show.ID, episode}} {
		if err := client.SetProgress(ctx, at.item, at.episode, abs.ProgressUpdate{CurrentTime: new(30.0), Duration: new(3600.0)}); err != nil {
			t.Fatal(err)
		}
	}

	// a share's files are read in the share's own session, which opening the share starts and a cookie carries
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	sdk := generated(t, pclient.WithCookies(jar))
	if _, err := sdk.ShareGetMediaItemShareBySlug(ctx, share.Slug); err != nil {
		t.Fatal(err)
	}
	svc, err := definitions.Load(filepath.Join("..", "..", "api-defs", "audiobookshelf-"+gen.DocumentVersion))
	if err != nil {
		t.Fatal(err)
	}

	id := func(v any) string { return fmt.Sprint(v) }
	year := id(time.Now().Year())
	// a path parameter is resolved by the segments before it and its name, the most of them first, then by its name alone
	fixtures := map[string]string{
		"libraries/id":                     books,
		"series/seriesId":                  series[0].ID,
		"items/id":                         book.ID,
		"file/fileid":                      book.Media.AudioFiles[0].Ino,
		"ffprobe/fileid":                   book.Media.AudioFiles[0].Ino,
		"authors/id":                       authors[0].ID,
		"series/id":                        series[0].ID,
		"collections/id":                   collection.ID,
		"playlists/id":                     playlist.ID,
		"users/id":                         me.ID,
		"podcasts/id":                      show.ID,
		"episodeId":                        episode,
		"backups/id":                       backup,
		"bookmarks/libraryItemId":          book.ID,
		"listening-sessions/libraryItemId": book.ID,
		"progress/id":                      book.ID,
		"session/id":                       session.ID,
		"track/index":                      "1",
		"feed/slug":                        slug,
		"share/slug":                       share.Slug,
		"year/year":                        year,
	}

	cases := map[string]sweep.Case{
		// Audiobookshelf answers some writes to a GET, which a sweep must not make
		"BackupApply":                         {Skip: "restores the backup over the server's database"},
		"LibraryMatchAll":                     {Skip: "matches every book of the library against the stores, rewriting their details"},
		"NotificationFireTestEvent":           {Skip: "sends a test notification to every notification set up"},
		"NotificationSendNotificationTest":    {Skip: "sends a test notification"},
		"PodcastClearEpisodeDownloadQueue":    {Skip: "clears the podcast's download queue"},
		"PodcastCheckNewEpisodes":             {Skip: "fetches the podcast's feed from the internet and downloads what is new"},
		"MeReaddSeriesFromContinueListening":  {Skip: "puts the series back on the shelf where listening continues"},
		"MeRemoveSeriesFromContinueListening": {Skip: "takes the series off the shelf where listening continues"},
		"MeRemoveItemFromContinueListening":   {Skip: "takes the book off the shelf where listening continues"},
		// the podcast routes take the show, and a podcast episode's progress and sessions are the show's with the episode
		"MeGetMediaProgress":         {Path: map[string]string{"id": show.ID}},
		"MeGetItemListeningSessions": {Path: map[string]string{"libraryItemId": show.ID}},
		"PodcastGetEpisode":          {Path: map[string]string{"id": show.ID}},
		"LibraryGetOPMLFile":         {Path: map[string]string{"id": shows}},
		"LibraryGetPodcastTitles":    {Path: map[string]string{"id": shows}},
		// recent episodes are none at all without a limit
		"LibraryGetRecentEpisodes": {Path: map[string]string{"id": shows}, Options: map[string]any{"Limit": 10}},

		// what a search or a download names, the searches with what the provider answers were recorded for
		"LibrarySearch":           {Options: map[string]any{"Q": book.Title()}},
		"LibraryDownloadMultiple": {Options: map[string]any{"Ids": book.ID}},
		"SearchFindAuthor":        {Options: map[string]any{"Q": "Isaac Asimov"}},
		"SearchFindBooks":         {Options: map[string]any{"Provider": "audible", "Title": "Foundation", "Author": "Isaac Asimov"}},
		"SearchFindCovers":        {Options: map[string]any{"Provider": "audible", "Title": "Foundation", "Author": "Isaac Asimov"}},
		"SearchFindPodcasts":      {Options: map[string]any{"Term": "Well There's Your Problem"}},

		// what the fixtures hold none of
		"LibraryItemGetEBookFile":        {Path: map[string]string{"fileid": book.Media.AudioFiles[0].Ino}, Status: http.StatusBadRequest, Why: "the fixture libraries hold no ebook, and an audio file is not one"},
		"LibraryItemGetEBookFile2":       {Status: http.StatusNotFound, Why: "the fixture libraries hold no ebook"},
		"PodcastFindEpisode":             {Status: http.StatusBadRequest, Why: "the fixture show has no feed to look its episodes up in"},
		"LibraryGetEpisodeDownloadQueue": {Path: map[string]string{"id": shows}, Empty: "no episode is downloading, which would fetch it from the internet"},
		"PodcastGetEpisodeDownloads":     {Empty: "no episode is downloading, which would fetch it from the internet"},
		"MiscGetTasks":                   {MayBeEmpty: "a task is listed only while it runs, such as a scan another test started"},
	}

	sweep.Sweep{Definitions: svc, Client: sdk, StatusCode: pclient.StatusCode, Fixtures: sweep.Fixtures{Path: fixtures}, Cases: cases, Strict: true}.Run(t)
}

// TestShelf reads both fixture libraries' shelves through the generated client: each shelf is the kind its type names, and holds no field the
// record of that kind lacks, which the sweep does not look inside a union for.
func TestShelf(t *testing.T) {
	ctx := skipUnlessLive(t)
	sdk := generated(t)

	seen := map[string]bool{}
	for _, lib := range []string{library(t), podcastLibrary(t)} {
		res, err := sdk.LibraryGetUserPersonalizedShelves(ctx, gen.LibraryID(lib))
		if err != nil {
			t.Fatal(err)
		}

		for _, shelf := range res.Answer {
			kind := shelf.Kind()
			seen[kind] = true
			switch kind {
			case "book", "episode", "podcast":
				_, err = shelf.AsItemShelf()
				if err == nil {
					_, err = pclient.DecodeStrict[gen.ItemShelf](shelf.Raw)
				}
			case "series":
				_, err = shelf.AsSeriesShelf()
				if err == nil {
					_, err = pclient.DecodeStrict[gen.SeriesShelf](shelf.Raw)
				}
			case "authors":
				_, err = shelf.AsAuthorShelf()
				if err == nil {
					_, err = pclient.DecodeStrict[gen.AuthorShelf](shelf.Raw)
				}
			default:
				err = errors.New("a kind the union does not name")
			}
			if err != nil {
				t.Errorf("a %q shelf of %s: %v", kind, lib, err)
			}
		}
	}

	t.Logf("kinds of shelf seen: %v", slices.Sorted(maps.Keys(seen)))
}

// generated is a client of the generated package for the suite's server, which records what it reads when the suite records.
func generated(t *testing.T, opts ...pclient.Option) *gen.Client {
	t.Helper()

	if rt := recorder.Transport(); rt != nil {
		opts = append(opts, pclient.WithTransport(rt))
	}
	c, err := gen.New(client.BaseURL(), os.Getenv("ABS_TOKEN"), opts...)
	if err != nil {
		t.Fatal(err)
	}

	return c
}
