package abs

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
)

// FlexString accepts a JSON string or number (Audiobookshelf stores several
// fields, e.g. publishedYear and episode numbers, as strings but some
// providers and older records return numbers).
type FlexString string

func (f *FlexString) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || bytes.Equal(b, []byte("null")) {
		*f = ""
		return nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*f = FlexString(s)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	*f = FlexString(n.String())
	return nil
}

func (f FlexString) String() string { return string(f) }

// Int parses the value as an integer, 0 when empty or not numeric.
func (f FlexString) Int() int {
	n, _ := strconv.Atoi(strings.TrimSpace(string(f)))
	return n
}

// NameRef is the {id, name} pair used for authors and series references.
type NameRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// SeriesRef is a book's membership in a series.
type SeriesRef struct {
	ID       string `json:"id,omitempty"`
	Name     string `json:"name"`
	Sequence string `json:"sequence,omitempty"`
}

// SeriesRefs accepts either an array of series refs (expanded/normal item
// JSON) or a single object (list results filtered by series collapse the
// field to the matched series).
type SeriesRefs []SeriesRef

func (s *SeriesRefs) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || bytes.Equal(b, []byte("null")) {
		*s = nil
		return nil
	}
	if b[0] == '{' {
		var one SeriesRef
		if err := json.Unmarshal(b, &one); err != nil {
			return err
		}
		*s = SeriesRefs{one}
		return nil
	}
	var many []SeriesRef
	if err := json.Unmarshal(b, &many); err != nil {
		return err
	}
	*s = many
	return nil
}

// Status is the unauthenticated GET /status response.
type Status struct {
	App           string `json:"app"`
	ServerVersion string `json:"serverVersion"`
	IsInit        bool   `json:"isInit"`
	Language      string `json:"language"`
}

type Folder struct {
	// omitempty matters on create: the server takes an empty id literally and
	// the second library then collides on the primary key
	ID        string `json:"id,omitempty"`
	FullPath  string `json:"fullPath"`
	LibraryID string `json:"libraryId,omitempty"`
	AddedAt   int64  `json:"addedAt,omitempty"`
}

type LibrarySettings struct {
	CoverAspectRatio                   int      `json:"coverAspectRatio"`
	DisableWatcher                     bool     `json:"disableWatcher"`
	SkipMatchingMediaWithASIN          bool     `json:"skipMatchingMediaWithAsin"`
	SkipMatchingMediaWithISBN          bool     `json:"skipMatchingMediaWithIsbn"`
	AudiobooksOnly                     bool     `json:"audiobooksOnly"`
	EpubsAllowScriptedContent          bool     `json:"epubsAllowScriptedContent"`
	HideSingleBookSeries               bool     `json:"hideSingleBookSeries"`
	OnlyShowLaterBooksInContinueSeries bool     `json:"onlyShowLaterBooksInContinueSeries"`
	MetadataPrecedence                 []string `json:"metadataPrecedence"`
	PodcastSearchRegion                string   `json:"podcastSearchRegion"`
	MarkAsFinishedPercentComplete      *float64 `json:"markAsFinishedPercentComplete"`
	MarkAsFinishedTimeRemaining        *float64 `json:"markAsFinishedTimeRemaining"`
}

type Library struct {
	ID              string          `json:"id"`
	Name            string          `json:"name"`
	Folders         []Folder        `json:"folders"`
	DisplayOrder    int             `json:"displayOrder"`
	Icon            string          `json:"icon"`
	MediaType       string          `json:"mediaType"` // book or podcast
	Provider        string          `json:"provider"`  // default metadata provider, e.g. audible, google, itunes
	Settings        LibrarySettings `json:"settings"`
	LastScan        int64           `json:"lastScan"`
	LastScanVersion string          `json:"lastScanVersion"`
	CreatedAt       int64           `json:"createdAt"`
	LastUpdate      int64           `json:"lastUpdate"`
}

// IsPodcast reports whether the library holds podcasts rather than books.
func (l *Library) IsPodcast() bool { return l.MediaType == "podcast" }

type FileMetadata struct {
	Filename    string `json:"filename"`
	Ext         string `json:"ext"`
	Path        string `json:"path"`
	RelPath     string `json:"relPath"`
	Size        int64  `json:"size"`
	MtimeMs     int64  `json:"mtimeMs"`
	CtimeMs     int64  `json:"ctimeMs"`
	BirthtimeMs int64  `json:"birthtimeMs"`
}

