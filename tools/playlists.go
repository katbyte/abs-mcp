package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/katbyte/abs-mcp/lib/abs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// resolvePlaylist finds one of the API key user's playlists by name or id.
func resolvePlaylist(ctx context.Context, client *abs.Client, nameOrID string) (*abs.Playlist, error) {
	nameOrID = strings.TrimSpace(nameOrID)
	if nameOrID == "" {
		return nil, errors.New("playlist name or id is required")
	}
	if looksLikeID(nameOrID) {
		return client.Playlist(ctx, nameOrID)
	}
	pls, err := client.Playlists(ctx, "")
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(pls))
	for i := range pls {
		if strings.EqualFold(pls[i].Name, nameOrID) {
			return &pls[i], nil
		}
		names = append(names, pls[i].Name)
	}

	return nil, fmt.Errorf("no playlist named %q (have: %s)", nameOrID, strings.Join(names, ", "))
}

// playlistEntryIn names a book, or an episode of a podcast, for a playlist.
type playlistEntryIn struct {
	Item    string `json:"item"              jsonschema:"library item id or exact title"`
	Episode string `json:"episode,omitempty" jsonschema:"episode id (podcasts); omit for books"`
}

func resolvePlaylistEntries(ctx context.Context, client *abs.Client, library string, entries []playlistEntryIn) ([]abs.PlaylistEntry, error) {
	if len(entries) == 0 {
		return nil, errors.New("at least one entry is required")
	}
	out := make([]abs.PlaylistEntry, 0, len(entries))
	for _, e := range entries {
		id := e.Item
		if !looksLikeID(id) {
			it, err := resolveItem(ctx, client, library, e.Item)
			if err != nil {
				return nil, err
			}
			id = it.ID
		}
		out = append(out, abs.PlaylistEntry{LibraryItemID: id, EpisodeID: e.Episode})
	}
	return out, nil
}

