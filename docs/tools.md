# Tool reference

Tools are named for what they act on (`library_*`, `item_*`, `user_*`), so they group by resource.

- Every tool is marked as read-only or as changing something, and a tool that changes the server says so in its description.
- Where a tool takes a library, item, author, series, collection, playlist or user, it accepts a name as well as an id. An ambiguous title comes back as an error listing the candidates.
- `abs-mcp tools --toolsets all` prints every tool with a line on what it does. It needs no server.

Which tools load is covered in [toolsets.md](toolsets.md).

## What needs a flag, and what waits for `confirm`

**Deleting.** These load only with `--enable-delete` / `ABS_ENABLE_DELETE`: `item_delete`, `podcast_episode_delete`, `author_delete`, `library_issues_remove`, `library_issues_merge`, `collection_delete`, `playlist_delete` and `user_history_remove`.

**Erasing files or many records.** These say what they would remove, and change nothing until called again with `confirm`: `item_delete`, `podcast_episode_delete`, `library_issues_remove`, `user_history_remove`, and `metadata_rename` with `remove`.

**Writing a store's details over a book's.** These wait for `confirm` too: `item_match_apply`, `item_match_apply_batch` and `item_cover_upgrade`.

**Read-only.** `--read-only` loads the 59 read tools and nothing else. A write tool is then absent from the client's list, not refused when called.

## server

- `server_info`: connectivity, permissions, libraries, providers and server-wide totals.
- `server_tasks`: with `log`, today's server log, to see why a scan or merge failed.
- `server_sessions`
- `server_backups`
- `server_backup_create`
- `server_tags`

## libraries

- `library_list`
- `library_get`: in depth, with statistics.
- `library_create`
- `library_edit`
- `library_search`
- `library_items`: the server's own filters and sorts, such as genre, tag, author, series, narrator, progress and tracks.
- `library_recent`
- `library_filters`
- `library_scan`
- `library_issues_remove`: deletes the records of books whose folders are gone.
- `library_issues_merge`: for a folder that was renamed or moved, where the server made a new record. See [recovery.md](recovery.md).

## audits

`audit_all` and the 19 audits. Each has its full rules in [audits.md](audits.md).

## items

- `item_get`: with optional `chapters` and `files`.
- `item_edit`: a book's details. Also its track order, with its chapters moving with their files, and which ebook is the main one. With `items`, the same change is made on many at once. `add_tags`, `remove_tags`, `add_series` and `remove_series` edit each one's own list.
- `item_rescan`
- `item_embed_metadata`: writes the library's metadata into the audio files. With `m4b`, it merges the book into one m4b at its own bitrate, previewed until `confirm`.
- `item_send_ebook`: emails the main ebook to a Kindle or Kobo set up in the web app.
- `item_delete`: a book, or with `file` one file of it.
- `item_compare_audio`: are two items the same recording? See below.

### item_compare_audio

It takes stretches of one book at five points and finds each in the other by the rise and fall of the voice. It allows for a copy a few percent faster or slower. Then it checks the spectrum where they line up.

- A re-encode, a split copy or another edition's label is the same voice saying the same thing.
- Another narrator is not. Nor is the same narrator recording the book again.
- `points`, `stretch_s`, `reach_s` and `speed_pct` listen harder.
- It reads about half of the second book over the network.
- It needs ffmpeg where abs-mcp runs. The Docker image has it.

How it was tuned is in [CALIBRATION.md](CALIBRATION.md).

## matching

- `item_match`: candidates from the first of `providers`, in order, that has any.
- `item_match_apply`
- `item_match_batch`, then `item_match_apply_batch`: a window of books scored against the provider, paged by offset, then the accepted rows applied by asin.
- `item_match_tag`: backfills the store tag for books matched before the tag existed.
- `item_cover_search`
- `item_cover_edit`: a url, a file, or `remove`.
- `item_cover_upgrade`: the store's full-size cover, when it is bigger and the same picture. Set once `confirm` is passed.
- `item_chapters_set`: an explicit list, from Audible by asin, or `fit` to fit the book's own chapters to its audio.

**How the batch scores a reader.** A reader is taken from the narrator field, the folder's brackets (`(Tipton)`, `[John Lee]`) or a `Read by` credit standing in for the description. Each is checked against the candidate's. So a copy matched to another reading is no longer exact.

**What applying writes.** Both apply tools take `override_details` with a `keep` list of fields to put back afterwards. Or they take `smart`, which fills the empty fields and then decides each remaining difference by rule. It writes over file-tag titles and company narrators, keeps curated series and plain years, and reports the rest for review.

**Nothing is applied without `confirm`.** Until then the tool says what would be applied, and with `smart` every decision.

**The store tag.** Every applied match records its store as a `zz-provider:` tag. The audits and the batch search ask that store first.

## authors

- `author_list`
- `author_get`
- `author_edit`: rename to merge duplicates, or set a photo from a url.
- `author_match`, then `author_match_apply`: look up on Audible, check the candidate, then apply by asin.
- `author_delete`

## series

- `series_list`
- `series_get`
- `series_edit`
- `series_merge`: one series into another, with numbers and other series kept.

## narrators

- `narrator_list`

## metadata

- `metadata_rename`: a tag, genre, narrator, author, language or publisher, everywhere it is used. Renaming onto an existing value merges the two. `remove` drops it, once `confirm` follows the answer saying what would go.

## collections

- `collection_list`
- `collection_get`
- `collection_create`
- `collection_edit`: rename, describe, `add_items`, `remove_items`.
- `collection_delete`

## playlists

- `playlist_list`
- `playlist_get`
- `playlist_create`: also from a collection.
- `playlist_edit`: rename, describe, `add_entries`, `remove_entries`.
- `playlist_delete`

## feeds

- `feed_list`
- `feed_edit`: opens or closes an RSS feed of a book, series or collection. With `link`, a public web page that plays one book.

## podcasts

- `podcast_episodes`: one show, or the newest across the library.
- `podcast_episode_get`
- `podcast_episode_edit`
- `podcast_episode_download`
- `podcast_episode_delete`
- `podcast_check_new`
- `podcast_feed_episodes`
- `podcast_downloads`
- `podcast_search`
- `podcast_add`
- `podcast_edit`: the automatic download settings.

## users

- `user_get`
- `user_in_progress`
- `user_progress_get`
- `user_progress_set`: also takes a whole series off the Continue Series shelf, and `remove` deletes the progress.
- `user_bookmarks`
- `user_bookmark_edit`: `add_bookmarks`, `remove_bookmarks`.
- `user_history`
- `user_history_remove`
- `user_stats`: all-time or a year in review, or the whole server's year.
- `user_list`, `user_create`, `user_edit`: admin only.
