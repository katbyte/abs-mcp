# Changelog

## unreleased

- initial project scaffold: cobra/viper CLI (`serve`, `info`, `version`), MCP server over stdio
- Audiobookshelf API client (`lib/abs`) typed against the server source (the public API docs
  are out of date), covering libraries, items, authors, series, collections, playlists,
  listening progress, bookmarks, sessions, podcasts, provider search, tasks and backups
- 80 MCP tools (resource-first naming; 76 by default, 4 more with `--enable-delete`) covering server ops, libraries, audits, items and
  metadata matching, authors, series, collections, playlists, the API key user's progress
  and history, podcasts, and user administration
- every tool carries MCP annotations (read-only / destructive); `--read-only` registers only
  read tools, `--enable-delete` gates the tools that delete items, episodes and authors,
  `--allow-tools` / `--deny-tools` narrow the set (names, `library_*` globs, or the
  `essential` preset)
- tools accept names as well as ids: libraries, items (by exact title), authors, series,
  collections, playlists and users are all resolved, with candidates listed when ambiguous
- every response is a trimmed projection; audits return worklists (`library_audit` with 17
  checks, `library_duplicates`)
- `serve --listen` / `ABS_LISTEN` serves the Streamable HTTP transport at `/mcp` (with
  `GET /healthz`); `ABS_AUTH_TOKEN` requires a bearer token on it
- Dockerfile (alpine, non-root, healthcheck, HTTP transport by default), `docker-compose.yml` and
  `make docker`; releases push a multi-arch image to `ghcr.io/katbyte/abs-mcp`
