package main

import (
	"errors"
	"fmt"
	"strings"
)

// valueAt is a place the scan sees but cannot type, or does not see at all, and what the source says it holds.
type valueAt struct {
	// at is a place as recordsAt names one: a record, or an operation and status or body, then a path into it
	at string
	// value is a type written short: string, integer, number, boolean, any, or open for a record of any keys; [] before it for a list of
	// them, ? after it for one that may be null
	value string
}

// valuesAt are the values the source sets through something the scan cannot follow: a typedef, a list of keys it loops over, a value it only
// ever writes as null, or a field it fills from another object. Each says where in the source it comes from.
var valuesAt = []valueAt{
	// libraryItemsBookFilters.getFilteredLibraryItems: a collapsed series' id and name, from the Series model
	{"LibraryItemMinified .collapsedSeries.id", "string"},
	{"LibraryItemMinified .collapsedSeries.name", "string"},
	// models/Book.js AudioFileObject: discNumFromMeta is a number; AudioFile.error is what utils/prober.js met reading the file, as text
	{"AudioFile .discNumFromMeta", "integer?"},
	{"AudioFile .error", "string?"},
	// objects/settings/EmailSettings.js: null until the settings are sent them
	{"EmailSettings .host", "string?"},
	{"EmailSettings .testAddress", "string?"},
	{"EmailSettings .user", "string?"},
	// models/Library.js LibrarySettingsObject: a percent, null when the time left decides when a book is finished
	{"Library .settings.markAsFinishedPercentComplete", "number?"},
	{"GET /api/libraries/{id} 200 .settings.markAsFinishedPercentComplete", "number?"},
	// libraryFilters.getFilterData: sets of each book's language and publisher, and the decade of its year as text
	{"LibraryFilterData .languages[]", "string"},
	{"LibraryFilterData .publishedDecades[]", "string"},
	{"LibraryFilterData .publishers[]", "string"},
	// objects/PodcastEpisodeDownload.js: finishedAt is Date.now() once done; startedAt is never set in 2.37.1, so is always null, and is
	// the time it names
	{"PodcastEpisodeDownload .finishedAt", "integer?"},
	{"PodcastEpisodeDownload .startedAt", "integer?"},
	// seriesFilters.getFilteredSeries: SUM(b.duration), the series' length in seconds, sent when the list is sorted by it
	{"Series .totalDuration", "number"},
	// objects/DeviceInfo.js setData: what ua-parser names, or null
	{"DeviceInfo .browserName", "string?"},
	{"DeviceInfo .browserVersion", "string?"},
	{"DeviceInfo .deviceType", "string?"},
	{"DeviceInfo .osName", "string?"},
	{"DeviceInfo .osVersion", "string?"},
	// models/PlaybackSession.js getOldPlaybackSession: the episode's id for a podcast episode, null for a book
	{"PlaybackSession .episodeId", "string?"},
	{"PlaybackSessionForClient .episodeId", "string?"},
	// objects/settings/NotificationSettings.js and objects/Notification.js: null until set
	{"NotificationSettings .appriseApiUrl", "string?"},
	{"Notification .libraryId", "string?"},
	// AudioMetadataManager.getQueuedTaskData: each waiting embed's task data, which names its item
	{"GET /api/tasks 200 .queuedTaskData.embedMetadata[].libraryItemId", "string"},
	// SocketAuthority.getUsersOnline: each account as User.toJSONForPublic writes it; its session is in recordsAt
	{"GET /api/users/online 200 .usersOnline[].id", "string"},
	{"GET /api/users/online 200 .usersOnline[].username", "string"},
	{"GET /api/users/online 200 .usersOnline[].type", "string"},
	{"GET /api/users/online 200 .usersOnline[].lastSeen", "integer?"},
	{"GET /api/users/online 200 .usersOnline[].createdAt", "integer"},
	// BookFinder's providers each write an id of their own kind, which goTypesAt holds
	{"GET /api/search/books 200 .[].id", "any"},
	// podcastUtils.getPodcastFeed: podcast.numEpisodes = channel.item.length
	{"POST /api/podcasts/feed 200 .podcast.numEpisodes", "integer"},

	// Podcast.updateFromRequest takes the text of each key in its stringKeys, or null, and Book.updateFromRequest publishedDate from its
	// metadataStringKeys
	{"MediaUpdate .metadata.author", "string?"},
	{"MediaUpdate .metadata.feedUrl", "string?"},
	{"MediaUpdate .metadata.imageUrl", "string?"},
	{"MediaUpdate .metadata.itunesArtistId", "string?"},
	{"MediaUpdate .metadata.itunesId", "string?"},
	{"MediaUpdate .metadata.itunesPageUrl", "string?"},
	{"MediaUpdate .metadata.publishedDate", "string?"},
	{"MediaUpdate .metadata.releaseDate", "string?"},
	{"MediaUpdate .metadata.type", "string?"},
	// models/User.js ProgressUpdatePayload
	{"ProgressUpdatePayload .ebookLocation", "string?"},
	{"ProgressUpdatePayload .ebookProgress", "number?"},
	{"ProgressUpdatePayload .episodeId", "string"},
	// LibraryController.update: the text of each of its keysToCheck, the settings it is sent merged over the library's own
	{"PATCH /api/libraries/{id} body .icon", "string?"},
	{"PATCH /api/libraries/{id} body .mediaType", "string?"},
	{"PATCH /api/libraries/{id} body .settings", "open"},
	// LibraryController.create: icon, defaulting to database
	{"POST /api/libraries body .icon", "string"},
	// LibraryItemController.match: the text of each of its matchOptions
	{"POST /api/items/{id}/match body .isbn", "string"},
	// UserController.create and update
	{"POST /api/users body .email", "string"},
	{"PATCH /api/users/{id} body .librariesAccessible", "[]string"},
	{"PATCH /api/users/{id} body .username", "string?"},
	// EmailController.sendEBookToDevice
	{"POST /api/emails/send-ebook-to-device body .deviceName", "string"},
	{"POST /api/emails/send-ebook-to-device body .libraryItemId", "string"},
	// PlaybackSessionManager.syncLocalSessionsRequest: each session a client kept while away, as it sends it
	{"POST /api/session/local-all body .sessions", "[]open"},
	// MiscController.updateWatchedPath
	{"POST /api/watcher/update body .libraryId", "string"},
}

