package tools

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/katbyte/abs-mcp/lib/abs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Audiobookshelf keeps the asin a match wrote and forgets which store it came
// from, and an asin alone does not say: the same recording has another asin
// in each Audible region, some titles are in one region only, and an isbn
// from Google names a print edition. So the store is recorded as a tag,
// "provider:audible.ca", written when a match is applied and read back to ask
// that store first. "provider:none" is the collector's note that a book was
// looked at and has nothing to match, so the unmatched audit stops listing
// it and the batch search stops asking about it.

const (
	defaultProviderTag = "zz-provider:" // sorts last in the tag list, out of the subjects' way
	providerNone       = "none"
	// providerTagOff as the prefix means no tag is written or read
	providerTagOff = "off"
)

// providerConfig is how one server records and asks the stores, from its
// Options: the prefix of the provider tag and the stores to ask when a call
// names none. It is handed to whatever reads it rather than kept in package
// variables, where registering a second server, or a test running alongside,
// changed the first one's while its tools were reading them.
type providerConfig struct {
	tag       string   // Options.ProviderTag: empty is zz-provider:, off is none
	providers []string // Options.Providers: empty is the library's own provider
}

// providerConfig is the registry's provider settings.
func (r *registry) providerConfig() providerConfig {
	return providerConfig{tag: r.opts.ProviderTag, providers: r.opts.Providers}
}

// prefix is the tag prefix in use: a collector who wants it read as a word
// can make it "provider:", and one who wants none at all can turn it off.
func (p providerConfig) prefix() string {
	if p.tag == "" {
		return defaultProviderTag
	}
	return p.tag
}

// providersFor returns the providers a call should ask, in order: the ones
// it named, else the configured default, else the library's own.
func (p providerConfig) providersFor(named []string, lib *abs.Library) []string {
	if len(named) > 0 {
		return named
	}
	if len(p.providers) > 0 {
		return slices.Clone(p.providers)
	}
	return []string{lib.Provider}
}

// checkProviders refuses a store the server does not have, among the ones a
// call named or else the configured default: the server answers a name it
// does not know from Google, so every book comes back not found and the
// region gets the blame. lookup is for a caller that looks an asin up, which
// only an Audible store can. The library's own provider, the last fallback,
// is the server's own setting and is not checked.
func (p providerConfig) checkProviders(ctx context.Context, client *abs.Client, named []string, lookup bool) error {
	names := named
	if len(names) == 0 {
		names = p.providers
	}
	if len(names) == 0 {
		return nil
	}
	// the list is only what the names are checked against: a server that
	// cannot give it is not refused for that, and the search that follows
	// says what is wrong
	known, _, err := client.Providers(ctx)
	for _, name := range names {
		switch {
		case err == nil && !slices.Contains(known, name):
			return fmt.Errorf("the server has no provider %q; it has %s", name, strings.Join(known, ", "))
		case lookup && !isAudible(name):
			return fmt.Errorf("%s cannot look up an asin; only an Audible store can: %s", name, audibleStores(known))
		}
	}
	return nil
}

// lookupRefusal refuses a library whose asins would all come back not found:
// with no providers named and none configured, a lookup asks the library's
// own provider, and only an Audible store can look an asin up. A new library
// is on google unless someone changed it.
func (p providerConfig) lookupRefusal(named []string, lib *abs.Library) error {
	if len(named) > 0 || len(p.providers) > 0 || lib.IsPodcast() || isAudible(lib.Provider) {
		return nil
	}
	on := "the " + lib.Provider + " provider"
	if lib.Provider == "" {
		on = "the server's default provider"
	}
	return fmt.Errorf("library %q is on %s, which cannot look up an asin, so every book would come back not_found: pass providers, or start the server with --providers (ABS_PROVIDERS), e.g. audible.ca,audible", lib.Name, on)
}

// isAudible reports whether a provider is one of the Audible stores.
func isAudible(provider string) bool {
	return provider == "audible" || strings.HasPrefix(provider, "audible.")
}

// audibleRegion is an Audible store's region as the chapter lookup takes it:
// audible.ca is ca, and audible alone is us. Empty for any other provider.
func audibleRegion(provider string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	switch {
	case provider == "audible":
		return "us"
	case strings.HasPrefix(provider, "audible."):
		return strings.TrimPrefix(provider, "audible.")
	}
	return ""
}

