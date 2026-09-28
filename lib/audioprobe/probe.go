// Package audioprobe reads what an audio file's container says about it, a
// few kilobytes at a time: which container it really is, whatever its
// extension says, and whether its audio is locked to a store's player. The
// server's own probe names the codec, and a FairPlay book from the iTunes
// store reads there as plain AAC: the lock shows only in the container, as
// the sample description "drms" where a playable file says "mp4a".
//
// It never decodes anything and needs no ffmpeg. A file is read through a
// Fetch, which the caller points at the server's file route with byte
// ranges, so a long book costs its header and, when the header sits at the
// end, a few more small reads.
package audioprobe

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
)

// Fetch reads up to n bytes of a file from off, and says how long the whole
// file is. Fewer bytes come back only at the end of the file.
type Fetch func(ctx context.Context, off, n int64) (data []byte, size int64, err error)

// Containers, as Report names them.
const (
	MP4  = "mp4"  // MPEG-4 / QuickTime: m4a, m4b, mp4, aax
	MP3  = "mp3"  // MPEG audio, with or without an ID3 tag in front
	AAC  = "aac"  // raw AAC in ADTS frames
	FLAC = "flac" // FLAC
	Ogg  = "ogg"  // Ogg: Vorbis, Opus, or FLAC in Ogg
	WAV  = "wav"  // RIFF WAVE
	ASF  = "asf"  // Windows Media: wma
	MKV  = "mkv"  // Matroska / WebM
	AIFF = "aiff" // AIFF
	CAF  = "caf"  // Apple Core Audio Format
)

const (
	id3Tag           = "id3"  // what sniff says of an ID3 tag, which is not yet a container
	moovBox          = "moov" // the MP4 box that describes the tracks
	encrypted        = "encrypted"
	commonEncryption = "Common Encryption"
	windowsDRM       = "Windows Media DRM"
)

// Report is what a file's container says.
type Report struct {
	// Container is what the file holds, read from its first bytes; "" when
	// they match nothing known.
	Container string
	// Locked says how the audio is locked, and is "" for audio any player
	// can play: "Apple FairPlay (drms)".
	Locked string
	// Size is the file's length in bytes, and Want what its container says
	// it should be when that is more: a download or a copy that broke off.
	// Want is 0 for a file that is whole, or whose container does not say.
	Size, Want int64
}

// block is how much one read asks for. What the walk needs is box headers
// of 8 or 16 bytes and the first kilobyte or so of a moov, so a small block
// costs a read or two more on an odd file and saves most of every read on
// the rest: 64 KB a read was 3 GB over one library.
const block = 8 << 10

// maxReads is how many reads one file may take before the walk gives up: a
// header scattered over more than this is a file broken beyond the question.
const maxReads = 64

// ErrTooScattered is returned when a file's header needs more reads than a
// sane one does.
var ErrTooScattered = fmt.Errorf("%w: its header is scattered past what a readable file needs", ErrMalformed)

// ErrMalformed is what every error the file itself causes wraps: a header
// that makes no sense, or one this reading cannot follow. Any other error is
// the read failing, which says nothing of the file.
var ErrMalformed = errors.New("the file cannot be read as audio")

// Probe reads a file's container and whether its audio is locked.
func Probe(ctx context.Context, fetch Fetch) (Report, error) {
	r := &reader{fetch: fetch}
	head, err := r.at(ctx, 0, 64)
	if err != nil {
		return Report{}, err
	}
	switch c := sniff(head); c {
	case MP4:
		rep := Report{Container: MP4, Size: r.size}
		rep.Locked, rep.Want, err = r.mp4(ctx)
		return rep, err
	case ASF:
		locked, err := r.asf(ctx)
		return Report{Container: ASF, Locked: locked, Size: r.size}, err
	case id3Tag:
		// an ID3 tag is only a tag: what follows it is the file. Its size is
		// four syncsafe bytes, seven bits each, then a footer when flagged
		n := int64(head[6]&0x7f)<<21 | int64(head[7]&0x7f)<<14 | int64(head[8]&0x7f)<<7 | int64(head[9]&0x7f)
		n += 10
		if head[5]&0x10 != 0 {
			n += 10
		}
		after, err := r.at(ctx, n, 16)
		if err != nil {
			return Report{}, err
		}
		c = sniff(after)
		if c == MP4 || c == ASF {
			// every offset in the file is from its own start, which the tag
			// has moved: no player takes it, and its lock cannot be read
			return Report{Container: c, Size: r.size}, fmt.Errorf("%w: an ID3 tag in front of a %s file", ErrMalformed, c)
		}
		if c == "" || c == id3Tag {
			// a tag with nothing recognisable after it, most often padding
			// before the first frame: an mp3 all the same
			c = MP3
		}
		return Report{Container: c, Size: r.size}, nil
	default:
		return Report{Container: c, Size: r.size}, nil
	}
}

