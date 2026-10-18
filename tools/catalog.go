package tools

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/katbyte/abs-mcp/sdk/abs"
	mcpregistry "github.com/katbyte/go-kt/mcp/registry"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// authorPageSize is how many authors one listing request asks for. A lookup
// pages until the server has no more: a library can hold far more than one
// page, and an author on the second one is still an author.
const authorPageSize = 500

// allAuthors lists every author in a library, a page at a time, in the order
// opts asks for.
func allAuthors(ctx context.Context, gen *abs.Client, library abs.LibraryID, opts abs.LibraryGetAuthorsOptions) ([]abs.Author, error) {
	opts.Limit = new(authorPageSize)
	var all []abs.Author
	for page := 0; ; page++ {
		opts.Page = new(page)
		got, err := record(gen.LibraryGetAuthors(ctx, library, opts))
		if err != nil {
			return nil, err
		}
		authors, total := authorsOf(got)
		all = append(all, authors...)
		if len(authors) == 0 || len(authors) < authorPageSize || len(all) >= total {
			return all, nil
		}
	}
}

// authorsOf are the authors of one page and how many there are in all; a server before 2.20 answers them as authors, every one at once.
func authorsOf(got abs.LibraryGetAuthorsAnswer) (authors []abs.Author, total int) {
	if got.Results == nil && got.Authors != nil {
		return got.Authors, len(got.Authors)
	}

	return got.Results, got.Total
}

// getAuthor is one author, with their books.
func getAuthor(ctx context.Context, gen *abs.Client, id abs.AuthorID) (*abs.Author, error) {
	a, err := record(gen.AuthorFindOne(ctx, id, abs.AuthorFindOneOptions{Include: "items"}))
	if err != nil {
		return nil, err
	}

	return &a, nil
}

// nameTier says how a stored name answers one a caller gave: 2 when it is
// the name exactly as given, spaces and all; 1 when it is the given name
// with case set aside, and the given name has no space at either end; 0
// otherwise. A name given with a space at an end, as audit_whitespace
// reports one, is that record or none, never its tidy twin; and a stored
// name is never trimmed to answer a tidy one, so "Brandon Sanderson" does
// not reach "Brandon Sanderson " - a delete or rename by the tidy name must
// not land on a record spelled otherwise.
func nameTier(stored, given string) int {
	switch {
	case stored == given:
		return 2
	case given == strings.TrimSpace(given) && strings.EqualFold(stored, given):
		return 1
	}
	return 0
}

// nearNames is the error for a name nothing answers, naming the records
// that differ from it only by spaces at an end or case, with their ids, to
// pass one of them instead.
func nearNames(kind, given string, names, ids []string) error {
	msg := fmt.Sprintf("no %s named %q", kind, given)
	if near := nearList(given, names, ids); near != "" {
		return fmt.Errorf("%s; %s", msg, near)
	}
	return fmt.Errorf("%s (library_search finds partial names)", msg)
}

// nearList names the records that differ from a name only by spaces at an
// end or case, with their ids, or "" when none does.
func nearList(given string, names, ids []string) string {
	var near []string
	for i, n := range names {
		if n != given && strings.EqualFold(strings.TrimSpace(n), strings.TrimSpace(given)) {
			near = append(near, fmt.Sprintf("%q %s", n, ids[i]))
		}
	}
	if len(near) == 0 {
		return ""
	}
	return "spelled otherwise only by spaces or case: " + strings.Join(near, "; ") + " - pass the id of the one meant"
}

// severalNamed is the error for a name that picks out more than one record:
// each with its name, as the names may differ in their spaces, and its id.
func severalNamed(kind, given string, names, ids, libraries []string) error {
	where := "in one library"
	if slices.ContainsFunc(libraries, func(l string) bool { return l != libraries[0] }) {
		where = "across several libraries"
	}
	list := make([]string, 0, len(ids))
	for i := range ids {
		list = append(list, fmt.Sprintf("%q %s in library %s", names[i], ids[i], libraries[i]))
	}
	return fmt.Errorf("%q matches several %s %s; pass an id: %s", given, kind, where, strings.Join(list, "; "))
}

// resolveAuthor finds an author by id or name, searching the named library or
// all of them: the name exactly as given, and with case aside when it has no
// space at an end; several such are refused, each named with its id.
func resolveAuthor(ctx context.Context, gen *abs.Client, library, nameOrID string) (*abs.Author, error) {
	if id := strings.TrimSpace(nameOrID); looksLikeID(id) {
		return getAuthor(ctx, gen, abs.AuthorID(id))
	}
	candidates, err := authorsNamed(ctx, gen, library, nameOrID)
	if err != nil {
		return nil, err
	}
	if len(candidates) == 1 {
		return getAuthor(ctx, gen, candidates[0].Id)
	}
	names, ids, libraries := make([]string, 0, len(candidates)), make([]string, 0, len(candidates)), make([]string, 0, len(candidates))
	for _, c := range candidates {
		names, ids, libraries = append(names, c.Name), append(ids, string(c.Id)), append(libraries, string(c.LibraryId))
	}
	return nil, severalNamed("authors", nameOrID, names, ids, libraries)
}

