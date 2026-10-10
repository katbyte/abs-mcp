package cli

import (
	"slices"
	"strings"
	"testing"

	"github.com/katbyte/abs-mcp/tools"
)

// The binary registers core and nothing else unless asked, so a client that
// just points abs-mcp at a server does not spend 12,000 tokens of context on
// tools it will not call.
func TestDefaultToolsetsIsCore(t *testing.T) {
	t.Parallel()

	var f FlagData
	opts := f.ToolOptions()
	if !slices.Equal(opts.Toolsets, []string{coreSet}) {
		t.Errorf("default toolsets = %v, want [core]", opts.Toolsets)
	}

	got, err := tools.Describe(opts)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(got))
	for _, ti := range got {
		names = append(names, ti.Name)
		if ti.Kind != "read" {
			t.Errorf("%s is %s; the default set should never change server state", ti.Name, ti.Kind)
		}
	}
	want := slices.Clone(tools.Toolsets[coreSet])
	slices.Sort(want)
	if !slices.Equal(names, want) {
		t.Errorf("default registered %v, want %v", names, want)
	}
}

// An allow list asks for tools as toolsets do. On its own it is only the
// tools it names: the default set is for a binary told nothing, and would
// otherwise be added to every allow list. Beside toolsets it adds to them,
// "these sets and this tool as well", where it used to narrow them and so
// registered nothing for a tool the sets did not hold.
func TestAnAllowListAsksForTools(t *testing.T) {
	t.Parallel()

	names := func(f FlagData) []string {
		t.Helper()

		got, err := tools.Describe(f.ToolOptions())
		if err != nil {
			t.Fatal(err)
		}
		out := make([]string, 0, len(got))
		for _, ti := range got {
			out = append(out, ti.Name)
		}

		return out
	}

	if got := names(FlagData{AllowTools: []string{scanTool}}); !slices.Equal(got, []string{scanTool}) {
		t.Errorf("--allow-tools library_scan registered %v, want that tool alone", got)
	}

	core := slices.Clone(tools.Toolsets[coreSet])
	slices.Sort(core)
	want := slices.Sorted(slices.Values(append(slices.Clone(core), scanTool)))
	if got := names(FlagData{Toolsets: []string{coreSet}, AllowTools: []string{scanTool}}); !slices.Equal(got, want) {
		t.Errorf("--toolsets core --allow-tools library_scan registered %v, want core and the scan", got)
	}

	// and what narrows a set is a deny list
	if got := names(FlagData{Toolsets: []string{coreSet}, DenyTools: []string{"library_*"}}); len(got) == 0 || len(got) >= len(core) || slices.ContainsFunc(got, func(n string) bool { return strings.HasPrefix(n, "library_") }) {
		t.Errorf("--toolsets core --deny-tools 'library_*' registered %v, want core without its library tools", got)
	}

	// an allow list lets nothing past the gates
	if got := names(FlagData{AllowTools: []string{deleteTool, scanTool, listTool}, ReadOnly: true}); !slices.Equal(got, []string{listTool}) {
		t.Errorf("a read-only allow list registered %v, want the one read tool it names", got)
	}
	if got := names(FlagData{AllowTools: []string{deleteTool, listTool}}); !slices.Equal(got, []string{listTool}) {
		t.Errorf("an allow list naming a delete tool, with deletes off, registered %v", got)
	}
	if got := names(FlagData{AllowTools: []string{deleteTool, listTool}, EnableDelete: true}); !slices.Equal(got, []string{deleteTool, listTool}) {
		t.Errorf("an allow list naming a delete tool, with deletes on, registered %v", got)
	}
}

// An explicit --toolsets replaces the default rather than adding to it.
func TestExplicitToolsetsOverrideTheDefault(t *testing.T) {
	t.Parallel()

	f := FlagData{Toolsets: []string{"all"}}
	got, err := tools.Describe(f.ToolOptions())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) <= len(tools.Toolsets[coreSet]) {
		t.Errorf("--toolsets all registered %d tools, want the whole surface", len(got))
	}
}
