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

// resolveAuthor finds an author by id or name, searching the named library or
// all of them.
// authorResolveLimit caps the author listing a name lookup pages through.
const authorResolveLimit = 500

func resolveAuthor(ctx context.Context, client *abs.Client, library, nameOrID string) (*abs.Author, error) {
	nameOrID = strings.TrimSpace(nameOrID)
	if nameOrID == "" {
		return nil, errors.New("author id or name is required")
	}
	if looksLikeID(nameOrID) {
		return client.Author(ctx, nameOrID, true)
	}

	libs, err := resolveLibraries(ctx, client, library)
	if err != nil {
		return nil, err
	}
	var candidates []abs.Author
	for i := range libs {
		// the live authors endpoint, not FilterData: the server caches filter
		// data and does not invalidate it on an edit or a scan, so a renamed
		// author would not be found there
		authors, _, err := client.Authors(ctx, libs[i].ID, abs.ListOptions{Limit: authorResolveLimit})
		if err != nil {
			return nil, err
		}
		for _, a := range authors {
			if strings.EqualFold(a.Name, nameOrID) {
				full, err := client.Author(ctx, a.ID, true)
				if err != nil {
					return nil, err
				}
				candidates = append(candidates, *full)
			}
		}
	}
	switch len(candidates) {
	case 1:
		return &candidates[0], nil
	case 0:
		return nil, fmt.Errorf("no author named %q (library_search finds partial names)", nameOrID)
	}
	ids := make([]string, 0, len(candidates))
	for _, c := range candidates {
		ids = append(ids, fmt.Sprintf("%s in library %s", c.ID, c.LibraryID))
	}

	return nil, fmt.Errorf("%q exists in several libraries; pass an id: %s", nameOrID, strings.Join(ids, "; "))
}

// resolveSeries finds a series by id or name in the named library or all of
// them.
func resolveSeries(ctx context.Context, client *abs.Client, library, nameOrID string) (*abs.Series, error) {
	nameOrID = strings.TrimSpace(nameOrID)
	if nameOrID == "" {
		return nil, errors.New("series id or name is required")
	}
	if looksLikeID(nameOrID) {
		return client.Series(ctx, nameOrID)
	}

	libs, err := resolveLibraries(ctx, client, library)
	if err != nil {
		return nil, err
	}
	var found []string
	for i := range libs {
		fd, err := client.FilterData(ctx, libs[i].ID)
		if err != nil {
			return nil, err
		}
		for _, s := range fd.Series {
			if strings.EqualFold(s.Name, nameOrID) {
				found = append(found, s.ID)
			}
		}
	}
	switch len(found) {
	case 1:
		return client.Series(ctx, found[0])
	case 0:
		return nil, fmt.Errorf("no series named %q (library_search finds partial names)", nameOrID)
	}

	return nil, fmt.Errorf("%q exists in several libraries; pass an id: %s", nameOrID, strings.Join(found, ", "))
}

type authorRow struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Books       int    `json:"books"`
	ASIN        string `json:"asin,omitempty"`
	HasImage    bool   `json:"has_image"`
	Description string `json:"description,omitempty"`
}

func authorRowOf(a *abs.Author, withDescription bool) authorRow {
	row := authorRow{ID: a.ID, Name: a.Name, Books: a.NumBooks, ASIN: a.ASIN, HasImage: a.ImagePath != ""}
	if row.Books == 0 {
		row.Books = len(a.LibraryItems)
	}
	if withDescription {
		row.Description = clip(plain(a.Description), descriptionCap)
	}
	return row
}

