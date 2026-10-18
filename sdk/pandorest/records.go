package main

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// errInNamedRecord is a place whose path goes into a record the document names, where the operation's answer is that record and not one of
// its own: the place is not in this document, as when the server the document is read from answers with the record alone.
var errInNamedRecord = errors.New("the place is inside a record the document names")

// recordAt is a place the scan cannot follow and the record the source writes there.
type recordAt struct {
	// at is a record of the document, or an operation and status, then a path into it: "Author .libraryItems[]", "GET /api/session/{id} 200 ."
	at     string
	record string
}

// recordsAt name the record at each place where the source writes one through something the scan cannot follow, an object a middleware
// attaches or a loader it cannot read. There the document refers to the record, rather than keeping a copy of what one answer happened to
// show, and what the server sends there that the record lacks is added to it.
var recordsAt = []recordAt{
	// AuthorController.findOne: libraryItems.map((li) => li.toOldJSONMinified())
	{"Author .libraryItems[]", "LibraryItemMinified"},
	// Playlist.toOldJSONExpanded: a book as libraryItem.toOldJSONExpanded(), an episode's show as toOldJSONMinified(), which the expanded
	// record holds every field of; the episode as pmi.mediaItem.toOldJSONExpanded(libraryItem.id)
	{"Playlist .items[].libraryItem", "LibraryItemExpanded"},
	{"Playlist .items[].episode", "PodcastEpisode"},
	// LibraryController.getUserPlaylistsForLibrary: the playlists as playlist.toOldJSONExpanded()
	{"GET /api/libraries/{id}/playlists 200 .results[]", "Playlist"},
	// libraryItemsBookFilters.search: a series' books as libraryItem.toOldJSON()
	{"GET /api/libraries/{id}/search 200 .series[].books[]", "LibraryItem"},
	// libraryItemsPodcastFilters.getRecentEpisodes: ep.toOldJSONExpanded(...) with its show and library
	{"GET /api/libraries/{id}/recent-episodes 200 .episodes[]", "PodcastEpisode"},
	// SessionController.getOpenSession: req.playbackSession.toJSONForClient(libraryItem)
	{"GET /api/session/{id} 200 .", "PlaybackSessionForClient"},
	// SessionController.getAllWithUserData and UserController.getListeningSessions: the session object spread whole, with its user
	{"GET /api/sessions 200 .sessions[]", "PlaybackSessionForClient"},
	{"GET /api/users/{id}/listening-sessions 200 .sessions[]", "PlaybackSessionForClient"},
	// SessionController.getOpenSessions: { ...se.toJSON(), user }, and the share sessions' se.toJSON()
	{"GET /api/sessions/open 200 .sessions[]", "PlaybackSession"},
	{"GET /api/sessions/open 200 .shareSessions[]", "PlaybackSession"},
	// UserController.getOnlineUsers: the open sessions as they are held, written by their toJSON
	{"GET /api/users/online 200 .openSessions[]", "PlaybackSession"},
	// UserController.findAll: a user's latest session from Database.getPlaybackSessions, written by its toJSON
	{"UserForBrowser .latestSession", "PlaybackSession"},
	// ShareController.getMediaItemShareBySlug: the share as ShareManager holds it for clients, with its playback session as toJSONForClient
	// writes it; and an item's share
	{"GET /public/share/{slug} 200 .", "MediaItemShareForClient"},
	{"LibraryItemExpanded .mediaItemShare", "MediaItemShareForClient"},
	{"MediaItemShareForClient .playbackSession", "PlaybackSessionForClient"},

	// the stored records, outer ones first, so the records inside them are stated at the one record rather than at each copy: a book's audio
	// files and an episode's, and the tracks Book.getTracklist and PodcastEpisode make of them for playing
	{"LibraryItem .media.audioFiles[]", "AudioFile"},
	{"LibraryItemExpanded .media.audioFiles[]", "AudioFile"},
	{"PodcastEpisode .audioFile", "AudioFile"},
	{"LibraryItemExpanded .media.tracks[]", "AudioTrack"},
	{"PlaybackSessionForClient .audioTracks[]", "AudioTrack"},
	{"PodcastEpisode .audioTrack", "AudioTrack"},
	// a book's ebook file, and an item's files
	{"LibraryItem .media.ebookFile", "EBookFile"},
	{"LibraryItemExpanded .media.ebookFile", "EBookFile"},
	{"LibraryItem .libraryFiles[]", "LibraryFile"},
	{"LibraryItemExpanded .libraryFiles[]", "LibraryFile"},
	// what each file carries
	{"AudioFile .metadata", "FileMetadata"},
	{"AudioTrack .metadata", "FileMetadata"},
	{"EBookFile .metadata", "FileMetadata"},
	{"LibraryFile .metadata", "FileMetadata"},
	{"AudioFile .metaTags", "AudioMetaTags"},
	{"AudioTrack .metaTags", "AudioMetaTags"},
	// chapters: a book's, an episode's and an audio file's own as stored, a session's copy of the book's, and those a client sends
	{"AudioFile .chapters[]", "Chapter"},
	{"AudioTrack .chapters[]", "Chapter"},
	{"LibraryItem .media.chapters[]", "Chapter"},
	{"LibraryItemExpanded .media.chapters[]", "Chapter"},
	{"PodcastEpisode .chapters[]", "Chapter"},
	{"PlaybackSession .chapters[]", "Chapter"},
	{"PlaybackSessionForClient .chapters[]", "Chapter"},
	{"POST /api/items/{id}/chapters body .chapters[]", "Chapter"},
	// a book's authors and series, in its metadata, in a session's copy of it, and as a client sends them back
	{"LibraryItem .media.metadata.authors[]", "AuthorMinimal"},
	{"LibraryItemExpanded .media.metadata.authors[]", "AuthorMinimal"},
	{"PlaybackSession .mediaMetadata.authors[]", "AuthorMinimal"},
	{"PlaybackSessionForClient .mediaMetadata.authors[]", "AuthorMinimal"},
	{"PATCH /api/items/{id}/media body .metadata.authors[]", "AuthorMinimal"},
	{"POST /api/items/batch/update body .[].mediaPayload.metadata.authors[]", "AuthorMinimal"},
	{"LibraryItem .media.metadata.series[]", "SeriesMinimal"},
	{"LibraryItemExpanded .media.metadata.series[]", "SeriesMinimal"},
	{"LibraryItemMinified .media.metadata.series", "SeriesMinimal"},
	{"PlaybackSession .mediaMetadata.series[]", "SeriesMinimal"},
	{"PlaybackSessionForClient .mediaMetadata.series[]", "SeriesMinimal"},
	{"PATCH /api/items/{id}/media body .metadata.series[]", "SeriesMinimal"},
	{"POST /api/items/batch/update body .[].mediaPayload.metadata.series[]", "SeriesMinimal"},
	// a user's progress in an item, beside the item and on its own
	{"LibraryItemExpanded .userMediaProgress", "MediaProgress"},
	{"GET /api/me/progress 200 .mediaProgress[]", "MediaProgress"},
	{"GET /api/me/progress/{id} 200 .", "MediaProgress"},
	{"GET /api/me/progress/{id}/{episodeId} 200 .", "MediaProgress"},
	// and an account's, which UserController.findOne writes with each item's title and cover beside it
	{"UserForBrowser .mediaProgress[]", "MediaProgress"},
	// a user's bookmarks
	{"UserForBrowser .bookmarks[]", "AudioBookmark"},
	{"GET /api/me/bookmarks 200 .bookmarks[]", "AudioBookmark"},
	{"GET /api/me/bookmarks/{libraryItemId} 200 .bookmarks[]", "AudioBookmark"},
	{"POST /api/me/item/{id}/bookmark 200 .", "AudioBookmark"},
	{"PATCH /api/me/item/{id}/bookmark 200 .", "AudioBookmark"},
	// the e-reader devices, in the server's settings, as an admin or a user sets them, and as a login answers them
	{"EmailSettings .ereaderDevices[]", "EreaderDevice"},
	{"POST /api/authorize 200 .ereaderDevices[]", "EreaderDevice"},
	{"POST /api/emails/ereader-devices body .ereaderDevices[]", "EreaderDevice"},
	{"POST /api/emails/ereader-devices 200 .ereaderDevices[]", "EreaderDevice"},
	{"POST /api/me/ereader-devices body .ereaderDevices[]", "EreaderDevice"},
	{"POST /api/me/ereader-devices 200 .ereaderDevices[]", "EreaderDevice"},
	// a podcast feed's episodes as utils/podcastUtils reads them, and the ones a client picks from them to download; then their enclosure, and
	// a stored episode's, which PodcastEpisode.toOldJSON writes in the same shape
	{"POST /api/podcasts/feed 200 .podcast.episodes[]", "RssPodcastEpisode"},
	{"GET /api/podcasts/{id}/checknew 200 .episodes[]", "RssPodcastEpisode"},
	{"GET /api/podcasts/{id}/search-episode 200 .episodes[].episode", "RssPodcastEpisode"},
	{"POST /api/podcasts/{id}/download-episodes body .[]", "RssPodcastEpisode"},
	{"RssPodcastEpisode .enclosure", "PodcastEpisodeEnclosure"},
	{"PodcastEpisode .enclosure", "PodcastEpisodeEnclosure"},
	// the server's backups, listed, and as they are once one is made, uploaded or deleted
	{"GET /api/backups 200 .backups[]", "Backup"},
	{"POST /api/backups 200 .backups[]", "Backup"},
	{"POST /api/backups/upload 200 .backups[]", "Backup"},
	{"DELETE /api/backups/{id} 200 .backups[]", "Backup"},
	// the metadata providers to match books, book covers and podcasts with
	{"GET /api/search/providers 200 .providers.books[]", "ProviderOption"},
	{"GET /api/search/providers 200 .providers.booksCovers[]", "ProviderOption"},
	{"GET /api/search/providers 200 .providers.podcasts[]", "ProviderOption"},
	// the entries a playlist is made, changed, added to and taken from with, and the one added on its own
	{"POST /api/playlists body .items[]", "PlaylistEntry"},
	{"PATCH /api/playlists/{id} body .items[]", "PlaylistEntry"},
	{"POST /api/playlists/{id}/batch/add body .items[]", "PlaylistEntry"},
	{"POST /api/playlists/{id}/batch/remove body .items[]", "PlaylistEntry"},
	{"POST /api/playlists/{id}/item body .", "PlaylistEntry"},
	// MeController.createUpdateMediaProgress spreads the body into one with the path's ids, and batchUpdateMediaProgress takes a list of them
	{"PATCH /api/me/progress/{libraryItemId} body .", "ProgressUpdatePayload"},
	{"PATCH /api/me/progress/{libraryItemId}/{episodeId} body .", "ProgressUpdatePayload"},
	{"PATCH /api/me/progress/batch/update body .[]", "ProgressUpdatePayload"},
	// a session's user as SessionController and UserController write it beside the session
	{"PlaybackSession .user", "PlaybackSessionUser"},
	{"PlaybackSessionForClient .user", "PlaybackSessionUser"},
	// a book's or a podcast's metadata as its oldMetadataToJSON writes it: in an item, and kept by a session from what it played, which
	// PlaybackSession.setData writes the same way
	{"LibraryItem .media.metadata", "MediaMetadata"},
	{"PlaybackSession .mediaMetadata", "MediaMetadata"},
	{"PlaybackSessionForClient .mediaMetadata", "MediaMetadata"},
	// ApiRouter.getUserListeningStatsHelpers: an account's listening stats, its own or another's, with each item's time beside what its
	// session played
	{"GET /api/me/listening-stats 200 .", "ListeningStats"},
	{"GET /api/users/{id}/listening-stats 200 .", "ListeningStats"},
	// LibraryItemController.updateMedia and batchUpdate hand an item's media what they are sent for it, which Book and Podcast
	// updateFromRequest read alike
	{"PATCH /api/items/{id}/media body .", "MediaUpdate"},
	{"POST /api/items/batch/update body .[].mediaPayload", "MediaUpdate"},
	// the filter data a library's lists are built from, beside the library or on its own
	{"GET /api/libraries/{id} 200 .filterdata", "LibraryFilterData"},
	{"GET /api/libraries/{id}/filterdata 200 .", "LibraryFilterData"},
	// LibraryController.search: a book or a podcast found, with what of it matched
	{"GET /api/libraries/{id}/search 200 .book[]", "LibrarySearchMatch"},
	{"GET /api/libraries/{id}/search 200 .podcast[]", "LibrarySearchMatch"},
	// the folders a new library is made over, as it keeps them
	{"POST /api/libraries body .folders[]", "LibraryFolder"},
	// PodcastEpisodeDownload.toJSONForClient: what PodcastManager is downloading and what waits, for a library or one podcast
	{"GET /api/libraries/{id}/episode-downloads 200 .currentDownload", "PodcastEpisodeDownload"},
	{"GET /api/libraries/{id}/episode-downloads 200 .queue[]", "PodcastEpisodeDownload"},
	{"GET /api/podcasts/{id}/downloads 200 .downloads[]", "PodcastEpisodeDownload"},
	{"ListeningStats .items{}", "ListeningStatsItem"},
	{"ListeningStatsItem .mediaMetadata", "MediaMetadata"},
	// what an account may do, as an account is answered, and as UserController.create and update take any key of it they are sent
	{"UserForBrowser .permissions", "UserPermissions"},
	{"POST /api/users body .permissions", "UserPermissions"},
	{"PATCH /api/users/{id} body .permissions", "UserPermissions"},
}

