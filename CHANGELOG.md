## Unreleased

- `--toolsets` / `ABS_TOOLSETS` registers a group of tools rather than all of them:
  `core`, `curation`, `listening`, `podcasts`, `organise`, `admin`, `all`, or a resource
  family like `item`. `core` is always included
- **the default is now `core`** - five read-only tools, ~4,400 tokens of schema instead of
  ~33,000. `ABS_TOOLSETS=curation` for the audits and their fixers, `ABS_TOOLSETS=all` for
  the previous behaviour
- `abs-mcp tools` lists what the current flags would register, grouped by toolset; needs no
  server
- 97 tools became 88: the `me_*` family folded into `user_*` (omit `user` for your own
  account), `library_stats` into `library_get`, `server_stats` into `server_info`,
  `podcast_recent` into `podcast_episodes`, and `item_chapters`/`item_files` into `item_get`
  as opt-in `chapters` and `files`
- removed `library_match_all`: it applied provider matches to a whole library with nothing
  to review and no undo
- `server_tag_get` renamed to `server_tags`
- `server_info` says what an admin-only key could not see rather than omitting it silently
- fixed: a per-project `.abs-mcp` was ignored whenever a `~/.abs-mcp` existed

## 0.1.0 (2026-09-12)

- MCP server over stdio or HTTP (`--listen`), and a CLI (`serve`, `info`, `version`)
- 94 tools: server, libraries, items, authors, series, narrators, collections, playlists,
  podcasts, users (progress, bookmarks, history and stats, for the API key's own account or
  any other), and 16 audits
- `--read-only`, `--enable-delete`, `--allow-tools` / `--deny-tools` (names, globs, or the
  `essential` preset); every tool carries MCP read-only/destructive annotations
- tools take names as well as ids; every response is a trimmed projection rather than the
  raw API payload
- `lib/abs`: a standalone Audiobookshelf client for Go, stdlib only and with no knowledge of
  MCP. 204 methods covering all 202 API routes (`make apicheck`); downloads stream rather
  than buffer
- tested against a real Audiobookshelf in Docker: `integration/` covers the client,
  `acceptance/` the tools, with provider calls replayed from cassettes. Every tool and every
  client method is exercised, and the suite fails if a registered tool has no test
- binaries for linux, darwin, windows, freebsd, openbsd and solaris, a Homebrew tap, and a
  linux/amd64 + linux/arm64 image on `ghcr.io/katbyte/abs-mcp` with `docker-compose.yml`