var (
	asfHeader = []byte{0x30, 0x26, 0xB2, 0x75, 0x8E, 0x66, 0xCF, 0x11, 0xA6, 0xD9, 0x00, 0xAA, 0x00, 0x62, 0xCE, 0x6C}
	// the objects in an ASF header that say its content is encrypted:
	// Content Encryption (Windows Media DRM 7), Extended Content Encryption
	// (DRM 9 and 10), and Advanced Content Encryption (PlayReady), as their
	// GUIDs sit on disk
	asfLocks = [][]byte{
		{0xFB, 0xB3, 0x11, 0x22, 0x23, 0xBD, 0xD2, 0x11, 0xB4, 0xB7, 0x00, 0xA0, 0xC9, 0x55, 0xFC, 0x6E},
		{0x14, 0xE6, 0x8A, 0x29, 0x22, 0x26, 0x17, 0x4C, 0xB9, 0x35, 0xDA, 0xE0, 0x7E, 0xE9, 0x28, 0x9C},
		{0x33, 0x85, 0x05, 0x43, 0x81, 0x69, 0xE6, 0x49, 0x9B, 0x74, 0xAD, 0x12, 0xCB, 0x86, 0xD5, 0x8C},
	}
	// the boxes that can open an MP4 file
	mp4First = map[string]bool{"ftyp": true, moovBox: true, "mdat": true, "free": true, "skip": true, "wide": true, "pnot": true, "uuid": true}
	// and the ones that sit at its top level after that
	topLevel = map[string]bool{
		"ftyp": true, moovBox: true, "mdat": true, "free": true, "skip": true, "wide": true, "pnot": true, "uuid": true,
		"udta": true, "meta": true, "moof": true, "mfra": true, "sidx": true, "styp": true, "pdin": true, "prft": true, "emsg": true, "ssix": true,
	}
)

// sniff names the container its first bytes open, "id3" for an ID3 tag,
// which says nothing yet, or "" for nothing known.
func sniff(b []byte) string {
	switch {
	case len(b) >= 8 && mp4First[string(b[4:8])]:
		return MP4
	case bytes.HasPrefix(b, []byte("ID3")) && len(b) >= 10:
		return id3Tag
	case bytes.HasPrefix(b, []byte("fLaC")):
		return FLAC
	case bytes.HasPrefix(b, []byte("OggS")):
		return Ogg
	case len(b) >= 12 && bytes.HasPrefix(b, []byte("RIFF")) && string(b[8:12]) == "WAVE":
		return WAV
	case len(b) >= 12 && bytes.HasPrefix(b, []byte("FORM")) && (string(b[8:12]) == "AIFF" || string(b[8:12]) == "AIFC"):
		return AIFF
	case bytes.HasPrefix(b, []byte("caff")):
		return CAF
	case bytes.HasPrefix(b, asfHeader):
		return ASF
	case bytes.HasPrefix(b, []byte{0x1A, 0x45, 0xDF, 0xA3}):
		return MKV
	case len(b) >= 2 && b[0] == 0xFF && b[1]&0xE0 == 0xE0:
		// a frame sync: the layer bits say MPEG audio, or, when zero, AAC
		// in ADTS frames
		if b[1]&0x06 == 0 {
			return AAC
		}
		return MP3
	}
	return ""
}

// reader reads a file a block at a time from wherever the walk needs it,
// keeping what it read: a read starting at the box it is after, not at a
// round offset before it, has the whole of a moov's first kilobytes in one.
type reader struct {
	fetch   Fetch
	extents []extent
	size    int64 // the file's length, known after the first read
	reads   int
}

