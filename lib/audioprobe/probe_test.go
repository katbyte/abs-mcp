package audioprobe

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"slices"
	"strings"
	"testing"
)

// bx builds an MP4 box around its body.
func bx(typ string, body ...[]byte) []byte {
	b := bytes.Join(body, nil)
	out := binary.BigEndian.AppendUint32(nil, uint32(8+len(b))) //nolint:gosec // test sizes are small
	return append(append(out, typ...), b...)
}

// full is a full box's version and flags.
func full() []byte { return make([]byte, 4) }

// audioEntry is an audio sample description of a type, in QuickTime version
// v, with child boxes after its fields.
func audioEntry(typ string, v uint16, children ...[]byte) []byte {
	extra := map[uint16]int{1: 16, 2: 36}[v]
	fields := make([]byte, 28+extra)
	binary.BigEndian.PutUint16(fields[8:], v)
	return bx(typ, append([][]byte{fields}, children...)...)
}

// sinf is a protection box around an mp4a track: the original format and
// a scheme.
func sinf(scheme string) []byte {
	parts := [][]byte{bx("frma", []byte("mp4a"))}
	if scheme != "" {
		parts = append(parts, bx("schm", full(), []byte(scheme), make([]byte, 4)))
	}
	return bx("sinf", parts...)
}

// moov is a movie box with one track per sample description, each a sound
// track but a "text" description's, which is a chapter track.
func moov(entries ...[]byte) []byte {
	traks := make([][]byte, 0, len(entries))
	for _, e := range entries {
		kind := "soun"
		if string(e[4:8]) == "text" {
			kind = "text"
		}
		stsd := bx("stsd", full(), binary.BigEndian.AppendUint32(nil, 1), e)
		traks = append(traks, bx("trak", bx("tkhd", full(), make([]byte, 80)), bx("mdia", bx("mdhd", full(), make([]byte, 20)), bx("hdlr", full(), make([]byte, 4), []byte(kind), make([]byte, 13)), bx("minf", bx("smhd", full(), make([]byte, 4)), bx("stbl", stsd, bx("stts", full(), make([]byte, 4)))))))
	}
	return bx("moov", append([][]byte{bx("mvhd", full(), make([]byte, 96))}, traks...)...)
}

// fairPlay is what an iTunes store book's lock reads as.
const fairPlay = "Apple FairPlay (drms)"

var ftyp = bx("ftyp", []byte("M4B "), make([]byte, 4), []byte("M4B mp42isom"))

// file serves a byte slice as a Fetch, counting the reads.
type file struct {
	data  []byte
	reads int
	fail  func(off int64) error
}

func (f *file) fetch(_ context.Context, off, n int64) (data []byte, size int64, err error) {
	f.reads++
	if f.fail != nil {
		if ferr := f.fail(off); ferr != nil {
			return nil, 0, ferr
		}
	}
	size = int64(len(f.data))
	if off >= size {
		return nil, size, nil
	}
	return f.data[off:min(off+n, size)], size, nil
}

func probeBytes(t *testing.T, data []byte) (rep Report, reads int, err error) {
	t.Helper()
	f := &file{data: data}
	rep, err = Probe(t.Context(), f.fetch)
	return rep, f.reads, err
}