// storedRecords are the records the source keeps as JSON in a column and writes back as it was stored, which it describes in a typedef or a
// class but never writes through a method the scan can name, so the scan makes a copy at every place: each is made here from what the scan
// found at its places in recordsAt, so the answers fill one record rather than one copy a place.
var storedRecords = []string{
	// models/Book.js AudioFileObject, AudioTrack, ChapterObject and EBookFileObject, models/LibraryItem.js LibraryFileObject, and the
	// metadata and tags each file carries, written by objects/metadata FileMetadata and AudioMetaTags
	"AudioFile", "AudioTrack", "Chapter", "EBookFile", "LibraryFile", "FileMetadata", "AudioMetaTags",
	// a book's authors and series as Book.oldMetadataToJSON writes them, the shapes Author.toJSONMinimal and Series.toJSONMinimal write
	"AuthorMinimal", "SeriesMinimal",
	// MediaProgress.getOldMediaProgress, models/User.js AudioBookmarkObject, objects/settings/EmailSettings.js EreaderDeviceObject, and
	// utils/podcastUtils.js RssPodcastEpisode with its enclosure
	"MediaProgress", "AudioBookmark", "EreaderDevice", "RssPodcastEpisode", "PodcastEpisodeEnclosure",
	// objects/Backup.js, the backups BackupManager holds and answers as it holds them, and a metadata provider as SearchController.formatProvider
	// offers one to choose
	"Backup", "ProviderOption",
	// a book, or a podcast's episode, as a client names one for a playlist, which PlaylistController checks each entry it is sent against; and
	// models/User.js ProgressUpdatePayload, what a client sends to change its progress in one
	"PlaylistEntry", "ProgressUpdatePayload",
	// the user beside a session, the id and username User.getMinifiedUserObjects reads, and a book's or a podcast's metadata
	"PlaybackSessionUser", "MediaMetadata",
	// the listening stats one helper writes for two routes, and models/User.js getDefaultPermissionsForUserType, what an account may do
	"ListeningStats", "ListeningStatsItem", "UserPermissions",
	// objects/PodcastEpisodeDownload.js, held by PodcastManager where the scan cannot see it, a library's filter data, which a cache holds,
	// and a search's book or podcast found
	"PodcastEpisodeDownload", "LibraryFilterData", "LibrarySearchMatch",
	// what an item's media is changed with, one item or many
	"MediaUpdate",
}

