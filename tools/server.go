package tools

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	api "github.com/katbyte/abs-mcp/sdk/abs"
	"github.com/katbyte/go-kt/pointer"
	"github.com/katbyte/go-kt/version"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerServerTools(r *registry) {
	gen := r.gen

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
		status, err := record(gen.ServerGetStatus(ctx))
		if err != nil {
			return nil, infoOut{}, err
		}
		me, err := record(gen.MeGetCurrentUser(ctx))
		if err != nil {
			return nil, infoOut{}, err
		}
		libs, err := libraries(ctx, gen)
		if err != nil {
			return nil, infoOut{}, err
		}
		active, perms := pointer.From(me.IsActive), pointer.From(me.Permissions)

		out := infoOut{
			Version: status.ServerVersion,
			// both change what a caller can expect: a server ahead of the
			// client may answer with things it does not read
			AbsMCPVersion: version.Version,
			URL:           gen.Client.BaseURL,
			User:          me.Username,
			UserType:      me.Type,
			// the server grants each by the permission alone, an admin's too
			CanUpdate: active && pointer.From(perms.Update),
			CanDelete: active && pointer.From(perms.Delete),
			Libraries: []libraryRow{}, // none is an answer, not null
		}
		for i := range libs {
			out.Libraries = append(out.Libraries, libraryRowOf(&libs[i]))
		}
		providers, err := record(gen.SearchGetAllProviders(ctx))
		if err != nil {
			return nil, infoOut{}, fmt.Errorf("reading the server's metadata providers: %w", err)
		}
		if providers.Providers != nil {
			out.BookProviders, out.PodcastProviders = providerValues(providers.Providers.Books), providerValues(providers.Providers.Podcasts)
		}
		// the totals are admin-only, and this is the one tool that has to answer
		// for any key: a key the server refuses them is told so, as it would
		// read the silence as an empty server; any other failure is an error
		st, err := record(gen.StatsGetServerStats(ctx))
		switch {
		case err == nil:
			books := cmp.Or(st.Books, &api.StatsGetServerStatsAnswerBooks{})
			podcasts := cmp.Or(st.Podcasts, &api.StatsGetServerStatsAnswerPodcasts{})
			total := cmp.Or(st.Total, &api.StatsGetServerStatsAnswerTotal{})
			t := totalsOut{
				Books:        int(books.NumItems),
				Podcasts:     int(podcasts.NumItems),
				AudioFiles:   int(total.NumAudioFiles),
				TotalSize:    int64(total.TotalSize),
				BooksSize:    int64(books.TotalSize),
				PodcastsSize: int64(podcasts.TotalSize),
			}
			users, uerr := record(gen.UserFindAll(ctx))
			if uerr != nil {
				return nil, infoOut{}, fmt.Errorf("counting the server's users: %w", uerr)
			}
			sessions, serr := record(gen.SessionGetOpenSessions(ctx))
			if serr != nil {
				return nil, infoOut{}, fmt.Errorf("counting the open sessions: %w", serr)
			}
			t.Users, t.OpenSessions = len(users.Users), len(sessions.Sessions)
			out.Totals = &t
		case api.IsForbidden(err):
			out.Note = fmt.Sprintf("server-wide totals need an admin key and are not included: this key acts as %s (%s). "+
				"The libraries and permissions above are complete. Treat the totals as unknown rather than zero, "+
				"and expect the other admin-only tools (server_sessions, server_tasks, server_backups, server_tags, user_list) to be refused as well.",
				me.Username, me.Type)
		default:
			return nil, infoOut{}, fmt.Errorf("reading the server-wide totals: %w", err)
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
	type tasksIn struct {
		Log   bool   `json:"log,omitempty"   jsonschema:"also today's lines of the server's log, newest first: why a scan, match, embed or merge failed. Admin only"`
		Level string `json:"level,omitempty" jsonschema:"with log: the least severe line to include, debug, info, warn or error; default warn"`
		Match string `json:"match,omitempty" jsonschema:"with log: only lines containing this, in any case: a title, an item id, or a part of the server such as Scanner or AbMergeManager"`
		Limit int    `json:"limit,omitempty" jsonschema:"with log: the newest lines to return, default 50, at most 500"`
	}
	type logRow struct {
		Time    string `json:"time"             jsonschema:"the server's local time"`
		Level   string `json:"level"`
		Source  string `json:"source,omitempty" jsonschema:"the file and line in the server's code that wrote it"`
		Message string `json:"message"`
	}
	type tasksOut struct {
		Tasks      []taskRow `json:"tasks"                 jsonschema:"library scans, matches, metadata embeds and m4b merges running now"`
		Log        []logRow  `json:"log,omitempty"         jsonschema:"with log: today's lines, newest first"`
		LogMatched int       `json:"log_matched,omitempty" jsonschema:"with log: today's lines at the level and matching, before limit; the server serves today's log only, its last 5,000 lines"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "server_tasks",
		Description: "Background tasks running now: library scans, match-all runs, metadata embeds, m4b merges. Use it to see when a scan started by library_scan has finished: a task leaves the list the moment it ends, finished or failed alike, and with log the server's log for today says how it went.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in tasksIn) (*mcp.CallToolResult, tasksOut, error) {
		if !in.Log && (in.Level != "" || in.Match != "" || in.Limit != 0) {
			return nil, tasksOut{}, errors.New("level, match and limit narrow the log; pass log too")
		}
		least := logLevels["warn"]
		if in.Level != "" {
			var ok bool
			if least, ok = logLevels[strings.ToLower(in.Level)]; !ok {
				return nil, tasksOut{}, fmt.Errorf("unknown level %q: debug, info, warn or error", in.Level)
			}
		}
		if in.Limit < 0 || in.Limit > maxLogLines {
			return nil, tasksOut{}, fmt.Errorf("limit %d: 1 to %d", in.Limit, maxLogLines)
		}
		tasks, err := record(gen.MiscGetTasks(ctx))
		if err != nil {
			return nil, tasksOut{}, err
		}
		out := tasksOut{Tasks: []taskRow{}}
		if in.Log {
			logs, err := record(gen.MiscGetLoggerData(ctx))
			if err != nil {
				return nil, tasksOut{}, err
			}
			match := strings.ToLower(in.Match)
			for _, l := range slices.Backward(logs.CurrentDailyLogs) {
				if int(l.Level) < least || !strings.Contains(strings.ToLower(l.Message+" "+l.Source), match) {
					continue
				}
				out.LogMatched++
				if len(out.Log) < cmp.Or(in.Limit, defaultLogLines) {
					out.Log = append(out.Log, logRow{Time: l.Timestamp, Level: l.LevelName, Source: l.Source, Message: clip(l.Message, logMessageCap)})
				}
			}
		}
		for _, t := range tasks.Tasks {
			status := "running"
			switch {
			case t.IsFailed:
				status = "failed"
			case t.IsFinished:
				status = "finished"
			}
			out.Tasks = append(out.Tasks, taskRow{
				ID:          t.Id,
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
		open, err := record(gen.SessionGetOpenSessions(ctx))
		if api.IsNotFound(err) {
			return nil, sessionsOut{}, fmt.Errorf("open sessions are admin only: Audiobookshelf answers anyone else as if the route did not exist (%w)", err)
		}
		if err != nil {
			return nil, sessionsOut{}, err
		}
		out := sessionsOut{Sessions: []sessionSummary{}}
		var names map[string]string
		for i := range open.Sessions {
			row := summarizeSession(&open.Sessions[i])
			// an open session names its user by id only
			if row.User == "" && row.UserID != "" {
				if names == nil {
					names = map[string]string{}
					users, err := record(gen.UserFindAll(ctx))
					if err != nil {
						return nil, sessionsOut{}, fmt.Errorf("reading the users, to name each session's: %w", err)
					}
					for _, u := range users.Users {
						names[string(u.Id)] = u.Username
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
	backupRows := func(bs []api.Backup) []backupRow {
		rows := make([]backupRow, 0, len(bs))
		for _, b := range bs {
			rows = append(rows, backupRow{ID: b.Id, Filename: b.Filename, Size: int64(b.FileSize), Created: fmtTime(b.CreatedAt), Version: b.ServerVersion})
		}
		return rows
	}
	add(r, readTool, &mcp.Tool{
		Name:        "server_backups",
		Description: "List server backups (database + metadata) and where they are stored. Admin only.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, backupsOut, error) {
		all, err := record(gen.BackupGetAll(ctx))
		if err != nil {
			return nil, backupsOut{}, err
		}

		return nil, backupsOut{Location: all.BackupLocation, Backups: backupRows(all.Backups)}, nil
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
		listed, err := record(gen.BackupGetAll(ctx))
		if err != nil {
			return nil, backupCreateOut{}, err
		}
		made, err := record(gen.BackupCreate(ctx))
		if err != nil {
			return nil, backupCreateOut{}, err
		}
		before, after := listed.Backups, made.Backups
		if len(after) == 0 {
			return nil, backupCreateOut{}, errors.New("the server accepted the backup but lists none afterwards")
		}
		// the one made is the newest; its id can be one listed before, when
		// it replaced a backup made earlier in the same minute
		newest := slices.MaxFunc(after, func(a, b api.Backup) int { return a.CreatedAt.Compare(b.CreatedAt.Time) })
		out := backupCreateOut{Created: backupRows([]api.Backup{newest})[0], Backups: backupRows(after)}
		kept := map[string]bool{}
		for _, b := range after {
			kept[b.Id] = true
		}
		for _, b := range before {
			switch {
			case b.Id == newest.Id:
				out.Replaced = true
			case !kept[b.Id]:
				out.Pruned = append(out.Pruned, b.Id)
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
		if wantTags {
			tags, err := record(gen.MiscGetAllTags(ctx))
			if err != nil {
				return nil, tagsOut{}, err
			}
			out.Tags = tags.Tags
		}
		if wantGenres {
			genres, err := record(gen.MiscGetAllGenres(ctx))
			if err != nil {
				return nil, tagsOut{}, err
			}
			out.Genres = genres.Genres
		}

		return nil, out, nil
	})
}

// logLevels are the server's levels a caller can ask for the log from; NOTE,
// the server's own level for what it always says, and FATAL are above all.
var logLevels = map[string]int{"debug": 1, "info": 2, "warn": 3, "error": 4}

const (
	defaultLogLines = 50
	maxLogLines     = 500
	// logMessageCap is where a line is cut: the server logs whole objects
	logMessageCap = 1000
)

// providerValues are the providers' names as a match takes them.
func providerValues(opts []api.ProviderOption) []string {
	out := make([]string, 0, len(opts))
	for _, o := range opts {
		if o.Value != "" {
			out = append(out, o.Value)
		}
	}

	return out
}