// extent is a stretch of the file already read.
type extent struct {
	off  int64
	data []byte
}

// at is n bytes from off, fewer at the end of the file.
func (r *reader) at(ctx context.Context, off, n int64) ([]byte, error) {
	if off < 0 || n < 0 {
		return nil, fmt.Errorf("a read at %d for %d bytes", off, n)
	}
	out := make([]byte, 0, n)
	for n > 0 {
		if r.reads > 0 && off >= r.size {
			break
		}
		var have []byte
		for _, e := range r.extents {
			if off >= e.off && off < e.off+int64(len(e.data)) {
				have = e.data[off-e.off:]
				break
			}
		}
		if have == nil {
			if r.reads >= maxReads {
				return nil, ErrTooScattered
			}
			b, size, err := r.fetch(ctx, off, max(block, n))
			if err != nil {
				return nil, err
			}
			r.reads++
			r.size = size
			if len(b) == 0 {
				break // the file ends here
			}
			r.extents = append(r.extents, extent{off: off, data: b})
			have = b
		}
		k := min(n, int64(len(have)))
		out = append(out, have[:k]...)
		off += k
		n -= k
	}
	return out, nil
}

// box is one MP4 box: where it starts, its full size, its header's length
// and its type.
type box struct {
	off, size, hdr int64
	typ            string
}

func (b box) end() int64  { return b.off + b.size }
func (b box) body() int64 { return b.off + b.hdr }

// errBroken is a box that claims a size it cannot have.
var errBrokenASF = fmt.Errorf("%w: a Windows Media header shorter than its own fields", ErrMalformed)

var errBroken = fmt.Errorf("%w: a box in its header claims an impossible size", ErrMalformed)

// boxAt reads the header of the box at off, which must end by limit; a box
// of size 0 runs to limit.
func (r *reader) boxAt(ctx context.Context, off, limit int64) (box, error) {
	return r.boxTo(ctx, off, limit, limit)
}

// boxTo is boxAt with a box of size 0 running to to, not limit.
func (r *reader) boxTo(ctx context.Context, off, to, limit int64) (box, error) {
	h, err := r.at(ctx, off, 16)
	if err != nil {
		return box{}, err
	}
	if len(h) < 8 {
		return box{}, errBroken
	}
	b := box{off: off, size: int64(binary.BigEndian.Uint32(h)), hdr: 8, typ: string(h[4:8])}
	switch b.size {
	case 0: // to the end of what holds it
		b.size = to - off
	case 1: // a 64-bit size follows the type
		if len(h) < 16 {
			return box{}, errBroken
		}
		b.size, b.hdr = int64(binary.BigEndian.Uint64(h[8:])), 16 //nolint:gosec // checked against limit below
	}
	if b.size < b.hdr || b.size > limit-b.off {
		return box{}, errBroken
	}
	return b, nil
}

// children walks the boxes from start to end, handing each to fn until it
// says stop.
func (r *reader) children(ctx context.Context, start, end int64, fn func(b box) (stop bool, err error)) error {
	for off := start; off+8 <= end; {
		b, err := r.boxAt(ctx, off, end)
		if err != nil {
			return err
		}
		stop, err := fn(b)
		if err != nil || stop {
			return err
		}
		off = b.end()
	}
	return nil
}

// child is the first box of a type between start and end.
func (r *reader) child(ctx context.Context, start, end int64, typ string) (box, bool, error) {
	var found box
	ok := false
	err := r.children(ctx, start, end, func(b box) (bool, error) {
		if b.typ == typ {
			found, ok = b, true
		}
		return ok, nil
	})
	return found, ok, err
}

// lockedEntries are the sample descriptions a locked track carries in place
// of its codec's, the codec moved inside to an frma box.
var lockedEntries = map[string]string{
	"drms": "Apple FairPlay (drms)",
	"aavd": "Audible (aavd)",
	"enca": encrypted,
}

