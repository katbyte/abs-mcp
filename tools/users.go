package tools

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	api "github.com/katbyte/abs-mcp/sdk/abs"
	mcpregistry "github.com/katbyte/go-kt/mcp/registry"
	"github.com/katbyte/go-kt/pointer"
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
func resolveUser(ctx context.Context, gen *api.Client, nameOrID string) (*api.UserForBrowser, bool, error) {
	nameOrID = strings.TrimSpace(nameOrID)
	mine, meErr := record(gen.MeGetCurrentUser(ctx))
	me := &mine
	// "me" and the like mean the caller, as the argument says, whatever
	// the accounts are named
	if nameOrID == "" || slices.Contains(selfTokens, strings.ToLower(nameOrID)) {
		if meErr != nil {
			return nil, true, meErr
		}
		return me, true, nil
	}
	// a key whose own account cannot be read can still look others up; if
	// that fails as well, both are said
	isMe := func(id api.UserID) bool { return meErr == nil && id == me.Id }
	withMe := func(err error) error {
		if err != nil && meErr != nil {
			return fmt.Errorf("%w; reading the key's own account failed too: %w", err, meErr)
		}
		return err
	}
	if looksLikeID(nameOrID) {
		if isMe(api.UserID(nameOrID)) {
			return me, true, nil
		}
		return otherUser(ctx, gen, api.UserID(nameOrID), withMe)
	}

	all, lookupErr := record(gen.UserFindAll(ctx))
	users := all.Users
	switch {
	case lookupErr == nil:
		names := make([]string, 0, len(users))
		for i := range users {
			if strings.EqualFold(users[i].Username, nameOrID) {
				if isMe(users[i].Id) {
					return me, true, nil
				}
				return otherUser(ctx, gen, users[i].Id, withMe)
			}
			names = append(names, users[i].Username)
		}
		lookupErr = fmt.Errorf("no user named %q (have: %s)", nameOrID, strings.Join(names, ", "))
	// a key that may not list the accounts, answered as refused or as a
	// route that is not there: it can still name its own
	case api.IsForbidden(lookupErr) || api.IsNotFound(lookupErr):
	default:
		return nil, false, withMe(fmt.Errorf("looking up the user %q: %w", nameOrID, lookupErr))
	}

	// the caller's own username, which a non-admin key cannot look up
	if meErr == nil && strings.EqualFold(me.Username, nameOrID) {
		return me, true, nil
	}

	return nil, false, withMe(lookupErr)
}

// otherUser is another account, read by its id, with what went wrong reading the key's own account said beside a failure.
func otherUser(ctx context.Context, gen *api.Client, id api.UserID, withMe func(error) error) (*api.UserForBrowser, bool, error) {
	u, err := record(gen.UserFindOne(ctx, id))
	if err != nil {
		return nil, false, withMe(err)
	}

	return &u, false, nil
}

type userRow struct {
	ID        string `json:"id"`
	Username  string `json:"username"`
	Type      string `json:"type"`
	Active    bool   `json:"active"`
	LastSeen  string `json:"last_seen,omitempty"`
	Listening string `json:"last_listened,omitempty" jsonschema:"title of their most recent session"`
}

type progressRow struct {
	ItemID    string `json:"item_id"`
	EpisodeID string `json:"episode_id,omitempty"`
	Percent   int    `json:"percent"`
	Finished  bool   `json:"finished"`
	Updated   string `json:"updated,omitempty"`
}

