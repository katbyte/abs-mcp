# abs-mcp - an Audiobookshelf MCP server, CLI and Go SDK

[![GitHub release](https://img.shields.io/github/v/release/katbyte/abs-mcp?color=blueviolet)](https://github.com/katbyte/abs-mcp/releases/latest)
[![Go Version](https://img.shields.io/github/go-mod/go-version/katbyte/abs-mcp?color=00ADD8)](https://github.com/katbyte/abs-mcp/blob/main/go.mod)
[![License](https://img.shields.io/github/license/katbyte/abs-mcp?color=blue)](https://github.com/katbyte/abs-mcp/blob/main/LICENSE)
![build](https://github.com/katbyte/abs-mcp/actions/workflows/build.yaml/badge.svg)
![tests](https://github.com/katbyte/abs-mcp/actions/workflows/pr-integration.yaml/badge.svg)
![lint](https://github.com/katbyte/abs-mcp/actions/workflows/pr-golangci-lint.yaml/badge.svg)
[![coverage](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/katbyte/abs-mcp/badges/coverage.json)](https://github.com/katbyte/abs-mcp/actions/workflows/coverage.yaml)

An [MCP](https://modelcontextprotocol.io) server, CLI and Go SDK that **audits an [Audiobookshelf](https://www.audiobookshelf.org) library for the things that actually go wrong, and fixes what it finds** - from Claude Code, Claude Desktop, or any other MCP client.

There are several Audiobookshelf MCP servers, and they do a useful thing: expose the API as tools, so a model can browse your library and read your progress. This one does that too, but the reason it exists is the layer above: **19 audits**, each a sweep over the whole library for one specific thing that goes wrong in a real collection, returning a worklist rather than a dump, and naming the tool that fixes it.

This is not a demo. It has been battle-tested on a real collection: a large library, collected over years from every source and matched by hand or not at all, was cleaned up with these tools driven from Claude Code. Hundreds of titles were matched to the right store edition and checked against it, wrong matches caught by the folder the collector had named, author and narrator records merged and photographed, genres and tags brought to one vocabulary, and series names, numbering and titles brought to one style across the whole shelf in a sitting - every "The" dropped from a series label, every series past nine books zero-padded, every book linked to the series its folder names, every title that was really a series name replaced with the one the folder carried. Each pass was an audit, a review of the worklist, and a batch of edits. Most of the audits exist because that library had the problem.

### What else is in the box

- **The whole API, as tools.** 100 tools over all 202 Audiobookshelf routes, so everything an audit finds can be fixed from the same session: matching, covers, chapters, embedding, renaming a genre everywhere it is used, merging duplicate authors.
- **A Go SDK.** `sdk/abs` is a complete Audiobookshelf API client - 214 methods, nothing beneath it but the standard library and one small HTTP package that uses only the standard library itself, no knowledge of MCP - useful on its own, whether or not you care about AI.
- **Tested against a real server.** Every tool and every client method runs against an actual Audiobookshelf in Docker, and the suites fail if a registered tool or a client method has no test. Seven response-shape bugs in this client were found that way and could not have been found any other way, because Audiobookshelf publishes no OpenAPI spec and its public API docs say they are unmaintained.

### The audits

| audit | what it catches |
|---|---|
| `audit_all` | every audit in one call, counts only, so one call says where a library needs work - start here after a scan (`deep` adds the five that fetch something for every item, and counts `audit_chapters` and `audit_whitespace` whole) |
| `audit_unmatched` | books never matched to a metadata provider: no asin and no isbn, so nothing else can be filled in automatically; a book tagged `zz-provider:none`, checked and found on no provider, is not reported |
| `audit_missing` | items with one metadata field left empty - `field` is cover, description, narrator, series, author, genres, year, publisher, language or chapters; description also catches a stub, a credit line such as "Read by Paul Heck", or a bare url |
| `audit_issues` | items whose folder is missing from disk or holds no playable media: broken records, not metadata gaps |
| `audit_no_audio` | items with no audio tracks at all, usually an ebook-only folder that landed in an audiobook library |
| `audit_path` | items whose folder name disagrees with their title or author once subtitles, series prefixes and edition markers are set aside: a wrong match, a chapter tag left as the title, a pen name, or a book filed under another author; and books dated earlier than the year their folder carries (`Dune (1965)` holding a book dated 1959), as a recording is never older than the book; and books the record places in a series whose folder does not say so (`Salvation Lost` for book 2 of the Salvation Sequence, `Ringworld` for book 1 of Ringworld), which is one way of filing and is left out with `--audit-skip path-series`; with `files` the audio filenames are compared too |
| `audit_chapters` | everything wrong with a book's chapters: chapters that start past the end of the audio (a file taken out of a book leaves them behind), chapters out of order, chapters that end well before the audio does (a track added), and one chapter over a long book, as unnavigable as none but invisible to `audit_missing`; `item_chapters_set fit=true` fits a book's own chapters back to its audio |
| `audit_duplicates` | items that appear to be the same work: copies joined by any key they share - asin, isbn, or title+author, so a matched copy and an unmatched one of the same book are one group - but never two different asins. A title, or an isbn (the print edition's, which every recording of it carries), groups no copies anything tells apart, as a false group costs a deletion and a false split only a look: readers sharing no name (the narrator field, or a folder's brackets - `(Weiner)`, `[John Lee]` - unless they are a year, an edition, a format, a source, a language, a note on the copy or the author), a number in their names (`Vol. 17`, `CD1`, `Book One`) or a place in one series (`Honor - 01 - Above All`, `Honor - 02 - Honor Bound`, both titled `Honor`), folders naming different books, one copy holding no audio, or more than 15% apart in length; a title over 50 copies share is a placeholder and joins none. Nothing is dropped: `split` lists every title and isbn whose copies were kept apart, with how many copies, the first twenty parts and why, so a part that proves one recording is still in sight. Beside the groups, `candidates`: pairs no key joins that are probably one recording - an edition label on the same audio (`Ender's Game (20th Anniversary)`), the same title filed under another author, one copy's album tag naming the other, split files beside a single file, or one title's copies kept apart only by their folders, naming no different readers, within 4% (`Treason`, `A Planet Called Treason`) - each to confirm with `item_compare_audio`. And `incomplete`: a copy sharing a title, an asin or an isbn, more than 15% shorter than another copy, holding only some of its tracks in play order - not a second copy but a bad one, reported apart from the groups with the first five files it lacks |
| `audit_abridged` | books that are probably abridged but not marked so: the length of the store's abridged edition, far shorter than every unabridged one, or, for two readings of one book in the library, chapter lengths cut unevenly where two unabridged readings keep a steady ratio; a book under an hour long and under a fifth of the shortest unabridged edition, when the store sells no abridged edition under twice its length, comes back as `shorter_work`, probably a story sharing a collection's title and not abridged; productions (dramatised, full cast, a radio or audio drama, play or theatre, a Hörspiel, in the names or the reader credit, and not also saying unabridged) are set aside on both sides, and a copy the length of the store's production is that production (one store search per book, so it pages and `audit_all` runs it only with `deep`) |
| `audit_series` | everything wrong with series: a book missing between the lowest and highest number the library has (interior gaps only), with the unlinked book on the shelf that fills it when there is one, and what is still missing once two spellings are read as one series; two series that are one series spelled two ways (`The Chronicles of Amber` vs `Chronicles of Amber`), each with its author, fixed by `series_merge`; a book whose folder says ` - 1 - ` while its series says #4, a book whose folder puts it in a series it is not linked to, or two titles at one number; names that are not series names, ending in the word Series, called what the author is called, carrying a book number, or with a stray ™ or a space before a colon; titles that are the series name with a number, or carry it beside the real title, where the folder says what the title is; one series' folders written two ways on disk (`Pandora's Star` beside `Commonwealth Saga - 02 - Judas Unchained`, a double space, `- 3 -` beside `- 03 -`), each with the folder name that would match; and with `articles=true` the names that open with The, A or An |
| `audit_spelling` | the same value spelled several ways across genres, tags, languages and publishers: `Sci-Fi` vs `sci fi`, `en` vs `eng` vs `English`, `HarperAudio` vs `Harper Audio`, and spellings a letter apart such as `Romance` vs `Romances` |
| `audit_authors` | everything wrong with authors: a book whose author field holds its title; records never matched, with no photo, with no books, or whose biography opens with someone else's name (a wrong match: `Sarah Diemer` carrying `Sarah Miller began writing...`); and records that are one name spelled two ways (`C Z Dunn` vs `Christian Dunn`) |
| `audit_narrators` | everything wrong with the narrator field: names that wrote some books and read others (one volume written and the rest of the series read is author and narrator swapped on import; one written and many read is a narrator credited as co-author), and the spelling checks on narrators - `Read by Jim Dale`, `Narrator....Jim Dale`, `Fajer Al` vs `Fajer Al-Kaisi`, two names in one value, a stray `Ph.D.` |
| `audit_genres` | genres and tags against one rule, genres broad and few, tags fine and many: placeholders like `Audiobook` in either field, a category path written as one value (`Science Fiction & Fantasy, Fantasy`) with its parts, genres on too few books to be a genre, tags that repeat the book's genre, books with no genre; each with the `metadata_rename` call that fixes it |
| `audit_whitespace` | spaces where a name should not have them - a double space, a space at either end or before the extension (`End Credits .m4b`), a space before a colon or the look-alike `꞉`, a tab or non-breaking space (the ideographic space Japanese titles use is left alone) - in titles, subtitles, author, narrator and series names (read from the library's own lists, so `Jane Doe, Ph.D.` stays whole), every folder of the path, disc folders too, and every file in a book's folder. Each row shows the spaces and the name put right, with the item's title beside a folder or file (a double space often marks a dropped colon), the record id to fix an author or series by, and, on a folder or file row, under `taken` any suggestion already used beside it, which a plain rename would overwrite |
| `audit_unembedded` | books whose audio files do not carry the library's metadata in their tags, never embedded or stale since the last edit: what `item_embed_metadata` is due for |
| `audit_unplayable` | books with audio that will not play, though the server lists them like any other: files locked to a store's player (an iTunes FairPlay book reads as plain AAC everywhere but its container), a book whose only audio is the locked Audible original, files the server could not read, and files cut short by a download or copy that broke off; and, marked `plays`, files whose extension names another format (an `.m4b` holding mp3). It reads the start of each file that can be locked, a few kilobytes by byte range, so `audit_all` runs it only with `deep`; `decode` also plays five seconds at the start, middle and end of every file to find damage partway, which needs ffmpeg |
| `audit_covers` | everything wrong with cover art: missing, not square, or too small (reads every cover's header, so `audit_all` runs it only with `deep`); with `banner=true`, the "Only from Audible" ribbon across the bottom-right corner, found by colour and angle; and with `store=true`, compared with the store the book was matched to by perceptual hash: the same picture bigger is an `upgrade` with the full-size url, another picture is `differs` with both images to look at, a window of matched books per call, paged by offset |
| `audit_matched` | matched books whose asin is not the recording on disk: the provider's record for the asin compared on title, duration and narrator, so a match applied by title alone shows up as `duration_off` or `narrator_differs`; with `fields` every field is compared and the differences listed with both values, which is what `override_details` would change (one provider request per book, so it pages and `audit_all` runs it only with `deep`) |
| `audit_podcasts` | podcasts that need a look, each finding naming its problem: `no_episodes`, nothing downloaded; `stale_feed`, the newest episode is over 90 days old, the feed was never checked or there is no feed url: the show ended, or the feed url is dead |

The design principle: **detection is code, correction is judgment.** The server runs cheap deterministic checks over the whole library and produces worklists; the AI reasons only about the anomalies. Every response is a trimmed projection of what a decision needs, never the raw API object (an expanded library item carries every audio file, track and chapter with full ffprobe output).

## Installation

```bash
go install github.com/katbyte/abs-mcp@latest
```

Requires Audiobookshelf 2.26 or newer (earlier versions have no API keys).

## Configuration

All options can be passed as command-line flags, environment variables, or via a configuration file.

| Variable | Flag | Description |
|---|---|---|
| `ABS_SERVER` | `--server`, `-s` | Audiobookshelf URL, e.g. `http://nas:13378` |
| `ABS_TOKEN` | `--token`, `-t` | API key (Settings → Users → API Keys) |
| `ABS_READ_ONLY` | `--read-only` | register only tools that never change server state |
| `ABS_ENABLE_DELETE` | `--enable-delete` | register the tools that delete items, episodes, authors, collections and playlists |
| `ABS_PROVIDERS` | `--providers` | metadata providers to ask in order when a call names none, the store the books were bought from first: `audible.ca,audible`; default the library's own provider |
| `ABS_PROVIDER_TAG` | `--provider-tag` | prefix of the tag that records which store a match came from, `zz-provider:` by default so it sorts last in the tag list; `off` writes and reads none |
| `ABS_AUDIT_SKIP` | `--audit-skip` | audit rules to leave out. The audits hold a library to one way of keeping it and every rule is on by default; one that is a way of filing rather than a mistake can be switched off: `path-series` (`audit_path`: a book the record places in a series, in a folder that does not say so) |
| `ABS_TOOLSETS` | `--toolsets` | groups of tools to register, default `core`: `all`, `core`, `curation`, `listening`, `podcasts`, `organise`, `admin`, or a resource family like `item` (`core` is always included) |
| `ABS_ALLOW_TOOLS` | `--allow-tools` | register these tools as well as the toolsets asked for, or only these when no toolset is (names, `library_*` globs, or `essential`) |
| `ABS_DENY_TOOLS` | `--deny-tools` | never register these tools, whatever asked for them (names or globs such as `*_delete`) |
| `ABS_LOG` | | log level (`WARN` default; `DEBUG`, `TRACE`, ...) |
| `ABS_LISTEN` | `--listen` | serve MCP over HTTP on this address (e.g. `:8080`) instead of stdio |
| `ABS_AUTH_TOKEN` | `--auth-token` | bearer token required on the HTTP endpoint (required with `--listen`) |
| `ABS_ALLOW_NO_AUTH` | `--allow-no-auth` | serve HTTP with no bearer token at all: anyone who can reach the port can use every tool |

An API key acts as exactly one Audiobookshelf user and inherits that user's permissions: a key for a normal account cannot see libraries that account cannot see, and cannot scan, match or delete. Most write tools need an admin account; `server_info` reports what the key can do.

### Configuration File

You can place a `.abs-mcp` file in your home directory `~/.abs-mcp` (for global settings) and in your current directory `./.abs-mcp` (for per-project settings). Both are read, the project's over the home one, so a project file only needs the settings it changes. Keys are the environment variable names, with or without the `ABS_` prefix, in `env` format:

```env
SERVER=http://nas:13378
TOKEN=ey...
READ_ONLY=true
```

A flag or an environment variable beats either file.

## Usage

Quick connectivity check:

```bash
abs-mcp info
```

### Register with Claude Code

`.mcp.json`:

```json
{
  "mcpServers": {
    "audiobookshelf": {
      "command": "abs-mcp",
      "args": ["serve"],
      "env": {
        "ABS_SERVER": "http://nas:13378",
        "ABS_TOKEN": "..."
      }
    }
  }
}
```

Or from the shell:

```bash
claude mcp add audiobookshelf -e ABS_SERVER=http://nas:13378 -e ABS_TOKEN=... -- abs-mcp serve
```

### Run as a service (HTTP transport)

`serve --listen :8080` serves the MCP Streamable HTTP transport at `/mcp` (plus `GET /healthz`) instead of stdio. `ABS_AUTH_TOKEN` is required: clients must send `Authorization: Bearer <token>`, and the server refuses to start without one unless `ABS_ALLOW_NO_AUTH=true` says that anyone who can reach the port may use every tool. Register it from any machine:

```bash
claude mcp add --transport http audiobookshelf http://nas:8080/mcp \
  --header "Authorization: Bearer $ABS_AUTH_TOKEN"
```

### Docker

Releases publish a multi-arch (amd64, arm64) image to `ghcr.io/katbyte/abs-mcp`, tagged `vX.Y.Z`, `vX.Y` and `latest`. `docker-compose.yml` is the default always-on deployment: it runs that image and reads secrets from a gitignored `.env` (copy `.env.example`). Adjust `ABS_SERVER` and `TZ` in the compose file, then:

```bash
cp .env.example .env      # fill in ABS_TOKEN and ABS_AUTH_TOKEN
docker compose up -d
```

`make docker` builds the same image from source, tagged `abs-mcp`, with version info from git. The image is alpine-based (so `docker exec -it abs-mcp sh` works), runs as a non-root user and has a healthcheck against `/healthz`. The binary is the entrypoint, so `docker run --rm ghcr.io/katbyte/abs-mcp info` works as a connectivity check with the `ABS_*` variables passed via `-e`.

## MCP Tools

Tools are named resource-first (`library_*`, `item_*`, `user_*`...) so they group by what they act on. Every tool carries MCP annotations (read-only or destructive) and tools that change server state say so in their descriptions. Wherever a tool takes a library, item, author, series, collection, playlist or user it accepts a name as well as an id; an ambiguous title comes back as an error listing the candidates.

| Resource | Tools |
|---|---|
| server | `server_info` (connectivity, permissions, libraries, providers and server-wide totals), `server_tasks` (with `log`, today's server log, to see why a scan or merge failed), `server_sessions`, `server_backups`, `server_backup_create`, `server_tags` |
| libraries | `library_list`, `library_get` (in depth, with statistics), `library_create`, `library_edit`, `library_search`, `library_items` (the server's own filters and sorts: genre, tag, author, series, narrator, progress, tracks...), `library_recent`, `library_filters`, `library_scan`, `library_issues_remove` (delete the records of books whose folders are gone), `library_issues_merge` (after a folder was renamed or moved and the server made a new record for it: carry the old record's details, cover, chapters, everyone's progress and bookmarks, and its place in collections and playlists onto the new one, then delete the old) |
| audits | the 19 audits in [the table above](#the-audits): `audit_all`, `audit_unmatched`, `audit_missing`, `audit_issues`, `audit_no_audio`, `audit_path`, `audit_chapters`, `audit_duplicates`, `audit_abridged`, `audit_series`, `audit_spelling`, `audit_authors`, `audit_narrators`, `audit_genres`, `audit_whitespace`, `audit_unembedded`, `audit_unplayable`, `audit_covers`, `audit_matched`, `audit_podcasts` |
| items | `item_get` (with optional `chapters` and `files`), `item_edit` (also a book's track order, its chapters moving with their files, and which ebook is the main one; with `items`, the same change on many at once, `add_tags`, `remove_tags`, `add_series` and `remove_series` editing each one's own list), `item_rescan`, `item_embed_metadata` (or with `m4b`, merge the book into one m4b at its own bitrate, previewed until `confirm`), `item_send_ebook` (email the main ebook to a Kindle or Kobo set up in the web app), `item_compare_audio` (are two items the same recording? It takes stretches of one book at five points and finds each in the other by the rise and fall of the voice, allowing for a copy a few percent faster or slower, then checks the spectrum where they line up: a re-encode, a split copy or another edition's label is the same voice saying the same thing, another narrator is not, and nor is the same narrator recording the book again. `points`, `stretch_s`, `reach_s` and `speed_pct` listen harder. It reads about half of the second book over the network and needs ffmpeg where abs-mcp runs; the Docker image has it) |
| matching | `item_match` (candidates from the first of `providers`, in order, that has any), `item_match_apply`, `item_match_batch` → `item_match_apply_batch` (a window of books scored against the provider, paged by offset, a reader named in the narrator field, the folder's brackets (`(Tipton)`, `[John Lee]`) or a `Read by` credit standing in for the description each checked against the candidate's, so a copy matched to another reading is no longer exact, then the accepted rows applied by asin; both apply tools take `override_details` with a `keep` list of fields to put back afterwards, or `smart`, which fills the empty fields and then decides each remaining difference by rule, writing file-tag titles and company narrators over, keeping curated series and plain years, and reporting the rest for review; neither applies anything without `confirm`, and until then says what would be applied, and with `smart` every decision. Every applied match records its store as a `zz-provider:` tag, which the audits and the batch search ask first, and `item_match_tag` backfills it for books matched before the tag existed), `item_cover_search`, `item_cover_edit` (url, file, or `remove`), `item_cover_upgrade` (the store's full-size cover when it is bigger and the same picture, set once `confirm` is passed), `item_chapters_set` (explicit list, from Audible by asin, or `fit` to fit the book's own chapters to its audio) |
| authors | `author_list`, `author_get`, `author_edit` (rename to merge duplicates, or set a photo from a url), `author_match` → `author_match_apply` (look up on Audible, check the candidate, then apply by asin) |
| series | `series_list`, `series_get`, `series_edit`, `series_merge` (one series into another, numbers and other series kept) |
| narrators | `narrator_list` |
| metadata | `metadata_rename` (a tag, genre, narrator, author, language or publisher, everywhere it is used; renaming onto an existing value merges, `remove` drops it once `confirm` follows the answer saying what would go) |
| collections | `collection_list`, `collection_get`, `collection_create`, `collection_edit` (rename, describe, `add_items`, `remove_items`), `collection_delete` |
| playlists | `playlist_list`, `playlist_get`, `playlist_create` (also from a collection), `playlist_edit` (rename, describe, `add_entries`, `remove_entries`), `playlist_delete` |
| feeds | `feed_list`, `feed_edit` (open or close an RSS feed of a book, series or collection, or with `link` a public web page that plays one book) |
| podcasts | `podcast_episodes` (one show, or the newest across the library), `podcast_episode_get`, `podcast_episode_edit`, `podcast_check_new`, `podcast_feed_episodes`, `podcast_episode_download`, `podcast_downloads`, `podcast_search`, `podcast_add`, `podcast_edit` (the automatic download settings) |
| users | `user_get`, `user_in_progress`, `user_progress_get`, `user_progress_set` (also a whole series off the Continue Series shelf, and `remove` to delete the progress), `user_bookmarks`, `user_bookmark_edit` (`add_bookmarks`, `remove_bookmarks`), `user_history`, `user_history_remove`, `user_stats` (all-time or year in review, or the whole server's year), `user_list`, `user_create`, `user_edit` (admin) |

`item_delete` (a book, or with `file` one file of it), `podcast_episode_delete`, `author_delete`, `library_issues_remove`, `library_issues_merge`, `collection_delete`, `playlist_delete` and `user_history_remove` are only registered when `--enable-delete` / `ABS_ENABLE_DELETE` is set; the ones that erase files or many records at once (`item_delete`, `podcast_episode_delete`, `library_issues_remove`, `user_history_remove`, and `metadata_rename` with `remove`) say what they would remove and change nothing until called again with `confirm`. So do the tools that write over what a book has from a store: `item_match_apply`, `item_match_apply_batch` and `item_cover_upgrade`. `--read-only` registers the 59 read tools and nothing else, so a write tool is absent from `tools/list` rather than refused when called.

### Choosing which tools load

**The default is `core`: five read-only tools, about 1,200 tokens.** The whole surface is around 29,000 tokens of tool definitions before anyone asks a question, which is a poor way to spend a client's context by default. `--toolsets` / `ABS_TOOLSETS` loads the groups a session actually needs, and `core` comes along with whatever else is asked for, because nothing else can find a library or open an item.

**Curating a library needs `ABS_TOOLSETS=curation`** - the audits and everything that fixes what they find. `ABS_TOOLSETS=all` restores every tool.

| toolset | tools | with core | ~tokens |
|---|---|---|---|
| `core` *(default)* | 5 | 5 | 1,200 |
| `admin` | 16 | 21 | 5,100 |
| `organise` | 12 | 17 | 3,200 |
| `podcasts` | 11 | 16 | 3,200 |
| `listening` | 10 | 15 | 3,300 |
| `curation` | 46 | 51 | 18,800 |
| `all` | 100 | 100 | 28,900 |

Tokens are what the model sees: each tool's name, description and input schema, measured over a real `tools/list` at four bytes a token. Every tool also carries an output schema, another 38,000 tokens across `all`, but clients keep that to themselves to validate results rather than sending it to the model.

`--toolsets` also takes a resource family - `item`, `podcast`, `library`, `user`, `audit`, `author`, `series`, `narrator`, `collection`, `playlist`, `feed`, `server` - which is every tool with that prefix:

```sh
ABS_TOOLSETS=all                # every tool, which was the default before 0.2.0
ABS_TOOLSETS=curation           # audits plus everything that fixes what they find
ABS_TOOLSETS=listening,podcasts # a client that plays things rather than curates them
ABS_TOOLSETS=audit              # read-only detection, nothing that writes
ABS_TOOLSETS=core,item,series   # core plus two whole families
```

`abs-mcp tools` prints what the current flags would register, grouped by toolset, and needs no server:

```sh
abs-mcp tools                   # the default set
abs-mcp tools --toolsets all    # every tool
abs-mcp tools --read-only -q    # names only
```

### One tool more, or a few less

`--allow-tools` asks for tools by name, as `--toolsets` asks for them by set, and a session gets what either names: "these sets, and this tool as well". On its own, with no toolset beside it, it is only the tools it names. `--deny-tools` takes tools out of whatever was asked for, which is how a toolset is narrowed. Both take comma-separated tool names, globs with a leading or trailing `*`, and `--allow-tools` the `essential` preset (`library_list`, `library_search`, `library_items`, `item_get`, `user_in_progress`, `user_progress_get`, `user_progress_set`). Neither gets a tool past `--read-only` or the delete gate.

```sh
ABS_TOOLSETS=curation ABS_ALLOW_TOOLS=library_scan    # the curation set, and the scan as well
ABS_ALLOW_TOOLS=essential                             # the seven essential tools and nothing else
ABS_ALLOW_TOOLS=library_*,item_get,user_*             # only these
ABS_TOOLSETS=curation ABS_DENY_TOOLS=audit_*          # the curation set without its audits
ABS_DENY_TOOLS=*_delete,server_*
```

Before 0.7 an allow list beside toolsets narrowed them, so a tool the sets did not hold was silently left out.

A pattern that matches no tool aborts startup and names it, so a typo cannot silently hide a tool.

### A typical curation session

1. `audit_all` says where the library needs work; `audit_unmatched` lists the books never matched to a provider.
2. For each, `item_match` returns candidates with duration, narrator and series; compare them with the item and `item_match_apply candidate=N`.
3. `audit_missing field=cover` and `item_cover_search` / `item_cover_edit` fill the gaps.
4. `audit_missing field=chapters` finds long books with no chapters; `item_chapters_set` pulls them from Audible by asin.
5. `audit_duplicates` and `audit_series` show what to prune and what is missing.

### After a folder is renamed or moved

Audiobookshelf follows a renamed folder by its inode. Where the library sits on storage that gives a moved folder a new one (some network and pooled mounts), the next scan makes a new record for the folder and flags the old one missing, with everyone's listening progress still on it. `library_issues_merge` (in `admin`, and only with `--enable-delete`) pairs each missing record with the record made since for the same audio files, carries the old record's details, tags, chapters, cover, every account's progress and bookmarks, and its place in collections and playlists onto the new one, reads it back, and then deletes the old record. Without `confirm` it only reports. Its limits:

- the day a book was added, and the day each account started it, cannot be carried: the server sets both itself
- another account's progress, bookmarks and playlists can only be written as that account, so a confirmed run makes a key for each account that expires in fifteen minutes, and deletes it when the run ends
- a record holding the same audio that was already in the library before the folder went missing is taken for a second copy, not the folder moved, and is merged into only when `into` names it
- a missing record with an open RSS feed or share link is left as it is, since deleting it would close them
- a pair where anything fails to carry is left as it is, old record and all, with the reason
- books only: a podcast's progress hangs on its episodes

## Using the client on its own

`sdk/abs` is a plain Go client for the Audiobookshelf API with no knowledge of MCP. Beneath it are the standard library and [go-kt's HTTP package](https://github.com/katbyte/go-kt/tree/main/chttp), which **itself uses only the standard library**: nothing else is built into a program that imports it. If you only want to talk to Audiobookshelf from Go, take it and ignore the rest:

```go
import "github.com/katbyte/abs-mcp/sdk/abs"

client, err := abs.New("http://nas:13378", os.Getenv("ABS_TOKEN"))
items, err := client.Items(ctx, libraryID, abs.ItemsOptions{Limit: 50})
```

A read is asked for again when a gateway could not reach the server (502, 503, 504) or the connection dropped, three times in all; a write is sent once. It logs nothing unless handed a logger with `abs.WithLog`, and then traces every request and answer with the API key blanked: abs-mcp hands it its own, so `ABS_LOG=trace` shows the traffic. A status that is not success is a `*chttp.StatusError`; `abs.IsNotFound` and `abs.IsForbidden` read the common ones.

It has 214 methods covering **every one of Audiobookshelf's 202 API routes** - libraries, items, authors, series, narrators, collections, playlists, progress, bookmarks, podcasts, provider search, RSS feeds, tags, genres, tasks, backups, playback sessions, notifications, email, API keys, sharing, settings and user administration. File downloads stream rather than buffer, so a multi-gigabyte audiobook does not have to fit in memory.

`make apicheck` reads the route table out of the Audiobookshelf source and fails if anything is missing, so the coverage claim is checked rather than asserted. Audiobookshelf publishes no OpenAPI spec and its [public API docs say they are unmaintained](https://api.audiobookshelf.org), so the types here are written against the server source (see [docs/README.md](docs/README.md)) and then **proved against a running server** - which is the only thing that catches the server changing shape underneath you.

## Development

```bash
make            # fmt + build
make check-all  # build + unit tests + both live suites (needs docker) + every linter
```

### Tests

`make test` is hermetic and fast. It covers the pure logic - filter encoding, formatting, gap arithmetic, the audit heuristics, tool registration - and two things that need a server but not a real one: the `sdk/abs` requests and answers that have gone wrong before, each pinned against a canned server (`sdk/abs/fake_server_test.go`), and the tools end to end over an in-memory MCP session against a canned Audiobookshelf (`tools/fake_abs_test.go`). The first is not every method, about one in four: what proves every method is the live suite below. The second is where the cases the live fixtures cannot reach live: a library with covers, inconsistent spellings, tagged and untagged audio files, more findings than the limit.

Everything else runs against **a real Audiobookshelf in Docker**, because a stub can only confirm what you already believed. Two suites, each in its own container:

| | Covers | Command |
|---|---|---|
| `integration/` | the `sdk/abs` client: that every response decodes with its fields populated | `make testacc-integration` |
| `acceptance/` | the tools: name resolution, projections, audits, provider flows, journeys, and a smoke test of the built binary | `make testacc-acceptance` |

```bash
make testacc        # both, each in a throwaway container, torn down after
make check-all      # build + unit + both live suites + every linter
make cover          # all three suites, merged into one coverage number
```

The journeys (`acceptance/journey_*_test.go`) chain the tools the way a session does and read the server back after every write, because a 200 from Audiobookshelf is not proof: edits made while a scan runs; every fixable audit fixed, audited again and put back; a non-admin's playback reaching every listening tool; `audit_all` equal to each audit; every read-only lookup leaving the server byte-for-byte unchanged; accounts limited to one library or one tag; a library's whole life; writes done twice; every record got by id and by name; a podcast's whole life over a feed the suite serves itself through the provider proxy; metadata embedded, the record thrown away and the book scanned back from its file alone; a book deleted out from under collections, playlists, progress, bookmarks and someone's history; one turn's calls made at once on the same records; an account without rights calling every tool that changes the server; a match decided field by field, one asin on two books, and every page of every list read exactly once; books deleted with their files, a folder renamed under its book, a long book chaptered, and issues removed one library at a time; and an explicit book, a tag-limited listener's queue and a show holding one episode twice. They found some thirty tool bugs the per-tool tests had not, and the server behaviour behind them is listed in [docs/README.md](docs/README.md).

Those journeys, like everything else, drive `tools.RegisterAll` in process, which is every line of tool code the binary runs. What they never touch is the thin layer around it, where a breakage is silent: MCP over stdio uses stdout for the protocol, so one stray print or log line corrupts the stream and a client simply fails to connect with the whole suite still green. So `acceptance/binary_test.go` builds the real binary and speaks to it the way a client does: `serve` over stdio at the most verbose log level, with every line it writes to stdout checked for being a protocol message and nothing else; the flags, the environment and a `.abs-mcp` in the working directory each changing what a client lists; `--listen` serving `/mcp` behind the bearer check with `/healthz` open beside it, and shutting down on SIGTERM; and a start that cannot work - no server, no token, `--listen` with no auth token, a toolset that does not exist - failing with a message that names the problem rather than hanging.

Coverage has to span all three or it lies: `go test -cover ./...` reports about 40% for `tools/`, because almost everything real happens in the live suites behind the `integration` tag. `make cover` runs each into its own binary coverage directory and merges them with `go tool covdata` - stdlib tooling, no third-party merger - which is what the badge reports.

**All 100 tools and all 214 client methods are exercised**, 209 of the methods asserting a result rather than only that the call reached the server. The five that do not - sending an ebook by email, firing a notification, closing a device session, unlinking OpenID, syncing an offline session - need infrastructure a throwaway container has not got, and say so where they are written. Both are enforced rather than claimed: the acceptance suite records every tool it calls and fails if the server registered one nothing called, and a unit test reads the live suite and fails if the client has a method nothing in it calls, so neither a new tool nor a new method can ship untested. Each test file is named for the code it tests (`tools/items_test.go` for `tools/items.go`), and each package makes its canned servers in one file. Calls out to Audible, Audnexus and iTunes go through go-kt's record/replay proxy (`test/replayproxy`), so neither suite needs a network:

```bash
make record         # re-record every cassette against the real providers
make record-check   # check the cassettes still match, without rewriting them
```

`item_compare_audio` has a fourth kind of test: its thresholds are read off real speech, LibriVox's public-domain readings of one book by six different readers beside copies of one reading re-encoded, resampled, cut and split. The corpus is fetched into a cache outside the repo and nothing of it is redistributed; [docs/CALIBRATION.md](docs/CALIBRATION.md) has the licence, the pairs and the numbers.

`record-check` compares the *shape* of live responses against the recordings - renamed fields, vanished fields, changed types - and ignores values, so it goes red when a provider changes its contract rather than when a chart position moves.

Fixtures are generated, never committed: `scripts/abs-testenv.sh` writes one-second silent files with `ffmpeg` under `~/.cache/abs-mcp` (`ABS_TEST_DATA` to move them - not `$TMPDIR`, which Docker Desktop does not share), creates the libraries through `library_create`, fills them with `library_scan` and sets the metadata with `item_edit` - so building the fixtures is itself part of the coverage. Three libraries are clean, so the audits have something to leave alone; the fourth, `Messy`, is seeded with every defect they exist to find - a series spelled two ways, a gap whose missing book sits unlinked in a series folder, a narrator's name misspelt on half the books, a folder naming another book, a duplicate, a cover wearing the ribbon - and each audit has a test against it. `scripts/abs-testenv.sh fixtures` writes just the audio tree if you want to look at the layout. Requires docker, ffmpeg and jq; the suites skip when `ABS_SERVER` and `ABS_TOKEN` are unset, so they never fail for want of a daemon.

The Audiobookshelf API reference is the server source, not the public docs; see [docs/README.md](docs/README.md).
