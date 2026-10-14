# Configuration

Every setting can be a command-line flag, an environment variable or a line in a configuration file. Only the server and the token are required.

| Variable | Flag | What it does |
|---|---|---|
| `ABS_SERVER` | `--server`, `-s` | the Audiobookshelf address, such as `http://nas:13378` |
| `ABS_TOKEN` | `--token`, `-t` | an API key, from Settings → Users → API Keys |
| `ABS_READ_ONLY` | `--read-only` | load only tools that never change the server |
| `ABS_ENABLE_DELETE` | `--enable-delete` | load the tools that delete items, episodes, authors, collections and playlists |
| `ABS_TOOLSETS` | `--toolsets` | groups of tools to load; `core` by default. See [toolsets.md](toolsets.md) |
| `ABS_ALLOW_TOOLS` | `--allow-tools` | load these tools as well as the toolsets, or only these when no toolset is named |
| `ABS_DENY_TOOLS` | `--deny-tools` | never load these tools, whatever asked for them |
| `ABS_PROVIDERS` | `--providers` | metadata providers to ask, in order, when a call names none |
| `ABS_PROVIDER_TAG` | `--provider-tag` | start of the tag that records which store a match came from |
| `ABS_AUDIT_SKIP` | `--audit-skip` | audit rules to leave out |
| `ABS_LOG` | | log level; `WARN` by default |
| `ABS_LISTEN` | `--listen` | serve MCP over HTTP on this address, such as `:8080`, instead of stdio |
| `ABS_AUTH_TOKEN` | `--auth-token` | the bearer token the HTTP endpoint requires |
| `ABS_ALLOW_NO_AUTH` | `--allow-no-auth` | serve HTTP with no bearer token at all |

## Notes on some settings

- **`ABS_TOOLSETS`** takes `all`, `core`, `curation`, `listening`, `podcasts`, `organise`, `admin`, or a resource family like `item`. `core` is always included.
- **`ABS_ALLOW_TOOLS`** takes names, globs such as `library_*`, or `essential`.
- **`ABS_DENY_TOOLS`** takes names, or globs such as `*_delete`.
- **`ABS_PROVIDERS`** should name the store the books were bought from first: `audible.ca,audible`. The default is the library's own provider.
- **`ABS_PROVIDER_TAG`** is `zz-provider:` by default, so the tag sorts last in the tag list. `off` writes and reads none.
- **`ABS_AUDIT_SKIP`**: the audits hold a library to one way of keeping it, and every rule is on by default. A rule that is a way of filing, not a mistake, can be switched off. Today that is `path-series`: in `audit_path`, a book the record places in a series, in a folder that does not say so.
- **`ABS_LOG`**: `INFO` adds a line for each call of a tool that changes or deletes something. Passwords, keys, and the sign-in and query values of any address are blanked. `DEBUG` and `TRACE` say more.
- **`ABS_AUTH_TOKEN`** is required with `--listen`.
- **`ABS_ALLOW_NO_AUTH`** means anyone who can reach the port can use every tool.

## What the key can do

An API key acts as exactly one Audiobookshelf user and has that user's permissions. A key for a normal account cannot see libraries that account cannot see, and cannot scan, match or delete. Most write tools need an admin account. `server_info` reports what the key can do.

## Configuration file

Put a `.abs-mcp` file in your home folder (`~/.abs-mcp`) for global settings, and one in the current folder (`./.abs-mcp`) for a project's own. Both are read, the project's over the home one, so a project file needs only the settings it changes.

Keys are the environment variable names, with or without the `ABS_` prefix, in `env` format:

```env
SERVER=http://nas:13378
TOKEN=ey...
READ_ONLY=true
```

A flag or an environment variable beats either file.

## Registering with a client

Check the connection first:

```bash
abs-mcp info
```

In `.mcp.json`:

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

## Running as a service over HTTP

`serve --listen :8080` serves the MCP Streamable HTTP transport at `/mcp`, with `GET /healthz` beside it, instead of stdio.

- `ABS_AUTH_TOKEN` is required. Clients must send `Authorization: Bearer <token>`.
- The server refuses to start without one, unless `ABS_ALLOW_NO_AUTH=true` says that anyone who can reach the port may use every tool.

Register it from any machine:

```bash
claude mcp add --transport http audiobookshelf http://nas:8080/mcp \
  --header "Authorization: Bearer $ABS_AUTH_TOKEN"
```

## Docker

Releases publish an image for amd64 and arm64 to `ghcr.io/katbyte/abs-mcp`, tagged `vX.Y.Z`, `vX.Y` and `latest`.

`docker-compose.yml` is the default always-on deployment. It runs that image and reads secrets from a gitignored `.env`. Adjust `ABS_SERVER` and `TZ` in the compose file, then:

```bash
cp .env.example .env      # fill in ABS_TOKEN and ABS_AUTH_TOKEN
docker compose up -d
```

- `make docker` builds the same image from source, tagged `abs-mcp`, with version info from git.
- The image is alpine-based, so `docker exec -it abs-mcp sh` works.
- It runs as a non-root user and has a healthcheck against `/healthz`.
- The binary is the entrypoint. So `docker run --rm ghcr.io/katbyte/abs-mcp info` works as a connection check, with the `ABS_*` variables passed by `-e`.
