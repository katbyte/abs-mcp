## 0.2.0 (2026-09-12)

### Breaking

- default is now the `core` toolset: 5 read-only tools, ~4,400 tokens instead of ~33,000.
  `ABS_TOOLSETS=curation` for the audits and their fixers, `ABS_TOOLSETS=all` for everything
- `item_chapters` and `item_files` are now `chapters` and `files` flags on `item_get`, off by
  default (a 300-chapter book is 19x the rest of the answer)
- `library_stats` folded into `library_get`, `server_stats` into `server_info`,
  `podcast_recent` into `podcast_episodes` (omit `item` for the whole library)
- removed `library_match_all`: no candidates to review, no undo. Use `item_match` then
  `item_match_apply`. `lib/abs` keeps `MatchAll`
- 94 tools -> 88

### Added

- `--toolsets` / `ABS_TOOLSETS`: `core`, `curation`, `listening`, `podcasts`, `organise`,
  `admin`, `all`, or a resource family like `item`. `core` is always included
- `abs-mcp tools` lists what the current flags would register, grouped by toolset. No server
  needed; `-q` for names only
- coverage workflow and badge; `make cover` merges all three suites

### Fixed

- `server_info` says what an admin-only key could not see instead of dropping the totals
  silently, and no longer hides zero podcasts or zero open sessions
- `item_get chapters=true` on a podcast says chapters belong to the episodes again
- `abs-mcp tools -q > file` wrote to stdout past cobra's buffer
- the Homebrew formula never published for v0.1.0: the script was committed non-executable.
  The release workflow can now republish it for any tag

### Changed

- `library_items` and `library_search` say what each is for and name the other;
  `library_items` no longer advertises `missing:` and `issues`, which are `audit_missing` and
  `audit_issues`
- coverage 84.8% (`lib/abs` 87.1%, `tools` 86.1%, `cli` 58.8%); `requireBearer` 0% -> 100%
- ids are tested across every resolver, not just titles
- lowercase workflow names

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
