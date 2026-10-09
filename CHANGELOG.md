## Unreleased

### Breaking

- the Go client moves from `lib/abs` to `sdk/abs`, as in embyfin-mcp: change the import path; `lib` keeps the helpers (audioprobe, audiosample, providerproxy)

### Added

- `audit_path` reports a book the record places in a series when its folder does not say so
- `--audit-skip` / `ABS_AUDIT_SKIP` leaves out an audit rule that is a way of filing rather than a mistake: `path-series`
- on macOS, a connection the system refused with "no route to host" says that Local Network privacy may be blocking the process, and that a terminal app updated while running needs a restart

## 0.6.0 (2026-10-03)

### Breaking

- `item_batch_edit` is gone: `item_edit` takes `items` to make the same change on many
- `collection_books_edit` is gone: `collection_edit` takes `add_items` and `remove_items`
- `playlist_entries_edit` is gone: `playlist_edit` takes `add_entries` and `remove_entries`
- `author_image_set` is gone: `author_edit` takes `image_url`
- `user_progress_remove` is gone: `user_progress_set` takes `remove`
- `audit_podcast_no_episodes` and `audit_podcast_stale_feed` are one audit, `audit_podcasts`, each finding naming its `problem`
- `podcast_settings` is renamed `podcast_edit`
- `user_bookmark_edit` takes `add_bookmarks` and `remove_bookmarks` in place of `action`
- `item_match`, `item_match_apply`, `item_match_apply_batch` and `item_cover_search` take `providers`, a list tried in order, in place of `provider`
- `item_match_apply`, `item_match_apply_batch` and `item_cover_upgrade` change nothing without `confirm`; `preview` is gone
- `metadata_rename` answers `would_remove` in place of `preview`
- `library_filters` leaves out a list that is empty or not asked for; `totals` gives each list's length
- `lib/abs`: `LoggerData` returns log lines, `ServerYearStats` returns `AdminYearStats`, and `DeleteItemFile` returns only an error

### Added

- `audit_unplayable`: books whose audio is locked to a store, cut short, unreadable or misnamed; `decode` also finds damaged files
- `audit_path` finds books dated earlier than the year their folder carries
- `feed_list` and `feed_edit`: RSS feeds of a book, series or collection, and public links to a book
- `user_create` and `user_edit`
- `user_history_remove`: take listening sessions out of an account's history (needs `--enable-delete`)
- `item_send_ebook`: email a book's ebook to an e-reader
- `item_edit` sets a book's track order, moving its chapters with the files, and its main ebook
- `item_delete` `file` deletes one file of a book and keeps the book
- `item_embed_metadata` `m4b` merges a book into one m4b
- `server_tasks` `log`: today's server log
- `user_stats` `server`: the whole server's year in review
- `user_progress_set` `series` hides a series from Continue Series
- `item_get` shows which ebook is the main one, and where a file sits in a disc folder
- `item_edit` with `items` changes podcasts as well as books
- `collection_edit` and `playlist_edit` rename, describe, add and remove in one call
- `collection_list`, `playlist_list`, `user_list`, `feed_list`, `narrator_list` and `library_filters` take `limit` and `offset`
- `library_filters` `fields` asks for only some of its lists
- a match with no provider named asks each of `--providers` in turn, the book's recorded store first
- `item_cover_edit` `file` takes a filename, and finds an image added since the last scan
- `item_compare_audio` checks the voice as well as the rhythm where two copies line up, so the same words in another voice are `different` however alike the pacing; each point reports `spectral`, and the answer says how many were `found` and how fast the other copy plays
- `item_compare_audio` takes `points`, `stretch_s`, `reach_s` and `speed_pct` to listen harder, and steps past a stretch that is mostly silence
- `scripts/calibration-fetch.py` and a `calibration` test: the by-ear check measured against LibriVox readings of one text by different readers, and copies of one reading re-encoded, resampled, cut and split

### Fixed

- matching no longer calls a book exact when its folder, a "Read by" note or a full-cast label names a different reading
- `user_get` and `server_info` no longer say an account can delete, update, download or upload without that permission
- `item_delete` no longer removes an admin's bookmarks before the server refuses the delete
- `server_tasks` no longer promises recently finished tasks, which the server never lists
- `item_cover_edit` `file` works on Audiobookshelf 2.37, which refuses a file not yet on the book
- `metadata_rename` changes podcasts one at a time, after the server failed on them in a batch
- `lib/abs`: deleting one file of a book no longer reports a failure when it worked; listed feeds have their urls; the whole server's year keeps its fields

## 0.5.0 (2026-09-27)

### Breaking

