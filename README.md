# abs-mcp

[![GitHub release](https://img.shields.io/github/v/release/katbyte/abs-mcp?color=blueviolet)](https://github.com/katbyte/abs-mcp/releases/latest)
[![Go Version](https://img.shields.io/github/go-mod/go-version/katbyte/abs-mcp?color=00ADD8)](https://github.com/katbyte/abs-mcp/blob/main/go.mod)
[![License](https://img.shields.io/github/license/katbyte/abs-mcp?color=blue)](https://github.com/katbyte/abs-mcp/blob/main/LICENSE)
![build](https://github.com/katbyte/abs-mcp/actions/workflows/build.yaml/badge.svg)
![test](https://github.com/katbyte/abs-mcp/actions/workflows/pr-tests.yaml/badge.svg)
![lint](https://github.com/katbyte/abs-mcp/actions/workflows/pr-golangci-lint.yaml/badge.svg)

An MCP server (and CLI) for curating an [Audiobookshelf](https://www.audiobookshelf.org) library:
search, inspect, audit and fix metadata, manage listening progress, collections, playlists and
podcasts from an AI client such as Claude Code.

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

Tools are named resource-first (`library_*`, `item_*`, `me_*`...) so they group by what they
act on. Every tool carries MCP annotations (read-only or destructive) and tools that change
server state say so in their descriptions. Wherever a tool takes a library, item, author,
series, collection, playlist or user it accepts a name as well as an id; an ambiguous title
comes back as an error listing the candidates.

| Resource | Tools |
|---|---|
| server | `server_info`, `server_stats`, `server_tasks`, `server_sessions`, `server_backups`, `server_backup_create`, `server_rename_tag` |
| libraries | `library_list`, `library_get`, `library_search`, `library_items` (the server's own filters: genre, tag, author, series, narrator, progress, missing metadata, issues...), `library_recent`, `library_filters`, `library_stats`, `library_scan`, `library_match_all` |
| audits | `library_audit` (checks: unmatched, cover, description, narrator, series, author, genres, year, publisher, language, chapters, single_file, issues, no_audio, path, stale_feed, no_episodes), `library_duplicates` |
| items | `item_get`, `item_chapters`, `item_files`, `item_edit`, `item_rescan`, `item_embed_metadata` |
| matching | `item_match` (candidates from a provider), `item_match_apply`, `item_cover_search`, `item_cover_set`, `item_cover_remove`, `item_chapters_set` (explicit list or from Audible by asin) |
| authors | `author_list`, `author_get`, `author_edit` (rename to merge duplicates), `author_match` |
| series | `series_list` (with sequence gaps), `series_get`, `series_edit` |
| collections | `collection_list`, `collection_get`, `collection_create`, `collection_edit`, `collection_add`, `collection_remove`, `collection_delete` |
| playlists | `playlist_list`, `playlist_get`, `playlist_create` (also from a collection), `playlist_edit`, `playlist_add`, `playlist_remove`, `playlist_delete` |
| me (the API key's user) | `me_get`, `me_in_progress`, `me_progress_get`, `me_progress_set`, `me_progress_remove`, `me_bookmarks`, `me_bookmark_add`, `me_bookmark_remove`, `me_history`, `me_stats` (all-time or year in review) |
| podcasts | `podcast_episodes`, `podcast_episode_get`, `podcast_episode_edit`, `podcast_check_new`, `podcast_feed_episodes`, `podcast_episode_download`, `podcast_downloads`, `podcast_recent`, `podcast_search`, `podcast_add`, `podcast_settings` |
| users (admin) | `user_list`, `user_get`, `user_history`, `user_stats` |

`item_delete`, `podcast_episode_delete`, `author_delete` and `library_remove_issues` are only
registered when `--enable-delete` / `ABS_ENABLE_DELETE` is set. `--read-only` registers the
43 read tools and nothing else, so a write tool is absent from `tools/list` rather than refused
when called.

### Choosing which tools load

A model picks the right tool more reliably from eight than from eighty. `--allow-tools` and
`--deny-tools` take comma-separated tool names, globs with a leading or trailing `*`, or the
`essential` preset (`library_list`, `library_search`, `library_items`, `item_get`,
`item_chapters`, `me_in_progress`, `me_progress_get`, `me_progress_set`):

```sh
ABS_ALLOW_TOOLS=essential
ABS_ALLOW_TOOLS=library_*,item_get,me_*
ABS_DENY_TOOLS=*_delete,server_*
```

A pattern that matches no tool aborts startup and names it, so a typo cannot silently hide a
tool.

### A typical curation session

1. `library_audit check=unmatched` lists books that were never matched to a provider.
2. For each, `item_match` returns candidates with duration, narrator and series; compare them
   with the item and `item_match_apply candidate=N`.
3. `library_audit check=cover` and `item_cover_search` / `item_cover_set` fill the gaps.
4. `library_audit check=chapters` finds long books with no chapters; `item_chapters_set` pulls
   them from Audible by asin.
5. `library_duplicates` and `series_list` (sequence gaps) show what to prune and what is
   missing.

## Development

```bash
make            # fmt + build
make check-all  # build + test + all linters + depscheck
```

Dev tools are pinned in `.tools/go.mod` (actionlint in `.tools/actionlint/go.mod`) and built
into `.tools/bin` by make. On a noexec checkout point `TOOLS_BIN` somewhere local, e.g.
`make TOOLS_BIN=~/.cache/abs-mcp/bin lint`.

The Audiobookshelf API reference is the server source, not the public docs; see
[docs/README.md](docs/README.md).
