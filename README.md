# abs-mcp - an Audiobookshelf MCP server, CLI and Go SDK

[![GitHub release](https://img.shields.io/github/v/release/katbyte/abs-mcp?color=blueviolet)](https://github.com/katbyte/abs-mcp/releases/latest)
[![Go Version](https://img.shields.io/github/go-mod/go-version/katbyte/abs-mcp?color=00ADD8)](https://github.com/katbyte/abs-mcp/blob/main/go.mod)
[![License](https://img.shields.io/github/license/katbyte/abs-mcp?color=blue)](https://github.com/katbyte/abs-mcp/blob/main/LICENSE)
![build](https://github.com/katbyte/abs-mcp/actions/workflows/build.yaml/badge.svg)
![test](https://github.com/katbyte/abs-mcp/actions/workflows/pr-tests.yaml/badge.svg)
![lint](https://github.com/katbyte/abs-mcp/actions/workflows/pr-golangci-lint.yaml/badge.svg)
[![coverage](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/katbyte/abs-mcp/badges/coverage.json)](https://github.com/katbyte/abs-mcp/actions/workflows/coverage.yaml)

An [MCP](https://modelcontextprotocol.io) server, CLI and Go SDK that **audits an
[Audiobookshelf](https://www.audiobookshelf.org) library for the things that actually go wrong,
and fixes what it finds** - from Claude Code, Claude Desktop, or any other MCP client.

There are several Audiobookshelf MCP servers, and they do a useful thing: expose the API as
tools, so a model can browse your library and read your progress. This one does that too, but
the reason it exists is the layer above: **16 audits**, each a sweep over the whole library
for one specific thing that goes wrong in a real collection, returning a worklist rather than
a dump, and naming the tool that fixes it.

### The audits

| audit | what it catches |
|---|---|
| `audit_all` | every audit in one call, counts only, so one call says where a library needs work - start here after a scan (`deep` adds the two slow ones) |
| `audit_unmatched` | books never matched to a metadata provider: no asin and no isbn, so nothing else can be filled in automatically |
| `audit_missing` | items with one metadata field left empty - `field` is cover, description, narrator, series, author, genres, year, publisher, language or chapters |
| `audit_issues` | items whose folder is missing from disk or holds no playable media: broken records, not metadata gaps |
| `audit_no_audio` | items with no audio tracks at all, usually an ebook-only folder that landed in an audiobook library |
| `audit_path` | items whose folder name disagrees with their title or author, which usually means the metadata was matched to the wrong book |
| `audit_single_chapter` | long books carrying exactly one chapter spanning the whole recording, as unnavigable as none but invisible to `audit_missing` |
| `audit_duplicates` | items that appear to be the same work: identical asin, isbn, or title+author, each group listing every copy with size, duration and path |
| `audit_series_gaps` | series missing a book: sequence numbers absent between the lowest and highest the library has, interior gaps only |
| `audit_spelling` | the same value spelled several ways across genres, tags, languages and publishers: `Sci-Fi` vs `sci fi`, `en` vs `eng` vs `English`, `HarperAudio` vs `Harper Audio`, and spellings a letter apart such as `Romance` vs `Romances` |
| `audit_authors` | everything wrong with authors: a book whose author field holds its title; records never matched, with no photo, with no books, or whose biography opens with someone else's name (a wrong match: `Sarah Diemer` carrying `Sarah Miller began writing...`); and records that are one name spelled two ways (`C Z Dunn` vs `Christian Dunn`) |
| `audit_narrators` | everything wrong with the narrator field: names that wrote some books and read others (one volume written and the rest of the series read is author and narrator swapped on import; one written and many read is a narrator credited as co-author), and the spelling checks on narrators - `Read by Jim Dale`, `Narrator....Jim Dale`, `Fajer Al` vs `Fajer Al-Kaisi`, two names in one value, a stray `Ph.D.` |
| `audit_unembedded` | books whose audio files do not carry the library's metadata in their tags, never embedded or stale since the last edit: what `item_embed_metadata` is due for |
| `audit_cover_ratio` | covers that are not square or too small to look right in a client (fetches every cover's header, so `audit_all` runs it only with `deep`) |
| `audit_podcast_stale_feed` | podcasts with no new episodes in 90 days, or whose feed was never checked: the show ended, or the feed url is dead |
| `audit_podcast_no_episodes` | podcasts with nothing downloaded |

The design principle: **detection is code, correction is judgment.** The server runs cheap
deterministic checks over the whole library and produces worklists; the AI reasons only about
the anomalies. Every response is a trimmed projection of what a decision needs, never the raw
API object (an expanded library item carries every audio file, track and chapter with full
ffprobe output).

### What else is in the box

- **The whole API, as tools.** 88 tools over all 202 Audiobookshelf routes, so everything an
  audit finds can be fixed from the same session: matching, covers, chapters, embedding,
  renaming a genre everywhere it is used, merging duplicate authors.
- **A Go SDK.** `lib/abs` is a complete Audiobookshelf API client - 204 methods, no
  dependencies outside the standard library, no knowledge of MCP - useful on its own, whether
  or not you care about AI.
- **Tested against a real server.** Every tool and every client method runs against an
  actual Audiobookshelf in Docker, and the suite fails if a registered tool has no test. Seven
  response-shape bugs in this client were found that way and could not have been found any
  other way, because Audiobookshelf publishes no OpenAPI spec and its public API docs say they
  are unmaintained.

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
| `ABS_ENABLE_DELETE` | `--enable-delete` | register the tools that delete items, episodes and authors |
| `ABS_TOOLSETS` | `--toolsets` | groups of tools to register, default `core`: `all`, `core`, `curation`, `listening`, `podcasts`, `organise`, `admin`, or a resource family like `item` (`core` is always included) |
| `ABS_ALLOW_TOOLS` | `--allow-tools` | only register these tools (names, `library_*` globs, or `essential`) |
| `ABS_DENY_TOOLS` | `--deny-tools` | never register these tools (names or globs such as `*_delete`) |
| `ABS_LOG` | | log level (`WARN` default; `DEBUG`, `TRACE`, ...) |
| `ABS_LISTEN` | `--listen` | serve MCP over HTTP on this address (e.g. `:8080`) instead of stdio |
| `ABS_AUTH_TOKEN` | `--auth-token` | bearer token required on the HTTP endpoint (required with `--listen`) |
| `ABS_ALLOW_NO_AUTH` | `--allow-no-auth` | serve HTTP with no bearer token at all: anyone who can reach the port can use every tool |

An API key acts as exactly one Audiobookshelf user and inherits that user's permissions: a key
for a normal account cannot see libraries that account cannot see, and cannot scan, match or
delete. Most write tools need an admin account; `server_info` reports what the key can do.

### Configuration File

You can place a `.abs-mcp` file in your home directory `~/.abs-mcp` (for global settings)
or in your current directory `./.abs-mcp` (for per-project settings). Keys match the long flag
names using the `env` format:

```env
SERVER=http://nas:13378
TOKEN=ey...
```

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

`serve --listen :8080` serves the MCP Streamable HTTP transport at `/mcp` (plus `GET /healthz`)
instead of stdio. `ABS_AUTH_TOKEN` is required: clients must send `Authorization: Bearer
<token>`, and the server refuses to start without one unless `ABS_ALLOW_NO_AUTH=true` says
that anyone who can reach the port may use every tool. Register it from any machine:

```bash
claude mcp add --transport http audiobookshelf http://nas:8080/mcp \
  --header "Authorization: Bearer $ABS_AUTH_TOKEN"
```

### Docker

Releases publish a multi-arch (amd64, arm64) image to `ghcr.io/katbyte/abs-mcp`, tagged
`vX.Y.Z`, `vX.Y` and `latest`. `docker-compose.yml` is the default always-on deployment: it runs
that image and reads secrets from a gitignored `.env` (copy `.env.example`). Adjust
`ABS_SERVER` and `TZ` in the compose file, then:

```bash
cp .env.example .env      # fill in ABS_TOKEN and ABS_AUTH_TOKEN
docker compose up -d
```

`make docker` builds the same image from source, tagged `abs-mcp`, with version info from
git. The image is alpine-based (so `docker exec -it abs-mcp sh` works), runs as a non-root
user and has a healthcheck against `/healthz`. The binary is the entrypoint, so `docker run --rm
ghcr.io/katbyte/abs-mcp info` works as a connectivity check with the `ABS_*` variables
passed via `-e`.

## MCP Tools

Tools are named resource-first (`library_*`, `item_*`, `user_*`...) so they group by what they
act on. Every tool carries MCP annotations (read-only or destructive) and tools that change
server state say so in their descriptions. Wherever a tool takes a library, item, author,
series, collection, playlist or user it accepts a name as well as an id; an ambiguous title
comes back as an error listing the candidates.

| Resource | Tools |
|---|---|
| server | `server_info` (connectivity, permissions, libraries, providers and server-wide totals), `server_tasks`, `server_sessions`, `server_backups`, `server_backup_create`, `server_tags` |
| libraries | `library_list`, `library_get` (in depth, with statistics), `library_create`, `library_edit`, `library_search`, `library_items` (the server's own filters and sorts: genre, tag, author, series, narrator, progress, tracks...), `library_recent`, `library_filters`, `library_scan` |
| audits | the 16 audits in [the table above](#the-audits): `audit_all`, `audit_unmatched`, `audit_missing`, `audit_issues`, `audit_no_audio`, `audit_path`, `audit_single_chapter`, `audit_duplicates`, `audit_series_gaps`, `audit_spelling`, `audit_authors`, `audit_narrators`, `audit_unembedded`, `audit_cover_ratio`, `audit_podcast_stale_feed`, `audit_podcast_no_episodes` |
| items | `item_get` (with optional `chapters` and `files`), `item_edit`, `item_batch_edit` (same fields across many books), `item_rescan`, `item_embed_metadata` |
| matching | `item_match` (candidates from a provider), `item_match_apply`, `item_cover_search`, `item_cover_edit` (url, file, or `remove`), `item_chapters_set` (explicit list or from Audible by asin) |
| authors | `author_list`, `author_get`, `author_edit` (rename to merge duplicates), `author_match` → `author_match_apply` (look up on Audible, check the candidate, then apply by asin), `author_image_set` |
| series | `series_list`, `series_get`, `series_edit` |
| narrators | `narrator_list` |
| metadata | `metadata_rename` (a tag, genre, narrator, author, language or publisher, everywhere it is used; renaming onto an existing value merges, `remove` drops it) |
| collections | `collection_list`, `collection_get`, `collection_create`, `collection_edit`, `collection_books_edit` (add or remove), `collection_delete` |
| playlists | `playlist_list`, `playlist_get`, `playlist_create` (also from a collection), `playlist_edit`, `playlist_entries_edit` (add or remove), `playlist_delete` |
| podcasts | `podcast_episodes` (one show, or the newest across the library), `podcast_episode_get`, `podcast_episode_edit`, `podcast_check_new`, `podcast_feed_episodes`, `podcast_episode_download`, `podcast_downloads`, `podcast_search`, `podcast_add`, `podcast_settings` |
| users | `user_get`, `user_in_progress`, `user_progress_get`, `user_progress_set`, `user_progress_remove`, `user_bookmarks`, `user_bookmark_edit` (add or remove), `user_history`, `user_stats` (all-time or year in review), `user_list` (admin) |

`item_delete`, `podcast_episode_delete`, `author_delete` and `library_issues_remove` are only
registered when `--enable-delete` / `ABS_ENABLE_DELETE` is set. `--read-only` registers the
51 read tools and nothing else, so a write tool is absent from `tools/list` rather than refused
when called.

### Choosing which tools load

**The default is `core`: five read-only tools, about 1,000 tokens.** The whole surface is
around 12,000 tokens of tool definitions before anyone asks a question, which is a poor way to
spend a client's context by default. `--toolsets` / `ABS_TOOLSETS` loads the groups a session actually
needs, and `core` comes along with whatever else is asked for, because nothing else can find a
library or open an item.

**Curating a library needs `ABS_TOOLSETS=curation`** - the audits and everything that fixes
what they find. `ABS_TOOLSETS=all` restores every tool.

| toolset | tools | with core | ~tokens |
|---|---|---|---|
| `core` *(default)* | 5 | 5 | 1,000 |
| `admin` | 13 | 18 | 2,200 |
| `organise` | 12 | 17 | 2,100 |
| `podcasts` | 11 | 16 | 2,600 |
| `listening` | 9 | 14 | 2,400 |
| `curation` | 38 | 43 | 6,900 |
| `all` | 88 | 88 | 12,300 |

Tokens are what the model sees: each tool's name, description and input schema, measured over
a real `tools/list` at four bytes a token. Every tool also carries an output schema, another
17,000 tokens across `all`, but clients keep that to themselves to validate results rather than
sending it to the model.

`--toolsets` also takes a resource family - `item`, `podcast`, `library`, `user`, `audit`,
`author`, `series`, `narrator`, `collection`, `playlist`, `server` - which is every tool with
that prefix:

```sh
ABS_TOOLSETS=all                # every tool, which was the default before 0.2.0
ABS_TOOLSETS=curation           # audits plus everything that fixes what they find
ABS_TOOLSETS=listening,podcasts # a client that plays things rather than curates them
ABS_TOOLSETS=audit              # read-only detection, nothing that writes
ABS_TOOLSETS=core,item,series   # core plus two whole families
```

`abs-mcp tools` prints what the current flags would register, grouped by toolset, and needs no
server:

```sh
abs-mcp tools                   # the default set
abs-mcp tools --toolsets all    # every tool
abs-mcp tools --read-only -q    # names only
```

### Narrowing further

`--allow-tools` and `--deny-tools` narrow whatever the toolsets left, and
take comma-separated tool names, globs with a leading or trailing `*`, or the
`essential` preset (`library_list`, `library_search`, `library_items`, `item_get`, `user_in_progress`, `user_progress_get`, `user_progress_set`):

```sh
ABS_ALLOW_TOOLS=essential
ABS_ALLOW_TOOLS=library_*,item_get,user_*
ABS_DENY_TOOLS=*_delete,server_*
```

A pattern that matches no tool aborts startup and names it, so a typo cannot silently hide a
tool.

### A typical curation session

1. `audit_all` says where the library needs work; `audit_unmatched` lists the books never matched to a provider.
2. For each, `item_match` returns candidates with duration, narrator and series; compare them
   with the item and `item_match_apply candidate=N`.
3. `audit_missing field=cover` and `item_cover_search` / `item_cover_edit` fill the gaps.
4. `audit_missing field=chapters` finds long books with no chapters; `item_chapters_set` pulls
   them from Audible by asin.
5. `audit_duplicates` and `audit_series_gaps` show what to prune and what is missing.

## Using the client on its own

`lib/abs` is a plain Go client for the Audiobookshelf API with **no dependencies outside the
standard library**, and no knowledge of MCP. If you only want to talk to Audiobookshelf from Go,
take it and ignore the rest:

```go
import "github.com/katbyte/abs-mcp/lib/abs"

client, err := abs.New("http://nas:13378", os.Getenv("ABS_TOKEN"))
items, err := client.Items(ctx, libraryID, abs.ItemsOptions{Limit: 50})
```

It has 204 methods covering **every one of Audiobookshelf's 202 API routes** - libraries,
items, authors, series, narrators, collections, playlists, progress, bookmarks, podcasts,
provider search, RSS feeds, tags, genres, tasks, backups, playback sessions, notifications,
email, API keys, sharing, settings and user administration. File downloads stream rather than
buffer, so a multi-gigabyte audiobook does not have to fit in memory.

`make apicheck` reads the route table out of the Audiobookshelf source and fails if anything
is missing, so the coverage claim is checked rather than asserted.
Audiobookshelf publishes no OpenAPI spec and its
[public API docs say they are unmaintained](https://api.audiobookshelf.org), so the types here
are written against the server source (see [docs/README.md](docs/README.md)) and then **proved
against a running server** - which is the only thing that catches the server changing shape
underneath you.

## Development

```bash
make            # fmt + build
make check-all  # build + unit tests + both live suites (needs docker) + every linter
```

### Tests

`make test` is hermetic and fast. It covers the pure logic - filter encoding, formatting, gap
arithmetic, the audit heuristics, tool registration - and two things that need a server but not
a real one: every `lib/abs` request shape and response decoding against a canned server
(`lib/abs/requests_test.go`), and the tools end to end over an in-memory MCP session against a
canned Audiobookshelf (`tools/handlers_test.go`). The second is where the cases the live
fixtures cannot reach live: a library with covers, inconsistent spellings, tagged and untagged
audio files, more findings than the limit.

Everything else runs against **a real Audiobookshelf in Docker**, because a stub can only
confirm what you already believed. Two suites, each in its own container:

| | Covers | Command |
|---|---|---|
| `integration/` | the `lib/abs` client: that every response decodes with its fields populated | `make testacc-integration` |
| `acceptance/` | the tools: name resolution, projections, audits, provider flows | `make testacc-acceptance` |

```bash
make testacc        # both, each in a throwaway container, torn down after
make check-all      # build + unit + both live suites + every linter
make cover          # all three suites, merged into one coverage number
```

Coverage has to span all three or it lies: `go test -cover ./...` reports about 40% for
`tools/`, because almost everything real happens in the live suites behind the `integration`
tag. `make cover` runs each into its own binary coverage directory and merges them with
`go tool covdata` - stdlib tooling, no third-party merger - which is what the badge reports.

**All 88 tools and all 204 client methods are exercised**, 199 of them asserting a result
rather than only that the call reached the server. The five that do not - sending an ebook by
email, firing a notification, closing a device session, unlinking OpenID, syncing an offline
session - need infrastructure a throwaway container has not got, and say so where they are
written. Tool coverage is enforced rather than claimed: the acceptance suite records every tool
it calls and fails if the server registered one nothing called, so a new tool cannot ship
untested. Calls out to Audible, Audnexus and
iTunes go through a record/replay proxy (`lib/providerproxy`), so neither suite needs a network:

```bash
make record         # re-record the cassettes against the real providers
make record-check   # check the cassettes still match, without rewriting them
```

`record-check` compares the *shape* of live responses against the recordings - renamed fields,
vanished fields, changed types - and ignores values, so it goes red when a provider changes its
contract rather than when a chart position moves.

Fixtures are generated, never committed: `scripts/abs-testenv.sh` writes one-second silent files
with `ffmpeg` under `~/.cache/abs-mcp` (`ABS_TEST_DATA` to move them - not `$TMPDIR`, which
Docker Desktop does not share), creates the libraries through `library_create`, fills them
with `library_scan` and sets the metadata with `item_edit` - so building the fixtures is itself
part of the coverage. `scripts/abs-testenv.sh fixtures` writes just the audio tree if you want
to look at the layout. Requires docker, ffmpeg and jq; the suites skip when `ABS_SERVER` and
`ABS_TOKEN` are unset, so they never fail for want of a daemon.

The Audiobookshelf API reference is the server source, not the public docs; see
[docs/README.md](docs/README.md).
