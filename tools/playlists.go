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

	return oneNamed("playlist", nameOrID, pls, func(p *abs.Playlist) string { return p.Name }, func(p *abs.Playlist) string { return p.ID })
}

// playlistEntryIn names a book, or an episode of a podcast, for a playlist.
type playlistEntryIn struct {
	Item    string `json:"item"              jsonschema:"library item id or exact title"`
	Episode string `json:"episode,omitempty" jsonschema:"episode id (podcasts); omit for books"`
}

// playlistEntry is an entry resolved against the server: the item it names
// and, for a podcast, the episode.
type playlistEntry struct {
	Item    *abs.Item
	Episode *abs.Episode
}

func (e *playlistEntry) key() string {
	if e.Episode != nil {
		return e.Item.ID + "/" + e.Episode.ID
	}
	return e.Item.ID
}

func (e *playlistEntry) label() string {
	if e.Episode != nil {
		return e.Item.Title() + ": " + e.Episode.Title
	}
	return e.Item.Title()
}

func (e *playlistEntry) ref() abs.PlaylistEntry {
	out := abs.PlaylistEntry{LibraryItemID: e.Item.ID}
	if e.Episode != nil {
		out.EpisodeID = e.Episode.ID
	}
	return out
}

// playlistKey is the key of an entry a playlist already holds.
func playlistKey(pi *abs.PlaylistItem) string {
	if pi.EpisodeID != "" {
		return pi.LibraryItemID + "/" + pi.EpisodeID
	}
	return pi.LibraryItemID
}

// resolvePlaylistEntries turns what a caller named into entries the server
// can take, in one library. Audiobookshelf's batch add checks none of this:
// a book sent with an episode id, or a podcast with an episode that is not
// its own, crashes the server outright (an unhandled rejection that exits the
// process); a podcast sent without an episode is stored as a broken book
// entry; and an item from another library is accepted into the playlist. So
// every entry is checked here first. An entry named twice is kept once.
func resolvePlaylistEntries(ctx context.Context, client *abs.Client, libraryID string, entries []playlistEntryIn) ([]playlistEntry, error) {
	if len(entries) == 0 {
		return nil, errors.New("at least one entry is required")
	}
	out := make([]playlistEntry, 0, len(entries))
	for _, e := range entries {
		var it *abs.Item
		var err error
		if looksLikeID(e.Item) {
			it, err = client.Item(ctx, e.Item)
		} else {
			it, err = resolveItem(ctx, client, libraryID, e.Item)
		}
		if err != nil {
			return nil, err
		}
		if it.LibraryID != libraryID {
			return nil, fmt.Errorf("%q is in another library (%s): a playlist holds items from its own library", it.Title(), it.LibraryID)
		}
		entry := playlistEntry{Item: it}
		episode := strings.TrimSpace(e.Episode)
		switch {
		case !it.IsPodcast() && episode != "":
			return nil, fmt.Errorf("%q is a book: an episode id only goes with a podcast", it.Title())
		case it.IsPodcast() && episode == "":
			return nil, fmt.Errorf("%q is a podcast: name the episode (podcast_episodes lists them)", it.Title())
		case it.IsPodcast():
			for i := range it.Media.Episodes {
				if it.Media.Episodes[i].ID == episode {
					entry.Episode = &it.Media.Episodes[i]
				}
			}
			if entry.Episode == nil {
				return nil, fmt.Errorf("%q has no episode %s (podcast_episodes lists them)", it.Title(), episode)
			}
		}
		if !slices.ContainsFunc(out, func(o playlistEntry) bool { return o.key() == entry.key() }) {
			out = append(out, entry)
		}
	}
	return out, nil
}