func registerAuthorTools(r *registry) {
	client := r.client

	type listIn struct {
		Library string `json:"library,omitempty" jsonschema:"library name or id; optional when the server has one library"`
		Sort    string `json:"sort,omitempty"    jsonschema:"name (default), books, added, updated"`
		Desc    bool   `json:"desc,omitempty"`
		Limit   int    `json:"limit,omitempty"   jsonschema:"page size, default 50"`
		Offset  int    `json:"offset,omitempty"`
	}
	type listOut struct {
		Total   int         `json:"total"`
		Authors []authorRow `json:"authors"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "author_list",
		Description: "List a library's authors with book counts, sortable by name or number of books. Authors without an asin or image are candidates for author_match.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listIn) (*mcp.CallToolResult, listOut, error) {
		lib, err := resolveLibrary(ctx, client, in.Library)
		if err != nil {
			return nil, listOut{}, err
		}
		sortBy := map[string]string{"": "name", "name": "name", "books": "numBooks", "added": "addedAt", "updated": "updatedAt", "last_first": "lastFirst"}[strings.ToLower(in.Sort)]
		if sortBy == "" {
			sortBy = in.Sort
		}
		limit := limitOr(in.Limit, 50)
		authors, total, err := client.Authors(ctx, lib.ID, abs.ListOptions{Limit: limit, Page: in.Offset / limit, Sort: sortBy, Desc: in.Desc})
		if err != nil {
			return nil, listOut{}, err
		}

		out := listOut{Total: total, Authors: []authorRow{}}
		for i := range authors {
			out.Authors = append(out.Authors, authorRowOf(&authors[i], false))
		}

		return nil, out, nil
	})

	type authorIn struct {
		Author  string `json:"author"            jsonschema:"author id or exact name"`
		Library string `json:"library,omitempty" jsonschema:"narrow a name lookup to one library"`
	}
	type getOut struct {
		authorRow
		Books []itemSummary `json:"books"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "author_get",
		Description: "One author with their description, asin and every book of theirs in the library.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in authorIn) (*mcp.CallToolResult, getOut, error) {
		a, err := resolveAuthor(ctx, client, in.Library, in.Author)
		if err != nil {
			return nil, getOut{}, err
		}

		return nil, getOut{authorRow: authorRowOf(a, true), Books: summarizeAll(a.LibraryItems)}, nil
	})

	type missingImageIn struct {
		Library string `json:"library,omitempty" jsonschema:"library name or id; default every library"`
		Limit   int    `json:"limit,omitempty"   jsonschema:"maximum findings, default 100"`
	}
	type missingImageRow struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		Books int    `json:"books"`
		ASIN  string `json:"asin,omitempty" jsonschema:"present means author_match already ran and found no photo"`
	}
	type missingImageOut struct {
		Scanned  int               `json:"authors_scanned"`
		Found    int               `json:"total_findings"`
		Findings []missingImageRow `json:"findings"        jsonschema:"most books first: the authors worth fixing"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "audit_author_missing_image",
		Description: "Find authors with no photo, most-published first. Fix with author_match, which looks them up on Audible, or author_image_set with a url when that finds nothing. An author that already has an asin but no image is one author_match has tried.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in missingImageIn) (*mcp.CallToolResult, missingImageOut, error) {
		libs, err := resolveLibraries(ctx, client, in.Library)
		if err != nil {
			return nil, missingImageOut{}, err
		}

		out := missingImageOut{Findings: []missingImageRow{}}
		for i := range libs {
			if libs[i].MediaType == "podcast" {
				continue
			}
			authors, _, err := client.Authors(ctx, libs[i].ID, abs.ListOptions{Limit: authorResolveLimit, Sort: "numBooks", Desc: true})
			if err != nil {
				return nil, missingImageOut{}, err
			}
			for j := range authors {
				a := &authors[j]
				out.Scanned++
				if a.ImagePath != "" {
					continue
				}
				out.Found++
				out.Findings = append(out.Findings, missingImageRow{
					ID: a.ID, Name: a.Name, Books: a.NumBooks, ASIN: a.ASIN,
				})
			}
		}

		slices.SortFunc(out.Findings, func(x, y missingImageRow) int {
			if x.Books != y.Books {
				return y.Books - x.Books
			}
			return strings.Compare(x.Name, y.Name)
		})
		if limit := limitOr(in.Limit, 100); len(out.Findings) > limit {
			out.Findings = out.Findings[:limit]
		}

		return nil, out, nil
	})

	type editIn struct {
		authorIn
		Name        string `json:"name,omitempty"        jsonschema:"rename; renaming to an existing author's name merges them"`
		Description string `json:"description,omitempty"`
		ASIN        string `json:"asin,omitempty"`
	}
	type editOut struct {
		Merged bool      `json:"merged" jsonschema:"true when the rename merged into an existing author"`
		Author authorRow `json:"author"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "author_edit",
		Description: "Rename an author, or set their description or asin. Renaming to a name that already exists merges the two authors (the way to fix 'J.R.R. Tolkien' vs 'J. R. Tolkien'). Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in editIn) (*mcp.CallToolResult, editOut, error) {
		a, err := resolveAuthor(ctx, client, in.Library, in.Author)
		if err != nil {
			return nil, editOut{}, err
		}
		upd := abs.AuthorUpdate{Name: strPtr(in.Name), Description: strPtr(in.Description), ASIN: strPtr(in.ASIN)}
		if upd.Name == nil && upd.Description == nil && upd.ASIN == nil {
			return nil, editOut{}, errors.New("nothing to change: pass name, description or asin")
		}
		updated, merged, err := client.UpdateAuthor(ctx, a.ID, upd)
		if err != nil {
			return nil, editOut{}, err
		}

		return nil, editOut{Merged: merged, Author: authorRowOf(updated, true)}, nil
	})

	type imageIn struct {
		authorIn
		URL string `json:"url" jsonschema:"image url; the server downloads it"`
	}
	type imageOut struct {
		Author authorRow `json:"author"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "author_image_set",
		Description: "Set an author's photo from an image url, which the server downloads. author_match already fetches one from Audible, so use this for authors it cannot find. Requires the upload permission. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in imageIn) (*mcp.CallToolResult, imageOut, error) {
		if strings.TrimSpace(in.URL) == "" {
			return nil, imageOut{}, errors.New("url is required")
		}
		a, err := resolveAuthor(ctx, client, in.Library, in.Author)
		if err != nil {
			return nil, imageOut{}, err
		}
		updated, err := client.SetAuthorImage(ctx, a.ID, in.URL)
		if err != nil {
			return nil, imageOut{}, err
		}

		return nil, imageOut{Author: authorRowOf(updated, true)}, nil
	})

	type matchIn struct {
		authorIn
		Query  string `json:"query,omitempty"  jsonschema:"name to look up; default the author's name"`
		ASIN   string `json:"asin,omitempty"   jsonschema:"look up by Audible author asin instead"`
		Region string `json:"region,omitempty" jsonschema:"Audible region: us (default), uk, ca, au, de, fr, it, es, jp, in"`
	}
	type matchOut struct {
		Updated bool      `json:"updated"`
		Author  authorRow `json:"author"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "author_match",
		Description: "Look the author up on Audible (via Audnexus) and fill in their asin, description and photo. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in matchIn) (*mcp.CallToolResult, matchOut, error) {
		a, err := resolveAuthor(ctx, client, in.Library, in.Author)
		if err != nil {
			return nil, matchOut{}, err
		}
		q := in.Query
		if q == "" {
			q = a.Name
		}
		updated, changed, err := client.MatchAuthor(ctx, a.ID, q, in.ASIN, in.Region)
		if err != nil {
			return nil, matchOut{}, err
		}

		return nil, matchOut{Updated: changed, Author: authorRowOf(updated, true)}, nil
	})

	type deleteOut struct {
		Deleted string `json:"deleted"`
	}
	add(r, deleteTool, &mcp.Tool{
		Name:        "author_delete",
		Description: "Delete an author record; their books stay but lose the author link. Usually author_edit (merge by rename) is the better fix for duplicates. Requires the delete permission.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in authorIn) (*mcp.CallToolResult, deleteOut, error) {
		a, err := resolveAuthor(ctx, client, in.Library, in.Author)
		if err != nil {
			return nil, deleteOut{}, err
		}
		if err := client.DeleteAuthor(ctx, a.ID); err != nil {
			return nil, deleteOut{}, err
		}

		return nil, deleteOut{Deleted: a.Name}, nil
	})
}