// authorsNamed is every author a name or id picks out, at least one: the
// records whose name is the one given exactly, spaces and all, and, for a
// name with no space at an end, those equal to it case aside. A stored name
// is never trimmed to answer. An id is its record.
func authorsNamed(ctx context.Context, gen *abs.Client, library, nameOrID string) ([]abs.Author, error) {
	given := nameOrID
	nameOrID = strings.TrimSpace(nameOrID)
	if nameOrID == "" {
		return nil, errors.New("author id or name is required")
	}
	if looksLikeID(nameOrID) {
		a, err := getAuthor(ctx, gen, abs.AuthorID(nameOrID))
		if err != nil {
			return nil, err
		}
		return []abs.Author{*a}, nil
	}

	libs, err := resolveLibraries(ctx, gen, library)
	if err != nil {
		return nil, err
	}
	matches := [3][]abs.Author{} // by tier
	var names, ids []string
	for i := range libs {
		// the live authors endpoint, not FilterData: the server caches filter
		// data and does not invalidate it on an edit or a scan, so a renamed
		// author would not be found there
		authors, err := allAuthors(ctx, gen, libs[i].Id, abs.LibraryGetAuthorsOptions{})
		if err != nil {
			return nil, err
		}
		for _, a := range authors {
			names, ids = append(names, a.Name), append(ids, string(a.Id))
			if tier := nameTier(a.Name, given); tier > 0 {
				matches[tier] = append(matches[tier], a)
			}
		}
	}
	// the name exactly as given and its case variants alike: two records
	// one name answers, case aside, are both named and neither picked
	candidates := slices.Concat(matches[2], matches[1])
	if len(candidates) == 0 {
		return nil, nearNames("author", given, names, ids)
	}
	return candidates, nil
}

// allSeries lists every series in a library from the live series route, a
// page at a time. The filter data the server also offers is cached for half
// an hour and not refreshed when a series is renamed, so a series looked up
// there by its new name is not found and its old name still is.
func allSeries(ctx context.Context, gen *abs.Client, library abs.LibraryID) ([]abs.Series, error) {
	var all []abs.Series
	for page := 0; ; page++ {
		got, err := record(gen.LibraryGetAllSeriesForLibrary(ctx, library, abs.LibraryGetAllSeriesForLibraryOptions{Limit: new(seriesPageSize), Page: new(page), Sort: "name"}))
		if err != nil {
			return nil, err
		}
		all = append(all, got.Results...)
		if len(got.Results) == 0 || len(got.Results) < seriesPageSize || len(all) >= got.Total {
			return all, nil
		}
	}
}

// getSeries is one series, with the API key user's progress through it.
func getSeries(ctx context.Context, gen *abs.Client, id abs.SeriesID) (*abs.Series, error) {
	s, err := record(gen.SeriesFindOne(ctx, id, abs.SeriesFindOneOptions{Include: "progress"}))
	if err != nil {
		return nil, err
	}

	return &s, nil
}

