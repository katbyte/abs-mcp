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
			it, err = resolveItemToChange(ctx, client, libraryID, e.Item)
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
			return fmt.Errorf("a playlist named %q already exists (%s): playlist_edit add_entries adds to it", pls[i].Name, pls[i].ID)
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
		pageIn
	}
	type listOut struct {
		Total      int   `json:"total"`
		Offset     int   `json:"offset"`
		NextOffset int   `json:"next_offset,omitempty" jsonschema:"pass back as offset for the next page; absent on the last"`
		Playlists  []row `json:"playlists"             jsonschema:"playlists belong to the API key's user"`
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
		limit, offset := pageArgs(in.Limit, in.Offset, 50)
		page, next := pageOf(pls, limit, offset)
		out := listOut{Total: len(pls), Offset: offset, NextOffset: next, Playlists: []row{}}
		for i := range page {
			out.Playlists = append(out.Playlists, rowOf(&page[i]))
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
			// the server makes the copy with the collection's name and
			// description; either asked for is set on it afterwards
			var rename *string
			if !strings.EqualFold(name, p.Name) {
				rename = &name
			}
			if rename != nil || in.Description != "" {
				if p, err = client.UpdatePlaylist(ctx, p.ID, rename, strPtr(in.Description)); err != nil {
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
		Name          string            `json:"name,omitempty"`
		Description   string            `json:"description,omitempty"`
		AddEntries    []playlistEntryIn `json:"add_entries,omitempty"    jsonschema:"books or podcast episodes to append"`
		RemoveEntries []playlistEntryIn `json:"remove_entries,omitempty" jsonschema:"entries to take out; the items stay in the library"`
	}
	type editOut struct {
		row
		Added       []string `json:"added,omitempty"`
		AlreadyHeld []string `json:"already_held,omitempty" jsonschema:"entries asked for that the playlist already held, left where they were"`
		Removed     []string `json:"removed,omitempty"`
		NotHeld     []string `json:"not_held,omitempty"     jsonschema:"entries asked to be removed that the playlist did not hold"`
		Deleted     bool     `json:"deleted,omitempty"      jsonschema:"the last entry was removed, and Audiobookshelf deletes a playlist left empty"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "playlist_edit",
		Description: "Change a playlist: rename it, change its description, append books or podcast episodes to it or take entries out of it. The answer is the playlist as it now is, with which entries were added or removed and which it already held or never did. Removing only changes the playlist; the items stay in the library. Removing every entry deletes the playlist, as Audiobookshelf keeps no empty one, so that is refused unless the delete tools are switched on. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in editIn) (*mcp.CallToolResult, editOut, error) {
		// a name of only spaces would leave a playlist nothing can name
		name := strings.TrimSpace(in.Name)
		switch {
		case in.Name != "" && name == "":
			return nil, editOut{}, errors.New("name is blank: pass a name, or leave it out to keep the one it has")
		case name == "" && in.Description == "" && len(in.AddEntries) == 0 && len(in.RemoveEntries) == 0:
			return nil, editOut{}, errors.New("nothing to change: pass name, description, add_entries or remove_entries")
		}
		p, err := resolvePlaylist(ctx, client, in.Playlist)
		if err != nil {
			return nil, editOut{}, err
		}
		// what it holds is judged once held, so two adds of one entry at once
		// do not both say they added it
		defer r.locks.hold("playlist:" + p.ID)()
		if p, err = client.Playlist(ctx, p.ID); err != nil {
			return nil, editOut{}, err
		}
		if name != "" {
			if err := playlistNameInUse(ctx, client, p.LibraryID, name, p.ID); err != nil {
				return nil, editOut{}, err
			}
		}

		// everything is resolved before anything is sent, so an entry named
		// wrongly changes nothing at all
		var adding, removing []playlistEntry
		if len(in.AddEntries) > 0 {
			if adding, err = resolvePlaylistEntries(ctx, client, p.LibraryID, in.AddEntries); err != nil {
				return nil, editOut{}, err
			}
		}
		if len(in.RemoveEntries) > 0 {
			if removing, err = resolvePlaylistEntries(ctx, client, p.LibraryID, in.RemoveEntries); err != nil {
				return nil, editOut{}, err
			}
		}
		for i := range adding {
			if slices.ContainsFunc(removing, func(e playlistEntry) bool { return e.key() == adding[i].key() }) {
				return nil, editOut{}, fmt.Errorf("%q is in both add_entries and remove_entries; one or the other", adding[i].label())
			}
		}
		held := map[string]bool{}
		for i := range p.Items {
			held[playlistKey(&p.Items[i])] = true
		}
		// the server answers 200 to adding what it holds and to removing what
		// it does not, and changes nothing: split them here, so what comes back
		// says what was actually done
		out := editOut{}
		var add, remove []playlistEntry
		for i := range adding {
			if held[adding[i].key()] {
				out.AlreadyHeld = append(out.AlreadyHeld, adding[i].label())
			} else {
				add = append(add, adding[i])
			}
		}
		for i := range removing {
			if held[removing[i].key()] {
				remove = append(remove, removing[i])
			} else {
				out.NotHeld = append(out.NotHeld, removing[i].label())
			}
		}
		// the server deletes a playlist its last entry leaves, so emptying one
		// is a delete, and is only done where deleting is switched on
		if len(remove) > 0 && len(p.Items)+len(add)-len(remove) == 0 && !r.opts.EnableDelete {
			return nil, editOut{}, fmt.Errorf("removing every entry of %q deletes the playlist, as Audiobookshelf keeps no empty one; that needs the delete tools switched on (--enable-delete), and then playlist_delete says so plainly", p.Name)
		}
		refs := func(entries []playlistEntry) (refs []abs.PlaylistEntry, labels []string) {
			for i := range entries {
				refs, labels = append(refs, entries[i].ref()), append(labels, entries[i].label())
			}
			return refs, labels
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
			if p, err = client.UpdatePlaylist(ctx, p.ID, strPtr(name), strPtr(in.Description)); err != nil {
				return nil, editOut{}, err
			}
			done = append(done, "the name or description was changed")
		}
		if len(add) > 0 {
			send, labels := refs(add)
			if p, err = client.AddToPlaylist(ctx, p.ID, send); err != nil {
				return nil, editOut{}, failed("adding the entries", err)
			}
			now := map[string]bool{}
			for i := range p.Items {
				now[playlistKey(&p.Items[i])] = true
			}
			for i := range add {
				if !now[add[i].key()] {
					return nil, editOut{}, failed("adding the entries", fmt.Errorf("the server accepted the add but %q is not in the playlist afterwards", add[i].label()))
				}
			}
			out.Added = labels
			done = append(done, fmt.Sprintf("%d entries were added", len(add)))
		}
		if len(remove) > 0 {
			send, labels := refs(remove)
			if _, err = client.RemoveFromPlaylist(ctx, p.ID, send); err != nil {
				return nil, editOut{}, failed("removing the entries", err)
			}
			out.Removed = labels
			// the reply is the playlist as it was before the server deleted
			// an emptied one, so the playlist is read back rather than trusted
			var after *abs.Playlist
			after, err = client.Playlist(ctx, p.ID)
			switch {
			case abs.IsNotFound(err):
				out.row, out.Deleted = rowOf(p), true
				out.Entries = 0
				return nil, out, nil
			case err != nil:
				return nil, editOut{}, failed("reading the playlist back after removing the entries", err)
			}
			for i := range after.Items {
				for j := range remove {
					if playlistKey(&after.Items[i]) == remove[j].key() {
						return nil, editOut{}, failed("removing the entries", fmt.Errorf("the server accepted the removal but %q is still in the playlist", remove[j].label()))
					}
				}
			}
			p = after
		}
		out.row = rowOf(p)

		return nil, out, nil
	})

	type deleteOut struct {
		Deleted string `json:"deleted"`
		ID      string `json:"id"`
	}
	// a delete tool, registered only with --enable-delete, like every tool
	// that removes a record rather than changing one
	add(r, deleteTool, &mcp.Tool{
		Name:        "playlist_delete",
		Description: "Delete one of the API key user's playlists (its items stay in the library).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in getIn) (*mcp.CallToolResult, deleteOut, error) {
		p, err := resolvePlaylist(ctx, client, in.Playlist)
		if err != nil {
			return nil, deleteOut{}, err
		}
		if err := client.DeletePlaylist(ctx, p.ID); err != nil {
			return nil, deleteOut{}, err
		}
		// read back rather than trusted: the answer to a delete is not the
		// playlist gone
		left, err := client.Playlists(ctx, p.LibraryID)
		if err != nil {
			return nil, deleteOut{}, fmt.Errorf("deleted %q, but reading the playlists back failed: %w", p.Name, err)
		}
		for i := range left {
			if left[i].ID == p.ID {
				return nil, deleteOut{}, fmt.Errorf("the server accepted the delete but %q (%s) is still listed", p.Name, p.ID)
			}
		}

		return nil, deleteOut{Deleted: p.Name, ID: p.ID}, nil
	})
}
