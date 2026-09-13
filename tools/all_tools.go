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
	"cmp"
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
	// Toolsets, when set, restricts registration to the named groups (see
	// Toolsets). "core" is always included, so a set can be asked for on its
	// own. Allow and Deny narrow whatever is left.
	Toolsets []string
	// Allow, when set, restricts registration to matching tools: exact names,
	// prefix/suffix globs (library_*, *_delete) or the "essential" preset.
	Allow []string
	// Deny removes matching tools from whatever Allow left.
	Deny []string
}

// Toolsets group the tools by the job someone is doing, so a client can load a
// working subset instead of all of them. The whole surface is around 12,000
// tokens of tool definitions (name, description, input schema) before anyone
// has asked a question; core alone is about 1,000.
//
// "all" is every tool, which is what the library does when no toolset is asked
// for; the abs-mcp binary defaults to core instead (see cli.FlagData.ToolOptions).
//
// --toolsets also takes a resource family - item, podcast, library, user,
// audit, author, series, narrator, collection, playlist, server - which is
// every tool with that prefix. Those are derived from the registered names
// rather than listed here, so they cannot go stale.
//
// Every tool belongs to exactly one set (TestToolsetsPartition proves it), and
// core is added to whatever else is asked for, because none of the other sets
// can find a library or open an item on their own.
var Toolsets = map[string][]string{
	// enough to find things and read them: the base every other set assumes
	"core": {
		"server_info", "library_list", "library_search", "library_items", "item_get",
	},
	// the API key user's own listening: progress, bookmarks, history, statistics
	"listening": {
		"user_get", "user_in_progress", "user_progress_get", "user_progress_set",
		"user_progress_remove", "user_bookmarks", "user_bookmark_edit",
		"user_history", "user_stats",
	},
	// find what is wrong with a library and fix it: every audit, the metadata
	// and cover and chapter editors, and metadata_rename, which merges
	// near-duplicate authors, narrators, tags, genres, languages and publishers
	"curation": {
		"audit_all", "audit_missing", "audit_unmatched", "audit_issues", "audit_no_audio",
		"audit_podcast_no_episodes", "audit_single_chapter", "audit_author_as_title",
		"audit_author_missing_image", "audit_path", "audit_cover_ratio", "audit_podcast_stale_feed",
		"audit_duplicates", "audit_series_gaps", "audit_spelling", "audit_unembedded", "metadata_rename",
		"item_edit", "item_batch_edit", "item_match", "item_match_apply",
		"item_cover_search", "item_cover_edit", "item_chapters_set",
		"author_list", "author_get", "author_edit", "author_match", "author_image_set",
		"narrator_list", "series_list", "series_get", "series_edit",
		"library_get", "library_filters", "library_recent", "server_tags",
	},
	// subscribe, catch up and back-fill. Same tools as the "podcast" family;
	// both names work, because people reach for either.
	"podcasts": {
		"podcast_search", "podcast_add", "podcast_episodes", "podcast_episode_get",
		"podcast_episode_edit", "podcast_feed_episodes", "podcast_episode_download",
		"podcast_check_new", "podcast_downloads", "podcast_settings", "podcast_episode_delete",
	},
	// group things: shared collections and personal playlists
	"organise": {
		"collection_list", "collection_get", "collection_create", "collection_edit",
		"collection_books_edit", "collection_delete",
		"playlist_list", "playlist_get", "playlist_create", "playlist_edit",
		"playlist_entries_edit", "playlist_delete",
	},
	// running the server rather than using it: libraries, scans, tasks, backups,
	// other people's accounts, and the tools that remove records
	"admin": {
		"server_sessions", "server_tasks", "server_backups", "server_backup_create",
		"library_create", "library_edit", "library_scan", "library_issues_remove",
		"item_rescan", "item_embed_metadata", "item_delete", "author_delete", "user_list",
	},
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
	name        string
	kind        toolKind
	description string
	register    func()
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
		name:        t.Name,
		kind:        kind,
		description: t.Description,
		register:    func() { mcp.AddTool(r.server, t, h) },
	})
}

// RegisterAll adds every tool permitted by opts to the MCP server and returns
// the names registered. It fails when an allow/deny pattern matches no tool,
// so a typo cannot silently hide one.
func RegisterAll(server *mcp.Server, client *abs.Client, opts Options) ([]string, error) {
	r := &registry{server: server, client: client, opts: opts}
	queueTools(r)

	names := make([]string, 0, len(r.pending))
	for _, p := range r.pending {
		names = append(names, p.name)
	}
	keep, err := selected(r, names, opts)
	if err != nil {
		return nil, err
	}

	var registered []string
	for _, p := range r.pending {
		if !keep[p.name] {
			continue
		}
		p.register()
		registered = append(registered, p.name)
	}
	slices.Sort(registered)

	return registered, nil
}

// queueTools queues every tool, before any filtering.
func queueTools(r *registry) {
	registerServerTools(r)
	registerLibraryTools(r)
	registerAuditTools(r)
	registerEmbeddedAudit(r)
	registerSpellingTools(r)
	registerItemTools(r)
	registerAuthorTools(r)
	registerNarratorTools(r)
	registerSeriesTools(r)
	registerCollectionTools(r)
	registerPlaylistTools(r)
	registerPodcastTools(r)
	registerUserTools(r)
}

