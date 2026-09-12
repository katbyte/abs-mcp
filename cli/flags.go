package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/katbyte/abs-mcp/lib/abs"
	"github.com/katbyte/abs-mcp/lib/clog"
	"github.com/katbyte/abs-mcp/tools"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

type FlagData struct {
	Server       string   `mapstructure:"server"`
	Token        string   `mapstructure:"token"`
	ReadOnly     bool     `mapstructure:"read-only"`
	EnableDelete bool     `mapstructure:"enable-delete"`
	AllowTools   []string `mapstructure:"allow-tools"`
	DenyTools    []string `mapstructure:"deny-tools"`
	Listen       string   `mapstructure:"listen"`
	AuthToken    string   `mapstructure:"auth-token"`
}

func configureFlags(root *cobra.Command) error {
	pflags := root.PersistentFlags()

	pflags.StringP("server", "s", "", "the Audiobookshelf server url, e.g. http://nas:13378")
	pflags.StringP("token", "t", "", "an Audiobookshelf API key (consider exporting to ABS_TOKEN instead)")
	pflags.Bool("read-only", false, "register only tools that never change server state")
	pflags.Bool("enable-delete", false, "register the tools that delete library items, episodes and authors")
	pflags.StringSlice("allow-tools", nil, "only register these tools: names, prefix globs like library_*, or the essential preset")
	pflags.StringSlice("deny-tools", nil, "never register these tools: names or prefix globs like *_delete")
	pflags.String("listen", "", "serve MCP over HTTP on this address (e.g. :8080) instead of stdio")
	pflags.String("auth-token", "", "bearer token required on the HTTP endpoint (consider exporting to ABS_AUTH_TOKEN instead)")

	// binding map for viper/pflag -> env
	m := map[string]string{ //nolint:gosec // G101: these are env var names, not credentials
		"server":        "ABS_SERVER",
		"token":         "ABS_TOKEN",
		"read-only":     "ABS_READ_ONLY",
		"enable-delete": "ABS_ENABLE_DELETE",
		"allow-tools":   "ABS_ALLOW_TOOLS",
		"deny-tools":    "ABS_DENY_TOOLS",
		"listen":        "ABS_LISTEN",
		"auth-token":    "ABS_AUTH_TOKEN",
	}

	for name, env := range m {
		if err := viper.BindPFlag(name, pflags.Lookup(name)); err != nil {
			return fmt.Errorf("error binding '%s' flag: %w", name, err)
		}

		if env != "" {
			if err := viper.BindEnv(name, env); err != nil {
				return fmt.Errorf("error binding '%s' to env '%s' : %w", name, env, err)
			}
		}
	}

	viper.SetConfigName(".abs-mcp")
	viper.SetConfigType("env")
	// viper reads the first file it finds, so the working directory comes
	// first: a per-project .abs-mcp overrides the one in $HOME
	viper.AddConfigPath(".")
	if home, err := os.UserHomeDir(); err == nil {
		viper.AddConfigPath(home)
	}

	if err := viper.ReadInConfig(); err != nil {
		if _, ok := errors.AsType[viper.ConfigFileNotFoundError](err); !ok {
			clog.Log.Errorf("Error reading config file: %v", err)
		}
	}

	return nil
}

// GetFlags returns the fully populated FlagData.
// We must unmarshal from Viper instead of using globally bound pflags variables
// because pflags only parses command-line arguments. Viper merges environment
// variables (and config files) on top of the CLI flags.
func GetFlags() *FlagData {
	var f FlagData
	if err := viper.Unmarshal(&f); err != nil {
		clog.Log.Fatalf("failed to unmarshal configuration: %v", err)
	}

	return &f
}

func (f *FlagData) NewClient() (*abs.Client, error) {
	return abs.New(f.Server, f.Token)
}

// ToolOptions maps the flags onto the tool registration options.
func (f *FlagData) ToolOptions() tools.Options {
	return tools.Options{
		ReadOnly:     f.ReadOnly,
		EnableDelete: f.EnableDelete,
		Allow:        f.AllowTools,
		Deny:         f.DenyTools,
	}
}
