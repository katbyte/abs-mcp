## 0.5.0 (unreleased)

### Breaking

- a name two libraries, collections or playlists share is refused with their ids instead of taking the first; `library_create`, `collection_create`, `playlist_create` and the renames refuse a name already taken
- `collection_create` requires `items`: the server never made an empty one
- `library_create` refuses a folder that is not there (the server made it, empty)
- `user_get` `libraries` is only set with `all_libraries` false, and empty then means none
- `item_embed_metadata` waits for the embed (up to two minutes), rescans the item and checks its tags: `embedded`, `rescan`, `differs`, or `running` when it stopped waiting, instead of `started`
- `podcast_episode_download` `queued` lists only what was sent: `already_held` and `already_queued` list the rest
- `lib/abs`: `NewPodcast.EpisodesToDownload` is gone, as the server ignores it
- `item_delete`, `podcast_episode_delete`, `library_issues_remove` and `metadata_rename remove` change nothing without `confirm`: they answer what they would remove (the record, the files, the bookmarks, the titles and paths)
- `collection_delete` and `playlist_delete` are delete tools, registered only with `--enable-delete`; removing a playlist's last entry is refused without it, as the server deletes an empty playlist
- a tool that changes an item needs its whole title or its id: a title that was only part of one book's changed that book, so `item_delete item=Foundation` could delete Foundation and Empire
- a filter group or value, a sort, a provider, a `server_tags` kind, a podcast schedule or episode type the server does not know is refused by name, where it used to be sent and ignored: an unknown filter answered the whole library
- every list pages by offset, as a caller fixing things between calls cannot then skip rows: `library_items`, `author_list` and `series_list` take an offset that is not a whole page and answer `offset` and `next_offset`; `item_match_batch`, `item_match_tag`, `audit_matched` and `audit_covers store` take `offset` for `page` and answer `next_offset` for `next_page` (an old `page` is refused by name), counting matched books in the order they were added, so each call checks `limit` of them; `podcast_episodes` answers `next_offset`. `limit` is capped at 1,000 everywhere
- `audit_matched`, `audit_covers store`, `item_match_tag` and `item_cover_upgrade` refuse a library on a provider that cannot look an asin up (the server's default, google) unless providers are named, where every book came back not_found; `audit_all deep` skips `audit_matched` there and says why
- `item_match_apply_batch` counts `applied` only when the book changed, beside `unchanged`, `previewed` and `failed`
- `audit_podcast_stale_feed` reads the newest episode's publish date, not when the server last checked the feed
- `library_issues_remove` `items` are `{id, title, path}`: two books can carry one title
- no bare `done`: `podcast_episode_edit` answers `changed` and `unchanged`, `podcast_episode_delete` the episode and its file, `user_bookmark_edit` `result` and `bookmark`, `collection_delete` and `playlist_delete` `deleted` and `id`, `item_cover_edit` the `cover` it now has, `user_progress_remove` `removed`, and `podcast_settings` the settings as saved; each read back from the server
- times are seconds and sizes bytes everywhere, as in embyfin-mcp: `duration` is `duration_s`, `size_mb` is `size`, and likewise `total_duration_s`, `total_size`, `books_size`, `podcasts_size`, `item_duration_s`, `listened_s`, `position_s`, `current_time_s` (one field, where there were `current_time` and `current_seconds`), chapter `start_s` and `end_s`, bookmark `time_s`, and `user_stats` `total_listened_s`, `today_s`, `last_7_days_s`, `last_30_days_s` and each top row's `time_s`. Readable durations stay only in sentences. Inputs follow: `user_progress_set position_s`, `user_bookmark_edit time_s`
- `audit_matched` findings carry the book's own `duration_s` beside the store's
- write tools are annotated destructive unless they only ever add (`library_create`, `collection_create`, `playlist_create`, `podcast_add`, `podcast_episode_download`): MCP reads destructive false as only additive
- a list with nothing in it answers `[]`, never `null`
- `podcast_check_new` rows carry no `index`: they are queued already, and their place in that list is not their place in the feed
- `.abs-mcp` keys are the environment variable names (`READ_ONLY`, with or without `ABS_`), and `./.abs-mcp` is read over `~/.abs-mcp` rather than instead of it
- `make record` re-records every cassette (`ABS_TEST_RECORD=all`); `ABS_TEST_RECORD=1` on a `testacc` target records only what no cassette holds
- `audit_single_chapter` is `audit_chapters`, everything wrong with a book's chapters by problem: `past_end` (a file taken out of a book leaves its chapters behind, starting after the audio ends), `out_of_order`, `short` (the chapters end well before the audio does, as when a track is added) and `single` (one chapter over a long book). It reads every chaptered book whole, fifty to a request, as the listing carries only their count, so `audit_all` without `deep` counts `single` alone and says so under `partial`
- `item_chapters_set` chapters take `start_s`

### Added

- `collection_books_edit`, `playlist_entries_edit`: `added`, `already_held`, `removed`, `not_held`, and `deleted` when a playlist's last entry goes (the server deletes it)
- `user_get`: `all_libraries`, `all_tags`, `tags`, `denied_tags`, `explicit`
- `metadata_rename` `split`: a compound genre's `suggest` as-is, only on the books carrying it
- `item_edit` `add_tags`, `remove_tags`, which 0.4.0 said it had
- `item_get` `downloads` for a podcast: `auto_download`, `schedule`, `keep_episodes`, `new_per_check`, `last_check`, the settings `podcast_settings` changes
- `podcast_add` `queued` and `warning`; `item_delete` `bookmarks_removed`; `user_bookmarks` `item_deleted`
- `lib/abs` `EmbedPending`; `providerproxy` `Serve`, for a host a test answers itself
- acceptance journeys: tools chained against a changing server, across users and repeated; a podcast's whole life over a feed the suite serves, an embed read back from the file alone, a deleted book, calls made at once, and an account without rights calling every write tool
- a smoke test of the built binary (`acceptance/binary_test.go`): stdio carrying nothing but the protocol, the flags, environment and config file reaching the running server, the HTTP routes and bearer check, shutdown on SIGTERM, and a bad start failing with a reason
- `audit_all` `not_applicable` (book audits over podcasts and the reverse, no longer counted as clean) and `not_run`, a reason for every audit it did not run
- `items_scanned` on `audit_duplicates`, `audit_series` and `audit_covers`, so every audit answers it and `total_findings`
- `item_match_batch` `paging`: after applying rows from a window under a `missing:` filter, ask for the same offset again, as the applied books leave the list
- `item_chapters_set` `region`, from the book's provider tag; `series_merge` `from_removed`; `audit_series` gap `outliers`, the numbers set aside as too far from the rest
- `item_cover_upgrade` returns the rows it finished and `not_tried` when a book fails part way
- `item_chapters_set fit`: keep the book's own chapters, drop those past the end of the audio and end the last at its end, the fix `audit_chapters` gives for `past_end` and `short`; `dropped` and `end_s` say what it did
- `audit_all` `partial`: audits run in part, with what was left out
- `audit_abridged`: books probably abridged but not marked so, against the store's editions (the length of an abridged one, or under 60% of every unabridged one) and, for two readings of one book in the library, chapter lengths cut unevenly (a spread over 1.35 where two unabridged readings stay under 1.2; two readings within 10% of each other's length, or whose chapters do not line up, are not compared). One store search per book: it pages, 50 at most, and `audit_all` runs it only with `deep`
- `item_compare_audio`: are two items the same recording? Five points of one book are found in the other by their loudness, allowing for a copy up to 3% faster or slower; same when four of the five match. It reads through the API, so about half of the second book crosses the network: a lead to confirm, not a sweep. Needs ffmpeg, which the Docker image now carries
- `audit_duplicates` `candidates` and `total_candidates`: pairs no key joins that are probably one recording - an edition label, the same title under another author, one copy's album tag naming the other, split files beside a single file - each to confirm with `item_compare_audio`; they are leads, not findings, and `audit_all` does not count them
- `lib/audiosample`: decodes any stretch of a book's audio through the API, across its files, for the audio check now and transcripts later; the API key stays in the process, behind a local proxy ffmpeg reads from
- `audit_series` `folder_style`: the folders of one series written two ways on disk - a bare title beside "Series - NN - Title" folders, another spelling of the series, a double space round a dash, a number padded unlike the rest - each with the folder name in the style the library's series mostly use. A series written one way throughout is left alone
- `server_backup_create` `created`, the backup it made, with `replaced` when it took the place of one made in the same minute (the server names backups by the minute) and `pruned`, the old ones the server deleted to stay within its number
- `providerproxy` `Rerecord` mode, and replay, tunnel and handshake lines in its log
- `server_info` `abs_mcp_version`, the build answering, beside the server's own
- twenty-three more acceptance journeys: a match decided field by field and a match that found nothing; one asin on two books put right; copies joined and editions kept apart; every page of every list read exactly once; a book imported into its series; a book deleted with its files, folder and single-file alike; a long book chaptered; a folder renamed under its book; edits through a forced scan and a rescan; removing issues one library at a time; an ebook-only folder; folders naming their books; an explicit book kept from an account; a tag-limited listener's queue; lists deleted and read back; a listener's history and progress read back; a backup read back; podcast audits given something to find; an episode held twice; chapters left past the end of the audio and fitted back; a narrator with a comma in the name
- `library_issues_remove` items carry `full_path` beside `path`, which is inside the library as the audits give it