func registerNarratorTools(r *registry) {
	client := r.client

	type narratorRow struct {
		Name  string `json:"name"`
		Books int    `json:"books"`
	}
	type listIn struct {
		Library string `json:"library,omitempty" jsonschema:"library name or id; optional when the server has one library"`
	}
	type listOut struct {
		Total     int           `json:"total"`
		Narrators []narratorRow `json:"narrators"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "narrator_list",
		Description: "List a library's narrators with the number of books each reads. The vocabulary to normalize against: near-duplicates like 'Jim Dale' and 'jim dale' show up as separate entries, and narrator_edit merges them.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listIn) (*mcp.CallToolResult, listOut, error) {
		lib, err := resolveLibrary(ctx, client, in.Library)
		if err != nil {
			return nil, listOut{}, err
		}
		ns, err := client.Narrators(ctx, lib.ID)
		if err != nil {
			return nil, listOut{}, err
		}

		out := listOut{Total: len(ns), Narrators: make([]narratorRow, 0, len(ns))}
		for i := range ns {
			out.Narrators = append(out.Narrators, narratorRow{Name: ns[i].Name, Books: ns[i].NumBooks})
		}

		return nil, out, nil
	})

	type editIn struct {
		Library  string `json:"library,omitempty" jsonschema:"library name or id; optional when the server has one library"`
		Narrator string `json:"narrator"          jsonschema:"the narrator's current name, exactly as narrator_list reports it"`
		Name     string `json:"name,omitempty"    jsonschema:"the new name; renaming onto an existing narrator merges the two"`
		Remove   bool   `json:"remove,omitempty"  jsonschema:"instead of renaming, drop this narrator from every book"`
	}
	type editOut struct {
		ItemsUpdated int `json:"items_updated"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "narrator_edit",
		Description: "Rename a narrator on every book in the library that carries them, or with remove drop them entirely. Renaming onto a name that already exists merges the two, which is how to fix 'Jim Dale' vs 'jim dale'. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in editIn) (*mcp.CallToolResult, editOut, error) {
		narrator := strings.TrimSpace(in.Narrator)
		if narrator == "" {
			return nil, editOut{}, errors.New("narrator is required")
		}
		name := strings.TrimSpace(in.Name)
		if name == "" && !in.Remove {
			return nil, editOut{}, errors.New("pass name to rename, or remove to drop the narrator")
		}

		lib, err := resolveLibrary(ctx, client, in.Library)
		if err != nil {
			return nil, editOut{}, err
		}

		var n int
		if in.Remove {
			n, err = client.RemoveNarrator(ctx, lib.ID, narrator)
		} else {
			n, err = client.RenameNarrator(ctx, lib.ID, narrator, name)
		}
		if err != nil {
			return nil, editOut{}, err
		}

		return nil, editOut{ItemsUpdated: n}, nil
	})
}