// selected applies the kind gates and the toolset/allow/deny filters, and is
// shared by RegisterAll and Describe so `abs-mcp tools` cannot drift from what
// the server actually registers.
func selected(r *registry, names []string, opts Options) (map[string]bool, error) {
	sets, err := compileToolsets(opts.Toolsets, names)
	if err != nil {
		return nil, err
	}
	allow, err := compilePatterns(opts.Allow, names, "allow")
	if err != nil {
		return nil, err
	}
	deny, err := compilePatterns(opts.Deny, names, "deny")
	if err != nil {
		return nil, err
	}

	keep := make(map[string]bool, len(r.pending))
	for _, p := range r.pending {
		switch {
		case p.kind == deleteTool && !opts.EnableDelete:
		case p.kind != readTool && opts.ReadOnly:
		case len(sets) > 0 && !sets[p.name]:
		case len(allow) > 0 && !matchesAny(allow, p.name):
		case matchesAny(deny, p.name):
		default:
			keep[p.name] = true
		}
	}

	return keep, nil
}

// compileToolsets turns the requested set names into the tools they hold,
// always including core. An unknown name aborts startup naming the valid ones,
// the way an allow/deny pattern that matches nothing does.
func compileToolsets(raw, known []string) (map[string]bool, error) {
	var asked []string
	for _, entry := range raw {
		for name := range strings.SplitSeq(entry, ",") {
			if name = strings.TrimSpace(name); name != "" {
				asked = append(asked, name)
			}
		}
	}
	if len(asked) == 0 {
		return nil, nil
	}

	out := map[string]bool{}
	for _, name := range asked {
		if name == "all" {
			for _, t := range known {
				out[t] = true
			}
			continue
		}
		if tools, ok := Toolsets[name]; ok {
			for _, t := range tools {
				out[t] = true
			}
			continue
		}
		// not a named set: a resource family, every tool with that prefix
		found := false
		for _, t := range known {
			if strings.HasPrefix(t, name+"_") {
				out[t], found = true, true
			}
		}
		if !found {
			return nil, fmt.Errorf("unknown toolset %q (sets: all, %s; or a resource family: %s)",
				name, strings.Join(setNames(), ", "), strings.Join(resourceFamilies(known), ", "))
		}
	}
	// core is what every other set assumes: without it there is no way to find
	// a library or open an item
	for _, t := range Toolsets["core"] {
		out[t] = true
	}

	return out, nil
}

// setNames lists the curated toolsets, sorted.
func setNames() []string {
	out := make([]string, 0, len(Toolsets))
	for k := range Toolsets {
		out = append(out, k)
	}
	slices.Sort(out)

	return out
}

// resourceFamilies lists the resource prefixes in use, sorted.
func resourceFamilies(known []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range known {
		if i := strings.Index(t, "_"); i > 0 && !seen[t[:i]] {
			seen[t[:i]] = true
			out = append(out, t[:i])
		}
	}
	slices.Sort(out)

	return out
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

// ToolInfo describes a registered tool without a server to register it on.
type ToolInfo struct {
	Name        string
	Kind        string // read, write or delete
	Toolset     string // the curated set it belongs to
	Description string
}

// Describe lists the tools opts would register, for `abs-mcp tools`. It needs
// no connectivity: registration never calls the client, only the handlers do.
func Describe(opts Options) ([]ToolInfo, error) {
	client, err := abs.New("https://describe.invalid", "describe")
	if err != nil {
		return nil, err
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "abs-mcp", Version: "describe"}, nil)

	r := &registry{server: server, client: client, opts: opts}
	queueTools(r)

	names := make([]string, 0, len(r.pending))
	for _, p := range r.pending {
		names = append(names, p.name)
	}
	keep, err := selected(r, names, opts)
	if err != nil {
		return nil, err
	}

	set := map[string]string{}
	for name, members := range Toolsets {
		for _, m := range members {
			// core wins: it is the set a tool is reached through most often
			if set[m] == "" || name == "core" {
				set[m] = name
			}
		}
	}

	kinds := map[toolKind]string{readTool: "read", writeTool: "write", deleteTool: "delete"}
	out := make([]ToolInfo, 0, len(r.pending))
	for _, p := range r.pending {
		if !keep[p.name] {
			continue
		}
		out = append(out, ToolInfo{Name: p.name, Kind: kinds[p.kind], Toolset: set[p.name], Description: p.description})
	}
	slices.SortFunc(out, func(a, b ToolInfo) int { return cmp.Compare(a.Name, b.Name) })

	return out, nil
}

// ToolsetNames lists the curated toolsets, for help output.
func ToolsetNames() []string { return setNames() }

// FamilyNames lists the resource prefixes accepted by --toolsets, for help
// output.
func FamilyNames() []string {
	r := &registry{}
	queueTools(r)
	names := make([]string, 0, len(r.pending))
	for _, p := range r.pending {
		names = append(names, p.name)
	}

	return resourceFamilies(names)
}