// playlistNameInUse refuses a name the API key's user already has a playlist
// by in a library: the server would make a second, and a name that means two
// playlists can no longer be looked up by name.
func playlistNameInUse(ctx context.Context, client *abs.Client, libraryID, name, except string) error {
	pls, err := client.Playlists(ctx, libraryID)
	if err != nil {
		return err
	}
	for i := range pls {
		if pls[i].ID != except && strings.EqualFold(strings.TrimSpace(pls[i].Name), strings.TrimSpace(name)) {
			return fmt.Errorf("a playlist named %q already exists (%s): playlist_entries_edit adds to it", pls[i].Name, pls[i].ID)
		}
	}
	return nil
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
		Description: "Create a playlist, optionally pre-filled with books/episodes or copied from a collection. A name the API key's user already has a playlist by in the library is refused: two playlists with one name cannot be told apart by name. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in createIn) (*mcp.CallToolResult, row, error) {
		if in.FromCollection != "" {
			if len(in.Entries) > 0 {
				return nil, row{}, errors.New("entries or from_collection, not both")
			}
			c, err := resolveCollection(ctx, client, in.FromCollection)
			if err != nil {
				return nil, row{}, err
			}
			name := strings.TrimSpace(in.Name)
			if name == "" {
				name = c.Name // the server names the copy after the collection
			}
			if err := playlistNameInUse(ctx, client, c.LibraryID, name, ""); err != nil {
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

		name := strings.TrimSpace(in.Name)
		if name == "" {
			return nil, row{}, errors.New("name is required")
		}
		lib, err := resolveLibrary(ctx, client, in.Library)
		if err != nil {
			return nil, row{}, err
		}
		if err := playlistNameInUse(ctx, client, lib.ID, name, ""); err != nil {
			return nil, row{}, err
		}
		var refs []abs.PlaylistEntry
		if len(in.Entries) > 0 {
			entries, eerr := resolvePlaylistEntries(ctx, client, lib.ID, in.Entries)
			if eerr != nil {
				return nil, row{}, eerr
			}
			for i := range entries {
				refs = append(refs, entries[i].ref())
			}
		}
		p, err := client.CreatePlaylist(ctx, lib.ID, name, in.Description, refs)
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
		if in.Name != "" {
			if err := playlistNameInUse(ctx, client, p.LibraryID, in.Name, p.ID); err != nil {
				return nil, row{}, err
			}
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
		Playlist    string   `json:"playlist"`
		Entries     int      `json:"entries"                jsonschema:"size after the change"`
		Added       []string `json:"added,omitempty"`
		AlreadyHeld []string `json:"already_held,omitempty" jsonschema:"entries asked for that the playlist already held, left where they were"`
		Removed     []string `json:"removed,omitempty"`
		NotHeld     []string `json:"not_held,omitempty"     jsonschema:"entries asked to be removed that the playlist did not hold"`
		Deleted     bool     `json:"deleted,omitempty"      jsonschema:"the last entry was removed, and Audiobookshelf deletes a playlist left empty"`
	}
	type entriesEditIn struct {
		entriesIn
		Action string `json:"action" jsonschema:"add or remove"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "playlist_entries_edit",
		Description: "Append books or podcast episodes to a playlist, or take them out of it, and say which were added or removed and which it already held or never did. Removing only changes the playlist; the items stay in the library. Removing every entry deletes the playlist: Audiobookshelf does not keep an empty one. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in entriesEditIn) (*mcp.CallToolResult, changeOut, error) {
		action := strings.ToLower(strings.TrimSpace(in.Action))
		if action != "add" && action != "remove" {
			return nil, changeOut{}, fmt.Errorf("action %q must be add or remove", in.Action)
		}

		p, err := resolvePlaylist(ctx, client, in.Playlist)
		if err != nil {
			return nil, changeOut{}, err
		}
		entries, err := resolvePlaylistEntries(ctx, client, p.LibraryID, in.Entries)
		if err != nil {
			return nil, changeOut{}, err
		}
		held := map[string]bool{}
		for i := range p.Items {
			held[playlistKey(&p.Items[i])] = true
		}

		// the server answers 200 to adding what it holds and to removing what
		// it does not, and changes nothing: split them here, so what comes back
		// says what was actually done
		out := changeOut{Playlist: p.Name, Entries: len(p.Items)}
		var send []playlistEntry
		for i := range entries {
			switch {
			case action == "add" && held[entries[i].key()]:
				out.AlreadyHeld = append(out.AlreadyHeld, entries[i].label())
			case action == "remove" && !held[entries[i].key()]:
				out.NotHeld = append(out.NotHeld, entries[i].label())
			default:
				send = append(send, entries[i])
			}
		}
		if len(send) == 0 {
			return nil, out, nil
		}
		refs := make([]abs.PlaylistEntry, 0, len(send))
		labels := make([]string, 0, len(send))
		for i := range send {
			refs = append(refs, send[i].ref())
			labels = append(labels, send[i].label())
		}

		if action == "add" {
			updated, aerr := client.AddToPlaylist(ctx, p.ID, refs)
			if aerr != nil {
				return nil, changeOut{}, aerr
			}
			now := map[string]bool{}
			for i := range updated.Items {
				now[playlistKey(&updated.Items[i])] = true
			}
			for i := range send {
				if !now[send[i].key()] {
					return nil, changeOut{}, fmt.Errorf("the server accepted the add but %q is not in the playlist afterwards", send[i].label())
				}
			}
			out.Playlist, out.Entries, out.Added = updated.Name, len(updated.Items), labels
			return nil, out, nil
		}

		if _, err := client.RemoveFromPlaylist(ctx, p.ID, refs); err != nil {
			return nil, changeOut{}, err
		}
		out.Removed = labels
		// the reply is the playlist as it was before the server deleted an
		// emptied one, so the playlist is read back rather than trusted
		after, err := client.Playlist(ctx, p.ID)
		switch {
		case abs.IsNotFound(err):
			out.Entries, out.Deleted = 0, true
			return nil, out, nil
		case err != nil:
			return nil, changeOut{}, err
		}
		for i := range after.Items {
			for j := range send {
				if playlistKey(&after.Items[i]) == send[j].key() {
					return nil, changeOut{}, fmt.Errorf("the server accepted the removal but %q is still in the playlist", send[j].label())
				}
			}
		}
		out.Entries = len(after.Items)

		return nil, out, nil
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
