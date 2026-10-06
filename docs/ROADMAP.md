# Tool roadmap

Design rules, in priority order:

1. **Wrap judgment, not plumbing.** A tool exists only where an AI has a decision to make. Streaming, cover bytes, playback session heartbeats and socket events stay unwrapped.
2. **Trim every response.** Tools return the fields a decision needs, never raw payloads (an expanded library item is thousands of lines with ffprobe output per file; `item_get` returns ~30 fields).
3. **Composite over chatty.** If a task always takes N calls (search candidates → pick → apply → verify), it is one tool, not N.
4. **Names, not just ids.** Every tool that takes a library, item, author, series, collection, playlist or user resolves a name, and an ambiguous title lists candidates.
5. **Resource-first names** (`library_*`, `item_*`, `user_*`) so tools group by what they act on.
6. **Reads are cheap, writes are explicit, destructive is opt-in.** Every tool carries MCP annotations; anything that changes the server says so in its description; anything that deletes records or files is disabled unless the operator sets `--enable-delete`.
7. **One way to say each thing.** Members of a list are added and removed with `add_x` and `remove_x` lists (`add_tags`, `add_items`, `add_entries`, `add_bookmarks`); a single thing is set by giving its value and taken away with `remove`. Stores are `providers`, an ordered list tried in turn, never one `provider`. Every list takes `limit` and `offset` and answers `total` and `next_offset`.
8. **`confirm` before what cannot be put back.** A tool that erases, merges, or writes over what is there from a store changes nothing without `confirm`, and says what it would do: `would_delete`, `would_merge`, `would_remove`, `would_apply`, `would_upgrade`.

## Done

| Area | Tools | Answers |
|---|---|---|
| know the library | `server_info`, `library_list`, `library_get`, `library_create`, `library_edit`, `narrator_list`, `library_search`, `library_items`, `library_filters`, `library_recent`, `item_get` | "what do I have, and what shape is it in" |
| curation | `audit_all` + `audit_missing` + 7 per-item audits, `audit_duplicates`, `item_match` → `item_match_apply`, `item_match_batch` → `item_match_apply_batch`, `item_cover_search` → `item_cover_edit`, `item_chapters_set`, `item_edit` (with track order and the main ebook, and `items` for the same change on many), `author_match` → `author_match_apply`, `author_edit` (merge, and a photo from a url), `metadata_rename` (merge a tag, genre, narrator, author, language or publisher), `series_merge`, `audit_series`, `audit_spelling`, `audit_authors`, `audit_narrators`, `audit_unembedded`, `audit_covers`, `audit_matched`, `audit_path` (with a year earlier than the folder's) | "what is wrong, and fix it" |
| maintenance | `library_scan`, `item_rescan`, `item_embed_metadata` (or `m4b` to merge into one file), `item_delete` (or one `file` of a book), `server_tasks` (with today's `log`), `server_backups`, `server_tags` → `metadata_rename` | "keep it healthy" |
| listening | `user_in_progress`, `user_progress_*` (and a whole series off Continue Series), `user_bookmark*`, `user_history` → `user_history_remove`, `user_stats` (and the whole server's year), `user_list`, `server_sessions`, `item_send_ebook` | "what am I / are they listening to" |
| accounts | `user_create`, `user_edit` | "let someone in" |
| organise | `collection_*`, `playlist_*`, `feed_list`, `feed_edit` (an RSS feed of a book, series or collection, or a public link to one book) | "group these, and let others listen" |
| podcasts | `podcast_episodes`, `podcast_feed_episodes` → `podcast_episode_download`, `podcast_check_new`, `podcast_search` → `podcast_add`, `podcast_edit`, `podcast_downloads` | "subscribe, catch up, back-fill" |

## Candidates

| Tool | Endpoints | Answers |
|---|---|---|
| `author_match` photo fallback * | Wikipedia `pageimages` when Audnexus has no image | "give the 88 photo-less authors a photo" |
| podcast episode matching | `POST /api/podcasts/:id/match-episodes` | "match the downloaded episodes to their feed" |
| podcast OPML | `/api/podcasts/opml/*`, `/api/libraries/:id/opml` | "import my subscriptions from another app", "export them" |
| `user_delete` | `DELETE /api/users/:id`, behind `--enable-delete` | "remove this account"; until then `user_edit active=false` stops it signing in and keeps its history |

\* Audible holds no photo or bio for many authors, and the provider faithfully returns a bare name; on 2026-09-13 the photos were found by hand instead, one Wikipedia lookup per author, with the extract read to confirm the person. Worth automating only if it recurs.

## Guarded / deliberately excluded

- `item_delete`, `podcast_episode_delete`, `author_delete`, `library_issues_remove`, `collection_delete`, `playlist_delete`, `user_history_remove`: only registered with `--enable-delete` (`ABS_ENABLE_DELETE`).
- Deleting one audio file of a book is refused: the server takes it off the book but leaves the book's length as it was, and no scan corrects that. Taking it out on disk and rescanning does.
- Not wrapping **as tools**, ever: audio streaming and playback sessions, cover/image byte delivery, uploads, server settings and auth settings, API key management, notification config, cache purges, the file system browser. `sdk/abs` covers all of them - it is a general Audiobookshelf client - but none of it is judgment an AI should be making.