### Fixed

- a playlist entry naming an episode for a book, or another podcast's episode, took the server down; one with no episode for a podcast stored a broken entry; another library's book was accepted. Entries are checked before anything is sent
- a series renamed was not found by its new name, and its old name still resolved, for half an hour (`series_get`, `series_edit`, `series_merge`, `series:` filters, `library_filters`)
- an account limited by tag or explicit flag was shown hidden books' authors, narrators, series and titles by `library_filters`, `library_get`, `narrator_list` and `library_search`
- `user_in_progress` for the key's own account had no position or percent
- `server_sessions` and `user_history` rows never named their user; `server_sessions` for a non-admin said 404
- `author_edit`, `author_image_set`, `author_match_apply` reported `books: 0`
- `audit_matched` with no library refused, while `audit_all` counted it: pages now run on across book libraries
- following a compound genre's suggestion in two calls moved the part off every book
- a 403 on a read blamed admin rights, not a library or tag restriction
- `restoreBook` left the matched asin behind for later tests
- calls of one turn run at once, and edits of one record undid each other: eight `item_edit add_tags` calls on a book kept one tag. `item_edit`, `item_batch_edit`, the match tools, `collection_books_edit`, `playlist_entries_edit` and `user_bookmark_edit` hold the records they rewrite and read them again once held; `metadata_rename` and `series_merge` hold everything
- `podcast_add download_latest` queued nothing: the server ignores episodes sent with a new podcast
- `podcast_add` subscribed again to a feed the library already had, under another folder
- `podcast_episode_download` downloaded an episode the podcast already held as a second copy, and said an episode already downloading was queued
- `podcast_episodes` and `item_get` listed episodes newest downloaded first, not newest published
- `audit_unembedded` kept listing a book after `item_embed_metadata`: the server never reads back the tags it writes
- a bookmark on a deleted book had no title and no way to remove it; `item_delete` now removes the key user's own first, as the server will not afterwards
- a series left with no books still resolved by name, then failed with a 404
- a tool that panicked ended the whole session, stdio or HTTP: it now answers an error naming the tool, and the next call is served
- `lib/abs` downloads and uploads were cut off at two minutes, as the call limit covered reading the body, and an upload was held in memory whole; they now run as long as the file takes and stream
- an empty answer where a record was expected (something in front of the server) read as a blank record with every field empty
- `abs-mcp tools` cut a description off at "e.g."
- `lib/abs` sent a nil map as the JSON body `null`, which the server refuses: `CloseSession` with no final position never closed the session
- a write behind a redirect (an http address a proxy moves to https) reported success having done nothing, as Go turns the DELETE, PATCH or POST into a GET; the client refuses redirects, and a web page answered in the API's place
- two-word settings in `.abs-mcp` (`READ_ONLY`, `ENABLE_DELETE`, `DENY_TOOLS`) were ignored without a word, and a project `.abs-mcp` threw away the server and key in `~/.abs-mcp`
- `serve --listen` took ten seconds to stop, and exited failing, with a client connected; idle sessions were never closed
- the Docker image reported its version as `dev`: the build stamped a package that no longer exists. CI now checks the version the image reports
- the default store order and provider tag were package state, written by one server and read by another's calls (a data race); a second registration changed the first's
- a smart match wrote the provider tag over the tags the match had just filled; a match that found nothing still recorded the store it came from
- paging `library_items` from an offset that was not a whole page answered from the start of that page; a negative offset was sent to the server
- `abridged` was sent in a form the server does not know, and answered the whole library; an author or series name two records share took the first
- `podcast_episode_delete` and the episode tools took the first of two episodes with one title, which is what the server's own second download of an episode leaves: the original was deleted, not the copy
- `series_get` and `series_merge` stopped at 500 books; `series_merge` and `item_batch_edit` sent any number of updates in one request, and a failure part way said nothing of what landed
- `metadata_rename` split and sweep hid how many items changed before an error
- `author_edit` renaming onto another author and clearing the photo failed after the merge had happened
- `playlist_create from_collection` dropped the description unless the name changed
- naming the key's own account (`user_stats user=kt year=2025`) was refused as someone else's
- `user_history` for another user and one item came back empty: the item was looked for among the newest sessions only
- `item_delete` said nothing of bookmarks it had removed before failing
- `item_chapters_set` took any chapter list, and asked the US store for a Canadian book's chapters
- `podcast_feed_episodes` was in the feed's order, or the search's, not newest first
- `user_progress_set` took a percent outside 0-100, and any percent of a podcast with no episode as position 0; `podcast_add` joined a folder like `../x` onto the library's
- `audit_missing` cover, author, genres and language listed every podcast
- `audit_duplicates` missed a matched copy beside an unmatched one of the same book; it now joins copies by any key they share, never two different asins, and never by title alone two readings that name different narrators
- `audit_unembedded` never cleared a genre with a comma in it, or a publisher in an m4b (the server writes it there only as the copyright)
- the series numbering checks never ran for an Author/Series/Title layout, and `audit_series` missed one-book series a library hides
- names in other scripts were dropped by the name normalizer: every Cyrillic, Greek or CJK title was a path mismatch, and SF小説 and SF映画 one spelling
- `audit_spelling` offered to merge the store tags (`zz-provider:audible.ca` and `.uk`), did not count unrecognised languages, and called `en-US` unrecognised
- one series number far from the rest (a year typed as the number) listed thousands of missing books and set the padding width
- `audit_path` took "It" to agree with a folder named "The Institute"; a long description opening "Introduction by" was a credit line
- `audit_covers store=true` stopped for good at a book whose cover could not be fetched, repeated the library-wide findings on every page and counted ratio rows twice
- `audit_matched` windows moved when a fix changed the listing's order; `audit_genres` rows with tied counts came out in a different order each call
- `series_list` never showed which numbers a series holds: the server sends a book's series as a joined string, and only the structured list was read
- `library_search` put the exact title after the titles that contain it, so the first row was the wrong book
- a store already recorded was written again on every match when its tag was not the last
- `user_in_progress` kept books the listener had hidden from the shelf
- `user_history`'s total for another user on one book counted every session they had
- `library_search` counted hidden books in its author, narrator, tag and genre counts for a restricted account
- `podcast_downloads` never showed a download in progress (the server serves a library read from its cache until its next write; the queue is read past it), and an episode asked for twice was reported queued twice
- `podcast_episode_edit pub_date` changed nothing the server sorts by
- `item_chapters_set from_asin` wrote a store's chapters that start past the end of the audio: another recording's, which no player can reach
- `item_chapters_set from_asin` ended the last chapter where the store's recording ends, a few seconds off this file's
- a narrator or author whose name holds a comma ("Jane Doe, Ph.D.") was read as two names, and reported as a fragment `metadata_rename remove` could not find: the listing joins names with ", ", and each part is now read against the library's own names