- times are seconds and sizes bytes everywhere: `duration` is `duration_s`, `size_mb` is `size`, and so on
- deleting (items, episodes, issues, `metadata_rename remove`) needs `confirm`; without it you get a preview
- `collection_delete` and `playlist_delete` need `--enable-delete`
- changing a book needs its full title or id: a partial title could hit the wrong book
- unknown filters, sorts, providers and similar values are refused instead of ignored
- every list pages by `offset` and answers `next_offset`; `page` is refused; `limit` is at most 1,000
- a name shared by two libraries, collections or playlists is refused, listing their ids
- `audit_single_chapter` is `audit_chapters`, covering every chapter problem
- tools that answered `done` now say what changed
- empty lists are `[]`, never `null`
- `.abs-mcp` uses the environment variable names, and a project file adds to `~/.abs-mcp` instead of replacing it
- `lib/abs`: `NewPodcast.EpisodesToDownload` and `FlexString.Int` are gone

### Added

- `audit_whitespace`: double spaces and stray spaces in names, folders and files
- `audit_abridged`: books that are probably abridged but not marked so
- `audit_duplicates` finds the same recording under another title or author, lists copies with missing files as `incomplete`, and shows what it kept apart and why under `split`
- `audit_series` `folder_style`: one series written two ways on disk
- `item_compare_audio`: checks by ear whether two items are the same recording (needs ffmpeg, now in the Docker image)
- `collection_books_edit` and `playlist_entries_edit`
- `audit_all` says which audits don't apply, which weren't run and why, and which ran only in part
- more detail from `user_get`, `item_get` (podcast download settings) and `server_info` (the abs-mcp version)
- many more live tests, including a check of the built binary

### Fixed

- a failed server request no longer looks like an empty answer: the tool says what failed, and after a change, that the change was made
- `audit_duplicates` no longer groups different books or different readings, and finds a matched and an unmatched copy of one book together
- names with a stray space or odd case find the right record, and never its look-alike
- calls made at the same time no longer undo each other's edits
- `audit_abridged` no longer calls full-cast productions or short stories abridged
- a covers audit during an outage no longer reports clean covers, and a cover is not replaced when its size can't be read
- `item_compare_audio` no longer scores a file that broke off or ended early
- restricted accounts no longer see hidden books through filters and search
- a renamed series is found by its new name right away
- podcast fixes: new episodes queue, feeds aren't added twice, downloads aren't duplicated, newest episodes list first
- chapter fixes: nothing past the end of the audio, and the right store's chapters
- the server no longer crashes on a bad playlist entry or a negative offset
- a crashing tool no longer ends the session
- downloads and uploads are no longer cut off at two minutes
- a redirect no longer makes a failed change look successful
- names with a comma ("Jane Doe, Ph.D.") are no longer split in two
- the Docker image reports its real version
- many smaller fixes to paging, sorting and counts across the audits

### Changed

- CI runs the unit tests with the race detector and watches the Docker base images
- the test recordings no longer keep network details, and the live suite can run twice against one server

## 0.4.0 (2026-09-13)

### Breaking

- `audit_series_gaps` is `audit_series`: gaps, names (one series spelled two ways, with authors), numbering (folder vs series number, unlinked, duplicates, zero padding), odd names, and titles that are really the series name. Gap rows carry `unlinked` (the book is on the shelf) and `merged` (what is left once spellings are one series); `articles=true` lists names opening with The, A or An
- `audit_author_as_title` and `audit_author_missing_image` are folded into `audit_authors`, which also finds records with no asin, no photo, no books, a stranger's biography, or a name spelled two ways
- `audit_cover_ratio` is `audit_covers`: missing, not square, too small. `store=true` compares each matched book's cover with its store's by perceptual hash: `upgrade` (same picture, bigger), `differs` (another picture), a jacket scan as `ratio` with the store's square art attached. `banner=true` finds the "Only from Audible" ribbon
- `item_match_apply` requires `candidate`, `asin` or `isbn`
- `serve --listen` refuses to start without `ABS_AUTH_TOKEN`; `--allow-no-auth` opts out
- `audit_all` runs every audit; `deep=true` adds the three that fetch per item, otherwise `skipped`
- `author_match` looks and `author_match_apply` applies; `lib/abs` `SearchAuthors` is `SearchAuthor`
- the gaps audit and `audit_duplicates` report `total_findings` like the rest
- 87 tools -> 91

### Added

