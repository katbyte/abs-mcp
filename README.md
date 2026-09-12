# abs-mcp - an Audiobookshelf MCP server, CLI and Go SDK

[![GitHub release](https://img.shields.io/github/v/release/katbyte/abs-mcp?color=blueviolet)](https://github.com/katbyte/abs-mcp/releases/latest)
[![Go Version](https://img.shields.io/github/go-mod/go-version/katbyte/abs-mcp?color=00ADD8)](https://github.com/katbyte/abs-mcp/blob/main/go.mod)
[![License](https://img.shields.io/github/license/katbyte/abs-mcp?color=blue)](https://github.com/katbyte/abs-mcp/blob/main/LICENSE)
![build](https://github.com/katbyte/abs-mcp/actions/workflows/build.yaml/badge.svg)
![test](https://github.com/katbyte/abs-mcp/actions/workflows/pr-tests.yaml/badge.svg)
![lint](https://github.com/katbyte/abs-mcp/actions/workflows/pr-golangci-lint.yaml/badge.svg)

An [MCP](https://modelcontextprotocol.io) server, CLI and Go SDK for curating an
[Audiobookshelf](https://www.audiobookshelf.org) audiobook and podcast library: search it,
audit it for bad metadata, and fix what you find - from Claude Code, Claude Desktop, or any
other MCP client.

**94 tools**, including **16 audits** that sweep a whole library for the things that actually
go wrong: books never matched to a provider, missing covers or chapters, duplicates, gaps in a
series, folders that disagree with their metadata, near-duplicate genres and narrators
(`Sci-Fi` vs `sci fi`, `Jim Dale` vs `jim dale`), podcasts whose feed has gone dead.

It is two things in one repo: a **standalone Go client for the Audiobookshelf API**
(`lib/abs` - 204 methods over all 202 routes, no dependencies outside the standard library,
usable entirely on its own) and the MCP server built on top of it. Both are tested against a
real Audiobookshelf in Docker, not a stub.

### What makes this different

There are several Audiobookshelf MCP servers, and they do a useful thing: expose the API as
tools, so a model can browse your library and read your progress. This one does that too - all
202 routes are covered - but the reason it exists is the layer above:

- **It audits.** 16 sweeps over a whole library, each one looking for a specific thing that
  goes wrong in a real collection, returning a worklist rather than a dump. `audit_all` runs
  every per-item check in a single pass and tells you where to start.
- **It is also a Go SDK.** `lib/abs` is a complete Audiobookshelf API client with no
  dependencies outside the standard library and no knowledge of MCP - useful on its own,
  whether or not you care about AI.
- **It is tested against a real server.** Every tool and every client method runs against an
  actual Audiobookshelf in Docker, and the suite fails if a registered tool has no test. Seven
  response-shape bugs in this client were found that way and could not have been found any
  other way, because Audiobookshelf publishes no OpenAPI spec and its public API docs say they
  are unmaintained.

The design principle: **detection is code, correction is judgment.** The server runs cheap
deterministic checks over the whole library and produces worklists; the AI reasons only about
the anomalies. Every response is a trimmed projection of what a decision needs, never the raw
API object (an expanded library item carries every audio file, track and chapter with full
ffprobe output).

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
| `ABS_ALLOW_TOOLS` | `--allow-tools` | only register these tools (names, `library_*` globs, or `essential`) |
| `ABS_DENY_TOOLS` | `--deny-tools` | never register these tools (names or globs such as `*_delete`) |
| `ABS_LOG` | | log level (`WARN` default; `DEBUG`, `TRACE`, ...) |
| `ABS_LISTEN` | `--listen` | serve MCP over HTTP on this address (e.g. `:8080`) instead of stdio |
| `ABS_AUTH_TOKEN` | `--auth-token` | bearer token required on the HTTP endpoint |

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
instead of stdio. Set `ABS_AUTH_TOKEN` so clients must send `Authorization: Bearer <token>`;
without it anyone who can reach the port can use every tool. Register it from any machine:

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
| server | `server_info` (connectivity, permissions, libraries, providers and server-wide totals), `server_tasks`, `server_sessions`, `server_backups`, `server_backup_create`, `server_tags`, `server_tag_rename` |
| libraries | `library_list`, `library_get` (in depth, with statistics), `library_create`, `library_edit`, `library_search`, `library_items` (the server's own filters and sorts: genre, tag, author, series, narrator, progress, tracks...), `library_recent`, `library_filters`, `library_scan`, `library_match_all` |
| audits | `audit_all` (every per-item audit in one sweep - start here after a scan), `audit_missing` (field: cover, description, narrator, series, author, genres, year, publisher, language, chapters), `audit_unmatched`, `audit_issues`, `audit_no_audio`, `audit_path`, `audit_author_as_title`, `audit_single_chapter`, `audit_stale_feed`, `audit_no_episodes`, `audit_duplicates`, `audit_series_gaps`, `audit_terminology` / `audit_terminology_rename`, `audit_cover_ratio`, `audit_author_missing_image` |
| items | `item_get`, `item_chapters`, `item_files`, `item_edit`, `item_batch_edit` (same fields across many books), `item_rescan`, `item_embed_metadata` |
| matching | `item_match` (candidates from a provider), `item_match_apply`, `item_cover_search`, `item_cover_edit` (url, file, or removed), `item_chapters_set` (explicit list or from Audible by asin) |
| authors | `author_list`, `author_get`, `author_edit` (rename to merge duplicates), `author_match`, `author_image_set` |
| series | `series_list`, `series_get`, `series_edit` |
| narrators | `narrator_list`, `narrator_edit` (rename to merge, or remove) |
| collections | `collection_list`, `collection_get`, `collection_create`, `collection_edit`, `collection_books_edit` (add or remove), `collection_delete` |
| playlists | `playlist_list`, `playlist_get`, `playlist_create` (also from a collection), `playlist_edit`, `playlist_entries_edit` (add or remove), `playlist_delete` |
| podcasts | `podcast_episodes` (one show, or the newest across the library), `podcast_episode_get`, `podcast_episode_edit`, `podcast_check_new`, `podcast_feed_episodes`, `podcast_episode_download`, `podcast_downloads`, `podcast_search`, `podcast_add`, `podcast_settings` |
| users | `user_get`, `user_in_progress`, `user_progress_get`, `user_progress_set`, `user_progress_remove`, `user_bookmarks`, `user_bookmark_edit` (add or remove), `user_history`, `user_stats` (all-time or year in review), `user_list` (admin) |

`item_delete`, `podcast_episode_delete`, `author_delete` and `library_issues_remove` are only
registered when `--enable-delete` / `ABS_ENABLE_DELETE` is set. `--read-only` registers the
52 read tools and nothing else, so a write tool is absent from `tools/list` rather than refused
when called.

### Choosing which tools load

A model picks the right tool more reliably from eight than from eighty. `--allow-tools` and
`--deny-tools` take comma-separated tool names, globs with a leading or trailing `*`, or the
`essential` preset (`library_list`, `library_search`, `library_items`, `item_get`,
`item_chapters`, `user_in_progress`, `user_progress_get`, `user_progress_set`):

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

`make test` is hermetic and fast: unit tests over the pure logic - filter encoding, formatting,
gap arithmetic, the audit heuristics, tool registration.

Everything else runs against **a real Audiobookshelf in Docker**, because a stub can only
confirm what you already believed. Two suites, each in its own container:

| | Covers | Command |
|---|---|---|
| `integration/` | the `lib/abs` client: that every response decodes with its fields populated | `make testacc-integration` |
| `acceptance/` | the tools: name resolution, projections, audits, provider flows | `make testacc-acceptance` |

```bash
make testacc        # both, each in a throwaway container, torn down after
make check-all      # build + unit + both live suites + every linter
```

**All 91 tools and all 204 client methods are exercised**, 199 of them asserting a result
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
