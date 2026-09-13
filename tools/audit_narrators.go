package tools

import (
	"context"
	"slices"
	"strings"

	"github.com/katbyte/abs-mcp/lib/abs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// audit_narrators is everything wrong with the narrator field, in two parts.
//
// Roles: a name that wrote some books in the library and read others. Authors
// who narrate their own work are ordinary and are not reported; a name that
// is the author of one volume of a series and the narrator of the rest is the
// two fields swapped on import (four light novels arrived that way in one
// batch), and a narrator listed as a co-author is the other common shape.
//
// Names: the spelling detectors, run on narrators only. A list of four hundred
// narrators held "Narrator..........Sean Barrett" beside "Sean Barrett", a
// "Fajer Al" cut off from "Fajer Al-Kaisi", "Read by" on four names, two names
// in one field and a stray "Ph.D.", and audit_spelling saw none of it.

type roleItem struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// roleCounts gathers, per normalized name, which items list it as an author
// and which as a narrator.
type roleCounts struct {
	display  map[string]string
	author   map[string]map[string]roleItem
	narrator map[string]map[string]roleItem
}

func newRoleCounts() *roleCounts {
	return &roleCounts{
		display:  map[string]string{},
		author:   map[string]map[string]roleItem{},
		narrator: map[string]map[string]roleItem{},
	}
}

func (c *roleCounts) add(it *abs.Item) {
	ref := roleItem{ID: it.ID, Title: it.Media.Metadata.Title}
	note := func(into map[string]map[string]roleItem, names []string) {
		for _, name := range names {
			name = strings.TrimSpace(name)
			k := norm(name)
			if k == "" {
				continue
			}
			if _, ok := c.display[k]; !ok {
				c.display[k] = name
			}
			if into[k] == nil {
				into[k] = map[string]roleItem{}
			}
			into[k][it.ID] = ref
		}
	}
	note(c.author, valuesOf("authors", it))
	note(c.narrator, valuesOf("narrators", it))
}

type roleFinding struct {
	Name     string     `json:"name"`
	Written  int        `json:"books_written"   jsonschema:"items that list the name as an author"`
	ReadOnly int        `json:"books_read_only" jsonschema:"items that list the name as a narrator and not as an author; a book they both wrote and read does not count"`
	Wrote    []roleItem `json:"wrote"           jsonschema:"the written items, up to 5"`
	Read     []roleItem `json:"read"            jsonschema:"the read-only items, up to 10. The same series on both lists means author and narrator are swapped: fix with item_edit"`
}

// findings is every name in both roles with at least one book it only read,
// the fewest written first: one book written and several read is the swap.
func (c *roleCounts) findings() []roleFinding {
	var out []roleFinding
	for k, wrote := range c.author {
		read := c.narrator[k]
		if len(read) == 0 {
			continue
		}
		f := roleFinding{Name: c.display[k], Written: len(wrote)}
		for id, it := range read {
			if _, self := wrote[id]; !self {
				f.ReadOnly++
				f.Read = append(f.Read, it)
			}
		}
		if f.ReadOnly == 0 {
			continue
		}
		for _, it := range wrote {
			f.Wrote = append(f.Wrote, it)
		}
		sortRoleItems(f.Wrote)
		sortRoleItems(f.Read)
		f.Wrote = f.Wrote[:min(len(f.Wrote), 5)]
		f.Read = f.Read[:min(len(f.Read), 10)]
		out = append(out, f)
	}
	slices.SortFunc(out, func(a, b roleFinding) int {
		if a.Written != b.Written {
			return a.Written - b.Written
		}
		if a.ReadOnly != b.ReadOnly {
			return b.ReadOnly - a.ReadOnly
		}
		return strings.Compare(a.Name, b.Name)
	})
	return out
}

func sortRoleItems(items []roleItem) {
	slices.SortFunc(items, func(a, b roleItem) int {
		if a.Title != b.Title {
			return strings.Compare(a.Title, b.Title)
		}
		return strings.Compare(a.ID, b.ID)
	})
}

func registerNarratorAudit(r *registry) {
	client := r.client

	type rolesIn struct {
		Library string `json:"library,omitempty" jsonschema:"library name or id; default every library"`
		Limit   int    `json:"limit,omitempty"   jsonschema:"maximum findings, default 50"`
	}
	type rolesOut struct {
		Scanned int           `json:"items_scanned"`
		Found   int           `json:"total_findings" jsonschema:"roles and names together, before limit"`
		Roles   []roleFinding `json:"roles"          jsonschema:"names that are an author on some books and a narrator on others"`
		Names   []vocabGroup  `json:"names"          jsonschema:"narrator spellings to merge, remove or split, each with its kind, as audit_spelling reports for other fields"`
	}
	add(r, readTool, &mcp.Tool{
		Name: "audit_narrators",
		Description: "Everything wrong with the narrator field. Roles: names that are an author on some books and a narrator on others (an author reading their own book is not reported); one volume written and the rest of the series read is the author and narrator fields swapped on import, one book written and many read is a narrator credited as co-author - fix with item_edit, then author_delete the record if it is left with no books. " +
			"Names: the same value spelled several ways, a wrapper an importer left on ('Read by Jim Dale', 'Narrator....Jim Dale'), one name that is another cut short or without its initials ('Fajer Al' and 'Fajer Al-Kaisi'), two names a typo apart (read both: it can be two people), two names in one value, and leftovers such as 'Ph.D.' - fix with metadata_rename field=narrators, or item_edit for a split.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in rolesIn) (*mcp.CallToolResult, rolesOut, error) {
		libs, err := resolveLibraries(ctx, client, in.Library)
		if err != nil {
			return nil, rolesOut{}, err
		}
		roles := newRoleCounts()
		names := newSpellingCounts([]string{"narrators"})
		out := rolesOut{Roles: []roleFinding{}, Names: []vocabGroup{}}
		for i := range libs {
			if err := client.ItemsAll(ctx, libs[i].ID, abs.ItemsOptions{}, func(items []abs.Item) bool {
				for j := range items {
					out.Scanned++
					roles.add(&items[j])
					names.add(&items[j])
				}
				return true
			}); err != nil {
				return nil, rolesOut{}, err
			}
		}
		allRoles, allNames := roles.findings(), names.report("narrators")
		out.Found = len(allRoles) + len(allNames)
		limit := limitOr(in.Limit, 50)
		out.Roles = append(out.Roles, allRoles[:min(len(allRoles), limit)]...)
		out.Names = append(out.Names, allNames[:min(len(allNames), limit-len(out.Roles))]...)

		return nil, out, nil
	})
}