- `item_match_batch` scores a page of books against a provider (`exact`, `likely`, `edition`, `unsure`, `none`); `item_match_apply_batch` applies an explicit list of item and asin pairs
- `item_match_apply` `smart`: fill the empty fields, then decide each difference by rule, with `preview`; `keep` restores named fields after `override_details`; results report `applied` and warn when the item's asin is not the one applied
- `zz-provider:` tag records the store a match came from, `zz-provider:none` marks a book checked and unmatchable, `item_match_tag` backfills; `--provider-tag` / `ABS_PROVIDER_TAG`
- `--providers` / `ABS_PROVIDERS`: the store order every match and audit asks
- `audit_matched`: is each asin the recording on disk (title, duration, narrator); `fields=true` lists every field that differs, with both values
- `audit_narrators`: names that both wrote and read, and the spelling checks on narrators
- `audit_genres`: placeholders, compound values, narrow genres, tags repeating genres, books with no genre; every finding carries its `metadata_rename`
- `audit_spelling` finds importer wrappers ("Read by"), truncations, typos, two names in one value, and leftovers such as "Ph.D."
- `audit_missing description` also reports stubs under 120 characters, credit lines and bare urls
- `audit_path files=true` compares audio filenames as well
- `series_merge` moves one series into another, keeping numbers and other series; `series_edit` refuses a rename onto an existing name
- `item_edit` and `item_batch_edit`: `add_series`, `remove_series`, `add_tags`, `remove_tags`
- `item_cover_upgrade`: the store's full-size cover when bigger and the same picture; `square` for jacket scans, `any_picture`, `preview`; a ribboned store copy never replaces a clean cover
- `metadata_rename` `into` splits a value; `to_field` moves it between genres and tags
- `author_edit clear=[description, asin, image]`
- `series_list` lists every book library when none is named

### Fixed

- `audit_authors` and `audit_narrators` read "Sanderson, Brandon" as Brandon Sanderson, so the two spellings are one group
- `series_get`, and `library_items` with a series filter, show every series a book is in, not only the one asked for; an edit built from the old output dropped links
- `audit_series` reads a "The X Series" folder as series X
- the `smart` match no longer writes bare co-authors (illustrators, translators, pen names)
- `audit_path` reported writing style as a mismatch: 393 findings, a dozen real, now 16
- a title lookup took a search hit whose title did not contain the query
- an author past the first 500 in a library could not be found by name
- a negative `offset` to `podcast_episodes` took the server down
- server-filtered audits reported `items_scanned` wrong and sent book filters to podcast libraries
- `item_match` candidates lost their series name
- the name normalizer dropped accented letters
- `metadata_rename` on languages and publishers matched case-insensitively and sent hundreds of items in one request
- `author_edit clear=[image]` failed on an author with no photo

### Changed

- `audit_authors` says an asin without a photo means Audible has none
- the live fixtures carry covers: non-fiction has a square one, a jacket scan and one too small, so `audit_covers` measures real files there; fiction stays bare
- a fourth live fixture, `Messy`: 31 books seeded with the defects the curation audits are for (a gap with its book on the shelf unlinked, a series and a narrator spelled two ways, unpadded numbers, titles that are the series name, stub descriptions, genre placeholders, a folder naming another book, a duplicate, a single-file m4b, a ribboned cover, one matched book), and a test per audit against it
- `docker-compose.yml` no longer pins `dns: 1.1.1.1`

## 0.3.0 (2026-09-13)

### Breaking

- one rename tool, `metadata_rename`, replaces three; `audit_terminology` is `audit_spelling`
- the podcast audits are named `audit_podcast_no_episodes` and `audit_podcast_stale_feed`
- `item_cover_edit` removes a cover only with `remove=true`

### Added

- `audit_unembedded`: books whose audio files don't carry their current details

### Fixed

- clearing narrators, series, genres or tags did nothing
- the cover audit measured a thumbnail instead of the real file
- narrator spellings were never checked
- some audits returned more than `limit`

### Changed

- tests that run without Docker, for the tools and the client

## 0.2.0 (2026-09-12)

### Breaking

- the default is a small `core` set of tools; `ABS_TOOLSETS` picks more
- chapters and files are options on `item_get`; a few tools were folded into others
- `library_match_all` is gone: match one book at a time

### Added

- `--toolsets`, and `abs-mcp tools` to list what they register
- a coverage report

### Fixed

- `server_info` says what a non-admin key can't see
- the Homebrew formula now publishes

## 0.1.0 (2026-09-12)

- MCP server over stdio or HTTP, and a CLI
- 94 tools covering libraries, items, authors, series, podcasts, users and audits
- read-only mode, delete gated off by default, and tool allow and deny lists
- `lib/abs`: a standalone Go client for the whole Audiobookshelf API
- tested against a real Audiobookshelf in Docker
- binaries for most platforms, a Homebrew tap and a Docker image