func TestMP4Locks(t *testing.T) {
	t.Parallel()

	mdat := func(n int) []byte { return bx("mdat", make([]byte, n)) }
	cover := bx("udta", bx("meta", full(), bx("ilst", bx("covr", make([]byte, 300<<10)))))
	cases := []struct {
		name   string
		data   []byte
		locked string
	}{
		{"a playable m4b, header first", bytes.Join([][]byte{ftyp, moov(audioEntry("mp4a", 0)), mdat(1000)}, nil), ""},
		{"a playable m4b, header last behind a long book", bytes.Join([][]byte{ftyp, mdat(3 << 20), moov(audioEntry("mp4a", 0))}, nil), ""},
		{"an iTunes FairPlay book", bytes.Join([][]byte{ftyp, moov(audioEntry("drms", 0, bx("esds", full()), sinf("itun"))), mdat(1000)}, nil), fairPlay},
		{"an iTunes FairPlay book, header last", bytes.Join([][]byte{ftyp, mdat(3 << 20), moov(audioEntry("drms", 0, sinf("itun")))}, nil), fairPlay},
		{"an Audible aax", bytes.Join([][]byte{ftyp, moov(audioEntry("aavd", 0, bx("adrm", make([]byte, 60)))), mdat(1000)}, nil), "Audible (aavd)"},
		{"common encryption", bytes.Join([][]byte{ftyp, moov(audioEntry("enca", 0, sinf("cenc"))), mdat(10)}, nil), "Common Encryption (cenc)"},
		{"encrypted with no scheme said", bytes.Join([][]byte{ftyp, moov(audioEntry("enca", 0)), mdat(10)}, nil), "encrypted"},
		{"an unknown scheme", bytes.Join([][]byte{ftyp, moov(audioEntry("enca", 0, sinf("zzzz"))), mdat(10)}, nil), "encrypted (zzzz)"},
		{"a QuickTime version 1 description, locked", bytes.Join([][]byte{ftyp, moov(audioEntry("enca", 1, sinf("cbcs"))), mdat(10)}, nil), "Common Encryption (cbcs)"},
		{"a QuickTime version 2 description, playable", bytes.Join([][]byte{ftyp, moov(audioEntry("mp4a", 2, bx("wave", make([]byte, 20)))), mdat(10)}, nil), ""},
		{"a cover bigger than a read ahead of the tracks", bytes.Join([][]byte{ftyp, bx("moov", bx("mvhd", full(), make([]byte, 96)), cover, moov(audioEntry("drms", 0))[8:]), mdat(10)}, nil), fairPlay},
		{"a chapter track first, the locked audio second", bytes.Join([][]byte{ftyp, moov(bx("text", make([]byte, 40)), audioEntry("drms", 0)), mdat(10)}, nil), fairPlay},
		{"a track with no handler, locked", bytes.Join([][]byte{ftyp, bx("moov", bx("trak", bx("mdia", bx("minf", bx("stbl", bx("stsd", full(), binary.BigEndian.AppendUint32(nil, 1), audioEntry("drms", 0))))))), mdat(10)}, nil), fairPlay},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			rep, reads, err := probeBytes(t, c.data)
			if err != nil {
				t.Fatal(err)
			}
			if rep.Container != MP4 || rep.Locked != c.locked || rep.Want != 0 || rep.Size != int64(len(c.data)) {
				t.Errorf("= %+v, want an mp4 locked %q, whole, %d bytes", rep, c.locked, len(c.data))
			}
			// the header, and at most the box headers on the way to it
			if reads > 8 {
				t.Errorf("took %d reads, want a handful", reads)
			}
		})
	}
}

// A book with its header last, the chapter track behind the audio's
// tables: the start and the header's first read say it all.
func TestTwoReads(t *testing.T) {
	t.Parallel()

	m := moov(audioEntry("mp4a", 0, bx("stsz", make([]byte, 400<<10))), bx("text", make([]byte, 40)))
	for _, data := range [][]byte{
		bytes.Join([][]byte{ftyp, bx("mdat", make([]byte, 3<<20)), m}, nil),
		bytes.Join([][]byte{ftyp, m, bx("mdat", make([]byte, 3<<20))}, nil),
	} {
		rep, reads, err := probeBytes(t, data)
		if err != nil || rep.Locked != "" || reads != 2 {
			t.Errorf("= %+v, %v in %d reads; want clean in 2", rep, err, reads)
		}
	}
}

// A 64-bit mdat size is read as one, so the moov behind a book over 4 GB
// is still found.
func TestMP4LargeSize(t *testing.T) {
	t.Parallel()

	body := make([]byte, 1000)
	mdat := append(binary.BigEndian.AppendUint32(nil, 1), "mdat"...)
	mdat = binary.BigEndian.AppendUint64(mdat, uint64(16+len(body)))
	mdat = append(mdat, body...)
	rep, _, err := probeBytes(t, bytes.Join([][]byte{ftyp, mdat, moov(audioEntry("drms", 0))}, nil))
	if err != nil || rep.Locked != fairPlay {
		t.Errorf("= %+v, %v; want the drms behind a 64-bit mdat", rep, err)
	}
}