type LibraryFile struct {
	Ino             string       `json:"ino"`
	Metadata        FileMetadata `json:"metadata"`
	IsSupplementary bool         `json:"isSupplementary"`
	AddedAt         int64        `json:"addedAt"`
	UpdatedAt       int64        `json:"updatedAt"`
	FileType        string       `json:"fileType"` // audio, ebook, image, text, metadata, unknown
}

type Chapter struct {
	ID    int     `json:"id"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Title string  `json:"title"`
}

type AudioFile struct {
	Index                int               `json:"index"`
	Ino                  string            `json:"ino"`
	Metadata             FileMetadata      `json:"metadata"`
	AddedAt              int64             `json:"addedAt"`
	UpdatedAt            int64             `json:"updatedAt"`
	TrackNumFromMeta     *int              `json:"trackNumFromMeta"`
	DiscNumFromMeta      *int              `json:"discNumFromMeta"`
	TrackNumFromFilename *int              `json:"trackNumFromFilename"`
	DiscNumFromFilename  *int              `json:"discNumFromFilename"`
	ManuallyVerified     bool              `json:"manuallyVerified"`
	Exclude              bool              `json:"exclude"`
	Error                string            `json:"error"`
	Format               string            `json:"format"`
	Duration             float64           `json:"duration"`
	BitRate              int64             `json:"bitRate"`
	Language             string            `json:"language"`
	Codec                string            `json:"codec"`
	TimeBase             string            `json:"timeBase"`
	Channels             int               `json:"channels"`
	ChannelLayout        string            `json:"channelLayout"`
	Chapters             []Chapter         `json:"chapters"`
	EmbeddedCoverArt     string            `json:"embeddedCoverArt"`
	MetaTags             map[string]string `json:"metaTags"`
	MimeType             string            `json:"mimeType"`
}

type EbookFile struct {
	Ino         string       `json:"ino"`
	Metadata    FileMetadata `json:"metadata"`
	EbookFormat string       `json:"ebookFormat"`
	AddedAt     int64        `json:"addedAt"`
	UpdatedAt   int64        `json:"updatedAt"`
}

type AudioTrack struct {
	Index       int           `json:"index"`
	StartOffset float64       `json:"startOffset"`
	Duration    float64       `json:"duration"`
	Title       string        `json:"title"`
	ContentURL  string        `json:"contentUrl"`
	MimeType    string        `json:"mimeType"`
	Metadata    *FileMetadata `json:"metadata"`
}

// Metadata is the book or podcast metadata block. Book fields and podcast
// fields are both present; only the ones for the item's mediaType are set.
// The minified (list) shape carries the flattened *Name fields, the expanded
// (detail) shape carries the structured Authors/Narrators/Series slices as
// well.
type Metadata struct {
	Title            string     `json:"title"`
	Subtitle         string     `json:"subtitle"`
	AuthorName       string     `json:"authorName"`
	AuthorNameLF     string     `json:"authorNameLF"`
	NarratorName     string     `json:"narratorName"`
	SeriesName       string     `json:"seriesName"`
	Authors          []NameRef  `json:"authors"`
	Narrators        []string   `json:"narrators"`
	Series           SeriesRefs `json:"series"`
	Genres           []string   `json:"genres"`
	PublishedYear    FlexString `json:"publishedYear"`
	PublishedDate    string     `json:"publishedDate"`
	Publisher        string     `json:"publisher"`
	Description      string     `json:"description"`
	DescriptionPlain string     `json:"descriptionPlain"`
	ISBN             string     `json:"isbn"`
	ASIN             string     `json:"asin"`
	Language         string     `json:"language"`
	Explicit         bool       `json:"explicit"`
	Abridged         bool       `json:"abridged"`

	// podcast
	Author         string     `json:"author"`
	ReleaseDate    string     `json:"releaseDate"`
	FeedURL        string     `json:"feedUrl"`
	ImageURL       string     `json:"imageUrl"`
	ItunesPageURL  string     `json:"itunesPageUrl"`
	ItunesID       FlexString `json:"itunesId"`
	ItunesArtistID FlexString `json:"itunesArtistId"`
	PodcastType    string     `json:"type"`
}

// AuthorDisplay returns the author(s) as one string whichever shape was
// returned.
func (m *Metadata) AuthorDisplay() string {
	if m.AuthorName != "" {
		return m.AuthorName
	}
	if len(m.Authors) > 0 {
		names := make([]string, 0, len(m.Authors))
		for _, a := range m.Authors {
			names = append(names, a.Name)
		}
		return strings.Join(names, ", ")
	}
	return m.Author
}

// NarratorDisplay returns the narrator(s) as one string whichever shape was
// returned.
func (m *Metadata) NarratorDisplay() string {
	if m.NarratorName != "" {
		return m.NarratorName
	}
	return strings.Join(m.Narrators, ", ")
}

// SeriesDisplay returns "Series #seq" strings whichever shape was returned.
func (m *Metadata) SeriesDisplay() []string {
	if len(m.Series) > 0 {
		out := make([]string, 0, len(m.Series))
		for _, s := range m.Series {
			if s.Sequence != "" {
				out = append(out, s.Name+" #"+s.Sequence)
			} else {
				out = append(out, s.Name)
			}
		}
		return out
	}
	if m.SeriesName != "" {
		return strings.Split(m.SeriesName, ", ")
	}
	return nil
}

type Enclosure struct {
	URL    string     `json:"url"`
	Type   string     `json:"type"`
	Length FlexString `json:"length"`
}

type Episode struct {
	LibraryItemID string      `json:"libraryItemId"`
	PodcastID     string      `json:"podcastId"`
	ID            string      `json:"id"`
	Index         int         `json:"index"`
	Season        FlexString  `json:"season"`
	Episode       FlexString  `json:"episode"`
	EpisodeType   string      `json:"episodeType"`
	Title         string      `json:"title"`
	Subtitle      string      `json:"subtitle"`
	Description   string      `json:"description"`
	Enclosure     *Enclosure  `json:"enclosure"`
	GUID          string      `json:"guid"`
	PubDate       string      `json:"pubDate"`
	Chapters      []Chapter   `json:"chapters"`
	AudioFile     *AudioFile  `json:"audioFile"`
	PublishedAt   int64       `json:"publishedAt"`
	AddedAt       int64       `json:"addedAt"`
	UpdatedAt     int64       `json:"updatedAt"`
	AudioTrack    *AudioTrack `json:"audioTrack"`
	Size          int64       `json:"size"`
	Duration      float64     `json:"duration"`
	// filled by /me/items-in-progress and recent-episodes
	Progress *MediaProgress `json:"progress,omitempty"`
}

// DurationSeconds returns the episode's audio length from whichever field is
// populated.
func (e *Episode) DurationSeconds() float64 {
	if e.Duration > 0 {
		return e.Duration
	}
	if e.AudioFile != nil {
		return e.AudioFile.Duration
	}
	if e.AudioTrack != nil {
		return e.AudioTrack.Duration
	}
	return 0
}

// Media is the item's book or podcast payload.
type Media struct {
	ID            string       `json:"id"`
	LibraryItemID string       `json:"libraryItemId"`
	Metadata      Metadata     `json:"metadata"`
	CoverPath     string       `json:"coverPath"`
	Tags          []string     `json:"tags"`
	NumTracks     int          `json:"numTracks"`
	NumAudioFiles int          `json:"numAudioFiles"`
	NumChapters   int          `json:"numChapters"`
	Duration      float64      `json:"duration"`
	Size          int64        `json:"size"`
	EbookFormat   string       `json:"ebookFormat"`
	AudioFiles    []AudioFile  `json:"audioFiles"`
	Chapters      []Chapter    `json:"chapters"`
	EbookFile     *EbookFile   `json:"ebookFile"`
	Tracks        []AudioTrack `json:"tracks"`

	// podcast
	NumEpisodes              int       `json:"numEpisodes"`
	AutoDownloadEpisodes     bool      `json:"autoDownloadEpisodes"`
	AutoDownloadSchedule     string    `json:"autoDownloadSchedule"`
	LastEpisodeCheck         int64     `json:"lastEpisodeCheck"`
	MaxEpisodesToKeep        int       `json:"maxEpisodesToKeep"`
	MaxNewEpisodesToDownload int       `json:"maxNewEpisodesToDownload"`
	Episodes                 []Episode `json:"episodes"`
}

type CollapsedSeries struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	NumBooks int    `json:"numBooks"`
}

// Item is a library item (a book folder or a podcast). List endpoints return
// the minified shape, GET /api/items/:id?expanded=1 the expanded one.
type Item struct {
	ID                    string           `json:"id"`
	LibraryID             string           `json:"libraryId"`
	FolderID              string           `json:"folderId"`
	Path                  string           `json:"path"`
	RelPath               string           `json:"relPath"`
	IsFile                bool             `json:"isFile"`
	MtimeMs               int64            `json:"mtimeMs"`
	CtimeMs               int64            `json:"ctimeMs"`
	BirthtimeMs           int64            `json:"birthtimeMs"`
	AddedAt               int64            `json:"addedAt"`
	UpdatedAt             int64            `json:"updatedAt"`
	LastScan              int64            `json:"lastScan"`
	ScanVersion           string           `json:"scanVersion"`
	IsMissing             bool             `json:"isMissing"`
	IsInvalid             bool             `json:"isInvalid"`
	MediaType             string           `json:"mediaType"` // book or podcast
	Media                 Media            `json:"media"`
	NumFiles              int              `json:"numFiles"`
	Size                  int64            `json:"size"`
	LibraryFiles          []LibraryFile    `json:"libraryFiles"`
	CollapsedSeries       *CollapsedSeries `json:"collapsedSeries"`
	NumEpisodesIncomplete int              `json:"numEpisodesIncomplete"`
	RSSFeed               json.RawMessage  `json:"rssFeed"`

	// include=progress on the item endpoint; items-in-progress / continue shelves
	UserMediaProgress *MediaProgress `json:"userMediaProgress"`
	RecentEpisode     *Episode       `json:"recentEpisode"`
}

// IsPodcast reports whether the item is a podcast rather than a book.
func (i *Item) IsPodcast() bool { return i.MediaType == "podcast" }

// Title is the item's display title.
func (i *Item) Title() string { return i.Media.Metadata.Title }

// SizeBytes returns the item's size from whichever field is populated.
func (i *Item) SizeBytes() int64 {
	if i.Size > 0 {
		return i.Size
	}
	return i.Media.Size
}

// HasCover reports whether the item has a cover image.
func (i *Item) HasCover() bool { return i.Media.CoverPath != "" }

type Author struct {
	ID          string `json:"id"`
	ASIN        string `json:"asin"`
	Name        string `json:"name"`
	Description string `json:"description"`
	ImagePath   string `json:"imagePath"`
	LibraryID   string `json:"libraryId"`
	AddedAt     int64  `json:"addedAt"`
	UpdatedAt   int64  `json:"updatedAt"`
	NumBooks    int    `json:"numBooks"`
	// include=items on GET /api/authors/:id
	LibraryItems []Item `json:"libraryItems"`
}

type SeriesProgress struct {
	LibraryItemIDs         []string `json:"libraryItemIds"`
	LibraryItemIDsFinished []string `json:"libraryItemIdsFinished"`
	IsFinished             bool     `json:"isFinished"`
}

type Series struct {
	ID               string          `json:"id"`
	Name             string          `json:"name"`
	NameIgnorePrefix string          `json:"nameIgnorePrefix"`
	Description      string          `json:"description"`
	LibraryID        string          `json:"libraryId"`
	AddedAt          int64           `json:"addedAt"`
	UpdatedAt        int64           `json:"updatedAt"`
	Books            []Item          `json:"books"`
	TotalDuration    float64         `json:"totalDuration"`
	Progress         *SeriesProgress `json:"progress"`
	RSSFeed          json.RawMessage `json:"rssFeed"`
}

type Collection struct {
	ID          string `json:"id"`
	LibraryID   string `json:"libraryId"`
	UserID      string `json:"userId"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Books       []Item `json:"books"`
	LastUpdate  int64  `json:"lastUpdate"`
	CreatedAt   int64  `json:"createdAt"`
}

