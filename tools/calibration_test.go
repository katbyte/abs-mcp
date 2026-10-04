//go:build calibration

// The by-ear check against real speech. scripts/calibration-fetch.py fetches
// LibriVox's readings of Alice's Adventures in Wonderland - six complete solo
// readings, two abridged, a full cast, a group reading, and one of the solo
// readers on Treasure Island - into ABS_CALIBRATION_DIR. This makes copies
// of two of the solo readings the ways a library collects them: re-encoded
// at another bitrate and in other codecs, played faster, pitched down,
// quieter, compressed, down a phone line, under noise, with the intro cut,
// and split into twice the files. Then every copy is compared with its
// original (the same recording) and every reading with every other (the
// same words in another voice, the hardest case: among them the same reader
// twice, years apart, and the same reader on another book), and the scores
// of each kind are reported so the thresholds in compare_audio.go can be
// read off them with a margin, and checked whenever the method changes.
//
//	scripts/calibration-fetch.py
//	ABS_CALIBRATION_DIR=~/.cache/abs-mcp/calibration go test -tags calibration -run Calibration -v -timeout 2h ./tools/
//
// ABS_CALIBRATION_POINTS, _STRETCH, _REACH and _SPEED try another
// configuration; ABS_CALIBRATION_ONLY=same,different,abridged runs some kinds;
// ABS_CALIBRATION_PARTIAL=1 leaves out a recording not all fetched yet.
package tools

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/katbyte/abs-mcp/lib/audiosample"
)

type calRecording struct {
	ID       int    `json:"id"`
	Work     string `json:"work"`
	Kind     string `json:"kind"`
	Reader   string `json:"reader"`
	Title    string `json:"title"`
	Folder   string `json:"folder"`
	Seconds  int    `json:"seconds"`
	Sections []struct {
		Number  int      `json:"number"`
		Readers []string `json:"readers"`
		Seconds int      `json:"seconds"`
	} `json:"sections"`
	Files []struct {
		Name    string  `json:"name"`
		Seconds float64 `json:"seconds"`
	} `json:"files"`
}

type calManifest struct {
	Recordings []calRecording `json:"recordings"`
}

// calBook is one copy in the corpus: a name, who reads it, and its files.
type calBook struct {
	name, reader, kind string
	paths              []string
	book               *audiosample.Book
}

// calVariant is one way a library holds a copy of a recording: a name, and
// how ffmpeg makes it from the original's files, as one file or per chapter.
type calVariant struct {
	name   string
	single bool     // the whole book in one file
	ext    string   // the file's extension
	args   []string // ffmpeg's output arguments
	before []string // ffmpeg's input arguments
	halves bool     // each chapter split in two
}

var calVariants = []calVariant{
	{name: "mp3-32k", ext: "mp3", args: []string{"-c:a", "libmp3lame", "-b:a", "32k", "-ar", "22050"}},
	{name: "m4b-aac-48k", single: true, ext: "m4b", args: []string{"-c:a", "aac", "-b:a", "48k"}},
	{name: "opus-24k", single: true, ext: "opus", args: []string{"-c:a", "libopus", "-b:a", "24k"}},
	{name: "faster-3pct", single: true, ext: "mp3", args: []string{"-af", "atempo=1.03", "-c:a", "libmp3lame", "-b:a", "64k"}},
	{name: "slower-2pct-pitched", single: true, ext: "mp3", args: []string{"-af", "aresample=44100,asetrate=43218,aresample=44100", "-c:a", "libmp3lame", "-b:a", "64k"}},
	{name: "quiet-12db", ext: "mp3", args: []string{"-af", "volume=-12dB", "-c:a", "libmp3lame", "-b:a", "64k"}},
	{name: "compressed", single: true, ext: "mp3", args: []string{"-af", "acompressor=threshold=-30dB:ratio=6:attack=5:release=50,volume=6dB", "-c:a", "libmp3lame", "-b:a", "64k"}},
	{name: "phone-8k", ext: "mp3", args: []string{"-af", "highpass=f=300,lowpass=f=3000", "-ar", "8000", "-c:a", "libmp3lame", "-b:a", "24k"}},
	{name: "noisy", single: true, ext: "mp3", args: []string{"-filter_complex", "anoisesrc=color=pink:amplitude=0.03:sample_rate=44100:seed=7[n];[0:a]aresample=44100[a];[a][n]amix=inputs=2:duration=first:normalize=0", "-c:a", "libmp3lame", "-b:a", "64k"}},
	{name: "intro-cut-300s", single: true, ext: "mp3", before: []string{"-ss", "300"}, args: []string{"-c:a", "libmp3lame", "-b:a", "64k"}},
	{name: "split-halves", ext: "mp3", halves: true, args: []string{"-c:a", "libmp3lame", "-b:a", "48k"}},
}

