package tools

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path"
	"slices"
	"strings"
	"sync"

	"github.com/katbyte/abs-mcp/lib/audioprobe"
	"github.com/katbyte/abs-mcp/lib/audiosample"
	"github.com/katbyte/abs-mcp/sdk/abs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// What audit_unplayable finds, worst first. The server lists a book whose
// audio is locked to a store's player as a book like any other - its probe
// names the codec, and a FairPlay book from the iTunes store reads as plain
// AAC - so nothing else says it will never play.
const (
	unplayLocked   = "locked"          // locked to a store's player
	unplayOriginal = "original_only"   // no audio but a locked store original beside it
	unplayUnread   = "unreadable"      // the server could not read the file
	unplayCut      = "cut_short"       // the file broke off: a download or a copy
	unplayDamaged  = "damaged"         // decode: ffmpeg could not play a stretch cleanly
	unplayExt      = "wrong_extension" // plays, but its name says another format
)

var unplayRank = map[string]int{unplayLocked: 0, unplayOriginal: 1, unplayUnread: 2, unplayCut: 3, unplayDamaged: 4, unplayExt: 5}

// extContainer is the container a file's extension promises.
var extContainer = map[string]string{
	".m4a": audioprobe.MP4, ".m4b": audioprobe.MP4, ".mp4": audioprobe.MP4, ".m4p": audioprobe.MP4, ".mov": audioprobe.MP4,
	".aax": audioprobe.MP4, ".aaxc": audioprobe.MP4,
	".mp3": audioprobe.MP3, ".mp2": audioprobe.MP3,
	".aac":  audioprobe.AAC,
	".flac": audioprobe.FLAC,
	".ogg":  audioprobe.Ogg, ".oga": audioprobe.Ogg, ".opus": audioprobe.Ogg,
	".wav": audioprobe.WAV,
	".wma": audioprobe.ASF, ".asf": audioprobe.ASF,
	".webm": audioprobe.MKV, ".mka": audioprobe.MKV,
	".aif": audioprobe.AIFF, ".aiff": audioprobe.AIFF,
	".caf": audioprobe.CAF,
}

// extCodecs are the codecs a container that cannot be locked holds under its
// own extension; one of these needs no reading. Anything else under the name
// - aac in an ".mp3" - is read to see what the file is.
var extCodecs = map[string][]string{
	".mp3":  {"mp3", "mp2", "mp1"},
	".mp2":  {"mp3", "mp2", "mp1"},
	".flac": {"flac"},
	".ogg":  {"vorbis", "opus", "flac", "speex"},
	".oga":  {"vorbis", "opus", "flac", "speex"},
	".opus": {"opus"},
	".aac":  {"aac"},
}

// lockedOriginals are the store downloads the server does not take for
// audio at all, and so lists as other files: a book holding one and no
// audio is waiting to be converted.
var lockedOriginals = map[string]string{
	".aax":  "an Audible original (.aax)",
	".aaxc": "an Audible original (.aaxc)",
	".aa":   "an old Audible original (.aa)",
	".m4p":  "an iTunes protected file (.m4p)",
}

// probeWorkers is how many files' headers are read at once, and
// checkWorkers how many stretches ffmpeg decodes at once. The server answers
// a file request in a steady time whatever else it is doing, about 55 a
// second on the library this was measured on, so more at once only makes
// each one wait longer.
const (
	probeWorkers = 8
	checkWorkers = 4
)

// unplayableUncheckedCap caps the unchecked rows; the count is whole.
const unplayableUncheckedCap = 50

type unplayableIn struct {
	Library string `json:"library,omitempty" jsonschema:"library name or id; default every library"`
	Limit   int    `json:"limit,omitempty"   jsonschema:"maximum findings to return, default 100, at most 1000"`
	Decode  bool   `json:"decode,omitempty"  jsonschema:"also play five seconds at the start, middle and end of every audio file, to find files damaged partway; needs ffmpeg where abs-mcp runs, and takes a second or so a file"`
}

