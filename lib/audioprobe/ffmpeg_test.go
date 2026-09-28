package audioprobe

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// lock renames the first audio sample description in an MP4 file from mp4a
// to drms, the one change that makes a playable file read as an iTunes
// FairPlay book: what ffprobe reports as the codec tag of Children of the
// Mind.
func lock(data []byte) []byte {
	out := bytes.Clone(data)
	if i := bytes.Index(out, []byte("stsd")); i >= 0 {
		if j := bytes.Index(out[i:], []byte("mp4a")); j >= 0 {
			copy(out[i+j:], "drms")
		}
	}
	return out
}

// Files as ffmpeg writes them, rather than as the tests above build them:
// its m4b puts the header last, and with faststart first.
func TestFFmpegFiles(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed")
	}

	dir := t.TempDir()
	gen := func(name string, args ...string) []byte {
		t.Helper()
		p := filepath.Join(dir, name)
		all := append([]string{"-nostdin", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "sine=f=440:r=8000", "-t", "3"}, args...)
		if out, err := exec.CommandContext(t.Context(), "ffmpeg", append(all, p)...).CombinedOutput(); err != nil { //nolint:gosec // ffmpeg over this test's own lavfi sources
			t.Fatalf("ffmpeg %s: %v: %s", name, err, out)
		}
		b, err := os.ReadFile(p) //nolint:gosec // a file this test just wrote
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	fetch := func(data []byte) Fetch {
		return (&file{data: data}).fetch
	}

	m4b := gen("a.m4b", "-c:a", "aac", "-metadata", "title=A Book")
	fast := gen("b.m4b", "-c:a", "aac", "-movflags", "+faststart")
	for _, c := range []struct {
		name, container, locked string
		data                    []byte
	}{
		{"an m4b", MP4, "", m4b},
		{"an m4b made for streaming", MP4, "", fast},
		{"an m4b locked", MP4, fairPlay, lock(m4b)},
		{"an m4b made for streaming, locked", MP4, fairPlay, lock(fast)},
		{"an mp3", MP3, "", gen("c.mp3", "-q:a", "9")},
		{"an mp3 with no tag", MP3, "", gen("d.mp3", "-q:a", "9", "-id3v2_version", "0")},
		{"a flac", FLAC, "", gen("e.flac")},
		{"an opus", Ogg, "", gen("f.opus", "-c:a", "libopus")},
		{"a wav", WAV, "", gen("g.wav")},
		{"raw aac", AAC, "", gen("h.aac", "-c:a", "aac")},
		{"a wma", ASF, "", gen("i.wma", "-c:a", "wmav2")},
	} {
		rep, err := Probe(context.Background(), fetch(c.data))
		if err != nil || rep.Container != c.container || rep.Locked != c.locked || rep.Want != 0 {
			t.Errorf("%s = %+v, %v; want %q locked %q", c.name, rep, err, c.container, c.locked)
		}
	}

	// and ffprobe agrees the renamed file is what an iTunes book looks like
	locked := filepath.Join(dir, "locked.m4b")
	if err := os.WriteFile(locked, lock(m4b), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := exec.LookPath("ffprobe"); err == nil {
		out, err := exec.CommandContext(t.Context(), "ffprobe", "-v", "error", "-select_streams", "a:0", "-show_entries", "stream=codec_name,codec_tag_string", "-of", "csv=p=0", locked).CombinedOutput() //nolint:gosec // this test's own file
		if err != nil || !strings.Contains(string(out), "drms") {
			t.Errorf("ffprobe of the locked m4b = %q, %v; want the drms tag", out, err)
		}
	}
}
