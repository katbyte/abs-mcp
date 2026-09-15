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

// authorPageSize is how many authors one listing request asks for. A lookup
// pages until the server has no more: a library can hold far more than one
// page, and an author on the second one is still an author.
const authorPageSize = 500

// allAuthors lists every author in a library, a page at a time, in the order
// opts asks for.
func allAuthors(ctx context.Context, client *abs.Client, libraryID string, opts abs.ListOptions) ([]abs.Author, error) {
	opts.Limit = authorPageSize
	var all []abs.Author
	for opts.Page = 0; ; opts.Page++ {
		authors, total, err := client.Authors(ctx, libraryID, opts)
		if err != nil {
			return nil, err
		}
		all = append(all, authors...)
		if len(authors) == 0 || len(authors) < authorPageSize || len(all) >= total {
			return all, nil
		}
	}
}

// resolveAuthor finds an author by id or name, searching the named library or
// all of them.
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
		authors, err := allAuthors(ctx, client, libs[i].ID, abs.ListOptions{})
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

// allSeries lists every series in a library from the live series route, a
// page at a time. The filter data the server also offers is cached for half
// an hour and not refreshed when a series is renamed, so a series looked up
// there by its new name is not found and its old name still is.
func allSeries(ctx context.Context, client *abs.Client, libraryID string) ([]abs.Series, error) {
	var all []abs.Series
	for page := 0; ; page++ {
		series, total, err := client.SeriesList(ctx, libraryID, abs.ListOptions{Limit: seriesPageSize, Page: page, Sort: "name"})
		if err != nil {
			return nil, err
		}
		all = append(all, series...)
		if len(series) == 0 || len(series) < seriesPageSize || len(all) >= total {
			return all, nil
		}
	}
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
	// a series whose books have all gone can outlive them: the server lists
	// it, with no books, and answers 404 to opening it
	var found []string
	empty := false
	for i := range libs {
		if libs[i].IsPodcast() {
			continue
		}
		series, err := allSeries(ctx, client, libs[i].ID)
		if err != nil {
			return nil, err
		}
		for _, s := range series {
			if !strings.EqualFold(s.Name, nameOrID) {
				continue
			}
			// an empty list, not an absent one: that would be a reply that
			// left the books out, and says nothing about them
			if s.Books != nil && len(s.Books) == 0 {
				empty = true
				continue
			}
			found = append(found, s.ID)
		}
	}
	switch {
	case len(found) == 1:
		return client.Series(ctx, found[0])
	case len(found) == 0 && empty:
		return nil, fmt.Errorf("no series named %q with a book in it: the server still lists one by that name, but its books are gone and it cannot be opened", nameOrID)
	case len(found) == 0:
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

// withBooks reads an author back with their books after a write: the update,
// image and match routes answer with the record alone, so a row built from
// that reply says the author has no books.
func withBooks(ctx context.Context, client *abs.Client, a *abs.Author) *abs.Author {
	if full, err := client.Author(ctx, a.ID, true); err == nil {
		return full
	}
	return a
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

	type editIn struct {
		authorIn
		Name        string   `json:"name,omitempty"        jsonschema:"rename; renaming to an existing author's name merges them"`
		Description string   `json:"description,omitempty"`
		ASIN        string   `json:"asin,omitempty"`
		Clear       []string `json:"clear,omitempty"       jsonschema:"fields to blank: description, asin, image. How to undo an author_match that found the wrong person"`
	}
	type editOut struct {
		Merged bool      `json:"merged" jsonschema:"true when the rename merged into an existing author"`
		Author authorRow `json:"author"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "author_edit",
		Description: "Rename an author, set their description or asin, or blank those and the photo with clear. Renaming to a name that already exists merges the two authors (the way to fix 'J.R.R. Tolkien' vs 'J. R. Tolkien'); clear=[asin, description, image] undoes an author_match that found the wrong person. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in editIn) (*mcp.CallToolResult, editOut, error) {
		upd := abs.AuthorUpdate{Name: strPtr(in.Name), Description: strPtr(in.Description), ASIN: strPtr(in.ASIN)}
		var clearImage bool
		empty := ""
		for _, c := range in.Clear {
			switch strings.ToLower(strings.TrimSpace(c)) {
			case "description":
				upd.Description = &empty
			case "asin":
				upd.ASIN = &empty
			case "image":
				clearImage = true
			default:
				return nil, editOut{}, fmt.Errorf("cannot clear %q: choose from description, asin, image", c)
			}
		}
		if upd.Name == nil && upd.Description == nil && upd.ASIN == nil && !clearImage {
			return nil, editOut{}, errors.New("nothing to change: pass name, description or asin, or list fields in clear")
		}

		a, err := resolveAuthor(ctx, client, in.Library, in.Author)
		if err != nil {
			return nil, editOut{}, err
		}
		updated, merged := a, false
		if upd.Name != nil || upd.Description != nil || upd.ASIN != nil {
			if updated, merged, err = client.UpdateAuthor(ctx, a.ID, upd); err != nil {
				return nil, editOut{}, err
			}
		}
		// the server answers 400 to removing a photo that is not there, and
		// clearing what is already blank is not a failure
		if clearImage && a.ImagePath != "" {
			if updated, err = client.DeleteAuthorImage(ctx, a.ID); err != nil {
				return nil, editOut{}, err
			}
		}

		return nil, editOut{Merged: merged, Author: authorRowOf(withBooks(ctx, client, updated), true)}, nil
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

		return nil, imageOut{Author: authorRowOf(withBooks(ctx, client, updated), true)}, nil
	})

	type matchIn struct {
		authorIn
		Query string `json:"query,omitempty" jsonschema:"name to look up; default the author's name"`
	}
	type matchCandidate struct {
		ASIN        string `json:"asin"`
		Name        string `json:"name"`
		Description string `json:"description,omitempty"`
		HasImage    bool   `json:"has_image"`
		NameMatches bool   `json:"name_matches"          jsonschema:"the candidate's name is the author's own, case and punctuation aside; false means look closely before applying"`
	}
	type matchOut struct {
		Author    authorRow       `json:"author"`
		Candidate *matchCandidate `json:"candidate,omitempty" jsonschema:"who Audible has for the name; absent when nobody is close enough. Nothing is applied: pass the asin to author_match_apply"`
	}
	add(r, readTool, &mcp.Tool{
		Name: "author_match",
		Description: "Look the author up on Audible (via Audnexus) and return who it found, without changing anything. " +
			"The lookup is by name and tolerant of small differences, so the candidate can be someone else (Emily Andras came back as Emily Adrian): " +
			"check name_matches and the description, then author_match_apply with the asin to take it.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in matchIn) (*mcp.CallToolResult, matchOut, error) {
		a, err := resolveAuthor(ctx, client, in.Library, in.Author)
		if err != nil {
			return nil, matchOut{}, err
		}
		q := strings.TrimSpace(in.Query)
		if q == "" {
			q = a.Name
		}
		cand, err := client.SearchAuthor(ctx, q)
		if err != nil {
			return nil, matchOut{}, err
		}

		out := matchOut{Author: authorRowOf(a, true)}
		if cand != nil {
			out.Candidate = &matchCandidate{
				ASIN: cand.ASIN, Name: cand.Name,
				Description: clip(plain(cand.Description), descriptionCap),
				HasImage:    cand.Image != "",
				NameMatches: sameName(cand.Name, a.Name),
			}
		}

		return nil, out, nil
	})

	type applyIn struct {
		authorIn
		ASIN   string `json:"asin"             jsonschema:"the Audible author asin to apply, from author_match"`
		Region string `json:"region,omitempty" jsonschema:"Audible region: us (default), uk, ca, au, de, fr, it, es, jp, in"`
	}
	type applyOut struct {
		Updated bool      `json:"updated"`
		Author  authorRow `json:"author"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "author_match_apply",
		Description: "Apply an Audible author to the record: their asin, description and photo. Pass the asin of the candidate author_match returned, once its name and description say it is the right person. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in applyIn) (*mcp.CallToolResult, applyOut, error) {
		asin := strings.TrimSpace(in.ASIN)
		if asin == "" {
			return nil, applyOut{}, errors.New("pass the asin of the candidate to apply; author_match finds it")
		}
		a, err := resolveAuthor(ctx, client, in.Library, in.Author)
		if err != nil {
			return nil, applyOut{}, err
		}
		updated, changed, err := client.MatchAuthor(ctx, a.ID, "", asin, in.Region)
		if err != nil {
			return nil, applyOut{}, err
		}

		return nil, applyOut{Updated: changed, Author: authorRowOf(withBooks(ctx, client, updated), true)}, nil
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
		Description: "List a library's narrators with the number of books each reads. The vocabulary to normalize against: near-duplicates like 'Jim Dale' and 'jim dale' show up as separate entries, and metadata_rename merges them.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listIn) (*mcp.CallToolResult, listOut, error) {
		lib, err := resolveLibrary(ctx, client, in.Library)
		if err != nil {
			return nil, listOut{}, err
		}
		restricted, err := restrictedKey(ctx, client)
		if err != nil {
			return nil, listOut{}, err
		}
		if restricted {
			// the narrator route counts books this key may not see
			v, verr := sweepVisible(ctx, client, lib)
			if verr != nil {
				return nil, listOut{}, verr
			}
			out := listOut{Total: len(v.Narrators), Narrators: make([]narratorRow, 0, len(v.Narrators))}
			for _, name := range keysOf(v.Narrators) {
				out.Narrators = append(out.Narrators, narratorRow{Name: name, Books: v.Narrators[name]})
			}
			return nil, out, nil
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
}

// seriesBooks is a series' books in sequence order, each with its whole
// series list. An item listing filtered by series comes back with that field
// collapsed to the one series matched (docs/README.md), which is fine for the
// sequence but not for anything that writes the list back: four Stormlight
// books lost their Cosmere link to an edit built from it. So the order and
// the sequence come from the filtered listing and the series lists from one
// batch fetch of the same ids.
func seriesBooks(ctx context.Context, client *abs.Client, s *abs.Series) ([]abs.Item, error) {
	page, err := client.Items(ctx, s.LibraryID, abs.ItemsOptions{Limit: 500, Sort: "sequence", Filter: abs.EncodeFilter("series", s.ID), Minified: true})
	if err != nil {
		return nil, err
	}
	if err := fullSeriesLists(ctx, client, page.Results); err != nil {
		return nil, err
	}
	return page.Results, nil
}

// fullSeriesLists puts every series each item is in back onto items that came
// from a listing filtered by series, with one batch fetch of their ids.
func fullSeriesLists(ctx context.Context, client *abs.Client, items []abs.Item) error {
	if len(items) == 0 {
		return nil
	}
	ids := make([]string, 0, len(items))
	for i := range items {
		ids = append(ids, items[i].ID)
	}
	full, err := client.ItemsBatch(ctx, ids)
	if err != nil {
		return err
	}
	refs := make(map[string]abs.SeriesRefs, len(full))
	for i := range full {
		refs[full[i].ID] = full[i].Media.Metadata.Series
	}
	for i := range items {
		it := &items[i]
		if all, ok := refs[it.ID]; ok && len(all) > 0 {
			it.Media.Metadata.Series = all
			it.Media.Metadata.SeriesName = ""
		}
	}
	return nil
}

// mergeSeriesRefs is a book's series list with from replaced by into, in
// its place: the from entry's number goes to into unless into is already
// there with a number of its own, in which case the from entry just goes.
// Every other series is kept as it was.
func mergeSeriesRefs(refs []abs.SeriesRef, from, into *abs.Series) []abs.SeriesRef {
	sameSeries := func(ref abs.SeriesRef, s *abs.Series) bool {
		return (ref.ID != "" && ref.ID == s.ID) || strings.EqualFold(strings.TrimSpace(ref.Name), strings.TrimSpace(s.Name))
	}
	out := make([]abs.SeriesRef, 0, len(refs))
	seq, fromAt, intoAt := "", -1, -1
	for _, ref := range refs {
		switch {
		case sameSeries(ref, from):
			seq, fromAt = ref.Sequence, len(out)
			out = append(out, abs.SeriesRef{Name: into.Name})
		case sameSeries(ref, into):
			intoAt = len(out)
			out = append(out, ref)
		default:
			out = append(out, ref)
		}
	}
	switch {
	case fromAt < 0:
		return out
	case intoAt < 0:
		out[fromAt].Sequence = seq
	default:
		if out[intoAt].Sequence == "" {
			out[intoAt].Sequence = seq
		}
		out = slices.Delete(out, fromAt, fromAt+1)
	}
	return out
}

func seqSuffix(seq string) string {
	if seq == "" {
		return ""
	}
	return " #" + seq
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
		Library  string   `json:"library,omitempty"  jsonschema:"when more than one library is listed"`
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
		Description: "List series with book counts and the sequence numbers present, so gaps (missing books) stand out. One library by name or id, or every book library when none is given; limit and offset page each library.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listIn) (*mcp.CallToolResult, listOut, error) {
		libs, err := resolveLibraries(ctx, client, in.Library)
		if err != nil {
			return nil, listOut{}, err
		}
		sortBy := map[string]string{"": "name", "name": "name", "books": "numBooks", "duration": "totalDuration", "added": "addedAt", "last_book_added": "lastBookAdded", "last_book_updated": "lastBookUpdated"}[strings.ToLower(in.Sort)]
		if sortBy == "" {
			sortBy = in.Sort
		}
		limit := limitOr(in.Limit, 50)

		out := listOut{Series: []seriesRow{}}
		for i := range libs {
			lib := &libs[i]
			if lib.IsPodcast() {
				continue
			}
			series, total, err := client.SeriesList(ctx, lib.ID, abs.ListOptions{Limit: limit, Page: in.Offset / limit, Sort: sortBy, Desc: in.Desc})
			if err != nil {
				return nil, listOut{}, err
			}
			out.Total += total
			for j := range series {
				s := &series[j]
				row := seriesRow{ID: s.ID, Name: s.Name, Books: len(s.Books), Duration: fmtDuration(s.TotalDuration)}
				if len(libs) > 1 {
					row.Library = lib.Name
				}
				for k := range s.Books {
					m := &s.Books[k].Media.Metadata
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
		Description: "A series' books in order with sequence numbers and which ones the API key's user has finished. Each book's series list is complete, every series it is in and not only this one, so it is safe to build an item_edit series= list from.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in getIn) (*mcp.CallToolResult, getOut, error) {
		s, err := resolveSeries(ctx, client, in.Library, in.Series)
		if err != nil {
			return nil, getOut{}, err
		}
		books, err := seriesBooks(ctx, client, s)
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
		for i := range books {
			it := &books[i]
			row := bookRow{itemSummary: summarize(it), Finished: finished[it.ID]}
			for _, ref := range it.Media.Metadata.Series {
				if ref.ID == s.ID || strings.EqualFold(ref.Name, s.Name) {
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
		if upd.Name != nil {
			// two series with one name is not a merge, it is two series
			// with one name; series_merge is how books move
			others, ferr := allSeries(ctx, client, s.LibraryID)
			if ferr != nil {
				return nil, editOut{}, ferr
			}
			for _, other := range others {
				if other.ID != s.ID && strings.EqualFold(strings.TrimSpace(other.Name), strings.TrimSpace(in.Name)) {
					return nil, editOut{}, fmt.Errorf("a series named %q already exists (%s): series_merge from=%q into=%q moves the books there instead", other.Name, other.ID, s.Name, other.Name)
				}
			}
		}
		updated, err := client.UpdateSeries(ctx, s.ID, upd)
		if err != nil {
			return nil, editOut{}, err
		}

		return nil, editOut{ID: updated.ID, Name: updated.Name}, nil
	})

	type mergeIn struct {
		From    string `json:"from"              jsonschema:"series id or exact name whose books move; it is empty afterwards and goes away"`
		Into    string `json:"into"              jsonschema:"series id or exact name the books move into"`
		Library string `json:"library,omitempty" jsonschema:"narrow a name lookup to one library"`
	}
	type mergeOut struct {
		From   string   `json:"from"`
		Into   string   `json:"into"`
		IntoID string   `json:"into_id"`
		Moved  int      `json:"moved"`
		Books  []string `json:"books"   jsonschema:"each book as it now stands in the series, 'Title #2'"`
	}
	add(r, writeTool, &mcp.Tool{
		Name: "series_merge",
		Description: "Move every book of one series into another, keeping each book's number and every other series it is in; the emptied series goes away. " +
			"For two spellings of one series that audit_series names reports ('The Wheel of Time' into 'Wheel of Time'), and for a one-book 'Skyward Series' beside 'Skyward'. " +
			"A book already in both keeps its number in the target, or takes the one it had if the target had none. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in mergeIn) (*mcp.CallToolResult, mergeOut, error) {
		// every book of the series is read and its series list written back
		defer r.locks.holdAll()()
		from, err := resolveSeries(ctx, client, in.Library, in.From)
		if err != nil {
			return nil, mergeOut{}, fmt.Errorf("from: %w", err)
		}
		into, err := resolveSeries(ctx, client, in.Library, in.Into)
		if err != nil {
			return nil, mergeOut{}, fmt.Errorf("into: %w", err)
		}
		if from.ID == into.ID {
			return nil, mergeOut{}, fmt.Errorf("%q is one series: nothing to merge", from.Name)
		}
		books, err := seriesBooks(ctx, client, from)
		if err != nil {
			return nil, mergeOut{}, err
		}
		if len(books) == 0 {
			return nil, mergeOut{}, fmt.Errorf("%q has no books", from.Name)
		}

		out := mergeOut{From: from.Name, Into: into.Name, IntoID: into.ID, Books: make([]string, 0, len(books))}
		updates := make([]abs.BatchMediaUpdate, 0, len(books))
		for i := range books {
			it := &books[i]
			refs := mergeSeriesRefs(it.Media.Metadata.Series, from, into)
			updates = append(updates, abs.BatchMediaUpdate{ID: it.ID, MediaPayload: abs.MediaUpdate{Metadata: &abs.MetadataUpdate{Series: refs}}})
			for _, ref := range refs {
				if strings.EqualFold(ref.Name, into.Name) {
					out.Books = append(out.Books, it.Title()+seqSuffix(ref.Sequence))
				}
			}
		}
		n, err := client.BatchUpdate(ctx, updates)
		if err != nil {
			return nil, mergeOut{}, err
		}
		out.Moved = n

		return nil, out, nil
	})
}

// sameName reports whether two author names are the same name: case,
// punctuation and spacing aside, so "Ursula K. Le Guin" is "ursula k le guin",
// but "Emily Andras" is not "Emily Adrian".
func sameName(a, b string) bool {
	return strings.Join(strings.Fields(norm(a)), " ") == strings.Join(strings.Fields(norm(b)), " ")
}
