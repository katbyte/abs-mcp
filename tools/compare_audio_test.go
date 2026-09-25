package tools

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math/rand/v2"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// The verdict on the zbooks sort's own scores: the same recordings it found,
// including the anniversary copy whose extra material sinks the last point,
// and the other narrators it found, including the one pair with a single
// point over 0.7; and between them, unsure.
func TestCompareVerdictReadsTheCalibration(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name   string
		scores []float64
		want   string
	}{
		{"Redemption of Time, split and single file", []float64{0.98, 0.98, 0.99, 1.00, 0.98}, "same"},
		{"Ender's Game and the 20th Anniversary copy", []float64{0.94, 0.89, 0.93, 0.71, 0.48}, "same"},
		{"A Planet Called Treason and Treason", []float64{0.75, 0.76, 0.84, 0.80, 0.77}, "same"},
		{"Heartfire and Heartfire (Nana Visitor)", []float64{0.38, 0.35, 0.42, 0.37, 0.40}, "different"},
		{"A Gift from Earth, Braun and Ganser", []float64{0.71, 0.44, 0.44, 0.51, 0.50}, "different"},
		{"Lucifer's Hammer, Braun and Vietor", []float64{0.52, 0.62, 0.53, 0.59, 0.60}, "different"},
		{"two points found", []float64{0.9, 0.85, 0.5, 0.45, 0.4}, "unsure"},
		{"three points found", []float64{0.9, 0.85, 0.8, 0.45, 0.4}, "unsure"},
		{"none found but every one close", []float64{0.68, 0.67, 0.69, 0.66, 0.68}, "unsure"},
	} {
		verdict, meaning := compareVerdict(c.scores)
		if verdict != c.want || meaning == "" {
			t.Errorf("%s %v = %s (%q), want %s", c.name, c.scores, verdict, meaning, c.want)
		}
	}
	if _, meaning := compareVerdict([]float64{0.4, 0.4, 0.4, 0.4, 0.4}); !strings.Contains(meaning, "none of the 5") {
		t.Errorf("no point found reads %q, want it said as none", meaning)
	}
}

// reading is a stand-in for a narrator's recording: noise whose loudness
// follows a random run of syllables, gaps and pauses from a fixed seed. One
// seed is one recording; another seed is another reader.
func reading(seed uint64, seconds, rate int) []int16 {
	r := rand.New(rand.NewPCG(seed, seed))    //nolint:gosec // a fixed seed: the same made-up reading every run
	track := make([]float64, 0, seconds*1000) // loudness per millisecond
	for len(track) < seconds*1000 {
		level, syllable, gap := 0.3+0.7*r.Float64(), 80+r.IntN(220), 30+r.IntN(120)
		if r.IntN(8) == 0 {
			gap = 300 + r.IntN(500) // the end of a sentence
		}
		for range syllable {
			track = append(track, level)
		}
		for range gap {
			track = append(track, 0.02)
		}
	}
	out := make([]int16, seconds*rate)
	for i := range out {
		out[i] = int16(max(-32767, min(32767, r.NormFloat64()*6000*track[i*1000/rate])))
	}
	return out
}

// wavRate is the made-up readings' sample rate in these tests.
const wavRate = 8000

// wav wraps mono 16-bit samples at wavRate as a wav file, which ffmpeg seeks
// by byte.
func wav(pcm []int16) []byte {
	const rate = wavRate
	var b bytes.Buffer
	le := binary.LittleEndian
	b.WriteString("RIFF")
	_ = binary.Write(&b, le, uint32(36+2*len(pcm))) //nolint:gosec // a test file of a few megabytes
	b.WriteString("WAVEfmt ")
	_ = binary.Write(&b, le, []uint32{16})
	// PCM, mono, the rate and its bytes a second, 2 bytes a frame, 16 bits
	_ = binary.Write(&b, le, []uint16{1, 1})
	_ = binary.Write(&b, le, []uint32{uint32(rate), uint32(2 * rate)})
	_ = binary.Write(&b, le, []uint16{2, 16})
	b.WriteString("data")
	_ = binary.Write(&b, le, uint32(2*len(pcm))) //nolint:gosec // a test file of a few megabytes
	_ = binary.Write(&b, le, pcm)
	return b.Bytes()
}

