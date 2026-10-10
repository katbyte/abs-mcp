// Package tools defines the MCP tools exposed by abs-mcp, split by the resource
// they act on (server.go, libraries.go, items.go, ...). Tools are named
// resource-first (library_*, item_*, me_*) so they group by what they act on.
//
// Every tool is registered through add with a kind: read tools never change
// server state, write tools do (and are dropped under --read-only), and delete
// tools remove library records or files (and are only registered with
// --enable-delete). --toolsets and --allow-tools ask for tools, and
// --deny-tools takes tools out of what they asked for. Which tools a session
// gets from all that, and what each tells a client about itself, is go-kt's
// registry's work.
package tools

import (
	"slices"

	"github.com/katbyte/abs-mcp/sdk/abs"
	"github.com/katbyte/go-kt/lock"
	mcpregistry "github.com/katbyte/go-kt/mcp/registry"
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
	// own.
	Toolsets []string
	// Allow asks for tools by name: exact names, prefix/suffix globs
	// (library_*, *_delete) or the "essential" preset. Beside Toolsets it
	// adds to them, "these sets and these tools as well"; on its own it is
	// only the tools it names. It lets nothing past ReadOnly or EnableDelete.
	Allow []string
	// Deny removes matching tools from whatever Toolsets and Allow asked for:
	// it is how a toolset is narrowed.
	Deny []string
	// ProviderTag is the prefix of the tag that records which store a match
	// came from, "zz-provider:" by default; "off" writes and reads none.
	ProviderTag string
	// Providers is the default order of metadata providers to ask when a call
	// names none: the store the books were bought from first, then the rest,
	// [audible.ca, audible]. Empty means the library's own provider alone.
	Providers []string
	// AuditSkip names the audit rules to leave out (see AuditRules). Every
	// rule is on by default; one that is a way of filing rather than a
	// mistake can be switched off for a library kept another way.
	AuditSkip []string
}

