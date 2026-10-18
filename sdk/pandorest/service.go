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
	// an answer of plain text, such as the OK of a delete, is read and handed back as text, so a caller that only checks the error leaves no
	// connection open; files stay for the caller to read
	TextAnswers: true,
	// the fields an edit sends empty to clear, which a pointer tells from one left as it is
	KeepNull: keepNull,
	// a field the document requires is sent even when it is zero, as the server refuses a share made without its expiry, 0 for none
	SendRequired: true,
}}

// keepNull are the fields of the edits the tools make where empty or zero is a value rather than no change: an author's description and asin,
// and every text field of an item's metadata, cleared when sent empty; a progress's position, cleared by 0; and a podcast's episodes to keep
// and to fetch a check, all of them at 0.
var keepNull = []string{
	"AuthorUpdateRequest.asin", "AuthorUpdateRequest.description",
	"ProgressUpdatePayload.currentTime", "ProgressUpdatePayload.duration", "ProgressUpdatePayload.ebookLocation", "ProgressUpdatePayload.ebookProgress", "ProgressUpdatePayload.progress",
	"MediaUpdate.maxEpisodesToKeep", "MediaUpdate.maxNewEpisodesToDownload",
	"MediaUpdateMetadata.asin", "MediaUpdateMetadata.author", "MediaUpdateMetadata.description", "MediaUpdateMetadata.feedUrl", "MediaUpdateMetadata.imageUrl", "MediaUpdateMetadata.isbn", "MediaUpdateMetadata.itunesId",
	"MediaUpdateMetadata.language", "MediaUpdateMetadata.publishedDate", "MediaUpdateMetadata.publishedYear", "MediaUpdateMetadata.publisher", "MediaUpdateMetadata.releaseDate", "MediaUpdateMetadata.subtitle", "MediaUpdateMetadata.title", "MediaUpdateMetadata.type",
}

// fixes say what the document spec writes cannot: the source holds the value as a number and the server means a date by it.
var fixes = []workarounds.Workaround{
	// a record's times, a file's times and a progress's last update are milliseconds since 1970, read as the dates they are
	workarounds.UndeclaredUnixDates(workarounds.About{Name: "audiobookshelf-unix-dates", Service: serviceName, Bug: "times are written as milliseconds since 1970, which the source holds as plain numbers"}, "milliseconds", unixTimes...),
}

// unixTimes are the fields Audiobookshelf writes a time in as milliseconds since 1970, which the generated client reads as dates.
var unixTimes = []string{"addedAt", "birthtimeMs", "createdAt", "ctimeMs", "entityUpdatedAt", "finishedAt", "lastEpisodeCheck", "lastScan", "lastSeen", "lastUpdate", "loadedAt", "mtimeMs", "progressLastUpdate", "publishedAt", "startedAt", "updatedAt"}

// generation is what sdk/abs is generated from.
func generation() pandorest.Config {
	return pandorest.Config{Services: services, Workarounds: fixes}
}

