// Package cli implements the abs-mcp command line interface: the cobra commands, flag and
// config handling, and the MCP server exposing Audiobookshelf library tools to AI clients.
package cli

import (
	"errors"
	"fmt"

	"github.com/katbyte/abs-mcp/lib/version"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

func ValidateParams(params []string) func(cmd *cobra.Command, args []string) error {
	return func(_ *cobra.Command, _ []string) error {
		for _, p := range params {
			if viper.GetString(p) != "" {
				continue
			}
			return errors.New(p + " parameter can't be empty")
		}

		return nil
	}
}

// connectionParams are the flags every command that talks to the media server needs.
var connectionParams = []string{"server", "token"}

func Make() (*cobra.Command, error) {
	root := &cobra.Command{
		Use:   "abs-mcp [command]",
		Short: "abs-mcp is an MCP server and CLI for curating an Audiobookshelf library",
		Long: `An MCP server (and CLI) for curating an Audiobookshelf library: search, inspect,
audit and fix metadata, and manage listening progress, collections, playlists and podcasts
from an AI client such as Claude Code.
Complete documentation is available at https://github.com/katbyte/abs-mcp`,
		RunE: func(_ *cobra.Command, _ []string) error {
			fmt.Println("Run \"abs-mcp help\" for more information about available abs-mcp commands.")
			return nil
		},
	}

	root.AddCommand(&cobra.Command{
		Use:           "version",
		Short:         "Print the version number of abs-mcp",
		Long:          `Print the version number of abs-mcp`,
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		Run: func(_ *cobra.Command, _ []string) {
			fmt.Println("abs-mcp " + version.Version)
		},
	})

	root.AddCommand(&cobra.Command{
		Use:           "info",
		Short:         "Check connectivity and print server info",
		Long:          `Connects to the configured Audiobookshelf server and prints its version, the API key's user, and the libraries visible to it.`,
		Args:          cobra.NoArgs,
		PreRunE:       ValidateParams(connectionParams),
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true

			client, err := GetFlags().NewClient()
			if err != nil {
				return err
			}

			status, err := client.Status(cmd.Context())
			if err != nil {
				return err
			}

			me, err := client.Me(cmd.Context())
			if err != nil {
				return err
			}

			libs, err := client.Libraries(cmd.Context())
			if err != nil {
				return err
			}

			fmt.Printf("audiobookshelf %s at %s — user %s (%s)\n", status.ServerVersion, client.BaseURL(), me.Username, me.Type)
			for _, l := range libs {
				fmt.Printf("  library %q (%s) id %s\n", l.Name, l.MediaType, l.ID)
			}
			return nil
		},
	})

	root.AddCommand(serveCmd())

	if err := configureFlags(root); err != nil {
		return nil, fmt.Errorf("unable to configure flags: %w", err)
	}

	return root, nil
}