// applyValues types each place the document leaves untyped, making it where the record lacks it, and returns the places whose record or
// operation the document does not have, and the places that were typed already: a statement the scan or the answers have caught up with is
// to be removed. A place inside them that is not as stated is an error, so the statement is mended when the source changes.
func applyValues(doc *document, all []valueAt) (missing, typed []string, err error) {
	for _, v := range all {
		s, err := valueSchema(v.value)
		if err != nil {
			return nil, nil, fmt.Errorf("value at %s: %w", v.at, err)
		}
		root, path, err := placeRoot(doc, v.at)
		if err != nil {
			return nil, nil, err
		}
		if root == nil {
			missing = append(missing, v.at)
			continue
		}

		have, err := schemaAt(*root, path)
		if err == nil && !untyped(have) {
			typed = append(typed, v.at)
			continue
		}
		// a place the scan saw only as null stays one that may be
		s.Nullable = s.Nullable || err == nil && have.Nullable

		if _, err := setAt(root, path, s); errors.Is(err, errInNamedRecord) {
			missing = append(missing, v.at)
			continue
		} else if err != nil {
			return nil, nil, fmt.Errorf("value at %s: %w", v.at, err)
		}
	}

	return missing, typed, nil
}

// valueSchema is the schema a type written short stands for.
func valueSchema(value string) (*schema, error) {
	t, nullable := strings.CutSuffix(value, "?")

	var s *schema
	if item, list := strings.CutPrefix(t, "[]"); list {
		items, err := valueSchema(item)
		if err != nil {
			return nil, err
		}
		s = &schema{Type: "array", Items: items}
	} else {
		switch t {
		case "string", "integer", "number", "boolean":
			s = &schema{Type: t}
		case "any":
			s = &schema{}
		case "open":
			s = &schema{Type: "object", AdditionalProperties: true}
		default:
			return nil, fmt.Errorf("%q is not a type: string, integer, number, boolean, any or open, with [] before and ? after", value)
		}
	}
	s.Nullable = nullable

	return s, nil
}
