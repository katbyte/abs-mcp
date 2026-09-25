# Tool roadmap

Design rules, in priority order:

1. **Wrap judgment, not plumbing.** A tool exists only where an AI has a decision to make. Streaming, cover bytes, playback session heartbeats and socket events stay unwrapped.
2. **Trim every response.** Tools return the fields a decision needs, never raw payloads (an expanded library item is thousands of lines with ffprobe output per file; `item_get` returns ~30 fields).
3. **Composite over chatty.** If a task always takes N calls (search candidates → pick → apply → verify), it is one tool, not N.
4. **Names, not just ids.** Every tool that takes a library, item, author, series, collection, playlist or user resolves a name, and an ambiguous title lists candidates.
5. **Resource-first names** (`library_*`, `item_*`, `user_*`) so tools group by what they act on.
6. **Reads are cheap, writes are explicit, destructive is opt-in.** Every tool carries MCP annotations; anything that changes the server says so in its description; anything that deletes records or files is disabled unless the operator sets `--enable-delete`.

## Done

| Area | Tools | Answers |
|---|---|---|
| know the library | `server_info`, `library_list`, `library_get`, `library_create`, `library_edit`, `narrator_list`, `library_search`, `library_items`, `library_filters`, `library_recent`, `item_get` | "what do I have, and what shape is it in" |
| curation | `audit_all` + `audit_missing` + 7 per-item audits, `audit_duplicates`, `item_match` → `item_match_apply`, `item_match_batch` → `item_match_apply_batch`, `item_cover_search` → `item_cover_edit`, `item_chapters_set`, `item_edit`, `author_match` → `author_match_apply`, `author_edit` (merge), `author_image_set`, `metadata_rename` (merge a tag, genre, narrator, author, language or publisher), `item_batch_edit`, `series_merge`, `audit_series`, `audit_spelling`, `audit_authors`, `audit_narrators`, `audit_unembedded`, `audit_covers`, `audit_matched` | "what is wrong, and fix it" |
| maintenance | `library_scan`, `item_rescan`, `item_embed_metadata`, `server_tasks`, `server_backups`, `server_tags` → `metadata_rename` | "keep it healthy" |
| listening | `user_in_progress`, `user_progress_*`, `user_bookmark*`, `user_history`, `user_stats`, `user_list`, `server_sessions` | "what am I / are they listening to" |
| organise | `collection_*`, `playlist_*` | "group these" |
| podcasts | `podcast_episodes`, `podcast_feed_episodes` → `podcast_episode_download`, `podcast_check_new`, `podcast_search` → `podcast_add`, `podcast_settings`, `podcast_downloads` | "subscribe, catch up, back-fill" |

## Candidates

| Tool | Endpoints | Answers |
|---|---|---|
| `audit_year_mismatch` | folder `(year)` vs `publishedYear` | wrong-edition matches |
| `item_send_ebook` | `POST /api/emails/send-ebook-to-device` | "send this epub to my Kobo" |
| `feed_open` / `feed_close` | `/api/feeds/*` | "make an RSS feed for this series" |
| `user_create` / `user_edit` | `/api/users` | account admin |
| `item_encode_m4b` | `/api/tools/item/:id/encode-m4b` | "merge these mp3s into one m4b" |
| `metadata_rename` sweep for podcasts | `PATCH /api/items/:id/media` per podcast item | the batch update answered 502 for two podcasts carrying language `eng` while the same change through `item_edit` went through; the sweep should send podcast items one at a time or with a podcast-shaped payload |
| `author_match` photo fallback * | Wikipedia `pageimages` when Audnexus has no image | "give the 88 photo-less authors a photo" |

\* Audible holds no photo or bio for many authors, and the provider faithfully returns a bare name; on 2026-09-13 the photos were found by hand instead, one Wikipedia lookup per author, with the extract read to confirm the person. Worth automating only if it recurs.

## Guarded / deliberately excluded

- `item_delete`, `podcast_episode_delete`, `author_delete`, `library_issues_remove`, `collection_delete`, `playlist_delete`: only registered with `--enable-delete` (`ABS_ENABLE_DELETE`).
- Not wrapping **as tools**, ever: audio streaming and playback sessions, cover/image byte delivery, uploads, server settings and auth settings, API key management, notification config, cache purges, the file system browser. `lib/abs` covers all of them - it is a general Audiobookshelf client - but none of it is judgment an AI should be making.
