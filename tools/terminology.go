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

// vocabFields are the free-text fields worth normalizing. Every one of them is
// typed by hand or filled by an importer, so the same thing arrives spelled
// several ways: "Jim Dale" and "jim dale", "Sci-Fi" and "sci fi", "en" and
// "eng" and "English".
var vocabFields = []string{"genres", "tags", "narrators", "languages", "publishers", "authors"}

// languageAliases folds the ISO 639-1 and 639-2 codes and the English names of
// the languages a library is likely to hold onto one key, so that en, eng,
// English and english cluster together. Anything not here is compared on its
// normalized spelling alone and reported as unrecognized.
var languageAliases = map[string]string{
	"en": "en", "eng": "en", "english": "en",
	"de": "de", "ger": "de", "deu": "de", "german": "de", "deutsch": "de",
	"fr": "fr", "fra": "fr", "fre": "fr", "french": "fr", "francais": "fr",
	"es": "es", "spa": "es", "spanish": "es", "espanol": "es",
	"it": "it", "ita": "it", "italian": "it", "italiano": "it",
	"nl": "nl", "dut": "nl", "nld": "nl", "dutch": "nl", "nederlands": "nl",
	"pt": "pt", "por": "pt", "portuguese": "pt",
	"ru": "ru", "rus": "ru", "russian": "ru",
	"ja": "ja", "jpn": "ja", "japanese": "ja",
	"zh": "zh", "chi": "zh", "zho": "zh", "chinese": "zh", "mandarin": "zh",
	"sv": "sv", "swe": "sv", "swedish": "sv",
	"no": "no", "nor": "no", "norwegian": "no",
	"da": "da", "dan": "da", "danish": "da",
	"fi": "fi", "fin": "fi", "finnish": "fi",
	"pl": "pl", "pol": "pl", "polish": "pl",
	"cs": "cs", "cze": "cs", "ces": "cs", "czech": "cs",
	"la": "la", "lat": "la", "latin": "la",
}

// vocabKey is the value two spellings must share to count as the same thing.
func vocabKey(field, value string) string {
	n := norm(value)
	if field != "languages" {
		return n
	}
	if canonical, ok := languageAliases[n]; ok {
		return canonical
	}

	return n
}

// knownLanguage reports whether a language value is one this tool recognizes.
func knownLanguage(value string) bool {
	_, ok := languageAliases[norm(value)]
	return ok
}

// valuesOf pulls a field's values off one item.
func valuesOf(field string, it *abs.Item) []string {
	m := &it.Media.Metadata
	switch field {
	case "genres":
		return m.Genres
	case "tags":
		return it.Media.Tags
	case "narrators":
		return m.Narrators
	case "languages":
		if m.Language == "" {
			return nil
		}
		return []string{m.Language}
	case "publishers":
		if m.Publisher == "" {
			return nil
		}
		return []string{m.Publisher}
	case "authors":
		var out []string
		for _, a := range m.Authors {
			if a.Name != "" {
				out = append(out, a.Name)
			}
		}
		if len(out) == 0 && m.AuthorName != "" {
			out = strings.Split(m.AuthorName, ",")
		}
		return out
	}

	return nil
}

