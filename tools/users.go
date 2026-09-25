package tools

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/katbyte/abs-mcp/lib/abs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// selfTokens are values accepted for the "user" argument to mean the account
// the API key acts as, for callers that would rather say so than leave the
// argument out. A real account with one of these usernames still wins: the
// lookup runs first and only falls through to here.
var selfTokens = []string{"me", "@me", "$me", "self", "current"}

// progressRef names a book, or an episode of a podcast, for progress tools.
type progressRef struct {
	itemRef
	Episode string `json:"episode,omitempty" jsonschema:"episode id for podcasts; omit for books"`
}

// userRef is embedded by every user tool that can read someone else's data.
// Leaving it empty is the common case and the only one a non-admin key can do.
type userRef struct {
	User string `json:"user,omitempty" jsonschema:"username or id; omit (or pass me) for the account the API key acts as, which is the only one a non-admin key can read"`
}

// resolveUser finds the user a tool should act on and reports whether that is
// the API key's own account. An empty name is the caller themselves, which
// costs one request and needs no permissions; a name or id is looked up, which
// is admin only. The caller's own account named by its username or id is
// still the caller's own, read through the caller's own routes: a year in
// review is kept only there, and a non-admin key may not look itself up.
func resolveUser(ctx context.Context, client *abs.Client, nameOrID string) (*abs.User, bool, error) {
	nameOrID = strings.TrimSpace(nameOrID)
	me, meErr := client.Me(ctx)
	if nameOrID == "" {
		return me, true, meErr
	}
	// a key whose own account cannot be read can still look others up
	isMe := func(id string) bool { return meErr == nil && id == me.ID }
	if looksLikeID(nameOrID) {
		if isMe(nameOrID) {
			return me, true, nil
		}
		other, err := client.User(ctx, nameOrID)
		return other, false, err
	}

	users, lookupErr := client.Users(ctx, false)
	if lookupErr == nil {
		names := make([]string, 0, len(users))
		for i := range users {
			if strings.EqualFold(users[i].Username, nameOrID) {
				if isMe(users[i].ID) {
					return me, true, nil
				}
				other, err := client.User(ctx, users[i].ID)
				return other, false, err
			}
			names = append(names, users[i].Username)
		}
		lookupErr = fmt.Errorf("no user named %q (have: %s)", nameOrID, strings.Join(names, ", "))
	}

	// no account by that name, or no permission to look: the name may be a
	// stand-in for the caller, and a non-admin key cannot list anyone but
	// themselves. Both end up at the caller's own account.
	if meErr == nil && (slices.Contains(selfTokens, strings.ToLower(nameOrID)) || strings.EqualFold(me.Username, nameOrID)) {
		return me, true, nil
	}

	return nil, false, lookupErr
}