// resolveSeries finds a series by id or name in the named library or all of
// them, as resolveAuthor finds an author.
func resolveSeries(ctx context.Context, gen *abs.Client, library, nameOrID string) (*abs.Series, error) {
	given := nameOrID
	nameOrID = strings.TrimSpace(nameOrID)
	if nameOrID == "" {
		return nil, errors.New("series id or name is required")
	}
	if looksLikeID(nameOrID) {
		return getSeries(ctx, gen, abs.SeriesID(nameOrID))
	}

	libs, err := resolveLibraries(ctx, gen, library)
	if err != nil {
		return nil, err
	}
	// a series whose books have all gone can outlive them: the server lists
	// it, with no books, and answers 404 to opening it
	matches := [3][]abs.Series{} // by tier
	var names, ids, hiding []string
	for i := range libs {
		if libs[i].IsPodcast() {
			continue
		}
		if libs[i].Settings != nil && libs[i].Settings.HideSingleBookSeries {
			hiding = append(hiding, libs[i].Name)
		}
		series, err := allSeries(ctx, gen, libs[i].Id)
		if err != nil {
			return nil, err
		}
		for _, s := range series {
			names, ids = append(names, s.Name), append(ids, string(s.Id))
			if tier := nameTier(s.Name, given); tier > 0 {
				matches[tier] = append(matches[tier], s)
			}
		}
	}
	// a series with no books left is set aside before the closest match is
	// chosen: an empty "Mistborn" must not hide "Mistborn " holding the books.
	// An empty list, not an absent one: that would be a reply that left the
	// books out, and says nothing about them
	empty := false
	var exactEmpty []abs.Series // the name as given, held only by series with no books
	for t := range matches {
		matches[t] = slices.DeleteFunc(matches[t], func(s abs.Series) bool {
			gone := s.Books != nil && len(s.Books) == 0
			empty = empty || gone
			if gone && t == 2 {
				exactEmpty = append(exactEmpty, s)
			}
			return gone
		})
	}
	// the name exactly as given and its case variants alike, as a name
	// answers both; but where the name as written is an empty series and
	// only a case variant has books, neither is picked
	found := slices.Concat(matches[2], matches[1])
	if len(matches[2]) == 0 && len(exactEmpty) > 0 && len(found) > 0 {
		list := func(ss []abs.Series) string {
			out := make([]string, 0, len(ss))
			for _, s := range ss {
				out = append(out, fmt.Sprintf("%q (%s)", s.Name, s.Id))
			}
			return strings.Join(out, ", ")
		}
		return nil, fmt.Errorf("the series named exactly %s has no books left, and %s differs from it only in case: pass the id of the one meant", list(exactEmpty), list(found))
	}
	switch {
	case len(found) == 1:
		return getSeries(ctx, gen, found[0].Id)
	case len(found) == 0 && empty:
		err := fmt.Errorf("no series named %q with a book in it: the server still lists one by that name, but its books are gone and it cannot be opened", given)
		if near := nearList(given, names, ids); near != "" {
			err = fmt.Errorf("%w; %s", err, near)
		}
		return nil, err
	case len(found) == 0:
		err := nearNames("series", given, names, ids)
		// a one-book series such a library leaves out of its list, which is
		// all a lookup by name can read
		if len(hiding) > 0 {
			err = fmt.Errorf("%w; %s hides one-book series from its series list, so one of those is found by id alone, as audit_whitespace gives it", err, strings.Join(hiding, ", "))
		}
		return nil, err
	}
	names, ids, libraries := make([]string, 0, len(found)), make([]string, 0, len(found)), make([]string, 0, len(found))
	for _, s := range found {
		names, ids, libraries = append(names, s.Name), append(ids, string(s.Id)), append(libraries, string(s.LibraryId))
	}

	return nil, severalNamed("series", given, names, ids, libraries)
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
// that reply says the author has no books. A failed read says the write was
// made.
func withBooks(ctx context.Context, gen *abs.Client, a *abs.Author) (*abs.Author, error) {
	full, err := getAuthor(ctx, gen, a.Id)
	if err != nil {
		return nil, fmt.Errorf("the change to author %q (%s) was made, but reading it back failed: %w", a.Name, a.Id, err)
	}
	return full, nil
}

func authorRowOf(a *abs.Author, withDescription bool) authorRow {
	row := authorRow{ID: string(a.Id), Name: a.Name, Books: a.NumBooks, ASIN: a.Asin, HasImage: a.ImagePath != ""}
	if row.Books == 0 {
		row.Books = len(a.LibraryItems)
	}
	if withDescription {
		row.Description = clip(plain(a.Description), descriptionCap)
	}
	return row
}

// authorSorts and seriesSorts map the sort names author_list and series_list
// take, and the server's own names for them, to what the server sorts by. A
// name the server does not know leaves the listing in no order at all, so any
// other is refused.
var (
	authorSorts = map[string]string{
		"": "name", "name": "name", "last_first": "lastFirst", "books": "numBooks", "added": "addedAt", "updated": "updatedAt",
		"lastfirst": "lastFirst", "numbooks": "numBooks", "addedat": "addedAt", "updatedat": "updatedAt",
	}
	seriesSorts = map[string]string{
		"": "name", "name": "name", "books": "numBooks", "duration": "totalDuration", "added": "addedAt",
		"last_book_added": "lastBookAdded", "last_book_updated": "lastBookUpdated", "random": "random",
		"numbooks": "numBooks", "totalduration": "totalDuration", "addedat": "addedAt", "lastbookadded": "lastBookAdded", "lastbookupdated": "lastBookUpdated",
	}
)

func registerAuthorTools(r *registry) {
	gen := r.gen

	type listIn struct {
		Library string `json:"library,omitempty" jsonschema:"library name or id; optional when the server has one library"`
		Sort    string `json:"sort,omitempty"    jsonschema:"name (default), last_first, books, added, updated"`
		Desc    bool   `json:"desc,omitempty"`
		Limit   int    `json:"limit,omitempty"   jsonschema:"page size, default 50, at most 1000"`
		Offset  int    `json:"offset,omitempty"  jsonschema:"skip this many authors: a previous page's next_offset"`
	}
	type listOut struct {
		Total      int         `json:"total"`
		Offset     int         `json:"offset"`
		NextOffset int         `json:"next_offset,omitempty" jsonschema:"pass back as offset for the next page; absent on the last"`
		Authors    []authorRow `json:"authors"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "author_list",
		Description: "List a library's authors with book counts, sortable by name or number of books. Authors without an asin or image are candidates for author_match.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listIn) (*mcp.CallToolResult, listOut, error) {
		lib, err := resolveLibrary(ctx, gen, in.Library)
		if err != nil {
			return nil, listOut{}, err
		}
		sortBy, ok := authorSorts[strings.ToLower(strings.TrimSpace(in.Sort))]
		if !ok {
			return nil, listOut{}, fmt.Errorf("unknown sort %q; choose one of: name, last_first, books, added, updated", in.Sort)
		}
		limit, offset := pageArgs(in.Limit, in.Offset, 50)
		authors, total, err := window(offset, limit, func(page int) ([]abs.Author, int, error) {
			got, gerr := record(gen.LibraryGetAuthors(ctx, lib.Id, abs.LibraryGetAuthorsOptions{Limit: new(limit), Page: new(page), Sort: sortBy, Desc: descFlag(in.Desc)}))
			if gerr != nil {
				return nil, 0, gerr
			}
			authors, total := authorsOf(got)
			return authors, total, nil
		})
		if err != nil {
			return nil, listOut{}, err
		}

		out := listOut{Total: total, Offset: offset, NextOffset: mcpregistry.NextOffset(offset, len(authors), total), Authors: []authorRow{}}
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
		a, err := resolveAuthor(ctx, gen, in.Library, in.Author)
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
		ImageURL    string   `json:"image_url,omitempty"   jsonschema:"a photo for the author, which the server downloads from this url; needs the upload permission. author_match_apply already fetches one from Audible, so this is for an author it cannot find"`
		Clear       []string `json:"clear,omitempty"       jsonschema:"fields to blank: description, asin, image. How to undo an author_match that found the wrong person"`
	}
	type editOut struct {
		Merged    bool      `json:"merged"              jsonschema:"true when the rename merged into an existing author"`
		Unchanged bool      `json:"unchanged,omitempty" jsonschema:"the author already had this name and nothing else was asked for: nothing was written"`
		Author    authorRow `json:"author"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "author_edit",
		Description: "Rename an author, set their description, asin or photo, or blank those with clear. Renaming to a name that already exists merges the two authors (the way to fix 'J.R.R. Tolkien' vs 'J. R. Tolkien'); clear=[asin, description, image] undoes an author_match that found the wrong person. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in editIn) (*mcp.CallToolResult, editOut, error) {
		upd := abs.AuthorUpdateRequest{Name: in.Name, Description: strPtr(in.Description), Asin: strPtr(in.ASIN)}
		var clearImage bool
		empty := ""
		for _, c := range in.Clear {
			switch strings.ToLower(strings.TrimSpace(c)) {
			case "description":
				upd.Description = &empty
			case "asin":
				upd.Asin = &empty
			case "image":
				clearImage = true
			default:
				return nil, editOut{}, fmt.Errorf("cannot clear %q: choose from description, asin, image", c)
			}
		}
		imageURL := strings.TrimSpace(in.ImageURL)
		switch {
		case imageURL != "" && clearImage:
			return nil, editOut{}, errors.New("image_url sets the photo and clear image removes it; one or the other")
		case upd.Name == "" && upd.Description == nil && upd.Asin == nil && !clearImage && imageURL == "":
			return nil, editOut{}, errors.New("nothing to change: pass name, description, asin or image_url, or list fields in clear")
		}

		a, err := resolveAuthor(ctx, gen, in.Library, in.Author)
		if err != nil {
			return nil, editOut{}, err
		}
		// a rename that changes nothing writes nothing, and says so rather
		// than report a rename done
		if upd.Name != "" && upd.Name == a.Name && upd.Description == nil && upd.Asin == nil && !clearImage && imageURL == "" {
			return nil, editOut{Unchanged: true, Author: authorRowOf(a, true)}, nil
		}
		updated, merged := a, false
		// the photo goes first, while the record is there: a rename that
		// merges into another author deletes this one, and its photo with it,
		// so clearing it afterwards asked for a record that was gone and
		// failed a call whose merge had already happened. The surviving
		// author's photo is theirs, not this one's, and is left alone.
		// The server answers 400 to removing a photo that is not there, and
		// clearing what is already blank is not a failure.
		if clearImage && a.ImagePath != "" {
			got, derr := record(gen.AuthorDeleteImage(ctx, a.Id))
			if derr != nil {
				return nil, editOut{}, derr
			}
			updated = cmp.Or(got.Author, updated)
		}
		if upd.Name != "" || upd.Description != nil || upd.Asin != nil {
			got, uerr := record(gen.AuthorUpdate(ctx, a.Id, upd))
			if uerr != nil {
				return nil, editOut{}, uerr
			}
			updated, merged = cmp.Or(got.Author, updated), got.Merged
		}
		// a new photo goes last, onto the record that is left: after a
		// rename that merged, that is the author merged into
		if imageURL != "" {
			was := updated
			got, ierr := record(gen.AuthorUploadImage(ctx, was.Id, abs.AuthorUploadImageRequest{Url: imageURL}))
			if ierr != nil {
				if upd.Name != "" || upd.Description != nil || upd.Asin != nil {
					return nil, editOut{}, fmt.Errorf("%q was changed, but setting the photo failed: %w", was.Name, ierr)
				}
				return nil, editOut{}, ierr
			}
			updated = cmp.Or(got.Author, was)
		}

		full, err := withBooks(ctx, gen, updated)
		if err != nil {
			return nil, editOut{}, err
		}
		return nil, editOut{Merged: merged, Author: authorRowOf(full, true)}, nil
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
		a, err := resolveAuthor(ctx, gen, in.Library, in.Author)
		if err != nil {
			return nil, matchOut{}, err
		}
		q := strings.TrimSpace(in.Query)
		if q == "" {
			q = a.Name
		}
		// the server answers null for nobody found, which reads as no name and no asin
		cand, err := record(gen.SearchFindAuthor(ctx, abs.SearchFindAuthorOptions{Q: q}))
		if err != nil {
			return nil, matchOut{}, err
		}

		out := matchOut{Author: authorRowOf(a, true)}
		if cand.Name != "" || cand.Asin != "" {
			out.Candidate = &matchCandidate{
				ASIN: cand.Asin, Name: cand.Name,
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
		a, err := resolveAuthor(ctx, gen, in.Library, in.Author)
		if err != nil {
			return nil, applyOut{}, err
		}
		got, err := record(gen.AuthorMatch(ctx, a.Id, abs.AuthorMatchRequest{Asin: asin, Region: in.Region}))
		if err != nil {
			return nil, applyOut{}, err
		}

		full, err := withBooks(ctx, gen, cmp.Or(got.Author, a))
		if err != nil {
			return nil, applyOut{}, err
		}
		return nil, applyOut{Updated: got.Updated, Author: authorRowOf(full, true)}, nil
	})

	type deleteOut struct {
		Deleted string `json:"deleted"`
	}
	add(r, deleteTool, &mcp.Tool{
		Name:        "author_delete",
		Description: "Delete an author record; their books stay but lose the author link. Usually author_edit (merge by rename) is the better fix for duplicates. Requires the delete permission.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in authorIn) (*mcp.CallToolResult, deleteOut, error) {
		a, err := resolveAuthor(ctx, gen, in.Library, in.Author)
		if err != nil {
			return nil, deleteOut{}, err
		}
		if _, err := gen.AuthorDelete(ctx, a.Id); err != nil {
			return nil, deleteOut{}, err
		}

		return nil, deleteOut{Deleted: a.Name}, nil
	})
}

func registerNarratorTools(r *registry) {
	gen := r.gen

	type narratorRow struct {
		Name  string `json:"name"`
		Books int    `json:"books"`
	}
	type listIn struct {
		Library string `json:"library,omitempty" jsonschema:"library name or id; optional when the server has one library"`
		Limit   int    `json:"limit,omitempty"   jsonschema:"page size, default 100, at most 1000"`
		Offset  int    `json:"offset,omitempty"  jsonschema:"skip this many narrators: a previous page's next_offset"`
	}
	type listOut struct {
		Total      int           `json:"total"`
		Offset     int           `json:"offset"`
		NextOffset int           `json:"next_offset,omitempty" jsonschema:"pass back as offset for the next page; absent on the last"`
		Narrators  []narratorRow `json:"narrators"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "narrator_list",
		Description: "List a library's narrators with the number of books each reads. The vocabulary to normalize against: near-duplicates like 'Jim Dale' and 'jim dale' show up as separate entries, and metadata_rename merges them.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listIn) (*mcp.CallToolResult, listOut, error) {
		lib, err := resolveLibrary(ctx, gen, in.Library)
		if err != nil {
			return nil, listOut{}, err
		}
		limit, offset := pageArgs(in.Limit, in.Offset, 100)
		restricted, err := restrictedKey(ctx, gen)
		if err != nil {
			return nil, listOut{}, err
		}
		if restricted {
			// the narrator route counts books this key may not see
			v, verr := sweepVisible(ctx, gen, lib)
			if verr != nil {
				return nil, listOut{}, verr
			}
			names, next := pageOf(keysOf(v.Narrators), limit, offset)
			out := listOut{Total: len(v.Narrators), Offset: offset, NextOffset: next, Narrators: make([]narratorRow, 0, len(names))}
			for _, name := range names {
				out.Narrators = append(out.Narrators, narratorRow{Name: name, Books: v.Narrators[name]})
			}
			return nil, out, nil
		}
		got, err := record(gen.LibraryGetNarrators(ctx, lib.Id))
		if err != nil {
			return nil, listOut{}, err
		}
		ns := got.Narrators

		// by name, as an account kept from some books is answered, so a page
		// is the same page however the server happened to list them
		slices.SortStableFunc(ns, func(a, b abs.LibraryGetNarratorsAnswerNarrators) int { return strings.Compare(a.Name, b.Name) })
		page, next := pageOf(ns, limit, offset)
		out := listOut{Total: len(ns), Offset: offset, NextOffset: next, Narrators: make([]narratorRow, 0, len(page))}
		for i := range page {
			out.Narrators = append(out.Narrators, narratorRow{Name: page[i].Name, Books: page[i].NumBooks})
		}

		return nil, out, nil
	})
}

// seriesBooks is a series' books in sequence order, each whole, with its whole
// series list. An item listing filtered by series comes back with that field
// collapsed to the one series matched (docs/README.md), which is fine for the
// sequence but not for anything that writes the list back: four Stormlight
// books lost their Cosmere link to an edit built from it. So the order comes
// from the filtered listing, every page of it, and the books from a batch
// fetch of the same ids.
func seriesBooks(ctx context.Context, gen *abs.Client, s *abs.Series) ([]abs.LibraryItemExpanded, error) {
	var listed []*abs.LibraryItemMinified
	if err := eachItem(ctx, gen, s.LibraryId, abs.LibraryGetLibraryItemsOptions{Sort: "sequence", Filter: abs.ListFilter("series", string(s.Id))}, func(it *abs.LibraryItemMinified) bool {
		listed = append(listed, it)
		return true
	}); err != nil {
		return nil, err
	}

	return wholeInOrder(ctx, gen, listed)
}

// wholeInOrder fetches the items a listing named whole, in the listing's order, in batches: a thousand expanded items in one reply is what a
// reverse proxy times out on.
func wholeInOrder(ctx context.Context, gen *abs.Client, listed []*abs.LibraryItemMinified) ([]abs.LibraryItemExpanded, error) {
	ids := make([]abs.LibraryItemID, 0, len(listed))
	for _, it := range listed {
		ids = append(ids, it.Id)
	}
	byID := make(map[abs.LibraryItemID]abs.LibraryItemExpanded, len(ids))
	for chunk := range slices.Chunk(ids, sweepBatchSize) {
		full, err := itemsBatch(ctx, gen, chunk)
		if err != nil {
			return nil, err
		}
		for i := range full {
			byID[full[i].Id] = full[i]
		}
	}

	out := make([]abs.LibraryItemExpanded, 0, len(listed))
	for _, it := range listed {
		whole, ok := byID[it.Id]
		if !ok {
			// the batch leaves out what is gone
			return nil, fmt.Errorf("%q (%s) was not in the server's reply for its full series list: deleted meanwhile?", it.Title(), it.Id)
		}
		out = append(out, whole)
	}

	return out, nil
}

// mergeSeriesRefs is a book's series list with from replaced by into, in
// its place: the from entry's number goes to into unless into is already
// there with a number of its own, in which case the from entry just goes.
// Every other series is kept as it was.
func mergeSeriesRefs(refs []abs.SeriesMinimal, from, into *abs.Series) []abs.SeriesMinimal {
	// by id where the book's entry has one: "Mistborn " and "Mistborn" are
	// two records, and a name compared trimmed would take the one for the
	// other. An entry with no id goes by its name as written, and only when
	// neither series has that name exactly, by the name trimmed and folded
	sameSeries := func(ref abs.SeriesMinimal, s *abs.Series) bool {
		switch {
		case ref.Id != "":
			return ref.Id == s.Id
		case ref.Name == from.Name || ref.Name == into.Name:
			return ref.Name == s.Name
		}
		return strings.EqualFold(strings.TrimSpace(ref.Name), strings.TrimSpace(s.Name))
	}
	out := make([]abs.SeriesMinimal, 0, len(refs))
	seq, fromAt, intoAt := "", -1, -1
	for _, ref := range refs {
		switch {
		case sameSeries(ref, from):
			seq, fromAt = ref.Sequence, len(out)
			out = append(out, abs.SeriesMinimal{Name: into.Name})
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
	gen := r.gen

	type listIn struct {
		Library string `json:"library,omitempty" jsonschema:"library name or id; optional when the server has one library"`
		Sort    string `json:"sort,omitempty"    jsonschema:"name (default), books, duration, added, last_book_added, last_book_updated, random"`
		Desc    bool   `json:"desc,omitempty"`
		Limit   int    `json:"limit,omitempty"   jsonschema:"page size, default 50, at most 1000"`
		Offset  int    `json:"offset,omitempty"  jsonschema:"skip this many series in each library: a previous page's next_offset"`
	}
	type seriesRow struct {
		ID       string   `json:"id"`
		Name     string   `json:"name"`
		Library  string   `json:"library,omitempty"    jsonschema:"when more than one library is listed"`
		Books    int      `json:"books"`
		Duration int      `json:"duration_s,omitempty" jsonschema:"its books' lengths added together, in seconds"`
		Author   string   `json:"author,omitempty"`
		Sequence []string `json:"sequence,omitempty"   jsonschema:"the sequence numbers present; gaps mean missing books"`
	}
	type listOut struct {
		Total      int         `json:"total"`
		Offset     int         `json:"offset"`
		NextOffset int         `json:"next_offset,omitempty" jsonschema:"pass back as offset for the next page; absent when no library has more"`
		Series     []seriesRow `json:"series"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "series_list",
		Description: "List series with book counts and the sequence numbers present, so gaps (missing books) stand out. One library by name or id, or every book library when none is given; limit and offset page each library.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listIn) (*mcp.CallToolResult, listOut, error) {
		libs, err := resolveLibraries(ctx, gen, in.Library)
		if err != nil {
			return nil, listOut{}, err
		}
		sortBy, ok := seriesSorts[strings.ToLower(strings.TrimSpace(in.Sort))]
		if !ok {
			return nil, listOut{}, fmt.Errorf("unknown sort %q; choose one of: name, books, duration, added, last_book_added, last_book_updated, random", in.Sort)
		}
		limit, offset := pageArgs(in.Limit, in.Offset, 50)

		out := listOut{Offset: offset, Series: []seriesRow{}}
		for i := range libs {
			lib := &libs[i]
			if lib.IsPodcast() {
				continue
			}
			series, total, err := window(offset, limit, func(page int) ([]abs.Series, int, error) {
				got, err := record(gen.LibraryGetAllSeriesForLibrary(ctx, lib.Id, abs.LibraryGetAllSeriesForLibraryOptions{Limit: new(limit), Page: new(page), Sort: sortBy, Desc: descFlag(in.Desc)}))
				return got.Results, got.Total, err
			})
			if err != nil {
				return nil, listOut{}, err
			}
			out.Total += total
			if mcpregistry.NextOffset(offset, len(series), total) > 0 {
				out.NextOffset = offset + limit
			}
			for j := range series {
				s := &series[j]
				row := seriesRow{ID: string(s.Id), Name: s.Name, Books: len(s.Books), Duration: wholeSec(s.TotalDuration)}
				if len(libs) > 1 {
					row.Library = lib.Name
				}
				for k := range s.Books {
					filledMinified(&s.Books[k])
					m := s.Books[k].Media.Metadata
					if row.Author == "" {
						row.Author = m.AuthorDisplay()
					}
					// the listing's books are minified, with the series only
					// as the joined "Name #1" string, so by name as well as id
					for _, ref := range seriesRefsOf(&s.Books[k]) {
						if (ref.Id == s.Id || strings.EqualFold(ref.Name, s.Name)) && ref.Sequence != "" {
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
		s, err := resolveSeries(ctx, gen, in.Library, in.Series)
		if err != nil {
			return nil, getOut{}, err
		}
		books, err := seriesBooks(ctx, gen, s)
		if err != nil {
			return nil, getOut{}, err
		}

		finished := map[string]bool{}
		if s.Progress != nil {
			for _, id := range s.Progress.LibraryItemIdsFinished {
				finished[id] = true
			}
		}

		out := getOut{ID: string(s.Id), Name: s.Name, Description: clip(plain(s.Description), descriptionCap), Books: []bookRow{}}
		for i := range books {
			it := &books[i]
			row := bookRow{itemSummary: summarizeWhole(it), Finished: finished[string(it.Id)]}
			for _, ref := range it.Media.Metadata.Series {
				if ref.Id == s.Id || strings.EqualFold(ref.Name, s.Name) {
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
		ID        string `json:"id"`
		Name      string `json:"name"`
		Unchanged bool   `json:"unchanged,omitempty" jsonschema:"the series already had this name and nothing else was asked for: nothing was written"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "series_edit",
		Description: "Rename a series or set its description. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in editIn) (*mcp.CallToolResult, editOut, error) {
		name := strings.TrimSpace(in.Name)
		if in.Name != "" && name == "" {
			return nil, editOut{}, errors.New("name cannot be blank")
		}
		s, err := resolveSeries(ctx, gen, in.Library, in.Series)
		if err != nil {
			return nil, editOut{}, err
		}
		upd := abs.SeriesUpdateRequest{Name: name, Description: in.Description}
		if upd.Name == "" && upd.Description == "" {
			return nil, editOut{}, errors.New("nothing to change: pass name or description")
		}
		// the name it already has, spaces and all, is no rename: a
		// description set beside it leaves the name as it is
		if upd.Name != "" && (upd.Name == s.Name || in.Name == s.Name) {
			upd.Name = ""
		}
		// a rename that changes nothing writes nothing, and says so rather
		// than report a rename done
		if upd.Name == "" && upd.Description == "" {
			return nil, editOut{ID: string(s.Id), Name: s.Name, Unchanged: true}, nil
		}
		if upd.Name != "" && upd.Name != s.Name {
			// two series with one name is not a merge, it is two series
			// with one name; series_merge is how books move
			others, ferr := allSeries(ctx, gen, s.LibraryId)
			if ferr != nil {
				return nil, editOut{}, ferr
			}
			for _, other := range others {
				if other.Id != s.Id && strings.EqualFold(strings.TrimSpace(other.Name), name) {
					return nil, editOut{}, fmt.Errorf("a series named %q already exists (%s): series_merge from=%s into=%s moves the books of %q there instead", other.Name, other.Id, s.Id, other.Id, s.Name)
				}
			}
		}
		updated, err := record(gen.SeriesUpdate(ctx, s.Id, upd))
		if err != nil {
			return nil, editOut{}, err
		}

		return nil, editOut{ID: string(updated.Id), Name: updated.Name}, nil
	})

	type mergeIn struct {
		From    string `json:"from"              jsonschema:"series id or exact name whose books move; it is empty afterwards"`
		Into    string `json:"into"              jsonschema:"series id or exact name the books move into"`
		Library string `json:"library,omitempty" jsonschema:"narrow a name lookup to one library"`
	}
	type mergeOut struct {
		From        string   `json:"from"`
		Into        string   `json:"into"`
		IntoID      string   `json:"into_id"`
		Moved       int      `json:"moved"`
		Books       []string `json:"books"          jsonschema:"each book as it now stands in the series, 'Title #2'"`
		FromRemoved bool     `json:"from_removed"   jsonschema:"the emptied series is gone from the server; false means it is still listed, with no books"`
		Note        string   `json:"note,omitempty"`
	}
	add(r, writeTool, &mcp.Tool{
		Name: "series_merge",
		Description: "Move every book of one series into another, keeping each book's number and every other series it is in. The server usually drops the emptied series; from_removed says whether it did, read back after the move. " +
			"For two spellings of one series that audit_series names reports ('The Wheel of Time' into 'Wheel of Time'), and for a one-book 'Skyward Series' beside 'Skyward'. " +
			"A book already in both keeps its number in the target, or takes the one it had if the target had none. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in mergeIn) (*mcp.CallToolResult, mergeOut, error) {
		// every book of the series is read and its series list written back
		defer r.locks.All()()
		from, err := resolveSeries(ctx, gen, in.Library, in.From)
		if err != nil {
			return nil, mergeOut{}, fmt.Errorf("from: %w", err)
		}
		into, err := resolveSeries(ctx, gen, in.Library, in.Into)
		if err != nil {
			return nil, mergeOut{}, fmt.Errorf("into: %w", err)
		}
		if from.Id == into.Id {
			return nil, mergeOut{}, fmt.Errorf("%q is one series: nothing to merge", from.Name)
		}
		books, err := seriesBooks(ctx, gen, from)
		if err != nil {
			return nil, mergeOut{}, err
		}
		if len(books) == 0 {
			return nil, mergeOut{}, fmt.Errorf("%q has no books", from.Name)
		}

		out := mergeOut{From: from.Name, Into: into.Name, IntoID: string(into.Id), Books: make([]string, 0, len(books))}
		updates := make([]abs.LibraryItemBatchUpdateRequest, 0, len(books))
		for i := range books {
			it := &books[i]
			refs := mergeSeriesRefs(it.Media.Metadata.Series, from, into)
			updates = append(updates, abs.LibraryItemBatchUpdateRequest{Id: string(it.Id), MediaPayload: &abs.MediaUpdate{Metadata: &abs.MediaUpdateMetadata{Series: refs}}})
			for _, ref := range refs {
				if strings.EqualFold(ref.Name, into.Name) {
					out.Books = append(out.Books, it.Title()+seqSuffix(ref.Sequence))
				}
			}
		}
		// in batches, as the sweeps write: one request carrying hundreds of
		// books is what a reverse proxy times out on. A batch that fails
		// leaves the ones before it moved, and the error says how many.
		for chunk := range slices.Chunk(updates, sweepBatchSize) {
			got, berr := record(gen.LibraryItemBatchUpdate(ctx, chunk))
			n := got.Updates
			if berr != nil {
				return nil, mergeOut{}, fmt.Errorf("%d of the %d books of %q were moved into %q before this, and stay there; the rest were not: %w", out.Moved, len(books), from.Name, into.Name, berr)
			}
			out.Moved += n
			// the server counts the books it changed: fewer than sent is
			// books left behind, whatever the list below would say
			if n != len(chunk) {
				return nil, mergeOut{}, fmt.Errorf("the server moved %d of the %d books it was sent from %q into %q (%d moved in all), without saying why the rest were not: read the series with series_get", n, len(chunk), from.Name, into.Name, out.Moved)
			}
		}

		// read back rather than promised: a series can outlive its books
		left, err := allSeries(ctx, gen, from.LibraryId)
		if err != nil {
			return nil, mergeOut{}, fmt.Errorf("all %d books moved into %q, but the series could not be read back to see whether %q is gone: %w", out.Moved, into.Name, from.Name, err)
		}
		out.FromRemoved = !slices.ContainsFunc(left, func(s abs.Series) bool { return s.Id == from.Id })
		if !out.FromRemoved {
			out.Note = fmt.Sprintf("the server still lists %q (%s), with no books; a lookup by its name refuses it", from.Name, from.Id)
		}

		return nil, out, nil
	})
}

// sameName reports whether two author names are the same name: case,
// punctuation and spacing aside, so "Ursula K. Le Guin" is "ursula k le guin",
// but "Emily Andras" is not "Emily Adrian".
func sameName(a, b string) bool {
	return strings.Join(strings.Fields(norm(a)), " ") == strings.Join(strings.Fields(norm(b)), " ")
}

// fullSeriesLists are the series each item of a listing filtered by series is in, every one, by item: the listing gives only the one it was
// filtered by. An item the server has deleted meanwhile is an error, as its one series is not its list.
func fullSeriesLists(ctx context.Context, gen *abs.Client, items []abs.LibraryItemMinified) (map[abs.LibraryItemID][]string, error) {
	ids := make([]abs.LibraryItemID, 0, len(items))
	for i := range items {
		ids = append(ids, items[i].Id)
	}
	out := make(map[abs.LibraryItemID][]string, len(ids))
	// in batches: the fetch is of the expanded items, and a thousand of those
	// in one reply is what a reverse proxy times out on
	for chunk := range slices.Chunk(ids, sweepBatchSize) {
		full, err := itemsBatch(ctx, gen, chunk)
		if err != nil {
			return nil, err
		}
		for i := range full {
			out[full[i].Id] = full[i].Media.Metadata.SeriesDisplay()
		}
	}
	for i := range items {
		if _, ok := out[items[i].Id]; !ok {
			return nil, fmt.Errorf("%q (%s) was not in the server's reply for its full series list: deleted meanwhile?", items[i].Title(), items[i].Id)
		}
	}

	return out, nil
}
