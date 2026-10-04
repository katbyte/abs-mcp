// Package audiosample reads stretches of a book's audio from an
// Audiobookshelf server: seconds or minutes from anywhere in the book, as
// mono PCM at the rate asked for, across its files in play order. It is the
// groundwork for anything that listens to a book rather than reading its
// metadata - comparing two copies, transcribing a passage - and it holds the
// loudness envelope and the matching that compare two stretches.
//
// ffmpeg does the decoding and reads the server's file route itself, seeking
// with byte ranges, so only the stretch asked for crosses the network. The API
// key never goes on ffmpeg's command line, where ps would show it: ffmpeg is
// pointed at a proxy on 127.0.0.1, inside this process, which adds the key.
package audiosample

import (
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"os/exec"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/katbyte/abs-mcp/lib/abs"
)

// ErrNoFFmpeg is returned by New when ffmpeg is not on the PATH.
var ErrNoFFmpeg = errors.New("ffmpeg is not installed or not on the PATH: reading a book's audio needs it")

// Track is one audio file of a book: where it starts in the whole book and
// how long it runs, in seconds.
type Track struct {
	FileID   string // the file's ino, which the server's file route takes
	Start    float64
	Duration float64
}

// Book is an item's audio in play order.
type Book struct {
	ItemID string
	Tracks []Track
}

// Duration is the whole book's length in seconds.
func (b *Book) Duration() float64 {
	if len(b.Tracks) == 0 {
		return 0
	}
	last := b.Tracks[len(b.Tracks)-1]
	return last.Start + last.Duration
}

// BookOf reads an item's play order from its expanded record: the server's
// own track list, which leaves excluded files out and carries each file's
// start, or, when the item came without one, its included audio files in
// index order, which is what the server builds the list from.
func BookOf(it *abs.Item) (*Book, error) {
	if it.IsPodcast() {
		return nil, fmt.Errorf("%q is a podcast: its audio is its episodes, not one book", it.Title())
	}
	b := &Book{ItemID: it.ID}
	for _, t := range it.Media.Tracks {
		id := t.Ino
		if id == "" && t.ContentURL != "" {
			id = path.Base(t.ContentURL) // .../api/items/<id>/file/<ino>
		}
		if id == "" || t.Duration <= 0 {
			continue
		}
		b.Tracks = append(b.Tracks, Track{FileID: id, Start: t.StartOffset, Duration: t.Duration})
	}
	if len(b.Tracks) == 0 {
		files := slices.Clone(it.Media.AudioFiles)
		slices.SortStableFunc(files, func(x, y abs.AudioFile) int { return cmp.Compare(x.Index, y.Index) })
		start := 0.0
		for _, f := range files {
			if f.Exclude || f.Ino == "" || f.Duration <= 0 {
				continue
			}
			b.Tracks = append(b.Tracks, Track{FileID: f.Ino, Start: start, Duration: f.Duration})
			start += f.Duration
		}
	}
	if len(b.Tracks) == 0 {
		return nil, fmt.Errorf("%q has no audio to read", it.Title())
	}

	return b, nil
}

// Sampler decodes stretches of books through ffmpeg. It serves ffmpeg the
// server's file route through a proxy on 127.0.0.1 while it is open, so it
// must be closed. The proxy answers only under a random path, and only for
// the items this sampler was asked to read.
type Sampler struct {
	client *abs.Client
	ffmpeg string
	secret string
	base   string // the proxy's address and secret path, what ffmpeg is given
	srv    *http.Server
	served chan error // what Serve returned, once the proxy has stopped
	read   atomic.Int64
	reads  atomic.Int64 // numbers each decode, so the proxy can say which one a failure hit
	local  bool         // files on this machine, by path, with no proxy

	mu      sync.Mutex
	items   map[string]bool  // the items Read and Stream have been asked for
	failed  map[string]error // what went wrong fetching a file from the server, by decode
	stopped error            // why the proxy stopped serving, when it did before Close
}

// New starts a sampler reading through client. It fails with ErrNoFFmpeg when
// ffmpeg is not installed.
func New(ctx context.Context, client *abs.Client) (*Sampler, error) {
	bin, err := exec.LookPath("ffmpeg")
	if err != nil {
		return nil, ErrNoFFmpeg
	}
	var key [16]byte
	if _, err := rand.Read(key[:]); err != nil {
		return nil, err
	}
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("starting the local proxy ffmpeg reads through: %w", err)
	}

	s := &Sampler{
		client: client,
		ffmpeg: bin,
		secret: hex.EncodeToString(key[:]),
		items:  map[string]bool{},
		failed: map[string]error{},
		served: make(chan error, 1),
	}
	s.base = "http://" + ln.Addr().String() + "/" + s.secret
	s.srv = &http.Server{Handler: http.HandlerFunc(s.serve), ReadHeaderTimeout: 30 * time.Second}
	go func() {
		err := s.srv.Serve(smallBuffers{ln})
		if !errors.Is(err, http.ErrServerClosed) {
			s.mu.Lock()
			s.stopped = err
			s.mu.Unlock()
		}
		s.served <- err
	}()

	return s, nil
}

