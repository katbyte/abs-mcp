package tools

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"math"
	"path"
	"slices"
	"strings"

	"github.com/katbyte/abs-mcp/lib/abs"
)

// The files inside a book: its audio tracks in play order, which of its
// ebooks is the main one, one file taken out, and all its audio merged into
// one m4b. The server's route for each has a trap. The track route rebuilds
// the book's audio from the files it is sent, so a file left out of the list
// falls out of the book; and it leaves the chapters where they were. The
// ebook route ignores what it is asked and flips the file it is given between
// main and supplementary, so the same call twice undoes itself, and flipping
// the main ebook leaves the book with none. The file route leaves the book's
// length as it was when the file is audio. The merge always re-encodes, and
// moves the files it merged out of the folder.

// trackEdge is how far a chapter may reach past the edge of the track it sits
// in, in seconds: the scanner cuts one chapter per file at the file's
// duration, and a stored duration is rounded.
const trackEdge = 0.5

// fileNamed finds the file a caller named: by its path in the item's folder,
// or by its filename where no other file shares it (a disc folder can hold
// "01.mp3" twice).
func fileNamed(what, name string, files []abs.FileMetadata) (int, error) {
	if i := slices.IndexFunc(files, func(f abs.FileMetadata) bool { return f.RelPath == name }); i >= 0 {
		return i, nil
	}
	var hits []int
	for i, f := range files {
		if f.Filename == name {
			hits = append(hits, i)
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		return -1, fmt.Errorf("no %s named %q; the book has %s", what, name, fileList(files))
	default:
		paths := make([]string, 0, len(hits))
		for _, i := range hits {
			paths = append(paths, fmt.Sprintf("%q", files[i].RelPath))
		}
		return -1, fmt.Errorf("%d %ss are named %q; name one by its path: %s", len(hits), what, name, strings.Join(paths, ", "))
	}
}

// fileList names files for an error, by path, the first twenty.
func fileList(files []abs.FileMetadata) string {
	if len(files) == 0 {
		return "none"
	}
	names := make([]string, 0, min(len(files), 20))
	for _, f := range files[:min(len(files), 20)] {
		names = append(names, fmt.Sprintf("%q", cmp.Or(f.RelPath, f.Filename)))
	}
	if len(files) > 20 {
		names = append(names, fmt.Sprintf("and %d more", len(files)-20))
	}
	return strings.Join(names, ", ")
}

// playOrder is a book's audio files as they play: the ones not excluded, by
// index.
func playOrder(files []abs.AudioFile) []abs.AudioFile {
	out := slices.DeleteFunc(slices.Clone(files), func(af abs.AudioFile) bool { return af.Exclude })
	slices.SortStableFunc(out, func(a, b abs.AudioFile) int { return cmp.Compare(a.Index, b.Index) })
	return out
}

// trackOrder is what the track route is to be sent for the files named, in
// the order named: every audio file of the book, each keeping whether it is
// excluded. It refuses a list that leaves a file out, because the server
// would drop that file from the book, and one that names a file twice.
func trackOrder(it *abs.Item, names []string) ([]abs.TrackOrder, []abs.AudioFile, error) {
	files := it.Media.AudioFiles
	metas := make([]abs.FileMetadata, len(files))
	for i := range files {
		metas[i] = files[i].Metadata
	}

	listed := make([]bool, len(files))
	order := make([]abs.TrackOrder, 0, len(names))
	after := make([]abs.AudioFile, 0, len(names))
	for _, name := range names {
		i, err := fileNamed("audio file", name, metas)
		if err != nil {
			return nil, nil, err
		}
		if listed[i] {
			return nil, nil, fmt.Errorf("tracks lists %q twice", cmp.Or(metas[i].RelPath, metas[i].Filename))
		}
		listed[i] = true
		order = append(order, abs.TrackOrder{Ino: files[i].Ino, Exclude: files[i].Exclude})
		after = append(after, files[i])
	}

	var left []abs.FileMetadata
	for i := range files {
		if !listed[i] {
			left = append(left, metas[i])
		}
	}
	if len(left) > 0 {
		return nil, nil, fmt.Errorf("tracks leaves out %s: the server drops every audio file it is not sent from the book, so list all %d, in play order (item_get files=true)", fileList(left), len(files))
	}

	return order, after, nil
}

// trackNames names files in order by their path in the item's folder.
func trackNames(files []abs.AudioFile) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, cmp.Or(f.Metadata.RelPath, f.Metadata.Filename))
	}
	return out
}

