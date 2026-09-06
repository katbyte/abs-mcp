# docs

## API reference

Audiobookshelf's public API docs (<https://api.audiobookshelf.org>) state that they are out of
date and no longer maintained. The reference for `lib/abs` is the server source:

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

- Auth is `Authorization: Bearer <api key>`. A key acts as one user; admin-only routes return
  403 for other accounts.
- Timestamps are epoch milliseconds; durations are seconds.
- List endpoints (`/api/libraries/:id/items`, `/authors`, `/series`) page with `limit` and
  `page` (0-based) and return `{results, total, ...}`. `minified=1` returns the compact item
  shape; item detail uses `expanded=1`.
- The `filter` parameter is `<group>.<base64(value)>` (`abs.EncodeFilter`). Groups the book
  filters accept: genres, tags, series, authors, narrators, languages, publishers,
  publishedDecades, progress, missing, ebooks, tracks, issues, abridged, explicit,
  feed-open, recent.
- Items filtered by series come back with `media.metadata.series` as a single object rather
  than an array; `abs.SeriesRefs` decodes both.
- Scans, match-all and metadata embeds return 200 immediately and run as tasks
  (`/api/tasks`).

`ROADMAP.md` records the tool design rules and what is deliberately not wrapped.