### Changed

- CI: the unit tests are a job of the tests workflow, one tests badge; `make test` runs with the race detector; dependabot watches the Docker base images; `golang.org/x/text` 0.39.0
- the coverage badge push carries the job's own token: the checkout keeps no credentials
- the cassettes store a gzipped answer decoded, and drop CDN and request-id headers (an edge address and metro area among them); a rate limit or server error is not recorded; the Google Books cassette, nine 429s nothing replayed, is gone
- the test server listens on 127.0.0.1 only; the SDK suite has its own proxy port; `record-check` checks both suites; coverage counts the binary tests and `lib/providerproxy`
- `.dockerignore` keeps `.abs-mcp` and the test env files out of the build
- the live suite passes twice on one server: tests put back the covers, author photos, tagged audio files and providers they change, close the sessions they open and delete the backups they make

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

- one rename tool. `metadata_rename field=tags|genres|narrators|authors|languages|publishers` replaces `server_tag_rename`, `narrator_edit` and `audit_terminology_rename`, and with `remove` drops a tag, genre, narrator, language or publisher everywhere. `audit_terminology` is now `audit_spelling`, and every group it reports is fixed by the one tool. 88 tools -> 87
- `audit_no_episodes` and `audit_stale_feed` are `audit_podcast_no_episodes` and `audit_podcast_stale_feed`, so the name says they only look at podcasts
- `item_cover_edit` no longer removes the cover when called with neither `url` nor `file`: pass `remove=true`. A call that forgot its url used to delete the cover
- the README token table is now what the model sees. Most clients send only the description and input schema to the model, not the output schema, which was 60% of the earlier figure