func registerUserTools(r *registry) {
	client := r.client

	type userRow struct {
		ID        string `json:"id"`
		Username  string `json:"username"`
		Type      string `json:"type"`
		Active    bool   `json:"active"`
		LastSeen  string `json:"last_seen,omitempty"`
		Listening string `json:"last_listened,omitempty" jsonschema:"title of their most recent session"`
	}
	type listOut struct {
		Users []userRow `json:"users"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "user_list",
		Description: "List the server's user accounts with type, last seen, and what they last listened to. Admin only; every other user tool defaults to the API key's own account and needs no admin rights.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, listOut, error) {
		users, err := client.Users(ctx, true)
		if err != nil {
			return nil, listOut{}, err
		}
		out := listOut{Users: []userRow{}}
		for i := range users {
			u := &users[i]
			row := userRow{ID: u.ID, Username: u.Username, Type: u.Type, Active: u.IsActive, LastSeen: fmtTime(u.LastSeen)}
			if u.LatestSession != nil {
				row.Listening = u.LatestSession.DisplayTitle
			}
			out.Users = append(out.Users, row)
		}

		return nil, out, nil
	})

	type progressRow struct {
		ItemID    string `json:"item_id"`
		EpisodeID string `json:"episode_id,omitempty"`
		Percent   int    `json:"percent"`
		Finished  bool   `json:"finished"`
		Updated   string `json:"updated,omitempty"`
	}
	type getOut struct {
		userRow
		Self         bool          `json:"self"                  jsonschema:"true when this is the account the API key acts as"`
		Email        string        `json:"email,omitempty"`
		CanUpdate    bool          `json:"can_update"`
		CanDelete    bool          `json:"can_delete"`
		CanDownload  bool          `json:"can_download"`
		CanUpload    bool          `json:"can_upload"`
		AllLibraries bool          `json:"all_libraries"         jsonschema:"the account can open every library; when false, only those in libraries"`
		Libraries    []string      `json:"libraries,omitempty"   jsonschema:"with all_libraries false: the ids of the only libraries the account can open (none when empty)"`
		AllTags      bool          `json:"all_tags"              jsonschema:"the account sees books whatever their tags; when false, tags or denied_tags says which"`
		Tags         []string      `json:"tags,omitempty"        jsonschema:"with all_tags false: the account sees only books carrying one of these"`
		DeniedTags   []string      `json:"denied_tags,omitempty" jsonschema:"with all_tags false: the account sees every book except those carrying one of these"`
		Explicit     bool          `json:"explicit"              jsonschema:"the account sees books marked explicit"`
		InProgress   int           `json:"items_in_progress"`
		Finished     int           `json:"items_finished"`
		Bookmarks    int           `json:"bookmarks"`
		Created      string        `json:"created,omitempty"`
		Recent       []progressRow `json:"recent_progress"       jsonschema:"their 10 most recently updated items"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "user_get",
		Description: "An account's details, permissions, and a summary of its listening progress. Omit user for the account the API key acts as - the usual case, and the only one available without admin rights. Call this first when unsure what the key can do.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in userRef) (*mcp.CallToolResult, getOut, error) {
		u, self, err := resolveUser(ctx, client, in.User)
		if err != nil {
			return nil, getOut{}, err
		}
		row := userRow{ID: u.ID, Username: u.Username, Type: u.Type, Active: u.IsActive, LastSeen: fmtTime(u.LastSeen)}
		out := getOut{
			userRow:      row,
			Self:         self,
			Email:        u.Email,
			CanUpdate:    u.IsAdmin() || u.Permissions.Update,
			CanDelete:    u.IsAdmin() || u.Permissions.Delete,
			CanDownload:  u.IsAdmin() || u.Permissions.Download,
			CanUpload:    u.IsAdmin() || u.Permissions.Upload,
			AllLibraries: u.Permissions.AccessAllLibraries,
			AllTags:      u.Permissions.AccessAllTags,
			Explicit:     u.Permissions.AccessExplicitContent,
			Bookmarks:    len(u.Bookmarks),
			Created:      fmtDate(u.CreatedAt),
			Recent:       []progressRow{},
		}
		if !u.Permissions.AccessAllLibraries {
			out.Libraries = u.LibrariesAccessible
		}
		if !u.Permissions.AccessAllTags {
			if u.Permissions.SelectedTagsNotAccessible {
				out.DeniedTags = u.ItemTagsSelected
			} else {
				out.Tags = u.ItemTagsSelected
			}
		}
		progress := slices.Clone(u.MediaProgress)
		slices.SortFunc(progress, func(a, b abs.MediaProgress) int { return cmp.Compare(b.LastUpdate, a.LastUpdate) })
		for _, p := range progress {
			switch {
			case p.IsFinished:
				out.Finished++
			case p.CurrentTime > 0 || p.EbookProgress > 0:
				out.InProgress++
			}
			if len(out.Recent) < 10 {
				out.Recent = append(out.Recent, progressRow{ItemID: p.LibraryItemID, EpisodeID: p.EpisodeID, Percent: percent(p.Progress), Finished: p.IsFinished, Updated: fmtTime(p.LastUpdate)})
			}
		}

		return nil, out, nil
	})

	type inProgressIn struct {
		userRef
		Limit int `json:"limit,omitempty" jsonschema:"maximum items, default 25"`
	}
	type inProgressRow struct {
		itemSummary
		Episode *episodeSummary `json:"episode,omitempty" jsonschema:"for podcasts, the episode in progress"`
	}
	type inProgressOut struct {
		Items []inProgressRow `json:"items" jsonschema:"most recently listened first"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "user_in_progress",
		Description: "What a user is currently listening to (the continue-listening shelf), most recent first, with position and percent. Omit user for the account the API key acts as - 'what am I listening to'.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in inProgressIn) (*mcp.CallToolResult, inProgressOut, error) {
		u, self, err := resolveUser(ctx, client, in.User)
		if err != nil {
			return nil, inProgressOut{}, err
		}
		limit := limitOr(in.Limit, defaultLimit)

		var items []abs.Item
		var progressFor map[string]*abs.MediaProgress
		if self {
			// the server has a shelf endpoint for the caller, already ordered
			// and carrying the episode that is in progress - but not the
			// position or percent, which come from the caller's own record.
			// It also keeps what the caller hid from continue listening, so
			// as many more are asked for as are hidden, and those are dropped
			hidden := 0
			for i := range u.MediaProgress {
				if p := &u.MediaProgress[i]; p.HideFromContinueListening && !p.IsFinished {
					hidden++
				}
			}
			if items, err = client.ItemsInProgress(ctx, limit+hidden); err != nil {
				return nil, inProgressOut{}, err
			}
			progressFor = map[string]*abs.MediaProgress{}
			shown := items[:0]
			for i := range items {
				want := ""
				if items[i].RecentEpisode != nil {
					want = items[i].RecentEpisode.ID
				}
				var mine *abs.MediaProgress
				for j := range u.MediaProgress {
					if p := &u.MediaProgress[j]; p.LibraryItemID == items[i].ID && p.EpisodeID == want {
						mine = p
					}
				}
				if mine != nil && mine.HideFromContinueListening {
					continue
				}
				if mine != nil {
					progressFor[items[i].ID] = mine
				}
				shown = append(shown, items[i])
			}
			items = shown[:min(len(shown), limit)]
		} else {
			// for anyone else the shelf has to be rebuilt from their progress
			// records, which name items by id only
			started := make([]abs.MediaProgress, 0, len(u.MediaProgress))
			for _, p := range u.MediaProgress {
				if p.IsFinished || p.HideFromContinueListening {
					continue
				}
				if p.CurrentTime > 0 || p.EbookProgress > 0 {
					started = append(started, p)
				}
			}
			slices.SortFunc(started, func(a, b abs.MediaProgress) int { return cmp.Compare(b.LastUpdate, a.LastUpdate) })
			if len(started) > limit {
				started = started[:limit]
			}
			ids := make([]string, 0, len(started))
			progressFor = make(map[string]*abs.MediaProgress, len(started))
			for i := range started {
				p := &started[i]
				if _, seen := progressFor[p.LibraryItemID]; !seen {
					ids = append(ids, p.LibraryItemID)
				}
				progressFor[p.LibraryItemID] = p
			}
			if len(ids) > 0 {
				if items, err = client.ItemsBatch(ctx, ids); err != nil {
					return nil, inProgressOut{}, err
				}
				// ItemsBatch answers in its own order; restore theirs
				rank := map[string]int{}
				for i, id := range ids {
					rank[id] = i
				}
				slices.SortFunc(items, func(a, b abs.Item) int { return cmp.Compare(rank[a.ID], rank[b.ID]) })
			}
		}

		out := inProgressOut{Items: []inProgressRow{}}
		for i := range items {
			it := &items[i]
			row := inProgressRow{itemSummary: summarize(it)}
			if p, ok := progressFor[it.ID]; ok {
				row.Progress = progressOf(p)
			}
			switch {
			case it.RecentEpisode != nil:
				ep := summarizeEpisode(it.RecentEpisode, it.Title(), false)
				ep.Progress = row.Progress
				row.Episode = &ep
				row.Progress = nil
			case progressFor[it.ID] != nil && progressFor[it.ID].EpisodeID != "":
				for j := range it.Media.Episodes {
					if it.Media.Episodes[j].ID == progressFor[it.ID].EpisodeID {
						ep := summarizeEpisode(&it.Media.Episodes[j], it.Title(), false)
						ep.Progress = row.Progress
						row.Episode = &ep
						row.Progress = nil
					}
				}
			}
			out.Items = append(out.Items, row)
		}

		return nil, out, nil
	})

	type progressGetIn struct {
		userRef
		progressRef
	}
	type progressGetOut struct {
		User     string           `json:"user,omitempty"`
		Item     string           `json:"item"`
		Episode  string           `json:"episode,omitempty"`
		Duration int              `json:"duration_s,omitempty" jsonschema:"length in seconds"`
		Progress *progressSummary `json:"progress"             jsonschema:"null when they have never started it"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "user_progress_get",
		Description: "A user's listening progress on one book or podcast episode: percent, position, finished flag. Omit user for the account the API key acts as.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in progressGetIn) (*mcp.CallToolResult, progressGetOut, error) {
		u, self, err := resolveUser(ctx, client, in.User)
		if err != nil {
			return nil, progressGetOut{}, err
		}
		it, err := resolveItem(ctx, client, in.Library, in.Item)
		if err != nil {
			return nil, progressGetOut{}, err
		}
		out := progressGetOut{User: u.Username, Item: it.Title(), Duration: wholeSec(it.Media.Duration)}
		if in.Episode != "" {
			for _, e := range it.Media.Episodes {
				if e.ID == in.Episode {
					out.Episode = e.Title
					out.Duration = wholeSec(e.DurationSeconds())
				}
			}
			if out.Episode == "" {
				return nil, progressGetOut{}, fmt.Errorf("no episode %s in %q", in.Episode, it.Title())
			}
		}

		if self {
			p, err := client.Progress(ctx, it.ID, in.Episode)
			if err != nil {
				return nil, progressGetOut{}, err
			}
			out.Progress = progressOf(p)

			return nil, out, nil
		}
		for i := range u.MediaProgress {
			if p := &u.MediaProgress[i]; p.LibraryItemID == it.ID && p.EpisodeID == in.Episode {
				out.Progress = progressOf(p)
				break
			}
		}

		return nil, out, nil
	})

	type progressSetIn struct {
		progressRef
		Finished *bool    `json:"finished,omitempty"           jsonschema:"mark finished (true) or not finished (false)"`
		Position *float64 `json:"position_s,omitempty"         jsonschema:"set the playback position in seconds"`
		Percent  *float64 `json:"percent,omitempty"            jsonschema:"set the position as a percentage 0-100 instead"`
		Hide     *bool    `json:"hide_from_continue,omitempty" jsonschema:"remove from (true) or restore to (false) the continue-listening shelf"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "user_progress_set",
		Description: "Update the API key user's progress on a book or episode: mark finished/unfinished, set the position (seconds or percent), or hide it from continue-listening. Always the API key's own account - Audiobookshelf has no way to set someone else's progress. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in progressSetIn) (*mcp.CallToolResult, progressGetOut, error) {
		switch {
		case in.Percent != nil && (*in.Percent < 0 || *in.Percent > 100):
			return nil, progressGetOut{}, fmt.Errorf("percent %v is not between 0 and 100", *in.Percent)
		case in.Position != nil && *in.Position < 0:
			return nil, progressGetOut{}, fmt.Errorf("position_s %v is before the start", *in.Position)
		}
		it, err := resolveItemToChange(ctx, client, in.Library, in.Item)
		if err != nil {
			return nil, progressGetOut{}, err
		}
		// a podcast's progress is kept per episode; one set on the show
		// itself is on nothing that plays, and it has no length, so any
		// percent of it would be position 0
		if it.IsPodcast() && in.Episode == "" {
			return nil, progressGetOut{}, fmt.Errorf("%q is a podcast: name the episode (podcast_episodes lists them)", it.Title())
		}
		duration := it.Media.Duration
		if in.Episode != "" {
			found := false
			for _, e := range it.Media.Episodes {
				if e.ID == in.Episode {
					duration, found = e.DurationSeconds(), true
				}
			}
			if !found {
				return nil, progressGetOut{}, fmt.Errorf("no episode %s in %q", in.Episode, it.Title())
			}
		}
		if in.Position == nil && in.Percent != nil && duration <= 0 {
			return nil, progressGetOut{}, fmt.Errorf("%q has no length to take a percent of: pass position_s", it.Title())
		}

		upd := abs.ProgressUpdate{IsFinished: in.Finished, HideFromContinueListening: in.Hide}
		switch {
		case in.Position != nil:
			upd.CurrentTime = in.Position
		case in.Percent != nil:
			upd.CurrentTime = new(duration * *in.Percent / 100)
		}
		if upd.CurrentTime != nil && duration > 0 {
			upd.Progress = new(*upd.CurrentTime / duration)
			upd.Duration = &duration
		}
		if in.Finished != nil && !*in.Finished && upd.CurrentTime == nil {
			// un-finishing without a position: keep the current position but
			// clear the finished flag
			upd.Progress = new(0.0)
		}
		if upd.CurrentTime == nil && upd.IsFinished == nil && upd.HideFromContinueListening == nil {
			return nil, progressGetOut{}, errors.New("nothing to change: pass finished, position_s, percent or hide_from_continue")
		}
		if err := client.SetProgress(ctx, it.ID, in.Episode, upd); err != nil {
			return nil, progressGetOut{}, err
		}

		p, err := client.Progress(ctx, it.ID, in.Episode)
		if err != nil {
			return nil, progressGetOut{}, err
		}

		return nil, progressGetOut{Item: it.Title(), Duration: wholeSec(duration), Progress: progressOf(p)}, nil
	})

	type progressRemoveOut struct {
		Item    string `json:"item"`
		Removed bool   `json:"removed" jsonschema:"false when there was no progress to remove"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "user_progress_remove",
		Description: "Delete the API key user's progress on a book or episode, resetting it to never started. Always the API key's own account. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in progressRef) (*mcp.CallToolResult, progressRemoveOut, error) {
		it, err := resolveItemToChange(ctx, client, in.Library, in.Item)
		if err != nil {
			return nil, progressRemoveOut{}, err
		}
		p, err := client.Progress(ctx, it.ID, in.Episode)
		if err != nil {
			return nil, progressRemoveOut{}, err
		}
		if p == nil {
			return nil, progressRemoveOut{Item: it.Title()}, nil
		}
		if err := client.RemoveProgress(ctx, p.ID); err != nil {
			return nil, progressRemoveOut{}, err
		}
		if left, err := client.Progress(ctx, it.ID, in.Episode); err == nil && left != nil {
			return nil, progressRemoveOut{}, fmt.Errorf("the server accepted the removal but %q still has progress", it.Title())
		}

		return nil, progressRemoveOut{Item: it.Title(), Removed: true}, nil
	})

	type bookmarkRow struct {
		ItemID  string  `json:"item_id"`
		Item    string  `json:"item,omitempty"`
		Deleted bool    `json:"item_deleted,omitempty" jsonschema:"the item is no longer on the server; Audiobookshelf keeps the bookmark and will not remove it"`
		Title   string  `json:"title"`
		Time    float64 `json:"time_s"                 jsonschema:"position in seconds, exactly as held: pass it as seconds to user_bookmark_edit to remove it"`
		Created string  `json:"created,omitempty"`
	}
	type bookmarksIn struct {
		userRef
		Item    string `json:"item,omitempty"    jsonschema:"restrict to one item by id or title"`
		Library string `json:"library,omitempty"`
	}
	type bookmarksOut struct {
		User      string        `json:"user"`
		Bookmarks []bookmarkRow `json:"bookmarks"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "user_bookmarks",
		Description: "A user's bookmarks (named positions in books), optionally for one item. Omit user for the account the API key acts as.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in bookmarksIn) (*mcp.CallToolResult, bookmarksOut, error) {
		u, self, err := resolveUser(ctx, client, in.User)
		if err != nil {
			return nil, bookmarksOut{}, err
		}
		bms := u.Bookmarks
		if self {
			if bms, err = client.Bookmarks(ctx); err != nil {
				return nil, bookmarksOut{}, err
			}
		}
		only := ""
		if in.Item != "" {
			it, err := resolveItem(ctx, client, in.Library, in.Item)
			if err != nil {
				return nil, bookmarksOut{}, err
			}
			only = it.ID
		}

		titles := map[string]string{}
		var ids []string
		for _, b := range bms {
			if only != "" && b.LibraryItemID != only {
				continue
			}
			if _, seen := titles[b.LibraryItemID]; !seen {
				titles[b.LibraryItemID] = ""
				ids = append(ids, b.LibraryItemID)
			}
		}
		deleted := map[string]bool{}
		if len(ids) > 0 {
			if items, err := client.ItemsBatch(ctx, ids); err == nil {
				for i := range items {
					titles[items[i].ID] = items[i].Title()
				}
				// the batch leaves out what is gone without a word; asked one
				// at a time, a deleted item is a 404
				for _, id := range ids {
					if titles[id] == "" {
						_, gerr := client.Item(ctx, id)
						deleted[id] = abs.IsNotFound(gerr)
					}
				}
			}
		}

		out := bookmarksOut{User: u.Username, Bookmarks: []bookmarkRow{}}
		for _, b := range bms {
			if only != "" && b.LibraryItemID != only {
				continue
			}
			out.Bookmarks = append(out.Bookmarks, bookmarkRow{
				ItemID:  b.LibraryItemID,
				Item:    titles[b.LibraryItemID],
				Deleted: deleted[b.LibraryItemID],
				Title:   b.Title,
				Time:    b.Time,
				Created: fmtTime(b.CreatedAt),
			})
		}
		slices.SortStableFunc(out.Bookmarks, func(a, b bookmarkRow) int {
			if a.ItemID != b.ItemID {
				return cmp.Compare(a.Item, b.Item)
			}
			return cmp.Compare(a.Time, b.Time)
		})

		return nil, out, nil
	})

	type bookmarkEditIn struct {
		itemRef
		Action  string  `json:"action"          jsonschema:"add or remove"`
		Seconds float64 `json:"time_s"          jsonschema:"position in seconds; user_bookmarks' time_s when removing"`
		Title   string  `json:"title,omitempty" jsonschema:"bookmark name; required when adding"`
	}
	type bookmarkEditOut struct {
		Result    string       `json:"result"               jsonschema:"added, renamed (a bookmark was already at that position), or removed"`
		Bookmark  *bookmarkRow `json:"bookmark"             jsonschema:"the bookmark as it now stands, or the one removed"`
		WasTitled string       `json:"was_titled,omitempty" jsonschema:"renamed: the name it had before"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "user_bookmark_edit",
		Description: "Add a named bookmark at a position in a book, or remove the one at that position. Adding at a position that already has a bookmark renames it. Always the API key's own account - bookmarks belong to the user who made them. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in bookmarkEditIn) (*mcp.CallToolResult, bookmarkEditOut, error) {
		action := strings.ToLower(strings.TrimSpace(in.Action))
		if action != "add" && action != "remove" {
			return nil, bookmarkEditOut{}, fmt.Errorf("action %q must be add or remove", in.Action)
		}

		it, err := resolveItemToChange(ctx, client, in.Library, in.Item)
		if err != nil && abs.IsNotFound(err) && action == "remove" && looksLikeID(in.Item) {
			if me, merr := client.Me(ctx); merr == nil && slices.ContainsFunc(me.Bookmarks, func(b abs.Bookmark) bool { return b.LibraryItemID == strings.TrimSpace(in.Item) }) {
				return nil, bookmarkEditOut{}, fmt.Errorf("the item %s has been deleted, and Audiobookshelf will not remove a bookmark on an item it no longer has: the bookmark stays in the account", in.Item)
			}
		}
		if err != nil {
			return nil, bookmarkEditOut{}, err
		}
		if action == "add" && strings.TrimSpace(in.Title) == "" {
			return nil, bookmarkEditOut{}, errors.New("title is required when adding a bookmark")
		}
		// the server keeps an account's bookmarks as one list, read and
		// saved whole by every add and removal
		defer r.locks.hold("bookmarks")()

		rowOf := func(b *abs.Bookmark) *bookmarkRow {
			return &bookmarkRow{
				ItemID: it.ID, Item: it.Title(), Title: b.Title,
				Time: b.Time, Created: fmtTime(b.CreatedAt),
			}
		}
		// the one already at that position, which a removal takes and an add
		// renames
		bms, err := client.Bookmarks(ctx)
		if err != nil {
			return nil, bookmarkEditOut{}, err
		}
		held := bookmarkAt(bms, it.ID, in.Seconds)

		if action == "remove" {
			if held == nil {
				return nil, bookmarkEditOut{}, fmt.Errorf("no bookmark at %v seconds in %q: user_bookmarks lists them", in.Seconds, it.Title())
			}
			if err := client.DeleteBookmark(ctx, it.ID, in.Seconds); err != nil {
				return nil, bookmarkEditOut{}, err
			}
			// read back rather than trusted
			after, rerr := client.Bookmarks(ctx)
			switch {
			case rerr != nil:
				return nil, bookmarkEditOut{}, fmt.Errorf("removed %q, but reading the bookmarks back failed: %w", held.Title, rerr)
			case bookmarkAt(after, it.ID, in.Seconds) != nil:
				return nil, bookmarkEditOut{}, fmt.Errorf("the server accepted the removal but %q is still there", held.Title)
			}
			return nil, bookmarkEditOut{Result: "removed", Bookmark: rowOf(held)}, nil
		}

		out := bookmarkEditOut{Result: "added"}
		var b *abs.Bookmark
		if held != nil {
			out.Result, out.WasTitled = "renamed", held.Title
			b, err = client.UpdateBookmark(ctx, it.ID, in.Seconds, in.Title)
		} else if b, err = client.CreateBookmark(ctx, it.ID, in.Seconds, in.Title); err != nil {
			// made meanwhile by another client: renamed rather than duplicated
			out.Result = "renamed"
			b, err = client.UpdateBookmark(ctx, it.ID, in.Seconds, in.Title)
		}
		if err != nil {
			return nil, bookmarkEditOut{}, err
		}
		out.Bookmark = rowOf(b)

		return nil, out, nil
	})

	type historyIn struct {
		userRef
		Item    string `json:"item,omitempty"    jsonschema:"restrict to one item by id or title"`
		Library string `json:"library,omitempty"`
		Days    int    `json:"days,omitempty"    jsonschema:"how many days back, default 60"`
		Limit   int    `json:"limit,omitempty"   jsonschema:"maximum sessions, default 25"`
	}
	type historyOut struct {
		User     string           `json:"user"`
		Total    int              `json:"total_sessions" jsonschema:"all-time count"`
		Sessions []sessionSummary `json:"sessions"       jsonschema:"newest first"`
		Note     string           `json:"note,omitempty"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "user_history",
		Description: "A user's listening sessions, newest first, default last 60 days: what was played, for how long, on which device. Omit user for the account the API key acts as. For what is playing right now across everyone, use server_sessions.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in historyIn) (*mcp.CallToolResult, historyOut, error) {
		u, self, err := resolveUser(ctx, client, in.User)
		if err != nil {
			return nil, historyOut{}, err
		}
		limit := limitOr(in.Limit, defaultLimit)

		onlyItem := ""
		if in.Item != "" {
			it, rerr := resolveItem(ctx, client, in.Library, in.Item)
			if rerr != nil {
				return nil, historyOut{}, rerr
			}
			onlyItem = it.ID
		}

		cutoff := time.Now().AddDate(0, 0, -limitOr(in.Days, 60))
		var sessions []abs.Session
		var total int
		searched := true
		switch {
		case self && onlyItem != "":
			sessions, total, err = client.ItemListeningSessions(ctx, onlyItem, "", limit, 0)
			onlyItem = "" // the endpoint has already filtered
		case self:
			sessions, total, err = client.ListeningSessions(ctx, limit, 0)
		case onlyItem != "":
			sessions, total, searched, err = itemSessionsOf(ctx, client, u.ID, onlyItem, limit, cutoff)
			onlyItem = "" // filtered as it was paged
		default:
			sessions, total, err = client.UserSessions(ctx, u.ID, limit, 0)
		}
		if err != nil {
			return nil, historyOut{}, err
		}

		out := historyOut{User: u.Username, Total: total, Sessions: []sessionSummary{}}
		if !searched {
			out.Note = fmt.Sprintf("only their newest %d sessions were searched for the item, and total_sessions counts those", historyPages*historyPageSize)
		}
		for i := range sessions {
			if abs.Millis(sessions[i].UpdatedAt).Before(cutoff) {
				continue
			}
			if onlyItem != "" && sessions[i].LibraryItemID != onlyItem {
				continue
			}
			row := summarizeSession(&sessions[i])
			row.User = u.Username // every session here is theirs; the route does not say so
			out.Sessions = append(out.Sessions, row)
		}

		return nil, out, nil
	})

	type statsIn struct {
		userRef
		Year int `json:"year,omitempty" jsonschema:"year-in-review for this year instead of all-time totals; own account only"`
	}
	type itemTime struct {
		ID     string `json:"id"`
		Title  string `json:"title"`
		Author string `json:"author,omitempty"`
		Time   int    `json:"time_s"           jsonschema:"seconds listened; for a finished book, its length"`
	}
	type nameTime struct {
		Name string `json:"name"`
		Time int    `json:"time_s" jsonschema:"seconds listened"`
	}
	type statsOut struct {
		User          string     `json:"user"`
		Total         int        `json:"total_listened_s"         jsonschema:"seconds"`
		Today         int        `json:"today_s,omitempty"        jsonschema:"seconds"`
		Last7Days     int        `json:"last_7_days_s,omitempty"  jsonschema:"seconds"`
		Last30Days    int        `json:"last_30_days_s,omitempty" jsonschema:"seconds"`
		Items         int        `json:"items_listened,omitempty"`
		TopItems      []itemTime `json:"top_items,omitempty"      jsonschema:"most listened, all-time"`
		Sessions      int        `json:"sessions,omitempty"       jsonschema:"year view"`
		BooksListened int        `json:"books_listened,omitempty" jsonschema:"year view"`
		BooksFinished int        `json:"books_finished,omitempty" jsonschema:"year view"`
		TopAuthors    []nameTime `json:"top_authors,omitempty"    jsonschema:"year view"`
		TopNarrators  []nameTime `json:"top_narrators,omitempty"  jsonschema:"year view"`
		TopGenres     []nameTime `json:"top_genres,omitempty"     jsonschema:"year view"`
		Finished      []itemTime `json:"finished,omitempty"       jsonschema:"year view: books finished, with their length"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "user_stats",
		Description: "A user's listening statistics: total time, recent days, most listened items; or with year, a year-in-review with books finished and top authors, narrators and genres. Omit user for the account the API key acts as; year-in-review is only available for that account.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in statsIn) (*mcp.CallToolResult, statsOut, error) {
		u, self, err := resolveUser(ctx, client, in.User)
		if err != nil {
			return nil, statsOut{}, err
		}

		if in.Year > 0 {
			if !self {
				return nil, statsOut{}, fmt.Errorf("a year in review is only available for the account the API key acts as, not %s: omit user, or omit year for their all-time totals", u.Username)
			}
			ys, yerr := client.YearStats(ctx, in.Year)
			if yerr != nil {
				return nil, statsOut{}, yerr
			}
			out := statsOut{
				User:          u.Username,
				Total:         wholeSec(ys.TotalListeningTime),
				Sessions:      ys.TotalListeningSessions,
				BooksListened: ys.NumBooksListened,
				BooksFinished: ys.NumBooksFinished,
			}
			for _, a := range ys.TopAuthors {
				out.TopAuthors = append(out.TopAuthors, nameTime{Name: a.Name, Time: wholeSec(a.Time)})
			}
			for _, n := range ys.TopNarrators {
				out.TopNarrators = append(out.TopNarrators, nameTime{Name: n.Name, Time: wholeSec(n.Time)})
			}
			for _, g := range ys.TopGenres {
				out.TopGenres = append(out.TopGenres, nameTime{Name: g.Genre, Time: wholeSec(g.Time)})
			}
			for _, b := range ys.BooksFinished {
				out.Finished = append(out.Finished, itemTime{ID: b.ID, Title: b.Title, Time: wholeSec(b.Duration)})
			}
			return nil, out, nil
		}

		st, err := client.ListeningStats(ctx)
		if !self {
			st, err = client.UserListeningStats(ctx, u.ID)
		}
		if err != nil {
			return nil, statsOut{}, err
		}
		out := statsOut{User: u.Username, Total: wholeSec(st.TotalTime), Today: wholeSec(st.Today), Items: len(st.Items)}
		var week, month float64
		now := time.Now()
		for day, secs := range st.Days {
			d, err := time.Parse("2006-01-02", day)
			if err != nil {
				continue
			}
			if age := now.Sub(d); age <= 7*24*time.Hour {
				week += secs
			}
			if age := now.Sub(d); age <= 30*24*time.Hour {
				month += secs
			}
		}
		out.Last7Days, out.Last30Days = wholeSec(week), wholeSec(month)

		items := make([]abs.ListeningStatItem, 0, len(st.Items))
		for _, it := range st.Items {
			items = append(items, it)
		}
		slices.SortFunc(items, func(a, b abs.ListeningStatItem) int { return cmp.Compare(b.TimeListening, a.TimeListening) })
		for i, it := range items {
			if i >= 10 {
				break
			}
			var meta struct {
				Title      string `json:"title"`
				AuthorName string `json:"authorName"`
				Author     string `json:"author"`
			}
			_ = json.Unmarshal(it.MediaMetadata, &meta)
			author := meta.AuthorName
			if author == "" {
				author = meta.Author
			}
			out.TopItems = append(out.TopItems, itemTime{ID: it.ID, Title: meta.Title, Author: author, Time: wholeSec(it.TimeListening)})
		}

		return nil, out, nil
	})
}