// applyRecordsAt points each place at its record, making the place when the scan did not see the field, and returns the places it could not
// find, whose record, record or operation the document does not have, for the report. A record named in stored is made from what the scan
// found at its places. A place inside them that is not as stated is an error, so the statement is mended when the source changes.
func applyRecordsAt(doc *document, all []recordAt, stored []string) (missing []string, err error) {
	for _, r := range all {
		root, path, err := placeRoot(doc, r.at)
		if err != nil {
			return nil, err
		}
		made := slices.Contains(stored, r.record)
		if root == nil || doc.Components.Schemas[r.record] == nil && !made {
			missing = append(missing, r.at+" "+r.record)
			continue
		}

		old, err := setAt(root, path, &schema{Ref: "#/components/schemas/" + r.record})
		if errors.Is(err, errInNamedRecord) {
			missing = append(missing, r.at+" "+r.record)
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("record at %s: %w", r.at, err)
		}
		if made && old != nil && old.Ref != "" {
			return nil, fmt.Errorf("record at %s: the place is already %s", r.at, strings.TrimPrefix(old.Ref, "#/components/schemas/"))
		}
		if made {
			doc.Components.Schemas[r.record] = unite(doc.Components.Schemas[r.record], old)
		}
	}

	for _, n := range stored {
		widenWritten(doc.Components.Schemas[n])
	}

	return missing, nil
}

