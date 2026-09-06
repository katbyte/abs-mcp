package tools

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/katbyte/abs-mcp/lib/abs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// resolveUser finds a user by name or id (admin only).
func resolveUser(ctx context.Context, client *abs.Client, nameOrID string) (*abs.User, error) {
	nameOrID = strings.TrimSpace(nameOrID)
	if nameOrID == "" {
		return nil, errors.New("user name or id is required")
	}
	if looksLikeID(nameOrID) {
		return client.User(ctx, nameOrID)
	}
	users, err := client.Users(ctx, false)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(users))
	for i := range users {
		if strings.EqualFold(users[i].Username, nameOrID) {
			return client.User(ctx, users[i].ID)
		}
		names = append(names, users[i].Username)
	}

	return nil, fmt.Errorf("no user named %q (have: %s)", nameOrID, strings.Join(names, ", "))
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
		Description: "List the server's user accounts with type, last seen, and what they last listened to. Admin only.",
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

	type userIn struct {
		User string `json:"user" jsonschema:"username or id"`
	}
	type progressRow struct {
		ItemID    string `json:"item_id"`
		EpisodeID string `json:"episode_id,omitempty"`
		Percent   int    `json:"percent"`
		Finished  bool   `json:"finished"`
		Updated   string `json:"updated,omitempty"`
	}
	type getOut struct {
		userRow
		Email       string        `json:"email,omitempty"`
		CanUpdate   bool          `json:"can_update"`
		CanDelete   bool          `json:"can_delete"`
		CanDownload bool          `json:"can_download"`
		CanUpload   bool          `json:"can_upload"`
		Libraries   []string      `json:"libraries,omitempty" jsonschema:"library ids accessible; all when empty"`
		InProgress  int           `json:"items_in_progress"`
		Finished    int           `json:"items_finished"`
		Created     string        `json:"created,omitempty"`
		Recent      []progressRow `json:"recent_progress"     jsonschema:"their 10 most recently updated items"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "user_get",
		Description: "One user's account details, permissions, and a summary of their listening progress. Admin only.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in userIn) (*mcp.CallToolResult, getOut, error) {
		u, err := resolveUser(ctx, client, in.User)
		if err != nil {
			return nil, getOut{}, err
		}
		row := userRow{ID: u.ID, Username: u.Username, Type: u.Type, Active: u.IsActive, LastSeen: fmtTime(u.LastSeen)}
		out := getOut{
			userRow:     row,
			Email:       u.Email,
			CanUpdate:   u.IsAdmin() || u.Permissions.Update,
			CanDelete:   u.IsAdmin() || u.Permissions.Delete,
			CanDownload: u.IsAdmin() || u.Permissions.Download,
			CanUpload:   u.IsAdmin() || u.Permissions.Upload,
			Libraries:   u.LibrariesAccessible,
			Created:     fmtDate(u.CreatedAt),
			Recent:      []progressRow{},
		}
		progress := u.MediaProgress
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

	type historyIn struct {
		User  string `json:"user"            jsonschema:"username or id"`
		Days  int    `json:"days,omitempty"  jsonschema:"how many days back, default 60"`
		Limit int    `json:"limit,omitempty" jsonschema:"maximum sessions, default 25"`
	}
	type historyOut struct {
		User     string           `json:"user"`
		Total    int              `json:"total_sessions" jsonschema:"all-time count"`
		Sessions []sessionSummary `json:"sessions"       jsonschema:"newest first"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "user_history",
		Description: "A user's listening sessions, newest first, default last 60 days. Admin only. For the API key's own user use me_history.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in historyIn) (*mcp.CallToolResult, historyOut, error) {
		u, err := resolveUser(ctx, client, in.User)
		if err != nil {
			return nil, historyOut{}, err
		}
		sessions, total, err := client.UserSessions(ctx, u.ID, limitOr(in.Limit, defaultLimit), 0)
		if err != nil {
			return nil, historyOut{}, err
		}
		cutoff := time.Now().AddDate(0, 0, -limitOr(in.Days, 60))
		out := historyOut{User: u.Username, Total: total, Sessions: []sessionSummary{}}
		for i := range sessions {
			if abs.Millis(sessions[i].UpdatedAt).Before(cutoff) {
				continue
			}
			out.Sessions = append(out.Sessions, summariseSession(&sessions[i]))
		}

		return nil, out, nil
	})

	type statsOut struct {
		User   string `json:"user"`
		Total  string `json:"total_listened"`
		Today  string `json:"today,omitempty"`
		Last7  string `json:"last_7_days,omitempty"`
		Last30 string `json:"last_30_days,omitempty"`
		Items  int    `json:"items_listened"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "user_stats",
		Description: "A user's listening totals: all-time, today, last 7 and 30 days. Admin only. For the API key's own user use me_stats.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in userIn) (*mcp.CallToolResult, statsOut, error) {
		u, err := resolveUser(ctx, client, in.User)
		if err != nil {
			return nil, statsOut{}, err
		}
		st, err := client.UserListeningStats(ctx, u.ID)
		if err != nil {
			return nil, statsOut{}, err
		}
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

		return nil, statsOut{User: u.Username, Total: fmtDuration(st.TotalTime), Today: fmtDuration(st.Today), Last7: fmtDuration(week), Last30: fmtDuration(month), Items: len(st.Items)}, nil
	})
}
