package tools

import (
	"cmp"
	"strings"

	"github.com/katbyte/abs-mcp/sdk/abs"
)

// progressSummary is the trimmed view of a listening-progress record.
type progressSummary struct {
	Percent     int    `json:"percent"`
	Finished    bool   `json:"finished"`
	CurrentTime int    `json:"current_time_s"                 jsonschema:"position in seconds"`
	LastUpdate  string `json:"last_update,omitempty"`
	FinishedAt  string `json:"finished_at,omitempty"`
	Hidden      bool   `json:"hidden_from_continue,omitempty"`
	ProgressID  string `json:"progress_id,omitempty"          jsonschema:"the id of the progress record"`
}

func progressOf(p *abs.MediaProgress) *progressSummary {
	if p == nil {
		return nil
	}

	return &progressSummary{
		Percent:     percent(p.Progress),
		Finished:    p.IsFinished,
		CurrentTime: wholeSec(p.CurrentTime),
		LastUpdate:  fmtTime(p.LastUpdate),
		FinishedAt:  fmtTime(p.FinishedAt),
		Hidden:      p.HideFromContinueListening,
		ProgressID:  p.Id,
	}
}

// itemSummary is the trimmed view returned by list/search tools: enough to
// identify a book or podcast and judge its metadata without the full payload
// (an expanded item carries every audio file, track and chapter).
type itemSummary struct {
	ID        string           `json:"id"`
	Type      string           `json:"type"                   jsonschema:"book or podcast"`
	Title     string           `json:"title"`
	Subtitle  string           `json:"subtitle,omitempty"`
	Author    string           `json:"author,omitempty"`
	Narrator  string           `json:"narrator,omitempty"`
	Series    []string         `json:"series,omitempty"       jsonschema:"series name with #sequence"`
	Year      string           `json:"year,omitempty"`
	Publisher string           `json:"publisher,omitempty"`
	Genres    []string         `json:"genres,omitempty"`
	Tags      []string         `json:"tags,omitempty"`
	Language  string           `json:"language,omitempty"`
	ASIN      string           `json:"asin,omitempty"`
	ISBN      string           `json:"isbn,omitempty"`
	Duration  int              `json:"duration_s,omitempty"   jsonschema:"length in seconds"`
	Size      int64            `json:"size,omitempty"         jsonschema:"bytes on disk"`
	Tracks    int              `json:"audio_tracks,omitempty"`
	Chapters  int              `json:"chapters,omitempty"`
	Episodes  int              `json:"episodes,omitempty"     jsonschema:"podcasts only"`
	Ebook     string           `json:"ebook,omitempty"        jsonschema:"ebook format when the item has one"`
	Abridged  bool             `json:"abridged,omitempty"`
	Explicit  bool             `json:"explicit,omitempty"`
	NoCover   bool             `json:"no_cover,omitempty"`
	Missing   bool             `json:"missing,omitempty"      jsonschema:"folder no longer on disk"`
	Invalid   bool             `json:"invalid,omitempty"      jsonschema:"folder has no playable media"`
	Path      string           `json:"path,omitempty"         jsonschema:"path relative to the library folder"`
	Added     string           `json:"added,omitempty"`
	FeedURL   string           `json:"feed_url,omitempty"     jsonschema:"podcasts only"`
	Progress  *progressSummary `json:"progress,omitempty"     jsonschema:"the API key user's listening progress, when known"`
}

