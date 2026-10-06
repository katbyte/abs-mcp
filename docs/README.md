# docs

## API reference

Audiobookshelf's public API docs (<https://api.audiobookshelf.org>) state that they are out of date and no longer maintained. The reference for `sdk/abs` is the server source:

| What | Where in <https://github.com/advplyr/audiobookshelf> |
|---|---|
| Route list | `server/routers/ApiRouter.js` |
| Request handling and permissions | `server/controllers/*.js` |
| Response shapes ("old JSON") | `server/models/*.js` — `toOldJSON`, `toOldJSONMinified`, `toOldJSONExpanded` |
| List filters and sorts | `server/utils/queries/libraryItemsBookFilters.js`, `libraryItemsPodcastFilters.js` |
| Provider search results | `server/finders/*.js`, `server/providers/*.js` |

A quick way to list the routes for a checkout:

```bash
grep -oE "router\.(get|post|patch|delete)\('[^']+'" server/routers/ApiRouter.js | sort -u
```

## Conventions worth knowing

- Auth is `Authorization: Bearer <api key>`. A key acts as one user; admin-only routes return 403 for other accounts.
- Timestamps are epoch milliseconds; durations are seconds.
- List endpoints (`/api/libraries/:id/items`, `/authors`, `/series`) page with `limit` and `page` (0-based) and return `{results, total, ...}`. `minified=1` returns the compact item shape; item detail uses `expanded=1`.
- The `filter` parameter is `<group>.<base64(value)>` (`abs.EncodeFilter`). Groups the book filters accept: genres, tags, series, authors, narrators, languages, publishers, publishedDecades, progress, missing, ebooks, tracks, issues, abridged, explicit, feed-open, recent.
- Items filtered by series come back with `media.metadata.series` as a single object rather than an array; `abs.SeriesRefs` decodes both.
- Scans, match-all and metadata embeds return 200 immediately and run as tasks (`/api/tasks`).

## Server behaviour the tools work around

Found by the acceptance journeys against Audiobookshelf 2.36; each has a unit test in `tools/`, in the test file of the tool it belongs to.