### Added

- `audit_unembedded`: books whose audio files carry no tags, or tags that disagree with the current title, author, narrator, series, genres, year or publisher - what `item_embed_metadata` is due for after a curation pass. It reads the expanded items in batches of 50, so it runs apart from `audit_all`

### Fixed

- `item_edit clear` for `narrators`, `series`, `genres` and `tags` was a silent no-op: the empty list was dropped from the request (`omitempty`), so the server saw nothing to change. `lib/abs` list fields are now `omitzero`: nil leaves a field alone, an empty slice clears it
- `audit_cover_ratio` measured the server's 400-pixel-wide cache copy rather than the cover file, so every cover was "400 wide" and the too-small check could never fire. It now asks for the raw file, reads only the image header instead of buffering the whole image, and skips the request for items the listing already says have no cover
- `audit_spelling` (then `audit_terminology`) never found narrator spellings: the sweep sees the minified item shape, which carries narrators only as one joined `narratorName`, and only the expanded `narrators` list was read
- an audit with a server-side filter (`audit_missing`, `audit_issues`, `audit_no_audio`) overran `limit` once an earlier library had filled it: the request for the next library was sent with no limit, which the server reads as everything
- `lib/abs` `DeleteAuthorImage` returned an empty record; the server answers with the author under an `author` key, like the image and match routes