// widenWritten makes each whole number the source writes out in a stored record a number seen whole, as the scan does with a value it cannot
// tell: the record is one record at places the scan cannot all see, so the 0 written at one says nothing of the rest. The names of whole
// numbers settle it once the answers are in.
func widenWritten(s *schema) {
	if s == nil || s.Ref != "" {
		return
	}
	if s.Type == "integer" && s.written {
		s.Type, s.whole, s.written = "number", true, false
	}

	widenWritten(s.Items)
	widenWritten(s.Values)
	for _, p := range s.Properties {
		widenWritten(p)
	}
}

// placeRoot is the schema a place starts from, a record or an operation's JSON answer or body, and the path into it; nil for a record or an
// operation the document does not have.
func placeRoot(doc *document, at string) (**schema, string, error) {
	head, path, ok := strings.Cut(at, " .")
	if !ok {
		return nil, "", fmt.Errorf("record at %s: the place has no path", at)
	}
	path = strings.TrimPrefix(path, ".")

	fields := strings.Fields(head)
	switch len(fields) {
	case 1:
		s, ok := doc.Components.Schemas[fields[0]]
		if !ok {
			return nil, path, nil
		}
		return &s, path, nil
	case 3:
		op := doc.Paths[fields[1]][strings.ToLower(fields[0])]
		if op == nil {
			return nil, path, nil
		}
		if fields[2] == "body" {
			if op.RequestBody == nil || op.RequestBody.Content["application/json"].Schema == nil {
				return nil, "", fmt.Errorf("record at %s: the operation takes no JSON body", at)
			}
			m := op.RequestBody.Content["application/json"]
			return &m.Schema, path, nil
		}
		m, ok := op.Responses[fields[2]].Content["application/json"]
		if !ok || m.Schema == nil {
			return nil, "", fmt.Errorf("record at %s: the operation answers no JSON with %s", at, fields[2])
		}
		return &m.Schema, path, nil
	}

	return nil, "", fmt.Errorf("record at %s: a place is a record, or a method, path and status or body, before its path", at)
}

