// Package tools defines the MCP tools exposed by abs-mcp, split by the resource
// they act on (server.go, libraries.go, items.go, ...). Tools are named
// resource-first (library_*, item_*, me_*) so they group by what they act on.
//
// Every tool is registered through add with a kind: read tools never change
// server state, write tools do (and are dropped under --read-only), and delete
// tools remove library records or files (and are only registered with
// --enable-delete). --allow-tools / --deny-tools further narrow the set.
package tools

import (
	"fmt"
	"slices"
	"strings"

	"github.com/katbyte/abs-mcp/lib/abs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Options controls which tools are registered.
type Options struct {
	// ReadOnly registers only tools that never change server state.
	ReadOnly bool
	// EnableDelete registers the tools that delete library items, podcast
	// episodes and authors. Off unless the operator opts in.
	EnableDelete bool
	// Allow, when set, restricts registration to matching tools: exact names,
	// prefix/suffix globs (library_*, *_delete) or the "essential" preset.
	Allow []string
	// Deny removes matching tools from whatever Allow left.
	Deny []string
}

// EssentialTools is the curated preset selected by --allow-tools essential:
// enough to find things, read them, and keep listening progress in sync.
var EssentialTools = []string{
	"library_list",
	"library_search",
	"library_items",
	"item_get",
	"user_in_progress",
	"user_progress_get",
	"user_progress_set",
}

type toolKind int

const (
	readTool toolKind = iota
	writeTool
	deleteTool
)

type pending struct {
	name     string
	kind     toolKind
	register func()
}

// registry collects tool registrations so the allow/deny patterns can be
// validated against the full tool list before anything is added.
type registry struct {
	server  *mcp.Server
	client  *abs.Client
	opts    Options
	pending []pending
}

// add queues a tool for registration. It sets the MCP annotations from kind so
// clients can tell read-only from destructive tools without parsing
// descriptions.
func add[In, Out any](r *registry, kind toolKind, t *mcp.Tool, h mcp.ToolHandlerFor[In, Out]) {
	f := false
	switch kind {
	case readTool:
		t.Annotations = &mcp.ToolAnnotations{ReadOnlyHint: true, DestructiveHint: &f, OpenWorldHint: &f}
	case writeTool:
		t.Annotations = &mcp.ToolAnnotations{DestructiveHint: &f, OpenWorldHint: &f}
	case deleteTool:
		t.Annotations = &mcp.ToolAnnotations{DestructiveHint: new(true), OpenWorldHint: &f}
	}

	r.pending = append(r.pending, pending{
		name:     t.Name,
		kind:     kind,
		register: func() { mcp.AddTool(r.server, t, h) },
	})
}

// RegisterAll adds every tool permitted by opts to the MCP server and returns
// the names registered. It fails when an allow/deny pattern matches no tool,
// so a typo cannot silently hide one.
func RegisterAll(server *mcp.Server, client *abs.Client, opts Options) ([]string, error) {
	r := &registry{server: server, client: client, opts: opts}

	registerServerTools(r)
	registerLibraryTools(r)
	registerAuditTools(r)
	registerTerminologyTools(r)
	registerItemTools(r)
	registerAuthorTools(r)
	registerNarratorTools(r)
	registerSeriesTools(r)
	registerCollectionTools(r)
	registerPlaylistTools(r)
	registerPodcastTools(r)
	registerUserTools(r)

	names := make([]string, 0, len(r.pending))
	for _, p := range r.pending {
		names = append(names, p.name)
	}

	allow, err := compilePatterns(opts.Allow, names, "allow")
	if err != nil {
		return nil, err
	}
	deny, err := compilePatterns(opts.Deny, names, "deny")
	if err != nil {
		return nil, err
	}

	var registered []string
	for _, p := range r.pending {
		if p.kind == deleteTool && !opts.EnableDelete {
			continue
		}
		if p.kind != readTool && opts.ReadOnly {
			continue
		}
		if len(allow) > 0 && !matchesAny(allow, p.name) {
			continue
		}
		if matchesAny(deny, p.name) {
			continue
		}
		p.register()
		registered = append(registered, p.name)
	}

	slices.Sort(registered)

	return registered, nil
}

// compilePatterns expands the essential preset, splits comma-separated
// entries, and checks that every pattern matches at least one known tool.
func compilePatterns(raw, known []string, which string) ([]string, error) {
	var out []string
	for _, entry := range raw {
		for pat := range strings.SplitSeq(entry, ",") {
			pat = strings.TrimSpace(pat)
			if pat == "" {
				continue
			}
			if pat == "essential" {
				out = append(out, EssentialTools...)
				continue
			}
			if !slices.ContainsFunc(known, func(n string) bool { return matchPattern(pat, n) }) {
				return nil, fmt.Errorf("%s-tools pattern %q matches no tool (have: %s)", which, pat, strings.Join(known, ", "))
			}
			out = append(out, pat)
		}
	}

	return out, nil
}

func matchesAny(patterns []string, name string) bool {
	return slices.ContainsFunc(patterns, func(p string) bool { return matchPattern(p, name) })
}

// matchPattern supports exact names plus a single leading or trailing '*'.
func matchPattern(pattern, name string) bool {
	switch {
	case pattern == "*":
		return true
	case strings.HasSuffix(pattern, "*"):
		return strings.HasPrefix(name, strings.TrimSuffix(pattern, "*"))
	case strings.HasPrefix(pattern, "*"):
		return strings.HasSuffix(name, strings.TrimPrefix(pattern, "*"))
	default:
		return pattern == name
	}
}
