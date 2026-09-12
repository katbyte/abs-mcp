package abs

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/url"
	"strconv"
)

// Status returns the server's version and init state (no auth required).
func (c *Client) Status(ctx context.Context) (*Status, error) {
	var s Status
	if err := c.get(ctx, "/status", nil, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// Me returns the user the API key acts on behalf of, including their media
// progress and bookmarks.
func (c *Client) Me(ctx context.Context) (*User, error) {
	var u User
	if err := c.get(ctx, "/api/me", nil, &u); err != nil {
		return nil, err
	}
	return &u, nil
}

// ServerStats returns library size totals (admin only).
func (c *Client) ServerStats(ctx context.Context) (*ServerStats, error) {
	var s ServerStats
	if err := c.get(ctx, "/api/stats/server", nil, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// Tasks lists the task manager's current and recent tasks (scans, matches,
// embeds, encodes).
func (c *Client) Tasks(ctx context.Context) ([]Task, error) {
	var resp struct {
		Tasks []Task `json:"tasks"`
	}
	if err := c.get(ctx, "/api/tasks", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Tasks, nil
}

// Providers lists the metadata providers available for matching.
func (c *Client) Providers(ctx context.Context) (book, podcast []string, err error) {
	// {"providers":{"books":[{"value":"audible","text":"Audible.com"},...],
	//               "podcasts":[...]}} - the value is what the match endpoints take
	var resp struct {
		Providers struct {
			Books    []providerOption `json:"books"`
			Podcasts []providerOption `json:"podcasts"`
		} `json:"providers"`
	}
	if err := c.get(ctx, "/api/search/providers", nil, &resp); err != nil {
		return nil, nil, err
	}
	return providerValues(resp.Providers.Books), providerValues(resp.Providers.Podcasts), nil
}

type providerOption struct {
	Value string `json:"value"`
	Text  string `json:"text"`
}

func providerValues(opts []providerOption) []string {
	out := make([]string, 0, len(opts))
	for _, o := range opts {
		if o.Value != "" {
			out = append(out, o.Value)
		}
	}
	return out
}

// Users lists all user accounts (admin only). includeLatestSession adds each
// user's most recent playback session.
func (c *Client) Users(ctx context.Context, includeLatestSession bool) ([]User, error) {
	q := url.Values{}
	if includeLatestSession {
		q.Set("include", "latestSession")
	}
	var resp struct {
		Users []User `json:"users"`
	}
	if err := c.get(ctx, "/api/users", q, &resp); err != nil {
		return nil, err
	}
	return resp.Users, nil
}

// User fetches one user with their media progress (admin only).
func (c *Client) User(ctx context.Context, id string) (*User, error) {
	var u User
	if err := c.get(ctx, "/api/users/"+url.PathEscape(id), nil, &u); err != nil {
		return nil, err
	}
	return &u, nil
}

// UsersOnline returns the ids of users with an open socket plus the open
// playback sessions (admin only).
func (c *Client) UsersOnline(ctx context.Context) (usersOnline []User, openSessions []Session, err error) {
	var resp struct {
		UsersOnline  []User    `json:"usersOnline"`
		OpenSessions []Session `json:"openSessions"`
	}
	if err := c.get(ctx, "/api/users/online", nil, &resp); err != nil {
		return nil, nil, err
	}
	return resp.UsersOnline, resp.OpenSessions, nil
}

// SessionsOptions filters the admin playback-session listing.
type SessionsOptions struct {
	UserID       string
	Sort         string // displayTitle, duration, playMethod, startTime, currentTime, timeListening, updatedAt, createdAt
	Desc         bool
	ItemsPerPage int
	Page         int
}

// Sessions lists past playback sessions across all users (admin only),
// newest first by default.
func (c *Client) Sessions(ctx context.Context, opts SessionsOptions) ([]Session, int, error) {
	q := url.Values{}
	if opts.UserID != "" {
		q.Set("user", opts.UserID)
	}
	if opts.Sort != "" {
		q.Set("sort", opts.Sort)
	}
	q.Set("desc", boolQuery(opts.Desc))
	intQuery(q, "itemsPerPage", opts.ItemsPerPage)
	q.Set("page", strconv.Itoa(opts.Page))

	var resp struct {
		Total    int       `json:"total"`
		Sessions []Session `json:"sessions"`
	}
	if err := c.get(ctx, "/api/sessions", q, &resp); err != nil {
		return nil, 0, err
	}
	return resp.Sessions, resp.Total, nil
}

// OpenSessions lists playback sessions currently open on the server (admin
// only).
func (c *Client) OpenSessions(ctx context.Context) ([]Session, error) {
	var resp struct {
		Sessions []Session `json:"sessions"`
	}
	if err := c.get(ctx, "/api/sessions/open", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Sessions, nil
}

// UserSessions lists one user's listening sessions, newest first (admin
// only).
func (c *Client) UserSessions(ctx context.Context, userID string, itemsPerPage, page int) ([]Session, int, error) {
	q := url.Values{}
	intQuery(q, "itemsPerPage", itemsPerPage)
	q.Set("page", strconv.Itoa(page))
	var resp struct {
		Total    int       `json:"total"`
		Sessions []Session `json:"sessions"`
	}
	if err := c.get(ctx, "/api/users/"+url.PathEscape(userID)+"/listening-sessions", q, &resp); err != nil {
		return nil, 0, err
	}
	return resp.Sessions, resp.Total, nil
}

// UserListeningStats returns one user's listening totals (admin only).
func (c *Client) UserListeningStats(ctx context.Context, userID string) (*ListeningStats, error) {
	var s ListeningStats
	if err := c.get(ctx, "/api/users/"+url.PathEscape(userID)+"/listening-stats", nil, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// Backups lists server backups and where they are stored (admin only).
func (c *Client) Backups(ctx context.Context) ([]Backup, string, error) {
	var resp struct {
		Backups        []Backup `json:"backups"`
		BackupLocation string   `json:"backupLocation"`
	}
	if err := c.get(ctx, "/api/backups", nil, &resp); err != nil {
		return nil, "", err
	}
	return resp.Backups, resp.BackupLocation, nil
}

// CreateBackup runs a backup now and returns the updated backup list (admin
// only). Older servers return the single new backup instead; both are handled.
func (c *Client) CreateBackup(ctx context.Context) ([]Backup, error) {
	var resp struct {
		Backups []Backup `json:"backups"`
		Backup
	}
	if err := c.post(ctx, "/api/backups", nil, nil, &resp); err != nil {
		return nil, err
	}
	if len(resp.Backups) > 0 {
		return resp.Backups, nil
	}
	if resp.ID != "" {
		return []Backup{resp.Backup}, nil
	}
	return nil, nil
}

// Tags lists every tag in use across all libraries.
func (c *Client) Tags(ctx context.Context) ([]string, error) {
	var resp struct {
		Tags []string `json:"tags"`
	}
	if err := c.get(ctx, "/api/tags", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Tags, nil
}

// Genres lists every genre in use across all libraries.
func (c *Client) Genres(ctx context.Context) ([]string, error) {
	var resp struct {
		Genres []string `json:"genres"`
	}
	if err := c.get(ctx, "/api/genres", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Genres, nil
}

// RenameTag renames a tag on every item that carries it (admin only).
// Returns how many items were updated.
func (c *Client) RenameTag(ctx context.Context, tag, newTag string) (int, error) {
	var resp struct {
		NumItemsUpdated int `json:"numItemsUpdated"`
	}
	if err := c.post(ctx, "/api/tags/rename", nil, map[string]string{"tag": tag, "newTag": newTag}, &resp); err != nil {
		return 0, err
	}
	return resp.NumItemsUpdated, nil
}

// RenameGenre renames a genre on every item that carries it (admin only).
func (c *Client) RenameGenre(ctx context.Context, genre, newGenre string) (int, error) {
	var resp struct {
		NumItemsUpdated int `json:"numItemsUpdated"`
	}
	if err := c.post(ctx, "/api/genres/rename", nil, map[string]string{"genre": genre, "newGenre": newGenre}, &resp); err != nil {
		return 0, err
	}
	return resp.NumItemsUpdated, nil
}

// DeleteTag removes a tag from every item that carries it, across all
// libraries (admin only). Returns how many items were updated. Unlike
// RenameTag this drops the value entirely, which is what a placeholder such as
// "XXX" needs rather than a merge.
func (c *Client) DeleteTag(ctx context.Context, tag string) (int, error) {
	var resp struct {
		NumItemsUpdated int `json:"numItemsUpdated"`
	}
	if err := c.do(ctx, http.MethodDelete, "/api/tags/"+vocabularyID(tag), nil, nil, &resp); err != nil {
		return 0, err
	}
	return resp.NumItemsUpdated, nil
}

// DeleteGenre removes a genre from every item that carries it (admin only).
func (c *Client) DeleteGenre(ctx context.Context, genre string) (int, error) {
	var resp struct {
		NumItemsUpdated int `json:"numItemsUpdated"`
	}
	if err := c.do(ctx, http.MethodDelete, "/api/genres/"+vocabularyID(genre), nil, nil, &resp); err != nil {
		return 0, err
	}
	return resp.NumItemsUpdated, nil
}

// vocabularyID addresses a tag or genre the way the server does: base64 of the
// value, percent-encoded into the path, the same encoding the narrator
// endpoints use.
func vocabularyID(value string) string {
	return url.PathEscape(base64.StdEncoding.EncodeToString([]byte(value)))
}

// ServerYearStats returns the whole server's year in review, as opposed to
// YearStats which is the API key user's own.
func (c *Client) ServerYearStats(ctx context.Context, year int) (*YearStats, error) {
	var s YearStats
	if err := c.get(ctx, "/api/stats/year/"+strconv.Itoa(year), nil, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// DeleteBackup removes a backup file (admin only) and returns the remaining
// backups.
func (c *Client) DeleteBackup(ctx context.Context, id string) ([]Backup, error) {
	var resp struct {
		Backups []Backup `json:"backups"`
	}
	if err := c.do(ctx, http.MethodDelete, "/api/backups/"+url.PathEscape(id), nil, nil, &resp); err != nil {
		return nil, err
	}
	return resp.Backups, nil
}

// SetBackupPath changes where backups are written (admin only).
func (c *Client) SetBackupPath(ctx context.Context, path string) error {
	return c.patch(ctx, "/api/backups/path", map[string]string{"path": path}, nil)
}

// UserCreate holds the fields POST /api/users accepts. Type is the account
// kind: "user", "guest" or "admin".
type UserCreate struct {
	Username            string          `json:"username"`
	Password            string          `json:"password"`
	Type                string          `json:"type,omitempty"`
	Email               string          `json:"email,omitempty"`
	IsActive            *bool           `json:"isActive,omitempty"`
	Permissions         map[string]bool `json:"permissions,omitempty"`
	LibrariesAccessible []string        `json:"librariesAccessible,omitempty"`
	ItemTagsSelected    []string        `json:"itemTagsSelected,omitempty"`
}

// CreateUser adds an account (admin only).
func (c *Client) CreateUser(ctx context.Context, in UserCreate) (*User, error) {
	var resp struct {
		User User `json:"user"`
	}
	if err := c.post(ctx, "/api/users", nil, in, &resp); err != nil {
		return nil, err
	}
	return &resp.User, nil
}

// UserUpdate holds the editable account fields; the zero value changes
// nothing. Id, password hash, token and bookmarks cannot be set here.
type UserUpdate struct {
	Username            *string         `json:"username,omitempty"`
	Password            *string         `json:"password,omitempty"`
	Type                *string         `json:"type,omitempty"`
	Email               *string         `json:"email,omitempty"`
	IsActive            *bool           `json:"isActive,omitempty"`
	Permissions         map[string]bool `json:"permissions,omitempty"`
	LibrariesAccessible []string        `json:"librariesAccessible,omitempty"`
	ItemTagsSelected    []string        `json:"itemTagsSelected,omitempty"`
}

// UpdateUser edits an account (admin only).
func (c *Client) UpdateUser(ctx context.Context, id string, upd UserUpdate) (*User, error) {
	var resp struct {
		User User `json:"user"`
	}
	if err := c.patch(ctx, "/api/users/"+url.PathEscape(id), upd, &resp); err != nil {
		return nil, err
	}
	return &resp.User, nil
}

// DeleteUser removes an account (admin only). The root account cannot be
// deleted.
func (c *Client) DeleteUser(ctx context.Context, id string) error {
	return c.del(ctx, "/api/users/"+url.PathEscape(id), nil)
}
