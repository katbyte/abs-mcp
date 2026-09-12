## 0.1.0 (2026-09-12)

- CLI (`serve`, `info`, `version`) and MCP server over stdio or HTTP (`--listen`)
- `lib/abs`: Audiobookshelf API client, stdlib only, covering all 202 API routes
  (`make apicheck`); downloads stream rather than buffer
- 101 MCP tools: server, libraries, items, authors, series, narrators, collections,
  playlists, progress, podcasts, users, and 16 audits
- `--read-only`, `--enable-delete`, `--allow-tools` / `--deny-tools`
- tools take names as well as ids; every response is a trimmed projection
- tested against a real Audiobookshelf in Docker: `integration/` covers the client,
  `acceptance/` the tools, with provider calls replayed from cassettes
- Docker image, `docker-compose.yml`, multi-arch release to `ghcr.io/katbyte/abs-mcp`
