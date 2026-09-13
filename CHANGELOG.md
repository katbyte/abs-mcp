## Unreleased

### Breaking

- `audit_author_as_title` and `audit_author_missing_image` are folded into `audit_authors`,
  which is every author check in one place: the item whose author field holds its title;
  records never matched (no asin), with no photo, with no books, and whose biography reads as
  someone else's (it opens with a name and never mentions the author's surname: Sarah Diemer's
  record said "Sarah Miller began writing", Reba Buhr's "Reba Bale writes", and no audit
  noticed); and author records that are one name spelled two ways, which `audit_spelling` no
  longer reports. `counts` says how many of each. 89 tools -> 88

- `item_match_apply` requires `candidate`, `asin` or `isbn`. With none of them it applied the
  provider's first hit unseen, and with `override_details` replaced the metadata with it
- `serve --listen` refuses to start without `ABS_AUTH_TOKEN`. A blank token in a copied
  `.env` used to come up serving every tool to the whole network behind one warning line;
  `--allow-no-auth` (`ABS_ALLOW_NO_AUTH=true`) is how to say that is wanted
- `audit_all` runs every audit: `audit_duplicates`, `audit_spelling`, `audit_series_gaps`,
  `audit_authors` and `audit_narrators` now count alongside the per-item checks, and `deep=true` adds
  `audit_cover_ratio` and `audit_unembedded`, the two that fetch something for every item;
  without it they are listed under `skipped` rather than silently left out
- `audit_series_gaps` and `audit_duplicates` report `total_findings` like every other audit
  (`audit_series_gaps` called it `found`; `audit_duplicates` had no count before the limit)

- `author_match` looks and `author_match_apply` applies, the way `item_match` and
  `item_match_apply` work. The provider's name lookup tolerates a few letters of difference,
  so "Emily Andras" came back as Emily Adrian and "Sarah Diemer" as Sarah Miller, and the
  one-step tool wrote a stranger's photo and biography onto the record with no way to say
  no. Now `author_match` returns the candidate with `name_matches`, a description and
  whether there is a photo, and nothing changes until `author_match_apply` is given that
  asin. `lib/abs` `SearchAuthors` is `SearchAuthor`: the server answers one record or null,
  and the old list decode always came back empty. 87 tools -> 88
- `item_match_apply` reports `applied`, the candidate it sent, and warns when the item's
  asin or isbn is not that one afterwards (a field already set is kept unless
  `override_details`), so what was applied can be checked against the item rather than
  assumed

### Added

- `audit_narrators`: everything wrong with the narrator field, in two parts. Roles: a name that
  wrote some books and read others in the same library, an author reading their own book not
  counted. The two shapes it catches were found by hand first: four light novels imported with
  author and narrator swapped (the author of volume 2 was the narrator of 1, 3 and 4), and a
  narrator credited as co-author on one book. Names: the spelling checks below, run on
  narrators, which `audit_spelling` no longer covers. Runs in `audit_all`. 88 tools -> 89
  (then 88 again with the author audits folded, above)
- `audit_spelling` sees more than case and punctuation. For names it also reports an
  importer's wrapper left on (`introduction by Jane Doe`, and for narrators
  `Narrator..........Sean Barrett`, `Read by Jim Dale`), one name that is another cut short or
  without its initials (`Harper Audio` and `HarperAudio`, `Fajer Al` and `Fajer Al-Kaisi`, `Jack
  Evans` and `Jack R. R. Evans`), two names a typo apart (`Peter Whickam` and `Peter Wickham`),
  two names in one value (`A. Reader/B. Reader`, with the parts listed) and leftovers that are
  not a name (`Ph.D.`, `Dc`). Every group carries `kind`, and `keep` is the clean spelling even
  when the wrapped one is on more books. Spellings a typo apart from one another come back as
  one cluster (`Audiobook`, `Audio Book`, `Audiobooks`), not a pair per edge. A narrator list
  with all of these in it came back with zero findings before

- `author_edit clear=[description, asin, image]` blanks those fields and removes the photo,
  which is how an `author_match` that found the wrong person is undone. Before, an empty
  value was "nothing to change" and nothing could remove a photo at all

### Fixed

- a title lookup took a lone search hit as the item even when the title did not contain the
  words asked for. The server's search also answers on subtitle, asin and isbn (and older
  servers on authors and narrators) without saying which field matched, so `item_delete
  item=B0DUNE` deleted whichever book carried that asin. A hit whose title does not contain
  the query is now refused with the id on offer. This covers every tool that takes an item by
  title
- an author past the first 500 in a library could not be found by name: `author_edit`,
  `author_delete`, `author_get`, `metadata_rename field=authors` and the author photo audit
  read one page and stopped
- a negative `offset` to `podcast_episodes` indexed past the end of the list and, with no
  recover in the MCP transport, took the server down
- audits the server filters for (`audit_missing` most fields, `audit_issues`, `audit_no_audio`)
  reported `items_scanned` equal to the findings, and asked podcast libraries with book
  filters their podcast filters do not know. `items_scanned` is now the library's size, and
  checks that never fire for a podcast skip podcast libraries
- the name normalizer behind every audit dropped accented letters, so `Jo Nesbø` was `jo nesb`
  and did not match `Jo Nesbo`, and `Étienne` was `tienne`. Accented Latin letters now fold to
  their plain spelling
- `metadata_rename` of a language or publisher matched case-insensitively, so renaming
  `english` to `English` sent every book that already said `English` back to the server in one
  request of several hundred items, which the reverse proxy answered with a 502, and reported
  50 titles for one change. It now touches only the spelling asked for and sends the updates
  in pages of 100
- `author_edit clear=[image]` failed with the server's 400 when the author had no photo, after
  the asin and description had already been blanked. An author with no photo is now left as
  they are rather than asked to lose one

### Changed

- `audit_authors` says what an asin without a photo means: Audible holds no photo for that
  author, so the fix is a photo from elsewhere through `author_image_set`

- `docker-compose.yml` no longer pins `dns: 1.1.1.1`. On a user-defined network that sends
  every lookup to the public resolver, so a LAN name like `nas` in `ABS_SERVER` could not
  resolve; the container now uses the host's resolver like any other

## 0.3.0 (2026-09-13)

### Breaking

- one rename tool. `metadata_rename field=tags|genres|narrators|authors|languages|publishers`
  replaces `server_tag_rename`, `narrator_edit` and `audit_terminology_rename`, and with
  `remove` drops a tag, genre, narrator, language or publisher everywhere. `audit_terminology`
  is now `audit_spelling`, and every group it reports is fixed by the one tool. 88 tools -> 87
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