// sameTracks reports whether two play orders play the same files in the same
// order.
func sameTracks(a, b []abs.AudioFile) bool {
	return slices.EqualFunc(a, b, func(x, y abs.AudioFile) bool { return x.Ino == y.Ino })
}

// chaptersMoved moves each chapter with the track it sits inside, keeping its
// title and its place within the track. ok is false when a chapter runs
// across two tracks or lies outside the audio: chapters not cut along the
// files cannot follow them. before and after are the files in play order.
func chaptersMoved(chapters []abs.Chapter, before, after []abs.AudioFile) ([]abs.Chapter, bool) {
	type span struct{ start, length float64 }
	offsets := func(files []abs.AudioFile) map[string]span {
		out := map[string]span{}
		at := 0.0
		for _, f := range files {
			out[f.Ino] = span{at, f.Duration}
			at += f.Duration
		}
		return out
	}
	was, now := offsets(before), offsets(after)

	type placed struct {
		ch    abs.Chapter
		order int
	}
	moved := make([]placed, 0, len(chapters))
	for n, ch := range chapters {
		home := slices.IndexFunc(before, func(f abs.AudioFile) bool {
			s := was[f.Ino]
			return s.length > 0 && ch.Start >= s.start-trackEdge && ch.Start < s.start+s.length && ch.End <= s.start+s.length+trackEdge
		})
		if home < 0 {
			return nil, false
		}
		from, to := was[before[home].Ino], now[before[home].Ino]
		ch.Start = to.start + min(max(ch.Start-from.start, 0), from.length)
		ch.End = to.start + min(max(ch.End-from.start, 0), from.length)
		moved = append(moved, placed{ch, n})
	}
	slices.SortStableFunc(moved, func(a, b placed) int {
		return cmp.Or(cmp.Compare(a.ch.Start, b.ch.Start), cmp.Compare(a.order, b.order))
	})

	out := make([]abs.Chapter, len(moved))
	for i, p := range moved {
		p.ch.ID = i
		out[i] = p.ch
	}
	return out, true
}

// ebookFlip is the ebook file the ebook route has to be sent for name to be
// the book's main ebook, or for none to be when name is "none"; nil when that
// is already so. main is what the main ebook will then be.
func ebookFlip(it *abs.Item, name string) (flip *abs.LibraryFile, main string, err error) {
	var ebooks []abs.LibraryFile
	for _, f := range it.LibraryFiles {
		if f.FileType == "ebook" {
			ebooks = append(ebooks, f)
		}
	}
	current := -1
	if ef := it.Media.EbookFile; ef != nil {
		// the route is given a file of the folder's, and the main ebook is
		// only flipped back to supplementary through its own
		if current = slices.IndexFunc(ebooks, func(f abs.LibraryFile) bool { return f.Ino == ef.Ino }); current < 0 {
			return nil, "", fmt.Errorf("%q's main ebook %q is not among the files in its folder; item_rescan it first", it.Title(), mainEbook(it))
		}
	}

	if strings.EqualFold(name, "none") {
		if current < 0 {
			return nil, "none", nil
		}
		return &ebooks[current], "none", nil
	}

	if len(ebooks) == 0 {
		return nil, "", fmt.Errorf("%q has no ebook files", it.Title())
	}
	metas := make([]abs.FileMetadata, len(ebooks))
	for i := range ebooks {
		metas[i] = ebooks[i].Metadata
	}
	i, err := fileNamed("ebook file", name, metas)
	if err != nil {
		return nil, "", err
	}
	main = cmp.Or(metas[i].RelPath, metas[i].Filename)
	if i == current {
		return nil, main, nil
	}
	return &ebooks[i], main, nil
}

