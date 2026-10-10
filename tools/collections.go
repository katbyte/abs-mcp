package tools

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/katbyte/abs-mcp/sdk/abs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// resolveCollection finds a collection by name (case-insensitive) or id.
func resolveCollection(ctx context.Context, client *abs.Client, nameOrID string) (*abs.Collection, error) {
	nameOrID = strings.TrimSpace(nameOrID)
	if nameOrID == "" {
		return nil, errors.New("collection name or id is required")
	}
	if looksLikeID(nameOrID) {
		return client.Collection(ctx, nameOrID)
	}
	cols, err := client.Collections(ctx, "")
	if err != nil {
		return nil, err
	}

	return oneNamed("collection", nameOrID, cols, func(c *abs.Collection) string { return c.Name }, func(c *abs.Collection) string { return c.ID })
}

// libraryBooks resolves ids or titles to the books they name in one library.
// The server's batch routes drop what they cannot use without saying so - an
// id from another library, a podcast, an id that names nothing - so each is
// checked here and refused by name. A book named twice is kept once.
func libraryBooks(ctx context.Context, client *abs.Client, libraryID string, refs []string) ([]abs.Item, error) {
	if len(refs) == 0 {
		return nil, errors.New("at least one item is required")
	}
	out := make([]abs.Item, 0, len(refs))
	for _, ref := range refs {
		var it *abs.Item
		var err error
		if looksLikeID(ref) {
			it, err = client.Item(ctx, ref)
		} else {
			it, err = resolveItemToChange(ctx, client, libraryID, ref)
		}
		if err != nil {
			return nil, err
		}
		switch {
		case it.LibraryID != libraryID:
			return nil, fmt.Errorf("%q is in another library (%s): a collection holds books from its own library", it.Title(), it.LibraryID)
		case it.IsPodcast():
			return nil, fmt.Errorf("%q is a podcast: a collection holds books", it.Title())
		}
		if !slices.ContainsFunc(out, func(o abs.Item) bool { return o.ID == it.ID }) {
			out = append(out, *it)
		}
	}

	return out, nil
}

// bookIDs lists the ids of items, in order.
func bookIDs(items []abs.Item) []string {
	ids := make([]string, 0, len(items))
	for i := range items {
		ids = append(ids, items[i].ID)
	}
	return ids
}

// titles lists the titles of items, in order.
func titles(items []abs.Item) []string {
	out := make([]string, 0, len(items))
	for i := range items {
		out = append(out, items[i].Title())
	}
	return out
}

