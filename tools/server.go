package tools

import (
	"context"

	"github.com/katbyte/abs-mcp/lib/abs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerServerTools(r *registry) {
	client := r.client

	type infoOut struct {
		Version          string       `json:"version"`
		URL              string       `json:"url"`
		User             string       `json:"user"                        jsonschema:"the account the API key acts as"`
		UserType         string       `json:"user_type"                   jsonschema:"root, admin, user or guest; admin-only tools need root/admin"`
		CanUpdate        bool         `json:"can_update"`
		CanDelete        bool         `json:"can_delete"`
		Libraries        []libraryRow `json:"libraries"`
		BookProviders    []string     `json:"book_providers,omitempty"    jsonschema:"metadata providers accepted by item_match"`
		PodcastProviders []string     `json:"podcast_providers,omitempty"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "server_info",
		Description: "Check connectivity: server version, the user the API key acts as and their permissions, the libraries, and the metadata providers available for matching. Call this first when unsure what the key can do.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, infoOut, error) {
		status, err := client.Status(ctx)
		if err != nil {
			return nil, infoOut{}, err
		}
		me, err := client.Me(ctx)
		if err != nil {
			return nil, infoOut{}, err
		}
		libs, err := client.Libraries(ctx)
		if err != nil {
			return nil, infoOut{}, err
		}

		out := infoOut{
			Version:   status.ServerVersion,
			URL:       client.BaseURL(),
			User:      me.Username,
			UserType:  me.Type,
			CanUpdate: me.IsAdmin() || me.Permissions.Update,
			CanDelete: me.IsAdmin() || me.Permissions.Delete,
		}
		for i := range libs {
			out.Libraries = append(out.Libraries, libraryRowOf(&libs[i]))
		}
		// providers are informational; a failure here should not hide the rest
		if book, podcast, err := client.Providers(ctx); err == nil {
			out.BookProviders, out.PodcastProviders = book, podcast
		}

		return nil, out, nil
	})

	type statsOut struct {
		Books          int   `json:"books"`
		Podcasts       int   `json:"podcasts"`
		AudioFiles     int   `json:"audio_files"`
		TotalSizeGB    int64 `json:"total_size_gb"`
		BooksSizeGB    int64 `json:"books_size_gb"`
		PodcastsSizeGB int64 `json:"podcasts_size_gb"`
		Users          int   `json:"users,omitempty"`
		OpenSessions   int   `json:"open_sessions"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "server_stats",
		Description: "Server-wide totals: item counts, audio file counts, disk usage, user count and open playback sessions. Admin only.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, statsOut, error) {
		s, err := client.ServerStats(ctx)
		if err != nil {
			return nil, statsOut{}, err
		}
		out := statsOut{
			Books:          s.Books.NumItems,
			Podcasts:       s.Podcasts.NumItems,
			AudioFiles:     s.Total.NumAudioFiles,
			TotalSizeGB:    s.Total.TotalSize >> 30,
			BooksSizeGB:    s.Books.TotalSize >> 30,
			PodcastsSizeGB: s.Podcasts.TotalSize >> 30,
		}
		if users, err := client.Users(ctx, false); err == nil {
			out.Users = len(users)
		}
		if sessions, err := client.OpenSessions(ctx); err == nil {
			out.OpenSessions = len(sessions)
		}

		return nil, out, nil
	})

	type taskRow struct {
		ID          string `json:"id"`
		Action      string `json:"action"`
		Title       string `json:"title"`
		Description string `json:"description,omitempty"`
		Status      string `json:"status"                jsonschema:"running, finished or failed"`
		Error       string `json:"error,omitempty"`
		Started     string `json:"started,omitempty"`
		Finished    string `json:"finished,omitempty"`
	}
	type tasksOut struct {
		Tasks []taskRow `json:"tasks" jsonschema:"library scans, matches, metadata embeds and encodes, running and recently finished"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "server_tasks",
		Description: "Running and recently finished background tasks: library scans, match-all runs, metadata embeds, m4b encodes. Use it to see when a scan started by library_scan has finished.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, tasksOut, error) {
		tasks, err := client.Tasks(ctx)
		if err != nil {
			return nil, tasksOut{}, err
		}
		out := tasksOut{Tasks: []taskRow{}}
		for _, t := range tasks {
			status := "running"
			switch {
			case t.IsFailed:
				status = "failed"
			case t.IsFinished:
				status = "finished"
			}
			out.Tasks = append(out.Tasks, taskRow{
				ID:          t.ID,
				Action:      t.Action,
				Title:       t.Title,
				Description: t.Description,
				Status:      status,
				Error:       t.Error,
				Started:     fmtTime(t.StartedAt),
				Finished:    fmtTime(t.FinishedAt),
			})
		}

		return nil, out, nil
	})

	type sessionsOut struct {
		Sessions []sessionSummary `json:"sessions" jsonschema:"playback sessions open right now, across all users"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "server_sessions",
		Description: "What is playing right now: open playback sessions across all users with title, position and device. Admin only. For history use user_history or me_history.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, sessionsOut, error) {
		sessions, err := client.OpenSessions(ctx)
		if err != nil {
			return nil, sessionsOut{}, err
		}
		out := sessionsOut{Sessions: []sessionSummary{}}
		for i := range sessions {
			out.Sessions = append(out.Sessions, summariseSession(&sessions[i]))
		}

		return nil, out, nil
	})

	type backupRow struct {
		ID       string `json:"id"`
		Filename string `json:"filename"`
		SizeMB   int64  `json:"size_mb"`
		Created  string `json:"created"`
		Version  string `json:"server_version,omitempty"`
	}
	type backupsOut struct {
		Location string      `json:"location,omitempty"`
		Backups  []backupRow `json:"backups"`
	}
	backupRows := func(bs []abs.Backup) []backupRow {
		rows := make([]backupRow, 0, len(bs))
		for _, b := range bs {
			rows = append(rows, backupRow{ID: b.ID, Filename: b.Filename, SizeMB: mb(b.FileSize), Created: fmtTime(b.CreatedAt), Version: b.ServerVersion})
		}
		return rows
	}
	add(r, readTool, &mcp.Tool{
		Name:        "server_backups",
		Description: "List server backups (database + metadata) and where they are stored. Admin only.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, backupsOut, error) {
		bs, loc, err := client.Backups(ctx)
		if err != nil {
			return nil, backupsOut{}, err
		}

		return nil, backupsOut{Location: loc, Backups: backupRows(bs)}, nil
	})

	add(r, writeTool, &mcp.Tool{
		Name:        "server_backup_create",
		Description: "Run a server backup now and return the backup list. Admin only. Changes server state (writes a backup file).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, backupsOut, error) {
		bs, err := client.CreateBackup(ctx)
		if err != nil {
			return nil, backupsOut{}, err
		}

		return nil, backupsOut{Backups: backupRows(bs)}, nil
	})

	type renameIn struct {
		Kind string `json:"kind" jsonschema:"tag or genre"`
		From string `json:"from" jsonschema:"current name"`
		To   string `json:"to"   jsonschema:"new name; merges into it if it already exists"`
	}
	type renameOut struct {
		ItemsUpdated int `json:"items_updated"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "server_rename_tag",
		Description: "Rename a tag or genre everywhere it is used, across all libraries (e.g. to merge 'Sci-Fi' into 'Science Fiction'). Admin only. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in renameIn) (*mcp.CallToolResult, renameOut, error) {
		var n int
		var err error
		switch in.Kind {
		case "genre", "genres":
			n, err = client.RenameGenre(ctx, in.From, in.To)
		default:
			n, err = client.RenameTag(ctx, in.From, in.To)
		}
		if err != nil {
			return nil, renameOut{}, err
		}

		return nil, renameOut{ItemsUpdated: n}, nil
	})
}
