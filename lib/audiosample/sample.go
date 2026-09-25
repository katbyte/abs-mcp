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
	read   atomic.Int64

	mu      sync.Mutex
	items   map[string]bool  // the items Read and Stream have been asked for
	refused map[string]error // the server's answer when it would not serve a file, by item/file
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
		client:  client,
		ffmpeg:  bin,
		secret:  hex.EncodeToString(key[:]),
		items:   map[string]bool{},
		refused: map[string]error{},
	}
	s.base = "http://" + ln.Addr().String() + "/" + s.secret
	s.srv = &http.Server{Handler: http.HandlerFunc(s.serve), ReadHeaderTimeout: 30 * time.Second}
	go func() { _ = s.srv.Serve(smallBuffers{ln}) }()

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

// Close stops the proxy.
func (s *Sampler) Close() error { return s.srv.Close() }

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
		if err := s.decode(ctx, b.ItemID, t.FileID, off, n, rate, fn); err != nil {
			return err
		}
	}

	return nil
}

// decode runs ffmpeg over one file of the book, from off seconds in for n
// seconds, handing the samples to fn.
func (s *Sampler) decode(ctx context.Context, itemID, fileID string, off, n float64, rate int, fn func([]int16) error) error {
	src := s.base + "/" + itemID + "/" + fileID
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
	for got := 0; got < want && failed == nil; {
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
		cancel()
		_ = cmd.Wait()
		return failed
	}
	_, _ = io.Copy(io.Discard, stdout) // the quarter second over
	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		s.mu.Lock()
		refused := s.refused[itemID+"/"+fileID]
		s.mu.Unlock()
		if refused != nil {
			return fmt.Errorf("reading file %s of item %s: %w", fileID, itemID, refused)
		}
		msg := strings.TrimSpace(strings.ReplaceAll(stderr.String(), s.base, ""))
		return fmt.Errorf("ffmpeg could not decode file %s of item %s: %w: %s", fileID, itemID, err, msg[:min(len(msg), 300)])
	}

	return nil
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

	resp, err := s.client.ItemFileRange(r.Context(), item, file, r.Header.Get("Range"))
	if err != nil {
		status := http.StatusBadGateway
		if he, ok := errors.AsType[*abs.HTTPError](err); ok {
			status = he.Status
		}
		// a range past the end is ffmpeg probing, not the server refusing
		if status != http.StatusRequestedRangeNotSatisfiable {
			s.mu.Lock()
			s.refused[item+"/"+file] = err
			s.mu.Unlock()
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
	// ffmpeg hangs up once it has its stretch, which ends the copy
	n, _ := io.Copy(w, resp.Body)
	s.read.Add(n)
}
