# Using the Go client on its own

`sdk/abs` is a plain Go client for the Audiobookshelf API. It knows nothing about MCP. If you only want to talk to Audiobookshelf from Go, take it and ignore the rest.

```go
import "github.com/katbyte/abs-mcp/sdk/abs"

client, err := abs.New("http://nas:13378", os.Getenv("ABS_TOKEN"))
items, err := client.Items(ctx, libraryID, abs.ItemsOptions{Limit: 50})
```

## What it depends on

The standard library, and [go-kt's HTTP package](https://github.com/katbyte/go-kt/tree/main/chttp), which itself uses only the standard library. Nothing else is built into a program that imports it.

## How it behaves

- **Retries.** A read is asked for again when a gateway could not reach the server (502, 503, 504) or the connection dropped, three times in all. A write is sent once.
- **Logging.** It logs nothing unless handed a logger with `abs.WithLog`. Then it traces every request and answer, with the API key blanked. abs-mcp hands it its own, so `ABS_LOG=trace` shows the traffic.
- **Errors.** A status that is not success is a `*chttp.StatusError`. `abs.IsNotFound` and `abs.IsForbidden` read the common ones.
- **Downloads.** File downloads stream and are not held in memory, so a multi-gigabyte audiobook does not have to fit.

## What it covers

It has 214 methods covering every one of Audiobookshelf's 202 API routes: libraries, items, authors, series, narrators, collections, playlists, progress, bookmarks, podcasts, provider search, RSS feeds, tags, genres, tasks, backups, playback sessions, notifications, email, API keys, sharing, settings and user administration.

`make apicheck` reads the route table out of the Audiobookshelf source and fails if anything is missing. So the coverage claim is checked, not just stated.

## Where its shapes come from

Audiobookshelf publishes no OpenAPI spec, and its [public API docs say they are unmaintained](https://api.audiobookshelf.org). So the types here are written against the server source, as described in [README.md](README.md). They are then proved against a running server, which is the only thing that catches the server changing shape underneath you. See [testing.md](testing.md).
