// Package main implements abs-mcp, an MCP server and CLI for curating an Audiobookshelf library.
package main

import (
	"os"

	c "github.com/gookit/color"
	"github.com/katbyte/abs-mcp/cli"
	"github.com/katbyte/go-kt/clog"
)

func main() {
	// the log level comes from ABS_LOG; read it once here, before anything logs
	clog.SetLevelFromEnv("ABS_LOG")

	cmd, err := cli.Make()
	if err != nil {
		clog.Log.Error(c.Sprintf("<red>abs-mcp: building cmd</> %v", err))

		os.Exit(1)
	}

	if err := cmd.Execute(); err != nil {
		clog.Log.Error(c.Sprintf("<red>abs-mcp:</> %v", err))

		os.Exit(1)
	}

	os.Exit(0)
}