type unplayableFinding struct {
	ID      string   `json:"id"`
	Title   string   `json:"title"`
	Author  string   `json:"author,omitempty"`
	Path    string   `json:"path,omitempty"`
	Problem string   `json:"problem"          jsonschema:"locked, original_only, unreadable, cut_short, damaged or wrong_extension"`
	Plays   bool     `json:"plays"            jsonschema:"true when the book plays despite it: only a wrong extension"`
	Detail  string   `json:"detail"`
	Files   []string `json:"files,omitempty"  jsonschema:"the files it is about"`
}

type unplayableSkip struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	File  string `json:"file"`
	Why   string `json:"why"`
}

type unplayableOut struct {
	Check          string              `json:"check"`
	Scanned        int                 `json:"items_scanned"`
	FilesRead      int                 `json:"files_read"                jsonschema:"audio files whose container was read: every file that can be locked, and any whose codec its name does not fit"`
	FilesPlayed    int                 `json:"files_played,omitempty"    jsonschema:"audio files played in part, with decode"`
	Found          int                 `json:"total_findings"`
	Counts         map[string]int      `json:"counts"                    jsonschema:"findings by problem"`
	Findings       []unplayableFinding `json:"findings"                  jsonschema:"worst first, capped at limit; total_findings is the real count"`
	UncheckedCount int                 `json:"unchecked_count,omitempty" jsonschema:"files that could not be read or played: whether they play is not known"`
	Unchecked      []unplayableSkip    `json:"unchecked,omitempty"       jsonschema:"the first of them, and why"`
}

func registerUnplayableAudit(r *registry) {
	client := r.client

	add(r, readTool, &mcp.Tool{
		Name: "audit_unplayable",
		Description: "Find books with audio that will not play, though the server lists them like any other: files locked to a store's player (iTunes FairPlay, Audible, Windows Media DRM), a book whose only audio is the locked Audible original waiting to be converted, files the server could not read, and files cut short by a download or copy that broke off. " +
			"Also, as plays=true, files that play but whose extension names another format (an .m4b holding mp3), which some players refuse. " +
			"It reads the start of every file that can be locked, a few kilobytes each, so it is slower than the sweeping audits, and audit_all runs it only with deep. " +
			"With decode it also plays a few seconds at the start, middle and end of every file to find damage partway, which needs ffmpeg and takes far longer. " +
			"Nothing here is fixed by an edit: a locked file is replaced with an unlocked copy or converted, a cut file fetched again.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in unplayableIn) (*mcp.CallToolResult, unplayableOut, error) {
		libs, err := resolveLibraries(ctx, client, in.Library)
		if err != nil {
			return nil, unplayableOut{}, err
		}

		var sampler *audiosample.Sampler
		if in.Decode {
			sampler, err = audiosample.New(ctx, client)
			if err != nil {
				if errors.Is(err, audiosample.ErrNoFFmpeg) {
					return nil, unplayableOut{}, errors.New("audit_unplayable with decode needs ffmpeg, which is not installed where abs-mcp runs (not on its PATH): install it there, or run without decode")
				}
				return nil, unplayableOut{}, err
			}
		}

		out := newUnplayableOut()
		var rows []unplayableFinding
		for i := range libs {
			if err = sweepUnplayable(ctx, client, &libs[i], sampler, &out, &rows); err != nil {
				break
			}
		}
		if sampler != nil {
			// the proxy stopping early is said beside whatever the sweep met
			err = errors.Join(err, sampler.Close())
		}
		if err != nil {
			return nil, unplayableOut{}, err
		}
		out.finish(rows, auditLimit(in.Limit, 100))

		return nil, out, nil
	})
}

func newUnplayableOut() unplayableOut {
	return unplayableOut{Check: "unplayable", Counts: map[string]int{}, Findings: []unplayableFinding{}}
}

// finish orders the findings worst first, then by path, and keeps limit.
func (o *unplayableOut) finish(rows []unplayableFinding, limit int) {
	slices.SortStableFunc(rows, func(a, b unplayableFinding) int {
		if ra, rb := unplayRank[a.Problem], unplayRank[b.Problem]; ra != rb {
			return ra - rb
		}
		return strings.Compare(a.Path, b.Path)
	})
	o.Found = len(rows)
	for _, r := range rows {
		o.Counts[r.Problem]++
	}
	o.Findings = rows[:min(len(rows), limit)]
}