type PlaylistItem struct {
	LibraryItemID string   `json:"libraryItemId"`
	EpisodeID     string   `json:"episodeId,omitempty"`
	LibraryItem   *Item    `json:"libraryItem,omitempty"`
	Episode       *Episode `json:"episode,omitempty"`
}

type Playlist struct {
	ID          string         `json:"id"`
	LibraryID   string         `json:"libraryId"`
	UserID      string         `json:"userId"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Items       []PlaylistItem `json:"items"`
	LastUpdate  int64          `json:"lastUpdate"`
	CreatedAt   int64          `json:"createdAt"`
}

type MediaProgress struct {
	ID                        string  `json:"id"`
	UserID                    string  `json:"userId"`
	LibraryItemID             string  `json:"libraryItemId"`
	EpisodeID                 string  `json:"episodeId"`
	MediaItemID               string  `json:"mediaItemId"`
	MediaItemType             string  `json:"mediaItemType"` // book or podcastEpisode
	Duration                  float64 `json:"duration"`
	Progress                  float64 `json:"progress"` // 0..1
	CurrentTime               float64 `json:"currentTime"`
	IsFinished                bool    `json:"isFinished"`
	HideFromContinueListening bool    `json:"hideFromContinueListening"`
	EbookLocation             string  `json:"ebookLocation"`
	EbookProgress             float64 `json:"ebookProgress"`
	LastUpdate                int64   `json:"lastUpdate"`
	StartedAt                 int64   `json:"startedAt"`
	FinishedAt                int64   `json:"finishedAt"`
}

type Bookmark struct {
	LibraryItemID string  `json:"libraryItemId"`
	Title         string  `json:"title"`
	Time          float64 `json:"time"`
	CreatedAt     int64   `json:"createdAt"`
}

type Permissions struct {
	Download                  bool `json:"download"`
	Update                    bool `json:"update"`
	Delete                    bool `json:"delete"`
	Upload                    bool `json:"upload"`
	CreateEreader             bool `json:"createEreader"`
	AccessAllLibraries        bool `json:"accessAllLibraries"`
	AccessAllTags             bool `json:"accessAllTags"`
	AccessExplicitContent     bool `json:"accessExplicitContent"`
	SelectedTagsNotAccessible bool `json:"selectedTagsNotAccessible"`
}

type User struct {
	ID                  string          `json:"id"`
	Username            string          `json:"username"`
	Email               string          `json:"email"`
	Type                string          `json:"type"` // root, admin, user, guest
	MediaProgress       []MediaProgress `json:"mediaProgress"`
	Bookmarks           []Bookmark      `json:"bookmarks"`
	IsActive            bool            `json:"isActive"`
	IsLocked            bool            `json:"isLocked"`
	LastSeen            int64           `json:"lastSeen"`
	CreatedAt           int64           `json:"createdAt"`
	Permissions         Permissions     `json:"permissions"`
	LibrariesAccessible []string        `json:"librariesAccessible"`
	ItemTagsSelected    []string        `json:"itemTagsSelected"`
	HasOpenIDLink       bool            `json:"hasOpenIDLink"`
	LatestSession       *Session        `json:"latestSession,omitempty"`
}

// IsAdmin reports whether the user is root or admin.
func (u *User) IsAdmin() bool { return u.Type == "root" || u.Type == "admin" }

type DeviceInfo struct {
	ID             string `json:"id"`
	DeviceID       string `json:"deviceId"`
	IPAddress      string `json:"ipAddress"`
	DeviceName     string `json:"deviceName"`
	DeviceType     string `json:"deviceType"`
	ClientName     string `json:"clientName"`
	ClientVersion  string `json:"clientVersion"`
	Manufacturer   string `json:"manufacturer"`
	Model          string `json:"model"`
	SDKVersion     string `json:"sdkVersion"`
	BrowserName    string `json:"browserName"`
	BrowserVersion string `json:"browserVersion"`
	OSName         string `json:"osName"`
	OSVersion      string `json:"osVersion"`
}

// Describe returns a short "client on device" description.
func (d *DeviceInfo) Describe() string {
	if d == nil {
		return ""
	}
	parts := []string{}
	if d.ClientName != "" {
		parts = append(parts, d.ClientName)
	} else if d.BrowserName != "" {
		parts = append(parts, d.BrowserName)
	}
	device := d.DeviceName
	if device == "" {
		device = strings.TrimSpace(d.Manufacturer + " " + d.Model)
	}
	if device == "" {
		device = strings.TrimSpace(d.OSName + " " + d.OSVersion)
	}
	if device != "" {
		parts = append(parts, "on "+device)
	}
	return strings.Join(parts, " ")
}

type Session struct {
	ID            string          `json:"id"`
	UserID        string          `json:"userId"`
	LibraryID     string          `json:"libraryId"`
	LibraryItemID string          `json:"libraryItemId"`
	BookID        string          `json:"bookId"`
	EpisodeID     string          `json:"episodeId"`
	MediaType     string          `json:"mediaType"`
	MediaMetadata json.RawMessage `json:"mediaMetadata"`
	DisplayTitle  string          `json:"displayTitle"`
	DisplayAuthor string          `json:"displayAuthor"`
	CoverPath     string          `json:"coverPath"`
	Duration      float64         `json:"duration"`
	PlayMethod    int             `json:"playMethod"` // 0 direct play, 1 direct stream, 2 transcode, 3 local
	MediaPlayer   string          `json:"mediaPlayer"`
	DeviceInfo    *DeviceInfo     `json:"deviceInfo"`
	ServerVersion string          `json:"serverVersion"`
	Date          string          `json:"date"`
	DayOfWeek     string          `json:"dayOfWeek"`
	TimeListening float64         `json:"timeListening"`
	StartTime     float64         `json:"startTime"`
	CurrentTime   float64         `json:"currentTime"`
	StartedAt     int64           `json:"startedAt"`
	UpdatedAt     int64           `json:"updatedAt"`
	// open sessions only
	User *NameRef `json:"user,omitempty"`
}

type Task struct {
	ID          string         `json:"id"`
	Action      string         `json:"action"`
	Data        map[string]any `json:"data"`
	Title       string         `json:"title"`
	Description string         `json:"description"`
	Error       string         `json:"error"`
	ShowSuccess bool           `json:"showSuccess"`
	IsFailed    bool           `json:"isFailed"`
	IsFinished  bool           `json:"isFinished"`
	StartedAt   int64          `json:"startedAt"`
	FinishedAt  int64          `json:"finishedAt"`
}

type Backup struct {
	ID            string `json:"id"`
	Key           string `json:"key"`
	BackupDirPath string `json:"backupDirPath"`
	DatePretty    string `json:"datePretty"`
	FullPath      string `json:"fullPath"`
	Path          string `json:"path"`
	Filename      string `json:"filename"`
	FileSize      int64  `json:"fileSize"`
	CreatedAt     int64  `json:"createdAt"`
	ServerVersion string `json:"serverVersion"`
}

type CountRow struct {
	Name     string `json:"name"`
	NumBooks int    `json:"numBooks"`
	NumItems int    `json:"numItems"`
	Count    int    `json:"count"`
}

// N returns whichever count field the endpoint populated.
func (c CountRow) N() int {
	if c.NumBooks > 0 {
		return c.NumBooks
	}
	if c.NumItems > 0 {
		return c.NumItems
	}
	return c.Count
}

type StatItem struct {
	ID       string  `json:"id"`
	Title    string  `json:"title"`
	Size     int64   `json:"size"`
	Duration float64 `json:"duration"`
}

type LibraryStats struct {
	TotalItems       int        `json:"totalItems"`
	TotalAuthors     int        `json:"totalAuthors"`
	TotalGenres      int        `json:"totalGenres"`
	TotalDuration    float64    `json:"totalDuration"`
	TotalSize        int64      `json:"totalSize"`
	NumAudioTracks   int        `json:"numAudioTracks"`
	LargestItems     []StatItem `json:"largestItems"`
	LongestItems     []StatItem `json:"longestItems"`
	AuthorsWithCount []struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		Count int    `json:"count"`
	} `json:"authorsWithCount"`
	GenresWithCount []struct {
		Genre string `json:"genre"`
		Count int    `json:"count"`
	} `json:"genresWithCount"`
}

type FilterData struct {
	Authors          []NameRef `json:"authors"`
	Genres           []string  `json:"genres"`
	Tags             []string  `json:"tags"`
	Series           []NameRef `json:"series"`
	Narrators        []string  `json:"narrators"`
	Languages        []string  `json:"languages"`
	Publishers       []string  `json:"publishers"`
	PublishedDecades []string  `json:"publishedDecades"`
	BookCount        int       `json:"bookCount"`
	AuthorCount      int       `json:"authorCount"`
	SeriesCount      int       `json:"seriesCount"`
	PodcastCount     int       `json:"podcastCount"`
	NumIssues        int       `json:"numIssues"`
}

type SearchMatch struct {
	LibraryItem Item   `json:"libraryItem"`
	MatchKey    string `json:"matchKey"`
	MatchText   string `json:"matchText"`
}

type SeriesMatch struct {
	Series Series `json:"series"`
	Books  []Item `json:"books"`
}

type SearchResult struct {
	Book      []SearchMatch `json:"book"`
	Podcast   []SearchMatch `json:"podcast"`
	Narrators []CountRow    `json:"narrators"`
	Tags      []CountRow    `json:"tags"`
	Genres    []CountRow    `json:"genres"`
	Series    []SeriesMatch `json:"series"`
	Authors   []Author      `json:"authors"`
}

// Items returns book or podcast matches, whichever the library holds.
func (r *SearchResult) Items() []SearchMatch {
	if len(r.Book) > 0 {
		return r.Book
	}
	return r.Podcast
}

type NarratorRow struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	NumBooks int    `json:"numBooks"`
}

// MatchResult is the response of a quick match against a metadata provider.
type MatchResult struct {
	Updated     bool   `json:"updated"`
	LibraryItem *Item  `json:"libraryItem"`
	Warning     string `json:"warning"`
}

// BookSearchResult is one provider hit from GET /api/search/books.
type BookSearchResult struct {
	Title         string      `json:"title"`
	Subtitle      string      `json:"subtitle"`
	Author        string      `json:"author"`
	Narrator      string      `json:"narrator"`
	Publisher     string      `json:"publisher"`
	PublishedYear FlexString  `json:"publishedYear"`
	Description   string      `json:"description"`
	Cover         string      `json:"cover"`
	ASIN          string      `json:"asin"`
	ISBN          string      `json:"isbn"`
	Genres        []string    `json:"genres"`
	Tags          []string    `json:"tags"`
	Series        []SeriesRef `json:"series"`
	Language      string      `json:"language"`
	Duration      float64     `json:"duration"` // minutes
	Region        string      `json:"region"`
	Rating        FlexString  `json:"rating"`
	Abridged      bool        `json:"abridged"`
	// custom providers may return an id
	ID FlexString `json:"id"`
}

// PodcastSearchResult is one iTunes hit from GET /api/search/podcast.
type PodcastSearchResult struct {
	ID               FlexString `json:"id"`
	ArtistID         FlexString `json:"artistId"`
	Title            string     `json:"title"`
	ArtistName       string     `json:"artistName"`
	Description      string     `json:"description"`
	DescriptionPlain string     `json:"descriptionPlain"`
	ReleaseDate      string     `json:"releaseDate"`
	Genres           []string   `json:"genres"`
	Cover            string     `json:"cover"`
	TrackCount       int        `json:"trackCount"`
	FeedURL          string     `json:"feedUrl"`
	PageURL          string     `json:"pageUrl"`
	Explicit         bool       `json:"explicit"`
}

// FeedEpisode is an episode parsed from a podcast's RSS feed (not yet in the
// library). Passing it back to DownloadEpisodes queues the download.
type FeedEpisode struct {
	Title            string          `json:"title"`
	Subtitle         string          `json:"subtitle"`
	Description      string          `json:"description"`
	DescriptionPlain string          `json:"descriptionPlain"`
	PubDate          string          `json:"pubDate"`
	EpisodeType      string          `json:"episodeType"`
	Season           FlexString      `json:"season"`
	Episode          FlexString      `json:"episode"`
	Author           string          `json:"author"`
	Duration         FlexString      `json:"duration"`
	DurationSeconds  *float64        `json:"durationSeconds"`
	Explicit         FlexString      `json:"explicit"`
	PublishedAt      int64           `json:"publishedAt"`
	Enclosure        *Enclosure      `json:"enclosure"`
	GUID             string          `json:"guid"`
	ChaptersURL      string          `json:"chaptersUrl"`
	ChaptersType     string          `json:"chaptersType"`
	Chapters         json.RawMessage `json:"chapters"`
}

// FeedPodcast is the parsed feed from POST /api/podcasts/feed.
type FeedPodcast struct {
	Metadata struct {
		Image       string   `json:"image"`
		Categories  []string `json:"categories"`
		FeedURL     string   `json:"feedUrl"`
		Description string   `json:"description"`
		Title       string   `json:"title"`
		Language    string   `json:"language"`
		Explicit    string   `json:"explicit"`
		Author      string   `json:"author"`
		PubDate     string   `json:"pubDate"`
		Link        string   `json:"link"`
		Type        string   `json:"type"`
	} `json:"metadata"`
	Episodes    []FeedEpisode `json:"episodes"`
	NumEpisodes int           `json:"numEpisodes"`
}

// EpisodeDownload is a queued or running podcast episode download.
type EpisodeDownload struct {
	ID                  string     `json:"id"`
	EpisodeDisplayTitle string     `json:"episodeDisplayTitle"`
	URL                 string     `json:"url"`
	LibraryItemID       string     `json:"libraryItemId"`
	LibraryID           string     `json:"libraryId"`
	IsFinished          bool       `json:"isFinished"`
	Failed              bool       `json:"failed"`
	StartedAt           int64      `json:"startedAt"`
	CreatedAt           int64      `json:"createdAt"`
	FinishedAt          int64      `json:"finishedAt"`
	PodcastTitle        string     `json:"podcastTitle"`
	Season              FlexString `json:"season"`
	Episode             FlexString `json:"episode"`
	EpisodeType         string     `json:"episodeType"`
	PublishedAt         int64      `json:"publishedAt"`
}

type ListeningStatItem struct {
	ID            string          `json:"id"`
	TimeListening float64         `json:"timeListening"`
	MediaMetadata json.RawMessage `json:"mediaMetadata"`
}

type ListeningStats struct {
	TotalTime      float64                      `json:"totalTime"`
	Items          map[string]ListeningStatItem `json:"items"`
	Days           map[string]float64           `json:"days"`
	DayOfWeek      map[string]float64           `json:"dayOfWeek"`
	Today          float64                      `json:"today"`
	RecentSessions []Session                    `json:"recentSessions"`
}

type YearStats struct {
	TotalListeningSessions int     `json:"totalListeningSessions"`
	TotalListeningTime     float64 `json:"totalListeningTime"`
	NumBooksListened       int     `json:"numBooksListened"`
	NumBooksFinished       int     `json:"numBooksFinished"`
	NumAuthors             int     `json:"numAuthors"`
	NumGenres              int     `json:"numGenres"`
	NumSeries              int     `json:"numSeries"`
	NumNarrators           int     `json:"numNarrators"`
	TotalDurationListened  float64 `json:"totalDurationListened"`
	BooksFinished          []struct {
		ID         string  `json:"id"`
		Title      string  `json:"title"`
		Duration   float64 `json:"duration"`
		FinishedAt int64   `json:"finishedAt"`
	} `json:"booksFinished"`
	ListeningTimeByDay map[string]float64 `json:"listeningTimeByDay"`
	MostListenedAuthor *struct {
		Name string  `json:"name"`
		Time float64 `json:"time"`
	} `json:"mostListenedAuthor"`
	MostListenedNarrator *struct {
		Name string  `json:"name"`
		Time float64 `json:"time"`
	} `json:"mostListenedNarrator"`
	MostListenedGenre *struct {
		Genre string  `json:"genre"`
		Time  float64 `json:"time"`
	} `json:"mostListenedGenre"`
	TopAuthors []struct {
		Name string  `json:"name"`
		Time float64 `json:"time"`
	} `json:"topAuthors"`
	TopNarrators []struct {
		Name string  `json:"name"`
		Time float64 `json:"time"`
	} `json:"topNarrators"`
	TopGenres []struct {
		Genre string  `json:"genre"`
		Time  float64 `json:"time"`
	} `json:"topGenres"`
}

type SizeStats struct {
	TotalSize     int64 `json:"totalSize"`
	NumItems      int   `json:"numItems"`
	NumAudioFiles int   `json:"numAudioFiles"`
}

type ServerStats struct {
	Books    SizeStats `json:"books"`
	Podcasts SizeStats `json:"podcasts"`
	Total    SizeStats `json:"total"`
}

// Shelf is one row of the personalized home page.
type Shelf struct {
	ID       string          `json:"id"` // continue-listening, continue-series, recently-added, listen-again, recent-series, discover, newest-authors, newest-episodes, continue-reading, ...
	Label    string          `json:"label"`
	Type     string          `json:"type"` // book, podcast, episode, series, authors
	Entities json.RawMessage `json:"entities"`
}
