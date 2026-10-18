# Development and tests

```bash
make            # format and build
make test       # unit tests, no server needed
make testacc    # both live suites, each in a throwaway container, torn down after
make cover      # all three suites, merged into one coverage figure
make check-all  # build, unit tests, both live suites and every linter
```

The live suites need docker, ffmpeg and jq.

## Unit tests

`make test` needs nothing outside the repo and is fast. It covers:

- **The pure logic.** Filter encoding, formatting, gap arithmetic, the audit rules and tool registration.
- **The client and the API document.** pandorest generates a test for every operation of `sdk/abs`, against a canned server built from the document, and the scanner that reads the document out of the server source has tests of its own in `sdk/pandorest`. What proves the document right is the live suite.
- **The tools against a canned Audiobookshelf.** They run end to end over an in-memory MCP session, in `tools/fake_abs_test.go`. This is where the cases the live fixtures cannot reach are kept: a library with covers, inconsistent spellings, tagged and untagged audio files, more findings than the limit.

## Live suites

Everything else runs against a real Audiobookshelf in Docker, because a canned server can only confirm what you already believed. There are two suites, each in its own container.

| | Covers | Command |
|---|---|---|
| `acceptance/abs/` | the client `sdk/abs`: every read by the read sweep, and every call that changes something, read back to see it took | `make testacc-sdk` |
| `acceptance/tools/` | the tools: name lookup, trimmed answers, audits, provider flows, journeys, and a test of the built binary | `make testacc-tools` |

With `ABS_SERVER` and `ABS_TOKEN` unset the suites skip, so they never fail for want of Docker.

## Nothing ships untested

All 100 tools and every operation of the API document are run. Both are enforced, not just claimed:

- The tool suite records every tool it calls, and fails if the server registered one that nothing called.
- The client suite records every call it makes, and a full run fails if an operation of the document was never called.

So neither a new tool nor a route the document gains can ship untested.

Almost every call checks a result. A few only check that the call reached the server, because they need things a throwaway container has not got: sending an ebook by email, firing a notification, closing a device session, unlinking OpenID, syncing an offline session, deleting an author's image, and uploading or putting back a backup. Each says so where it is written.

Each test file is named for the code it tests, `tools/items_test.go` for `tools/items.go`. Each package makes its canned servers in one file.

## Journeys

The journeys, `acceptance/tools/journey_*_test.go`, chain the tools the way a session does. They read the server back after every write, because a 200 from Audiobookshelf is not proof. They cover:

- edits made while a scan runs
- every fixable audit fixed, audited again and put back
- a non-admin's playback reaching every listening tool
- `audit_all` equal to each audit
- every read-only lookup leaving the server byte-for-byte unchanged
- accounts limited to one library or one tag
- a library's whole life
- writes done twice
- every record got by id and by name
- a podcast's whole life, over a feed the suite serves itself through the provider proxy
- metadata embedded, the record thrown away and the book scanned back from its file alone
- a book deleted out from under collections, playlists, progress, bookmarks and someone's history
- one turn's calls made at once on the same records
- an account without rights calling every tool that changes the server
- a match decided field by field, one asin on two books, and every page of every list read exactly once
- books deleted with their files, a folder renamed under its book, a long book chaptered, and issues removed one library at a time
- an explicit book, a tag-limited listener's queue and a show holding one episode twice

They found some thirty tool bugs the per-tool tests had not. The server behaviour behind them is listed in [README.md](README.md).

## The built binary

The journeys, like everything else, drive `tools.RegisterAll` in process. That is every line of tool code the binary runs. What they never touch is the thin layer around it, where a breakage is silent. MCP over stdio uses stdout for the protocol, so one stray print or log line corrupts the stream. A client then fails to connect, with the whole suite still green.

So `acceptance/tools/binary_test.go` builds the real binary and speaks to it the way a client does:

- `serve` over stdio at the most verbose log level, with every line it writes to stdout checked for being a protocol message and nothing else
- the flags, the environment and a `.abs-mcp` in the working directory, each changing what a client lists
- `--listen` serving `/mcp` behind the bearer check, with `/healthz` open beside it, and shutting down on SIGTERM
- a start that cannot work failing with a message that names the problem, not hanging: no server, no token, `--listen` with no auth token, a toolset that does not exist

## Coverage

`make cover` runs each of the three suites into its own coverage directory and merges them with `go tool covdata`. That is standard Go tooling, with no third-party merger. The merged figure is what the badge reports.

A unit-only figure is lower, because some code runs only against a real server.

## Outside services

Calls out to Audible, Audnexus and iTunes go through go-kt's record and replay proxy, `test/replayproxy`. So neither live suite needs a network.

```bash
make record         # re-record every cassette against the real providers
make record-check   # check the cassettes still match, without rewriting them
```

`record-check` compares the shape of live responses against the recordings: renamed fields, vanished fields, changed types. It ignores values. So it goes red when a provider changes its contract, not when a chart position moves.

## The read sweep

`TestReadSweep` calls every read of the API document through the generated client, against the suite's server. It is [pandorest's sweep](https://github.com/katbyte/pandorest/tree/main/sweep): what is here is the fixtures it reads by id, made at the start and removed after, and a case for each read that cannot simply answer, saying why: a read that writes, such as restoring a backup, is skipped, and one the fixtures hold nothing for, such as an ebook, is expected to be refused. A read added to the document is swept on the next run with nothing to write, and a case whose read starts answering fails, so a stale one is noticed.

It is strict: an answer carrying a field the document does not declare fails it, as does a value outside a field's set of values. `TestShelf` does the same inside a library's shelves, which hold records of several kinds the sweep does not look into.

## Saving what Audiobookshelf answers

The live suites can save every request they send Audiobookshelf and every answer it gives, for the [API document](sdk.md#an-api-document-read-from-the-source). `make spec` then types the document from what the server really sent, rather than only from what its source suggests.

```bash
ABS_TEST_ANSWERS=1 make testacc
```

The answers go in `api-defs/audiobookshelf-<version>/answers/`, one file per operation of the document, and are committed. Tokens, keys and passwords are blanked first. A file keeps its first answer whole, and from each later one only what is new, with the field that says which kind of record it is, so a second run against the same server changes nothing. A request that matches no operation of the document is listed at the end of the run, as a route the document lacks.

## Audio comparison

`item_compare_audio` has a fourth kind of test. Its thresholds are read off real speech: LibriVox's public-domain readings of one book by six different readers, beside copies of one reading re-encoded, resampled, cut and split.

The corpus is fetched into a cache outside the repo, and nothing of it is redistributed. [CALIBRATION.md](CALIBRATION.md) has the licence, the pairs and the numbers.

## Fixtures

Fixtures are generated, never committed.

- `.scripts/abs-testenv.sh` writes one-second silent files with `ffmpeg` under `~/.cache/abs-mcp`. Set `ABS_TEST_DATA` to move them. Do not use `$TMPDIR`, which Docker Desktop does not share.
- The suite creates the libraries through `library_create`, fills them with `library_scan` and sets the metadata with `item_edit`. So building the fixtures is itself part of the coverage.
- Three libraries are clean, so the audits have something to leave alone.
- The fourth, `Messy`, is seeded with every defect the audits exist to find: a series spelled two ways, a gap whose missing book sits unlinked in a series folder, a narrator's name misspelt on half the books, a folder naming another book, a duplicate, a cover wearing the ribbon. Each audit has a test against it.
- `.scripts/abs-testenv.sh fixtures` writes just the audio tree, if you want to look at the layout.

## The API reference

The Audiobookshelf API reference is the server source, not the public docs. See [README.md](README.md).