// skip notes a file whose playability is not known, and why.
func (o *unplayableOut) skip(it *abs.Item, file, why string) {
	o.UncheckedCount++
	if len(o.Unchecked) < unplayableUncheckedCap {
		o.Unchecked = append(o.Unchecked, unplayableSkip{ID: it.ID, Title: it.Title(), File: file, Why: why})
	}
}

// sweepUnplayable adds a book library's unplayable books to rows. The
// listing picks the books; their expanded records, fifty at a time, carry
// the files; and each file that can be locked is read, a few at once.
func sweepUnplayable(ctx context.Context, client *abs.Client, lib *abs.Library, sampler *audiosample.Sampler, out *unplayableOut, rows *[]unplayableFinding) error {
	if lib.IsPodcast() {
		return nil
	}
	var ids []string
	if err := client.ItemsAll(ctx, lib.ID, abs.ItemsOptions{}, func(items []abs.Item) bool {
		for j := range items {
			if !items[j].IsPodcast() {
				ids = append(ids, items[j].ID)
			}
		}
		return true
	}); err != nil {
		return err
	}

	for chunk := range slices.Chunk(ids, embedBatchSize) {
		items, err := client.ItemsBatch(ctx, chunk)
		if err != nil {
			return err
		}
		if err := unplayableChunk(ctx, client, items, sampler, out, rows); err != nil {
			return err
		}
	}
	return nil
}

// unplayableFile is one audio file's verdict.
type unplayableFile struct {
	item   int
	file   *abs.AudioFile
	report audioprobe.Report
	read   bool     // the container was read
	health []string // what went wrong playing it, with decode
	err    error    // the file could not be read or played: unknown
}

// unplayableChunk judges a batch of expanded items.
func unplayableChunk(ctx context.Context, client *abs.Client, items []abs.Item, sampler *audiosample.Sampler, out *unplayableOut, rows *[]unplayableFinding) error {
	var probes, plays []*unplayableFile
	byItem := make([][]*unplayableFile, len(items))
	for i := range items {
		it := &items[i]
		out.Scanned++
		playable := 0
		for j := range it.Media.AudioFiles {
			af := &it.Media.AudioFiles[j]
			if af.Exclude {
				continue // left out of the book: nobody plays it
			}
			playable++
			f := &unplayableFile{item: i, file: af}
			byItem[i] = append(byItem[i], f)
			switch {
			case af.Error != "":
			case af.Ino == "":
				f.err = errors.New("the server gives the file no id, so it cannot be read")
			case needsProbe(af):
				probes = append(probes, f)
			}
		}
		if playable == 0 {
			if row, ok := lockedOriginalOnly(it); ok {
				*rows = append(*rows, row)
			}
		}
	}

	if err := eachAtOnce(ctx, len(probes), probeWorkers, func(ctx context.Context, k int) error {
		f := probes[k]
		f.report, f.err = audioprobe.Probe(ctx, audioprobe.ItemFile(client, items[f.item].ID, f.file.Ino))
		f.read = f.err == nil
		return serverFailure(ctx, items[f.item].Title(), f.err)
	}); err != nil {
		return err
	}
	for _, f := range probes {
		if f.err == nil {
			out.FilesRead++
		}
	}

	if sampler != nil {
		for _, files := range byItem {
			for _, f := range files {
				if f.err == nil && f.file.Error == "" && f.report.Locked == "" && f.report.Want == 0 {
					plays = append(plays, f)
				}
			}
		}
		if err := eachAtOnce(ctx, len(plays), checkWorkers, func(ctx context.Context, k int) error {
			f := plays[k]
			f.health, f.err = playStretches(ctx, sampler, items[f.item].ID, f.file)
			return serverFailure(ctx, items[f.item].Title(), f.err)
		}); err != nil {
			return err
		}
		for _, f := range plays {
			if f.err == nil {
				out.FilesPlayed++
			}
		}
	}

	for i := range items {
		*rows = append(*rows, judgeUnplayable(&items[i], byItem[i], out)...)
	}
	return nil
}

