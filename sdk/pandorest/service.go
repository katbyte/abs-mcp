package main

import (
	"slices"
	"strings"

	"github.com/katbyte/pandorest"
	"github.com/katbyte/pandorest/config"
	"github.com/katbyte/pandorest/importer/workarounds"
)

// serviceName is the name the document, its definitions and every fix carry.
const serviceName = "audiobookshelf"

// services is the one API generated here, from the document spec writes.
var services = []config.Service{{
	Name:    serviceName,
	Package: "abs",
	// in place of the hand-written client, which is at sdk/abs-byhand until the tools move over
	Output: "sdk/abs",
	// the document is this repository's own, so each operationId is chosen: the handler that answers it, LibraryGetLibraryItems
	Naming: config.OperationIDNaming,
	Auth:   "Bearer",
	// the ids that travel between calls are told apart by the compiler
	IDs:   absIDs,
	Plain: absPlainIDs,
	// lists page by limit and page and answer results and total, but count pages from 0, which pandorest's pager does not, so none is
	// declared a list yet
}}

// fixes say what the document spec writes cannot: the source holds the value as a number and the server means a date by it.
var fixes = []workarounds.Workaround{
	// a record's times, a file's times and a progress's last update are milliseconds since 1970, read as the dates they are
	workarounds.UndeclaredUnixDates(workarounds.About{Name: "audiobookshelf-unix-dates", Service: serviceName, Bug: "times are written as milliseconds since 1970, which the source holds as plain numbers"}, "milliseconds", "addedAt", "birthtimeMs", "createdAt", "ctimeMs", "entityUpdatedAt", "finishedAt", "lastEpisodeCheck", "lastScan", "lastSeen", "lastUpdate", "loadedAt", "mtimeMs", "progressLastUpdate", "publishedAt", "startedAt", "updatedAt"),
}

// generation is what sdk/abs is generated from.
func generation() pandorest.Config {
	return pandorest.Config{Services: services, Workarounds: fixes}
}

// absIDs are the kinds of id that get a type of their own, so one passed where another belongs does not compile. Every id is text.
var absIDs = []config.ID{
	{Name: "LibraryID", Of: []string{"Library"}, Names: []string{"libraryId", "userDefaultLibraryId"}, Except: untypedIDs("libraryId")},
	// a collection takes its books, and a feed is opened on a book, by the book's library item id
	{Name: "LibraryItemID", Of: []string{"LibraryItem", "LibraryItemExpanded", "LibraryItemMinified"}, Names: []string{"libraryItemId", "libraryItemIds"}, In: []string{"DELETE /api/collections/{id}/book/{bookId} bookId", "POST /api/feeds/item/{itemId}/open itemId"}, Except: untypedIDs("libraryItemId", "libraryItemIds")},
	{Name: "AuthorID", Of: []string{"Author"}, Names: []string{"authorId"}},
	// a library's series are under the library's own id
	{Name: "SeriesID", Of: []string{"Series"}, Names: []string{"seriesId"}, Except: []string{"GET /api/libraries/{id}/series/{seriesId} id"}},
	{Name: "CollectionID", Of: []string{"Collection"}, Names: []string{"collectionId"}},
	{Name: "PlaylistID", Of: []string{"Playlist"}, Names: []string{"playlistId"}},
	// a podcast's episodes are under the podcast's library item id
	{Name: "EpisodeID", Of: []string{"PodcastEpisode"}, Names: []string{"episodeId"}, Except: append([]string{"DELETE /api/podcasts/{id}/episode/{episodeId} id", "GET /api/podcasts/{id}/episode/{episodeId} id", "PATCH /api/podcasts/{id}/episode/{episodeId} id"}, untypedIDs("episodeId")...)},
	{Name: "FeedID", Of: []string{"Feed", "FeedMinified"}, Names: []string{"feedId"}},
	{Name: "FolderID", Of: []string{"LibraryFolder"}, Names: []string{"folderId"}},
	{Name: "UserID", Of: []string{"UserForBrowser"}, Names: []string{"userId", "createdByUserId"}, Except: untypedIDs("userId")},
	{Name: "APIKeyID", Of: []string{"ApiKeyRecord"}},
	{Name: "ProviderID", Of: []string{"CustomMetadataProviderRecord"}},
}

// untypedByScan are the places an id is read or written that the scan of the source could not type: body fields, a playback session the code
// fills in after making it, a device copied field by field, a progress kept as JSON, and the books a database query joins to a series.
// pandorest types no id that has no type, so each is left out of its kind until it has one.
var untypedByScan = []string{
	"ApiKeyUpdateRequest .userId",
	"DeviceInfo .userId",
	"LibraryItemMinifiedCollapsedSeries .libraryItemIds",
	"MeGetMediaProgressAnswer .libraryItemId",
	"PlaybackSessionForClient .episodeId",
	"PlaybackSessionForClient .libraryId",
	"PlaybackSessionForClient .libraryItemId",
	"PlaybackSessionForClient .userId",
	"SessionSyncLocalRequest .episodeId",
	"SessionSyncLocalRequest .libraryId",
	"SessionSyncLocalRequest .libraryItemId",
	"SessionSyncLocalRequest .userId",
}

// untypedIDs are the places in untypedByScan of the names given.
func untypedIDs(names ...string) []string {
	var out []string
	for _, p := range untypedByScan {
		if _, name, _ := strings.Cut(p, " ."); slices.Contains(names, name) {
			out = append(out, p)
		}
	}

	return out
}

// absPlainIDs are the names that look like ids and are left as plain text on purpose: a record's own id where no kind claims it, such as a
// book's or a device's, a file's inode, another service's ids, such as a podcast feed's guid, and ids that name one of several kinds of record.
var absPlainIDs = []string{"id", "ids", "fileid", "itunesArtistId", "itunesId", "guid", "deviceId", "entityId", "mediaItemId", "bookId", "narratorId", "podcastId"}
