package tools

import (
	"reflect"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// A client reads a tool's annotations to tell what calling it can do, and MCP
// reads destructive false as "only ever adds". Reads say so; a write that
// only creates something new may say it is not destructive; every other
// write, and every delete, says it is. Each additive tool must exist, so the
// list cannot rot into names nothing registers.
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
		if a == nil || a.DestructiveHint == nil {
			t.Errorf("%s has no destructive hint", tool.Name)
			continue
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
