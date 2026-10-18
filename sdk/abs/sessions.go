package abs

// This file is written by hand, beside the generated ones.

// Session is the session as a list of a user's own sessions sends it. A client's form is the same session with what playing it needs added, the
// tracks, the whole item and the stream, which are left out here.
func (s PlaybackSessionForClient) Session() PlaybackSession {
	return PlaybackSession{
		BookId:        s.BookId,
		Chapters:      s.Chapters,
		CoverPath:     s.CoverPath,
		CurrentTime:   s.CurrentTime,
		Date:          s.Date,
		DayOfWeek:     s.DayOfWeek,
		DeviceInfo:    s.DeviceInfo,
		DisplayAuthor: s.DisplayAuthor,
		DisplayTitle:  s.DisplayTitle,
		Duration:      s.Duration,
		EpisodeId:     EpisodeID(s.EpisodeId),
		Id:            s.Id,
		LibraryId:     s.LibraryId,
		LibraryItemId: s.LibraryItemId,
		MediaMetadata: s.MediaMetadata,
		MediaPlayer:   s.MediaPlayer,
		MediaType:     s.MediaType,
		PlayMethod:    s.PlayMethod,
		ServerVersion: s.ServerVersion,
		StartTime:     s.StartTime,
		StartedAt:     s.StartedAt,
		TimeListening: s.TimeListening,
		UpdatedAt:     s.UpdatedAt,
		User:          s.User,
		UserId:        s.UserId,
	}
}