// schemes names the protection schemes an schm box can carry.
var schemes = map[string]string{
	"itun": "Apple FairPlay",
	"cenc": commonEncryption,
	"cens": commonEncryption,
	"cbc1": commonEncryption,
	"cbcs": commonEncryption,
	"adrm": "Audible",
}

// mp4 walks an MP4 file to each track's sample descriptions and says how
// the first locked one is locked, or "" when none is, and how long the file
// should be when its last box runs past its end. The moov box holding the
// descriptions comes first in a file made for streaming and last in most
// others; the walk reads only box headers until it reaches it.
func (r *reader) mp4(ctx context.Context) (locked string, want int64, err error) {
	var moov box
	found := false
	// every top-level box, not only as far as the moov: a file cut short
	// is cut in its last box, which is most often the mdat after it. Past
	// the moov, what does not read as a box ends the walk and is no cut:
	// a tag stuck on the end, padding, or a fragmented file's hundred
	// fragments, which say nothing of the lock
	afterMoov := 0 // reads spent past the moov, looking for a cut
	for off := int64(0); off+8 <= r.size; {
		if found && r.reads-afterMoov >= maxAfterMoov {
			break // a fragmented file's fragments: whether it is cut stays unknown
		}
		// a box may run past the end here, which is the file cut short
		b, berr := r.boxTo(ctx, off, r.size, 1<<62)
		if berr != nil {
			if found && errors.Is(berr, ErrMalformed) {
				break
			}
			return "", 0, berr
		}
		if !topLevel[b.typ] {
			if found {
				break
			}
			// an old QuickTime file's own boxes before the moov, "junk",
			// "PICT", are passed over like any box; bytes that are no box
			// type at all are no MP4
			if !printable(b.typ) {
				return "", 0, fmt.Errorf("%w: an MP4 file with %q where a box should be", ErrMalformed, b.typ)
			}
		}
		if b.end() > r.size {
			// the last box runs past the end: the file broke off. Whatever
			// came before it may still be read
			want = b.end()
			if b.typ != moovBox {
				break
			}
		}
		if b.typ == moovBox && !found {
			moov, found, afterMoov = b, true, r.reads
			moov.size = min(moov.size, r.size-moov.off)
		}
		off = b.end()
	}
	if !found {
		if want > 0 {
			return "", want, nil // cut off before the moov: that says enough
		}
		return "", 0, fmt.Errorf("%w: an MP4 file with no moov box, so nothing says what its audio is", ErrMalformed)
	}
	// the first sound track says it: the chapter track after it sits behind
	// the audio's tables, a read further on, and cannot be locked itself
	err = r.children(ctx, moov.body(), moov.end(), func(trak box) (bool, error) {
		if trak.typ != "trak" {
			return false, nil
		}
		mdia, ok, cerr := r.child(ctx, trak.body(), trak.end(), "mdia")
		if cerr != nil || !ok {
			return false, cerr
		}
		kind, cerr := r.handler(ctx, mdia)
		if cerr != nil {
			return false, cerr
		}
		if kind != "" && kind != "soun" {
			return false, nil // a chapter, text or cover track
		}
		stsd, ok, cerr := r.path(ctx, mdia, "minf", "stbl", "stsd")
		if cerr != nil || !ok {
			return false, cerr
		}
		locked, cerr = r.entries(ctx, stsd)
		return true, cerr
	})
	if want > 0 && errors.Is(err, errBroken) {
		err = nil // a moov cut short breaks off inside: the cut is the finding
	}
	return locked, want, err
}

// maxAfterMoov is how many reads the walk spends past the moov looking for
// a box that runs past the end: the mdat after it in a whole file, and a
// fragmented file's first fragments, not all hundred of them.
const maxAfterMoov = 3

// printable reports whether a box type is four printable characters, as
// every real one is.
func printable(typ string) bool {
	for _, c := range []byte(typ) {
		if c < 0x20 || c > 0x7e {
			return false
		}
	}
	return len(typ) == 4
}

// handler is the kind of track an mdia box describes, "soun" for audio,
// from its hdlr box: a full box, four bytes unused, then the kind. "" when
// it has none.
func (r *reader) handler(ctx context.Context, mdia box) (string, error) {
	hdlr, ok, err := r.child(ctx, mdia.body(), mdia.end(), "hdlr")
	if err != nil || !ok {
		return "", err
	}
	b, err := r.at(ctx, hdlr.body()+8, 4)
	if err != nil || len(b) < 4 {
		return "", err
	}
	return string(b), nil
}

