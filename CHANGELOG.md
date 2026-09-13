## 0.3.0 (2026-09-13)

### Breaking

- one rename tool. `metadata_rename field=tags|genres|narrators|authors|languages|publishers`
  replaces `server_tag_rename`, `narrator_edit` and `audit_terminology_rename`, and with
  `remove` drops a tag, genre, narrator, language or publisher everywhere. `audit_terminology`
  is now `audit_spelling`, and every group it reports is fixed by the one tool. 88 tools -> 86
- `audit_no_episodes` and `audit_stale_feed` are `audit_podcast_no_episodes` and
  `audit_podcast_stale_feed`, so the name says they only look at podcasts
- `item_cover_edit` no longer removes the cover when called with neither `url` nor `file`:
  pass `remove=true`. A call that forgot its url used to delete the cover
- the README token table is now what the model sees. Most clients send only the description
  and input schema to the model, not the output schema, which was 60% of the earlier figure

### Added

- `audit_unembedded`: books whose audio files carry no tags, or tags that disagree with the
  current title, author, narrator, series, genres, year or publisher - what
  `item_embed_metadata` is due for after a curation pass. It reads the expanded items in
  batches of 50, so it runs apart from `audit_all`

### Fixed

- `item_edit clear` for `narrators`, `series`, `genres` and `tags` was a silent no-op: the
  empty list was dropped from the request (`omitempty`), so the server saw nothing to change.
  `lib/abs` list fields are now `omitzero`: nil leaves a field alone, an empty slice clears it
- `audit_cover_ratio` measured the server's 400-pixel-wide cache copy rather than the cover
  file, so every cover was "400 wide" and the too-small check could never fire. It now asks for
  the raw file, reads only the image header instead of buffering the whole image, and skips
  the request for items the listing already says have no cover
- `audit_spelling` (then `audit_terminology`) never found narrator spellings: the sweep sees the minified item shape,
  which carries narrators only as one joined `narratorName`, and only the expanded
  `narrators` list was read
- an audit with a server-side filter (`audit_missing`, `audit_issues`, `audit_no_audio`)
  overran `limit` once an earlier library had filled it: the request for the next library was
  sent with no limit, which the server reads as everything
- `lib/abs` `DeleteAuthorImage` returned an empty record; the server answers with the author
  under an `author` key, like the image and match routes

### Changed

- docker-free tests for the tools: an in-memory MCP session against a canned Audiobookshelf
  (`tools/handlers_test.go`), covering what the live fixtures cannot reach - a library with
  covers, inconsistent narrator spellings, more findings than the limit
- `lib/abs` request and response-shape tests without a server: paging, streaming, multipart
  upload, the vocabulary path encoding, the chapter and feed-episode unwrapping
- `intParam`/`intQuery` and `narratorID`/`vocabularyID` were the same function twice

## 0.2.0 (2026-09-12)

### Breaking

- default is now the `core` toolset: 5 read-only tools, ~4,400 tokens instead of ~33,000.
  `ABS_TOOLSETS=curation` for the audits and their fixers, `ABS_TOOLSETS=all` for everything
- `item_chapters` and `item_files` are now `chapters` and `files` flags on `item_get`, off by
  default (a 300-chapter book is 19x the rest of the answer)
- `library_stats` folded into `library_get`, `server_stats` into `server_info`,
  `podcast_recent` into `podcast_episodes` (omit `item` for the whole library)
- removed `library_match_all`: no candidates to review, no undo. Use `item_match` then
  `item_match_apply`. `lib/abs` keeps `MatchAll`
- 94 tools -> 88

### Added

- `--toolsets` / `ABS_TOOLSETS`: `core`, `curation`, `listening`, `podcasts`, `organise`,
  `admin`, `all`, or a resource family like `item`. `core` is always included
- `abs-mcp tools` lists what the current flags would register, grouped by toolset. No server
  needed; `-q` for names only
- coverage workflow and badge; `make cover` merges all three suites

### Fixed

- `server_info` says what an admin-only key could not see instead of dropping the totals
  silently, and no longer hides zero podcasts or zero open sessions
- `item_get chapters=true` on a podcast says chapters belong to the episodes again
- `abs-mcp tools -q > file` wrote to stdout past cobra's buffer
- the Homebrew formula never published for v0.1.0: the script was committed non-executable.
  The release workflow can now republish it for any tag

### Changed

- `library_items` and `library_search` say what each is for and name the other;
  `library_items` no longer advertises `missing:` and `issues`, which are `audit_missing` and
  `audit_issues`
- coverage 84.8% (`lib/abs` 87.1%, `tools` 86.1%, `cli` 58.8%); `requireBearer` 0% -> 100%
- ids are tested across every resolver, not just titles
- lowercase workflow names

## 0.1.0 (2026-09-12)

- MCP server over stdio or HTTP (`--listen`), and a CLI (`serve`, `info`, `version`)
- 94 tools: server, libraries, items, authors, series, narrators, collections, playlists,
  podcasts, users (progress, bookmarks, history and stats, for the API key's own account or
  any other), and 16 audits
- `--read-only`, `--enable-delete`, `--allow-tools` / `--deny-tools` (names, globs, or the
  `essential` preset); every tool carries MCP read-only/destructive annotations
- tools take names as well as ids; every response is a trimmed projection rather than the
  raw API payload
- `lib/abs`: a standalone Audiobookshelf client for Go, stdlib only and with no knowledge of
  MCP. 204 methods covering all 202 API routes (`make apicheck`); downloads stream rather
  than buffer
- tested against a real Audiobookshelf in Docker: `integration/` covers the client,
  `acceptance/` the tools, with provider calls replayed from cassettes. Every tool and every
  client method is exercised, and the suite fails if a registered tool has no test
- binaries for linux, darwin, windows, freebsd, openbsd and solaris, a Homebrew tap, and a
  linux/amd64 + linux/arm64 image on `ghcr.io/katbyte/abs-mcp` with `docker-compose.yml`
