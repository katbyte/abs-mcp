package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// viper is global, so none of these can run in parallel and each resets it.
const (
	testServer  = "http://nas:13378"
	testTool    = "item_get"
	testSet     = "curation"
	denyDeletes = "*_delete"
	tagOff      = "off"
)

// load drives the real flag wiring against a temporary home and working
// directory.
func load(t *testing.T, home, wd string) *FlagData {
	t.Helper()

	viper.Reset()
	t.Cleanup(viper.Reset)
	t.Setenv("HOME", home)
	t.Chdir(wd)

	if err := configureFlags(&cobra.Command{Use: "abs-mcp"}); err != nil {
		t.Fatalf("configureFlags: %v", err)
	}

	return GetFlags()
}

func write(t *testing.T, dir, body string) {
	t.Helper()

	if err := os.WriteFile(filepath.Join(dir, ".abs-mcp"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// on is how a boolean is switched on from the environment.
const on = "true"

// A .abs-mcp in the working directory is documented as per-project settings,
// which it only is if it is found before the one in $HOME. viper reads the
// first file it finds, and $HOME used to be on the search path first, so a
// project config was silently ignored for anyone who had a global one.
//
//nolint:paralleltest // viper is global state; these mutate it
func TestConfigFilePrecedence(t *testing.T) {
	home, project := t.TempDir(), t.TempDir()

	t.Run("project wins over home", func(t *testing.T) {
		write(t, home, "SERVER=http://from-home\n")
		write(t, project, "SERVER=http://from-project\n")
		if got := load(t, home, project).Server; got != "http://from-project" {
			t.Errorf("server = %q, want the project config", got)
		}
	})

	t.Run("home is used when there is no project config", func(t *testing.T) {
		empty := t.TempDir()
		if got := load(t, home, empty).Server; got != "http://from-home" {
			t.Errorf("server = %q, want the home config", got)
		}
	})

	t.Run("a project file changes what it names and keeps the rest", func(t *testing.T) {
		layered := t.TempDir()
		write(t, home, "SERVER=http://from-home\nTOKEN=from-home\n")
		write(t, layered, "TOOLSETS=curation\n")
		f := load(t, home, layered)
		if f.Server != "http://from-home" || f.Token != "from-home" {
			t.Errorf("server, token = %q, %q: the project file threw away the home one", f.Server, f.Token)
		}
		if len(f.Toolsets) != 1 || f.Toolsets[0] != testSet {
			t.Errorf("toolsets = %v, want the project's", f.Toolsets)
		}
	})

	t.Run("no config at all is not an error", func(t *testing.T) {
		if got := load(t, t.TempDir(), t.TempDir()).Server; got != "" {
			t.Errorf("server = %q, want empty", got)
		}
	})
}

// The environment beats a config file, so a container can override what is
// baked into an image.
func TestEnvironmentOverridesConfigFile(t *testing.T) {
	home := t.TempDir()
	write(t, home, "SERVER=http://from-home\nTOKEN=from-home\n")

	t.Setenv("ABS_SERVER", "http://from-env")
	f := load(t, home, t.TempDir())

	if f.Server != "http://from-env" {
		t.Errorf("server = %q, want the environment", f.Server)
	}
	// and a key the environment did not set still comes from the file
	if f.Token != "from-home" {
		t.Errorf("token = %q, want the config file", f.Token)
	}
}

// Every documented ABS_* variable has to actually reach its field; a typo in
// the binding map is invisible until someone sets the variable and nothing
// happens.
//
//nolint:paralleltest // viper is global state; these mutate it
func TestEnvironmentBindings(t *testing.T) {
	dir := t.TempDir()
	for _, env := range []struct {
		key, value string
		check      func(*FlagData) bool
	}{
		{"ABS_SERVER", testServer, func(f *FlagData) bool { return f.Server == testServer }},
		{"ABS_TOKEN", "tok", func(f *FlagData) bool { return f.Token == "tok" }},
		{"ABS_READ_ONLY", on, func(f *FlagData) bool { return f.ReadOnly }},
		{"ABS_ENABLE_DELETE", on, func(f *FlagData) bool { return f.EnableDelete }},
		{"ABS_LISTEN", ":8080", func(f *FlagData) bool { return f.Listen == ":8080" }},
		{"ABS_AUTH_TOKEN", "bearer", func(f *FlagData) bool { return f.AuthToken == "bearer" }},
		{"ABS_ALLOW_NO_AUTH", on, func(f *FlagData) bool { return f.AllowNoAuth }},
		{"ABS_TOOLSETS", testSet, func(f *FlagData) bool { return len(f.Toolsets) == 1 && f.Toolsets[0] == testSet }},
		{"ABS_ALLOW_TOOLS", testTool, func(f *FlagData) bool { return len(f.AllowTools) == 1 && f.AllowTools[0] == testTool }},
		{"ABS_DENY_TOOLS", denyDeletes, func(f *FlagData) bool { return len(f.DenyTools) == 1 && f.DenyTools[0] == denyDeletes }},
		{"ABS_PROVIDERS", "audible.ca,audible", func(f *FlagData) bool { return len(f.Providers) == 2 && f.Providers[0] == "audible.ca" }},
		{"ABS_PROVIDER_TAG", tagOff, func(f *FlagData) bool { return f.ProviderTag == tagOff }},
	} {
		t.Setenv(env.key, env.value)
		if f := load(t, dir, dir); !env.check(f) {
			t.Errorf("%s=%s did not reach its field: %+v", env.key, env.value, f)
		}
		_ = os.Unsetenv(env.key)
	}
}

// NewClient is where a missing server or token is caught, before anything
// tries to talk to Audiobookshelf.
func TestNewClientValidation(t *testing.T) {
	t.Parallel()

	if _, err := (&FlagData{}).NewClient(); err == nil {
		t.Error("a client with no server should be refused")
	}
	if _, err := (&FlagData{Server: testServer}).NewClient(); err == nil {
		t.Error("a client with no token should be refused")
	}
	if _, err := (&FlagData{Server: "nas", Token: "t"}).NewClient(); err == nil {
		t.Error("a server url with no scheme should be refused")
	}
	if _, err := (&FlagData{Server: testServer, Token: "t"}).NewClient(); err != nil {
		t.Errorf("a complete configuration was refused: %v", err)
	}
}

// A config file spells a setting the way the environment does, with or
// without the prefix. viper matched only the flag's own spelling, so every
// two-word setting in a file - READ_ONLY, DENY_TOOLS - was ignored without a
// word, and a file asking for read-only registered every write tool.
func TestConfigFileTwoWordSettings(t *testing.T) {
	home := t.TempDir()
	write(t, home, "SERVER=http://from-home\nREAD_ONLY=true\nABS_ENABLE_DELETE=true\nDENY_TOOLS=*_delete\nPROVIDER_TAG=off\nALLOW_NO_AUTH=true\n")

	f := load(t, home, t.TempDir())
	if !f.ReadOnly || !f.EnableDelete || !f.AllowNoAuth {
		t.Errorf("read-only, enable-delete, allow-no-auth = %v, %v, %v: the file's two-word settings were ignored", f.ReadOnly, f.EnableDelete, f.AllowNoAuth)
	}
	if len(f.DenyTools) != 1 || f.DenyTools[0] != denyDeletes || f.ProviderTag != tagOff {
		t.Errorf("deny-tools, provider-tag = %v, %q", f.DenyTools, f.ProviderTag)
	}

	// and the environment still beats the file
	t.Setenv("ABS_PROVIDER_TAG", "zz-store:")
	if f := load(t, home, t.TempDir()); f.ProviderTag != "zz-store:" {
		t.Errorf("provider-tag = %q, want the environment over the file", f.ProviderTag)
	}
}