// smallBuffers keeps the proxy's side of each connection to ffmpeg to a
// small send buffer. ffmpeg reads the start of a file, then hangs up and asks
// for the stretch it wants; whatever the proxy has pushed into the socket by
// then was fetched from the server for nothing, and on loopback the kernel's
// own sizing lets that run to megabytes.
type smallBuffers struct{ net.Listener }

func (l smallBuffers) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if tc, ok := c.(*net.TCPConn); ok {
		_ = tc.SetWriteBuffer(64 << 10)
	}
	return c, err
}

// NewFiles is a sampler over files on this machine rather than a server's:
// each Track's FileID is a path. It is what the calibration reads a corpus
// through, and what reads a book before it is on a server. It fails with
// ErrNoFFmpeg when ffmpeg is not installed.
func NewFiles() (*Sampler, error) {
	bin, err := exec.LookPath("ffmpeg")
	if err != nil {
		return nil, ErrNoFFmpeg
	}
	return &Sampler{ffmpeg: bin, items: map[string]bool{}, failed: map[string]error{}, local: true}, nil
}

// BookFromFiles is a book made of audio files on this machine, in the order
// given, for a sampler from NewFiles: each file's length comes from ffprobe.
func BookFromFiles(ctx context.Context, id string, paths []string) (*Book, error) {
	probe, err := exec.LookPath("ffprobe")
	if err != nil {
		return nil, errors.New("ffprobe is not installed or not on the PATH: reading a book from files needs it")
	}
	b := &Book{ItemID: id}
	start := 0.0
	for _, p := range paths {
		out, err := exec.CommandContext(ctx, probe, "-v", "error", "-show_entries", "format=duration", "-of", "default=noprint_wrappers=1:nokey=1", p).Output() //nolint:gosec // ffprobe from the PATH over a path the caller chose
		if err != nil {
			return nil, fmt.Errorf("ffprobe %s: %w", p, err)
		}
		d, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
		if err != nil || d <= 0 {
			return nil, fmt.Errorf("ffprobe gives %s no length: %q", p, strings.TrimSpace(string(out)))
		}
		b.Tracks = append(b.Tracks, Track{FileID: p, Start: start, Duration: d})
		start += d
	}
	if len(b.Tracks) == 0 {
		return nil, fmt.Errorf("%q has no audio to read", id)
	}
	return b, nil
}

// Close stops the proxy, and says why it had stopped if it stopped early.
func (s *Sampler) Close() error {
	if s.local {
		return nil
	}
	err := s.srv.Close()
	if serr := <-s.served; !errors.Is(serr, http.ErrServerClosed) {
		err = errors.Join(err, fmt.Errorf("the local proxy ffmpeg reads through had stopped: %w", serr))
	}
	return err
}

// BytesRead is how many bytes of audio files the server has sent so far.
func (s *Sampler) BytesRead() int64 { return s.read.Load() }

// Read decodes length seconds of the book from start seconds in, as mono
// 16-bit PCM at rate samples a second, crossing from file to file in play
// order. ffmpeg seeks each file itself (-ss before -i), which over http is a
// ranged request, so only the stretch and the file's header are fetched;
// in an mp3 the seek lands where the file's table of contents or its bitrate
// says, which can be a second or so off. A stretch running past the end of
// the book comes back short.
func (s *Sampler) Read(ctx context.Context, b *Book, start, length float64, rate int) ([]int16, error) {
	out := make([]int16, 0, int(max(length, 0)*float64(max(rate, 0))))
	if err := s.Stream(ctx, b, start, length, rate, func(pcm []int16) error {
		out = append(out, pcm...)
		return nil
	}); err != nil {
		return nil, err
	}

	return out, nil
}

// Stream is Read handing the samples to fn as ffmpeg decodes them, a chunk
// at a time, rather than holding the whole stretch: an hour at 4 kHz is
// 29 MB. fn must not keep the slice past the call. An error from fn stops
// the read and is returned.
func (s *Sampler) Stream(ctx context.Context, b *Book, start, length float64, rate int, fn func(pcm []int16) error) error {
	if rate <= 0 || length <= 0 {
		return fmt.Errorf("a stretch needs a positive rate and length, not %d Hz for %gs", rate, length)
	}
	s.mu.Lock()
	s.items[b.ItemID] = true
	s.mu.Unlock()

	start = max(start, 0)
	end := start + length
	for _, t := range b.Tracks {
		if t.Start+t.Duration <= start || t.Start >= end {
			continue
		}
		off := max(start-t.Start, 0)
		n := min(end, t.Start+t.Duration) - t.Start - off
		if err := s.decode(ctx, b.ItemID, t.FileID, off, n, rate, fn, nil); err != nil {
			return err
		}
	}

	return nil
}

