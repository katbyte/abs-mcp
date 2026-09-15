# docs

## API reference

Audiobookshelf's public API docs (<https://api.audiobookshelf.org>) state that they are out of date and no longer maintained. The reference for `lib/abs` is the server source:

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

Found by the acceptance journeys against Audiobookshelf 2.36; each has a unit test in `tools/journey_fixes_test.go`.

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

`ROADMAP.md` records the tool design rules and what is deliberately not wrapped.
