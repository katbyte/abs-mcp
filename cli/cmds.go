// Package cli implements the abs-mcp command line interface: the cobra commands, flag and
// config handling, and the MCP server exposing Audiobookshelf library tools to AI clients.
package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/katbyte/abs-mcp/lib/version"
	"github.com/katbyte/abs-mcp/tools"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// firstSentence trims a tool description to its opening sentence, so the list
// stays one line per tool.
func firstSentence(s string) string {
	if i := strings.Index(s, ". "); i > 0 {
		return s[:i+1]
	}

	return s
}

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
		Run: func(cmd *cobra.Command, _ []string) {
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "abs-mcp "+version.Version)
		},
	})

	root.AddCommand(&cobra.Command{
		Use:   "tools",
		Short: "List the tools and toolsets, and what the current flags would register",
		Long: `Lists every tool abs-mcp would register with the current flags, grouped by toolset,
with its kind (read, write or delete) and what it does.

Needs no server: it reports what would be registered, not what a server accepts.

  abs-mcp tools                        # the default set (core)
  abs-mcp tools --toolsets all         # every tool
  abs-mcp tools --toolsets curation    # audits and the tools that fix what they find
  abs-mcp tools --read-only            # only the tools that never change state
  abs-mcp tools -q                     # names only`,
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true
			out := cmd.OutOrStdout()
			quiet, _ := cmd.Flags().GetBool("quiet")

			f := GetFlags()
			list, err := tools.Describe(f.ToolOptions())
			if err != nil {
				return err
			}

			if quiet {
				for _, t := range list {
					_, _ = fmt.Fprintln(out, t.Name)
				}
				return nil
			}

			byset := map[string][]tools.ToolInfo{}
			for _, t := range list {
				byset[t.Toolset] = append(byset[t.Toolset], t)
			}
			order := []string{"core", "curation", "listening", "podcasts", "organise", "admin"}
			counts := map[string]int{}
			for _, t := range list {
				counts[t.Kind]++
			}

			for _, set := range order {
				in := byset[set]
				if len(in) == 0 {
					continue
				}
				_, _ = fmt.Fprintf(out, "\n%s (%d)\n", set, len(in))
				for _, t := range in {
					_, _ = fmt.Fprintf(out, "  %-26s %-6s %s\n", t.Name, t.Kind, firstSentence(t.Description))
				}
			}
			_, _ = fmt.Fprintf(out, "\n%d tools: %d read, %d write, %d delete\n",
				len(list), counts["read"], counts["write"], counts["delete"])
			if !f.EnableDelete {
				_, _ = fmt.Fprintln(out, "delete tools are hidden; --enable-delete registers them")
			}
			_, _ = fmt.Fprintf(out, "\ntoolsets: all, %s\n", strings.Join(tools.ToolsetNames(), ", "))
			_, _ = fmt.Fprintf(out, "families: %s\n", strings.Join(tools.FamilyNames(), ", "))
			_, _ = fmt.Fprintf(out, "select with --toolsets / ABS_TOOLSETS; core is always included. "+
				"Default is %s - use --toolsets all for every tool.\n", strings.Join(DefaultToolsets, ","))

			return nil
		},
	})
	if c, _, err := root.Find([]string{"tools"}); err == nil {
		c.Flags().BoolP("quiet", "q", false, "print tool names only")
	}

	root.AddCommand(&cobra.Command{
		Use:           "info",
		Short:         "Check connectivity and print server info",
		Long:          `Connects to the configured Audiobookshelf server and prints its version, the API key's user, and the libraries visible to it.`,
		Args:          cobra.NoArgs,
		PreRunE:       ValidateParams(connectionParams),
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true
			out := cmd.OutOrStdout()

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

			_, _ = fmt.Fprintf(out, "audiobookshelf %s at %s — user %s (%s)\n", status.ServerVersion, client.BaseURL(), me.Username, me.Type)
			for _, l := range libs {
				_, _ = fmt.Fprintf(out, "  library %q (%s) id %s\n", l.Name, l.MediaType, l.ID)
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