// serverFailure is a read's error when it is the server failing rather
// than one file: the audit stops and says so, rather than counting every
// file after an outage as unchecked and the library as clean. What one file
// causes - a header that makes no sense, a file gone from disk (404) or one
// the server could not send (500) - is that file's, and it is noted as
// unchecked.
func serverFailure(ctx context.Context, title string, err error) error {
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	if err == nil || errors.Is(err, audioprobe.ErrMalformed) {
		return nil
	}
	if he, ok := errors.AsType[*abs.HTTPError](err); ok && (he.Status == http.StatusNotFound || he.Status == http.StatusInternalServerError) {
		return nil
	}
	return fmt.Errorf("reading the files of %q: %w", title, err)
}

// needsProbe reports whether a file's container is worth reading: every one
// that can carry a lock, and any whose codec its name does not fit.
func needsProbe(af *abs.AudioFile) bool {
	ext := fileExt(af)
	switch extContainer[ext] {
	// the ones that can be locked, and a name that promises nothing: read
	// what it is
	case audioprobe.MP4, audioprobe.ASF, "":
		return true
	}
	if codecs, ok := extCodecs[ext]; ok {
		return !slices.Contains(codecs, strings.ToLower(af.Codec))
	}
	return false
}

// fileExt is a file's extension, lower-case with its dot.
func fileExt(af *abs.AudioFile) string {
	if ext := af.Metadata.Ext; ext != "" {
		return strings.ToLower(ext)
	}
	return strings.ToLower(path.Ext(af.Metadata.Filename))
}

// lockedOriginalOnly reports a book with no audio but a store's locked
// download beside it: what is left when the conversion never happened.
func lockedOriginalOnly(it *abs.Item) (unplayableFinding, bool) {
	var files, kinds []string
	for _, lf := range it.LibraryFiles {
		ext := strings.ToLower(lf.Metadata.Ext)
		if ext == "" {
			ext = strings.ToLower(path.Ext(lf.Metadata.Filename))
		}
		if kind, ok := lockedOriginals[ext]; ok && lf.FileType != "audio" {
			files = append(files, lf.Metadata.Filename)
			if !slices.Contains(kinds, kind) {
				kinds = append(kinds, kind)
			}
		}
	}
	if len(files) == 0 {
		return unplayableFinding{}, false
	}
	return unplayableRow(it, unplayOriginal, false, files,
		fmt.Sprintf("no audio the server can play, only %s: convert it to an unlocked copy with the account's key (Libation, OpenAudible) and keep the original aside", strings.Join(kinds, " and "))), true
}

