//go:build integration

// Is it the same recording? One made-up reading, four minutes of noise shaped
// by a random run of syllables, laid out the ways a library collects copies
// of one book - re-encoded at another bitrate and 2% faster, split into
// three files - beside another reading of the same length under the same
// title. item_compare_audio reads them from the server's file route through
// its own proxy, as it would a real library, and audit_duplicates finds the
// copies its keys do not join.
package acceptance

import (
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// voiceRate is the made-up readings' sample rate: speech's own, near enough.
const voiceRate = 16000

// voice writes a reading as raw 16-bit mono: noise whose loudness follows
// syllables of 80-300 ms, the gaps between them and now and then the pause
// at a sentence's end, all from seed. One seed is one recording.
func voice(t *testing.T, seed uint64, seconds int) string {
	t.Helper()

	r := rand.New(rand.NewPCG(seed, seed))
	track := make([]float64, 0, seconds*1000) // loudness per millisecond
	for len(track) < seconds*1000 {
		level, syllable, gap := 0.3+0.7*r.Float64(), 80+r.IntN(220), 30+r.IntN(120)
		if r.IntN(8) == 0 {
			gap = 300 + r.IntN(500)
		}
		for range syllable {
			track = append(track, level)
		}
		for range gap {
			track = append(track, 0.02)
		}
	}
	pcm := make([]byte, 2*seconds*voiceRate) // s16le
	for i := range len(pcm) / 2 {
		v := int16(max(-32767, min(32767, r.NormFloat64()*6000*track[i*1000/voiceRate])))
		pcm[2*i], pcm[2*i+1] = byte(v), byte(v>>8) //nolint:gosec // the two bytes of a sample
	}
	raw := filepath.Join(t.TempDir(), "voice.raw")
	if err := os.WriteFile(raw, pcm, 0o600); err != nil {
		t.Fatal(err)
	}
	return raw
}

// encode has ffmpeg make an audio file from a raw reading: the arguments
// before the input pick the stretch, those after the encoding.
func encode(t *testing.T, raw, out string, before, after []string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(out), 0o777); err != nil {
		t.Fatal(err)
	}
	args := slices.Concat([]string{"-nostdin", "-loglevel", "error", "-y", "-f", "s16le", "-ar", "16000", "-ac", "1"}, before, []string{"-i", raw}, after, []string{out})
	if b, err := exec.CommandContext(t.Context(), "ffmpeg", args...).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg %s: %v\n%s", out, err, b)
	}
}