// mainEbook names a book's main ebook by its path in the folder, or none.
func mainEbook(it *abs.Item) string {
	ef := it.Media.EbookFile
	if ef == nil {
		return "none"
	}
	return cmp.Or(ef.Metadata.RelPath, ef.Metadata.Filename, ef.Ino)
}

// subfolderPath is a file's path in the item's folder when that says more
// than its name: the file is in a folder of its own.
func subfolderPath(f abs.FileMetadata) string {
	if f.RelPath == f.Filename {
		return ""
	}
	return f.RelPath
}

// fileToDelete is the file of a book that item_delete may take out on its
// own, or why it may not. An audio file is refused because the server takes
// it out of the book but leaves the book's length as it was, and no scan
// corrects it afterwards: a scan rebuilds the audio only when the files on
// disk and on the record differ in number, and after the delete they agree.
// The cover is refused because the server keeps pointing at it.
func fileToDelete(it *abs.Item, name string) (*abs.LibraryFile, error) {
	switch {
	case it.IsPodcast():
		return nil, errors.New("a podcast's files are its episodes: podcast_episode_delete takes one out")
	case it.IsFile:
		return nil, fmt.Errorf("%q is a single file, not a folder of them: item_delete it without file", it.Title())
	}
	metas := make([]abs.FileMetadata, len(it.LibraryFiles))
	for i := range it.LibraryFiles {
		metas[i] = it.LibraryFiles[i].Metadata
	}
	i, err := fileNamed("file", name, metas)
	if err != nil {
		return nil, err
	}
	f := &it.LibraryFiles[i]
	rel := cmp.Or(f.Metadata.RelPath, f.Metadata.Filename)
	switch {
	case f.FileType == "audio" || slices.ContainsFunc(it.Media.AudioFiles, func(af abs.AudioFile) bool { return af.Ino == f.Ino }):
		return nil, fmt.Errorf("%q is an audio file: the server would take it out of the book but leave the book's length as it was, and no scan corrects that afterwards. Take it out of the folder on disk, then item_rescan the book, which does", rel)
	case it.Media.CoverPath != "" && it.Media.CoverPath == f.Metadata.Path:
		return nil, fmt.Errorf("%q is the book's cover, and the server would go on pointing at it: item_cover_edit another cover, or remove, first", rel)
	}
	return f, nil
}

// fileGone asks the server whether a file it was told to delete is off the
// disk: the delete route takes the file out of the book even when removing
// it from disk fails. The path check answers exists for a missing path inside
// an item's folder too, naming the item, so only exists with no item named is
// a file still there.
func fileGone(ctx context.Context, client *abs.Client, lib *abs.Library, it *abs.Item, f *abs.LibraryFile) (bool, error) {
	i := slices.IndexFunc(lib.Folders, func(fo abs.Folder) bool { return fo.ID == it.FolderID })
	if i < 0 {
		return false, fmt.Errorf("the library %q has no folder %s, which %q is in", lib.Name, it.FolderID, it.Title())
	}
	exists, holder, err := client.PathExists(ctx, lib.Folders[i].FullPath, path.Join(it.RelPath, f.Metadata.RelPath))
	if err != nil {
		return false, err
	}
	return !exists || holder != "", nil
}

// m4bPlan is what merging a book into one m4b does: the server encodes the
// files that play, in play order, into a file named after the folder, and
// moves the originals out of the folder into its cache.
type m4bPlan struct {
	Files     []string `json:"files"          jsonschema:"the audio files merged, in play order"`
	Into      string   `json:"into"           jsonschema:"the file the book plays afterwards, in its folder"`
	Bitrate   int      `json:"bitrate_kbps"`
	Channels  int      `json:"channels"`
	Originals string   `json:"originals"      jsonschema:"where the merged files go: the server's cache, which a cache purge empties"`
	Left      []string `json:"left,omitempty" jsonschema:"excluded audio files, left in the folder as they are"`
}