// summarize is the summary of an item as a library's list sends it.
func summarize(it *abs.LibraryItemMinified) itemSummary {
	filledMinified(it)
	m := it.Media.Metadata
	s := itemSummary{
		ID:        string(it.Id),
		Type:      it.MediaType,
		Title:     m.Title,
		Subtitle:  m.Subtitle,
		Author:    m.AuthorDisplay(),
		Narrator:  m.NarratorDisplay(),
		Series:    m.SeriesDisplay(),
		Year:      m.PublishedYear,
		Publisher: m.Publisher,
		Genres:    m.Genres,
		Tags:      it.Media.Tags,
		Language:  m.Language,
		ASIN:      m.Asin,
		ISBN:      m.Isbn,
		Duration:  wholeSec(it.Media.Duration),
		Size:      it.SizeBytes(),
		Tracks:    it.Media.NumTracks,
		Chapters:  it.Media.NumChapters,
		Ebook:     it.Media.EbookFormat,
		Abridged:  m.Abridged != nil && *m.Abridged,
		Explicit:  m.Explicit != nil && *m.Explicit,
		NoCover:   !it.HasCover(),
		Missing:   it.IsMissing,
		Invalid:   it.IsInvalid,
		Path:      it.RelPath,
		Added:     fmtDate(it.AddedAt),
	}
	if it.IsPodcast() {
		s.Episodes = it.Media.NumEpisodes
		s.FeedURL = m.FeedUrl
		s.Year = m.ReleaseDate
	}

	return s
}

// summarizeWhole is the summary of an item fetched whole: the list's summary, its counts taken from the files and episodes where the record
// has none, and the API key user's progress in it.
func summarizeWhole(it *abs.LibraryItemExpanded) itemSummary {
	filledExpanded(it)
	m := it.Minified()
	s := summarize(&m)
	s.Progress = progressOf(it.UserMediaProgress)

	// a series the whole item names is the whole list of them, not the one a list was filtered by
	s.Series = it.Media.Metadata.SeriesDisplay()
	if s.Author == "" {
		s.Author = it.Media.Metadata.AuthorDisplay()
	}
	if s.Narrator == "" {
		s.Narrator = strings.Join(it.Media.Metadata.Narrators, ", ")
	}
	if it.IsPodcast() && s.Episodes == 0 {
		s.Episodes = len(it.Media.Episodes)
	}
	if s.Tracks == 0 {
		s.Tracks = len(it.Media.Tracks)
	}
	if s.Chapters == 0 {
		s.Chapters = len(it.Media.Chapters)
	}
	if s.Ebook == "" && it.Media.EbookFile != nil {
		s.Ebook = it.Media.EbookFile.EbookFormat
	}

	return s
}

// summarizeAll summarizes the items of a list.
func summarizeAll(items []abs.LibraryItemMinified) []itemSummary {
	out := make([]itemSummary, 0, len(items))
	for i := range items {
		out = append(out, summarize(&items[i]))
	}

	return out
}

// summarizeAllWhole summarizes items fetched whole.
func summarizeAllWhole(items []abs.LibraryItemExpanded) []itemSummary {
	out := make([]itemSummary, 0, len(items))
	for i := range items {
		out = append(out, summarizeWhole(&items[i]))
	}

	return out
}

// episodeSummary is the trimmed view of a podcast episode.
type episodeSummary struct {
	ID          string           `json:"id"`
	PodcastID   string           `json:"podcast_id,omitempty"  jsonschema:"the podcast's library item id"`
	Podcast     string           `json:"podcast,omitempty"`
	Title       string           `json:"title"`
	Season      string           `json:"season,omitempty"`
	Episode     string           `json:"episode,omitempty"`
	Type        string           `json:"type,omitempty"        jsonschema:"full, trailer or bonus"`
	Published   string           `json:"published,omitempty"`
	Duration    int              `json:"duration_s,omitempty"  jsonschema:"length in seconds"`
	Size        int64            `json:"size,omitempty"        jsonschema:"bytes on disk"`
	Description string           `json:"description,omitempty"`
	Progress    *progressSummary `json:"progress,omitempty"`
}

