package abs

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
)

// Server administration: notifications, email, API keys, custom providers,
// settings and the filesystem browser. None of this is wrapped as an MCP tool
// - an AI curating a library has no business changing auth settings - but the
// client is a general Audiobookshelf client, so it covers the API.

// --- notifications ------------------------------------------------------

// Notification is one configured notification: an event, and the webhook to
// fire when it happens.
type Notification struct {
	ID            string            `json:"id"`
	LibraryID     string            `json:"libraryId"`
	EventName     string            `json:"eventName"`
	URLs          []string          `json:"urls"`
	TitleTemplate string            `json:"titleTemplate"`
	BodyTemplate  string            `json:"bodyTemplate"`
	Headers       map[string]string `json:"headers"`
	Enabled       bool              `json:"enabled"`
	Type          string            `json:"type"`
	LastFiredAt   int64             `json:"lastFiredAt"`
	NumSuccess    int               `json:"numSuccess"`
	NumFailed     int               `json:"numFailed"`
}

// NotificationSettings is the server's notification configuration, with the
// notifications themselves.
type NotificationSettings struct {
	AppriseType          string         `json:"appriseType"`
	AppriseAPIURL        string         `json:"appriseApiUrl"`
	Notifications        []Notification `json:"notifications"`
	MaxFailedAttempts    int            `json:"maxFailedAttempts"`
	MaxNotificationQueue int            `json:"maxNotificationQueue"`
	Enabled              bool           `json:"enabled"`
}

// Notifications returns the notification settings (admin only).
func (c *Client) Notifications(ctx context.Context) (*NotificationSettings, error) {
	var resp struct {
		Settings NotificationSettings `json:"settings"`
	}
	if err := c.get(ctx, "/api/notifications", nil, &resp); err != nil {
		return nil, err
	}
	return &resp.Settings, nil
}