// judgeUnplayable turns an item's file verdicts into findings, one per
// problem, naming the files.
func judgeUnplayable(it *abs.Item, files []*unplayableFile, out *unplayableOut) []unplayableFinding {
	var locked, unread, cut, damaged, wrongExt []*unplayableFile
	for _, f := range files {
		switch {
		case f.file.Error != "":
			unread = append(unread, f)
		case f.err != nil:
			out.skip(it, f.file.Metadata.Filename, f.err.Error())
		// read, and the start of it is no audio container known: the server
		// took something from it, but what the file holds is unknown
		case f.read && f.report.Container == "" && extContainer[fileExt(f.file)] != "":
			out.skip(it, f.file.Metadata.Filename, "its first bytes are no audio format known, though its name says "+fileExt(f.file))
		case f.report.Locked != "":
			locked = append(locked, f)
		case f.report.Want > 0:
			cut = append(cut, f)
		case len(f.health) > 0:
			damaged = append(damaged, f)
		}
		// a locked or broken file can also be misnamed, but that is the
		// least of it
		if f.err == nil && f.report.Container != "" && f.file.Error == "" && f.report.Locked == "" && f.report.Want == 0 {
			if want := extContainer[fileExt(f.file)]; want != "" && want != f.report.Container {
				wrongExt = append(wrongExt, f)
			}
		}
	}

	total := len(files)
	var rows []unplayableFinding
	if len(locked) > 0 {
		var kinds []string
		for _, f := range locked {
			if !slices.Contains(kinds, f.report.Locked) {
				kinds = append(kinds, f.report.Locked)
			}
		}
		rows = append(rows, unplayableRow(it, unplayLocked, false, names(locked),
			fmt.Sprintf("%s locked to a store's player, %s: no other player can play it, the server's included. %s", filesAre(len(locked), total), strings.Join(kinds, " and "), lockAdvice(kinds))))
	}
	if len(unread) > 0 {
		why := make([]string, 0, len(unread))
		for _, f := range unread {
			why = append(why, fmt.Sprintf("%s: %s", f.file.Metadata.Filename, f.file.Error))
		}
		rows = append(rows, unplayableRow(it, unplayUnread, false, names(unread),
			fmt.Sprintf("the server could not read %s: %s", countOf(len(unread), total), strings.Join(why, "; "))))
	}
	if len(cut) > 0 {
		what := make([]string, 0, len(cut))
		for _, f := range cut {
			what = append(what, fmt.Sprintf("%s has %s of %s (%.0f%%)", f.file.Metadata.Filename, fmtBytes(f.report.Size), fmtBytes(f.report.Want), 100*float64(f.report.Size)/float64(f.report.Want)))
		}
		rows = append(rows, unplayableRow(it, unplayCut, false, names(cut),
			fmt.Sprintf("%s cut short, a download or copy that broke off, and play stops where the file ends: %s. Fetch it again", filesAre(len(cut), total), strings.Join(what, "; "))))
	}
	if len(damaged) > 0 {
		what := make([]string, 0, len(damaged))
		for _, f := range damaged {
			what = append(what, fmt.Sprintf("%s: %s", f.file.Metadata.Filename, strings.Join(f.health, "; ")))
		}
		rows = append(rows, unplayableRow(it, unplayDamaged, false, names(damaged),
			fmt.Sprintf("%s did not play cleanly: %s. Fetch it again, or play it through to hear the damage", countOf(len(damaged), total), strings.Join(what, "; "))))
	}
	if len(wrongExt) > 0 {
		what := make([]string, 0, len(wrongExt))
		for _, f := range wrongExt {
			ext := fileExt(f.file)
			what = append(what, fmt.Sprintf("%s is named %s but holds %s", f.file.Metadata.Filename, ext, containerName(f.report.Container)))
		}
		rows = append(rows, unplayableRow(it, unplayExt, true, names(wrongExt),
			strings.Join(what, "; ")+". The server plays it, but a player that goes by the name may refuse it: rename it to what it holds, and rescan"))
	}
	return rows
}

func unplayableRow(it *abs.Item, problem string, plays bool, files []string, detail string) unplayableFinding {
	return unplayableFinding{
		ID: it.ID, Title: it.Title(), Author: it.Media.Metadata.AuthorDisplay(), Path: it.RelPath,
		Problem: problem, Plays: plays, Detail: detail, Files: files,
	}
}

func names(files []*unplayableFile) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, f.file.Metadata.Filename)
	}
	return out
}

// filesAre words how many of a book's files something is true of.
func filesAre(n, total int) string {
	switch {
	case total == 1:
		return "its file is"
	case n == total:
		return fmt.Sprintf("all %d files are", total)
	case n == 1:
		return fmt.Sprintf("1 of %d files is", total)
	}
	return fmt.Sprintf("%d of %d files are", n, total)
}

// countOf words how many of a book's files, as an object.
func countOf(n, total int) string {
	switch {
	case total == 1:
		return "its file"
	case n == total:
		return fmt.Sprintf("all %d files", total)
	}
	return fmt.Sprintf("%d of %d files", n, total)
}

// lockAdvice says what can be done about each kind of lock found.
func lockAdvice(kinds []string) string {
	var out []string
	for _, k := range kinds {
		switch {
		case strings.HasPrefix(k, "Audible"):
			out = append(out, "An Audible file converts to an unlocked copy with the account's key (Libation, OpenAudible); keep the original aside, where the server does not take it for audio.")
		case strings.HasPrefix(k, "Apple FairPlay"):
			out = append(out, "An iTunes store book stays locked: replace it with an unlocked copy.")
		default:
			out = append(out, "Replace it with an unlocked copy.")
		}
	}
	slices.Sort(out)
	return strings.Join(slices.Compact(out), " ")
}