// calHour is how much of a reading the copies are made from: the first
// chapters adding up to at least this, so making them takes minutes.
const calHour = 3600.0

func TestCalibration(t *testing.T) {
	dir := os.Getenv("ABS_CALIBRATION_DIR")
	if dir == "" {
		t.Skip("ABS_CALIBRATION_DIR is not set: run scripts/calibration-fetch.py and point it at the corpus")
	}
	for _, bin := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s is not installed", bin)
		}
	}
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m calManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	cfg := compareDefaults
	for _, e := range []struct {
		env string
		set func(float64)
	}{
		{"ABS_CALIBRATION_POINTS", func(v float64) { cfg.Points = int(v) }},
		{"ABS_CALIBRATION_STRETCH", func(v float64) { cfg.Stretch = v }},
		{"ABS_CALIBRATION_REACH", func(v float64) { cfg.Reach = v }},
		{"ABS_CALIBRATION_SPEED", func(v float64) { cfg.SpeedPct = v }},
	} {
		if s := os.Getenv(e.env); s != "" {
			v, perr := strconv.ParseFloat(s, 64)
			if perr != nil {
				t.Fatalf("%s=%q: %v", e.env, s, perr)
			}
			e.set(v)
		}
	}
	t.Logf("config: %+v", cfg)
	ctx := t.Context()

	// the readings as fetched, whole; one not all here yet is left out
	// with ABS_CALIBRATION_PARTIAL set, and otherwise stops the run
	readings := make([]*calBook, 0, len(m.Recordings))
	byID := map[int]*calBook{}
	m.Recordings = slices.DeleteFunc(m.Recordings, func(r calRecording) bool {
		for _, f := range r.Files {
			if _, err := os.Stat(filepath.Join(dir, r.Folder, f.Name)); err != nil {
				if os.Getenv("ABS_CALIBRATION_PARTIAL") == "" {
					t.Fatalf("%s is missing %s: run scripts/calibration-fetch.py again, or set ABS_CALIBRATION_PARTIAL=1 to leave the recording out", r.Title, f.Name)
				}
				t.Logf("left out: %s (%s) is missing %s", r.Title, r.reader(), f.Name)
				return true
			}
		}
		return false
	})
	for _, r := range m.Recordings {
		b := &calBook{name: fmt.Sprintf("%d %s (%s, %s)", r.ID, r.Work, r.reader(), r.Kind), reader: r.Reader, kind: r.Kind}
		for _, f := range r.Files {
			b.paths = append(b.paths, filepath.Join(dir, r.Folder, f.Name))
		}
		readings = append(readings, b)
		byID[r.ID] = b
	}

	// the copies: two solo readings, their first hour, held eleven ways
	type pair struct {
		kind string // same, different, abridged
		a, b *calBook
	}
	var pairs []pair
	for _, id := range []int{900, 4139} {
		rec := slices.IndexFunc(m.Recordings, func(r calRecording) bool { return r.ID == id })
		if rec < 0 {
			t.Fatalf("recording %d is not in the manifest", id)
		}
		r := m.Recordings[rec]
		n, total := 0, 0.0
		for n < len(r.Files) && total < calHour {
			total += r.Files[n].Seconds
			n++
		}
		base := &calBook{name: fmt.Sprintf("%d first %d chapters (%s)", id, n, r.reader()), reader: r.Reader, kind: "solo", paths: byID[id].paths[:n]}
		variants := makeVariants(ctx, t, dir, r, base.paths)
		for _, v := range variants {
			pairs = append(pairs, pair{"same", base, v})
		}
		// two copies of one recording that are both not the original: the
		// faster and the compressed, the slower and the noisy, the m4b and
		// the split
		pairs = append(pairs, pair{"same", variants[3], variants[6]}, pair{"same", variants[4], variants[8]}, pair{"same", variants[1], variants[10]})
		// another reading against a copy, not only against the original
		for _, other := range []int{4240, 13477} {
			if byID[other] != nil {
				pairs = append(pairs, pair{"different", variants[0], byID[other]})
			}
		}
	}
	// every reading against every other: the same words in another voice
	for i := range readings {
		for j := i + 1; j < len(readings); j++ {
			kind := "different"
			if readings[i].kind == "abridged" || readings[j].kind == "abridged" {
				kind = "abridged"
			}
			pairs = append(pairs, pair{kind, readings[i], readings[j]})
		}
	}
	// the same reader twice: a chapter of the group reading against the same
	// chapter of the reader's own later recording, alone, so every stretch
	// lands in her voice saying the same words
	for _, g := range m.Recordings {
		if g.Kind != "group" {
			continue
		}
		for _, sec := range g.Sections {
			for _, solo := range m.Recordings {
				if solo.Kind != "solo" || solo.Work != g.Work || len(sec.Readers) != 1 || !strings.HasPrefix(sec.Readers[0], solo.Reader) || sec.Number > len(solo.Files) || sec.Number > len(g.Files) {
					continue
				}
				a := &calBook{name: fmt.Sprintf("%d chapter %d (%s, group)", g.ID, sec.Number, solo.Reader), reader: solo.Reader, kind: "chapter", paths: byID[g.ID].paths[sec.Number-1 : sec.Number]}
				b := &calBook{name: fmt.Sprintf("%d chapter %d (%s, solo)", solo.ID, sec.Number, solo.Reader), reader: solo.Reader, kind: "chapter", paths: byID[solo.ID].paths[sec.Number-1 : sec.Number]}
				pairs = append(pairs, pair{"different", a, b})
			}
		}
	}
	if only := os.Getenv("ABS_CALIBRATION_ONLY"); only != "" {
		kinds := strings.Split(only, ",")
		pairs = slices.DeleteFunc(pairs, func(p pair) bool { return !slices.Contains(kinds, p.kind) })
	}

	sampler, err := audiosample.NewFiles()
	if err != nil {
		t.Fatal(err)
	}
	books := map[*calBook]bool{}
	for _, p := range pairs {
		for _, b := range []*calBook{p.a, p.b} {
			if books[b] {
				continue
			}
			books[b] = true
			if b.book, err = audiosample.BookFromFiles(ctx, b.name, b.paths); err != nil {
				t.Fatal(err)
			}
		}
	}

	type row struct {
		pair
		res     compareResult
		out     compareOut
		elapsed time.Duration
	}
	rows := make([]row, len(pairs))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 3) // pairs at once, each already decoding its points in parallel
	for i, p := range pairs {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			began := time.Now()
			res, err := compareBooks(ctx, sampler, p.a.book, p.b.book, cfg)
			if err != nil {
				t.Errorf("%s vs %s: %v", p.a.name, p.b.name, err)
				return
			}
			rows[i] = row{p, res, compareOutOf(res), time.Since(began)}
		})
	}
	wg.Wait()

	// the report: every pair, then each kind's distribution
	wrong := 0
	stats := map[string]*calStats{}
	for _, r := range rows {
		if r.res.Scores == nil {
			continue
		}
		ok := true
		switch r.kind {
		case "same":
			ok = r.out.Verdict == "same"
		case "different":
			ok = r.out.Verdict == "different"
		case "abridged":
			ok = r.out.Verdict != "same"
		}
		mark := "ok  "
		if !ok {
			mark, wrong = "BAD ", wrong+1
		}
		t.Logf("%s %-9s %-8s env %v (median %.2f) spec %v (median %.2f) speed %.3f %4.0fs read %5s | %s | %s",
			mark, r.kind, r.out.Verdict, fmtScores(r.res.Scores), r.out.Median, fmtScores(r.res.Spectral), r.out.SpectralMedian, r.out.Speed, r.res.Read, r.elapsed.Round(time.Second), r.a.name, r.b.name)
		s := stats[r.kind]
		if s == nil {
			s = &calStats{}
			stats[r.kind] = s
		}
		s.add(r.res, r.out.Verdict)
	}
	for _, kind := range []string{"same", "different", "abridged"} {
		if s := stats[kind]; s != nil {
			t.Logf("%-9s %s", kind, s)
		}
	}
	if same, diff := stats["same"], stats["different"]; same != nil && diff != nil {
		t.Logf("gap: envelope, worst same point %.2f vs best different point %.2f; spectral, worst same %.2f vs best different %.2f; same medians from %.2f/%.2f, different medians to %.2f/%.2f",
			same.minEnv, diff.maxEnv, same.minSpec, diff.maxSpec, same.minEnvMedian, same.minSpecMedian, diff.maxEnvMedian, diff.maxSpecMedian)
	}
	if wrong > 0 {
		t.Errorf("%d of %d pairs judged wrong", wrong, len(rows))
	}
}

