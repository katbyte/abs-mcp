package tools

import (
	"context"
	"errors"
	"fmt"
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
	names := make([]string, 0, len(cols))
	for i := range cols {
		if strings.EqualFold(cols[i].Name, nameOrID) {
			return &cols[i], nil
		}
		names = append(names, cols[i].Name)
	}

	return nil, fmt.Errorf("no collection named %q (have: %s)", nameOrID, strings.Join(names, ", "))
}

// resolveItemIDs turns a list of ids or titles into item ids.
func resolveItemIDs(ctx context.Context, client *abs.Client, library string, refs []string) ([]string, error) {
	ids := make([]string, 0, len(refs))
	for _, ref := range refs {
		if looksLikeID(ref) {
			ids = append(ids, ref)
			continue
		}
		it, err := resolveItem(ctx, client, library, ref)
		if err != nil {
			return nil, err
		}
		ids = append(ids, it.ID)
	}
	if len(ids) == 0 {
		return nil, errors.New("at least one item is required")
	}
	return ids, nil
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
		Items       []string `json:"items,omitempty"       jsonschema:"initial books by id or exact title, in order"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "collection_create",
		Description: "Create a collection, optionally pre-filled with books. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in createIn) (*mcp.CallToolResult, row, error) {
		lib, err := resolveLibrary(ctx, client, in.Library)
		if err != nil {
			return nil, row{}, err
		}
		var ids []string
		if len(in.Items) > 0 {
			if ids, err = resolveItemIDs(ctx, client, lib.Name, in.Items); err != nil {
				return nil, row{}, err
			}
		}
		c, err := client.CreateCollection(ctx, lib.ID, in.Name, in.Description, ids)
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
		Collection string `json:"collection"`
		Books      int    `json:"books"      jsonschema:"size after the change"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "collection_add",
		Description: "Add books to a collection. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in itemsIn) (*mcp.CallToolResult, changeOut, error) {
		c, err := resolveCollection(ctx, client, in.Collection)
		if err != nil {
			return nil, changeOut{}, err
		}
		ids, err := resolveItemIDs(ctx, client, c.LibraryID, in.Items)
		if err != nil {
			return nil, changeOut{}, err
		}
		updated, err := client.AddToCollection(ctx, c.ID, ids)
		if err != nil {
			return nil, changeOut{}, err
		}

		return nil, changeOut{Collection: updated.Name, Books: len(updated.Books)}, nil
	})

	add(r, writeTool, &mcp.Tool{
		Name:        "collection_remove",
		Description: "Remove books from a collection (they stay in the library). Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in itemsIn) (*mcp.CallToolResult, changeOut, error) {
		c, err := resolveCollection(ctx, client, in.Collection)
		if err != nil {
			return nil, changeOut{}, err
		}
		ids, err := resolveItemIDs(ctx, client, c.LibraryID, in.Items)
		if err != nil {
			return nil, changeOut{}, err
		}
		updated, err := client.RemoveFromCollection(ctx, c.ID, ids)
		if err != nil {
			return nil, changeOut{}, err
		}

		return nil, changeOut{Collection: updated.Name, Books: len(updated.Books)}, nil
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