// containerName words a container for a reader.
func containerName(c string) string {
	switch c {
	case audioprobe.MP4:
		return "MPEG-4 audio (.m4a/.m4b)"
	case audioprobe.MP3:
		return "mp3"
	case audioprobe.AAC:
		return "raw AAC (.aac)"
	case audioprobe.FLAC:
		return "FLAC"
	case audioprobe.Ogg:
		return "Ogg (.ogg/.opus)"
	case audioprobe.WAV:
		return "WAV"
	case audioprobe.ASF:
		return "Windows Media (.wma)"
	case audioprobe.MKV:
		return "Matroska (.mka/.webm)"
	case audioprobe.AIFF:
		return "AIFF"
	case audioprobe.CAF:
		return "Core Audio (.caf)"
	}
	return c
}

// fmtBytes words a size: 1.4 GB, 320 MB, 12 KB.
func fmtBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d bytes", n)
}

// playStretch is how long each played stretch runs, and a file shorter than
// three of them and their gaps is played whole.
const (
	playStretch = 5.0
	playWhole   = 20.0
	// a stretch may come back this much short before it counts: a seek
	// landing late, an encoder's padding at the end
	playShortBy = 2.0
	// and ffmpeg may complain this often in one: a seek into an mp3 can land
	// mid-frame and say so once
	playComplaints = 3
)

// playStretches plays a file at its start, middle and end and says what
// went wrong, nothing when it played cleanly. An error is the server or
// the proxy failing, which leaves the file unknown.
func playStretches(ctx context.Context, s *audiosample.Sampler, itemID string, af *abs.AudioFile) ([]string, error) {
	d := af.Duration
	if d <= 0 {
		return []string{"the server gives it no length, so there is nothing to play"}, nil
	}
	at := []float64{0, d/2 - playStretch/2, d - playStretch - 3}
	length := playStretch
	if d <= playWhole {
		at, length = []float64{0}, d
	}
	var out []string
	for i := range at {
		at[i] = max(at[i], 0)
	}
	for _, off := range at {
		h, err := s.Check(ctx, itemID, af.Ino, off, length)
		if err != nil {
			return nil, err
		}
		where := fmtDuration(off)
		switch {
		case h.Failed != "":
			why := h.Failed
			if len(h.First) > 0 {
				why = h.First[0]
			}
			out = append(out, fmt.Sprintf("at %s ffmpeg could not play it: %s", where, why))
		case h.Complaints >= playComplaints:
			out = append(out, fmt.Sprintf("at %s %d decoder errors in %.0fs (%s)", where, h.Complaints, length, h.First[0]))
		// at the end, the file stopping early: a cut, or a length the
		// server only estimated, as it does for an mp3 without a frame count
		case h.Seconds < length-playShortBy && len(at) > 1 && off == at[len(at)-1]:
			out = append(out, fmt.Sprintf("it ends about %.0fs before the %s the server gives it", length-h.Seconds+playStretch/2, fmtDuration(d)))
		case h.Seconds < length-playShortBy:
			out = append(out, fmt.Sprintf("at %s only %.1fs of %.0fs came back", where, h.Seconds, length))
		}
	}
	return out, nil
}

// eachAtOnce runs fn for each of n jobs, at most workers at a time, and
// returns the first error, which cancels the rest.
func eachAtOnce(ctx context.Context, n, workers int, fn func(ctx context.Context, i int) error) error {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	var (
		wg    sync.WaitGroup
		once  sync.Once
		first error
	)
	sem := make(chan struct{}, max(workers, 1))
	for i := range n {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
		wg.Go(func() {
			defer func() { <-sem }()
			if err := fn(ctx, i); err != nil {
				once.Do(func() {
					first = err
					cancel(err)
				})
			}
		})
	}
	wg.Wait()
	if first != nil {
		return first
	}
	// only the caller's context can have ended this one
	return context.Cause(ctx)
}
