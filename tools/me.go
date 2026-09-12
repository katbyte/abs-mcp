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

// progressRef names a book, or an episode of a podcast, for progress tools.
type progressRef struct {
	itemRef
	Episode string `json:"episode,omitempty" jsonschema:"episode id for podcasts; omit for books"`
}

func registerMeTools(r *registry) {
	client := r.client

	type meOut struct {
		ID          string   `json:"id"`
		Username    string   `json:"username"`
		Type        string   `json:"type"`
		Libraries   []string `json:"libraries"           jsonschema:"library ids accessible; all when empty"`
		CanUpdate   bool     `json:"can_update"`
		CanDelete   bool     `json:"can_delete"`
		CanDownload bool     `json:"can_download"`
		CanUpload   bool     `json:"can_upload"`
		InProgress  int      `json:"items_in_progress"`
		Finished    int      `json:"items_finished"`
		Bookmarks   int      `json:"bookmarks"`
		LastSeen    string   `json:"last_seen,omitempty"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "me_get",
		Description: "The user the API key acts as: account type, permissions, and how many items they have in progress or finished.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, meOut, error) {
		me, err := client.Me(ctx)
		if err != nil {
			return nil, meOut{}, err
		}
		out := meOut{
			ID:          me.ID,
			Username:    me.Username,
			Type:        me.Type,
			Libraries:   me.LibrariesAccessible,
			CanUpdate:   me.IsAdmin() || me.Permissions.Update,
			CanDelete:   me.IsAdmin() || me.Permissions.Delete,
			CanDownload: me.IsAdmin() || me.Permissions.Download,
			CanUpload:   me.IsAdmin() || me.Permissions.Upload,
			Bookmarks:   len(me.Bookmarks),
			LastSeen:    fmtTime(me.LastSeen),
		}
		for _, p := range me.MediaProgress {
			switch {
			case p.IsFinished:
				out.Finished++
			case p.CurrentTime > 0 || p.EbookProgress > 0:
				out.InProgress++
			}
		}

		return nil, out, nil
	})

	type inProgressIn struct {
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
		Name:        "me_in_progress",
		Description: "What the API key's user is currently listening to (the continue-listening shelf), most recent first, with position and percent.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in inProgressIn) (*mcp.CallToolResult, inProgressOut, error) {
		items, err := client.ItemsInProgress(ctx, limitOr(in.Limit, defaultLimit))
		if err != nil {
			return nil, inProgressOut{}, err
		}
		out := inProgressOut{Items: []inProgressRow{}}
		for i := range items {
			it := &items[i]
			row := inProgressRow{itemSummary: summarize(it)}
			if it.RecentEpisode != nil {
				ep := summarizeEpisode(it.RecentEpisode, it.Title(), false)
				ep.Progress = row.Progress
				row.Episode = &ep
				row.Progress = nil
			}
			out.Items = append(out.Items, row)
		}

		return nil, out, nil
	})

	type progressGetOut struct {
		Item     string           `json:"item"`
		Episode  string           `json:"episode,omitempty"`
		Duration string           `json:"duration,omitempty"`
		Progress *progressSummary `json:"progress"           jsonschema:"null when the user has never started it"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "me_progress_get",
		Description: "The API key user's listening progress on one book or podcast episode: percent, position, finished flag.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in progressRef) (*mcp.CallToolResult, progressGetOut, error) {
		it, err := resolveItem(ctx, client, in.Library, in.Item)
		if err != nil {
			return nil, progressGetOut{}, err
		}
		out := progressGetOut{Item: it.Title(), Duration: fmtDuration(it.Media.Duration)}
		if in.Episode != "" {
			for _, e := range it.Media.Episodes {
				if e.ID == in.Episode {
					out.Episode = e.Title
					out.Duration = fmtDuration(e.DurationSeconds())
				}
			}
			if out.Episode == "" {
				return nil, progressGetOut{}, fmt.Errorf("no episode %s in %q", in.Episode, it.Title())
			}
		}
		p, err := client.Progress(ctx, it.ID, in.Episode)
		if err != nil {
			return nil, progressGetOut{}, err
		}
		out.Progress = progressOf(p)

		return nil, out, nil
	})

	type progressSetIn struct {
		progressRef
		Finished *bool    `json:"finished,omitempty"           jsonschema:"mark finished (true) or not finished (false)"`
		Position *float64 `json:"position_seconds,omitempty"   jsonschema:"set the playback position in seconds"`
		Percent  *float64 `json:"percent,omitempty"            jsonschema:"set the position as a percentage 0-100 instead"`
		Hide     *bool    `json:"hide_from_continue,omitempty" jsonschema:"remove from (true) or restore to (false) the continue-listening shelf"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "me_progress_set",
		Description: "Update the API key user's progress on a book or episode: mark finished/unfinished, set the position (seconds or percent), or hide it from continue-listening. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in progressSetIn) (*mcp.CallToolResult, progressGetOut, error) {
		it, err := resolveItem(ctx, client, in.Library, in.Item)
		if err != nil {
			return nil, progressGetOut{}, err
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
			return nil, progressGetOut{}, errors.New("nothing to change: pass finished, position_seconds, percent or hide_from_continue")
		}
		if err := client.SetProgress(ctx, it.ID, in.Episode, upd); err != nil {
			return nil, progressGetOut{}, err
		}

		p, err := client.Progress(ctx, it.ID, in.Episode)
		if err != nil {
			return nil, progressGetOut{}, err
		}

		return nil, progressGetOut{Item: it.Title(), Duration: fmtDuration(duration), Progress: progressOf(p)}, nil
	})

	type doneOut struct {
		Done bool `json:"done"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "me_progress_remove",
		Description: "Delete the API key user's progress on a book or episode, resetting it to never started. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in progressRef) (*mcp.CallToolResult, doneOut, error) {
		it, err := resolveItem(ctx, client, in.Library, in.Item)
		if err != nil {
			return nil, doneOut{}, err
		}
		p, err := client.Progress(ctx, it.ID, in.Episode)
		if err != nil {
			return nil, doneOut{}, err
		}
		if p == nil {
			return nil, doneOut{Done: false}, nil
		}
		if err := client.RemoveProgress(ctx, p.ID); err != nil {
			return nil, doneOut{}, err
		}

		return nil, doneOut{Done: true}, nil
	})

	type bookmarkRow struct {
		ItemID  string  `json:"item_id"`
		Item    string  `json:"item,omitempty"`
		Title   string  `json:"title"`
		Time    string  `json:"time"`
		Seconds float64 `json:"seconds"           jsonschema:"pass to me_bookmark_remove"`
		Created string  `json:"created,omitempty"`
	}
	type bookmarksIn struct {
		Item    string `json:"item,omitempty"    jsonschema:"restrict to one item by id or title"`
		Library string `json:"library,omitempty"`
	}
	type bookmarksOut struct {
		Bookmarks []bookmarkRow `json:"bookmarks"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "me_bookmarks",
		Description: "The API key user's bookmarks (named positions in books), optionally for one item.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in bookmarksIn) (*mcp.CallToolResult, bookmarksOut, error) {
		bms, err := client.Bookmarks(ctx)
		if err != nil {
			return nil, bookmarksOut{}, err
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
		if len(ids) > 0 {
			if items, err := client.ItemsBatch(ctx, ids); err == nil {
				for i := range items {
					titles[items[i].ID] = items[i].Title()
				}
			}
		}

		out := bookmarksOut{Bookmarks: []bookmarkRow{}}
		for _, b := range bms {
			if only != "" && b.LibraryItemID != only {
				continue
			}
			out.Bookmarks = append(out.Bookmarks, bookmarkRow{
				ItemID:  b.LibraryItemID,
				Item:    titles[b.LibraryItemID],
				Title:   b.Title,
				Time:    fmtDuration(b.Time),
				Seconds: b.Time,
				Created: fmtTime(b.CreatedAt),
			})
		}
		slices.SortStableFunc(out.Bookmarks, func(a, b bookmarkRow) int {
			if a.ItemID != b.ItemID {
				return cmp.Compare(a.Item, b.Item)
			}
			return cmp.Compare(a.Seconds, b.Seconds)
		})

		return nil, out, nil
	})

	type bookmarkAddIn struct {
		itemRef
		Seconds float64 `json:"seconds" jsonschema:"position in seconds"`
		Title   string  `json:"title"   jsonschema:"bookmark name"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "me_bookmark_add",
		Description: "Add a named bookmark at a position in a book (or rename the one already at that position). Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in bookmarkAddIn) (*mcp.CallToolResult, bookmarkRow, error) {
		it, err := resolveItem(ctx, client, in.Library, in.Item)
		if err != nil {
			return nil, bookmarkRow{}, err
		}
		if strings.TrimSpace(in.Title) == "" {
			return nil, bookmarkRow{}, errors.New("title is required")
		}
		b, err := client.CreateBookmark(ctx, it.ID, in.Seconds, in.Title)
		if err != nil {
			// an existing bookmark at this time is updated instead
			if b, err = client.UpdateBookmark(ctx, it.ID, in.Seconds, in.Title); err != nil {
				return nil, bookmarkRow{}, err
			}
		}

		return nil, bookmarkRow{ItemID: it.ID, Item: it.Title(), Title: b.Title, Time: fmtDuration(b.Time), Seconds: b.Time, Created: fmtTime(b.CreatedAt)}, nil
	})

	type bookmarkRemoveIn struct {
		itemRef
		Seconds float64 `json:"seconds" jsonschema:"the bookmark's position from me_bookmarks"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "me_bookmark_remove",
		Description: "Remove the bookmark at a position in a book. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in bookmarkRemoveIn) (*mcp.CallToolResult, doneOut, error) {
		it, err := resolveItem(ctx, client, in.Library, in.Item)
		if err != nil {
			return nil, doneOut{}, err
		}
		if err := client.DeleteBookmark(ctx, it.ID, in.Seconds); err != nil {
			return nil, doneOut{}, err
		}

		return nil, doneOut{Done: true}, nil
	})

	type historyIn struct {
		Item    string `json:"item,omitempty"    jsonschema:"restrict to one item by id or title"`
		Library string `json:"library,omitempty"`
		Days    int    `json:"days,omitempty"    jsonschema:"how many days back, default 60"`
		Limit   int    `json:"limit,omitempty"   jsonschema:"maximum sessions, default 25"`
	}
	type historyOut struct {
		Total    int              `json:"total_sessions" jsonschema:"all-time count"`
		Sessions []sessionSummary `json:"sessions"       jsonschema:"newest first"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "me_history",
		Description: "The API key user's listening sessions, newest first, default last 60 days: what was played, for how long, on which device.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in historyIn) (*mcp.CallToolResult, historyOut, error) {
		limit := limitOr(in.Limit, defaultLimit)
		var sessions []abs.Session
		var total int
		var err error
		if in.Item != "" {
			it, rerr := resolveItem(ctx, client, in.Library, in.Item)
			if rerr != nil {
				return nil, historyOut{}, rerr
			}
			sessions, total, err = client.ItemListeningSessions(ctx, it.ID, "", limit, 0)
		} else {
			sessions, total, err = client.ListeningSessions(ctx, limit, 0)
		}
		if err != nil {
			return nil, historyOut{}, err
		}

		cutoff := time.Now().AddDate(0, 0, -limitOr(in.Days, 60))
		out := historyOut{Total: total, Sessions: []sessionSummary{}}
		for i := range sessions {
			if abs.Millis(sessions[i].UpdatedAt).Before(cutoff) {
				continue
			}
			out.Sessions = append(out.Sessions, summarizeSession(&sessions[i]))
		}

		return nil, out, nil
	})

	type statsIn struct {
		Year int `json:"year,omitempty" jsonschema:"year-in-review for this year instead of all-time totals"`
	}
	type itemTime struct {
		ID     string `json:"id"`
		Title  string `json:"title"`
		Author string `json:"author,omitempty"`
		Time   string `json:"time"`
	}
	type nameTime struct {
		Name string `json:"name"`
		Time string `json:"time"`
	}
	type statsOut struct {
		Total         string     `json:"total_listened"`
		Today         string     `json:"today,omitempty"`
		Last7Days     string     `json:"last_7_days,omitempty"`
		Last30Days    string     `json:"last_30_days,omitempty"`
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
		Name:        "me_stats",
		Description: "The API key user's listening statistics: total time, recent days, most listened items; or with year, a year-in-review with books finished and top authors, narrators and genres.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in statsIn) (*mcp.CallToolResult, statsOut, error) {
		if in.Year > 0 {
			ys, err := client.YearStats(ctx, in.Year)
			if err != nil {
				return nil, statsOut{}, err
			}
			out := statsOut{
				Total:         fmtDuration(ys.TotalListeningTime),
				Sessions:      ys.TotalListeningSessions,
				BooksListened: ys.NumBooksListened,
				BooksFinished: ys.NumBooksFinished,
			}
			for _, a := range ys.TopAuthors {
				out.TopAuthors = append(out.TopAuthors, nameTime{Name: a.Name, Time: fmtDuration(a.Time)})
			}
			for _, n := range ys.TopNarrators {
				out.TopNarrators = append(out.TopNarrators, nameTime{Name: n.Name, Time: fmtDuration(n.Time)})
			}
			for _, g := range ys.TopGenres {
				out.TopGenres = append(out.TopGenres, nameTime{Name: g.Genre, Time: fmtDuration(g.Time)})
			}
			for _, b := range ys.BooksFinished {
				out.Finished = append(out.Finished, itemTime{ID: b.ID, Title: b.Title, Time: fmtDuration(b.Duration)})
			}
			return nil, out, nil
		}

		st, err := client.ListeningStats(ctx)
		if err != nil {
			return nil, statsOut{}, err
		}
		out := statsOut{Total: fmtDuration(st.TotalTime), Today: fmtDuration(st.Today)}
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
		out.Last7Days, out.Last30Days = fmtDuration(week), fmtDuration(month)

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
			out.TopItems = append(out.TopItems, itemTime{ID: it.ID, Title: meta.Title, Author: author, Time: fmtDuration(it.TimeListening)})
		}

		return nil, out, nil
	})
}