// NotificationData returns the events a notification can be attached to, and
// the variables each event makes available to its template.
func (c *Client) NotificationData(ctx context.Context) (map[string]any, error) {
	var out map[string]any
	if err := c.get(ctx, "/api/notificationdata", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// UpdateNotificationSettings changes the server-wide notification settings.
func (c *Client) UpdateNotificationSettings(ctx context.Context, settings map[string]any) error {
	return c.patch(ctx, "/api/notifications", settings, nil)
}

// CreateNotification adds a notification (admin only) and returns the settings
// as they now stand. Note the server answers this one with the settings
// unwrapped, where the GET wraps them in a settings key.
func (c *Client) CreateNotification(ctx context.Context, n map[string]any) (*NotificationSettings, error) {
	var settings NotificationSettings
	if err := c.post(ctx, "/api/notifications", nil, n, &settings); err != nil {
		return nil, err
	}
	return &settings, nil
}

// UpdateNotification edits one notification and returns the settings as they
// now stand.
func (c *Client) UpdateNotification(ctx context.Context, id string, n map[string]any) (*NotificationSettings, error) {
	var settings NotificationSettings
	if err := c.patch(ctx, "/api/notifications/"+url.PathEscape(id), n, &settings); err != nil {
		return nil, err
	}
	return &settings, nil
}

// DeleteNotification removes one.
func (c *Client) DeleteNotification(ctx context.Context, id string) error {
	return c.del(ctx, "/api/notifications/"+url.PathEscape(id), nil)
}

// TestNotification fires a test event at every configured notification.
func (c *Client) TestNotification(ctx context.Context) error {
	return c.get(ctx, "/api/notifications/test", nil, nil)
}

// TestOneNotification fires a test event at one notification.
func (c *Client) TestOneNotification(ctx context.Context, id string) error {
	return c.get(ctx, "/api/notifications/"+url.PathEscape(id)+"/test", nil, nil)
}

// --- email and ereader devices ------------------------------------------

// EReaderDevice is somewhere an ebook can be sent.
type EReaderDevice struct {
	Name        string   `json:"name"`
	Email       string   `json:"email"`
	AvailableTo string   `json:"availabilityOption"`
	Users       []string `json:"users"`
}

// EmailSettings is the SMTP configuration used to send ebooks.
type EmailSettings struct {
	ID                 string          `json:"id"`
	Host               string          `json:"host"`
	Port               int             `json:"port"`
	Secure             bool            `json:"secure"`
	RejectUnauthorized bool            `json:"rejectUnauthorized"`
	User               string          `json:"user"`
	FromAddress        string          `json:"fromAddress"`
	TestAddress        string          `json:"testAddress"`
	EReaderDevices     []EReaderDevice `json:"ereaderDevices"`
}

// EmailSettings returns the SMTP configuration (admin only).
func (c *Client) EmailSettings(ctx context.Context) (*EmailSettings, error) {
	var resp struct {
		Settings EmailSettings `json:"settings"`
	}
	if err := c.get(ctx, "/api/emails/settings", nil, &resp); err != nil {
		return nil, err
	}
	return &resp.Settings, nil
}

// UpdateEmailSettings changes the SMTP configuration.
func (c *Client) UpdateEmailSettings(ctx context.Context, settings map[string]any) error {
	return c.patch(ctx, "/api/emails/settings", settings, nil)
}

// TestEmail sends a test message to the configured test address.
func (c *Client) TestEmail(ctx context.Context) error {
	return c.post(ctx, "/api/emails/test", nil, nil, nil)
}

// UpdateEReaderDevices replaces the list of devices ebooks can be sent to
// (admin only). MeUpdateEReaderDevices is the same for the calling user.
func (c *Client) UpdateEReaderDevices(ctx context.Context, devices []EReaderDevice) ([]EReaderDevice, error) {
	var resp struct {
		EReaderDevices []EReaderDevice `json:"ereaderDevices"`
	}
	body := map[string]any{"ereaderDevices": devices}
	if err := c.post(ctx, "/api/emails/ereader-devices", nil, body, &resp); err != nil {
		return nil, err
	}
	return resp.EReaderDevices, nil
}

// MeUpdateEReaderDevices replaces the calling user's own device list. The
// server requires every device here to have AvailableTo "specificUsers" with
// Users naming only the calling user: a non-admin cannot make a device others
// can see.
func (c *Client) MeUpdateEReaderDevices(ctx context.Context, devices []EReaderDevice) ([]EReaderDevice, error) {
	var resp struct {
		EReaderDevices []EReaderDevice `json:"ereaderDevices"`
	}
	body := map[string]any{"ereaderDevices": devices}
	if err := c.post(ctx, "/api/me/ereader-devices", nil, body, &resp); err != nil {
		return nil, err
	}
	return resp.EReaderDevices, nil
}

// SendEbookToDevice emails an item's ebook file to a configured device.
func (c *Client) SendEbookToDevice(ctx context.Context, itemID, deviceName string) error {
	body := map[string]string{"libraryItemId": itemID, "deviceName": deviceName}
	return c.post(ctx, "/api/emails/send-ebook-to-device", nil, body, nil)
}

// --- API keys -----------------------------------------------------------

// APIKey is a key that acts as one user. The plaintext key is returned only
// when it is created.
type APIKey struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	UserID string `json:"userId"`
	// these are ISO timestamps, not the epoch milliseconds the rest of the
	// API uses
	ExpiresAt string `json:"expiresAt"`
	LastUsed  string `json:"lastUsedAt"`
	CreatedAt string `json:"createdAt"`
	IsActive  bool   `json:"isActive"`
	Key       string `json:"apiKey,omitempty"`
}

// APIKeys lists the server's API keys (admin only). The keys themselves are
// not returned, only their metadata.
func (c *Client) APIKeys(ctx context.Context) ([]APIKey, error) {
	var resp struct {
		APIKeys []APIKey `json:"apiKeys"`
	}
	if err := c.get(ctx, "/api/api-keys", nil, &resp); err != nil {
		return nil, err
	}
	return resp.APIKeys, nil
}

// CreateAPIKey mints a key for a user (admin only). The plaintext key is in
// the returned Key field and is never shown again.
func (c *Client) CreateAPIKey(ctx context.Context, name, userID string, expiresIn int, active bool) (*APIKey, error) {
	body := map[string]any{"name": name, "userId": userID, "isActive": active}
	if expiresIn > 0 {
		body["expiresIn"] = expiresIn
	}
	var resp struct {
		APIKey APIKey `json:"apiKey"`
	}
	if err := c.post(ctx, "/api/api-keys", nil, body, &resp); err != nil {
		return nil, err
	}
	return &resp.APIKey, nil
}

// UpdateAPIKey changes a key's name or whether it is active.
func (c *Client) UpdateAPIKey(ctx context.Context, id string, fields map[string]any) (*APIKey, error) {
	var resp struct {
		APIKey APIKey `json:"apiKey"`
	}
	if err := c.patch(ctx, "/api/api-keys/"+url.PathEscape(id), fields, &resp); err != nil {
		return nil, err
	}
	return &resp.APIKey, nil
}

// DeleteAPIKey revokes a key.
func (c *Client) DeleteAPIKey(ctx context.Context, id string) error {
	return c.del(ctx, "/api/api-keys/"+url.PathEscape(id), nil)
}

// --- custom metadata providers ------------------------------------------

// CustomMetadataProvider is a provider the server will call for book matches,
// alongside the built-in ones.
type CustomMetadataProvider struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	URL             string `json:"url"`
	MediaType       string `json:"mediaType"`
	AuthHeaderValue string `json:"authHeaderValue"`
	Slug            string `json:"slug"`
}