// audioBook serves an item whose tracks are the given files, and the files
// themselves on the file route, ranges and all.
func audioBook(f *fakeABS, id, title string, files ...[]byte) {
	tracks := make([]string, 0, len(files))
	start := 0.0
	for i, file := range files {
		ino := fmt.Sprintf("%s-%d", id, i+1)
		seconds := float64(len(file)-44) / 2 / wavRate
		tracks = append(tracks, fmt.Sprintf(`{"index":%d,"ino":%q,"startOffset":%g,"duration":%g}`, i+1, ino, start, seconds))
		start += seconds
		f.mux.HandleFunc("GET /api/items/"+id+"/file/"+ino, func(w http.ResponseWriter, r *http.Request) {
			http.ServeContent(w, r, ino+".wav", time.Time{}, bytes.NewReader(file))
		})
	}
	f.json("GET /api/items/"+id, item(id, title, `"authorName":"Zed"`, `"duration":`+fmt.Sprint(start)+`,"tracks":[`+strings.Join(tracks, ",")+`]`))
}

// Two items holding one recording, one of them split in two files, are the
// same; a recording of another reader is different; both answers say what
// they read to decide.
func TestCompareAudioTellsARecordingFromAnother(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed")
	}

	const seconds = 240
	one := reading(1, seconds, wavRate)
	f := newFakeABS(t)
	audioBook(f, "li_a", "Zed Book", wav(one))
	audioBook(f, "li_b", "Zed Book (single file)", wav(one[:len(one)/2]), wav(one[len(one)/2:]))
	audioBook(f, "li_c", "Zed Book (Other Reader)", wav(reading(2, seconds, wavRate)))
	call := toolCaller(t, f)

	same, err := call("item_compare_audio", map[string]any{"item": "li_a", "other": "li_b"})
	if err != nil {
		t.Fatal(err)
	}
	scores, ok := same["scores"].([]any)
	if !ok || same["verdict"] != "same" || len(scores) != 5 || num(t, same["audio_read_s"]) < 5*30 || num(t, same["bytes_read"]) == 0 {
		t.Errorf("one recording split in two = %v, want same, five scores, and what was read", same)
	}
	for _, s := range scores {
		if v, ok := s.(float64); !ok || v < 0.9 {
			t.Errorf("a point of one recording scored %v, want over 0.9: %v", s, same)
		}
	}
	if other, ok := same["other"].(map[string]any); !ok || num(t, other["duration_s"]) != seconds || str(t, other["title"]) != "Zed Book (single file)" {
		t.Errorf("other = %v, want its title and the %ds its tracks add up to", same["other"], seconds)
	}

	different, err := call("item_compare_audio", map[string]any{"item": "li_a", "other": "li_c"})
	if err != nil {
		t.Fatal(err)
	}
	if median, ok := different["median"].(float64); !ok || different["verdict"] != "different" || median > 0.6 {
		t.Errorf("another reader = %v, want different, scoring under 0.6", different)
	}
}

// What cannot be compared is refused before anything is read: one item
// twice, a book too short to take stretches from.
func TestCompareAudioRefusesWhatItCannotCompare(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	audioBook(f, "li_a", "Zed Book", wav(make([]int16, wavRate*60)))
	audioBook(f, "li_b", "Zed Other", wav(make([]int16, wavRate*60)))
	call := toolCaller(t, f)

	for _, c := range []struct {
		other, want string
	}{
		{"li_a", "both"},
		{"li_b", "too short to compare"},
	} {
		if _, err := call("item_compare_audio", map[string]any{"item": "li_a", "other": c.other}); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("li_a against %s = %v, want %q", c.other, err, c.want)
		}
	}
	if got := f.requests("/api/items/li_a/file/li_a-1"); len(got) != 0 {
		t.Errorf("audio was fetched for a refused comparison: %v", got)
	}
}

// Without ffmpeg the tool says what it needs and where, rather than failing
// inside a decode.
func TestCompareAudioNamesFFmpegWhenMissing(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	f := newFakeABS(t)
	audioBook(f, "li_a", "Zed Book", wav(make([]int16, wavRate*120)))
	audioBook(f, "li_b", "Zed Other", wav(make([]int16, wavRate*120)))
	call := toolCaller(t, f)

	if _, err := call("item_compare_audio", map[string]any{"item": "li_a", "other": "li_b"}); err == nil || !strings.Contains(err.Error(), "needs ffmpeg") {
		t.Errorf("with no ffmpeg = %v, want it named", err)
	}
}
