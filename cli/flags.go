package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/katbyte/abs-mcp/lib/abs"
	"github.com/katbyte/abs-mcp/tools"
	"github.com/katbyte/go-kt/clog"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

type FlagData struct {
	Server       string   `mapstructure:"server"`
	Token        string   `mapstructure:"token"`
	ReadOnly     bool     `mapstructure:"read-only"`
	EnableDelete bool     `mapstructure:"enable-delete"`
	Toolsets     []string `mapstructure:"toolsets"`
	AllowTools   []string `mapstructure:"allow-tools"`
	DenyTools    []string `mapstructure:"deny-tools"`
	Listen       string   `mapstructure:"listen"`
	AuthToken    string   `mapstructure:"auth-token"`
	AllowNoAuth  bool     `mapstructure:"allow-no-auth"`
	ProviderTag  string   `mapstructure:"provider-tag"`
	Providers    []string `mapstructure:"providers"`
}

func configureFlags(root *cobra.Command) error {
	pflags := root.PersistentFlags()

	pflags.StringP("server", "s", "", "the Audiobookshelf server url, e.g. http://nas:13378")
	pflags.StringP("token", "t", "", "an Audiobookshelf API key (consider exporting to ABS_TOKEN instead)")
	pflags.Bool("read-only", false, "register only tools that never change server state")
	pflags.Bool("enable-delete", false, "register the tools that delete items, episodes, authors, collections and playlists")
	pflags.StringSlice("toolsets", nil, "groups of tools to register: all, core (default), curation, listening, podcasts, organise, admin, or a resource family like item (core is always included)")
	pflags.StringSlice("allow-tools", nil, "only register these tools: names, prefix globs like library_*, or the essential preset")
	pflags.StringSlice("deny-tools", nil, "never register these tools: names or prefix globs like *_delete")
	pflags.String("listen", "", "serve MCP over HTTP on this address (e.g. :8080) instead of stdio")
	pflags.String("auth-token", "", "bearer token required on the HTTP endpoint (consider exporting to ABS_AUTH_TOKEN instead)")
	pflags.Bool("allow-no-auth", false, "serve HTTP with no bearer token: anyone who can reach the port can use every tool")
	pflags.StringSlice("providers", nil, "metadata providers to ask in order when a call names none, the store the books were bought from first: audible.ca,audible (default: the library's own provider)")
	pflags.String("provider-tag", "zz-provider:", "prefix of the tag that records which store a match came from (zz-provider:audible.ca, sorted last in the tag list); off writes none")

	// binding map for viper/pflag -> env
	m := map[string]string{ //nolint:gosec // G101: these are env var names, not credentials
		"server":        "ABS_SERVER",
		"token":         "ABS_TOKEN",
		"read-only":     "ABS_READ_ONLY",
		"enable-delete": "ABS_ENABLE_DELETE",
		"toolsets":      "ABS_TOOLSETS",
		"allow-tools":   "ABS_ALLOW_TOOLS",
		"deny-tools":    "ABS_DENY_TOOLS",
		"listen":        "ABS_LISTEN",
		"auth-token":    "ABS_AUTH_TOKEN",
		"allow-no-auth": "ABS_ALLOW_NO_AUTH",
		"provider-tag":  "ABS_PROVIDER_TAG",
		"providers":     "ABS_PROVIDERS",
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

	readConfigFiles()

	// a config file spells a setting the way the environment does, with or
	// without the prefix - READ_ONLY or ABS_READ_ONLY for --read-only - and
	// viper only matches a key spelled as the flag is, so every two-word
	// setting was ignored. Carried across as defaults, they still lose to a
	// flag or the environment.
	for name, env := range m {
		for _, alt := range []string{strings.ReplaceAll(name, "-", "_"), strings.ToLower(env)} {
			if alt != name && viper.InConfig(alt) {
				viper.SetDefault(name, viper.Get(alt))
				break
			}
		}
	}

	return nil
}

// readConfigFiles reads ~/.abs-mcp and then ./.abs-mcp over it, key by key:
// the home file holds the global settings and a project's file changes the
// ones it names. viper on its own reads only the first file it finds, so a
// project file that set one thing lost the server and key from home.
func readConfigFiles() {
	viper.SetConfigType("env")

	var files []string
	if home, err := os.UserHomeDir(); err == nil {
		files = append(files, filepath.Join(home, ".abs-mcp"))
	}
	if wd, err := os.Getwd(); err == nil && (len(files) == 0 || filepath.Join(wd, ".abs-mcp") != files[0]) {
		files = append(files, filepath.Join(wd, ".abs-mcp"))
	}

	read := false
	for _, f := range files {
		if _, err := os.Stat(f); err != nil {
			continue
		}
		viper.SetConfigFile(f)
		var err error
		if read {
			err = viper.MergeInConfig()
		} else {
			err = viper.ReadInConfig()
		}
		if err != nil {
			clog.Log.Errorf("Error reading config file %s: %v", f, err)
			continue
		}
		read = true
	}
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

// DefaultToolsets is what the binary registers when --toolsets is not given:
// enough to find things and read them, and nothing that writes. The whole
// surface is around 12,000 tokens of tool definitions before a question is
// asked, which is a poor thing to spend a client's context on by default. Ask for more with
// --toolsets, or --toolsets all for everything.
var DefaultToolsets = []string{"core"}

// ToolOptions maps the flags onto the tool registration options.
func (f *FlagData) ToolOptions() tools.Options {
	sets := f.Toolsets
	if len(sets) == 0 {
		sets = DefaultToolsets
	}

	return tools.Options{
		ReadOnly:     f.ReadOnly,
		EnableDelete: f.EnableDelete,
		Toolsets:     sets,
		Allow:        f.AllowTools,
		Deny:         f.DenyTools,
		ProviderTag:  f.ProviderTag,
		Providers:    f.Providers,
	}
}
