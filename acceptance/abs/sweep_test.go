//go:build acceptance

package abs_test

// The read sweep: every GET of the API document, called against the running Audiobookshelf through the client generated from it, with
// arguments from this suite's fixtures. The other tests here make changes and read them back; the sweep proves the client reads every answer
// the server gives, and keeps doing so as the document changes: a GET the document gains is swept on the next run with nothing to write, and
// one that cannot answer must say why here.
//
// The sweep is pandorest's (github.com/katbyte/pandorest/sweep); what is here is the fixtures and the cases. A case whose operation starts
// answering fails the sweep, so a stale one is noticed and removed.

import (
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/katbyte/abs-mcp/sdk/abs"
	pclient "github.com/katbyte/pandorest/client"
	"github.com/katbyte/pandorest/definitions"
	"github.com/katbyte/pandorest/sweep"
)

// TestReadSweep calls every GET against the fixture libraries, with one of each thing Audiobookshelf reads by id made for it.
func TestReadSweep(t *testing.T) {
	ctx := skipUnlessLive(t)

	books, shows := library(t), podcastLibrary(t)
	me := must(client.MeGetCurrentUser(ctx))

	book, show := whole(ctx, firstItems(ctx, books, 1)[0].Id), whole(ctx, firstItems(ctx, shows, 1)[0].Id)
	if len(book.Media.AudioFiles) == 0 || len(show.Media.Episodes) == 0 {
		t.Fatalf("the fixtures are incomplete: %d audio files on %s, %d episodes on %s", len(book.Media.AudioFiles), book.Id, len(show.Media.Episodes), show.Id)
	}
	episode := show.Media.Episodes[0].Id

	authors := firstAuthors(ctx, books, 1)
	series := must(client.LibraryGetAllSeriesForLibrary(ctx, books, abs.LibraryGetAllSeriesForLibraryOptions{Limit: new(1)})).Results
	if len(authors) == 0 || len(series) == 0 {
		t.Fatalf("the fixtures are incomplete: %d authors, %d series", len(authors), len(series))
	}

	// what is read back by id is made here and removed after; an image the server fetches comes from the recorded provider answers
	cover, kind, err := form("cover", "cover.jpg", tinyJPEG(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := errorOf(client.LibraryItemUploadCover(ctx, book.Id, cover, kind)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = errorOf(client.LibraryItemRemoveCover(afterwards(t), book.Id)) })
	if err := errorOf(client.AuthorUploadImage(ctx, authors[0].Id, abs.AuthorUploadImageRequest{Url: "https://m.media-amazon.com/images/I/81Sr4Ms8SaL.jpg"})); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = errorOf(client.AuthorDeleteImage(afterwards(t), authors[0].Id)) })
	if err := errorOf(client.LibraryItemUpdateMedia(ctx, book.Id, abs.MediaUpdate{Metadata: &abs.MediaUpdateMetadata{Genres: []string{"Sweep"}}})); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = errorOf(client.LibraryItemUpdateMedia(afterwards(t), book.Id, abs.MediaUpdate{Metadata: &abs.MediaUpdateMetadata{Genres: append([]string{}, book.Media.Metadata.Genres...)}}))
	})
	provider := must(client.CustomMetadataProviderCreate(ctx, abs.CustomMetadataProviderCreateRequest{Name: "Sweep", Url: "http://localhost:1", MediaType: "book"})).Provider
	t.Cleanup(func() { _ = errorOf(client.CustomMetadataProviderDelete(afterwards(t), string(provider.Id))) })
	collection := must(client.CollectionCreate(ctx, abs.CollectionCreateRequest{LibraryId: books, Name: "Sweep", Books: []string{string(book.Id)}}))
	t.Cleanup(func() { _ = errorOf(client.CollectionDelete(afterwards(t), collection.Id)) })
	playlist := must(client.PlaylistCreate(ctx, abs.PlaylistCreateRequest{LibraryId: books, Name: "Sweep", Items: []abs.PlaylistEntry{{LibraryItemId: book.Id}}}))
	t.Cleanup(func() { _ = errorOf(client.PlaylistDelete(afterwards(t), playlist.Id)) })
	// an opened feed's answer leaves its slug out
	const slug = "sweep-feed"
	feed := must(client.RSSFeedOpenRSSFeedForItem(ctx, book.Id, abs.RSSFeedOpenRSSFeedForItemRequest{Slug: slug, ServerAddress: client.Client.BaseURL})).Feed
	t.Cleanup(func() { _ = errorOf(client.RSSFeedCloseRSSFeed(afterwards(t), string(feed.Id))) })
	share := must(client.ShareCreateMediaItemShare(ctx, abs.ShareCreateMediaItemShareRequest{MediaItemId: book.Media.Id, MediaItemType: abs.MediaItemTypeBook, Slug: "sweep-share", ExpiresAt: int(time.Now().Add(time.Hour).UnixMilli()), IsDownloadable: new(true)}))
	t.Cleanup(func() { _ = errorOf(client.ShareDeleteMediaItemShare(afterwards(t), share.Id)) })
	backups := must(client.BackupCreate(ctx)).Backups
	backup := backups[len(backups)-1].Id
	t.Cleanup(func() { _ = errorOf(client.BackupDelete(afterwards(t), backup)) })
	bookmark := strconv.FormatFloat(must(client.MeCreateBookmark(ctx, book.Id, abs.MeCreateBookmarkRequest{Time: 5, Title: "sweep"})).Time, 'f', -1, 64)
	t.Cleanup(func() { _ = errorOf(client.MeRemoveBookmark(afterwards(t), book.Id, bookmark)) })
	session := must(client.LibraryItemStartPlaybackSession(ctx, book.Id, abs.LibraryItemStartPlaybackSessionRequest{ForceDirectPlay: new(true), MediaPlayer: "sweep"}))
	t.Cleanup(func() { _ = errorOf(client.SessionClose(afterwards(t), session.Id, abs.SessionCloseRequest{})) })
	// time listened is what the listening stats count
	if err := errorOf(client.SessionSync(ctx, session.Id, abs.SessionSyncRequest{CurrentTime: 40, TimeListened: 10})); err != nil {
		t.Fatal(err)
	}

	// the fixture recordings last seconds, and the server counts anything within ten seconds of the end as finished, so the progress is set after the
	// sync, against an hour, to leave the book and the episode in progress
	inProgress := abs.ProgressUpdatePayload{CurrentTime: new(30.0), Duration: new(3600.0)}
	if err := errorOf(client.MeCreateUpdateMediaProgress2(ctx, book.Id, inProgress)); err != nil {
		t.Fatal(err)
	}
	if err := errorOf(client.MeCreateUpdateMediaProgress(ctx, show.Id, episode, inProgress)); err != nil {
		t.Fatal(err)
	}

	// a share's files are read in the share's own session, which opening the share starts and a cookie carries
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	sdk, err := connect(os.Getenv("ABS_TOKEN"), pclient.WithCookies(jar))
	if err != nil {
		t.Fatal(err)
	}
	if err := errorOf(sdk.ShareGetMediaItemShareBySlug(ctx, share.Slug)); err != nil {
		t.Fatal(err)
	}
	svc, err := definitions.Load(filepath.Join("..", "..", "api-defs", "audiobookshelf-"+abs.DocumentVersion))
	if err != nil {
		t.Fatal(err)
	}

	id := func(v any) string { return fmt.Sprint(v) }
	year := id(time.Now().Year())
	// a path parameter is resolved by the segments before it and its name, the most of them first, then by its name alone
	fixtures := map[string]string{
		"libraries/id":                     id(books),
		"series/seriesId":                  id(series[0].Id),
		"items/id":                         id(book.Id),
		"file/fileid":                      book.Media.AudioFiles[0].Ino,
		"ffprobe/fileid":                   book.Media.AudioFiles[0].Ino,
		"authors/id":                       id(authors[0].Id),
		"series/id":                        id(series[0].Id),
		"collections/id":                   id(collection.Id),
		"playlists/id":                     id(playlist.Id),
		"users/id":                         id(me.Id),
		"podcasts/id":                      id(show.Id),
		"episodeId":                        id(episode),
		"backups/id":                       backup,
		"bookmarks/libraryItemId":          id(book.Id),
		"listening-sessions/libraryItemId": id(book.Id),
		"progress/id":                      id(book.Id),
		"session/id":                       session.Id,
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
		"MeGetMediaProgress":         {Path: map[string]string{"id": id(show.Id)}},
		"MeGetItemListeningSessions": {Path: map[string]string{"libraryItemId": id(show.Id)}},
		"PodcastGetEpisode":          {Path: map[string]string{"id": id(show.Id)}},
		"LibraryGetOPMLFile":         {Path: map[string]string{"id": id(shows)}},
		"LibraryGetPodcastTitles":    {Path: map[string]string{"id": id(shows)}},
		// recent episodes are none at all without a limit
		"LibraryGetRecentEpisodes": {Path: map[string]string{"id": id(shows)}, Options: map[string]any{"Limit": 10}},

		// what a search or a download names, the searches with what the provider answers were recorded for
		"LibrarySearch":           {Options: map[string]any{"Q": book.Title()}},
		"LibraryDownloadMultiple": {Options: map[string]any{"Ids": id(book.Id)}},
		"SearchFindAuthor":        {Options: map[string]any{"Q": "Isaac Asimov"}},
		"SearchFindBooks":         {Options: map[string]any{"Provider": "audible", "Title": "Foundation", "Author": "Isaac Asimov"}},
		"SearchFindCovers":        {Options: map[string]any{"Provider": "audible", "Title": "Foundation", "Author": "Isaac Asimov"}},
		"SearchFindPodcasts":      {Options: map[string]any{"Term": "Well There's Your Problem"}},

		// what the fixtures hold none of
		"LibraryItemGetEBookFile":        {Path: map[string]string{"fileid": book.Media.AudioFiles[0].Ino}, Status: http.StatusBadRequest, Why: "the fixture libraries hold no ebook, and an audio file is not one"},
		"LibraryItemGetEBookFile2":       {Status: http.StatusNotFound, Why: "the fixture libraries hold no ebook"},
		"PodcastFindEpisode":             {Status: http.StatusBadRequest, Why: "the fixture show has no feed to look its episodes up in"},
		"LibraryGetEpisodeDownloadQueue": {Path: map[string]string{"id": id(shows)}, Empty: "no episode is downloading, which would fetch it from the internet"},
		"PodcastGetEpisodeDownloads":     {Empty: "no episode is downloading, which would fetch it from the internet"},
		"MiscGetTasks":                   {MayBeEmpty: "a task is listed only while it runs, such as a scan another test started"},
	}

	sweep.Sweep{Definitions: svc, Client: sdk, StatusCode: pclient.StatusCode, Fixtures: sweep.Fixtures{Path: fixtures}, Cases: cases, Strict: true}.Run(t)
}

// TestShelf reads both fixture libraries' shelves through the generated client: each shelf is the kind its type names, and holds no field the
// record of that kind lacks, which the sweep does not look inside a union for.
func TestShelf(t *testing.T) {
	ctx := skipUnlessLive(t)

	seen := map[string]bool{}
	for _, lib := range []abs.LibraryID{library(t), podcastLibrary(t)} {
		res, err := client.LibraryGetUserPersonalizedShelves(ctx, lib)
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
					_, err = pclient.DecodeStrict[abs.ItemShelf](shelf.Raw)
				}
			case "series":
				_, err = shelf.AsSeriesShelf()
				if err == nil {
					_, err = pclient.DecodeStrict[abs.SeriesShelf](shelf.Raw)
				}
			case "authors":
				_, err = shelf.AsAuthorShelf()
				if err == nil {
					_, err = pclient.DecodeStrict[abs.AuthorShelf](shelf.Raw)
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
