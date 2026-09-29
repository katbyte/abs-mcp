package tools

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/katbyte/abs-mcp/lib/abs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// A client reads a tool's annotations to tell what calling it can do, and MCP
// reads destructive false as "only ever adds". Reads say so; a write that
// only creates something new, or changes nothing on the server, may say it is
// not destructive; every other write, and every delete, says it is. Only a
// tool that reaches past the server, as an email does, says it is open world.
// Each tool on either list must exist, so the lists cannot rot into names
// nothing registers.
func TestAnnotationsSayWhatAToolCanDo(t *testing.T) {
	t.Parallel()

	srv := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	r := &registry{server: srv, client: newTestClient(t), opts: Options{EnableDelete: true}}
	queueTools(r)
	kinds := map[string]toolKind{}
	for _, p := range r.pending {
		kinds[p.name] = p.kind
	}
	for name := range additiveTools {
		if kinds[name] != writeTool {
			t.Errorf("%s is listed as additive but is not a registered write tool", name)
		}
	}
	for name := range openWorldTools {
		if _, ok := kinds[name]; !ok {
			t.Errorf("%s is listed as open world but is not a registered tool", name)
		}
	}

	st, ct := mcp.NewInMemoryTransports()
	for _, p := range r.pending {
		p.register()
	}
	if _, err := srv.Connect(t.Context(), st, nil); err != nil {
		t.Fatal(err)
	}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(t.Context(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	res, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range res.Tools {
		a := tool.Annotations
		if a == nil || a.DestructiveHint == nil || a.OpenWorldHint == nil {
			t.Errorf("%s has no destructive or open world hint", tool.Name)
			continue
		}
		if *a.OpenWorldHint != openWorldTools[tool.Name] {
			t.Errorf("%s: open world %v", tool.Name, *a.OpenWorldHint)
		}
		switch kinds[tool.Name] {
		case readTool:
			if !a.ReadOnlyHint || *a.DestructiveHint {
				t.Errorf("%s is a read tool annotated %+v", tool.Name, a)
			}
		case writeTool:
			if a.ReadOnlyHint || *a.DestructiveHint == additiveTools[tool.Name] {
				t.Errorf("%s: read-only %v, destructive %v; additive %v", tool.Name, a.ReadOnlyHint, *a.DestructiveHint, additiveTools[tool.Name])
			}
		case deleteTool:
			if a.ReadOnlyHint || !*a.DestructiveHint {
				t.Errorf("%s is a delete tool annotated %+v", tool.Name, a)
			}
		}
	}
}

// A list with nothing in it answers [] rather than null: null cannot tell
// "none" from "not fetched", and Go leaves a list nothing was appended to nil.
func TestEmptyNilSlices(t *testing.T) {
	t.Parallel()

	type inner struct{ Tags []string }
	type out struct {
		Items  []inner
		Ptr    *inner
		Names  []string
		Nested [][]string
		Keep   []string
	}
	v := out{Items: []inner{{}}, Ptr: &inner{}, Keep: []string{"x"}}
	emptyNilSlices(reflect.ValueOf(&v).Elem())

	if v.Names == nil || v.Nested == nil || v.Items[0].Tags == nil || v.Ptr.Tags == nil {
		t.Errorf("nil slices survived: %+v", v)
	}
	if len(v.Keep) != 1 {
		t.Error("a populated slice was touched")
	}
}

// A handler that panics answers its call with an error naming the tool and
// logs the stack, rather than ending the session: the next call is served.
func TestAPanicInAToolIsAnErrorNotACrash(t *testing.T) {
	t.Parallel()

	var (
		mu     sync.Mutex
		logged strings.Builder
	)
	srv := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	r := &registry{server: srv, client: newTestClient(t), errorLog: func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		_, _ = fmt.Fprintf(&logged, format, args...)
	}}
	type none struct{}
	add(r, writeTool, &mcp.Tool{Name: "zzyzx_panic"}, func(context.Context, *mcp.CallToolRequest, none) (*mcp.CallToolResult, none, error) {
		var found map[string]*abs.Library

		return nil, none{}, errors.New(found["9"].ID) // a nil dereference
	})
	add(r, readTool, &mcp.Tool{Name: "zzyzx_fine"}, func(context.Context, *mcp.CallToolRequest, none) (*mcp.CallToolResult, none, error) {
		return nil, none{}, nil
	})
	for _, p := range r.pending {
		p.register()
	}

	st, ct := mcp.NewInMemoryTransports()
	ctx := t.Context()
	if _, err := srv.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })

	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "zzyzx_panic", Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("the panic reached the session: %v", err)
	}
	var texts []string
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			texts = append(texts, tc.Text)
		}
	}
	msg := strings.Join(texts, "; ")
	if !res.IsError || !strings.Contains(msg, "internal error in zzyzx_panic") || !strings.Contains(msg, "nil pointer") {
		t.Errorf("the answer = %q (error %v), want the tool and the panic named", msg, res.IsError)
	}
	mu.Lock()
	log := logged.String()
	mu.Unlock()
	if !strings.Contains(log, "internal error in zzyzx_panic") || !strings.Contains(log, "goroutine") {
		t.Errorf("the log = %q, want the panic and its stack", log)
	}

	res, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "zzyzx_fine", Arguments: map[string]any{}})
	if err != nil || res.IsError {
		t.Fatalf("the call after the panic failed: %v %+v", err, res)
	}
}

