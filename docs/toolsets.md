# Choosing which tools load

**The default is `core`: five read-only tools, about 1,200 tokens.** Every tool is around 27,000 tokens of definitions before anyone asks a question. That is a poor way to spend a client's context by default.

`--toolsets` / `ABS_TOOLSETS` loads the groups a session needs. `core` comes along with whatever else is asked for, because nothing else can find a library or open an item.

**Curating a library needs `ABS_TOOLSETS=curation`.** That is the audits and everything that fixes what they find. `ABS_TOOLSETS=all` loads every tool.

| toolset | for | tools | ~tokens | with `--enable-delete` |
|---|---|---|---|---|
| `core` *(default)* | finding libraries and books | 5 | 1,200 | 5 / 1,200 |
| `curation` | the audits and what fixes their findings | 51 | 18,800 | 51 / 18,800 |
| `listening` | progress, bookmarks and history | 14 | 3,100 | 15 / 3,400 |
| `podcasts` | shows and episodes | 15 | 3,000 | 16 / 3,200 |
| `organise` | collections, playlists and feeds | 15 | 3,100 | 17 / 3,200 |
| `admin` | scans, backups, users and new libraries | 17 | 3,900 | 21 / 5,200 |
| `all` | everything | 92 | 27,100 | 100 / 29,000 |

Every row includes `core`.

Tokens are what the model sees: each tool's name, description and input schema, measured over a real tool listing at four bytes a token. Every tool also carries an output schema, another 39,500 tokens across `all`. Clients keep that to themselves to check results, and do not send it to the model.

## By name prefix

`--toolsets` also takes a resource family, which is every tool with that prefix: `audit`, `author`, `collection`, `feed`, `item`, `library`, `metadata`, `narrator`, `playlist`, `podcast`, `series`, `server`, `user`.

```sh
ABS_TOOLSETS=all                # every tool, which was the default before 0.2.0
ABS_TOOLSETS=curation           # audits plus everything that fixes what they find
ABS_TOOLSETS=listening,podcasts # a client that plays things rather than curates them
ABS_TOOLSETS=audit              # read-only detection, nothing that writes
ABS_TOOLSETS=core,item,series   # core plus two whole families
```

## Seeing what would load

`abs-mcp tools` prints what the current flags would load, grouped by toolset. It needs no server.

```sh
abs-mcp tools                   # the default set
abs-mcp tools --toolsets all    # every tool
abs-mcp tools --read-only -q    # names only
```

## One tool more, or a few less

- `--allow-tools` asks for tools by name, as `--toolsets` asks for them by set. A session gets what either names: "these sets, and this tool as well".
- On its own, with no toolset beside it, `--allow-tools` loads only the tools it names.
- `--deny-tools` takes tools out of whatever was asked for. That is how a toolset is narrowed.
- Both take comma-separated tool names, and globs with a leading or trailing `*`.
- `--allow-tools` also takes the `essential` preset: `library_list`, `library_search`, `library_items`, `item_get`, `user_in_progress`, `user_progress_get` and `user_progress_set`.
- Neither gets a tool past `--read-only` or the delete gate.

```sh
ABS_TOOLSETS=curation ABS_ALLOW_TOOLS=library_scan    # the curation set, and the scan as well
ABS_ALLOW_TOOLS=essential                             # the seven essential tools and nothing else
ABS_ALLOW_TOOLS=library_*,item_get,user_*             # only these
ABS_TOOLSETS=curation ABS_DENY_TOOLS=audit_*          # the curation set without its audits
ABS_DENY_TOOLS=*_delete,server_*
```

Before 0.7 an allow list beside toolsets narrowed them, so a tool the sets did not hold was silently left out.

A pattern that matches no tool stops the server at start and names it, so a typo cannot silently hide a tool.