// setAt puts s at the path under root, and returns what was there: fields by name, a list's items by [], and the root itself for an empty
// path. A field the path ends in is made when its record lacks it, as is any field of a record the scan saw nothing of, and a step the scan
// left untyped is made what the path says it is; any other step the record lacks, or has as something else, is an error.
func setAt(root **schema, path string, s *schema) (*schema, error) {
	if path == "" {
		old := clone(*root)
		**root = *s
		return old, nil
	}

	at := *root
	steps := strings.Split(path, ".")
	for i, step := range steps {
		name, list := strings.CutSuffix(step, "[]")
		name, keyed := strings.CutSuffix(name, "{}")
		last := i == len(steps)-1

		if name != "" {
			if at.Ref != "" {
				return nil, errInNamedRecord
			}
			unseen := untyped(at) || at.unseen
			if unseen {
				at.Type, at.unseen = "object", true
			}
			if at.Type != "object" {
				return nil, fmt.Errorf("%s is not a record", name)
			}
			next, ok := at.Properties[name]
			switch {
			case !ok && (last || unseen):
				next = &schema{}
				if list {
					next = &schema{Type: "array", Items: &schema{}}
				}
				if at.Properties == nil {
					at.Properties = map[string]*schema{}
				}
				at.Properties[name] = next
			case !ok:
				return nil, fmt.Errorf("the record has no %s", name)
			}
			if last && !list && !keyed {
				old := clone(next)
				*next = *s
				return old, nil
			}
			at = next
		}

		if keyed {
			if at.Type != "object" || at.Values == nil {
				return nil, fmt.Errorf("%s is not a map", step)
			}
			if last {
				old := at.Values
				at.Values = s
				return old, nil
			}
			at = at.Values
		}

		if list {
			if untyped(at) {
				at.Type, at.Items = "array", &schema{}
			}
			if at.Type != "array" {
				return nil, fmt.Errorf("%s is not a list", step)
			}
			if last {
				old := at.Items
				at.Items = s
				return old, nil
			}
			at = at.Items
		}
	}

	return nil, nil
}