// calStats is the distribution of one kind of pair's scores.
type calStats struct {
	n                                int
	verdicts                         map[string]int
	minEnv, maxEnv, minSpec, maxSpec float64
	minEnvMedian, maxEnvMedian       float64
	minSpecMedian, maxSpecMedian     float64
	sumEnv, sumSpec                  float64
	points                           int
	minFound, maxFound               int
}

func (s *calStats) add(res compareResult, verdict string) {
	if s.n == 0 {
		s.minEnv, s.minSpec, s.minEnvMedian, s.minSpecMedian = 2, 2, 2, 2
		s.minFound = math.MaxInt
		s.verdicts = map[string]int{}
	}
	s.n++
	s.verdicts[verdict]++
	found := 0
	for i := range res.Scores {
		s.minEnv, s.maxEnv = min(s.minEnv, res.Scores[i]), max(s.maxEnv, res.Scores[i])
		s.minSpec, s.maxSpec = min(s.minSpec, res.Spectral[i]), max(s.maxSpec, res.Spectral[i])
		s.sumEnv += res.Scores[i]
		s.sumSpec += res.Spectral[i]
		s.points++
		if compareFoundAt(res, i) {
			found++
		}
	}
	em, sm := compareMedian(res.Scores), compareMedian(res.Spectral)
	s.minEnvMedian, s.maxEnvMedian = min(s.minEnvMedian, em), max(s.maxEnvMedian, em)
	s.minSpecMedian, s.maxSpecMedian = min(s.minSpecMedian, sm), max(s.maxSpecMedian, sm)
	s.minFound, s.maxFound = min(s.minFound, found), max(s.maxFound, found)
}