// audibleStores names the Audible stores among the server's providers.
func audibleStores(known []string) string {
	var out []string
	for _, k := range known {
		if isAudible(k) {
			out = append(out, k)
		}
	}
	if len(out) == 0 {
		return "audible, or a region such as audible.ca"
	}
	return strings.Join(out, ", ")
}

// providerTagging reports whether the tag is in use.
func (p providerConfig) providerTagging() bool { return p.prefix() != providerTagOff }

// providerTag returns the provider recorded on the item, "none", or "".
func (p providerConfig) providerTag(it *abs.Item) string {
	if !p.providerTagging() {
		return ""
	}
	prefix := p.prefix()
	for _, t := range it.Media.Tags {
		if strings.HasPrefix(strings.ToLower(t), strings.ToLower(prefix)) {
			return strings.TrimSpace(t[len(prefix):])
		}
	}
	return ""
}

// markedUnmatchable reports whether the collector has said there is nothing
// to match this book to.
func (p providerConfig) markedUnmatchable(it *abs.Item) bool {
	return strings.EqualFold(p.providerTag(it), providerNone)
}

// auditCheck is the named audit check as this server runs it: the unmatched
// one leaves out the books the provider tag marks as having nothing to match,
// which only the configured prefix can recognise.
func (p providerConfig) auditCheck(name string) auditCheck {
	check := auditChecksByName[name]
	if name != "unmatched" {
		return check
	}
	return func(it *abs.Item) (string, bool) {
		if p.markedUnmatchable(it) {
			return "", false
		}
		return check(it)
	}
}

// withProviderTag returns tags with the provider tag set to provider and any
// earlier provider tag dropped.
func (p providerConfig) withProviderTag(tags []string, provider string) []string {
	if !p.providerTagging() {
		return tags
	}
	prefix := p.prefix()
	out := make([]string, 0, len(tags)+1)
	for _, t := range tags {
		if !strings.HasPrefix(strings.ToLower(t), strings.ToLower(prefix)) {
			out = append(out, t)
		}
	}
	if provider = strings.TrimSpace(provider); provider != "" {
		out = append(out, prefix+provider)
	}
	return out
}

// tagProvider records provider on the item unless it is already recorded. A
// list that already holds it, and no other store, is left in the order the
// collector has it: withProviderTag puts the tag last, and a list compared in
// order was written back only to move it there.
func (p providerConfig) tagProvider(ctx context.Context, client *abs.Client, itemID string, tags []string, provider string) error {
	want := p.withProviderTag(tags, provider)
	have, next := slices.Clone(tags), slices.Clone(want)
	slices.Sort(have)
	slices.Sort(next)
	if slices.Equal(have, next) {
		return nil
	}
	_, err := client.UpdateMedia(ctx, itemID, abs.MediaUpdate{Tags: want})
	return err
}

// providerOrder puts the item's recorded provider first: the store that had
// the book last time is the one to ask first.
func (p providerConfig) providerOrder(it *abs.Item, providers []string) []string {
	recorded := p.providerTag(it)
	if recorded == "" || strings.EqualFold(recorded, providerNone) {
		return providers
	}
	out := []string{recorded}
	for _, q := range providers {
		if !strings.EqualFold(q, recorded) {
			out = append(out, q)
		}
	}
	return out
}

// tagOne records a store on one book, from its tags as they are once held
// rather than as a listing had them. The hold is let go however the call
// ends: one left behind by a panic would stall every later edit of the book.
func (r *registry) tagOne(ctx context.Context, itemID, provider string) error {
	release := r.locks.hold(itemKeys(itemID)...)
	defer release()
	fresh, err := r.client.Item(ctx, itemID)
	if err != nil {
		return err
	}
	return r.providerConfig().tagProvider(ctx, r.client, itemID, fresh.Media.Tags, provider)
}

