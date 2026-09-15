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
			it, err = resolveItem(ctx, client, libraryID, ref)
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
	}
	type listOut struct {
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
		out := listOut{Collections: []row{}}
		for i := range cols {
			out.Collections = append(out.Collections, rowOf(&cols[i]))
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
				return nil, row{}, fmt.Errorf("a collection named %q already exists in %s (%s): collection_books_edit adds books to it", existing[i].Name, lib.Name, existing[i].ID)
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
		Name        string `json:"name,omitempty"`
		Description string `json:"description,omitempty"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "collection_edit",
		Description: "Rename a collection or change its description. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in editIn) (*mcp.CallToolResult, row, error) {
		c, err := resolveCollection(ctx, client, in.Collection)
		if err != nil {
			return nil, row{}, err
		}
		if in.Name == "" && in.Description == "" {
			return nil, row{}, errors.New("nothing to change: pass name or description")
		}
		if in.Name != "" {
			others, cerr := client.Collections(ctx, c.LibraryID)
			if cerr != nil {
				return nil, row{}, cerr
			}
			for i := range others {
				if others[i].ID != c.ID && strings.EqualFold(strings.TrimSpace(others[i].Name), strings.TrimSpace(in.Name)) {
					return nil, row{}, fmt.Errorf("a collection named %q already exists (%s): two collections with one name cannot be told apart by name", others[i].Name, others[i].ID)
				}
			}
		}
		updated, err := client.UpdateCollection(ctx, c.ID, strPtr(in.Name), strPtr(in.Description))
		if err != nil {
			return nil, row{}, err
		}

		return nil, rowOf(updated), nil
	})

	type itemsIn struct {
		getIn
		Items []string `json:"items" jsonschema:"books by id or exact title"`
	}
	type changeOut struct {
		Collection  string   `json:"collection"`
		Books       int      `json:"books"                  jsonschema:"size after the change"`
		Added       []string `json:"added,omitempty"`
		AlreadyHeld []string `json:"already_held,omitempty" jsonschema:"books asked for that the collection already held, left where they were"`
		Removed     []string `json:"removed,omitempty"`
		NotHeld     []string `json:"not_held,omitempty"     jsonschema:"books asked to be removed that the collection did not hold"`
	}
	type booksEditIn struct {
		itemsIn
		Action string `json:"action" jsonschema:"add or remove"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "collection_books_edit",
		Description: "Add books to a collection or take them out of it, and say which were added or removed and which it already held or never did. Removing only changes the collection; the books stay in the library. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in booksEditIn) (*mcp.CallToolResult, changeOut, error) {
		action := strings.ToLower(strings.TrimSpace(in.Action))
		if action != "add" && action != "remove" {
			return nil, changeOut{}, fmt.Errorf("action %q must be add or remove", in.Action)
		}

		c, err := resolveCollection(ctx, client, in.Collection)
		if err != nil {
			return nil, changeOut{}, err
		}
		books, err := libraryBooks(ctx, client, c.LibraryID, in.Items)
		if err != nil {
			return nil, changeOut{}, err
		}
		held := func(id string) bool { return slices.ContainsFunc(c.Books, func(b abs.Item) bool { return b.ID == id }) }

		// the server answers 200 to adding a book it holds and to removing one
		// it does not, and changes nothing: the split is made here, so what
		// comes back says what was actually done
		out := changeOut{Collection: c.Name, Books: len(c.Books)}
		var send []abs.Item
		for i := range books {
			switch {
			case action == "add" && held(books[i].ID):
				out.AlreadyHeld = append(out.AlreadyHeld, books[i].Title())
			case action == "remove" && !held(books[i].ID):
				out.NotHeld = append(out.NotHeld, books[i].Title())
			default:
				send = append(send, books[i])
			}
		}
		if len(send) == 0 {
			return nil, out, nil
		}

		var updated *abs.Collection
		if action == "add" {
			updated, err = client.AddToCollection(ctx, c.ID, bookIDs(send))
		} else {
			updated, err = client.RemoveFromCollection(ctx, c.ID, bookIDs(send))
		}
		if err != nil {
			return nil, changeOut{}, err
		}
		for i := range send {
			if held := slices.ContainsFunc(updated.Books, func(b abs.Item) bool { return b.ID == send[i].ID }); held != (action == "add") {
				return nil, changeOut{}, fmt.Errorf("the server accepted the %s but %q is %s the collection afterwards", action, send[i].Title(), map[bool]string{true: "still in", false: "not in"}[held])
			}
		}
		out.Collection, out.Books = updated.Name, len(updated.Books)
		if action == "add" {
			out.Added = titles(send)
		} else {
			out.Removed = titles(send)
		}

		return nil, out, nil
	})

	type deleteOut struct {
		Deleted string `json:"deleted"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "collection_delete",
		Description: "Delete a collection (its books stay in the library). Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in getIn) (*mcp.CallToolResult, deleteOut, error) {
		c, err := resolveCollection(ctx, client, in.Collection)
		if err != nil {
			return nil, deleteOut{}, err
		}
		if err := client.DeleteCollection(ctx, c.ID); err != nil {
			return nil, deleteOut{}, err
		}

		return nil, deleteOut{Deleted: c.Name}, nil
	})
}