// decodeShortBy is how many seconds a file's stretch may come back short of
// what was asked before it is an error.
const decodeShortBy = 2

// Health is how a stretch of one file decoded: how much audio came back, and
// what ffmpeg complained of on the way.
type Health struct {
	Seconds    float64  // audio decoded
	Complaints int      // lines ffmpeg wrote at its error level
	First      []string // the first few of them
	Failed     string   // why ffmpeg gave up on the file, when it did
}

// checkRate is the rate a check decodes at: enough to decode every frame,
// little enough to cost nothing to hold.
const checkRate = 8000

// Check decodes length seconds of one file of an item from off seconds in
// and says how it went. What is wrong with the file - ffmpeg failing on it,
// complaining of it, or running out of it early - is in the Health; an
// error is the server or the proxy failing, which says nothing of the file.
func (s *Sampler) Check(ctx context.Context, itemID, fileID string, off, length float64) (Health, error) {
	if length <= 0 {
		return Health{}, fmt.Errorf("a check needs a positive length, not %gs", length)
	}
	s.mu.Lock()
	s.items[itemID] = true
	s.mu.Unlock()

	h := Health{}
	got := 0
	err := s.decode(ctx, itemID, fileID, max(off, 0), length, checkRate, func(pcm []int16) error {
		got += len(pcm)
		return nil
	}, &h)
	h.Seconds = float64(got) / checkRate
	return h, err
}

// decode runs ffmpeg over one file of the book, from off seconds in for n
// seconds, handing the samples to fn. With h, it is a check: what ffmpeg
// says of the file goes in h rather than coming back as an error, and only
// the server, the proxy or the context failing is one.
func (s *Sampler) decode(ctx context.Context, itemID, fileID string, off, n float64, rate int, fn func([]int16) error, h *Health) error {
	// the decode's number rides along, so what the proxy meets fetching the
	// file is this decode's to report
	read := strconv.FormatInt(s.reads.Add(1), 10)
	src := s.base + "/" + itemID + "/" + fileID + "?read=" + read
	if s.local {
		src = fileID
	}
	// -nostdin: ffmpeg otherwise reads the caller's stdin for keystrokes,
	// which in stdio mode is the MCP session itself. fastseek: without it
	// ffmpeg reaches a point in an mp3 by reading the file up to it, the whole
	// book over the network; with it, one ranged request to where the table
	// of contents or the bitrate puts it, near enough for a stretch of speech
	args := []string{"-nostdin", "-hide_banner", "-loglevel", "error", "-fflags", "+fastseek"}
	if off > 0 {
		args = append(args, "-ss", strconv.FormatFloat(off, 'f', 3, 64))
	}
	// a quarter second more than wanted, cut off below: the resampler holds
	// back its last few milliseconds, and each file's stretch would otherwise
	// come up short and the ones after it start early
	args = append(args, "-i", src, "-t", strconv.FormatFloat(n+0.25, 'f', 3, 64),
		"-vn", "-ac", "1", "-ar", strconv.Itoa(rate), "-f", "s16le", "pipe:1")

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, s.ffmpeg, args...) //nolint:gosec // ffmpeg from the PATH, over a url this process serves
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting ffmpeg: %w", err)
	}

	want := int(math.Round(n * float64(rate)))
	raw := make([]byte, 64<<10)
	pcm := make([]int16, len(raw)/2)
	var failed error
	got := 0
	for got < want && failed == nil {
		k, err := io.ReadFull(stdout, raw[:2*min(len(pcm), want-got)])
		for i := range k / 2 {
			pcm[i] = int16(binary.LittleEndian.Uint16(raw[2*i:])) //nolint:gosec // s16le: the bits are the sample
		}
		if k >= 2 {
			failed = fn(pcm[:k/2])
			got += k / 2
		}
		if err != nil {
			break // the file ended, or ffmpeg did: Wait says which
		}
	}
	if failed != nil {
		// ffmpeg is killed here, so Wait can only say it was
		cancel()
		_ = cmd.Wait()
		s.forget(read)
		return failed
	}
	_, drainErr := io.Copy(io.Discard, stdout) // the quarter second over
	waitErr := cmd.Wait()
	s.mu.Lock()
	fetch, stopped := s.failed[read], s.stopped
	delete(s.failed, read)
	s.mu.Unlock()
	msg := strings.TrimSpace(stderr.String())
	if s.base != "" {
		msg = strings.ReplaceAll(msg, s.base, "")
	}
	if h != nil {
		switch {
		case waitErr != nil && ctx.Err() != nil:
			return context.Cause(ctx)
		case fetch != nil:
			return fmt.Errorf("reading file %s of item %s: %w", fileID, itemID, fetch)
		case waitErr != nil && stopped != nil:
			return fmt.Errorf("reading file %s of item %s: the local proxy ffmpeg reads through stopped: %w", fileID, itemID, stopped)
		case drainErr != nil:
			return fmt.Errorf("reading what ffmpeg decoded from file %s of item %s: %w", fileID, itemID, drainErr)
		}
		for line := range strings.SplitSeq(msg, "\n") {
			if line = strings.TrimSpace(line); line != "" {
				h.Complaints++
				if len(h.First) < 3 {
					h.First = append(h.First, line[:min(len(line), 200)])
				}
			}
		}
		if waitErr != nil {
			h.Failed = waitErr.Error()
		}
		return nil
	}
	switch {
	case waitErr != nil && ctx.Err() != nil:
		return context.Cause(ctx)
	// a file the server refused or broke off is the reason, whether ffmpeg
	// failed on it or decoded what it got and came back short
	case fetch != nil:
		return fmt.Errorf("reading file %s of item %s: %w", fileID, itemID, fetch)
	case waitErr != nil && stopped != nil:
		return fmt.Errorf("reading file %s of item %s: the local proxy ffmpeg reads through stopped: %w", fileID, itemID, stopped)
	case waitErr != nil:
		return fmt.Errorf("ffmpeg could not decode file %s of item %s: %w: %s", fileID, itemID, waitErr, msg[:min(len(msg), 300)])
	case drainErr != nil:
		return fmt.Errorf("reading what ffmpeg decoded from file %s of item %s: %w", fileID, itemID, drainErr)
	// the stretch was cut to the file's length as the server gives it, so
	// coming back short is the file ending early: a stretch of silence
	// taken for audio would lower a comparison without a word. A second or
	// two is a seek landing late, or an encoder's padding
	case want-got > decodeShortBy*rate:
		return fmt.Errorf("file %s of item %s ended %.1fs into a %.1fs stretch starting %.1fs in, short of the length the server gives it", fileID, itemID, float64(got)/float64(rate), n, off)
	}

	return nil
}

