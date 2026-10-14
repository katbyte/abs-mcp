// Command pandorest is this repository's part of github.com/katbyte/pandorest. For now it holds spec, which writes the Audiobookshelf API
// document sdk/abs is to be generated from, read out of the server's own source, because Audiobookshelf publishes no complete one.
//
// It is run from the repository root, as make spec does:
//
//	go run ./sdk/pandorest spec ../audiobookshelf      # an Audiobookshelf checkout -> api-defs/audiobookshelf-openapi-<version>.json
//	go run ./sdk/pandorest spec -v ../audiobookshelf   # also list every operation and how much of its answer is typed
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "spec" {
		if err := spec(os.Args[2:], "api-defs", os.Stdout); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, "spec:", err)
			os.Exit(1)
		}

		return
	}

	_, _ = fmt.Fprintln(os.Stderr, "usage: go run ./sdk/pandorest spec [-v] <audiobookshelf checkout>")
	os.Exit(2)
}