// Toolsets group the tools by the job someone is doing, so a client can load a
// working subset instead of all of them. The whole surface is around 29,000
// tokens of tool definitions (name, description, input schema) before anyone
// has asked a question; core alone is about 1,200.
//
// "all" is every tool, which is what the library does when no toolset is asked
// for; the abs-mcp binary defaults to core instead (see cli.FlagData.ToolOptions).
//
// --toolsets also takes a resource family - item, podcast, library, user,
// audit, author, series, narrator, collection, playlist, feed, server - which is
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
	// the API key user's own listening: progress, bookmarks, history, statistics,
	// and sending a book to an e-reader
	"listening": {
		"user_get", "user_in_progress", "user_progress_get", "user_progress_set",
		"user_bookmarks", "user_bookmark_edit",
		"user_history", "user_history_remove", "user_stats", "item_send_ebook",
	},
	// find what is wrong with a library and fix it: every audit, the metadata
	// and cover and chapter editors, and metadata_rename, which merges
	// near-duplicate authors, narrators, tags, genres, languages and publishers
	"curation": {
		"audit_all", "audit_missing", "audit_unmatched", "audit_issues", "audit_no_audio",
		"audit_podcasts", "audit_chapters", "audit_authors",
		"audit_path", "audit_covers",
		"audit_duplicates", "audit_series", "audit_spelling", "audit_narrators", "audit_genres", "audit_unembedded", "audit_matched", "audit_abridged", "audit_whitespace", "audit_unplayable", "metadata_rename",
		"item_edit", "item_match", "item_match_apply", "item_match_batch", "item_match_apply_batch", "item_match_tag", "item_compare_audio",
		"item_cover_search", "item_cover_edit", "item_cover_upgrade", "item_chapters_set",
		"author_list", "author_get", "author_edit", "author_match", "author_match_apply",
		"narrator_list", "series_list", "series_get", "series_edit", "series_merge",
		"library_get", "library_filters", "library_recent", "server_tags",
	},
	// subscribe, catch up and back-fill. Same tools as the "podcast" family;
	// both names work, because people reach for either.
	"podcasts": {
		"podcast_search", "podcast_add", "podcast_episodes", "podcast_episode_get",
		"podcast_episode_edit", "podcast_feed_episodes", "podcast_episode_download",
		"podcast_check_new", "podcast_downloads", "podcast_edit", "podcast_episode_delete",
	},
	// group things: shared collections and personal playlists, and the feeds
	// and links that let someone listen to them from outside the app
	"organise": {
		"collection_list", "collection_get", "collection_create", "collection_edit",
		"collection_delete",
		"playlist_list", "playlist_get", "playlist_create", "playlist_edit",
		"playlist_delete", "feed_list", "feed_edit",
	},
	// running the server rather than using it: libraries, scans, tasks, backups,
	// other people's accounts, and the tools that remove records
	"admin": {
		"server_sessions", "server_tasks", "server_backups", "server_backup_create",
		"library_create", "library_edit", "library_scan", "library_issues_remove", "library_issues_merge",
		"item_rescan", "item_embed_metadata", "item_delete", "author_delete", "user_list",
		"user_create", "user_edit",
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

const (
	readTool   = mcpregistry.Read
	writeTool  = mcpregistry.Write
	deleteTool = mcpregistry.Delete
)

// registry is this application's tools and what they share. Which of them a
// session gets, and what each tells a client about itself, is go-kt's
// registry's work (tools), so the allow and deny patterns are checked
// against every tool before any is registered.
type registry struct {
	client *abs.Client
	opts   Options
	// tools holds every tool queued, made when the first is (see queued)
	tools *mcpregistry.Registry
	// locks keep the calls of one turn from undoing each other (see
	// locks.go): a set of this server's own
	locks lock.Set
	// errorLog is where a handler's panic is logged; nil is the process's
	// own log, which writes to stderr
	errorLog func(format string, args ...any)
}

// toolHints are what a tool tells a client about itself beyond its kind.
//
// A write tool here as additive only ever adds - a new library, collection,
// playlist, podcast or account, or episodes the podcast did not hold - or
// changes nothing on the server, as sending an ebook does not, and so can say
// it is not destructive. Every other write tool can overwrite or remove
// something. A backup is not here: the server prunes the oldest past its
// limit.
//
// One that sends out itself sends something to a person or a service beyond
// the server: sending an ebook emails it out. A tool that has the server ask
// its own metadata providers is not one. It is what MCP calls open world.
var toolHints = map[string]mcpregistry.Hints{
	"library_create":           {Additive: true},
	"collection_create":        {Additive: true},
	"playlist_create":          {Additive: true},
	"podcast_add":              {Additive: true},
	"podcast_episode_download": {Additive: true},
	"user_create":              {Additive: true},
	"item_send_ebook":          {Additive: true, SendsOut: true},
}

// queued is every tool queued so far, in go-kt's registry, which is made
// when the first tool is.
func (r *registry) queued() *mcpregistry.Registry {
	if r.tools == nil {
		r.tools = mcpregistry.New(mcpregistry.Config{
			Toolsets:  Toolsets,
			Essential: EssentialTools,
			Hints:     toolHints,
			LogError:  r.errorLog,
		})
	}

	return r.tools
}

// add queues a typed tool for registration. go-kt's registry sets the MCP
// annotations from kind and the tool's hints so clients can tell read-only
// from destructive tools without parsing descriptions, turns a panic in the
// handler into an ordinary tool error, and sends empty collections as []
// rather than null: an AI client reading "items": null cannot tell "none"
// from "not fetched", and Go leaves un-appended slices nil.
func add[In, Out any](r *registry, kind mcpregistry.Kind, t *mcp.Tool, h mcp.ToolHandlerFor[In, Out]) {
	mcpregistry.Add(r.queued(), kind, t, h)
}

// queuedFor is every tool queued for these options, with the options made
// the server's own: registration never calls the client, only the handlers
// do.
func queuedFor(client *abs.Client, opts Options) (*registry, error) {
	opts.Providers = slices.Clone(opts.Providers) // the server's own, whatever the caller does with its slice
	var err error
	if opts.AuditSkip, err = auditSkips(opts.AuditSkip); err != nil {
		return nil, err
	}
	r := &registry{client: client, opts: opts}
	queueTools(r)

	return r, nil
}

// RegisterAll adds every tool permitted by opts to the MCP server and returns
// the names registered. It fails when an allow/deny pattern matches no tool,
// so a typo cannot silently hide one.
func RegisterAll(server *mcp.Server, client *abs.Client, opts Options) ([]string, error) {
	r, err := queuedFor(client, opts)
	if err != nil {
		return nil, err
	}

	return r.queued().Register(server, opts.selection())
}

// selection is the part of the options that chooses tools, as go-kt's
// registry takes it.
func (o *Options) selection() mcpregistry.Selection {
	return mcpregistry.Selection{
		ReadOnly:     o.ReadOnly,
		EnableDelete: o.EnableDelete,
		Toolsets:     o.Toolsets,
		Allow:        o.Allow,
		Deny:         o.Deny,
	}
}

// queueTools queues every tool, before any filtering.
func queueTools(r *registry) {
	registerServerTools(r)
	registerLibraryTools(r)
	registerIssuesMerge(r)
	registerAuditTools(r)
	registerPathAudit(r)
	registerChaptersAudit(r)
	registerCoverAudit(r)
	registerEmbeddedAudit(r)
	registerSpellingTools(r)
	registerNarratorAudit(r)
	registerAuthorAudit(r)
	registerSeriesAudit(r)
	registerMatchedAudit(r)
	registerAbridgedAudit(r)
	registerWhitespaceAudit(r)
	registerUnplayableAudit(r)
	registerGenresAudit(r)
	registerMatchBatchTools(r)
	registerMatchTagTool(r)
	registerCompareAudio(r)
	registerItemTools(r)
	registerAuthorTools(r)
	registerNarratorTools(r)
	registerSeriesTools(r)
	registerCollectionTools(r)
	registerPlaylistTools(r)
	registerPodcastTools(r)
	registerUserTools(r)
	registerAccountTools(r)
	registerFeedTools(r)
	registerSendEbookTool(r)
}

// ToolInfo describes a registered tool without a server to register it on.
type ToolInfo = mcpregistry.Info

// described is every tool queued with no server behind it, for the answers
// that need none.
func described(opts Options) (*registry, error) {
	client, err := abs.New("https://describe.invalid", "describe")
	if err != nil {
		return nil, err
	}

	return queuedFor(client, opts)
}

// Describe lists the tools opts would register, for `abs-mcp tools`. It needs
// no connectivity, and makes the same choice RegisterAll does.
func Describe(opts Options) ([]ToolInfo, error) {
	r, err := described(opts)
	if err != nil {
		return nil, err
	}

	return r.queued().Describe(opts.selection())
}

// ToolsetNames lists the curated toolsets, for help output.
func ToolsetNames() []string {
	return mcpregistry.New(mcpregistry.Config{Toolsets: Toolsets}).ToolsetNames()
}

// FamilyNames lists the resource prefixes accepted by --toolsets, for help
// output.
func FamilyNames() []string {
	r, err := described(Options{})
	if err != nil {
		return nil
	}

	return r.queued().FamilyNames()
}