func (s *calStats) String() string {
	vs := make([]string, 0, len(s.verdicts))
	for _, k := range slices.Sorted(maps.Keys(s.verdicts)) {
		vs = append(vs, fmt.Sprintf("%s %d", k, s.verdicts[k]))
	}
	return fmt.Sprintf("%d pairs (%s): envelope points %.2f-%.2f mean %.2f, medians %.2f-%.2f; spectral points %.2f-%.2f mean %.2f, medians %.2f-%.2f; found %d-%d",
		s.n, strings.Join(vs, ", "), s.minEnv, s.maxEnv, s.sumEnv/float64(max(s.points, 1)), s.minEnvMedian, s.maxEnvMedian,
		s.minSpec, s.maxSpec, s.sumSpec/float64(max(s.points, 1)), s.minSpecMedian, s.maxSpecMedian, s.minFound, s.maxFound)
}

func fmtScores(xs []float64) string {
	parts := make([]string, len(xs))
	for i, x := range xs {
		parts[i] = fmt.Sprintf("%.2f", x)
	}
	return "[" + strings.Join(parts, " ") + "]"
}

// reader is who reads a recording, for its name.
func (r calRecording) reader() string {
	return cmp.Or(r.Reader, "various")
}

// makeVariants makes every calVariant of the given chapters of r under
// dir/variants, unless it is already there, and returns them as books in
// calVariants order.
func makeVariants(ctx context.Context, t *testing.T, dir string, r calRecording, paths []string) []*calBook {
	t.Helper()

	root := filepath.Join(dir, "variants", strconv.Itoa(r.ID))
	// a concat list of the chapters, for the single-file copies
	list := filepath.Join(root, "chapters.txt")
	if err := os.MkdirAll(root, 0o777); err != nil {
		t.Fatal(err)
	}
	lines := make([]string, 0, len(paths))
	for _, p := range paths {
		lines = append(lines, "file '"+strings.ReplaceAll(p, "'", `'\''`)+"'")
	}
	if err := os.WriteFile(list, []byte(strings.Join(lines, "\n")+"\n"), 0o666); err != nil {
		t.Fatal(err)
	}

	out := make([]*calBook, len(calVariants))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for i, v := range calVariants {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			folder := filepath.Join(root, v.name)
			b := &calBook{name: fmt.Sprintf("%d %s (%s)", r.ID, v.name, r.reader()), reader: r.Reader, kind: "copy"}
			done := filepath.Join(folder, ".done")
			if _, err := os.Stat(done); err != nil {
				_ = os.RemoveAll(folder)
				if err := os.MkdirAll(folder, 0o777); err != nil {
					t.Error(err)
					return
				}
				began := time.Now()
				switch {
				case v.single:
					ffmpeg(ctx, t, slices.Concat([]string{"-f", "concat", "-safe", "0"}, v.before, []string{"-i", list}, v.args, []string{filepath.Join(folder, "book."+v.ext)}))
				case v.halves:
					for k, p := range paths {
						half := r.Files[k].Seconds / 2
						ffmpeg(ctx, t, slices.Concat([]string{"-t", fmt.Sprint(half), "-i", p}, v.args, []string{filepath.Join(folder, fmt.Sprintf("%02da.%s", k+1, v.ext))}))
						ffmpeg(ctx, t, slices.Concat([]string{"-ss", fmt.Sprint(half), "-i", p}, v.args, []string{filepath.Join(folder, fmt.Sprintf("%02db.%s", k+1, v.ext))}))
					}
				default:
					for k, p := range paths {
						ffmpeg(ctx, t, slices.Concat(v.before, []string{"-i", p}, v.args, []string{filepath.Join(folder, fmt.Sprintf("%02d.%s", k+1, v.ext))}))
					}
				}
				if err := os.WriteFile(done, nil, 0o666); err != nil {
					t.Error(err)
				}
				t.Logf("made %s in %s", b.name, time.Since(began).Round(time.Second))
			}
			entries, err := os.ReadDir(folder)
			if err != nil {
				t.Error(err)
				return
			}
			for _, e := range entries {
				if !strings.HasPrefix(e.Name(), ".") {
					b.paths = append(b.paths, filepath.Join(folder, e.Name()))
				}
			}
			out[i] = b
		})
	}
	wg.Wait()
	if t.Failed() {
		t.FailNow()
	}
	return out
}

func ffmpeg(ctx context.Context, t *testing.T, args []string) {
	t.Helper()
	args = slices.Concat([]string{"-nostdin", "-hide_banner", "-loglevel", "error", "-y"}, args)
	if b, err := exec.CommandContext(ctx, "ffmpeg", args...).CombinedOutput(); err != nil {
		t.Errorf("ffmpeg %s: %v\n%s", strings.Join(args, " "), err, b)
	}
}
