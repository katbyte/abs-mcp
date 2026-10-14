# abs-mcp

[![GitHub release](https://img.shields.io/github/v/release/katbyte/abs-mcp?color=blueviolet)](https://github.com/katbyte/abs-mcp/releases/latest)
[![Go Version](https://img.shields.io/github/go-mod/go-version/katbyte/abs-mcp?color=00ADD8)](https://github.com/katbyte/abs-mcp/blob/main/go.mod)
[![License](https://img.shields.io/github/license/katbyte/abs-mcp?color=blue)](https://github.com/katbyte/abs-mcp/blob/main/LICENSE)
![build](https://github.com/katbyte/abs-mcp/actions/workflows/build.yaml/badge.svg)
![tests](https://github.com/katbyte/abs-mcp/actions/workflows/pr-tests.yaml/badge.svg)
![lint](https://github.com/katbyte/abs-mcp/actions/workflows/pr-golangci-lint.yaml/badge.svg)
[![coverage](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/katbyte/abs-mcp/badges/coverage.json)](https://github.com/katbyte/abs-mcp/actions/workflows/coverage.yaml)

An [MCP](https://modelcontextprotocol.io) server that finds what is wrong with an [Audiobookshelf](https://www.audiobookshelf.org) library and helps fix it. It is also a command-line tool and a Go client.

**Detection is code, correction is judgment.** The server runs the checks over the whole library and returns short worklists. You and your client decide what to change.

## Install

```bash
go install github.com/katbyte/abs-mcp@latest
```

Needs Audiobookshelf 2.26 or newer.

## Quick start

Make an API key in Audiobookshelf under Settings → Users → API Keys. Then register the server with Claude Code:

```bash
claude mcp add audiobookshelf -e ABS_SERVER=http://nas:13378 -e ABS_TOKEN=... -e ABS_TOOLSETS=curation -- abs-mcp serve
```

Ask the client to run `audit_all`. It shows where the library needs work.

Without `ABS_TOOLSETS` only five read-only tools load. `curation` adds the audits and the tools that fix what they find.

## The audits

Each audit sweeps the whole library for one kind of mistake. Each name links to its full rules.

| audit | what it catches |
|---|---|
| [`audit_all`](docs/audits.md#audit_all) | Every audit in one call, counts only. Start here. |
| [`audit_unmatched`](docs/audits.md#audit_unmatched) | Books never matched to a store record. Fix with `item_match`. |
| [`audit_matched`](docs/audits.md#audit_matched) | Books matched to a recording other than the one on disk. Fix with `item_match`. |
| [`audit_missing`](docs/audits.md#audit_missing) | An empty field: cover, description, narrator, series and more. Fix with `item_edit`. |
| [`audit_issues`](docs/audits.md#audit_issues) | Records whose folder is gone or holds nothing playable. Fix with `library_issues_merge` or `library_issues_remove`. |
| [`audit_no_audio`](docs/audits.md#audit_no_audio) | Books with no audio at all, usually an ebook in an audiobook library. |
| [`audit_unplayable`](docs/audits.md#audit_unplayable) | Audio that will not play: locked to a store's player, unreadable or cut short. |
| [`audit_path`](docs/audits.md#audit_path) | A folder name that disagrees with the title, author, year or series. Fix with `item_edit`. |
| [`audit_chapters`](docs/audits.md#audit_chapters) | Chapters out of order, past the end of the audio, or one chapter over a long book. Fix with `item_chapters_set`. |
| [`audit_duplicates`](docs/audits.md#audit_duplicates) | The same book held more than once, and incomplete copies. Confirm with `item_compare_audio`. |
| [`audit_abridged`](docs/audits.md#audit_abridged) | Books that are probably abridged but not marked so. Fix with `item_edit`. |
| [`audit_series`](docs/audits.md#audit_series) | Gaps, one series spelled two ways, wrong numbers and bad names. Fix with `series_merge` or `series_edit`. |
| [`audit_authors`](docs/audits.md#audit_authors) | Authors that are unmatched, empty, duplicated or wrongly matched. Fix with `author_match` or `author_edit`. |
| [`audit_narrators`](docs/audits.md#audit_narrators) | Author and narrator swapped, and misspelt narrator names. Fix with `item_edit` or `metadata_rename`. |
| [`audit_genres`](docs/audits.md#audit_genres) | Genres and tags that are placeholders, repeats or too rarely used. Fix with `metadata_rename`. |
| [`audit_spelling`](docs/audits.md#audit_spelling) | One value spelled several ways in genres, tags, languages and publishers. Fix with `metadata_rename`. |
| [`audit_whitespace`](docs/audits.md#audit_whitespace) | Stray spaces in titles, names, folders and files. Fix with `item_edit`, `author_edit` or `series_edit`. |
| [`audit_covers`](docs/audits.md#audit_covers) | Covers that are missing, small, not square or carry a store ribbon. Fix with `item_cover_edit` or `item_cover_upgrade`. |
| [`audit_unembedded`](docs/audits.md#audit_unembedded) | Audio files whose tags do not carry the library's metadata. Fix with `item_embed_metadata`. |
| [`audit_podcasts`](docs/audits.md#audit_podcasts) | Podcasts with nothing downloaded or a feed gone quiet. Fix with `podcast_episode_download` or `podcast_check_new`. |

## What else is here

- **100 tools** cover the whole Audiobookshelf API, so what an audit finds can be fixed in the same session.
- **A Go client**, `sdk/abs`, works on its own. It covers all 202 API routes.
- **Proven on a real library.** A large collection, gathered over years, was cleaned up with these tools. Most of the audits exist because that library had the problem.

## What keeps a library safe

- `--read-only` loads only the tools that change nothing.
- The tools that delete load only with `--enable-delete`.
- A tool that erases files, removes many records, or writes a store's details over a book's first says what it would do. It changes nothing until called again with `confirm`.

## More

- [Configuration](docs/configuration.md): every setting, the config file, serving over HTTP, and Docker.
- [Tool reference](docs/tools.md): every tool, by what it acts on.
- [Choosing which tools load](docs/toolsets.md): toolsets, and adding or dropping single tools.
- [A typical curation session](docs/curation.md): the order to run things in.
- [After a folder is renamed or moved](docs/recovery.md): putting a book's two records back together.
- [The Go client](docs/sdk.md): using `sdk/abs` without the server.
- [Development and tests](docs/testing.md): building, the test suites and the fixtures.
- [Audiobookshelf API notes](docs/README.md), the [roadmap](docs/ROADMAP.md), and [how the audio comparison was tuned](docs/CALIBRATION.md).
- [Contributing](CONTRIBUTING.md): how releases are cut.

## How this is built

The code is written with Claude Code. I review it, then prune or rework it until it has the shape I want. I set the design, the scope and the audit rules, and I decide what stays.

Every tool and every client method must have a live test against a real Audiobookshelf in Docker. CI fails otherwise.

CI also runs:

- **Lint:** golangci-lint, actionlint, yamllint, shellcheck, markdownlint and a spelling check.
- **Security:** CodeQL, govulncheck, a vulnerability scan of the built binary, and an audit of the workflows.
- **Dependencies:** a check that `go.mod` and the vendored code agree.
