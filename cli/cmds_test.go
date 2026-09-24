package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/viper"
)

// viper is global, so none of these can run in parallel.
const coreTool = "item_get"

// run executes the command tree the binary builds and returns what it printed.
func run(t *testing.T, args ...string) (string, error) {
	t.Helper()

	viper.Reset()
	t.Cleanup(viper.Reset)

	root, err := Make()
	if err != nil {
		t.Fatalf("Make: %v", err)
	}
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)
	err = root.Execute()

	return out.String(), err
}

//nolint:paralleltest // viper is global state; Make mutates it
func TestMakeBuildsTheCommands(t *testing.T) {
	out, err := run(t, "--help")
	if err != nil {
		t.Fatalf("--help: %v", err)
	}
	for _, want := range []string{"serve", "info", "version", "tools"} {
		if !strings.Contains(out, want) {
			t.Errorf("%q is missing from --help:\n%s", want, out)
		}
	}
}

//nolint:paralleltest // viper is global state; Make mutates it
func TestVersionCommand(t *testing.T) {
	if _, err := run(t, "version"); err != nil {
		t.Errorf("version: %v", err)
	}
}

// `abs-mcp tools` needs no server, so it has to work with nothing configured -
// which is also what makes it the one command a test can drive end to end.
//
//nolint:paralleltest // viper is global state; Make mutates it
func TestToolsCommand(t *testing.T) {
	out, err := run(t, "tools")
	if err != nil {
		t.Fatalf("tools: %v", err)
	}

	// the default is core, so those are listed and nothing else is
	for _, want := range []string{"core", "server_info", "library_search", coreTool} {
		if !strings.Contains(out, want) {
			t.Errorf("%q missing from `tools`:\n%s", want, out)
		}
	}
	if strings.Contains(out, "audit_duplicates") {
		t.Error("an audit tool is listed by default; the default is core")
	}
	// and it says how to get more
	for _, want := range []string{"toolsets:", "families:", "--toolsets all"} {
		if !strings.Contains(out, want) {
			t.Errorf("%q missing from the footer:\n%s", want, out)
		}
	}
}

//nolint:paralleltest // viper is global state; Make mutates it
func TestToolsCommandToolsets(t *testing.T) {
	out, err := run(t, "tools", "--toolsets", "curation", "-q")
	if err != nil {
		t.Fatalf("tools --toolsets curation: %v", err)
	}
	lines := strings.Fields(out)
	if len(lines) < 40 {
		t.Errorf("curation listed %d tools, want the whole set", len(lines))
	}
	var hasAudit, hasCore bool
	for _, l := range lines {
		if l == "audit_duplicates" {
			hasAudit = true
		}
		if l == coreTool {
			hasCore = true
		}
	}
	if !hasAudit {
		t.Error("audit_duplicates missing from curation")
	}
	if !hasCore {
		t.Error("core was not included alongside curation")
	}

	// -q prints names only, so nothing should carry a description
	if strings.Contains(out, "read ") || strings.Contains(out, "Find ") {
		t.Errorf("-q printed more than names:\n%s", out)
	}
}

//nolint:paralleltest // viper is global state; Make mutates it
func TestToolsCommandRejectsAnUnknownToolset(t *testing.T) {
	_, err := run(t, "tools", "--toolsets", "nope")
	if err == nil {
		t.Fatal("an unknown toolset should fail the command")
	}
	if !strings.Contains(err.Error(), "curation") {
		t.Errorf("the error should name the valid sets: %v", err)
	}
}

func TestFirstSentence(t *testing.T) {
	t.Parallel()

	for _, c := range []struct{ in, want string }{
		{"One thing. And then another.", "One thing."},
		{"No full stop at all", "No full stop at all"},
		{"Trailing full stop.", "Trailing full stop."},
		{"", ""},
		// an abbreviation's full stop is not the end of the sentence
		{"Set an item's cover from a url (e.g. from item_cover_search) or a file. Changes server state.", "Set an item's cover from a url (e.g. from item_cover_search) or a file."},
		{"Names like Jane Doe, Ph.D. are one name. More.", "Names like Jane Doe, Ph.D. are one name."},
	} {
		if got := firstSentence(c.in); got != c.want {
			t.Errorf("firstSentence(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
