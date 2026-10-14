// Command pandorest generates sdk/abs with github.com/katbyte/pandorest from the Audiobookshelf API document under api-defs, which
// spec writes by reading the server's own source, because Audiobookshelf publishes no complete one.
//
// It is run from the repository root, as the make targets do:
//
//	go run ./sdk/pandorest spec ../audiobookshelf      # an Audiobookshelf checkout -> api-defs/audiobookshelf-openapi-<version>.json
//	go run ./sdk/pandorest spec -v ../audiobookshelf   # also list every operation and how much of its answer is typed
//	go run ./sdk/pandorest import                      # the document -> api-defs/audiobookshelf-<version>/
//	go run ./sdk/pandorest diff                        # what a refreshed document changes against the definitions
package main

import (
	"fmt"
	"os"

	"github.com/katbyte/pandorest"
)

func main() {
	// the document is read from the Audiobookshelf source, which pandorest has no part in (spec.go)
	if len(os.Args) > 1 && os.Args[1] == "spec" {
		if err := spec(os.Args[2:], "api-defs", os.Stdout); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, "spec:", err)
			os.Exit(1)
		}

		return
	}

	pandorest.Main(generation())
}
