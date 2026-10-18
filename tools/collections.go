package tools

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	api "github.com/katbyte/abs-mcp/sdk/abs"
	"github.com/katbyte/go-kt/lock"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// resolveCollection finds a collection by name (case-insensitive) or id.
func resolveCollection(ctx context.Context, gen *api.Client, nameOrID string) (*api.Collection, error) {
	nameOrID = strings.TrimSpace(nameOrID)
	if nameOrID == "" {
		return nil, errors.New("collection name or id is required")
	}
	if looksLikeID(nameOrID) {
		return getCollection(ctx, gen, api.CollectionID(nameOrID))
	}
	all, err := record(gen.CollectionFindAll(ctx))
	if err != nil {
		return nil, err
	}

	return oneNamed("collection", nameOrID, all.Collections, func(c *api.Collection) string { return c.Name }, func(c *api.Collection) string { return string(c.Id) })
}

// getCollection is one collection, with its books.
func getCollection(ctx context.Context, gen *api.Client, id api.CollectionID) (*api.Collection, error) {
	c, err := record(gen.CollectionFindOne(ctx, id))
	if err != nil {
		return nil, err
	}

	return &c, nil
}

// libraryCollections are the collections of one library.
func libraryCollections(ctx context.Context, gen *api.Client, library api.LibraryID) ([]api.Collection, error) {
	got, err := record(gen.LibraryGetCollectionsForLibrary(ctx, library))
	if err != nil {
		return nil, err
	}

	return got.Results, nil
}

// libraryBooks resolves ids or titles to the books they name in one library.
// The server's batch routes drop what they cannot use without saying so - an
// id from another library, a podcast, an id that names nothing - so each is
// checked here and refused by name. A book named twice is kept once.
func libraryBooks(ctx context.Context, gen *api.Client, libraryID api.LibraryID, refs []string) ([]api.LibraryItemExpanded, error) {
	if len(refs) == 0 {
		return nil, errors.New("at least one item is required")
	}
	out := make([]api.LibraryItemExpanded, 0, len(refs))
	for _, ref := range refs {
		var it *api.LibraryItemExpanded
		var err error
		if looksLikeID(ref) {
			it, err = getItem(ctx, gen, api.LibraryItemID(ref))
		} else {
			it, err = resolveItemToChange(ctx, gen, string(libraryID), ref)
		}
		if err != nil {
			return nil, err
		}
		if it == nil {
			return nil, fmt.Errorf("%q: the lookup gave neither a book nor an error", ref)
		}

		switch {
		case it.LibraryId != libraryID:
			return nil, fmt.Errorf("%q is in another library (%s): a collection holds books from its own library", it.Title(), it.LibraryId)
		case it.IsPodcast():
			return nil, fmt.Errorf("%q is a podcast: a collection holds books", it.Title())
		}
		if !slices.ContainsFunc(out, func(o api.LibraryItemExpanded) bool { return o.Id == it.Id }) {
			out = append(out, *it)
		}
	}

	return out, nil
}

// bookIDs lists the ids of items, in order.
func bookIDs(items []api.LibraryItemExpanded) []string {
	ids := make([]string, 0, len(items))
	for i := range items {
		ids = append(ids, string(items[i].Id))
	}
	return ids
}

// titles lists the titles of items, in order.
func titles(items []api.LibraryItemExpanded) []string {
	out := make([]string, 0, len(items))
	for i := range items {
		out = append(out, items[i].Title())
	}
	return out
}

