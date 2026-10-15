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
	// what is Audiobookshelf's own about being talked to is sdk/abs/service.go, written by hand beside the generated files
	ClientService: "service",
	// New is worded for Audiobookshelf: its credential is an API key, or a user's token, sent as a bearer token
	Credential: "API key",
	NewDoc:     "New returns a client for the Audiobookshelf server at baseURL (scheme and host) that authenticates with an API key, made under\nSettings → Users → API Keys, or a user's token. The options are the base client's: client.WithLog to trace what is sent,\nclient.WithTransport to send it through a transport of the caller's own.",
	Notes:      "Audiobookshelf writes its times as milliseconds since 1970, read here as dates, and counts the pages of a list from 0. Its ids are\nuuids, except on a server upgraded from an old version, which may still hold ids of an older form.",
	// the ids that travel between calls are told apart by the compiler
	IDs:   absIDs,
	Plain: absPlainIDs,
	// lists take a page, counted from 0, and a limit, and answer results and total; lists of listening sessions take itemsPerPage and
	// answer sessions
	Paging:  &config.Paging{Start: "page", Limit: "limit", Items: "results", Total: "total", ByPage: true, FirstPage: new(0)},
	Pagings: []config.Paging{{Start: "page", Limit: "itemsPerPage", Items: "sessions", Total: "total", ByPage: true, FirstPage: new(0)}},
	// a value of several kinds, such as a library's shelves, is a type that hands over the kind asked for, checked against the field that names it
	TypedUnions: true,
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
	// a collection takes its books, and a feed is opened on a book, by the book's library item id; a book or a show is read and changed by it
	// under /api/items and /api/tools/item, whose answers are built route by route and name no record to find it by
	{Name: "LibraryItemID", Of: []string{"LibraryItem", "LibraryItemExpanded", "LibraryItemMinified"}, Names: []string{"libraryItemId", "libraryItemIds"}, In: append([]string{"DELETE /api/collections/{id}/book/{bookId} bookId", "POST /api/feeds/item/{itemId}/open itemId"}, pathIDs("id", itemRoutes...)...), Except: untypedIDs("libraryItemId", "libraryItemIds")},
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

// itemRoutes are the routes that take a book or a show by its library item id as {id}, other than the one whose answer names the item.
var itemRoutes = []string{
	"DELETE /api/items/{id}",
	"GET /api/items/{id}",
	"POST /api/items/{id}/chapters",
	"DELETE /api/items/{id}/cover",
	"GET /api/items/{id}/cover",
	"PATCH /api/items/{id}/cover",
	"POST /api/items/{id}/cover",
	"GET /api/items/{id}/download",
	"GET /api/items/{id}/ebook",
	"GET /api/items/{id}/ebook/{fileid}",
	"PATCH /api/items/{id}/ebook/{fileid}/status",
	"GET /api/items/{id}/ffprobe/{fileid}",
	"DELETE /api/items/{id}/file/{fileid}",
	"GET /api/items/{id}/file/{fileid}",
	"GET /api/items/{id}/file/{fileid}/download",
	"POST /api/items/{id}/match",
	"PATCH /api/items/{id}/media",
	"GET /api/items/{id}/metadata-object",
	"POST /api/items/{id}/play",
	"POST /api/items/{id}/play/{episodeId}",
	"POST /api/items/{id}/scan",
	"POST /api/tools/item/{id}/embed-metadata",
	"DELETE /api/tools/item/{id}/encode-m4b",
	"POST /api/tools/item/{id}/encode-m4b",
}

// pathIDs are the places of one path parameter in each of the routes, in the form an id kind's In takes.
func pathIDs(param string, routes ...string) []string {
	out := make([]string, 0, len(routes))
	for _, r := range routes {
		out = append(out, r+" "+param)
	}

	return out
}

// untypedByScan are the places an id is read or written that neither the source nor the answers the live suites saved can type, as the suites
// never do what fills them: move a key to another user, collapse series, play a podcast episode, or sync a session played offline. pandorest
// types no id that has no type, so each is left out of its kind until it has one. A share's session is never given a user or an episode, so its
// two are always empty.
var untypedByScan = []string{
	"ApiKeyUpdateRequest .userId",
	"LibraryItemMinifiedCollapsedSeries .libraryItemIds",
	"PlaybackSessionForClient .episodeId",
	"SessionGetOpenSessionsAnswerShareSessions .episodeId",
	"SessionGetOpenSessionsAnswerShareSessions .userId",
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