// summarizeEpisode is the summary of a podcast's episode, with the API key user's progress in it when known.
func summarizeEpisode(e *abs.PodcastEpisode, podcastTitle string, withDescription bool, progress *abs.MediaProgress) episodeSummary {
	s := episodeSummary{
		ID:        string(e.Id),
		PodcastID: string(e.LibraryItemId),
		Podcast:   podcastTitle,
		Title:     e.Title,
		Season:    e.Season,
		Episode:   e.Episode,
		Type:      e.EpisodeType,
		Published: cmp.Or(fmtDate(e.PublishedAt), e.PubDate),
		Duration:  wholeSec(episodeLength(e)),
		Progress:  progressOf(progress),
	}
	switch {
	case e.Size > 0:
		s.Size = int64(e.Size)
	case e.AudioFile != nil && e.AudioFile.Metadata != nil:
		s.Size = int64(e.AudioFile.Metadata.Size)
	}
	if withDescription {
		s.Description = clip(plain(e.Description), descriptionCap)
	}
	return s
}

// episodeLength is an episode's length in seconds: its own, or its file's.
func episodeLength(e *abs.PodcastEpisode) float64 {
	switch {
	case e.Duration > 0:
		return e.Duration
	case e.AudioFile != nil:
		return e.AudioFile.Duration
	case e.AudioTrack != nil:
		return e.AudioTrack.Duration
	}
	return 0
}

// libraryRow is the trimmed view of a library.
type libraryRow struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	MediaType string   `json:"media_type"          jsonschema:"book or podcast"`
	Provider  string   `json:"provider,omitempty"  jsonschema:"default metadata provider used by item_match"`
	Folders   []string `json:"folders,omitempty"`
	LastScan  string   `json:"last_scan,omitempty"`
}

func libraryRowOf(l *abs.Library) libraryRow {
	r := libraryRow{ID: string(l.Id), Name: l.Name, MediaType: l.MediaType, Provider: l.Provider, LastScan: fmtTime(l.LastScan)}
	for _, f := range l.Folders {
		r.Folders = append(r.Folders, f.FullPath)
	}

	return r
}

// sessionSummary is the trimmed view of a playback session.
type sessionSummary struct {
	ID          string `json:"id"`
	User        string `json:"user,omitempty"`
	UserID      string `json:"user_id,omitempty"`
	ItemID      string `json:"item_id"`
	EpisodeID   string `json:"episode_id,omitempty"`
	Title       string `json:"title"`
	Author      string `json:"author,omitempty"`
	Type        string `json:"type,omitempty"`
	Listened    int    `json:"listened_s"             jsonschema:"seconds spent listening in this session"`
	Position    int    `json:"position_s"             jsonschema:"playback position at the end of the session, in seconds"`
	Percent     int    `json:"percent,omitempty"`
	Device      string `json:"device,omitempty"`
	Started     string `json:"started,omitempty"`
	LastUpdated string `json:"last_updated,omitempty"`
}

// summarizeSession is the summary of a playback session as the server holds it.
func summarizeSession(s *abs.PlaybackSession) sessionSummary {
	out := sessionSummary{
		ID:          s.Id,
		UserID:      string(s.UserId),
		ItemID:      string(s.LibraryItemId),
		EpisodeID:   string(s.EpisodeId),
		Title:       s.DisplayTitle,
		Author:      s.DisplayAuthor,
		Type:        s.MediaType,
		Listened:    wholeSec(s.TimeListening),
		Position:    wholeSec(s.CurrentTime),
		Device:      cmp.Or(describeDevice(s.DeviceInfo), s.MediaPlayer),
		Started:     fmtTime(s.StartedAt),
		LastUpdated: fmtTime(s.UpdatedAt),
	}
	if s.Duration > 0 {
		out.Percent = percent(s.CurrentTime / s.Duration)
	}
	if s.User != nil {
		out.User = s.User.Username
	}

	return out
}

// describeDevice is what played a session, as one line: the app or browser, and the device or system it ran on.
func describeDevice(d *abs.DeviceInfo) string {
	if d == nil {
		return ""
	}

	var parts []string
	if app := cmp.Or(d.ClientName, d.BrowserName); app != "" {
		parts = append(parts, app)
	}
	device := cmp.Or(d.DeviceName, strings.TrimSpace(d.Manufacturer+" "+d.Model), strings.TrimSpace(d.OsName+" "+d.OsVersion))
	if device != "" {
		parts = append(parts, "on "+device)
	}

	return strings.Join(parts, " ")
}
