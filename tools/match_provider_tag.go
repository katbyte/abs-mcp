package tools

import (
	"context"
	"errors"
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

// providerTagPrefix is the tag prefix in use, set from Options.ProviderTag at
// registration: a collector who wants it read as a word can make it
// "provider:", and one who wants none at all can turn it off.
var providerTagPrefix = defaultProviderTag

// defaultProviders is the order of stores to ask when a call names none, set
// from Options.Providers; empty means the library's own provider.
var defaultProviders []string

// providersFor returns the providers a call should ask, in order: the ones
// it named, else the configured default, else the library's own.
func providersFor(named []string, lib *abs.Library) []string {
	if len(named) > 0 {
		return named
	}
	if len(defaultProviders) > 0 {
		return slices.Clone(defaultProviders)
	}
	return []string{lib.Provider}
}

// providerTagging reports whether the tag is in use.
func providerTagging() bool { return providerTagPrefix != providerTagOff && providerTagPrefix != "" }

// providerTag returns the provider recorded on the item, "none", or "".
func providerTag(it *abs.Item) string {
	if !providerTagging() {
		return ""
	}
	for _, t := range it.Media.Tags {
		if strings.HasPrefix(strings.ToLower(t), strings.ToLower(providerTagPrefix)) {
			return strings.TrimSpace(t[len(providerTagPrefix):])
		}
	}
	return ""
}

// markedUnmatchable reports whether the collector has said there is nothing
// to match this book to.
func markedUnmatchable(it *abs.Item) bool {
	return strings.EqualFold(providerTag(it), providerNone)
}

// withProviderTag returns tags with the provider tag set to provider and any
// earlier provider tag dropped.
func withProviderTag(tags []string, provider string) []string {
	if !providerTagging() {
		return tags
	}
	out := make([]string, 0, len(tags)+1)
	for _, t := range tags {
		if !strings.HasPrefix(strings.ToLower(t), strings.ToLower(providerTagPrefix)) {
			out = append(out, t)
		}
	}
	if provider = strings.TrimSpace(provider); provider != "" {
		out = append(out, providerTagPrefix+provider)
	}
	return out
}

// tagProvider records provider on the item unless it is already recorded.
func tagProvider(ctx context.Context, client *abs.Client, itemID string, tags []string, provider string) error {
	want := withProviderTag(tags, provider)
	if slices.Equal(want, tags) {
		return nil
	}
	_, err := client.UpdateMedia(ctx, itemID, abs.MediaUpdate{Tags: want})
	return err
}

// providerOrder puts the item's recorded provider first: the store that had
// the book last time is the one to ask first.
func providerOrder(it *abs.Item, providers []string) []string {
	p := providerTag(it)
	if p == "" || strings.EqualFold(p, providerNone) {
		return providers
	}
	out := []string{p}
	for _, q := range providers {
		if !strings.EqualFold(q, p) {
			out = append(out, q)
		}
	}
	return out
}

// registerMatchTagTool adds item_match_tag, the backfill: books matched
// before the tag existed carry an asin and no record of where it came from.
func registerMatchTagTool(r *registry) {
	client := r.client

	type tagIn struct {
		Library   string   `json:"library"             jsonschema:"library name or id"`
		Filter    string   `json:"filter,omitempty"    jsonschema:"which books, as library_items takes it: authors:Terry Pratchett, series:Discworld; default every book with an asin or isbn"`
		Providers []string `json:"providers,omitempty" jsonschema:"stores to look each asin up at, in order; the first that has it is recorded, so put the store the books were bought from first: [audible.ca, audible]. Default the server's --providers, else the library's provider alone"`
		Limit     int      `json:"limit,omitempty"     jsonschema:"books per call, default 50, at most 100: each untagged book is a provider request"`
		Page      int      `json:"page,omitempty"      jsonschema:"0-based page; next_page says when there is more"`
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
		Checked  int      `json:"checked"             jsonschema:"matched books looked at"`
		Tagged   int      `json:"tagged"`
		Already  int      `json:"already_tagged"      jsonschema:"left alone; overwrite re-tags them"`
		NotFound int      `json:"not_found"           jsonschema:"no store in providers has the asin; these are listed with no provider"`
		Rows     []tagRow `json:"rows"                jsonschema:"the books tagged or not found; already-tagged books are only counted"`
		NextPage *int     `json:"next_page,omitempty"`
	}
	add(r, writeTool, &mcp.Tool{
		Name: "item_match_tag",
		Description: "Record which store each matched book's asin comes from, as the " + defaultProviderTag + " tag, for books matched before the tag existed. " +
			"Each asin is looked up at the providers in order and the first store that has it is recorded, so [audible.ca, audible] tags a book sold in both stores as Canadian. " +
			"Books that already carry the tag are left alone unless overwrite is set; a book no store has is listed with no provider so it can be looked at. One provider request per untagged book, so it pages. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in tagIn) (*mcp.CallToolResult, tagOut, error) {
		if !providerTagging() {
			return nil, tagOut{}, errors.New("the provider tag is off (--provider-tag off); nothing to write")
		}
		lib, err := resolveLibrary(ctx, client, in.Library)
		if err != nil {
			return nil, tagOut{}, err
		}
		if lib.IsPodcast() {
			return nil, tagOut{}, errors.New("a podcast library has no matched books")
		}
		providers := providersFor(in.Providers, lib)
		limit := min(limitOr(in.Limit, 50), 100)
		filter := strings.TrimSpace(in.Filter)
		var items []abs.Item
		var total int
		if filter != "" {
			encoded, ferr := buildFilter(ctx, client, lib, filter)
			if ferr != nil {
				return nil, tagOut{}, ferr
			}
			page, perr := client.Items(ctx, lib.ID, abs.ItemsOptions{Limit: limit, Page: max(in.Page, 0), Sort: "media.metadata.title", Filter: encoded, Minified: true})
			if perr != nil {
				return nil, tagOut{}, perr
			}
			items, total = page.Results, page.Total
		} else {
			// the server has no "has an asin or isbn" filter: sweep and page by hand
			var matched []abs.Item
			if err := client.ItemsAll(ctx, lib.ID, abs.ItemsOptions{}, func(page []abs.Item) bool {
				for i := range page {
					if it := &page[i]; !it.IsPodcast() && (strings.TrimSpace(it.Media.Metadata.ASIN) != "" || strings.TrimSpace(it.Media.Metadata.ISBN) != "") {
						matched = append(matched, *it)
					}
				}
				return true
			}); err != nil {
				return nil, tagOut{}, err
			}
			total = len(matched)
			start := min(max(in.Page, 0)*limit, total)
			items = matched[start:min(start+limit, total)]
		}

		out := tagOut{Rows: []tagRow{}}
		for i := range items {
			it := &items[i]
			asin, isbn := strings.TrimSpace(it.Media.Metadata.ASIN), strings.TrimSpace(it.Media.Metadata.ISBN)
			if it.IsPodcast() || (asin == "" && isbn == "") {
				continue
			}
			out.Checked++
			if providerTag(it) != "" && !in.Overwrite {
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
			} else if err := tagProvider(ctx, client, it.ID, it.Media.Tags, row.Provider); err != nil {
				row.Error = err.Error()
			} else {
				out.Tagged++
			}
			out.Rows = append(out.Rows, row)
		}
		if (max(in.Page, 0)+1)*limit < total {
			out.NextPage = new(max(in.Page, 0) + 1)
		}
		return nil, out, nil
	})
}