// bookmarkAt is the bookmark at a position in an item, or nil. The server
// knows a bookmark by the two, and compares the time exactly.
func bookmarkAt(bms []abs.Bookmark, itemID string, seconds float64) *abs.Bookmark {
	for i := range bms {
		if bms[i].LibraryItemID == itemID && bms[i].Time == seconds {
			return &bms[i]
		}
	}
	return nil
}

// historyPageSize and historyPages bound how far back itemSessionsOf reads.
const (
	historyPageSize = 100
	historyPages    = 20
)

// itemSessionsOf is up to limit of a user's sessions on one item, newest
// first, back to the cutoff, and how many sessions they have on it in all.
// The route for someone else's sessions has no item filter, and the newest
// limit of them can hold none of the item's however many it has, so they are
// paged through to the end of their history, or until historyPages pages are
// read, which searched says was not the case. Those past the limit or the
// cutoff are only counted: the listener's own route counts every session on
// the item, and the two have to agree.
func itemSessionsOf(ctx context.Context, client *abs.Client, userID, itemID string, limit int, cutoff time.Time) (found []abs.Session, count int, searched bool, err error) {
	for page := range historyPages {
		sessions, total, err := client.UserSessions(ctx, userID, historyPageSize, page)
		if err != nil {
			return nil, 0, false, err
		}
		for i := range sessions {
			if sessions[i].LibraryItemID != itemID {
				continue
			}
			count++
			// newest first, so once one is past the cutoff the rest are too
			if len(found) < limit && !abs.Millis(sessions[i].UpdatedAt).Before(cutoff) {
				found = append(found, sessions[i])
			}
		}
		if len(sessions) < historyPageSize || (page+1)*historyPageSize >= total {
			return found, count, true, nil
		}
	}

	return found, count, false, nil
}
