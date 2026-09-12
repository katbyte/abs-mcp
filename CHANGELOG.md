## 0.2.0 (2026-09-12)

### Breaking

- **the server now registers only the `core` toolset by default**: `server_info`, `library_list`,
  `library_search`, `library_items`, `item_get` - five read-only tools, about 4,400 tokens of
  schema instead of 33,000. Curating a library, which is what this server is for, now has to be
  asked for: `ABS_TOOLSETS=curation`. `ABS_TOOLSETS=all` restores every tool
- `item_chapters` and `item_files` are gone: `item_get` takes `chapters` and `files` instead,
  both off by default. Measured on a 300-chapter book against a real server, the chapter list is
  19 times the rest of the answer - 26,962 bytes against 1,504 - so it stays opt-in, but
  `item_get` always reports how many chapters and tracks an item has, so a model can decide
  before asking
- `library_stats` folded into `library_get`, which already made the same call and threw the
  result away; `server_stats` into `server_info`; `podcast_recent` into `podcast_episodes`
  (omit `item` for the newest across the whole library)
- `library_match_all` removed. It applied provider matches to an entire library with nothing to
  review and no undo - correction without judgment, which is the one thing this server says it
  does not do. `item_match` then `item_match_apply` is the reviewed path. `lib/abs` keeps
  `MatchAll`, so the SDK still covers every route
- 94 tools became 88 (50 read, 34 write, 4 delete)

### Added

- `--toolsets` / `ABS_TOOLSETS` registers a group rather than everything: `core`, `curation`,
  `listening`, `podcasts`, `organise`, `admin`, `all`, or a resource family like `item`,
  `podcast` or `audit`. `core` is always included, because nothing else can find a library or
  open an item. Families are derived from the registered names, so a new tool joins its family
  on its own; a test fails if a tool belongs to no curated set or to two. Globs could not do
  this: `--allow-tools` ANDs with everything, so "core plus every item tool" had no spelling
- `abs-mcp tools` lists what the current flags would register, grouped by toolset, with each
  tool's kind and what it does. Needs no server, and shares the filter with the real
  registration so it cannot drift from it. `-q` prints names only
- a coverage workflow and badge. `make cover` runs all three suites into separate coverage
  directories and merges them with `go tool covdata`, because `go test -cover ./...` reports
  about 40% for `tools/` - almost everything real happens in the live suites behind the
  `integration` tag, which that flag cannot see. The badge is published from the repository's
  own CI with no coverage service and no secret

### Fixed

- `server_info` now says what an admin-only key could not see, instead of dropping the totals
  silently - absent counts were indistinguishable from a server with no books and nobody
  listening. They are one nested object, present or absent as a whole, which also fixes
  `omitempty` hiding the perfectly ordinary answers of zero podcasts and zero open sessions
- folding `item_chapters` into `item_get` had quietly dropped its refusal for podcasts, which
  said chapters belong to each episode and to use `podcast_episode_get`. The guidance is back
- `abs-mcp tools -q > file` wrote past cobra's buffer straight to stdout; every command now
  writes through `cmd.OutOrStdout()`
- the Homebrew formula was never published for v0.1.0: `scripts/update-homebrew-formula.sh` was
  committed non-executable, so the release job failed with "Permission denied". The release
  workflow can now regenerate the formula for any existing tag without cutting a new release

### Changed

- `library_items` and `library_search` no longer read as the same tool. They are different
  endpoints - one takes a structured filter, sort and page and no text at all, the other one
  free-text query and no filters - and each now says so and names the other. `library_items`
  also stopped advertising `missing:<field>` and `issues`, which are the same server-side
  filters `audit_missing` and `audit_issues` are built on, with worse output
- coverage is measured across all three suites: 84.8% overall, `lib/abs` 87.1%, `tools` 86.1%,
  `cli` 58.8%. `requireBearer` - the whole of the authentication on `--listen` - was at 0% and
  is now at 100%
- the id half of "id, or an exact title" is tested for the first time, across every resolver.
  An id is what a model has after a search, and only the title path had ever been exercised
- workflow names are lowercase, so the badges read `build` / `tests` / `golangci-lint`

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