// absIDs are the kinds of id that get a type of their own, so one passed where another belongs does not compile. Every id is text.
var absIDs = []config.ID{
	{Name: "LibraryID", Of: []string{"Library"}, Names: []string{"libraryId", "userDefaultLibraryId"}, Except: untypedIDs("libraryId")},
	// a collection takes its books, a feed is opened on a book, and the tools work on a book, by the book's library item id
	{Name: "LibraryItemID", Of: []string{"LibraryItem", "LibraryItemExpanded", "LibraryItemMinified", "ListeningStatsItem"}, Names: []string{"libraryItemId", "libraryItemIds"}, In: slices.Concat([]string{"DELETE /api/collections/{id}/book/{bookId} bookId", "POST /api/feeds/item/{itemId}/open itemId"}, pathIDs(itemRoutes...), pathIDs(meItemRoutes...)), Except: untypedIDs("libraryItemId", "libraryItemIds")},
	{Name: "AuthorID", Of: []string{"Author", "AuthorMinimal"}, Names: []string{"authorId"}},
	// a library's series are under the library's own id
	{Name: "SeriesID", Of: []string{"Series", "SeriesMinimal"}, Names: []string{"seriesId"}, In: pathIDs(meSeriesRoutes...), Except: []string{"GET /api/libraries/{id}/series/{seriesId} id"}},
	{Name: "CollectionID", Of: []string{"Collection"}, Names: []string{"collectionId"}},
	{Name: "PlaylistID", Of: []string{"Playlist"}, Names: []string{"playlistId"}},
	// a podcast's episodes are under the podcast's library item id
	{Name: "EpisodeID", Of: []string{"PodcastEpisode"}, Names: []string{"episodeId"}, Except: append([]string{"DELETE /api/podcasts/{id}/episode/{episodeId} id", "GET /api/podcasts/{id}/episode/{episodeId} id", "PATCH /api/podcasts/{id}/episode/{episodeId} id"}, untypedIDs("episodeId")...)},
	{Name: "FeedID", Of: []string{"Feed", "FeedMinified"}, Names: []string{"feedId"}},
	{Name: "FolderID", Of: []string{"LibraryFolder"}, Names: []string{"folderId"}},
	// the account's own routes are under /api/me, which answers the user, but their {id} is something of the user's: a book, a series, a
	// progress record or a session
	{Name: "UserID", Of: []string{"UserForBrowser", "PlaybackSessionUser"}, Names: []string{"userId", "createdByUserId"}, Except: slices.Concat(untypedIDs("userId"), pathIDs(meItemRoutes...), pathIDs(meSeriesRoutes...), pathIDs(meOwnRoutes...))},
	{Name: "APIKeyID", Of: []string{"ApiKeyRecord"}},
	{Name: "ProviderID", Of: []string{"CustomMetadataProviderRecord"}},
}

// itemRoutes are the routes under /api/tools/item that take a book by its library item id as {id}, whose answers name no record to find it by.
var itemRoutes = []string{
	"POST /api/tools/item/{id}/embed-metadata",
	"DELETE /api/tools/item/{id}/encode-m4b",
	"POST /api/tools/item/{id}/encode-m4b",
}

// meItemRoutes take a book by its library item id as {id}, meSeriesRoutes a series by its id, and meOwnRoutes a progress record or a
// session of the account's own by its id, which no kind claims.
var (
	meItemRoutes = []string{
		"POST /api/me/item/{id}/bookmark",
		"PATCH /api/me/item/{id}/bookmark",
		"DELETE /api/me/item/{id}/bookmark/{time}",
		"GET /api/me/progress/{id}",
		"GET /api/me/progress/{id}/{episodeId}",
	}
	meSeriesRoutes = []string{
		"GET /api/me/series/{id}/readd-to-continue-listening",
		"GET /api/me/series/{id}/remove-from-continue-listening",
	}
	meOwnRoutes = []string{
		"DELETE /api/me/progress/{id}",
		"GET /api/me/progress/{id}/remove-from-continue-listening",
		"DELETE /api/me/sessions/{id}",
	}
)

// pathIDs are the places of each route's {id}, in the form an id kind's In takes.
func pathIDs(routes ...string) []string {
	out := make([]string, 0, len(routes))
	for _, r := range routes {
		out = append(out, r+" id")
	}

	return out
}

// untypedByScan are the places an id is read or written that neither the source nor the answers the live suites saved can type, as the suites
// never do what fills them: move a key to another user, collapse series, play a podcast episode, or sync a session played offline; and a
// playlist's library and owner, which its update reads only to refuse. pandorest types no id that has no type, so each is left out of its kind
// until it has one.
var untypedByScan = []string{
	"ApiKeyUpdateRequest .userId",
	"LibraryItemMinifiedCollapsedSeries .libraryItemIds",
	"PlaybackSessionForClient .episodeId",
	"PlaylistUpdateRequest .libraryId",
	"PlaylistUpdateRequest .userId",
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