func registerSeriesTools(r *registry) {
	client := r.client

	type listIn struct {
		Library string `json:"library,omitempty" jsonschema:"library name or id; optional when the server has one library"`
		Sort    string `json:"sort,omitempty"    jsonschema:"name (default), books, duration, added, last_book_added"`
		Desc    bool   `json:"desc,omitempty"`
		Limit   int    `json:"limit,omitempty"   jsonschema:"page size, default 50"`
		Offset  int    `json:"offset,omitempty"`
	}
	type seriesRow struct {
		ID       string   `json:"id"`
		Name     string   `json:"name"`
		Books    int      `json:"books"`
		Duration string   `json:"duration,omitempty"`
		Author   string   `json:"author,omitempty"`
		Sequence []string `json:"sequence,omitempty" jsonschema:"the sequence numbers present; gaps mean missing books"`
	}
	type listOut struct {
		Total  int         `json:"total"`
		Series []seriesRow `json:"series"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "series_list",
		Description: "List a library's series with book counts and the sequence numbers present, so gaps (missing books) stand out.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listIn) (*mcp.CallToolResult, listOut, error) {
		lib, err := resolveLibrary(ctx, client, in.Library)
		if err != nil {
			return nil, listOut{}, err
		}
		sortBy := map[string]string{"": "name", "name": "name", "books": "numBooks", "duration": "totalDuration", "added": "addedAt", "last_book_added": "lastBookAdded", "last_book_updated": "lastBookUpdated"}[strings.ToLower(in.Sort)]
		if sortBy == "" {
			sortBy = in.Sort
		}
		limit := limitOr(in.Limit, 50)
		series, total, err := client.SeriesList(ctx, lib.ID, abs.ListOptions{Limit: limit, Page: in.Offset / limit, Sort: sortBy, Desc: in.Desc})
		if err != nil {
			return nil, listOut{}, err
		}

		out := listOut{Total: total, Series: []seriesRow{}}
		for i := range series {
			s := &series[i]
			row := seriesRow{ID: s.ID, Name: s.Name, Books: len(s.Books), Duration: fmtDuration(s.TotalDuration)}
			for j := range s.Books {
				m := &s.Books[j].Media.Metadata
				if row.Author == "" {
					row.Author = m.AuthorDisplay()
				}
				for _, ref := range m.Series {
					if ref.ID == s.ID && ref.Sequence != "" {
						row.Sequence = append(row.Sequence, ref.Sequence)
					}
				}
			}
			out.Series = append(out.Series, row)
		}

		return nil, out, nil
	})

	type getIn struct {
		Series  string `json:"series"            jsonschema:"series id or exact name"`
		Library string `json:"library,omitempty" jsonschema:"narrow a name lookup to one library"`
	}
	type bookRow struct {
		Sequence string `json:"sequence,omitempty"`
		itemSummary
		Finished bool `json:"finished,omitempty" jsonschema:"the API key user has finished this book"`
	}
	type getOut struct {
		ID          string    `json:"id"`
		Name        string    `json:"name"`
		Description string    `json:"description,omitempty"`
		Books       []bookRow `json:"books"                 jsonschema:"in series order"`
		Finished    int       `json:"finished"`
		Complete    bool      `json:"complete"              jsonschema:"the API key user has finished every book"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "series_get",
		Description: "A series' books in order with sequence numbers and which ones the API key's user has finished.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in getIn) (*mcp.CallToolResult, getOut, error) {
		s, err := resolveSeries(ctx, client, in.Library, in.Series)
		if err != nil {
			return nil, getOut{}, err
		}
		lib, err := client.Library(ctx, s.LibraryID)
		if err != nil {
			return nil, getOut{}, err
		}
		page, err := client.Items(ctx, lib.ID, abs.ItemsOptions{Limit: 200, Sort: "sequence", Filter: abs.EncodeFilter("series", s.ID), Minified: true})
		if err != nil {
			return nil, getOut{}, err
		}

		finished := map[string]bool{}
		if s.Progress != nil {
			for _, id := range s.Progress.LibraryItemIDsFinished {
				finished[id] = true
			}
		}

		out := getOut{ID: s.ID, Name: s.Name, Description: clip(plain(s.Description), descriptionCap), Books: []bookRow{}}
		for i := range page.Results {
			it := &page.Results[i]
			row := bookRow{itemSummary: summarize(it), Finished: finished[it.ID]}
			for _, ref := range it.Media.Metadata.Series {
				if ref.ID == s.ID || ref.Name == s.Name {
					row.Sequence = ref.Sequence
				}
			}
			if row.Finished {
				out.Finished++
			}
			out.Books = append(out.Books, row)
		}
		out.Complete = len(out.Books) > 0 && out.Finished == len(out.Books)

		return nil, out, nil
	})

	type editIn struct {
		getIn
		Name        string `json:"name,omitempty"`
		Description string `json:"description,omitempty"`
	}
	type editOut struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "series_edit",
		Description: "Rename a series or set its description. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in editIn) (*mcp.CallToolResult, editOut, error) {
		s, err := resolveSeries(ctx, client, in.Library, in.Series)
		if err != nil {
			return nil, editOut{}, err
		}
		upd := abs.SeriesUpdate{Name: strPtr(in.Name), Description: strPtr(in.Description)}
		if upd.Name == nil && upd.Description == nil {
			return nil, editOut{}, errors.New("nothing to change: pass name or description")
		}
		updated, err := client.UpdateSeries(ctx, s.ID, upd)
		if err != nil {
			return nil, editOut{}, err
		}

		return nil, editOut{ID: updated.ID, Name: updated.Name}, nil
	})
}