// Four copies of one title, three of them one recording: item_compare_audio
// calls the re-encoded, faster copy and the split copy the same as the
// original, and the other reading different; audit_duplicates offers the
// copies as candidates and keeps the other reading out of both its groups and
// its candidates, as the two name different readers. It would catch a sampler that cannot seek or cross a
// file boundary on a real server, a key that never reached the file route,
// a speed search that no longer absorbs a 2% faster copy, and candidates
// that stop joining copies or start joining readings.
func TestJourneySameRecording(t *testing.T) {
	const author = "Zzyzx Voice Author"
	const seconds = 240
	original, faster, split, other := author+"/Zzyzx Echo", author+"/Zzyzx Echo (20th Anniversary Edition)", author+"/Zzyzx Echo Parts", author+"/Zzyzx Echo (Zzyzx Reader Two)"

	s := newDiskShelf(t, "Zzyzx Same Recording Shelf", "zzyzx-same")
	one, two := voice(t, 1, seconds), voice(t, 2, seconds)
	encode(t, one, filepath.Join(s.root, original, "Zzyzx Echo.mp3"), nil, []string{"-c:a", "libmp3lame", "-b:a", "64k"})
	encode(t, one, filepath.Join(s.root, faster, "Zzyzx Echo.m4b"), nil, []string{"-af", "atempo=1.02", "-c:a", "aac", "-b:a", "32k"})
	for i, start := range []string{"0", "80", "160"} {
		encode(t, one, filepath.Join(s.root, split, fmt.Sprintf("%02d.mp3", i+1)), []string{"-ss", start, "-t", "80"}, []string{"-c:a", "libmp3lame", "-b:a", "48k"})
	}
	encode(t, two, filepath.Join(s.root, other, "Zzyzx Echo.mp3"), nil, []string{"-c:a", "libmp3lame", "-b:a", "64k"})
	s.open(t, 4)
	ids := s.ids(t)

	// each copy says who reads it, as a matched book would
	for path, edit := range map[string]map[string]any{
		original: {"title": "Zzyzx Echo", "narrators": []any{"Zzyzx Reader One"}},
		faster:   {"title": "Zzyzx Echo (20th Anniversary Edition)", "narrators": []any{"Zzyzx Reader One"}},
		split:    {"title": "Zzyzx Echo: In Three Parts", "narrators": []any{"Zzyzx Reader One"}},
		other:    {"title": "Zzyzx Echo", "narrators": []any{"Zzyzx Reader Two"}},
	} {
		edit["item"], edit["authors"] = ids[path], []any{author}
		call(t, "item_edit", edit)
	}

	t.Run("item_compare_audio hears the copies and the other reading", func(t *testing.T) {
		for _, c := range []struct {
			name, other, want string
		}{
			{"re-encoded and 2% faster", faster, "same"},
			{"split in three files", split, "same"},
			{"another reading", other, "different"},
		} {
			began := time.Now()
			out := call(t, "item_compare_audio", map[string]any{"item": ids[original], "other": ids[c.other]})
			t.Logf("%s: %v %v, median %v, %vs of audio, %v bytes, %v", c.name, out["verdict"], out["scores"], out["median"], out["audio_read_s"], out["bytes_read"], time.Since(began).Round(10*time.Millisecond))
			if out["verdict"] != c.want || text(out["meaning"]) == "" {
				t.Errorf("%s: %v, want %s", c.name, out, c.want)
			}
			if num(t, out["audio_read_s"], "audio_read_s") < 5*20 || num(t, out["bytes_read"], "bytes_read") == 0 {
				t.Errorf("%s read %vs, %v bytes: want what it read said", c.name, out["audio_read_s"], out["bytes_read"])
			}
		}
	})

	t.Run("audit_duplicates offers the copies and not the other reading", func(t *testing.T) {
		out := call(t, "audit_duplicates", map[string]any{"library": s.name})
		// the exact title and author do not make a group of two readings
		// naming different narrators: those are two recordings of one book,
		// kept on purpose, not one held twice
		if groups := rows(t, out["groups"], "groups"); len(groups) != 0 || num(t, out["total_findings"], "total_findings") != 0 {
			t.Fatalf("groups = %v, want none: the one pair sharing a title is two readings", groups)
		}

		name := map[string]string{ids[original]: "original", ids[faster]: "faster", ids[split]: "split", ids[other]: "other"}
		got := map[string]string{}
		for _, c := range rows(t, out["candidates"], "candidates") {
			var pair []string
			for _, it := range rows(t, c["items"], "items") {
				pair = append(pair, name[text(it["id"])])
			}
			slices.Sort(pair)
			got[strings.Join(pair, "+")] = text(c["why"])
		}
		want := map[string]string{
			"faster+original": "the same title, \"Zzyzx Echo\", and author; lengths 2.", // atempo 1.02, and the encoders' padding
			"original+split":  "one is a single file, the other 3",
			"faster+split":    "one is a single file, the other 3",
		}
		if len(got) != len(want) || num(t, out["total_candidates"], "total_candidates") != len(want) {
			t.Errorf("candidates = %v, want %v", got, want)
		}
		for pair, w := range want {
			if why := got[pair]; !strings.Contains(why, w) || !strings.HasSuffix(why, "confirm with item_compare_audio") {
				t.Errorf("%s: why = %q, want it to say %q and how to confirm it", pair, why, w)
			}
		}
	})
}
