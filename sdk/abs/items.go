package abs

import (
	"cmp"
	"strings"
)

// This file is written by hand, beside the generated ones: small readings of a library and its items, the item's for every form the server
// sends one in.

// IsPodcast is whether the library holds podcasts rather than books.
func (l Library) IsPodcast() bool { return l.MediaType == "podcast" }

// Title is the book's or podcast's title, empty when the server sent no metadata.
func (i LibraryItemMinified) Title() string {
	if i.Media == nil || i.Media.Metadata == nil {
		return ""
	}

	return i.Media.Metadata.Title
}

// Title is the book's or podcast's title, empty when the server sent no metadata.
func (i LibraryItemExpanded) Title() string {
	if i.Media == nil || i.Media.Metadata == nil {
		return ""
	}

	return i.Media.Metadata.Title
}

// IsPodcast is whether the item is a podcast rather than a book.
func (i LibraryItemMinified) IsPodcast() bool { return i.MediaType == "podcast" }

// IsPodcast is whether the item is a podcast rather than a book.
func (i LibraryItemExpanded) IsPodcast() bool { return i.MediaType == "podcast" }

// HasCover is whether the item has a cover image.
func (i LibraryItemMinified) HasCover() bool { return i.Media != nil && i.Media.CoverPath != "" }

// SizeBytes is the item's size on disk: the item's own, or its media's when the server sent only that.
func (i LibraryItemMinified) SizeBytes() int64 {
	if i.Size > 0 || i.Media == nil {
		return int64(i.Size)
	}

	return int64(i.Media.Size)
}

// AuthorDisplay is the author as one line: a book's authors, or a podcast's author.
func (m LibraryItemMinifiedMediaMetadata) AuthorDisplay() string {
	return cmp.Or(m.AuthorName, m.Author)
}

// NarratorDisplay is a book's narrators as one line.
func (m LibraryItemMinifiedMediaMetadata) NarratorDisplay() string { return m.NarratorName }

// SeriesDisplay is a book's series, each as its name and, when it has one, its number in the series: "Discworld #3".
func (m LibraryItemMinifiedMediaMetadata) SeriesDisplay() []string {
	if m.Series != nil {
		return []string{seriesLine(m.Series.Name, m.Series.Sequence)}
	}
	if m.SeriesName == "" {
		return nil
	}

	return strings.Split(m.SeriesName, ", ")
}

// AuthorDisplay is the author as one line: a book's authors, or a podcast's author.
func (m LibraryItemExpandedMediaMetadata) AuthorDisplay() string {
	if m.AuthorName != "" || len(m.Authors) == 0 {
		return cmp.Or(m.AuthorName, m.Author)
	}

	names := make([]string, 0, len(m.Authors))
	for _, a := range m.Authors {
		names = append(names, a.Name)
	}

	return strings.Join(names, ", ")
}

// NarratorDisplay is a book's narrators as one line.
func (m LibraryItemExpandedMediaMetadata) NarratorDisplay() string {
	return cmp.Or(m.NarratorName, strings.Join(m.Narrators, ", "))
}

// AuthorDisplay is the author as one line: a book's authors, or a podcast's author.
func (m MediaMetadata) AuthorDisplay() string {
	if len(m.Authors) == 0 {
		return m.Author
	}

	return LibraryItemExpandedMediaMetadata{Authors: m.Authors}.AuthorDisplay()
}

// SeriesDisplay is a book's series, each as its name and, when it has one, its number in the series: "Discworld #3".
func (m LibraryItemExpandedMediaMetadata) SeriesDisplay() []string {
	if len(m.Series) == 0 {
		return LibraryItemMinifiedMediaMetadata{SeriesName: m.SeriesName}.SeriesDisplay()
	}

	out := make([]string, 0, len(m.Series))
	for _, s := range m.Series {
		out = append(out, seriesLine(s.Name, s.Sequence))
	}

	return out
}

func seriesLine(name, sequence string) string {
	if sequence == "" {
		return name
	}

	return name + " #" + sequence
}

// Minified is the item as a library's list sends it. The server writes the whole item by spreading the short one and adding to it, so every
// field of the short form is here to copy; the list's own additions, the series it was filtered by and the collapsed series, are not.
func (i LibraryItemExpanded) Minified() LibraryItemMinified {
	out := LibraryItemMinified{
		AddedAt:          i.AddedAt,
		BirthtimeMs:      i.BirthtimeMs,
		CtimeMs:          i.CtimeMs,
		FolderId:         i.FolderId,
		Id:               i.Id,
		Ino:              i.Ino,
		IsFile:           i.IsFile,
		IsInvalid:        i.IsInvalid,
		IsMissing:        i.IsMissing,
		LibraryId:        i.LibraryId,
		MediaType:        i.MediaType,
		MtimeMs:          i.MtimeMs,
		NumFiles:         i.NumFiles,
		OldLibraryItemId: i.OldLibraryItemId,
		Path:             i.Path,
		RecentEpisode:    i.RecentEpisode,
		RelPath:          i.RelPath,
		Size:             i.Size,
		UpdatedAt:        i.UpdatedAt,
		Media:            &LibraryItemMinifiedMedia{Metadata: &LibraryItemMinifiedMediaMetadata{}},
	}
	if i.MediaItemShare != nil {
		out.MediaItemShare = i.MediaItemShare
	}
	if i.RssFeed != nil {
		out.RssFeed = i.RssFeed
	}
	if i.Media == nil {
		return out
	}

	m := i.Media
	out.Media = &LibraryItemMinifiedMedia{
		AutoDownloadEpisodes:     m.AutoDownloadEpisodes,
		AutoDownloadSchedule:     m.AutoDownloadSchedule,
		CoverPath:                m.CoverPath,
		Duration:                 m.Duration,
		EbookFormat:              m.EbookFormat,
		Id:                       m.Id,
		LastEpisodeCheck:         m.LastEpisodeCheck,
		MaxEpisodesToKeep:        m.MaxEpisodesToKeep,
		MaxNewEpisodesToDownload: m.MaxNewEpisodesToDownload,
		NumAudioFiles:            m.NumAudioFiles,
		NumChapters:              m.NumChapters,
		NumEpisodes:              m.NumEpisodes,
		NumTracks:                m.NumTracks,
		Size:                     m.Size,
		Tags:                     m.Tags,
		Metadata:                 &LibraryItemMinifiedMediaMetadata{},
	}
	if m.Metadata == nil {
		return out
	}

	d := m.Metadata
	out.Media.Metadata = &LibraryItemMinifiedMediaMetadata{
		Abridged:          d.Abridged,
		Asin:              d.Asin,
		Author:            d.Author,
		AuthorName:        d.AuthorName,
		AuthorNameLF:      d.AuthorNameLF,
		Description:       d.Description,
		Explicit:          d.Explicit,
		FeedUrl:           d.FeedUrl,
		Genres:            d.Genres,
		ImageUrl:          d.ImageUrl,
		Isbn:              d.Isbn,
		ItunesArtistId:    d.ItunesArtistId,
		ItunesId:          d.ItunesId,
		ItunesPageUrl:     d.ItunesPageUrl,
		Language:          d.Language,
		NarratorName:      d.NarratorName,
		PublishedDate:     d.PublishedDate,
		PublishedYear:     d.PublishedYear,
		Publisher:         d.Publisher,
		ReleaseDate:       d.ReleaseDate,
		SeriesName:        d.SeriesName,
		Subtitle:          d.Subtitle,
		Title:             d.Title,
		TitleIgnorePrefix: d.TitleIgnorePrefix,
		Type:              d.Type,
	}

	return out
}