// CustomMetadataProviders lists them (admin only).
func (c *Client) CustomMetadataProviders(ctx context.Context) ([]CustomMetadataProvider, error) {
	var resp struct {
		Providers []CustomMetadataProvider `json:"providers"`
	}
	if err := c.get(ctx, "/api/custom-metadata-providers", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Providers, nil
}

// CreateCustomMetadataProvider adds one. mediaType is "book" or "podcast".
// The server will call <url>/search?mediaType=&query=&author= and expects
// {"matches":[...]} back.
func (c *Client) CreateCustomMetadataProvider(ctx context.Context, name, providerURL, mediaType, authHeader string) (*CustomMetadataProvider, error) {
	if mediaType == "" {
		mediaType = "book"
	}
	body := map[string]string{"name": name, "url": providerURL, "mediaType": mediaType, "authHeaderValue": authHeader}
	var resp struct {
		Provider CustomMetadataProvider `json:"provider"`
	}
	if err := c.post(ctx, "/api/custom-metadata-providers", nil, body, &resp); err != nil {
		return nil, err
	}
	return &resp.Provider, nil
}

// DeleteCustomMetadataProvider removes one.
func (c *Client) DeleteCustomMetadataProvider(ctx context.Context, id string) error {
	return c.del(ctx, "/api/custom-metadata-providers/"+url.PathEscape(id), nil)
}

// --- settings and maintenance -------------------------------------------

// UpdateServerSettings changes the server-wide settings (admin only).
func (c *Client) UpdateServerSettings(ctx context.Context, settings map[string]any) (map[string]any, error) {
	var out map[string]any
	if err := c.patch(ctx, "/api/settings", settings, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// AuthSettings returns the authentication configuration (admin only).
func (c *Client) AuthSettings(ctx context.Context) (map[string]any, error) {
	var out map[string]any
	if err := c.get(ctx, "/api/auth-settings", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// UpdateAuthSettings changes the authentication configuration.
func (c *Client) UpdateAuthSettings(ctx context.Context, settings map[string]any) error {
	return c.patch(ctx, "/api/auth-settings", settings, nil)
}

// UpdateSortingPrefixes sets the words ignored when sorting titles ("the",
// "a"), which changes how every list is ordered.
func (c *Client) UpdateSortingPrefixes(ctx context.Context, prefixes []string) error {
	return c.patch(ctx, "/api/sorting-prefixes", map[string]any{"sortingPrefixes": prefixes}, nil)
}

// ValidateCron checks a cron expression without scheduling anything.
func (c *Client) ValidateCron(ctx context.Context, expression string) error {
	return c.post(ctx, "/api/validate-cron", nil, map[string]string{"expression": expression}, nil)
}

// UpdateWatcher turns the filesystem watcher on or off for a library.
func (c *Client) UpdateWatcher(ctx context.Context, libraryID string, enabled bool) error {
	body := map[string]any{"libraryId": libraryID, "enabled": enabled}
	return c.post(ctx, "/api/watcher/update", nil, body, nil)
}

// LoggerData returns the server's log settings and the levels available.
func (c *Client) LoggerData(ctx context.Context) (map[string]any, error) {
	var out map[string]any
	if err := c.get(ctx, "/api/logger-data", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// PurgeCache empties the whole server cache (admin only).
func (c *Client) PurgeCache(ctx context.Context) error {
	return c.post(ctx, "/api/cache/purge", nil, nil, nil)
}

// PurgeItemsCache empties just the cached item images.
func (c *Client) PurgeItemsCache(ctx context.Context) error {
	return c.post(ctx, "/api/cache/items/purge", nil, nil, nil)
}

// --- filesystem browser -------------------------------------------------

// FilesystemPath is a directory the server can see, as the folder picker
// shows it.
type FilesystemPath struct {
	Path     string           `json:"path"`
	DirName  string           `json:"dirname"`
	Children []FilesystemPath `json:"children"`
}

// Filesystem lists the directories the server can reach, which is what the
// folder picker reads when a library is created.
func (c *Client) Filesystem(ctx context.Context) ([]FilesystemPath, error) {
	var resp struct {
		Directories []FilesystemPath `json:"directories"`
	}
	if err := c.get(ctx, "/api/filesystem", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Directories, nil
}

// PathExists reports whether a directory exists inside a library folder, and
// names the library item already there when one is. This is what the new-item
// dialog calls before it will let you create a folder.
func (c *Client) PathExists(ctx context.Context, folderPath, directory string) (exists bool, itemTitle string, err error) {
	var resp struct {
		Exists           bool   `json:"exists"`
		LibraryItemTitle string `json:"libraryItemTitle"`
	}
	body := map[string]string{"folderPath": folderPath, "directory": directory}
	if err := c.post(ctx, "/api/filesystem/pathexists", nil, body, &resp); err != nil {
		return false, "", err
	}
	return resp.Exists, resp.LibraryItemTitle, nil
}

// --- sharing ------------------------------------------------------------

// MediaItemShare is a public link to one item.
type MediaItemShare struct {
	ID          string `json:"id"`
	MediaItemID string `json:"mediaItemId"`
	Slug        string `json:"slug"`
	// ISO timestamps, like the API key ones and unlike the epoch
	// milliseconds the rest of the API uses
	ExpiresAt string `json:"expiresAt"`
	CreatedAt string `json:"createdAt"`
}

// ShareMediaItem opens a public link to a media item (admin only). expiresAt
// is epoch milliseconds, not a duration; zero never expires. mediaItemType is
// "book" or "podcastEpisode". Note the id is the media item's, which is not
// the library item's id - take it from Item.Media.ID.
func (c *Client) ShareMediaItem(ctx context.Context, mediaItemID, mediaItemType, slug string, expiresAt int64, downloadable bool) (*MediaItemShare, error) {
	if mediaItemType == "" {
		mediaItemType = "book"
	}
	body := map[string]any{
		"mediaItemId": mediaItemID, "mediaItemType": mediaItemType, "slug": slug,
		"expiresAt": expiresAt, "isDownloadable": downloadable,
	}
	var share MediaItemShare
	if err := c.post(ctx, "/api/share/mediaitem", nil, body, &share); err != nil {
		return nil, err
	}
	return &share, nil
}

// UnshareMediaItem closes a public link.
func (c *Client) UnshareMediaItem(ctx context.Context, id string) error {
	return c.del(ctx, "/api/share/mediaitem/"+url.PathEscape(id), nil)
}

// --- playback sessions --------------------------------------------------

// PlayRequest is what a client tells the server when it starts playing, so the
// server can pick a stream and track progress.
type PlayRequest struct {
	DeviceInfo         map[string]any `json:"deviceInfo,omitempty"`
	SupportedMimeTypes []string       `json:"supportedMimeTypes,omitempty"`
	MediaPlayer        string         `json:"mediaPlayer,omitempty"`
	ForceDirectPlay    bool           `json:"forceDirectPlay,omitempty"`
	ForceTranscode     bool           `json:"forceTranscode,omitempty"`
}

// Play opens a playback session for an item, returning the session with the
// tracks to play. episodeID may be empty for a book.
func (c *Client) Play(ctx context.Context, itemID, episodeID string, req PlayRequest) (*Session, error) {
	if episodeID != "" {
		return c.openPlaybackSession(ctx, "/api/items/"+url.PathEscape(itemID)+"/play/"+url.PathEscape(episodeID), req)
	}
	return c.openPlaybackSession(ctx, "/api/items/"+url.PathEscape(itemID)+"/play", req)
}

func (c *Client) openPlaybackSession(ctx context.Context, path string, req PlayRequest) (*Session, error) {
	var s Session
	if err := c.post(ctx, path, nil, req, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// Session returns one playback session by id.
func (c *Client) Session(ctx context.Context, id string) (*Session, error) {
	var s Session
	if err := c.get(ctx, "/api/session/"+url.PathEscape(id), nil, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// SyncSession reports listening progress against an open session.
func (c *Client) SyncSession(ctx context.Context, id string, currentTime, timeListened float64) error {
	body := map[string]any{"currentTime": currentTime, "timeListened": timeListened}
	return c.post(ctx, "/api/session/"+url.PathEscape(id)+"/sync", nil, body, nil)
}

// CloseSession ends a playback session, optionally with a final sync.
func (c *Client) CloseSession(ctx context.Context, id string, final map[string]any) error {
	return c.post(ctx, "/api/session/"+url.PathEscape(id)+"/close", nil, final, nil)
}

// SyncLocalSession uploads a session recorded while offline.
func (c *Client) SyncLocalSession(ctx context.Context, session map[string]any) error {
	// answers with plain text rather than JSON
	return c.post(ctx, "/api/session/local", nil, session, nil)
}

// SyncLocalSessions uploads several offline sessions at once.
func (c *Client) SyncLocalSessions(ctx context.Context, sessions []map[string]any) error {
	return c.post(ctx, "/api/session/local-all", nil, map[string]any{"sessions": sessions}, nil)
}

// DeleteSession removes a session record (admin only).
func (c *Client) DeleteSession(ctx context.Context, id string) error {
	return c.del(ctx, "/api/sessions/"+url.PathEscape(id), nil)
}

// DeleteSessions removes several session records at once (admin only).
func (c *Client) DeleteSessions(ctx context.Context, ids []string) error {
	return c.post(ctx, "/api/sessions/batch/delete", nil, map[string][]string{"sessions": ids}, nil)
}

// MeSessions lists the calling user's own device sessions - the logins, not
// the playback sessions.
func (c *Client) MeSessions(ctx context.Context) (map[string]any, error) {
	var out map[string]any
	if err := c.get(ctx, "/api/me/sessions", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// CloseMeSession ends one of the calling user's device sessions.
func (c *Client) CloseMeSession(ctx context.Context, id string) error {
	return c.del(ctx, "/api/me/sessions/"+url.PathEscape(id), nil)
}

// --- auth ---------------------------------------------------------------

// Authorize exchanges the current credentials for the user and server
// settings, which is what a client calls on start-up.
func (c *Client) Authorize(ctx context.Context) (map[string]any, error) {
	var out map[string]any
	if err := c.post(ctx, "/api/authorize", nil, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ChangePassword changes the calling user's password.
func (c *Client) ChangePassword(ctx context.Context, currentPassword, newPassword string) error {
	body := map[string]string{"password": currentPassword, "newPassword": newPassword}
	return c.patch(ctx, "/api/me/password", body, nil)
}

// UnlinkOpenID detaches a user's OpenID identity (admin only).
func (c *Client) UnlinkOpenID(ctx context.Context, userID string) error {
	return c.patch(ctx, "/api/users/"+url.PathEscape(userID)+"/openid-unlink", nil, nil)
}

// --- item files and ebooks ----------------------------------------------

// DeleteItemFile removes one file from an item, by the file id the item
// reports in LibraryFiles.
func (c *Client) DeleteItemFile(ctx context.Context, itemID, fileID string) (*Item, error) {
	var it Item
	path := "/api/items/" + url.PathEscape(itemID) + "/file/" + url.PathEscape(fileID)
	if err := c.do(ctx, http.MethodDelete, path, nil, nil, &it); err != nil {
		return nil, err
	}
	return &it, nil
}

// SetEbookPrimary marks which ebook file is the primary one when an item has
// several. isPrimary false makes it supplementary.
func (c *Client) SetEbookPrimary(ctx context.Context, itemID, fileID string, isPrimary bool) error {
	path := "/api/items/" + url.PathEscape(itemID) + "/ebook/" + url.PathEscape(fileID) + "/status"
	return c.patch(ctx, path, map[string]bool{"isSupplementary": !isPrimary}, nil)
}

// --- podcast opml -------------------------------------------------------

// ParseOPML reads an OPML document and returns the feeds in it, without
// subscribing to anything.
func (c *Client) ParseOPML(ctx context.Context, opmlText string) ([]map[string]any, error) {
	var resp struct {
		Feeds []map[string]any `json:"feeds"`
	}
	if err := c.post(ctx, "/api/podcasts/opml/parse", nil, map[string]string{"opmlText": opmlText}, &resp); err != nil {
		return nil, err
	}
	return resp.Feeds, nil
}

// CreatePodcastsFromOPML subscribes to every feed in an OPML document.
func (c *Client) CreatePodcastsFromOPML(ctx context.Context, libraryID, folderID string, feedURLs []string, autoDownload bool) error {
	body := map[string]any{
		"libraryId": libraryID, "folderId": folderID,
		"feeds": feedURLs, "autoDownloadEpisodes": autoDownload,
	}
	return c.post(ctx, "/api/podcasts/opml/create", nil, body, nil)
}

// --- backups ------------------------------------------------------------

// ApplyBackup restores the server from a backup (admin only). This replaces
// the database and restarts the server: everything created since the backup
// is lost.
func (c *Client) ApplyBackup(ctx context.Context, id string) error {
	return c.get(ctx, "/api/backups/"+url.PathEscape(id)+"/apply", nil, nil)
}

// intParam is a small helper for the query strings above.
func intParam(q url.Values, key string, v int) {
	if v > 0 {
		q.Set(key, strconv.Itoa(v))
	}
}