// A file whose last box runs past its end broke off, and says how long it
// should be; what came before still says whether it is locked.
func TestMP4CutShort(t *testing.T) {
	t.Parallel()

	whole := bytes.Join([][]byte{ftyp, moov(audioEntry("drms", 0)), bx("mdat", make([]byte, 5000))}, nil)
	rep, _, err := probeBytes(t, whole[:len(whole)-3000])
	if err != nil || rep.Want != int64(len(whole)) || rep.Size != int64(len(whole)-3000) || rep.Locked != fairPlay {
		t.Errorf("mdat cut = %+v, %v; want %d wanted of %d, still locked", rep, err, len(whole), len(whole)-3000)
	}

	last := bytes.Join([][]byte{ftyp, bx("mdat", make([]byte, 5000)), moov(audioEntry("mp4a", 0))}, nil)
	rep, _, err = probeBytes(t, last[:len(last)-100])
	if err != nil || rep.Want != int64(len(last)) || rep.Locked != "" {
		t.Errorf("moov cut = %+v, %v; want %d wanted and no error", rep, err, len(last))
	}

	gone := bytes.Join([][]byte{ftyp, bx("mdat", make([]byte, 5000))}, nil)
	rep, _, err = probeBytes(t, gone[:len(gone)-10])
	if err != nil || rep.Want != int64(len(gone)) {
		t.Errorf("cut before the moov = %+v, %v; want %d wanted", rep, err, len(gone))
	}
}

// A header that is nonsense is an error, never a clean file.
func TestMP4Broken(t *testing.T) {
	t.Parallel()

	if _, _, err := probeBytes(t, bytes.Join([][]byte{ftyp, bx("mdat", make([]byte, 10))}, nil)); err == nil || !strings.Contains(err.Error(), "no moov") {
		t.Errorf("no moov = %v, want it said", err)
	}
	// a trak claiming more than its moov holds
	m := moov(audioEntry("mp4a", 0))
	binary.BigEndian.PutUint32(m[8+108:], 1<<20) // the trak after mvhd
	if _, _, err := probeBytes(t, bytes.Join([][]byte{ftyp, m}, nil)); !errors.Is(err, errBroken) {
		t.Errorf("an impossible trak = %v, want errBroken", err)
	}
	// a box of size 4, shorter than its own header
	if _, _, err := probeBytes(t, append(append([]byte(nil), ftyp...), 0, 0, 0, 4, 'm', 'o', 'o', 'v')); !errors.Is(err, errBroken) {
		t.Errorf("a box shorter than its header = %v, want errBroken", err)
	}
}

// A read that fails is the caller's error at every step of the walk, never
// a file taken for playable: the server failing says nothing about the file.
func TestReadFailuresSurface(t *testing.T) {
	t.Parallel()

	boom := errors.New("the server went away")
	// the header last, and a codec box a read long between the description
	// and its sinf, so the walk, the description and the lock each need a
	// read of their own
	data := bytes.Join([][]byte{ftyp, bx("mdat", make([]byte, 3<<20)), moov(audioEntry("enca", 0, bx("esds", make([]byte, block+100)), sinf("itun")))}, nil)
	var offsets []int64
	clean := &file{data: data, fail: func(off int64) error {
		offsets = append(offsets, off)
		return nil
	}}
	if _, err := Probe(t.Context(), clean.fetch); err != nil || len(offsets) < 3 {
		t.Fatalf("a clean walk = %v in %v; want a few reads", err, offsets)
	}
	for _, at := range offsets {
		f := &file{data: data, fail: func(off int64) error {
			if off == at {
				return boom
			}
			return nil
		}}
		if rep, err := Probe(t.Context(), f.fetch); !errors.Is(err, boom) {
			t.Errorf("a failed read at %d = %+v, %v; want the failure", at, rep, err)
		}
	}
}

// A header scattered over more reads than a readable file needs stops
// rather than reading the whole book.
func TestScatteredStops(t *testing.T) {
	t.Parallel()

	parts := make([][]byte, 0, maxReads+12)
	parts = append(parts, ftyp)
	for range maxReads + 10 {
		parts = append(parts, bx("free", make([]byte, block)))
	}
	parts = append(parts, moov(audioEntry("mp4a", 0)))
	if _, reads, err := probeBytes(t, bytes.Join(parts, nil)); !errors.Is(err, ErrTooScattered) || reads > maxReads {
		t.Errorf("= %v after %d reads, want ErrTooScattered within %d", err, reads, maxReads)
	}
}

