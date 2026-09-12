package tools

import (
	"github.com/katbyte/abs-mcp/lib/abs"
)

// progressSummary is the trimmed view of a listening-progress record.
type progressSummary struct {
	Percent     int    `json:"percent"`
	Finished    bool   `json:"finished"`
	CurrentTime string `json:"current_time,omitempty"         jsonschema:"position as h m s"`
	Seconds     int    `json:"current_seconds,omitempty"`
	LastUpdate  string `json:"last_update,omitempty"`
	FinishedAt  string `json:"finished_at,omitempty"`
	Hidden      bool   `json:"hidden_from_continue,omitempty"`
	ProgressID  string `json:"progress_id,omitempty"          jsonschema:"pass to me_progress_remove"`
}

func progressOf(p *abs.MediaProgress) *progressSummary {
	if p == nil {
		return nil
	}
	return &progressSummary{
		Percent:     percent(p.Progress),
		Finished:    p.IsFinished,
		CurrentTime: fmtDuration(p.CurrentTime),
		Seconds:     int(p.CurrentTime),
		LastUpdate:  fmtTime(p.LastUpdate),
		FinishedAt:  fmtTime(p.FinishedAt),
		Hidden:      p.HideFromContinueListening,
		ProgressID:  p.ID,
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
	Duration  string           `json:"duration,omitempty"`
	SizeMB    int64            `json:"size_mb,omitempty"`
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

func summarize(it *abs.Item) itemSummary {
	m := &it.Media.Metadata
	s := itemSummary{
		ID:        it.ID,
		Type:      it.MediaType,
		Title:     m.Title,
		Subtitle:  m.Subtitle,
		Author:    m.AuthorDisplay(),
		Narrator:  m.NarratorDisplay(),
		Series:    m.SeriesDisplay(),
		Year:      m.PublishedYear.String(),
		Publisher: m.Publisher,
		Genres:    m.Genres,
		Tags:      it.Media.Tags,
		Language:  m.Language,
		ASIN:      m.ASIN,
		ISBN:      m.ISBN,
		Duration:  fmtDuration(it.Media.Duration),
		SizeMB:    mb(it.SizeBytes()),
		Tracks:    it.Media.NumTracks,
		Chapters:  it.Media.NumChapters,
		Ebook:     it.Media.EbookFormat,
		Abridged:  m.Abridged,
		Explicit:  m.Explicit,
		NoCover:   !it.HasCover(),
		Missing:   it.IsMissing,
		Invalid:   it.IsInvalid,
		Path:      it.RelPath,
		Added:     fmtDate(it.AddedAt),
		Progress:  progressOf(it.UserMediaProgress),
	}
	if it.IsPodcast() {
		s.Episodes = it.Media.NumEpisodes
		if s.Episodes == 0 {
			s.Episodes = len(it.Media.Episodes)
		}
		s.FeedURL = m.FeedURL
		s.Year = m.ReleaseDate
	}
	// expanded shape: counts come from the slices
	if s.Tracks == 0 && len(it.Media.Tracks) > 0 {
		s.Tracks = len(it.Media.Tracks)
	}
	if s.Chapters == 0 && len(it.Media.Chapters) > 0 {
		s.Chapters = len(it.Media.Chapters)
	}
	if s.Ebook == "" && it.Media.EbookFile != nil {
		s.Ebook = it.Media.EbookFile.EbookFormat
	}

	return s
}

func summarizeAll(items []abs.Item) []itemSummary {
	out := make([]itemSummary, 0, len(items))
	for i := range items {
		out = append(out, summarize(&items[i]))
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
	Duration    string           `json:"duration,omitempty"`
	SizeMB      int64            `json:"size_mb,omitempty"`
	Description string           `json:"description,omitempty"`
	Progress    *progressSummary `json:"progress,omitempty"`
}

func summarizeEpisode(e *abs.Episode, podcastTitle string, withDescription bool) episodeSummary {
	s := episodeSummary{
		ID:        e.ID,
		PodcastID: e.LibraryItemID,
		Podcast:   podcastTitle,
		Title:     e.Title,
		Season:    e.Season.String(),
		Episode:   e.Episode.String(),
		Type:      e.EpisodeType,
		Published: fmtDate(e.PublishedAt),
		Duration:  fmtDuration(e.DurationSeconds()),
		Progress:  progressOf(e.Progress),
	}
	if s.Published == "" {
		s.Published = e.PubDate
	}
	if e.Size > 0 {
		s.SizeMB = mb(e.Size)
	} else if e.AudioFile != nil {
		s.SizeMB = mb(e.AudioFile.Metadata.Size)
	}
	if withDescription {
		s.Description = clip(plain(e.Description), descriptionCap)
	}
	return s
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
	r := libraryRow{ID: l.ID, Name: l.Name, MediaType: l.MediaType, Provider: l.Provider, LastScan: fmtTime(l.LastScan)}
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
	Listened    string `json:"listened,omitempty"     jsonschema:"time spent listening in this session"`
	Position    string `json:"position,omitempty"     jsonschema:"playback position at the end of the session"`
	Percent     int    `json:"percent,omitempty"`
	Device      string `json:"device,omitempty"`
	Started     string `json:"started,omitempty"`
	LastUpdated string `json:"last_updated,omitempty"`
}

func summarizeSession(s *abs.Session) sessionSummary {
	out := sessionSummary{
		ID:          s.ID,
		UserID:      s.UserID,
		ItemID:      s.LibraryItemID,
		EpisodeID:   s.EpisodeID,
		Title:       s.DisplayTitle,
		Author:      s.DisplayAuthor,
		Type:        s.MediaType,
		Listened:    fmtDuration(s.TimeListening),
		Position:    fmtDuration(s.CurrentTime),
		Device:      s.DeviceInfo.Describe(),
		Started:     fmtTime(s.StartedAt),
		LastUpdated: fmtTime(s.UpdatedAt),
	}
	if s.Duration > 0 {
		out.Percent = percent(s.CurrentTime / s.Duration)
	}
	if s.User != nil {
		out.User = s.User.Name
	}
	if out.Device == "" && s.MediaPlayer != "" {
		out.Device = s.MediaPlayer
	}
	return out
}