func registerTerminologyTools(r *registry) {
	client := r.client

	type spelling struct {
		Value string `json:"value"`
		Items int    `json:"items" jsonschema:"how many items carry this exact spelling"`
	}
	type vocabGroup struct {
		Field     string     `json:"field"`
		Keep      string     `json:"keep"      jsonschema:"the most used spelling, the obvious one to merge into"`
		Spellings []spelling `json:"spellings" jsonschema:"every spelling of the same value, most used first"`
	}
	type vocabIn struct {
		Library string `json:"library,omitempty" jsonschema:"library name or id; default every library"`
		Field   string `json:"field,omitempty"   jsonschema:"genres, tags, narrators, languages, publishers or authors; default all of them"`
		Limit   int    `json:"limit,omitempty"   jsonschema:"maximum groups to return, default 50"`
	}
	type vocabOut struct {
		Scanned      int          `json:"items_scanned"`
		Found        int          `json:"total_findings"                   jsonschema:"groups with more than one spelling, before limit"`
		Groups       []vocabGroup `json:"groups"`
		OddLanguages []spelling   `json:"unrecognized_languages,omitempty" jsonschema:"language values that are not a code or name this tool knows, e.g. a placeholder like XXX"`
	}

	add(r, readTool, &mcp.Tool{
		Name: "audit_terminology",
		Description: "Find values that mean the same thing but are spelled differently, across genres, tags, narrators, languages, publishers and authors: 'Jim Dale' and 'jim dale', 'Sci-Fi' and 'sci fi', 'en' and 'eng' and 'English'. " +
			"Fix narrators with narrator_edit, tags and genres with server_tag_rename, authors with author_edit, and languages or publishers with item_edit or item_batch_edit. " +
			"Language values that are not a code or name this tool recognizes are reported separately, which is how placeholders like XXX surface.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in vocabIn) (*mcp.CallToolResult, vocabOut, error) {
		fields := vocabFields
		if f := strings.ToLower(strings.TrimSpace(in.Field)); f != "" && f != "all" {
			if !slices.Contains(vocabFields, f) {
				return nil, vocabOut{}, fmt.Errorf("unknown field %q; choose one of: %s", in.Field, strings.Join(vocabFields, ", "))
			}
			fields = []string{f}
		}

		libs, err := resolveLibraries(ctx, client, in.Library)
		if err != nil {
			return nil, vocabOut{}, err
		}

		// field -> key -> spelling -> count. The sweep is the reliable source:
		// the server's own filter data is cached and is not invalidated by an
		// edit or a rescan, so it goes stale as soon as anything is fixed.
		counts := map[string]map[string]map[string]int{}
		for _, f := range fields {
			counts[f] = map[string]map[string]int{}
		}

		out := vocabOut{Groups: []vocabGroup{}}
		for i := range libs {
			if err := client.ItemsAll(ctx, libs[i].ID, abs.ItemsOptions{}, func(items []abs.Item) bool {
				for j := range items {
					out.Scanned++
					for _, f := range fields {
						for _, v := range valuesOf(f, &items[j]) {
							v = strings.TrimSpace(v)
							if v == "" {
								continue
							}
							k := vocabKey(f, v)
							if k == "" {
								continue
							}
							if counts[f][k] == nil {
								counts[f][k] = map[string]int{}
							}
							counts[f][k][v]++
						}
					}
				}
				return true
			}); err != nil {
				return nil, vocabOut{}, err
			}
		}

		limit := limitOr(in.Limit, 50)
		for _, f := range fields {
			keys := make([]string, 0, len(counts[f]))
			for k, spellings := range counts[f] {
				if len(spellings) > 1 {
					keys = append(keys, k)
				}
			}
			slices.Sort(keys)

			for _, k := range keys {
				out.Found++
				if len(out.Groups) >= limit {
					continue
				}
				g := vocabGroup{Field: f}
				for v, n := range counts[f][k] {
					g.Spellings = append(g.Spellings, spelling{Value: v, Items: n})
				}
				// most used first, then alphabetical so the output is stable
				slices.SortFunc(g.Spellings, func(a, b spelling) int {
					if a.Items != b.Items {
						return b.Items - a.Items
					}
					return strings.Compare(a.Value, b.Value)
				})
				g.Keep = g.Spellings[0].Value
				out.Groups = append(out.Groups, g)
			}
		}

		// a language nobody recognizes is usually a placeholder rather than a
		// spelling variant, so it needs looking at rather than merging
		if slices.Contains(fields, "languages") {
			for _, spellings := range counts["languages"] {
				for v, n := range spellings {
					if !knownLanguage(v) {
						out.OddLanguages = append(out.OddLanguages, spelling{Value: v, Items: n})
					}
				}
			}
			slices.SortFunc(out.OddLanguages, func(a, b spelling) int {
				return strings.Compare(a.Value, b.Value)
			})
		}

		return nil, out, nil
	})

	type renameIn struct {
		Library string `json:"library,omitempty" jsonschema:"library name or id; default every library"`
		Field   string `json:"field"             jsonschema:"languages or publishers; narrators use narrator_edit, tags and genres server_tag_rename, authors author_edit"`
		From    string `json:"from"              jsonschema:"the spelling to replace, exactly as audit_terminology reports it"`
		To      string `json:"to"                jsonschema:"the spelling to keep"`
	}
	type renameOut struct {
		ItemsUpdated int      `json:"items_updated"`
		Items        []string `json:"items,omitempty" jsonschema:"the titles changed, capped at 50"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "audit_terminology_rename",
		Description: "Replace one spelling of a language or publisher with another across every item that carries it, which is how a group from audit_terminology gets merged. Narrators, tags, genres and authors have their own rename tools and are refused here. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in renameIn) (*mcp.CallToolResult, renameOut, error) {
		field := strings.ToLower(strings.TrimSpace(in.Field))
		if field != "languages" && field != "publishers" {
			return nil, renameOut{}, errors.New("field must be languages or publishers; use narrator_edit, server_tag_rename or author_edit for the others")
		}
		from, to := strings.TrimSpace(in.From), strings.TrimSpace(in.To)
		if from == "" || to == "" {
			return nil, renameOut{}, errors.New("from and to are required")
		}

		libs, err := resolveLibraries(ctx, client, in.Library)
		if err != nil {
			return nil, renameOut{}, err
		}

		var updates []abs.BatchMediaUpdate
		out := renameOut{Items: []string{}}
		for i := range libs {
			if err := client.ItemsAll(ctx, libs[i].ID, abs.ItemsOptions{}, func(items []abs.Item) bool {
				for j := range items {
					it := &items[j]
					m := &it.Media.Metadata
					var carries bool
					switch field {
					case "languages":
						carries = strings.EqualFold(strings.TrimSpace(m.Language), from)
					case "publishers":
						carries = strings.EqualFold(strings.TrimSpace(m.Publisher), from)
					}
					if !carries {
						continue
					}

					md := abs.MetadataUpdate{}
					if field == "languages" {
						md.Language = &to
					} else {
						md.Publisher = &to
					}
					updates = append(updates, abs.BatchMediaUpdate{
						ID: it.ID, MediaPayload: abs.MediaUpdate{Metadata: &md},
					})
					if len(out.Items) < 50 {
						out.Items = append(out.Items, it.Title())
					}
				}
				return true
			}); err != nil {
				return nil, renameOut{}, err
			}
		}

		if len(updates) == 0 {
			return nil, renameOut{}, fmt.Errorf("nothing carries %s %q", field, from)
		}

		n, err := client.BatchUpdate(ctx, updates)
		if err != nil {
			return nil, renameOut{}, err
		}
		out.ItemsUpdated = n

		return nil, out, nil
	})
}