### Changed

- docker-free tests for the tools: an in-memory MCP session against a canned Audiobookshelf (`tools/handlers_test.go`), covering what the live fixtures cannot reach - a library with covers, inconsistent narrator spellings, more findings than the limit
- `lib/abs` request and response-shape tests without a server: paging, streaming, multipart upload, the vocabulary path encoding, the chapter and feed-episode unwrapping
- `intParam`/`intQuery` and `narratorID`/`vocabularyID` were the same function twice

## 0.2.0 (2026-09-12)

### Breaking

- default is now the `core` toolset: 5 read-only tools, ~4,400 tokens instead of ~33,000. `ABS_TOOLSETS=curation` for the audits and their fixers, `ABS_TOOLSETS=all` for everything
- `item_chapters` and `item_files` are now `chapters` and `files` flags on `item_get`, off by default (a 300-chapter book is 19x the rest of the answer)
- `library_stats` folded into `library_get`, `server_stats` into `server_info`, `podcast_recent` into `podcast_episodes` (omit `item` for the whole library)
- removed `library_match_all`: no candidates to review, no undo. Use `item_match` then `item_match_apply`. `lib/abs` keeps `MatchAll`
- 94 tools -> 88

### Added

- `--toolsets` / `ABS_TOOLSETS`: `core`, `curation`, `listening`, `podcasts`, `organise`, `admin`, `all`, or a resource family like `item`. `core` is always included
- `abs-mcp tools` lists what the current flags would register, grouped by toolset. No server needed; `-q` for names only
- coverage workflow and badge; `make cover` merges all three suites

### Fixed

- `server_info` says what an admin-only key could not see instead of dropping the totals silently, and no longer hides zero podcasts or zero open sessions
- `item_get chapters=true` on a podcast says chapters belong to the episodes again
- `abs-mcp tools -q > file` wrote to stdout past cobra's buffer
- the Homebrew formula never published for v0.1.0: the script was committed non-executable. The release workflow can now republish it for any tag

### Changed

- `library_items` and `library_search` say what each is for and name the other; `library_items` no longer advertises `missing:` and `issues`, which are `audit_missing` and `audit_issues`
- coverage 84.8% (`lib/abs` 87.1%, `tools` 86.1%, `cli` 58.8%); `requireBearer` 0% -> 100%
- ids are tested across every resolver, not just titles
- lowercase workflow names

## 0.1.0 (2026-09-12)

- MCP server over stdio or HTTP (`--listen`), and a CLI (`serve`, `info`, `version`)
- 94 tools: server, libraries, items, authors, series, narrators, collections, playlists, podcasts, users (progress, bookmarks, history and stats, for the API key's own account or any other), and 16 audits
- `--read-only`, `--enable-delete`, `--allow-tools` / `--deny-tools` (names, globs, or the `essential` preset); every tool carries MCP read-only/destructive annotations
- tools take names as well as ids; every response is a trimmed projection rather than the raw API payload
- `lib/abs`: a standalone Audiobookshelf client for Go, stdlib only and with no knowledge of MCP. 204 methods covering all 202 API routes (`make apicheck`); downloads stream rather than buffer
- tested against a real Audiobookshelf in Docker: `integration/` covers the client, `acceptance/` the tools, with provider calls replayed from cassettes. Every tool and every client method is exercised, and the suite fails if a registered tool has no test
- binaries for linux, darwin, windows, freebsd, openbsd and solaris, a Homebrew tap, and a linux/amd64 + linux/arm64 image on `ghcr.io/katbyte/abs-mcp` with `docker-compose.yml`