func registerPlaylistTools(r *registry) {
	client := r.client

	type row struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Library     string `json:"library_id"`
		Entries     int    `json:"entries"`
		Description string `json:"description,omitempty"`
		Updated     string `json:"updated,omitempty"`
	}
	rowOf := func(p *abs.Playlist) row {
		return row{ID: p.ID, Name: p.Name, Library: p.LibraryID, Entries: len(p.Items), Description: p.Description, Updated: fmtDate(p.LastUpdate)}
	}

	type listIn struct {
		Library string `json:"library,omitempty" jsonschema:"restrict to one library by name or id"`
	}
	type listOut struct {
		Playlists []row `json:"playlists" jsonschema:"playlists belong to the API key's user"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "playlist_list",
		Description: "List the API key user's playlists (personal, ordered queues of books or podcast episodes).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listIn) (*mcp.CallToolResult, listOut, error) {
		libID := ""
		if in.Library != "" {
			lib, err := resolveLibrary(ctx, client, in.Library)
			if err != nil {
				return nil, listOut{}, err
			}
			libID = lib.ID
		}
		pls, err := client.Playlists(ctx, libID)
		if err != nil {
			return nil, listOut{}, err
		}
		out := listOut{Playlists: []row{}}
		for i := range pls {
			out.Playlists = append(out.Playlists, rowOf(&pls[i]))
		}

		return nil, out, nil
	})

	type getIn struct {
		Playlist string `json:"playlist" jsonschema:"playlist name (case-insensitive) or id"`
	}
	type entryRow struct {
		Item    *itemSummary    `json:"item,omitempty"`
		Episode *episodeSummary `json:"episode,omitempty"`
	}
	type getOut struct {
		row
		Entries []entryRow `json:"entries" jsonschema:"in playlist order"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "playlist_get",
		Description: "A playlist's entries in order.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in getIn) (*mcp.CallToolResult, getOut, error) {
		p, err := resolvePlaylist(ctx, client, in.Playlist)
		if err != nil {
			return nil, getOut{}, err
		}
		out := getOut{row: rowOf(p), Entries: []entryRow{}}
		for _, e := range p.Items {
			var er entryRow
			if e.Episode != nil {
				title := ""
				if e.LibraryItem != nil {
					title = e.LibraryItem.Title()
				}
				ep := summarizeEpisode(e.Episode, title, false)
				er.Episode = &ep
			} else if e.LibraryItem != nil {
				s := summarize(e.LibraryItem)
				er.Item = &s
			}
			out.Entries = append(out.Entries, er)
		}

		return nil, out, nil
	})

	type createIn struct {
		Name           string            `json:"name"`
		Library        string            `json:"library,omitempty"         jsonschema:"library name or id; optional when the server has one library"`
		Description    string            `json:"description,omitempty"`
		Entries        []playlistEntryIn `json:"entries,omitempty"         jsonschema:"initial entries in order"`
		FromCollection string            `json:"from_collection,omitempty" jsonschema:"instead: copy a collection's books into the new playlist"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "playlist_create",
		Description: "Create a playlist, optionally pre-filled with books/episodes or copied from a collection. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in createIn) (*mcp.CallToolResult, row, error) {
		if in.FromCollection != "" {
			c, err := resolveCollection(ctx, client, in.FromCollection)
			if err != nil {
				return nil, row{}, err
			}
			p, err := client.CreatePlaylistFromCollection(ctx, c.ID)
			if err != nil {
				return nil, row{}, err
			}
			if in.Name != "" && !strings.EqualFold(in.Name, p.Name) {
				if p, err = client.UpdatePlaylist(ctx, p.ID, &in.Name, strPtr(in.Description)); err != nil {
					return nil, row{}, err
				}
			}
			return nil, rowOf(p), nil
		}

		lib, err := resolveLibrary(ctx, client, in.Library)
		if err != nil {
			return nil, row{}, err
		}
		var entries []abs.PlaylistEntry
		if len(in.Entries) > 0 {
			if entries, err = resolvePlaylistEntries(ctx, client, lib.Name, in.Entries); err != nil {
				return nil, row{}, err
			}
		}
		p, err := client.CreatePlaylist(ctx, lib.ID, in.Name, in.Description, entries)
		if err != nil {
			return nil, row{}, err
		}

		return nil, rowOf(p), nil
	})

	type editIn struct {
		getIn
		Name        string `json:"name,omitempty"`
		Description string `json:"description,omitempty"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "playlist_edit",
		Description: "Rename a playlist or change its description. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in editIn) (*mcp.CallToolResult, row, error) {
		p, err := resolvePlaylist(ctx, client, in.Playlist)
		if err != nil {
			return nil, row{}, err
		}
		if in.Name == "" && in.Description == "" {
			return nil, row{}, errors.New("nothing to change: pass name or description")
		}
		updated, err := client.UpdatePlaylist(ctx, p.ID, strPtr(in.Name), strPtr(in.Description))
		if err != nil {
			return nil, row{}, err
		}

		return nil, rowOf(updated), nil
	})

	type entriesIn struct {
		getIn
		Entries []playlistEntryIn `json:"entries"`
	}
	type changeOut struct {
		Playlist string `json:"playlist"`
		Entries  int    `json:"entries"  jsonschema:"size after the change"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "playlist_add",
		Description: "Append books or podcast episodes to a playlist. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in entriesIn) (*mcp.CallToolResult, changeOut, error) {
		p, err := resolvePlaylist(ctx, client, in.Playlist)
		if err != nil {
			return nil, changeOut{}, err
		}
		entries, err := resolvePlaylistEntries(ctx, client, p.LibraryID, in.Entries)
		if err != nil {
			return nil, changeOut{}, err
		}
		updated, err := client.AddToPlaylist(ctx, p.ID, entries)
		if err != nil {
			return nil, changeOut{}, err
		}

		return nil, changeOut{Playlist: updated.Name, Entries: len(updated.Items)}, nil
	})

	add(r, writeTool, &mcp.Tool{
		Name:        "playlist_remove",
		Description: "Remove entries from a playlist (the items stay in the library). Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in entriesIn) (*mcp.CallToolResult, changeOut, error) {
		p, err := resolvePlaylist(ctx, client, in.Playlist)
		if err != nil {
			return nil, changeOut{}, err
		}
		entries, err := resolvePlaylistEntries(ctx, client, p.LibraryID, in.Entries)
		if err != nil {
			return nil, changeOut{}, err
		}
		updated, err := client.RemoveFromPlaylist(ctx, p.ID, entries)
		if err != nil {
			return nil, changeOut{}, err
		}

		return nil, changeOut{Playlist: updated.Name, Entries: len(updated.Items)}, nil
	})

	type deleteOut struct {
		Deleted string `json:"deleted"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "playlist_delete",
		Description: "Delete a playlist (its items stay in the library). Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in getIn) (*mcp.CallToolResult, deleteOut, error) {
		p, err := resolvePlaylist(ctx, client, in.Playlist)
		if err != nil {
			return nil, deleteOut{}, err
		}
		if err := client.DeletePlaylist(ctx, p.ID); err != nil {
			return nil, deleteOut{}, err
		}

		return nil, deleteOut{Deleted: p.Name}, nil
	})
}