func registerCollectionTools(r *registry) {
	client := r.client

	type row struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Library     string `json:"library_id"`
		Books       int    `json:"books"`
		Description string `json:"description,omitempty"`
		Updated     string `json:"updated,omitempty"`
	}
	rowOf := func(c *abs.Collection) row {
		return row{ID: c.ID, Name: c.Name, Library: c.LibraryID, Books: len(c.Books), Description: c.Description, Updated: fmtDate(c.LastUpdate)}
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
		libID := ""
		if in.Library != "" {
			lib, err := resolveLibrary(ctx, client, in.Library)
			if err != nil {
				return nil, listOut{}, err
			}
			libID = lib.ID
		}
		cols, err := client.Collections(ctx, libID)
		if err != nil {
			return nil, listOut{}, err
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
		c, err := resolveCollection(ctx, client, in.Collection)
		if err != nil {
			return nil, getOut{}, err
		}

		return nil, getOut{row: rowOf(c), Items: summarizeAll(c.Books)}, nil
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
		lib, err := resolveLibrary(ctx, client, in.Library)
		if err != nil {
			return nil, row{}, err
		}
		if len(in.Items) == 0 {
			return nil, row{}, errors.New("items is required: Audiobookshelf will not create a collection with no books")
		}
		existing, err := client.Collections(ctx, lib.ID)
		if err != nil {
			return nil, row{}, err
		}
		for i := range existing {
			if strings.EqualFold(strings.TrimSpace(existing[i].Name), name) {
				return nil, row{}, fmt.Errorf("a collection named %q already exists in %s (%s): collection_edit add_items adds books to it", existing[i].Name, lib.Name, existing[i].ID)
			}
		}
		books, err := libraryBooks(ctx, client, lib.ID, in.Items)
		if err != nil {
			return nil, row{}, err
		}
		c, err := client.CreateCollection(ctx, lib.ID, name, in.Description, bookIDs(books))
		if err != nil {
			return nil, row{}, err
		}

		return nil, rowOf(c), nil
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
		c, err := resolveCollection(ctx, client, in.Collection)
		if err != nil {
			return nil, editOut{}, err
		}
		// what it holds is judged once held, so two adds of one book at once
		// do not both say they added it
		defer r.locks.By(c)()
		if c, err = client.Collection(ctx, c.ID); err != nil {
			return nil, editOut{}, err
		}
		if name != "" {
			others, cerr := client.Collections(ctx, c.LibraryID)
			if cerr != nil {
				return nil, editOut{}, cerr
			}
			for i := range others {
				if others[i].ID != c.ID && strings.EqualFold(strings.TrimSpace(others[i].Name), name) {
					return nil, editOut{}, fmt.Errorf("a collection named %q already exists (%s): two collections with one name cannot be told apart by name", others[i].Name, others[i].ID)
				}
			}
		}

		// everything is resolved before anything is sent, so a book named
		// wrongly changes nothing at all
		var adding, removing []abs.Item
		if len(in.AddItems) > 0 {
			if adding, err = libraryBooks(ctx, client, c.LibraryID, in.AddItems); err != nil {
				return nil, editOut{}, err
			}
		}
		if len(in.RemoveItems) > 0 {
			if removing, err = libraryBooks(ctx, client, c.LibraryID, in.RemoveItems); err != nil {
				return nil, editOut{}, err
			}
		}
		for i := range adding {
			if slices.ContainsFunc(removing, func(b abs.Item) bool { return b.ID == adding[i].ID }) {
				return nil, editOut{}, fmt.Errorf("%q is in both add_items and remove_items; one or the other", adding[i].Title())
			}
		}
		holds := func(c *abs.Collection, id string) bool {
			return slices.ContainsFunc(c.Books, func(b abs.Item) bool { return b.ID == id })
		}
		// the server answers 200 to adding a book it holds and to removing one
		// it does not, and changes nothing: the split is made here, so what
		// comes back says what was actually done
		out := editOut{}
		var add, remove []abs.Item
		for i := range adding {
			if holds(c, adding[i].ID) {
				out.AlreadyHeld = append(out.AlreadyHeld, adding[i].Title())
			} else {
				add = append(add, adding[i])
			}
		}
		for i := range removing {
			if holds(c, removing[i].ID) {
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
			if c, err = client.UpdateCollection(ctx, c.ID, strPtr(name), strPtr(in.Description)); err != nil {
				return nil, editOut{}, err
			}
			done = append(done, "the name or description was changed")
		}
		if len(add) > 0 {
			if c, err = client.AddToCollection(ctx, c.ID, bookIDs(add)); err != nil {
				return nil, editOut{}, failed("adding the books", err)
			}
			for i := range add {
				if !holds(c, add[i].ID) {
					return nil, editOut{}, failed("adding the books", fmt.Errorf("the server accepted the add but %q is not in the collection afterwards", add[i].Title()))
				}
			}
			out.Added = titles(add)
			done = append(done, fmt.Sprintf("%d books were added", len(add)))
		}
		if len(remove) > 0 {
			if c, err = client.RemoveFromCollection(ctx, c.ID, bookIDs(remove)); err != nil {
				return nil, editOut{}, failed("removing the books", err)
			}
			for i := range remove {
				if holds(c, remove[i].ID) {
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
		c, err := resolveCollection(ctx, client, in.Collection)
		if err != nil {
			return nil, deleteOut{}, err
		}
		if err := client.DeleteCollection(ctx, c.ID); err != nil {
			return nil, deleteOut{}, err
		}
		// read back rather than trusted: the answer to a delete is not the
		// collection gone
		left, err := client.Collections(ctx, c.LibraryID)
		if err != nil {
			return nil, deleteOut{}, fmt.Errorf("deleted %q, but reading the collections back failed: %w", c.Name, err)
		}
		for i := range left {
			if left[i].ID == c.ID {
				return nil, deleteOut{}, fmt.Errorf("the server accepted the delete but %q (%s) is still listed", c.Name, c.ID)
			}
		}

		return nil, deleteOut{Deleted: c.Name, ID: c.ID}, nil
	})
}
