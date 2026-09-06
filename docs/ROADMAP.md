# Tool roadmap

Design rules, in priority order:

1. **Wrap judgment, not plumbing.** A tool exists only where an AI has a decision to
   make. Streaming, cover bytes, playback session heartbeats and socket events stay unwrapped.
2. **Trim every response.** Tools return the fields a decision needs, never raw payloads
   (an expanded library item is thousands of lines with ffprobe output per file; `item_get`
   returns ~30 fields).
3. **Composite over chatty.** If a task always takes N calls (search candidates →
   pick → apply → verify), it is one tool, not N.
4. **Names, not just ids.** Every tool that takes a library, item, author, series,
   collection, playlist or user resolves a name, and an ambiguous title lists candidates.
5. **Resource-first names** (`library_*`, `item_*`, `me_*`) so tools group by what they act on.
6. **Reads are cheap, writes are explicit, destructive is opt-in.** Every tool carries MCP
   annotations; anything that changes the server says so in its description; anything that
   deletes records or files is disabled unless the operator sets `--enable-delete`.

## Done

| Area | Tools | Answers |
|---|---|---|
| know the library | `server_info`, `library_list`, `library_get`, `library_search`, `library_items`, `library_filters`, `library_stats`, `library_recent`, `item_get`, `item_chapters`, `item_files` | "what do I have, and what shape is it in" |
| curation | `library_audit` (17 checks), `library_duplicates`, `item_match` → `item_match_apply`, `item_cover_search` → `item_cover_set`, `item_chapters_set`, `item_edit`, `author_match`, `author_edit` (merge), `series_list` (gaps) | "what is wrong, and fix it" |
| maintenance | `library_scan`, `library_match_all`, `item_rescan`, `item_embed_metadata`, `server_tasks`, `server_backups`, `server_rename_tag` | "keep it healthy" |
| listening | `me_in_progress`, `me_progress_*`, `me_bookmark*`, `me_history`, `me_stats`, `server_sessions`, `user_*` | "what am I / are they listening to" |
| organise | `collection_*`, `playlist_*` | "group these" |
| podcasts | `podcast_episodes`, `podcast_feed_episodes` → `podcast_episode_download`, `podcast_check_new`, `podcast_search` → `podcast_add`, `podcast_settings`, `podcast_downloads`, `podcast_recent` | "subscribe, catch up, back-fill" |

## Candidates

| Tool | Endpoints | Answers |
|---|---|---|
| `library_audit check=year_mismatch` | folder `(year)` vs `publishedYear` | wrong-edition matches |
| `item_batch_edit` | `POST /api/items/batch/update` | "set the genre on these 40 books" in one call |
| `item_send_ebook` | `POST /api/emails/send-ebook-to-device` | "send this epub to my Kobo" |
| `feed_open` / `feed_close` | `/api/feeds/*` | "make an RSS feed for this series" |
| `user_create` / `user_edit` | `/api/users` | account admin |
| `item_encode_m4b` | `/api/tools/item/:id/encode-m4b` | "merge these mp3s into one m4b" |
| `library_audit check=narrator_case` | vocabulary normalisation against `library_filters` | "Jim Dale" vs "jim dale" |

## Guarded / deliberately excluded

- `item_delete`, `podcast_episode_delete`, `author_delete`, `library_remove_issues`: only
  registered with `--enable-delete` (`ABS_ENABLE_DELETE`).
- Not wrapping, ever: audio streaming and playback sessions, cover/image byte delivery,
  uploads, server settings and auth settings, API key management, notification config,
  cache purges, the file system browser.