// m4bQuality bounds what an m4b is encoded at: the server encodes AAC.
const (
	m4bMinKbps = 16
	m4bMaxKbps = 320
)

// planM4B is the merge of a book into one m4b, or why it cannot be merged.
// The server always encodes, at 128k stereo unless told otherwise, so the
// book's own bitrate and channels are the default: a mono 64k book would
// come out twice the size, and a better one worse.
func planM4B(it *abs.Item, kbps, channels int) (*m4bPlan, error) {
	play := playOrder(it.Media.AudioFiles)
	switch {
	case it.IsPodcast():
		return nil, errNotBook
	case it.IsFile:
		return nil, fmt.Errorf("%q is one file at the library root: merging names the m4b after it, so the file the book is changes and the book loses its record and everyone's progress on it. Put it in a folder of its own first", it.Title())
	case len(play) == 0:
		return nil, fmt.Errorf("%q has no audio that plays", it.Title())
	case len(play) == 1 && strings.EqualFold(path.Ext(play[0].Metadata.Filename), ".m4b"):
		return nil, fmt.Errorf("%q is already one m4b, %q", it.Title(), play[0].Metadata.Filename)
	}

	plan := &m4bPlan{Files: trackNames(play), Into: path.Base(it.Path) + ".m4b", Originals: "the server's cache for the book, metadata/cache/items/" + it.ID}
	for _, af := range play {
		plan.Bitrate = max(plan.Bitrate, int((af.BitRate+999)/1000))
		plan.Channels = max(plan.Channels, af.Channels)
	}
	if kbps > 0 {
		plan.Bitrate = kbps
	}
	if channels > 0 {
		plan.Channels = channels
	}
	switch {
	case plan.Bitrate < m4bMinKbps || plan.Bitrate > m4bMaxKbps:
		return nil, fmt.Errorf("a bitrate of %dk is outside %d to %d; pass bitrate_kbps", plan.Bitrate, m4bMinKbps, m4bMaxKbps)
	case plan.Channels != 1 && plan.Channels != 2:
		return nil, fmt.Errorf("%d channels: an audiobook is 1 or 2; pass channels", plan.Channels)
	}

	// the server renames the merged file into place over whatever is there
	for _, af := range it.Media.AudioFiles {
		if af.Exclude {
			plan.Left = append(plan.Left, cmp.Or(af.Metadata.RelPath, af.Metadata.Filename))
		}
	}
	merged := func(ino string) bool {
		return slices.ContainsFunc(play, func(af abs.AudioFile) bool { return af.Ino == ino })
	}
	for _, f := range it.LibraryFiles {
		if f.Metadata.RelPath == plan.Into && !merged(f.Ino) {
			return nil, fmt.Errorf("the folder already holds %q, which the merge would overwrite: move it or rename it first", plan.Into)
		}
	}
	return plan, nil
}

// mergedInto reports whether a book now plays the one m4b a merge made, at
// the length it had before: an encode can pad or trim its start by a frame,
// so within a second or half a percent.
func mergedInto(it *abs.Item, into string, before float64) error {
	play := playOrder(it.Media.AudioFiles)
	if len(play) != 1 || play[0].Metadata.RelPath != into {
		return fmt.Errorf("it plays %s, not %q", strings.Join(trackNames(play), ", "), into)
	}
	if d := math.Abs(play[0].Duration - before); d > max(1, before*0.005) {
		return fmt.Errorf("%q is %s long, the files it was made from %s", into, fmtDuration(play[0].Duration), fmtDuration(before))
	}
	return nil
}

// mergeRunning is the server's m4b merge of an item still in its task list.
func mergeRunning(tasks []abs.Task, itemID string) bool {
	return slices.ContainsFunc(tasks, func(t abs.Task) bool {
		return t.Action == "encode-m4b" && !t.IsFinished && t.Data["libraryItemId"] == itemID
	})
}