func TestRegisterAllKinds(t *testing.T) {
	t.Parallel()

	all := register(t, Options{EnableDelete: true})
	dflt := register(t, Options{})
	ro := register(t, Options{ReadOnly: true})

	if len(all) <= len(dflt) || len(dflt) <= len(ro) || len(ro) == 0 {
		t.Fatalf("counts all=%d default=%d read-only=%d", len(all), len(dflt), len(ro))
	}
	for _, name := range []string{"item_delete", "podcast_episode_delete", "author_delete", "library_issues_remove", "user_history_remove"} {
		if slices.Contains(dflt, name) {
			t.Errorf("%s registered without --enable-delete", name)
		}
		if !slices.Contains(all, name) {
			t.Errorf("%s missing with --enable-delete", name)
		}
	}
	for _, name := range ro {
		if strings.HasSuffix(name, "_set") || strings.HasSuffix(name, "_delete") || strings.HasSuffix(name, "_scan") || strings.HasSuffix(name, "_edit") {
			t.Errorf("%s registered under --read-only", name)
		}
	}
	for _, name := range EssentialTools {
		if !slices.Contains(dflt, name) {
			t.Errorf("essential tool %s does not exist", name)
		}
	}
	if !slices.IsSorted(dflt) {
		t.Error("registered names not sorted")
	}
}

func TestRegisterAllFilters(t *testing.T) {
	t.Parallel()

	got := register(t, Options{Allow: []string{"essential"}})
	want := slices.Clone(EssentialTools)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("essential = %v", got)
	}

	got = register(t, Options{Allow: []string{"library_*,user_get"}, Deny: []string{"*_scan"}})
	for _, name := range got {
		if !strings.HasPrefix(name, "library_") && name != "user_get" {
			t.Errorf("unexpected %s", name)
		}
		if name == "library_scan" {
			t.Error("denied tool registered")
		}
	}
	if !slices.Contains(got, "library_list") || !slices.Contains(got, "user_get") {
		t.Errorf("allow list not honoured: %v", got)
	}

	if _, err := RegisterAll(mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil), newTestClient(t), Options{Allow: []string{"bogus_*"}}); err == nil {
		t.Error("unknown allow pattern accepted")
	}
	if _, err := RegisterAll(mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil), newTestClient(t), Options{Deny: []string{"nope"}}); err == nil {
		t.Error("unknown deny pattern accepted")
	}
}

func TestMatchPattern(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		pattern, name string
		want          bool
	}{
		{"item_get", "item_get", true},
		{"item_get", "item_gets", false},
		{"item_*", "item_get", true},
		{"item_*", "library_items", false},
		{"*_delete", "item_delete", true},
		{"*", "anything", true},
	} {
		if got := matchPattern(tc.pattern, tc.name); got != tc.want {
			t.Errorf("matchPattern(%q, %q) = %v", tc.pattern, tc.name, got)
		}
	}
}

// Every tool belongs to exactly one toolset. Without this a tool added later
// is silently unreachable for anyone using --toolsets, which is the failure
// mode that would go unnoticed longest.
func TestToolsetsPartition(t *testing.T) {
	t.Parallel()

	all := register(t, Options{EnableDelete: true})

	seen := map[string]string{}
	for set, names := range Toolsets {
		if set == "core" {
			continue
		}
		for _, name := range names {
			if !slices.Contains(all, name) {
				t.Errorf("toolset %s lists %q, which is not a registered tool", set, name)
			}
			if other, dup := seen[name]; dup {
				t.Errorf("%q is in both %s and %s; a tool belongs to one set", name, other, set)
			}
			seen[name] = set
		}
	}
	for _, name := range Toolsets["core"] {
		if !slices.Contains(all, name) {
			t.Errorf("core lists %q, which is not a registered tool", name)
		}
		seen[name] = "core"
	}

	for _, name := range all {
		if seen[name] == "" {
			t.Errorf("%q is in no toolset; add it to one in tools/all_tools.go", name)
		}
	}
}