func registerCollectionTools(r *registry) {
	gen := r.gen

	type row struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Library     string `json:"library_id"`
		Books       int    `json:"books"`
		Description string `json:"description,omitempty"`
		Updated     string `json:"updated,omitempty"`
	}
	rowOf := func(c *api.Collection) row {
		return row{ID: string(c.Id), Name: c.Name, Library: string(c.LibraryId), Books: len(c.Books), Description: c.Description, Updated: fmtDate(c.LastUpdate)}
	}

	type listIn struct {
		Library string `json:"library,omitempty" jsonschema:"restrict to one library by name or id"`
		pageIn
	}
	type listOut struct {
		Total       int   `json:"total"`
		Offset      int   `json:"offset"`
		NextOffset  int   `json:"next_offset,omitempty" jsonschema:"pass back as offset for the next page; absent on the last"`
		Collections []row `json:"collections"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "collection_list",
		Description: "List collections (shared, curated groups of books) with their sizes.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listIn) (*mcp.CallToolResult, listOut, error) {
		var cols []api.Collection
		if in.Library != "" {
			lib, err := resolveLibrary(ctx, gen, in.Library)
			if err != nil {
				return nil, listOut{}, err
			}
			if cols, err = libraryCollections(ctx, gen, lib.Id); err != nil {
				return nil, listOut{}, err
			}
		} else {
			all, err := record(gen.CollectionFindAll(ctx))
			if err != nil {
				return nil, listOut{}, err
			}
			cols = all.Collections
		}
		limit, offset := pageArgs(in.Limit, in.Offset, 50)
		page, next := pageOf(cols, limit, offset)
		out := listOut{Total: len(cols), Offset: offset, NextOffset: next, Collections: []row{}}
		for i := range page {
			out.Collections = append(out.Collections, rowOf(&page[i]))
		}

		return nil, out, nil
	})

	type getIn struct {
		Collection string `json:"collection" jsonschema:"collection name (case-insensitive) or id"`
	}
	type getOut struct {
		row
		Items []itemSummary `json:"items" jsonschema:"in collection order"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "collection_get",
		Description: "A collection's books in order.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in getIn) (*mcp.CallToolResult, getOut, error) {
		c, err := resolveCollection(ctx, gen, in.Collection)
		if err != nil {
			return nil, getOut{}, err
		}

		return nil, getOut{row: rowOf(c), Items: summarizeAllWhole(c.Books)}, nil
	})

	type createIn struct {
		Name        string   `json:"name"`
		Library     string   `json:"library,omitempty"     jsonschema:"library name or id; optional when the server has one library"`
		Description string   `json:"description,omitempty"`
		Items       []string `json:"items"                 jsonschema:"its books by id or exact title, in order; at least one, as Audiobookshelf will not create an empty collection"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "collection_create",
		Description: "Create a collection with at least one book. A name already used by a collection in the library is refused: two collections with one name cannot be told apart by name. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in createIn) (*mcp.CallToolResult, row, error) {
		name := strings.TrimSpace(in.Name)
		if name == "" {
			return nil, row{}, errors.New("name is required")
		}
		lib, err := resolveLibrary(ctx, gen, in.Library)
		if err != nil {
			return nil, row{}, err
		}
		if len(in.Items) == 0 {
			return nil, row{}, errors.New("items is required: Audiobookshelf will not create a collection with no books")
		}
		existing, err := libraryCollections(ctx, gen, lib.Id)
		if err != nil {
			return nil, row{}, err
		}
		for i := range existing {
			if strings.EqualFold(strings.TrimSpace(existing[i].Name), name) {
				return nil, row{}, fmt.Errorf("a collection named %q already exists in %s (%s): collection_edit add_items adds books to it", existing[i].Name, lib.Name, existing[i].Id)
			}
		}
		books, err := libraryBooks(ctx, gen, lib.Id, in.Items)
		if err != nil {
			return nil, row{}, err
		}
		c, err := record(gen.CollectionCreate(ctx, api.CollectionCreateRequest{LibraryId: lib.Id, Name: name, Description: in.Description, Books: bookIDs(books)}))
		if err != nil {
			return nil, row{}, err
		}

		return nil, rowOf(&c), nil
	})

	type editIn struct {
		getIn
		Name        string   `json:"name,omitempty"`
		Description string   `json:"description,omitempty"`
		AddItems    []string `json:"add_items,omitempty"    jsonschema:"books to add, by id or exact title"`
		RemoveItems []string `json:"remove_items,omitempty" jsonschema:"books to take out, by id or exact title; they stay in the library"`
	}
	type editOut struct {
		row
		Added       []string `json:"added,omitempty"`
		AlreadyHeld []string `json:"already_held,omitempty" jsonschema:"books asked for that the collection already held, left where they were"`
		Removed     []string `json:"removed,omitempty"`
		NotHeld     []string `json:"not_held,omitempty"     jsonschema:"books asked to be removed that the collection did not hold"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "collection_edit",
		Description: "Change a collection: rename it, change its description, add books to it or take books out of it. The answer is the collection as it now is, with which books were added or removed and which it already held or never did. Removing only changes the collection; the books stay in the library. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in editIn) (*mcp.CallToolResult, editOut, error) {
		// a name of only spaces would leave a collection nothing can name
		name := strings.TrimSpace(in.Name)
		switch {
		case in.Name != "" && name == "":
			return nil, editOut{}, errors.New("name is blank: pass a name, or leave it out to keep the one it has")
		case name == "" && in.Description == "" && len(in.AddItems) == 0 && len(in.RemoveItems) == 0:
			return nil, editOut{}, errors.New("nothing to change: pass name, description, add_items or remove_items")
		}
		c, err := resolveCollection(ctx, gen, in.Collection)
		if err != nil {
			return nil, editOut{}, err
		}
		// what it holds is judged once held, so two adds of one book at once
		// do not both say they added it
		defer r.locks.By(lock.ID[api.Collection](string(c.Id)))()
		if c, err = getCollection(ctx, gen, c.Id); err != nil {
			return nil, editOut{}, err
		}
		if name != "" {
			others, cerr := libraryCollections(ctx, gen, c.LibraryId)
			if cerr != nil {
				return nil, editOut{}, cerr
			}
			for i := range others {
				if others[i].Id != c.Id && strings.EqualFold(strings.TrimSpace(others[i].Name), name) {
					return nil, editOut{}, fmt.Errorf("a collection named %q already exists (%s): two collections with one name cannot be told apart by name", others[i].Name, others[i].Id)
				}
			}
		}

		// everything is resolved before anything is sent, so a book named
		// wrongly changes nothing at all
		var adding, removing []api.LibraryItemExpanded
		if len(in.AddItems) > 0 {
			if adding, err = libraryBooks(ctx, gen, c.LibraryId, in.AddItems); err != nil {
				return nil, editOut{}, err
			}
		}
		if len(in.RemoveItems) > 0 {
			if removing, err = libraryBooks(ctx, gen, c.LibraryId, in.RemoveItems); err != nil {
				return nil, editOut{}, err
			}
		}
		for i := range adding {
			if slices.ContainsFunc(removing, func(b api.LibraryItemExpanded) bool { return b.Id == adding[i].Id }) {
				return nil, editOut{}, fmt.Errorf("%q is in both add_items and remove_items; one or the other", adding[i].Title())
			}
		}
		holds := func(c *api.Collection, id api.LibraryItemID) bool {
			return slices.ContainsFunc(c.Books, func(b api.LibraryItemExpanded) bool { return b.Id == id })
		}
		// the server answers 200 to adding a book it holds and to removing one
		// it does not, and changes nothing: the split is made here, so what
		// comes back says what was actually done
		out := editOut{}
		var add, remove []api.LibraryItemExpanded
		for i := range adding {
			if holds(c, adding[i].Id) {
				out.AlreadyHeld = append(out.AlreadyHeld, adding[i].Title())
			} else {
				add = append(add, adding[i])
			}
		}
		for i := range removing {
			if holds(c, removing[i].Id) {
				remove = append(remove, removing[i])
			} else {
				out.NotHeld = append(out.NotHeld, removing[i].Title())
			}
		}

		// each change after the first is said to have followed it when it
		// fails, so a partial edit is never reported as none
		var done []string
		failed := func(what string, err error) error {
			if len(done) == 0 {
				return err
			}
			return fmt.Errorf("%s, but %s failed: %w", strings.Join(done, " and "), what, err)
		}
		if name != "" || in.Description != "" {
			updated, err := record(gen.CollectionUpdate(ctx, c.Id, api.CollectionUpdateRequest{Name: name, Description: in.Description}))
			if err != nil {
				return nil, editOut{}, err
			}
			c = &updated
			done = append(done, "the name or description was changed")
		}
		if len(add) > 0 {
			updated, err := record(gen.CollectionAddBatch(ctx, c.Id, api.CollectionAddBatchRequest{Books: bookIDs(add)}))
			if err != nil {
				return nil, editOut{}, failed("adding the books", err)
			}
			c = &updated
			for i := range add {
				if !holds(c, add[i].Id) {
					return nil, editOut{}, failed("adding the books", fmt.Errorf("the server accepted the add but %q is not in the collection afterwards", add[i].Title()))
				}
			}
			out.Added = titles(add)
			done = append(done, fmt.Sprintf("%d books were added", len(add)))
		}
		if len(remove) > 0 {
			updated, err := record(gen.CollectionRemoveBatch(ctx, c.Id, api.CollectionRemoveBatchRequest{Books: bookIDs(remove)}))
			if err != nil {
				return nil, editOut{}, failed("removing the books", err)
			}
			c = &updated
			for i := range remove {
				if holds(c, remove[i].Id) {
					return nil, editOut{}, failed("removing the books", fmt.Errorf("the server accepted the removal but %q is still in the collection afterwards", remove[i].Title()))
				}
			}
			out.Removed = titles(remove)
		}
		out.row = rowOf(c)

		return nil, out, nil
	})

	type deleteOut struct {
		Deleted string `json:"deleted"`
		ID      string `json:"id"`
	}
	// a delete tool, registered only with --enable-delete: a collection is
	// shared by every account, and what it gathered is not kept anywhere else
	add(r, deleteTool, &mcp.Tool{
		Name:        "collection_delete",
		Description: "Delete a collection, which every account shares (its books stay in the library). Requires the delete permission.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in getIn) (*mcp.CallToolResult, deleteOut, error) {
		c, err := resolveCollection(ctx, gen, in.Collection)
		if err != nil {
			return nil, deleteOut{}, err
		}
		if _, err := gen.CollectionDelete(ctx, c.Id); err != nil {
			return nil, deleteOut{}, err
		}
		// read back rather than trusted: the answer to a delete is not the
		// collection gone
		left, err := libraryCollections(ctx, gen, c.LibraryId)
		if err != nil {
			return nil, deleteOut{}, fmt.Errorf("deleted %q, but reading the collections back failed: %w", c.Name, err)
		}
		for i := range left {
			if left[i].Id == c.Id {
				return nil, deleteOut{}, fmt.Errorf("the server accepted the delete but %q (%s) is still listed", c.Name, c.Id)
			}
		}

		return nil, deleteOut{Deleted: c.Name, ID: string(c.Id)}, nil
	})
}
