package tools

import (
	"context"
	"regexp"
	"slices"
	"strings"

	"github.com/katbyte/abs-mcp/lib/abs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// audit_genres: genres and tags against one rule. Genres hold a short list
// of broad categories a reader browses by, a handful of values across the
// whole library; tags hold everything finer and the collector's own markers.
// Nothing in Audiobookshelf enforces that, and an importer fills both fields
// from the same tree, so what accumulates is "Audiobook" on 457 books, a
// category path written as one value ("Science Fiction & Fantasy, Fantasy"),
// a genre carried by one book, and a tag that repeats the genre beside it.
// Spelling variants are audit_spelling's and are not repeated here.

var (
	// a value that says nothing about the book
	genrePlaceholder = regexp.MustCompile(`(?i)^(audio ?books?|books?|unknown|other|general|vocal|misc(ellaneous)?|none|n/a|default)$`)
	// "Audiobook - Fantasy": a placeholder glued to a real value
	genrePlaceholderPrefix = regexp.MustCompile(`(?i)^audio ?books?\s*[-:/,]\s*`)
	// what joins the parts of a compound value
	genreJoin = regexp.MustCompile(`\s*,\s*|\s*/\s*|\s*:\s*|\s*;\s*`)
	// a marker rather than a subject: "zz-provider:audible.ca"
	genreMarker = regexp.MustCompile(`^[a-z0-9_-]+:`)
)

type genreRef struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

type genreUse struct {
	value string
	items int
	refs  []genreRef
}

type genreValue struct {
	Field   string     `json:"field"`
	Value   string     `json:"value"`
	Items   int        `json:"items"`
	Sample  []genreRef `json:"sample,omitempty"  jsonschema:"up to three of the items"`
	Suggest string     `json:"suggest,omitempty" jsonschema:"what to do: the metadata_rename call, or the value to keep"`
}

type genreCompound struct {
	Field   string         `json:"field"`
	Value   string         `json:"value"`
	Items   int            `json:"items"`
	Parts   []genrePart    `json:"parts"            jsonschema:"the value split at commas, slashes or colons; known says whether the part is already a value on its own, which is how a category path written as one value differs from a category whose name has a comma"`
	Sample  []genreRef     `json:"sample,omitempty"`
	Suggest genreSplitPlan `json:"suggest"          jsonschema:"pass as split to metadata_rename field=<field> from=<value>: the first part is the broad category and stays where it is, the rest are finer and go to tags, on these books only"`
}

type genrePart struct {
	Value string `json:"value"`
	Known bool   `json:"known"`
}

type genreSplitPlan struct {
	Genres []string `json:"genres,omitempty"`
	Tags   []string `json:"tags,omitempty"`
}

type genresCounts struct {
	Books           int `json:"books"`
	DistinctGenres  int `json:"distinct_genres"`
	DistinctTags    int `json:"distinct_tags"`
	NoGenres        int `json:"no_genres"        jsonschema:"books with no genre at all"`
	PlaceholderOnly int `json:"placeholder_only" jsonschema:"books whose only genres are placeholders: as good as none"`
	Placeholders    int `json:"placeholders"`
	Compound        int `json:"compound"`
	Narrow          int `json:"narrow"`
	Redundant       int `json:"redundant"`
	Markers         int `json:"markers"`
}

type genresOut struct {
	Scanned      int             `json:"items_scanned"`
	Found        int             `json:"total_findings"`
	Counts       genresCounts    `json:"counts"`
	Placeholders []genreValue    `json:"placeholders"   jsonschema:"values that say nothing (Audiobook, Audio Book, Vocal, Unknown) in either field: drop with metadata_rename remove, or when a real value is glued on (Audiobook - Fantasy) rename to it"`
	Compound     []genreCompound `json:"compound"       jsonschema:"one value holding several, joined by a comma, slash or colon"`
	Narrow       []genreValue    `json:"narrow"         jsonschema:"genres on fewer books than min_items: too specific for a genre, move to tags with metadata_rename to_field=tags"`
	Redundant    []genreValue    `json:"redundant"      jsonschema:"tags that repeat one of the same book's genres: drop the tag with metadata_rename remove"`
	Markers      []string        `json:"markers"        jsonschema:"tags that are markers rather than subjects (a prefix and a colon); listed so they are seen, not judged"`
	NoGenres     []genreRef      `json:"no_genres"      jsonschema:"up to limit of the books with no genre"`
}

// genresCollector gathers every genre and tag with the books that carry it.
type genresCollector struct {
	genres, tags map[string]*genreUse
	redundant    map[string]*genreUse
	noGenres     []genreRef
	placeholder  int
	books        int
}

func newGenresCollector() *genresCollector {
	return &genresCollector{genres: map[string]*genreUse{}, tags: map[string]*genreUse{}, redundant: map[string]*genreUse{}}
}

func note(into map[string]*genreUse, value string, ref genreRef) {
	u := into[value]
	if u == nil {
		u = &genreUse{value: value}
		into[value] = u
	}
	u.items++
	if len(u.refs) < 3 {
		u.refs = append(u.refs, ref)
	}
}

func (c *genresCollector) add(it *abs.Item) {
	if it.IsPodcast() {
		return
	}
	c.books++
	ref := genreRef{ID: it.ID, Title: it.Title()}
	named := 0
	for _, g := range it.Media.Metadata.Genres {
		if g = strings.TrimSpace(g); g == "" {
			continue
		}
		note(c.genres, g, ref)
		if !genrePlaceholder.MatchString(g) {
			named++
		}
	}
	switch {
	case len(it.Media.Metadata.Genres) == 0:
		c.noGenres = append(c.noGenres, ref)
	case named == 0:
		c.placeholder++
	}
	for _, t := range it.Media.Tags {
		if t = strings.TrimSpace(t); t == "" {
			continue
		}
		note(c.tags, t, ref)
		if slices.ContainsFunc(it.Media.Metadata.Genres, func(g string) bool { return strings.EqualFold(strings.TrimSpace(g), t) }) {
			note(c.redundant, t, ref)
		}
	}
}

func (c *genresCollector) findings(minItems, limit int) genresOut {
	out := genresOut{
		Scanned: c.books, Placeholders: []genreValue{}, Compound: []genreCompound{}, Narrow: []genreValue{},
		Redundant: []genreValue{}, Markers: []string{}, NoGenres: []genreRef{},
		Counts: genresCounts{Books: c.books, DistinctGenres: len(c.genres), DistinctTags: len(c.tags), NoGenres: len(c.noGenres), PlaceholderOnly: c.placeholder},
	}
	known := func(v string) bool {
		_, g := c.genres[v]
		_, t := c.tags[v]
		return g || t
	}
	for _, field := range []string{"genres", "tags"} {
		uses := c.genres
		if field == "tags" {
			uses = c.tags
		}
		values := make([]*genreUse, 0, len(uses))
		for _, u := range uses {
			values = append(values, u)
		}
		slices.SortFunc(values, func(a, b *genreUse) int {
			if a.items != b.items {
				return b.items - a.items
			}
			return strings.Compare(a.value, b.value)
		})
		for _, u := range values {
			v := u.value
			switch {
			case field == "tags" && genreMarker.MatchString(v):
				out.Markers = append(out.Markers, v)
			case genrePlaceholder.MatchString(v):
				out.Placeholders = append(out.Placeholders, genreValue{
					Field: field, Value: v, Items: u.items, Sample: u.refs,
					Suggest: "metadata_rename field=" + field + " from=" + quote(v) + " remove=true",
				})
			case genrePlaceholderPrefix.MatchString(v):
				rest := strings.TrimSpace(genrePlaceholderPrefix.ReplaceAllString(v, ""))
				out.Placeholders = append(out.Placeholders, genreValue{
					Field: field, Value: v, Items: u.items, Sample: u.refs,
					Suggest: "metadata_rename field=" + field + " from=" + quote(v) + " to=" + quote(rest),
				})
			case len(genreJoin.Split(v, -1)) > 1:
				parts := genreJoin.Split(v, -1)
				comp := genreCompound{Field: field, Value: v, Items: u.items, Sample: u.refs}
				for _, p := range parts {
					if p = strings.TrimSpace(p); p != "" {
						comp.Parts = append(comp.Parts, genrePart{Value: p, Known: known(p)})
					}
				}
				if field == "genres" && len(comp.Parts) > 0 {
					comp.Suggest.Genres = []string{comp.Parts[0].Value}
					for _, p := range comp.Parts[1:] {
						comp.Suggest.Tags = append(comp.Suggest.Tags, p.Value)
					}
				} else {
					for _, p := range comp.Parts {
						comp.Suggest.Tags = append(comp.Suggest.Tags, p.Value)
					}
				}
				out.Compound = append(out.Compound, comp)
			case field == "genres" && u.items < minItems:
				out.Narrow = append(out.Narrow, genreValue{
					Field: field, Value: v, Items: u.items, Sample: u.refs,
					Suggest: "metadata_rename field=genres from=" + quote(v) + " to_field=tags",
				})
			}
		}
	}
	redundant := make([]*genreUse, 0, len(c.redundant))
	for _, u := range c.redundant {
		redundant = append(redundant, u)
	}
	slices.SortFunc(redundant, func(a, b *genreUse) int { return b.items - a.items })
	for _, u := range redundant {
		out.Redundant = append(out.Redundant, genreValue{
			Field: "tags", Value: u.value, Items: u.items, Sample: u.refs,
			Suggest: "metadata_rename field=tags from=" + quote(u.value) + " remove=true",
		})
	}
	slices.Sort(out.Markers)
	out.Counts.Placeholders, out.Counts.Compound, out.Counts.Narrow = len(out.Placeholders), len(out.Compound), len(out.Narrow)
	out.Counts.Redundant, out.Counts.Markers = len(out.Redundant), len(out.Markers)
	out.NoGenres = c.noGenres[:min(len(c.noGenres), limit)]
	out.Found = out.Counts.Placeholders + out.Counts.Compound + out.Counts.Narrow + out.Counts.Redundant + len(c.noGenres)
	return out
}

func quote(s string) string {
	if strings.ContainsAny(s, " ,:/") {
		return `"` + s + `"`
	}
	return s
}

func registerGenresAudit(r *registry) {
	client := r.client

	type genresIn struct {
		Library  string `json:"library,omitempty"   jsonschema:"library name or id; default every book library"`
		MinItems int    `json:"min_items,omitempty" jsonschema:"a genre on fewer books than this is too narrow to be a genre; default 5"`
		Limit    int    `json:"limit,omitempty"     jsonschema:"books with no genre to list, default 50"`
	}
	add(r, readTool, &mcp.Tool{
		Name: "audit_genres",
		Description: "Check genres and tags against one rule: genres are a short list of broad categories to browse by, tags hold everything finer and the collector's own markers. " +
			"Reports placeholders that say nothing (Audiobook, Audio Book, Vocal) in either field; compound values that are a category path written as one (\"Science Fiction & Fantasy, Fantasy\"), with each part and whether it already exists on its own; " +
			"genres carried by fewer books than min_items, which belong in tags; tags that repeat one of the same book's genres; books with no genre; and marker tags, listed but not judged. " +
			"Every finding carries the metadata_rename call that fixes it: remove drops a value, to merges, to_field moves a value between genres and tags, and a compound value's suggest is passed as split. Spelling variants are audit_spelling's.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in genresIn) (*mcp.CallToolResult, genresOut, error) {
		libs, err := resolveLibraries(ctx, client, in.Library)
		if err != nil {
			return nil, genresOut{}, err
		}
		c := newGenresCollector()
		for i := range libs {
			if libs[i].IsPodcast() {
				continue
			}
			if err := client.ItemsAll(ctx, libs[i].ID, abs.ItemsOptions{}, func(items []abs.Item) bool {
				for j := range items {
					c.add(&items[j])
				}
				return true
			}); err != nil {
				return nil, genresOut{}, err
			}
		}
		return nil, c.findings(limitOr(in.MinItems, 5), limitOr(in.Limit, 50)), nil
	})
}