// path follows a chain of first children down from b.
func (r *reader) path(ctx context.Context, b box, types ...string) (box, bool, error) {
	for _, typ := range types {
		next, ok, err := r.child(ctx, b.body(), b.end(), typ)
		if err != nil || !ok {
			return box{}, false, err
		}
		b = next
	}
	return b, true, nil
}

// entries reads an stsd box's sample descriptions and says how the first
// locked one is locked.
func (r *reader) entries(ctx context.Context, stsd box) (string, error) {
	start := stsd.body() + 8 // version, flags and the entry count
	locked := ""
	err := r.children(ctx, start, stsd.end(), func(e box) (bool, error) {
		kind, isLocked := lockedEntries[e.typ]
		sinf, err := r.sinf(ctx, e)
		if err != nil {
			return false, err
		}
		if !isLocked && sinf == "" {
			return false, nil
		}
		// the schm box names the scheme better than the entry does:
		// "encrypted (Apple FairPlay, itun)"
		switch {
		case sinf != "" && isLocked && kind != encrypted:
			locked = kind
		case sinf != "":
			locked = sinf
		default:
			locked = kind
		}
		return true, nil
	})
	return locked, err
}

// sinf finds a sample description's protection box and names its scheme:
// "Apple FairPlay (itun)", "encrypted (cbcs)", or "encrypted" when the box
// says no more; "" when there is none. An audio description's own fields
// run 28 bytes past its header in version 0, and 16 or 36 more in the
// QuickTime versions 1 and 2, before its child boxes.
func (r *reader) sinf(ctx context.Context, e box) (string, error) {
	h, err := r.at(ctx, e.body(), 28)
	if err != nil {
		return "", err
	}
	if len(h) < 28 {
		return "", nil // too short to be an audio description at all
	}
	start := e.body() + 28
	switch binary.BigEndian.Uint16(h[8:10]) {
	case 1:
		start += 16
	case 2:
		start += 36
	}
	if start > e.end() {
		return "", nil
	}
	sinf, ok, err := r.child(ctx, start, e.end(), "sinf")
	switch {
	// a description this reading does not fit (a codec's own layout) is not
	// locked for having no sinf where audio's would be; a failed read is
	// the server's, and says nothing about the file
	case errors.Is(err, errBroken):
		return "", nil
	case err != nil:
		return "", err
	case !ok:
		return "", nil
	}
	schm, ok, err := r.child(ctx, sinf.body(), sinf.end(), "schm")
	switch {
	case errors.Is(err, errBroken), err == nil && !ok:
		return encrypted, nil // a sinf alone says the track is locked
	case err != nil:
		return "", err
	}
	b, err := r.at(ctx, schm.body(), 8)
	if err != nil {
		return "", err
	}
	if len(b) < 8 {
		return encrypted, nil
	}
	scheme := string(b[4:8]) // after the full box's version and flags
	if name, ok := schemes[scheme]; ok {
		return fmt.Sprintf("%s (%s)", name, scheme), nil
	}
	return fmt.Sprintf("encrypted (%s)", scheme), nil
}

// asf reads a Windows Media file's header object and says whether it holds
// one of the objects that encrypt its content. The header is the file's
// first object, its size eight bytes after its GUID; a broken one is read
// no further than a megabyte.
func (r *reader) asf(ctx context.Context) (string, error) {
	h, err := r.at(ctx, 16, 8)
	if err != nil {
		return "", err
	}
	if len(h) < 8 {
		return "", errBroken
	}
	size := min(int64(binary.LittleEndian.Uint64(h)), 1<<20) //nolint:gosec // capped
	if size < 30 {
		return "", errBrokenASF // shorter than the header object's own fields
	}
	header, err := r.at(ctx, 0, size)
	if err != nil {
		return "", err
	}
	for _, guid := range asfLocks {
		if bytes.Contains(header, guid) {
			return windowsDRM, nil
		}
	}
	return "", nil
}
