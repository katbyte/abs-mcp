# Changelog

## unreleased

- CLI (`serve`, `info`, `version`) and MCP server over stdio or HTTP (`--listen`)
- `lib/abs`: Audiobookshelf API client, stdlib only, 137 methods covering every
  in-scope endpoint (`make apicheck`)
- 101 MCP tools: server, libraries, items, authors, series, narrators, collections,
  playlists, progress, podcasts, users, and 16 audits
- `--read-only`, `--enable-delete`, `--allow-tools` / `--deny-tools`
- tools take names as well as ids; every response is a trimmed projection
- tested against a real Audiobookshelf in Docker: `integration/` covers the client,
  `acceptance/` the tools, with provider calls replayed from cassettes
- Docker image, `docker-compose.yml`, multi-arch release to `ghcr.io/katbyte/abs-mcp`
