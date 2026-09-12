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