func TestContainers(t *testing.T) {
	t.Parallel()

	id3 := func(size int, footer bool) []byte {
		h := []byte{'I', 'D', '3', 4, 0, 0, byte(size >> 21 & 0x7f), byte(size >> 14 & 0x7f), byte(size >> 7 & 0x7f), byte(size & 0x7f)}
		if footer {
			h[5] = 0x10
			size += 10
		}
		return slices.Concat(h, make([]byte, size))
	}
	frame := []byte{0xFF, 0xFB, 0x90, 0x00}
	adts := []byte{0xFF, 0xF1, 0x50, 0x80}
	asf := func(lock []byte) []byte {
		body := slices.Concat(bytes.Repeat([]byte{7}, 40), lock)
		return slices.Concat(asfHeader, binary.LittleEndian.AppendUint64(nil, uint64(24+len(body))), body)
	}
	cases := []struct {
		name, container, locked string
		data                    []byte
	}{
		{"an mp3 with an ID3 tag", MP3, "", slices.Concat(id3(300, false), frame)},
		{"an mp3 with a footer-flagged tag", MP3, "", slices.Concat(id3(20, true), frame)},
		{"an mp3 whose tag runs past the first read", MP3, "", slices.Concat(id3(200<<10, false), frame)},
		{"an ID3 tag over padding", MP3, "", slices.Concat(id3(30, false), make([]byte, 20))},
		{"a bare mp3", MP3, "", slices.Concat(frame, make([]byte, 100))},
		{"raw aac", AAC, "", slices.Concat(adts, make([]byte, 100))},
		{"an aac with an ID3 tag", AAC, "", slices.Concat(id3(10, false), adts)},
		{"flac", FLAC, "", []byte("fLaC\x00\x00\x00\x22")},
		{"flac behind an ID3 tag", FLAC, "", slices.Concat(id3(10, false), []byte("fLaC"))},
		{"ogg", Ogg, "", []byte("OggS\x00\x02")},
		{"wav", WAV, "", []byte("RIFF\x24\x00\x00\x00WAVEfmt ")},
		{"aiff", AIFF, "", []byte("FORM\x00\x00\x00\x00AIFFCOMM")},
		{"caf", CAF, "", []byte("caff\x00\x01\x00\x00")},
		{"matroska", MKV, "", []byte{0x1A, 0x45, 0xDF, 0xA3, 0, 0}},
		{"a playable wma", ASF, "", asf(nil)},
		{"a wma under DRM 7", ASF, windowsDRM, asf(asfLocks[0])},
		{"a wma under DRM 10", ASF, windowsDRM, asf(asfLocks[1])},
		{"a wma under PlayReady", ASF, windowsDRM, asf(asfLocks[2])},
		{"nothing known", "", "", []byte("hello, world, this is text")},
		{"an empty file", "", "", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			rep, _, err := probeBytes(t, c.data)
			if err != nil || rep.Container != c.container || rep.Locked != c.locked {
				t.Errorf("= %+v, %v; want %q locked %q", rep, err, c.container, c.locked)
			}
		})
	}
}

// The mp3 at the heart of the Network Effect case: an .m4b that is an mp3
// through and through reads as one.
func TestMP3NamedM4B(t *testing.T) {
	t.Parallel()

	rep, reads, err := probeBytes(t, append([]byte("ID3\x03\x00\x00\x00\x00\x02\x00"), append(make([]byte, 256), 0xFF, 0xFB, 0x90, 0x00)...))
	if err != nil || rep.Container != MP3 || reads != 1 {
		t.Errorf("= %+v, %v in %d reads; want an mp3 in one", rep, err, reads)
	}
}