// forget drops what the proxy recorded for a decode that is over.
func (s *Sampler) forget(read string) {
	s.mu.Lock()
	delete(s.failed, read)
	s.mu.Unlock()
}

// fail records what went wrong fetching a file for a decode, the first
// thing only.
func (s *Sampler) fail(read string, err error) {
	if read == "" {
		return // not one of ours: ffmpeg always sends the number
	}
	s.mu.Lock()
	if s.failed[read] == nil {
		s.failed[read] = err
	}
	s.mu.Unlock()
}

// readErr is a response body that keeps the error a read of it ended with,
// other than its end.
type readErr struct {
	r   io.Reader
	err error
}

func (e *readErr) Read(p []byte) (int, error) {
	n, err := e.r.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		e.err = err
	}
	return n, err
}

// serve is the proxy: GET /<secret>/<item>/<file> is the server's file route
// for that item and file, with the key added and the Range passed through.
func (s *Sampler) serve(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
	if len(parts) != 3 || subtle.ConstantTimeCompare([]byte(parts[0]), []byte(s.secret)) != 1 {
		http.NotFound(w, r)
		return
	}
	item, file := parts[1], parts[2]
	s.mu.Lock()
	known := s.items[item]
	s.mu.Unlock()
	if !known {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}

	read := r.URL.Query().Get("read")
	resp, err := s.client.ItemFileRange(r.Context(), item, file, r.Header.Get("Range"))
	if err != nil {
		status := http.StatusBadGateway
		if he, ok := errors.AsType[*abs.HTTPError](err); ok {
			status = he.Status
		}
		// a range past the end is ffmpeg probing, not the server refusing;
		// and a request ffmpeg gave up on is not the server failing
		if status != http.StatusRequestedRangeNotSatisfiable && r.Context().Err() == nil {
			s.fail(read, err)
		}
		http.Error(w, http.StatusText(status), status)
		return
	}
	defer func() { _ = resp.Body.Close() }()

	for _, h := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges", "Last-Modified", "Etag"} {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	// ffmpeg hangs up once it has its stretch, which ends the copy with a
	// write that fails or a read cancelled with the request: neither is the
	// server's doing. A read failing while ffmpeg still waits is, and the
	// audio it was reading comes back short
	body := &readErr{r: resp.Body}
	n, _ := io.Copy(w, body) // a failed write is ffmpeg hanging up; a failed read is kept in body.err
	s.read.Add(n)
	if body.err != nil && r.Context().Err() == nil {
		s.fail(read, fmt.Errorf("the server's reply broke off after %d bytes: %w", n, body.err))
	}
}