- `POST /api/playlists/:id/batch/add` checks nothing: a book sent with an `episodeId`, or a podcast with an episode that is not its own, throws an unhandled rejection that exits the server; a podcast with no `episodeId` is stored as a broken book entry; an item from another library is accepted. `POST /api/playlists` does check. The tools check every entry first.
- Removing a playlist's last entry deletes the playlist, by either remove route.
- Batch add and remove on collections and playlists answer 200 to adding what is held and removing what is not, and change nothing. Collection batch add drops ids from another library, podcasts and unknown ids when any id in the batch is good, and adds in the database's order, not the request's.
- `POST /api/collections` answers 400 without at least one book.
- Libraries, collections and playlists can share a name; creating under a taken name makes a second.
- `POST /api/libraries` over a folder that does not exist creates the folder. `GET /api/filesystem?path=` answers 400 for a path that is not there.
- `/api/libraries/:id/filterdata` is cached per library for 30 minutes, not per user. Adds are patched in; renames and removals are not, so a renamed series keeps its old name. It, `/stats`, `/narrators` and the name groups of `/search` are built over the whole library, so they name books an account restricted by tag or explicit flag cannot open. The item, author and series routes are restricted properly.
- `/api/me/items-in-progress` carries no position or percent.
- Open sessions name their user by id only; `/api/users/:id/listening-sessions` rows do not name the user. `/api/sessions/open` answers a non-admin 404, not 403.
- `PATCH /api/authors/:id`, and the image and match routes, answer with the record and no `libraryItems`.
- `POST /api/podcasts` ignores `episodesToDownload`, and refuses only a folder that already holds a podcast, not a feed the library already subscribes to.
- `POST /api/podcasts/:id/download-episodes` downloads an episode the podcast already holds a second time, under a filename with a random suffix, as another episode; a request for one already downloading or queued is dropped without a word. A podcast's episodes come back in the order they were downloaded, not published.
- `POST /api/tools/item/:id/embed-metadata` tags the files and never probes them again: the item's `metaTags` stay as they were until the item is scanned. A task leaves `/api/tasks` the moment it ends, failed or finished, and a queued embed is only listed with `include=queue`.
- Bookmarks live on the user record. Deleting an item leaves them there, and `DELETE /api/me/item/:id/bookmark/:time` answers 404 once the item is gone, so nothing can remove them.
- A series whose last book is deleted is not always removed: it stays in `/api/libraries/:id/series` with no books, and `GET /api/series/:id` answers 404.
- Media updates replace the tag list, and diff the series list against the book as the request loaded it, so two edits of one book at once keep only one of them. `POST /api/me/item/:id/bookmark` saves the account's whole bookmark list.
- `PATCH /api/items/:id/tracks` rebuilds the book's audio from the files it is sent, so a file left out falls out of the book, and leaves the chapters where they were. The order holds through a scan until the audio files change on disk; the scan that finds that sorts them by track number again.
- `PATCH /api/items/:id/ebook/:fileid/status` reads no body: it flips the file between main and supplementary, so flipping the main ebook leaves the book with none.
- `DELETE /api/items/:id/file/:fileid` answers a plain `OK`, and takes the file off the book even when removing it from disk fails. Taking out an audio file leaves the book's duration as it was, and no scan corrects it: a scan rebuilds the audio only when the files on disk and on the record differ in number. Taking out the cover leaves the book pointing at it.
- `POST /api/filesystem/pathexists` answers `exists` for a path not on disk that sits one or two levels inside an item's folder, naming the item; only `exists` with no item named is a path really there.
- `POST /api/tools/item/:id/encode-m4b` always re-encodes, at 128k stereo AAC unless told otherwise; names the m4b after the book's folder, or for a book that is one file after that file; and moves the files merged into `metadata/cache/items/<id>`. The task leaves `/api/tasks` when it ends, failed or finished alike: only the book and the log say which.
- `GET /api/items/:id` honours `include` (`progress`, `rssfeed`, `share`, `downloads`) only with `expanded=1`.
- `GET /api/feeds` gives each feed's url inside `meta`, where opening a feed gives it at the top.
- `POST /api/users` makes an inactive account unless `isActive` is sent, and drops a permission it does not know, or one that is not true or false, with only a log line. `PATCH /api/users/:id` takes a library list only beside a `permissions` object, and leaves the root account's type as it was whatever is asked.
- A permission is granted by the permission alone and only while the account is active, not by the account's type: an admin without `delete` may not delete.
- `GET /api/stats/year/:year` (the whole server) answers a shape of its own, not `/api/me/stats/year/:year`'s, and refuses a year before 2000.
- `GET /api/logger-data` answers today's log, its last 5,000 lines, and an empty string rather than a list before the day's first line.
- `PATCH /api/items/:id/cover` takes, from 2.37.0, only the path of a file already on the item's record, and answers 500 `Invalid cover path` for an image put in the folder since the last scan; before, any image in the folder.
- `GET /public/share/:slug/download` answers 404 `Share session not set` to a visitor who has not opened `/public/share/:slug` first, which sets the cookie it knows them by; then 403 unless the link allows downloads.
- `POST /api/items/batch/update` answered 502 behind a reverse proxy for two podcasts of a real library on 2.36.0, where `PATCH /api/items/:id/media` took the same change. Neither 2.36.0 nor 2.37.1 in a container reproduces it, with a show laid out on disk or one subscribed from a feed.
- `GET /api/search/books` at a store with nothing for the title and author goes on to ask Audnexus (`/authors?name=`) whether the title, or the author, is an author's name, and searches again the other way round: a store that sells nothing costs three requests, not one.
- `DELETE /api/sessions/:id` needs the delete permission and deletes any account's session; `POST /api/sessions/batch/delete` needs an admin.

`ROADMAP.md` records the tool design rules and what is deliberately not wrapped.