// What an independent review found the walk got wrong: bytes stuck on the
// end of a whole file read as a cut, a fragmented file run out of reads,
// a box too big to add up, a tag in front of an MP4 passed over, and a
// Windows Media header too short to hold its own fields read as clean.
func TestReviewedEdges(t *testing.T) {
	t.Parallel()

	whole := bytes.Join([][]byte{ftyp, moov(audioEntry("drms", 0)), bx("mdat", make([]byte, 5000))}, nil)
	id3v1 := append([]byte("TAG"), make([]byte, 125)...)
	for name, data := range map[string][]byte{
		"an ID3v1 tag on the end":        slices.Concat(whole, id3v1),
		"8 junk bytes with size 3":       slices.Concat(whole, []byte{0, 0, 0, 3, 'x', 'y', 'z', 'w'}),
		"zeros on the end":               slices.Concat(whole, make([]byte, 64)),
		"a fragmented file, 200 moofs":   fragmented(200),
		"an old QuickTime box first":     slices.Concat(ftyp, bx("junk", make([]byte, 40)), moov(audioEntry("drms", 0)), bx("mdat", make([]byte, 10))),
		"a moov last, junk after":        slices.Concat(ftyp, bx("mdat", make([]byte, 500)), moov(audioEntry("drms", 0)), []byte("junkjunkjunk")),
		"a moov then a free box cut off": slices.Concat(ftyp, moov(audioEntry("drms", 0)), []byte{0, 0, 1, 0, 'f', 'r', 'e', 'e'}),
	} {
		rep, _, err := probeBytes(t, data)
		switch {
		case name == "a moov then a free box cut off":
			// a known box running past the end is a cut, however small
			if err != nil || rep.Want == 0 || rep.Locked != fairPlay {
				t.Errorf("%s = %+v, %v; want locked and cut", name, rep, err)
			}
		case err != nil || rep.Want != 0 || rep.Locked != fairPlay:
			t.Errorf("%s = %+v, %v; want locked, whole", name, rep, err)
		}
	}

	// a 64-bit size too big to add to its offset is broken, not a negative read
	if _, _, err := probeBytes(t, slices.Concat(ftyp, binary.BigEndian.AppendUint32(nil, 1), []byte("mdat"), binary.BigEndian.AppendUint64(nil, 1<<63-1), make([]byte, 100))); !errors.Is(err, errBroken) {
		t.Errorf("a huge box = %v, want errBroken", err)
	}

	// an ID3 tag in front of an MP4 moves every offset in it
	if rep, _, err := probeBytes(t, slices.Concat([]byte{'I', 'D', '3', 4, 0, 0, 0, 0, 0, 20}, make([]byte, 20), whole)); !errors.Is(err, ErrMalformed) || rep.Container != MP4 {
		t.Errorf("a tag in front of an MP4 = %+v, %v; want mp4 and ErrMalformed", rep, err)
	}

	// a Windows Media header claiming 0 bytes, with a lock right after it
	if _, _, err := probeBytes(t, slices.Concat(asfHeader, make([]byte, 8), asfLocks[0], make([]byte, 30))); !errors.Is(err, ErrMalformed) {
		t.Errorf("an empty Windows Media header = %v, want ErrMalformed", err)
	}

	// every error the file causes is ErrMalformed; a failed read is not
	for _, err := range []error{errBroken, errBrokenASF, ErrTooScattered} {
		if !errors.Is(err, ErrMalformed) {
			t.Errorf("%v is not ErrMalformed", err)
		}
	}
	boom := errors.New("the server went away")
	f := &file{data: whole, fail: func(int64) error { return boom }}
	if _, err := Probe(t.Context(), f.fetch); errors.Is(err, ErrMalformed) || !errors.Is(err, boom) {
		t.Errorf("a failed read = %v, want it and not ErrMalformed", err)
	}
}

// A fragmented file's fragments are not walked: whether its last one is cut
// is not worth a read per fragment.
func TestFragmentedReads(t *testing.T) {
	t.Parallel()

	rep, reads, err := probeBytes(t, fragmented(200))
	if err != nil || rep.Locked != fairPlay || reads > 2+maxAfterMoov {
		t.Errorf("= %+v, %v in %d reads; want locked in at most %d", rep, err, reads, 2+maxAfterMoov)
	}
}

// fragmented is a fragmented MP4: its moov, then n fragments.
func fragmented(n int) []byte {
	parts := make([][]byte, 0, 2+2*n)
	parts = append(parts, ftyp, moov(audioEntry("drms", 0)))
	for range n {
		parts = append(parts, bx("moof", make([]byte, 100)), bx("mdat", make([]byte, 20000)))
	}
	return bytes.Join(parts, nil)
}