// core comes along with whatever else is asked for, because nothing else can
// find a library or open an item.
func TestToolsetsIncludeCore(t *testing.T) {
	t.Parallel()

	got := register(t, Options{Toolsets: []string{"podcast"}, EnableDelete: true})

	for _, name := range Toolsets["core"] {
		if !slices.Contains(got, name) {
			t.Errorf("core tool %q missing from --toolsets podcasts", name)
		}
	}
	if !slices.Contains(got, "podcast_search") {
		t.Error("podcast_search missing from --toolsets podcast")
	}
	if slices.Contains(got, "collection_list") {
		t.Error("collection_list registered for --toolsets podcast")
	}
}

// a family name is every tool with that prefix, derived from what is
// registered so a new tool joins its family without anyone remembering to.
func TestToolsetsResourceFamily(t *testing.T) {
	t.Parallel()

	got := register(t, Options{Toolsets: []string{"item"}, EnableDelete: true})

	all := register(t, Options{EnableDelete: true})
	for _, name := range all {
		want := strings.HasPrefix(name, "item_") || slices.Contains(Toolsets["core"], name)
		if has := slices.Contains(got, name); has != want {
			t.Errorf("--toolsets item: %q registered=%v, want %v", name, has, want)
		}
	}
}

// a family and a curated set compose, which globs could not do: --toolsets
// core is ANDed with --allow-tools, so "core plus every item tool" had no
// spelling before.
func TestToolsetsFamilyAndSet(t *testing.T) {
	t.Parallel()

	got := register(t, Options{Toolsets: []string{"listening", "audit"}})

	for _, want := range []string{"user_in_progress", "audit_all", "item_get", "library_search"} {
		if !slices.Contains(got, want) {
			t.Errorf("%q missing from --toolsets listening,audit", want)
		}
	}
	if slices.Contains(got, "podcast_search") {
		t.Error("podcast_search should not be in listening,audit")
	}
}

// toolsets select the pool; allow and deny still narrow it.
func TestToolsetsComposeWithDeny(t *testing.T) {
	t.Parallel()

	got := register(t, Options{Toolsets: []string{"listening"}, Deny: []string{"user_stats"}})

	if slices.Contains(got, "user_stats") {
		t.Error("user_stats survived a deny")
	}
	if !slices.Contains(got, "user_in_progress") {
		t.Error("user_in_progress should still be there")
	}
}

func TestToolsetsUnknown(t *testing.T) {
	t.Parallel()

	_, err := RegisterAll(mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil), newTestClient(t), Options{Toolsets: []string{"nope"}})
	if err == nil {
		t.Fatal("an unknown toolset should abort startup")
	}
	if !strings.Contains(err.Error(), "curation") {
		t.Errorf("the error should name the valid sets, got: %v", err)
	}
}

// "all" is every tool, which is how someone gets the full surface back once
// the binary defaults to core.
func TestToolsetsAll(t *testing.T) {
	t.Parallel()

	all := register(t, Options{EnableDelete: true})
	got := register(t, Options{EnableDelete: true, Toolsets: []string{"all"}})

	if len(got) != len(all) {
		t.Errorf("--toolsets all registered %d tools, want all %d", len(got), len(all))
	}
	// and it still composes with deny
	less := register(t, Options{EnableDelete: true, Toolsets: []string{"all"}, Deny: []string{"*_delete"}})
	if len(less) >= len(got) {
		t.Errorf("deny had no effect on --toolsets all: %d vs %d", len(less), len(got))
	}
}

// no toolset at all is every tool: the library stays unopinionated, and the
// binary applies its own default on top (cli.DefaultToolsets).
func TestToolsetsEmptyMeansEverything(t *testing.T) {
	t.Parallel()

	if got, all := register(t, Options{}), register(t, Options{Toolsets: []string{"all"}}); len(got) != len(all) {
		t.Errorf("no toolsets registered %d, want %d", len(got), len(all))
	}
}

// ToolsetNames and FamilyNames back `abs-mcp tools`, so they have to stay in
// step with what is actually registered.
func TestToolsetAndFamilyNames(t *testing.T) {
	t.Parallel()

	sets := ToolsetNames()
	if !slices.IsSorted(sets) {
		t.Errorf("toolset names are not sorted: %v", sets)
	}
	for _, want := range []string{"core", "curation", "listening", "admin"} {
		if !slices.Contains(sets, want) {
			t.Errorf("%q missing from ToolsetNames: %v", want, sets)
		}
	}
	if len(sets) != len(Toolsets) {
		t.Errorf("ToolsetNames returned %d, want %d", len(sets), len(Toolsets))
	}

	families := FamilyNames()
	if !slices.IsSorted(families) {
		t.Errorf("family names are not sorted: %v", families)
	}
	// every registered tool's prefix must be offered as a family
	for _, name := range register(t, Options{EnableDelete: true}) {
		prefix, _, ok := strings.Cut(name, "_")
		if !ok {
			t.Errorf("%q has no resource prefix", name)
			continue
		}
		if !slices.Contains(families, prefix) {
			t.Errorf("%q is registered but %q is not offered as a family", name, prefix)
		}
	}
}