// registerMatchTagTool adds item_match_tag, the backfill: books matched
// before the tag existed carry an asin and no record of where it came from.
func registerMatchTagTool(r *registry) {
	client := r.client
	prov := r.providerConfig()

	type tagIn struct {
		Library   string   `json:"library"             jsonschema:"library name or id"`
		Filter    string   `json:"filter,omitempty"    jsonschema:"which books, as library_items takes it: authors:Terry Pratchett, series:Discworld; default every book with an asin or isbn"`
		Providers []string `json:"providers,omitempty" jsonschema:"Audible stores to look each asin up at, in order; the first that has it is recorded, so put the store the books were bought from first: [audible.ca, audible]. Default the server's --providers, else the library's provider alone, which must then be an Audible store"`
		Limit     int      `json:"limit,omitempty"     jsonschema:"matched books per call, default 50, at most 100: each untagged book is a provider request"`
		Offset    int      `json:"offset,omitempty"    jsonschema:"skip this many matched books, counted in the order they were added: a previous call's next_offset"`
		Overwrite bool     `json:"overwrite,omitempty" jsonschema:"re-tag books that already carry a provider tag; default only books without one"`
	}
	type tagRow struct {
		ID       string `json:"id"`
		Title    string `json:"title"`
		ASIN     string `json:"asin,omitempty"`
		ISBN     string `json:"isbn,omitempty"`
		Provider string `json:"provider,omitempty" jsonschema:"the store recorded; empty when none had it"`
		Error    string `json:"error,omitempty"`
	}
	type tagOut struct {
		Checked    int      `json:"checked"               jsonschema:"matched books looked at: this call's window"`
		Tagged     int      `json:"tagged"`
		Already    int      `json:"already_tagged"        jsonschema:"left alone; overwrite re-tags them"`
		NotFound   int      `json:"not_found"             jsonschema:"no store in providers has the asin; these are listed with no provider"`
		Rows       []tagRow `json:"rows"                  jsonschema:"the books tagged or not found; already-tagged books are only counted"`
		NextOffset int      `json:"next_offset,omitempty" jsonschema:"pass back as offset for the next matched books; absent when this call reached the last"`
	}
	add(r, writeTool, &mcp.Tool{
		Name: "item_match_tag",
		Description: "Record which store each matched book's asin comes from, as the provider tag (zz-provider:audible.ca by default), for books matched before the tag existed. " +
			"Each asin is looked up at the providers in order and the first store that has it is recorded, so [audible.ca, audible] tags a book sold in both stores as Canadian. " +
			"Books that already carry the tag are left alone unless overwrite is set; a book no store has is listed with no provider so it can be looked at. One provider request per untagged book, so it works through limit matched books per call, in the order they were added: pass next_offset back as offset for the next. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in tagIn) (*mcp.CallToolResult, tagOut, error) {
		if !prov.providerTagging() {
			return nil, tagOut{}, errors.New("the provider tag is off (--provider-tag off); nothing to write")
		}
		lib, err := resolveLibrary(ctx, client, in.Library)
		if err != nil {
			return nil, tagOut{}, err
		}
		if lib.IsPodcast() {
			return nil, tagOut{}, errors.New("a podcast library has no matched books")
		}
		if err := prov.checkProviders(ctx, client, in.Providers, true); err != nil {
			return nil, tagOut{}, err
		}
		if err := prov.lookupRefusal(in.Providers, lib); err != nil {
			return nil, tagOut{}, err
		}
		providers := prov.providersFor(in.Providers, lib)
		limit, offset := min(limitOr(in.Limit, 50), 100), max(in.Offset, 0)
		var filter string
		if f := strings.TrimSpace(in.Filter); f != "" {
			if filter, err = buildFilter(ctx, client, lib, f); err != nil {
				return nil, tagOut{}, err
			}
		}
		var items []abs.Item
		more, err := matchedWindow(ctx, client, lib.ID, bookWindow{Filter: filter, Matched: hasASINOrISBN, Offset: offset, Limit: limit}, func(it *abs.Item) error {
			items = append(items, *it)
			return nil
		})
		if err != nil {
			return nil, tagOut{}, err
		}

		out := tagOut{Rows: []tagRow{}}
		for i := range items {
			it := &items[i]
			asin, isbn := strings.TrimSpace(it.Media.Metadata.ASIN), strings.TrimSpace(it.Media.Metadata.ISBN)
			out.Checked++
			if prov.providerTag(it) != "" && !in.Overwrite {
				out.Already++
				continue
			}
			row := tagRow{ID: it.ID, Title: it.Title(), ASIN: asin, ISBN: isbn}
			for _, provider := range providers {
				if _, err := providerRecord(ctx, client, it, provider, asin, isbn); err == nil {
					row.Provider = provider
					break
				}
			}
			if row.Provider == "" {
				out.NotFound++
			} else if err := r.tagOne(ctx, it.ID, row.Provider); err != nil {
				row.Error = err.Error()
			} else {
				out.Tagged++
			}
			out.Rows = append(out.Rows, row)
		}
		if more {
			out.NextOffset = offset + len(items)
		}
		return nil, out, nil
	})
}