type userDetail struct {
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

// describeUser is an account as user_get answers it. The server grants a
// permission by the permission alone, not by the account's type, and
// only while the account is active: an admin without delete may not
// delete
func describeUser(u *api.UserForBrowser, self bool) userDetail {
	active, perms := pointer.From(u.IsActive), pointer.From(u.Permissions)
	row := userRow{ID: string(u.Id), Username: u.Username, Type: u.Type, Active: active, LastSeen: fmtTime(u.LastSeen)}
	out := userDetail{
		userRow:      row,
		Self:         self,
		Email:        u.Email,
		CanUpdate:    active && pointer.From(perms.Update),
		CanDelete:    active && pointer.From(perms.Delete),
		CanDownload:  active && pointer.From(perms.Download),
		CanUpload:    active && pointer.From(perms.Upload),
		AllLibraries: pointer.From(perms.AccessAllLibraries),
		AllTags:      pointer.From(perms.AccessAllTags),
		Explicit:     pointer.From(perms.AccessExplicitContent),
		Bookmarks:    len(u.Bookmarks),
		Created:      fmtDate(u.CreatedAt),
		Recent:       []progressRow{},
	}
	if !out.AllLibraries {
		out.Libraries = u.LibrariesAccessible
	}
	if !out.AllTags {
		if pointer.From(perms.SelectedTagsNotAccessible) {
			out.DeniedTags = u.ItemTagsSelected
		} else {
			out.Tags = u.ItemTagsSelected
		}
	}
	progress := slices.Clone(u.MediaProgress)
	slices.SortFunc(progress, func(a, b api.MediaProgress) int { return b.LastUpdate.Compare(a.LastUpdate.Time) })
	for _, p := range progress {
		switch {
		case p.IsFinished:
			out.Finished++
		case p.CurrentTime > 0 || p.EbookProgress > 0:
			out.InProgress++
		}
		if len(out.Recent) < 10 {
			out.Recent = append(out.Recent, progressRow{ItemID: string(p.LibraryItemId), EpisodeID: string(p.EpisodeId), Percent: percent(p.Progress), Finished: p.IsFinished, Updated: fmtTime(p.LastUpdate)})
		}
	}
	return out
}

func registerUserTools(r *registry) {
	gen := r.gen

	type listOut struct {
		Total      int       `json:"total"`
		Offset     int       `json:"offset"`
		NextOffset int       `json:"next_offset,omitempty" jsonschema:"pass back as offset for the next page; absent on the last"`
		Users      []userRow `json:"users"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "user_list",
		Description: "List the server's user accounts with type, last seen, and what they last listened to. Admin only; every other user tool defaults to the API key's own account and needs no admin rights.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in pageIn) (*mcp.CallToolResult, listOut, error) {
		all, err := record(gen.UserFindAll(ctx, api.UserFindAllOptions{Include: "latestSession"}))
		if err != nil {
			return nil, listOut{}, err
		}
		users := all.Users
		limit, offset := pageArgs(in.Limit, in.Offset, 50)
		page, next := pageOf(users, limit, offset)
		out := listOut{Total: len(users), Offset: offset, NextOffset: next, Users: []userRow{}}
		for i := range page {
			u := &page[i]
			row := userRow{ID: string(u.Id), Username: u.Username, Type: u.Type, Active: pointer.From(u.IsActive), LastSeen: fmtTime(u.LastSeen)}
			if u.LatestSession != nil {
				row.Listening = u.LatestSession.DisplayTitle
			}
			out.Users = append(out.Users, row)
		}

		return nil, out, nil
	})

	add(r, readTool, &mcp.Tool{
		Name:        "user_get",
		Description: "An account's details, permissions, and a summary of its listening progress. Omit user for the account the API key acts as - the usual case, and the only one available without admin rights. Call this first when unsure what the key can do.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in userRef) (*mcp.CallToolResult, userDetail, error) {
		u, self, err := resolveUser(ctx, gen, in.User)
		if err != nil {
			return nil, userDetail{}, err
		}
		return nil, describeUser(u, self), nil
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
		u, self, err := resolveUser(ctx, gen, in.User)
		if err != nil {
			return nil, inProgressOut{}, err
		}
		limit := mcpregistry.LimitOr(in.Limit, defaultLimit)

		out := inProgressOut{Items: []inProgressRow{}}
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
			shelf, serr := record(gen.MeGetAllLibraryItemsInProgress(ctx, api.MeGetAllLibraryItemsInProgressOptions{Limit: new(limit + hidden)}))
			if serr != nil {
				return nil, inProgressOut{}, serr
			}
			for i := range shelf.LibraryItems {
				it := &shelf.LibraryItems[i]
				var want api.EpisodeID
				if it.RecentEpisode != nil {
					want = it.RecentEpisode.Id
				}
				var mine *api.MediaProgress
				for j := range u.MediaProgress {
					if p := &u.MediaProgress[j]; p.LibraryItemId == it.Id && p.EpisodeId == want {
						mine = p
					}
				}
				if mine != nil && mine.HideFromContinueListening {
					continue
				}
				if len(out.Items) == limit {
					break
				}

				row := inProgressRow{itemSummary: summarize(it)}
				row.Progress = progressOf(mine)
				if it.RecentEpisode != nil {
					ep := summarizeEpisode(it.RecentEpisode, it.Title(), false, mine)
					row.Episode, row.Progress = &ep, nil
				}
				out.Items = append(out.Items, row)
			}

			return nil, out, nil
		}
		// for anyone else the shelf has to be rebuilt from their progress
		// records, which name items by id only
		started := make([]api.MediaProgress, 0, len(u.MediaProgress))
		for _, p := range u.MediaProgress {
			if p.IsFinished || p.HideFromContinueListening {
				continue
			}
			if p.CurrentTime > 0 || p.EbookProgress > 0 {
				started = append(started, p)
			}
		}
		slices.SortFunc(started, func(a, b api.MediaProgress) int { return b.LastUpdate.Compare(a.LastUpdate.Time) })
		if len(started) > limit {
			started = started[:limit]
		}
		ids := make([]api.LibraryItemID, 0, len(started))
		progressFor := make(map[api.LibraryItemID]*api.MediaProgress, len(started))
		for i := range started {
			p := &started[i]
			if _, seen := progressFor[p.LibraryItemId]; !seen {
				ids = append(ids, p.LibraryItemId)
			}
			progressFor[p.LibraryItemId] = p
		}
		if len(ids) == 0 {
			return nil, out, nil
		}

		items, err := itemsBatch(ctx, gen, ids)
		if err != nil {
			return nil, inProgressOut{}, err
		}
		// the batch answers in its own order; restore theirs
		rank := map[api.LibraryItemID]int{}
		for i, id := range ids {
			rank[id] = i
		}
		slices.SortFunc(items, func(a, b api.LibraryItemExpanded) int { return cmp.Compare(rank[a.Id], rank[b.Id]) })

		for i := range items {
			it := &items[i]
			p := progressFor[it.Id]
			row := inProgressRow{itemSummary: summarizeWhole(it)}
			row.Progress = progressOf(p)
			if p != nil && p.EpisodeId != "" {
				for j := range it.Media.Episodes {
					if it.Media.Episodes[j].Id == p.EpisodeId {
						ep := summarizeEpisode(&it.Media.Episodes[j], it.Title(), false, p)
						row.Episode, row.Progress = &ep, nil
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
		u, self, err := resolveUser(ctx, gen, in.User)
		if err != nil {
			return nil, progressGetOut{}, err
		}
		it, err := resolveItem(ctx, gen, in.Library, in.Item)
		if err != nil {
			return nil, progressGetOut{}, err
		}
		out := progressGetOut{User: u.Username, Item: it.Title(), Duration: wholeSec(it.Media.Duration)}
		if in.Episode != "" {
			for i := range it.Media.Episodes {
				if e := &it.Media.Episodes[i]; string(e.Id) == in.Episode {
					out.Episode = e.Title
					out.Duration = wholeSec(episodeLength(e))
				}
			}
			if out.Episode == "" {
				return nil, progressGetOut{}, fmt.Errorf("no episode %s in %q", in.Episode, it.Title())
			}
		}

		if self {
			p, err := ownProgress(ctx, gen, it.Id, in.Episode)
			if err != nil {
				return nil, progressGetOut{}, err
			}
			out.Progress = progressOf(p)

			return nil, out, nil
		}
		for i := range u.MediaProgress {
			if p := &u.MediaProgress[i]; p.LibraryItemId == it.Id && string(p.EpisodeId) == in.Episode {
				out.Progress = progressOf(p)
				break
			}
		}

		return nil, out, nil
	})

	type progressSetIn struct {
		Item     string   `json:"item,omitempty"               jsonschema:"library item id, or its whole title; or series instead"`
		Library  string   `json:"library,omitempty"            jsonschema:"narrow a title lookup to one library by name or id"`
		Episode  string   `json:"episode,omitempty"            jsonschema:"episode id for podcasts; omit for books"`
		Series   string   `json:"series,omitempty"             jsonschema:"instead of item: a series by name or id, with hide_from_continue alone, to take it off (true) or put it back on (false) the Continue Series shelf, which offers the next book of a series once one is finished"`
		Finished *bool    `json:"finished,omitempty"           jsonschema:"mark finished (true) or not finished (false)"`
		Position *float64 `json:"position_s,omitempty"         jsonschema:"set the playback position in seconds"`
		Percent  *float64 `json:"percent,omitempty"            jsonschema:"set the position as a percentage 0-100 instead"`
		Hide     *bool    `json:"hide_from_continue,omitempty" jsonschema:"remove from (true) or restore to (false) the continue-listening shelf; with series, the Continue Series shelf"`
		Remove   bool     `json:"remove,omitempty"             jsonschema:"instead of setting anything: delete the progress on the book or episode, resetting it to never started"`
	}
	type progressSetOut struct {
		progressGetOut
		Series       string `json:"series,omitempty"        jsonschema:"with series: the series"`
		SeriesHidden *bool  `json:"series_hidden,omitempty" jsonschema:"with series: kept off the Continue Series shelf, read back from the account"`
		Removed      *bool  `json:"removed,omitempty"       jsonschema:"with remove: false when there was no progress to remove"`
	}
	// hideSeries takes a series off the Continue Series shelf or puts it
	// back, and reads the account back to say which it is
	hideSeries := func(ctx context.Context, in progressSetIn) (progressSetOut, error) {
		if in.Item != "" || in.Episode != "" {
			return progressSetOut{}, errors.New("series stands in for item; pass one or the other")
		}
		if in.Hide == nil {
			return progressSetOut{}, errors.New("with series, pass hide_from_continue: true takes it off the Continue Series shelf, false puts it back")
		}
		if in.Finished != nil || in.Position != nil || in.Percent != nil {
			return progressSetOut{}, errors.New("a series has no progress of its own: set finished, position_s or percent on a book")
		}

		s, err := resolveSeries(ctx, gen, in.Library, in.Series)
		if err != nil {
			return progressSetOut{}, err
		}
		if *in.Hide {
			_, err = gen.MeRemoveSeriesFromContinueListening(ctx, s.Id)
		} else {
			_, err = gen.MeReaddSeriesFromContinueListening(ctx, s.Id)
		}
		if err != nil {
			return progressSetOut{}, err
		}
		me, err := record(gen.MeGetCurrentUser(ctx))
		if err != nil {
			return progressSetOut{}, fmt.Errorf("the series was sent, but reading the account back failed: %w", err)
		}
		hidden := slices.Contains(me.SeriesHideFromContinueListening, string(s.Id))
		if hidden != *in.Hide {
			return progressSetOut{}, fmt.Errorf("the server was asked to change %q on the Continue Series shelf, but the account still has it %s", s.Name, map[bool]string{true: "hidden", false: "shown"}[hidden])
		}
		return progressSetOut{Series: s.Name, SeriesHidden: &hidden}, nil
	}
	// removeProgress deletes the account's progress on a book or episode, and
	// reads it back: the server has answered a removal it did not make
	removeProgress := func(ctx context.Context, in progressSetIn) (progressSetOut, error) {
		it, err := resolveItemToChange(ctx, gen, in.Library, in.Item)
		if err != nil {
			return progressSetOut{}, err
		}
		p, err := ownProgress(ctx, gen, it.Id, in.Episode)
		if err != nil {
			return progressSetOut{}, err
		}
		out := progressSetOut{Item: it.Title(), Removed: new(false)}
		if p == nil {
			return out, nil
		}
		if _, err := gen.MeRemoveMediaProgress(ctx, p.Id); err != nil {
			return progressSetOut{}, err
		}
		left, err := ownProgress(ctx, gen, it.Id, in.Episode)
		if err != nil {
			return progressSetOut{}, fmt.Errorf("the server accepted the removal of %q's progress, but reading it back to check failed: %w", it.Title(), err)
		}
		if left != nil {
			return progressSetOut{}, fmt.Errorf("the server accepted the removal but %q still has progress", it.Title())
		}
		out.Removed = new(true)
		return out, nil
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "user_progress_set",
		Description: "Update the API key user's progress on a book or episode: mark finished/unfinished, set the position (seconds or percent), hide it from continue-listening, or with remove delete the progress; or hide a whole series from the Continue Series shelf. Always the API key's own account - Audiobookshelf has no way to set someone else's progress. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in progressSetIn) (*mcp.CallToolResult, progressSetOut, error) {
		set := in.Finished != nil || in.Position != nil || in.Percent != nil || in.Hide != nil
		switch {
		case in.Remove && (set || in.Series != ""):
			return nil, progressSetOut{}, errors.New("remove deletes the progress, and takes nothing to set; pass it alone with the item")
		case in.Remove:
			out, err := removeProgress(ctx, in)
			return nil, out, err
		case in.Series != "":
			out, err := hideSeries(ctx, in)
			return nil, out, err
		}
		switch {
		case in.Percent != nil && (*in.Percent < 0 || *in.Percent > 100):
			return nil, progressSetOut{}, fmt.Errorf("percent %v is not between 0 and 100", *in.Percent)
		case in.Position != nil && *in.Position < 0:
			return nil, progressSetOut{}, fmt.Errorf("position_s %v is before the start", *in.Position)
		}
		it, err := resolveItemToChange(ctx, gen, in.Library, in.Item)
		if err != nil {
			return nil, progressSetOut{}, err
		}
		// a podcast's progress is kept per episode; one set on the show
		// itself is on nothing that plays, and it has no length, so any
		// percent of it would be position 0
		if it.IsPodcast() && in.Episode == "" {
			return nil, progressSetOut{}, fmt.Errorf("%q is a podcast: name the episode (podcast_episodes lists them)", it.Title())
		}
		duration := it.Media.Duration
		if in.Episode != "" {
			found := false
			for i := range it.Media.Episodes {
				if e := &it.Media.Episodes[i]; string(e.Id) == in.Episode {
					duration, found = episodeLength(e), true
				}
			}
			if !found {
				return nil, progressSetOut{}, fmt.Errorf("no episode %s in %q", in.Episode, it.Title())
			}
		}
		if in.Position == nil && in.Percent != nil && duration <= 0 {
			return nil, progressSetOut{}, fmt.Errorf("%q has no length to take a percent of: pass position_s", it.Title())
		}

		upd := api.ProgressUpdatePayload{IsFinished: in.Finished, HideFromContinueListening: in.Hide}
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
			return nil, progressSetOut{}, errors.New("nothing to change: pass finished, position_s, percent or hide_from_continue")
		}
		if in.Episode == "" {
			_, err = gen.MeCreateUpdateMediaProgress2(ctx, it.Id, upd)
		} else {
			_, err = gen.MeCreateUpdateMediaProgress(ctx, it.Id, api.EpisodeID(in.Episode), upd)
		}
		if err != nil {
			return nil, progressSetOut{}, err
		}

		p, err := ownProgress(ctx, gen, it.Id, in.Episode)
		if err != nil {
			return nil, progressSetOut{}, err
		}

		return nil, progressSetOut{Item: it.Title(), Duration: wholeSec(duration), Progress: progressOf(p)}, nil
	})

	type bookmarkRow struct {
		ItemID  string  `json:"item_id"`
		Item    string  `json:"item,omitempty"`
		Deleted bool    `json:"item_deleted,omitempty" jsonschema:"the item is no longer on the server; Audiobookshelf keeps the bookmark and will not remove it"`
		Title   string  `json:"title"`
		Time    float64 `json:"time_s"                 jsonschema:"position in seconds, exactly as held: pass it in user_bookmark_edit remove_bookmarks to remove it"`
		Created string  `json:"created,omitempty"`
		// set by user_bookmark_edit on a bookmark it renamed
		WasTitled string `json:"was_titled,omitempty" jsonschema:"renamed: the name it had before"`
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
		u, self, err := resolveUser(ctx, gen, in.User)
		if err != nil {
			return nil, bookmarksOut{}, err
		}
		bms := u.Bookmarks
		if self {
			if bms, err = ownBookmarks(ctx, gen); err != nil {
				return nil, bookmarksOut{}, err
			}
		}
		var only api.LibraryItemID
		if in.Item != "" {
			it, err := resolveItem(ctx, gen, in.Library, in.Item)
			if err != nil {
				return nil, bookmarksOut{}, err
			}
			only = it.Id
		}

		titles := map[api.LibraryItemID]string{}
		var ids []api.LibraryItemID
		for _, b := range bms {
			if only != "" && b.LibraryItemId != only {
				continue
			}
			if _, seen := titles[b.LibraryItemId]; !seen {
				titles[b.LibraryItemId] = ""
				ids = append(ids, b.LibraryItemId)
			}
		}
		deleted := map[api.LibraryItemID]bool{}
		if len(ids) > 0 {
			items, err := itemsBatch(ctx, gen, ids)
			if err != nil {
				return nil, bookmarksOut{}, fmt.Errorf("reading the bookmarked items: %w", err)
			}
			for i := range items {
				titles[items[i].Id] = items[i].Title()
			}
			// the batch leaves out what is gone without a word; asked one
			// at a time, a deleted item is a 404
			for _, id := range ids {
				if titles[id] != "" {
					continue
				}
				_, gerr := getItem(ctx, gen, id)
				switch {
				case api.IsNotFound(gerr):
					deleted[id] = true
				case gerr != nil:
					return nil, bookmarksOut{}, fmt.Errorf("reading bookmarked item %s, which the batch left out: %w", id, gerr)
				}
			}
		}

		out := bookmarksOut{User: u.Username, Bookmarks: []bookmarkRow{}}
		for _, b := range bms {
			if only != "" && b.LibraryItemId != only {
				continue
			}
			out.Bookmarks = append(out.Bookmarks, bookmarkRow{
				ItemID:  string(b.LibraryItemId),
				Item:    titles[b.LibraryItemId],
				Deleted: deleted[b.LibraryItemId],
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

	type bookmarkAdd struct {
		Seconds float64 `json:"time_s" jsonschema:"position in seconds"`
		Title   string  `json:"title"  jsonschema:"bookmark name"`
	}
	type bookmarkEditIn struct {
		itemRef
		Add    []bookmarkAdd `json:"add_bookmarks,omitempty"    jsonschema:"bookmarks to add, each a position and a name; one at a position that already has a bookmark renames it"`
		Remove []float64     `json:"remove_bookmarks,omitempty" jsonschema:"positions in seconds of the bookmarks to remove: user_bookmarks' time_s"`
	}
	type bookmarkEditOut struct {
		Added   []bookmarkRow `json:"added,omitempty"`
		Renamed []bookmarkRow `json:"renamed,omitempty" jsonschema:"a bookmark was already at that position: it has the new name, and was_titled the old"`
		Removed []bookmarkRow `json:"removed,omitempty"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "user_bookmark_edit",
		Description: "Add named bookmarks at positions in a book, or remove the ones at positions. Adding at a position that already has a bookmark renames it. Always the API key's own account - bookmarks belong to the user who made them. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in bookmarkEditIn) (*mcp.CallToolResult, bookmarkEditOut, error) {
		if len(in.Add) == 0 && len(in.Remove) == 0 {
			return nil, bookmarkEditOut{}, errors.New("nothing to change: pass add_bookmarks or remove_bookmarks")
		}
		for i, a := range in.Add {
			switch {
			case strings.TrimSpace(a.Title) == "":
				return nil, bookmarkEditOut{}, fmt.Errorf("the bookmark to add at %v seconds has no title", a.Seconds)
			case slices.Contains(in.Remove, a.Seconds):
				return nil, bookmarkEditOut{}, fmt.Errorf("%v seconds is in both add_bookmarks and remove_bookmarks; one or the other", a.Seconds)
			case slices.ContainsFunc(in.Add[:i], func(o bookmarkAdd) bool { return o.Seconds == a.Seconds }):
				return nil, bookmarkEditOut{}, fmt.Errorf("add_bookmarks names %v seconds twice", a.Seconds)
			}
		}

		it, err := resolveItemToChange(ctx, gen, in.Library, in.Item)
		if err != nil && api.IsNotFound(err) && len(in.Remove) > 0 && looksLikeID(in.Item) {
			me, merr := record(gen.MeGetCurrentUser(ctx))
			if merr != nil {
				return nil, bookmarkEditOut{}, fmt.Errorf("%w; reading the account's bookmarks to say whether one is left on it also failed: %w", err, merr)
			}
			if slices.ContainsFunc(me.Bookmarks, func(b api.AudioBookmark) bool { return string(b.LibraryItemId) == strings.TrimSpace(in.Item) }) {
				return nil, bookmarkEditOut{}, fmt.Errorf("the item %s has been deleted, and Audiobookshelf will not remove a bookmark on an item it no longer has: the bookmark stays in the account", in.Item)
			}
		}
		if err != nil {
			return nil, bookmarkEditOut{}, err
		}
		// the server keeps an account's bookmarks as one list, read and
		// saved whole by every add and removal
		defer r.locks.ByString(bookmarksLock)()

		rowOf := func(b *api.AudioBookmark) bookmarkRow {
			return bookmarkRow{
				ItemID: string(it.Id), Item: it.Title(), Title: b.Title,
				Time: b.Time, Created: fmtTime(b.CreatedAt),
			}
		}
		// the ones already at those positions, which a removal takes and an
		// add renames: every removal is checked before anything is sent
		bms, err := ownBookmarks(ctx, gen)
		if err != nil {
			return nil, bookmarkEditOut{}, err
		}
		for _, at := range in.Remove {
			if bookmarkAt(bms, it.Id, at) == nil {
				return nil, bookmarkEditOut{}, fmt.Errorf("no bookmark at %v seconds in %q: user_bookmarks lists them; nothing was changed", at, it.Title())
			}
		}

		// a change that fails after others were made says which those were
		out := bookmarkEditOut{}
		failed := func(err error) error {
			if n := len(out.Added) + len(out.Renamed) + len(out.Removed); n > 0 {
				return fmt.Errorf("%d bookmarks were changed before this, and stay changed (%d removed, %d added, %d renamed): %w", n, len(out.Removed), len(out.Added), len(out.Renamed), err)
			}
			return err
		}
		for _, at := range slices.Compact(slices.Sorted(slices.Values(in.Remove))) {
			held := bookmarkAt(bms, it.Id, at)
			if _, err := gen.MeRemoveBookmark(ctx, it.Id, strconv.FormatFloat(at, 'f', -1, 64)); err != nil {
				return nil, bookmarkEditOut{}, failed(err)
			}
			out.Removed = append(out.Removed, rowOf(held))
		}
		if len(out.Removed) > 0 {
			// read back rather than trusted
			after, rerr := ownBookmarks(ctx, gen)
			if rerr != nil {
				return nil, bookmarkEditOut{}, failed(fmt.Errorf("reading the bookmarks back after the removal failed: %w", rerr))
			}
			for _, gone := range out.Removed {
				if bookmarkAt(after, it.Id, gone.Time) != nil {
					return nil, bookmarkEditOut{}, fmt.Errorf("the server accepted the removal but %q is still there", gone.Title)
				}
			}
		}
		for _, a := range in.Add {
			held := bookmarkAt(bms, it.Id, a.Seconds)
			renamed := held != nil
			rename := func() (api.AudioBookmark, error) {
				return record(gen.MeUpdateBookmark(ctx, it.Id, api.MeUpdateBookmarkRequest{Time: a.Seconds, Title: a.Title}))
			}
			var b api.AudioBookmark
			if renamed {
				b, err = rename()
			} else if b, err = record(gen.MeCreateBookmark(ctx, it.Id, api.MeCreateBookmarkRequest{Time: a.Seconds, Title: a.Title})); err != nil {
				// made meanwhile by another client: renamed rather than
				// duplicated. When that fails too, both say what went wrong
				renamed = true
				addErr := err
				if b, err = rename(); err != nil {
					err = fmt.Errorf("adding the bookmark failed: %w; renaming one made meanwhile at that time failed too: %w", addErr, err)
				}
			}
			if err != nil {
				return nil, bookmarkEditOut{}, failed(err)
			}
			row := rowOf(&b)
			if renamed {
				if held != nil {
					row.WasTitled = held.Title
				}
				out.Renamed = append(out.Renamed, row)
			} else {
				out.Added = append(out.Added, row)
			}
		}

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
		u, self, err := resolveUser(ctx, gen, in.User)
		if err != nil {
			return nil, historyOut{}, err
		}
		limit := mcpregistry.LimitOr(in.Limit, defaultLimit)

		var onlyItem api.LibraryItemID
		if in.Item != "" {
			it, rerr := resolveItem(ctx, gen, in.Library, in.Item)
			if rerr != nil {
				return nil, historyOut{}, rerr
			}
			onlyItem = it.Id
		}

		cutoff := time.Now().AddDate(0, 0, -mcpregistry.LimitOr(in.Days, 60))
		var sessions []api.PlaybackSession
		var total int
		searched := true
		switch {
		case self && onlyItem != "":
			var got api.MeGetItemListeningSessions2Answer
			got, err = record(gen.MeGetItemListeningSessions2(ctx, onlyItem, api.MeGetItemListeningSessions2Options{ItemsPerPage: &limit, Page: new(0)}))
			sessions, total = got.Sessions, got.Total
			onlyItem = "" // the endpoint has already filtered
		case onlyItem != "":
			sessions, total, searched, err = itemSessionsOf(ctx, gen, u.Id, onlyItem, limit, cutoff)
			onlyItem = "" // filtered as it was paged
		default:
			sessions, total, err = sessionPage(ctx, gen, u.Id, self, limit, 0)
		}
		if err != nil {
			return nil, historyOut{}, err
		}

		out := historyOut{User: u.Username, Total: total, Sessions: []sessionSummary{}}
		if !searched {
			out.Note = fmt.Sprintf("only their newest %d sessions were searched for the item, and total_sessions counts those", historyPages*historyPageSize)
		}
		for i := range sessions {
			if sessions[i].UpdatedAt.Before(cutoff) {
				continue
			}
			if onlyItem != "" && sessions[i].LibraryItemId != onlyItem {
				continue
			}
			row := summarizeSession(&sessions[i])
			row.User = u.Username // every session here is theirs; the route does not say so
			out.Sessions = append(out.Sessions, row)
		}

		return nil, out, nil
	})

	type historyRemoveIn struct {
		userRef
		Sessions []string `json:"sessions"          jsonschema:"the ids of the sessions to remove, as user_history lists them"`
		Confirm  bool     `json:"confirm,omitempty" jsonschema:"true to remove; without it nothing changes and the answer lists what a confirmed call removes"`
	}
	type historyRemoveOut struct {
		User        string           `json:"user"`
		WouldRemove []sessionSummary `json:"would_remove,omitempty" jsonschema:"without confirm: the sessions a confirmed call removes; nothing has changed"`
		Removed     []sessionSummary `json:"removed,omitempty"      jsonschema:"the sessions removed, and checked gone"`
	}
	add(r, deleteTool, &mcp.Tool{
		Name: "user_history_remove",
		Description: "Remove listening sessions from an account's history: a session logged twice, or a book left playing overnight. Progress and finished marks stay; the sessions' listening time goes from user_stats and the year in review. " +
			"Omit user for the account the API key acts as. Without confirm=true nothing changes and the answer lists the sessions that would go. Requires the delete permission.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in historyRemoveIn) (*mcp.CallToolResult, historyRemoveOut, error) {
		if len(in.Sessions) == 0 {
			return nil, historyRemoveOut{}, errors.New("sessions is required: the ids user_history lists")
		}
		u, self, err := resolveUser(ctx, gen, in.User)
		if err != nil {
			return nil, historyRemoveOut{}, err
		}
		// the server reads a stored session by no id, and deletes any id it
		// is given whosever it is: each is found in this account's own history
		found, err := sessionsOf(ctx, gen, u.Id, self, in.Sessions)
		if err != nil {
			return nil, historyRemoveOut{}, err
		}
		var missing []string
		rows := make([]sessionSummary, 0, len(in.Sessions))
		for _, id := range in.Sessions {
			ses, ok := found[id]
			if !ok {
				missing = append(missing, id)
				continue
			}
			row := summarizeSession(&ses)
			row.User = u.Username
			rows = append(rows, row)
		}
		if len(missing) > 0 {
			return nil, historyRemoveOut{}, fmt.Errorf("%s has no session %s: pass ids from user_history for that account", u.Username, strings.Join(missing, ", "))
		}
		out := historyRemoveOut{User: u.Username}
		if !in.Confirm {
			out.WouldRemove = rows
			return nil, out, nil
		}

		for i, row := range rows {
			if _, err := gen.SessionDelete(ctx, row.ID); err != nil {
				if i == 0 {
					return nil, historyRemoveOut{}, err
				}
				return nil, historyRemoveOut{}, fmt.Errorf("%d of the %d sessions were removed, then removing %s failed: %w", i, len(rows), row.ID, err)
			}
		}
		left, err := sessionsOf(ctx, gen, u.Id, self, in.Sessions)
		if err != nil {
			return nil, historyRemoveOut{}, fmt.Errorf("the sessions were removed, but reading the history back failed: %w", err)
		}
		if len(left) > 0 {
			ids := make([]string, 0, len(left))
			for id := range left {
				ids = append(ids, id)
			}
			slices.Sort(ids)
			return nil, historyRemoveOut{}, fmt.Errorf("the server answered each removal, but %s's history still holds %s", u.Username, strings.Join(ids, ", "))
		}
		out.Removed = rows
		return nil, out, nil
	})

	type statsIn struct {
		userRef
		Year   int  `json:"year,omitempty"   jsonschema:"year-in-review for this year instead of all-time totals; own account only, or with server every account's"`
		Server bool `json:"server,omitempty" jsonschema:"with year: the whole server's year instead of one account's, every account's listening and what the library gained. Admin only"`
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
		BooksAdded    int        `json:"books_added,omitempty"    jsonschema:"server year: books added to the library that year"`
		AddedSize     int64      `json:"added_size,omitempty"     jsonschema:"server year: bytes"`
		AddedLength   int        `json:"added_s,omitempty"        jsonschema:"server year: the books added, in seconds"`
		AuthorsAdded  int        `json:"authors_added,omitempty"  jsonschema:"server year"`
		Books         int        `json:"books,omitempty"          jsonschema:"server year: the library at the end of the year, every book added by then"`
		Size          int64      `json:"size,omitempty"           jsonschema:"server year: bytes"`
		Length        int        `json:"length_s,omitempty"       jsonschema:"server year: seconds"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "user_stats",
		Description: "A user's listening statistics: total time, recent days, most listened items; or with year, a year-in-review with books finished and top authors, narrators and genres. Omit user for the account the API key acts as; year-in-review is only available for that account, or with server for the whole server: every account's listening and what the library gained.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in statsIn) (*mcp.CallToolResult, statsOut, error) {
		if in.Server {
			switch {
			case in.Year == 0:
				return nil, statsOut{}, errors.New("server is a year in review of the whole server: pass year")
			case in.User != "":
				return nil, statsOut{}, errors.New("server covers every account; omit user")
			}
			ys, err := record(gen.StatsGetAdminStatsForYear(ctx, strconv.Itoa(in.Year)))
			if err != nil {
				return nil, statsOut{}, err
			}
			out := statsOut{
				User:         "every account",
				Total:        wholeSec(ys.TotalListeningTime),
				Sessions:     ys.NumListeningSessions,
				BooksAdded:   ys.NumBooksAdded,
				AddedSize:    int64(ys.TotalBooksAddedSize),
				AddedLength:  ys.TotalBooksAddedDuration,
				AuthorsAdded: ys.NumAuthorsAdded,
				Books:        int(ys.NumBooks),
				Size:         int64(ys.TotalBooksSize),
				Length:       wholeSec(ys.TotalBooksDuration),
			}
			for _, a := range ys.TopAuthors {
				out.TopAuthors = append(out.TopAuthors, nameTime{Name: a.Name, Time: a.Time})
			}
			for _, n := range ys.TopNarrators {
				out.TopNarrators = append(out.TopNarrators, nameTime{Name: n.Name, Time: n.Time})
			}
			for _, g := range ys.TopGenres {
				out.TopGenres = append(out.TopGenres, nameTime{Name: g.Genre, Time: g.Time})
			}
			return nil, out, nil
		}
		u, self, err := resolveUser(ctx, gen, in.User)
		if err != nil {
			return nil, statsOut{}, err
		}

		if in.Year > 0 {
			if !self {
				return nil, statsOut{}, fmt.Errorf("a year in review is only available for the account the API key acts as, not %s: omit user, or omit year for their all-time totals", u.Username)
			}
			ys, yerr := record(gen.MeGetStatsForYear(ctx, strconv.Itoa(in.Year)))
			if yerr != nil {
				return nil, statsOut{}, yerr
			}
			out := statsOut{
				User:          u.Username,
				Total:         ys.TotalListeningTime,
				Sessions:      ys.TotalListeningSessions,
				BooksListened: ys.NumBooksListened,
				BooksFinished: ys.NumBooksFinished,
			}
			for _, a := range ys.TopAuthors {
				out.TopAuthors = append(out.TopAuthors, nameTime{Name: a.Name, Time: a.Time})
			}
			for _, g := range ys.TopGenres {
				out.TopGenres = append(out.TopGenres, nameTime{Name: g.Genre, Time: g.Time})
			}
			return nil, out, nil
		}

		var st api.ListeningStats
		if self {
			st, err = record(gen.MeGetListeningStats(ctx))
		} else {
			st, err = record(gen.UserGetListeningStats(ctx, u.Id))
		}
		if err != nil {
			return nil, statsOut{}, fmt.Errorf("reading the listening stats: %w", err)
		}
		out := statsOut{User: u.Username, Total: wholeSec(st.TotalTime), Today: wholeSec(st.Today), Items: len(st.Items)}
		var week, month float64
		now := time.Now()
		for day, secs := range st.Days {
			d, err := time.Parse("2006-01-02", day)
			if err != nil {
				return nil, statsOut{}, fmt.Errorf("the server's listening stats name a day %q that is not a date, so the totals by week and month cannot be counted: %w", day, err)
			}
			if age := now.Sub(d); age <= 7*24*time.Hour {
				week += secs
			}
			if age := now.Sub(d); age <= 30*24*time.Hour {
				month += secs
			}
		}
		out.Last7Days, out.Last30Days = wholeSec(week), wholeSec(month)

		items := slices.Collect(maps.Values(st.Items))
		slices.SortFunc(items, func(a, b api.ListeningStatsItem) int { return cmp.Compare(b.TimeListening, a.TimeListening) })
		for _, it := range items[:min(len(items), 10)] {
			// an item the server sends no metadata for has no title to give
			meta := pointer.From(it.MediaMetadata)
			out.TopItems = append(out.TopItems, itemTime{ID: string(it.Id), Title: meta.Title, Author: meta.AuthorDisplay(), Time: wholeSec(it.TimeListening)})
		}

		return nil, out, nil
	})
}

// bookmarkAt is the bookmark at a position in an item, or nil. The server
// knows a bookmark by the two, and compares the time exactly.
func bookmarkAt(bms []api.AudioBookmark, itemID api.LibraryItemID, seconds float64) *api.AudioBookmark {
	for i := range bms {
		if bms[i].LibraryItemId == itemID && bms[i].Time == seconds {
			return &bms[i]
		}
	}
	return nil
}

// ownBookmarks are the API key user's bookmarks, in every item.
func ownBookmarks(ctx context.Context, gen *api.Client) ([]api.AudioBookmark, error) {
	all, err := record(gen.MeGetAllBookmarks(ctx))
	if err != nil {
		return nil, err
	}

	return all.Bookmarks, nil
}

// ownProgress is the API key user's progress in a book, or in an episode when one is named; nil when they have never started it.
func ownProgress(ctx context.Context, gen *api.Client, item api.LibraryItemID, episode string) (*api.MediaProgress, error) {
	var p api.MediaProgress
	var err error
	if episode == "" {
		p, err = record(gen.MeGetMediaProgress2(ctx, item))
	} else {
		p, err = record(gen.MeGetMediaProgress(ctx, item, api.EpisodeID(episode)))
	}
	if api.IsNotFound(err) {
		return nil, nil //nolint:nilnil // no progress record is a normal state
	}
	if err != nil {
		return nil, err
	}

	return &p, nil
}

// sessionPage is one page of an account's listening sessions, newest first, and how many it has in all: the API key's own through its own
// route, which another's cannot be read by.
func sessionPage(ctx context.Context, gen *api.Client, userID api.UserID, self bool, perPage, page int) ([]api.PlaybackSession, int, error) {
	if self {
		got, err := record(gen.MeGetListeningSessions(ctx, api.MeGetListeningSessionsOptions{ItemsPerPage: &perPage, Page: &page}))
		return got.Sessions, got.Total, err
	}

	got, err := record(gen.UserGetListeningSessions(ctx, userID, api.UserGetListeningSessionsOptions{ItemsPerPage: &perPage, Page: &page}))
	if err != nil {
		return nil, 0, err
	}
	sessions := make([]api.PlaybackSession, 0, len(got.Sessions))
	for _, s := range got.Sessions {
		sessions = append(sessions, s.Session())
	}

	return sessions, got.Total, nil
}

// sessionsOf finds sessions by id in one account's history, reading it a
// page at a time, newest first, until each is found or the history ends.
func sessionsOf(ctx context.Context, gen *api.Client, userID api.UserID, self bool, ids []string) (map[string]api.PlaybackSession, error) {
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	found := map[string]api.PlaybackSession{}
	for page := 0; len(found) < len(want); page++ {
		sessions, total, err := sessionPage(ctx, gen, userID, self, historyPageSize, page)
		if err != nil {
			return nil, err
		}
		for _, s := range sessions {
			if want[s.Id] {
				found[s.Id] = s
			}
		}
		if len(sessions) == 0 || (page+1)*historyPageSize >= total {
			break
		}
	}
	return found, nil
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
func itemSessionsOf(ctx context.Context, gen *api.Client, userID api.UserID, itemID api.LibraryItemID, limit int, cutoff time.Time) (found []api.PlaybackSession, count int, searched bool, err error) {
	for page := range historyPages {
		sessions, total, err := sessionPage(ctx, gen, userID, false, historyPageSize, page)
		if err != nil {
			return nil, 0, false, err
		}
		for i := range sessions {
			if sessions[i].LibraryItemId != itemID {
				continue
			}
			count++
			// newest first, so once one is past the cutoff the rest are too
			if len(found) < limit && !sessions[i].UpdatedAt.Before(cutoff) {
				found = append(found, sessions[i])
			}
		}
		if len(sessions) < historyPageSize || (page+1)*historyPageSize >= total {
			return found, count, true, nil
		}
	}

	return found, count, false, nil
}
