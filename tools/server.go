package tools

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/katbyte/abs-mcp/lib/abs"
	"github.com/katbyte/go-kt/version"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerServerTools(r *registry) {
	client := r.client

	// totals are a nested object rather than flat fields so their absence is a
	// single unambiguous fact. Flat and omitempty could not tell "this key may
	// not ask" from the perfectly ordinary answers of no podcasts and nobody
	// listening.
	type totalsOut struct {
		Books        int   `json:"books"`
		Podcasts     int   `json:"podcasts"`
		AudioFiles   int   `json:"audio_files"`
		TotalSize    int64 `json:"total_size"    jsonschema:"bytes on disk"`
		BooksSize    int64 `json:"books_size"    jsonschema:"bytes on disk"`
		PodcastsSize int64 `json:"podcasts_size" jsonschema:"bytes on disk"`
		Users        int   `json:"users"`
		OpenSessions int   `json:"open_sessions" jsonschema:"how many are playing right now; server_sessions lists them"`
	}
	type infoOut struct {
		Version          string       `json:"version"`
		AbsMCPVersion    string       `json:"abs_mcp_version"             jsonschema:"the build of this MCP server answering, e.g. v0.4.0+12@g3592143: the tag, the commits since it, and the commit"`
		URL              string       `json:"url"`
		User             string       `json:"user"                        jsonschema:"the account the API key acts as"`
		UserType         string       `json:"user_type"                   jsonschema:"root, admin, user or guest; admin-only tools need root/admin"`
		CanUpdate        bool         `json:"can_update"`
		CanDelete        bool         `json:"can_delete"`
		Libraries        []libraryRow `json:"libraries"`
		BookProviders    []string     `json:"book_providers,omitempty"    jsonschema:"metadata providers accepted by item_match"`
		PodcastProviders []string     `json:"podcast_providers,omitempty"`
		Totals           *totalsOut   `json:"totals,omitempty"            jsonschema:"server-wide totals; present only for an admin key, so absent means unknown rather than zero - see note"`
		Note             string       `json:"note,omitempty"              jsonschema:"what this key could not see, when part of the answer needed permissions it does not have"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "server_info",
		Description: "Check connectivity and see the whole server at once: its version and this MCP server's, the user the API key acts as and their permissions, the libraries, the metadata providers available for matching, and - for an admin key - server-wide item counts, disk usage, user count and how many people are listening. Call this first when unsure what the key can do. server_sessions lists who is playing what.",
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
			Version: status.ServerVersion,
			// both change what a caller can expect: a server ahead of the
			// client may answer with things it does not read
			AbsMCPVersion: version.Version,
			URL:           client.BaseURL(),
			User:          me.Username,
			UserType:      me.Type,
			CanUpdate:     me.IsAdmin() || me.Permissions.Update,
			CanDelete:     me.IsAdmin() || me.Permissions.Delete,
			Libraries:     []libraryRow{}, // none is an answer, not null
		}
		for i := range libs {
			out.Libraries = append(out.Libraries, libraryRowOf(&libs[i]))
		}
		// providers are informational; a failure here should not hide the rest
		if book, podcast, err := client.Providers(ctx); err == nil {
			out.BookProviders, out.PodcastProviders = book, podcast
		}
		// the totals are admin-only, and this is the one tool that has to answer
		// for any key, so they are best-effort - but a key that may not ask must
		// be told that, or it reads the silence as an empty server
		if st, err := client.ServerStats(ctx); err == nil {
			t := totalsOut{
				Books:        st.Books.NumItems,
				Podcasts:     st.Podcasts.NumItems,
				AudioFiles:   st.Total.NumAudioFiles,
				TotalSize:    st.Total.TotalSize,
				BooksSize:    st.Books.TotalSize,
				PodcastsSize: st.Podcasts.TotalSize,
			}
			if users, err := client.Users(ctx, false); err == nil {
				t.Users = len(users)
			}
			if sessions, err := client.OpenSessions(ctx); err == nil {
				t.OpenSessions = len(sessions)
			}
			out.Totals = &t
		} else {
			out.Note = fmt.Sprintf("server-wide totals need an admin key and are not included: this key acts as %s (%s). "+
				"The libraries and permissions above are complete. Treat the totals as unknown rather than zero, "+
				"and expect the other admin-only tools (server_sessions, server_tasks, server_backups, server_tags, user_list) to be refused as well.",
				me.Username, me.Type)
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
		Description: "What is playing right now: open playback sessions across all users with title, position and device. Admin only. For history use user_history.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, sessionsOut, error) {
		sessions, err := client.OpenSessions(ctx)
		if abs.IsNotFound(err) {
			return nil, sessionsOut{}, fmt.Errorf("open sessions are admin only: Audiobookshelf answers anyone else as if the route did not exist (%w)", err)
		}
		if err != nil {
			return nil, sessionsOut{}, err
		}
		out := sessionsOut{Sessions: []sessionSummary{}}
		var names map[string]string
		for i := range sessions {
			row := summarizeSession(&sessions[i])
			// an open session names its user by id only
			if row.User == "" && row.UserID != "" {
				if names == nil {
					names = map[string]string{}
					if users, uerr := client.Users(ctx, false); uerr == nil {
						for j := range users {
							names[users[j].ID] = users[j].Username
						}
					}
				}
				row.User = names[row.UserID]
			}
			out.Sessions = append(out.Sessions, row)
		}

		return nil, out, nil
	})

	type backupRow struct {
		ID       string `json:"id"`
		Filename string `json:"filename"`
		Size     int64  `json:"size"                     jsonschema:"bytes"`
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
			rows = append(rows, backupRow{ID: b.ID, Filename: b.Filename, Size: b.FileSize, Created: fmtTime(b.CreatedAt), Version: b.ServerVersion})
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

	type backupCreateOut struct {
		Created  backupRow   `json:"created"            jsonschema:"the backup this call made"`
		Replaced bool        `json:"replaced,omitempty" jsonschema:"the server names a backup by the minute it was made, so this one took the place of one made earlier in the same minute"`
		Pruned   []string    `json:"pruned,omitempty"   jsonschema:"ids of older backups the server deleted to stay within the number it keeps"`
		Backups  []backupRow `json:"backups"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "server_backup_create",
		Description: "Run a server backup now and say which it made, then list the backups. The server names a backup by the minute it was made, so a second one in the same minute replaces the first, and it deletes the oldest past the number it keeps: replaced and pruned say when either happened. Admin only. Changes server state (writes a backup file).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, backupCreateOut, error) {
		before, _, err := client.Backups(ctx)
		if err != nil {
			return nil, backupCreateOut{}, err
		}
		after, err := client.CreateBackup(ctx)
		if err != nil {
			return nil, backupCreateOut{}, err
		}
		if len(after) == 0 {
			return nil, backupCreateOut{}, errors.New("the server accepted the backup but lists none afterwards")
		}
		// the one made is the newest; its id can be one listed before, when
		// it replaced a backup made earlier in the same minute
		made := slices.MaxFunc(after, func(a, b abs.Backup) int { return cmp.Compare(a.CreatedAt, b.CreatedAt) })
		out := backupCreateOut{Created: backupRows([]abs.Backup{made})[0], Backups: backupRows(after)}
		kept := map[string]bool{}
		for _, b := range after {
			kept[b.ID] = true
		}
		for _, b := range before {
			switch {
			case b.ID == made.ID:
				out.Replaced = true
			case !kept[b.ID]:
				out.Pruned = append(out.Pruned, b.ID)
			}
		}

		return nil, out, nil
	})

	type tagsIn struct {
		Kind string `json:"kind,omitempty" jsonschema:"tags, genres, or omit for both"`
	}
	type tagsOut struct {
		Tags   []string `json:"tags,omitempty"`
		Genres []string `json:"genres,omitempty"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "server_tags",
		Description: "Every tag and genre in use across all libraries: the server-wide vocabulary to normalize against before merging with metadata_rename. Admin only. library_filters is the per-library equivalent.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in tagsIn) (*mcp.CallToolResult, tagsOut, error) {
		// "Tags" once fell through both tests and answered genres as well
		var wantTags, wantGenres bool
		switch strings.ToLower(strings.TrimSpace(in.Kind)) {
		case "":
			wantTags, wantGenres = true, true
		case "tag", "tags":
			wantTags = true
		case "genre", "genres":
			wantGenres = true
		default:
			return nil, tagsOut{}, fmt.Errorf("unknown kind %q; choose tags or genres, or omit it for both", in.Kind)
		}
		var out tagsOut
		var err error
		if wantTags {
			if out.Tags, err = client.Tags(ctx); err != nil {
				return nil, tagsOut{}, err
			}
		}
		if wantGenres {
			if out.Genres, err = client.Genres(ctx); err != nil {
				return nil, tagsOut{}, err
			}
		}

		return nil, out, nil
	})
}
