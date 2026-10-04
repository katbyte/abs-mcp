package tools

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"sync"

	"github.com/katbyte/abs-mcp/lib/audiosample"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// item_compare_audio: are two items one recording? Titles, tags and folders
// cannot say - a copy filed under the wrong author, split into chapters or
// re-encoded is the same recording, and another narrator's reading under the
// same title is not - but the audio can. Two measures, each at several points
// through the book: the rise and fall of the voice, which finds where the
// two line up and how alike their rhythm is, and the spectrum at that spot,
// which says whether it is the same voice saying it. The thresholds come
// from the calibration corpus (calibration_test.go): LibriVox's complete
// readings of one text by different readers, the same words in another
// voice, beside copies of one reading re-encoded, resampled, split, cut and
// played faster; and before that from the zbooks sort (2026-09-24), which
// told 86 same-title pairs apart by the envelope alone.

const (
	compareRate     = 4000  // Hz: the voice's rhythm and its lower spectrum need no more, and it keeps the decoding cheap
	compareWindow   = 200   // samples: 50 ms, about a syllable
	compareFound    = 0.7   // an envelope score this high found its stretch: one recording scores 0.85 and up, another reader 0.66 at the most and 0.40 on average
	compareSpectral = 0.6   // and a spectrum this alike there is the same voice: one recording 0.85 and up, another reader 0.53 at the most and 0.31 on average
	compareApart    = 0.65  // an envelope median under this is what another narrator scores: 0.34-0.46 in the corpus, against 0.88 and up for one recording
	compareShort    = 100.0 // seconds: a shorter book leaves stretches under 10 s, too short to tell
	compareStep     = 0.25  // percent between the speeds tried: a quarter percent off drifts 30 s by 75 ms, a window and a half
	compareQuiet    = 0.6   // a stretch with this share of its windows near silent is a pause, not speech
)

// compareConfig is how hard a comparison listens. The defaults are the
// calibration's: more points, longer stretches and a wider search cost
// compute and bytes, never accuracy, so they are open to the caller.
type compareConfig struct {
	Points   int     // stretches of item, spread evenly through it
	Stretch  float64 // seconds of each
	Reach    float64 // seconds searched either side of the same place in other, at least
	ReachOf  float64 // or this share of other's length, when that is more
	SpeedPct float64 // one copy playing up to this much faster or slower is allowed for
	Retries  int     // quiet stretches stepped past, one stretch at a time
}

var compareDefaults = compareConfig{Points: 5, Stretch: 30, Reach: 600, ReachOf: 0.05, SpeedPct: 3, Retries: 3}

// shares are where the stretches are taken, as shares of item's length,
// spread evenly and away from both ends: publishers' intros are the same
// across readings, and the end holds credits.
func (c compareConfig) shares() []float64 {
	out := make([]float64, c.Points)
	for i := range out {
		out[i] = (2*float64(i) + 1) / (2 * float64(c.Points))
	}
	return out
}

// speeds are the speeds tried, a quarter percent apart across the range.
func (c compareConfig) speeds() []float64 {
	n := int(math.Round(2*c.SpeedPct/compareStep)) + 1
	return audiosample.Speeds(1-c.SpeedPct/100, 1+c.SpeedPct/100, max(n, 1))
}

func (c compareConfig) with(points int, stretch, reach, speedPct float64) (compareConfig, error) {
	switch {
	case points < 0 || points > 50:
		return c, fmt.Errorf("points must be 1 to 50, not %d", points)
	case stretch < 0 || stretch > 600:
		return c, fmt.Errorf("stretch_s must be 5 to 600, not %g", stretch)
	case stretch > 0 && stretch < 5:
		return c, fmt.Errorf("stretch_s must be 5 to 600, not %g: a shorter stretch holds too few syllables to tell", stretch)
	case reach < 0 || reach > 7200:
		return c, fmt.Errorf("reach_s must be 60 to 7200, not %g", reach)
	case reach > 0 && reach < 60:
		return c, fmt.Errorf("reach_s must be 60 to 7200, not %g", reach)
	case speedPct < 0 || speedPct > 25:
		return c, fmt.Errorf("speed_pct must be 0 to 25, not %g", speedPct)
	}
	if points > 0 {
		c.Points = points
	}
	if stretch > 0 {
		c.Stretch = stretch
	}
	if reach > 0 {
		c.Reach = reach
	}
	if speedPct > 0 {
		c.SpeedPct = speedPct
	}
	return c, nil
}

type compareSide struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Author   string `json:"author,omitempty"`
	Path     string `json:"path,omitempty"`
	Duration int    `json:"duration_s"`
}

// compareResult is what listening found: a score of each kind per point.
type compareResult struct {
	Scores   []float64 // envelope: how well each stretch was found
	Spectral []float64 // spectrum where it was found: how alike the voice is there
	Speeds   []float64 // the speed other played at to fit, per point
	At       []float64 // seconds into item each stretch was taken from
	Read     float64   // seconds of audio decoded, both items together
}

type compareOut struct {
	Verdict        string      `json:"verdict"         jsonschema:"same, different or unsure"`
	Meaning        string      `json:"meaning"`
	Scores         []float64   `json:"scores"          jsonschema:"how well each stretch of item was found in other near the same place by the rise and fall of the voice, 1 being a perfect fit: one recording scores 0.85 and up, another reader of the same words 0.3-0.5, rarely 0.65"`
	Spectral       []float64   `json:"spectral"        jsonschema:"how alike the spectrum is where each stretch was found, 1 being the same voice frame for frame: one recording scores 0.85 and up, another voice saying the same words 0.2-0.4, rarely 0.55"`
	Median         float64     `json:"median"          jsonschema:"of scores"`
	SpectralMedian float64     `json:"spectral_median" jsonschema:"of spectral"`
	Found          int         `json:"found"           jsonschema:"stretches found by both measures"`
	Speed          float64     `json:"speed,omitempty" jsonschema:"how fast other plays against item where the stretches were found: 1.02 is 2% faster"`
	At             []int       `json:"at_s"            jsonschema:"seconds into item each stretch was taken from"`
	Item           compareSide `json:"item"`
	Other          compareSide `json:"other"`
	AudioRead      int         `json:"audio_read_s"    jsonschema:"seconds of audio decoded, both items together"`
	BytesRead      int64       `json:"bytes_read"      jsonschema:"bytes of audio files fetched from the server"`
}

func registerCompareAudio(r *registry) {
	client := r.client

	type compareIn struct {
		itemRef
		Other    string  `json:"other"               jsonschema:"the item to compare it with: library item id, or its title"`
		Points   int     `json:"points,omitempty"    jsonschema:"stretches taken from item, spread evenly through it; default 5, up to 50"`
		Stretch  float64 `json:"stretch_s,omitempty" jsonschema:"seconds of each stretch; default 30, 5 to 600"`
		Reach    float64 `json:"reach_s,omitempty"   jsonschema:"seconds searched either side of the same place in other, or a twentieth of other when that is more; default 600, 60 to 7200: raise it when one copy has an intro or a foreword the other lacks"`
		SpeedPct float64 `json:"speed_pct,omitempty" jsonschema:"how much faster or slower one copy may play, in percent; default 3, up to 25"`
	}
	add(r, readTool, &mcp.Tool{
		Name: "item_compare_audio",
		Description: "Tell whether two books are the same recording, whatever their titles, tags and files say, by listening: stretches of item, 30 seconds at 5 points spread through it by default, are each searched for in other near the same place by the rise and fall of the voice, allowing for one copy playing up to 3% faster; where one is found, the spectrum there says whether it is the same voice. " +
			"same when at least four in five stretches are found by both: a duplicate, however it was split, encoded or filed. Another narrator reading the same book is different, and so is the same narrator reading it again. unsure in between: an abridgement, extra material in one copy, or a damaged file. " +
			"points, stretch_s, reach_s and speed_pct listen harder for compute and bytes, never for accuracy. " +
			"It reads audio over the network: at each point one stretch of item and 20 minutes of other or a twentieth of it, whichever is more, so half of a long book, a hundred megabytes or more and a minute or two. It is for the candidate pairs audit_duplicates lists, not for a sweep of the library. Needs ffmpeg where abs-mcp runs.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in compareIn) (*mcp.CallToolResult, compareOut, error) {
		cfg, err := compareDefaults.with(in.Points, in.Stretch, in.Reach, in.SpeedPct)
		if err != nil {
			return nil, compareOut{}, err
		}
		a, err := resolveItem(ctx, client, in.Library, in.Item)
		if err != nil {
			return nil, compareOut{}, err
		}
		b, err := resolveItem(ctx, client, in.Library, in.Other)
		if err != nil {
			return nil, compareOut{}, err
		}
		if a.ID == b.ID {
			return nil, compareOut{}, fmt.Errorf("item and other are both %q (%s): name two items", a.Title(), a.ID)
		}
		bookA, err := audiosample.BookOf(a)
		if err != nil {
			return nil, compareOut{}, err
		}
		bookB, err := audiosample.BookOf(b)
		if err != nil {
			return nil, compareOut{}, err
		}
		if d := bookA.Duration(); d < compareShort || d < cfg.Stretch*float64(cfg.Points)/2 {
			return nil, compareOut{}, fmt.Errorf("%q is %s long, too short to compare by ear: %d stretches of %gs would overlap or fall under 10 seconds", a.Title(), fmtDuration(d), cfg.Points, cfg.Stretch)
		}

		sampler, err := audiosample.New(ctx, client)
		if err != nil {
			if errors.Is(err, audiosample.ErrNoFFmpeg) {
				return nil, compareOut{}, errors.New("item_compare_audio needs ffmpeg, which is not installed where abs-mcp runs (not on its PATH): install it there, or run abs-mcp where it is")
			}
			return nil, compareOut{}, err
		}
		res, err := compareBooks(ctx, sampler, bookA, bookB, cfg)
		// the proxy stopping early is said beside whatever the read found
		if err := errors.Join(err, sampler.Close()); err != nil {
			return nil, compareOut{}, err
		}
		out := compareOutOf(res)
		out.Item = compareSide{ID: a.ID, Title: a.Title(), Author: a.Media.Metadata.AuthorDisplay(), Path: a.RelPath, Duration: wholeSec(bookA.Duration())}
		out.Other = compareSide{ID: b.ID, Title: b.Title(), Author: b.Media.Metadata.AuthorDisplay(), Path: b.RelPath, Duration: wholeSec(bookB.Duration())}
		out.BytesRead = sampler.BytesRead()

		return nil, out, nil
	})
}

// compareOutOf reads a result into the tool's answer, the two sides and the
// bytes left for the caller.
func compareOutOf(res compareResult) compareOut {
	out := compareOut{
		Scores:         make([]float64, len(res.Scores)),
		Spectral:       make([]float64, len(res.Spectral)),
		Median:         roundScore(compareMedian(res.Scores)),
		SpectralMedian: roundScore(compareMedian(res.Spectral)),
		At:             make([]int, len(res.At)),
		AudioRead:      wholeSec(res.Read),
	}
	var speeds []float64
	for i := range res.Scores {
		out.Scores[i] = roundScore(res.Scores[i])
		out.Spectral[i] = roundScore(res.Spectral[i])
		out.At[i] = wholeSec(res.At[i])
		if compareFoundAt(res, i) {
			speeds = append(speeds, res.Speeds[i])
		}
	}
	out.Found = len(speeds)
	if len(speeds) > 0 {
		out.Speed = math.Round(compareMedian(speeds)*1000) / 1000
	}
	out.Verdict, out.Meaning = compareVerdict(res)
	return out
}

// compareBooks scores each point of a against b, the points at once, and
// says how many seconds of audio that took.
func compareBooks(ctx context.Context, s *audiosample.Sampler, a, b *audiosample.Book, cfg compareConfig) (compareResult, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	shares := cfg.shares()
	res := compareResult{
		Scores: make([]float64, len(shares)), Spectral: make([]float64, len(shares)),
		Speeds: make([]float64, len(shares)), At: make([]float64, len(shares)),
	}
	seconds := make([]float64, len(shares))
	errs := make([]error, len(shares))
	var wg sync.WaitGroup
	for i, at := range shares {
		wg.Go(func() {
			var p comparePointResult
			p, errs[i] = comparePoint(ctx, s, a, b, at, cfg)
			res.Scores[i], res.Spectral[i], res.Speeds[i], res.At[i], seconds[i] = p.score, p.spectral, p.speed, p.at, p.read
			if errs[i] != nil {
				cancel() // one failed read fails the comparison; stop the rest
			}
		})
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return compareResult{}, err
	}
	for _, sec := range seconds {
		res.Read += sec
	}

	return res, nil
}

type comparePointResult struct {
	score, spectral, speed, at, read float64
}

// comparePoint takes a stretch of a at share at of its length and finds it
// in b, searching around the same share of b. A stretch that is mostly
// silence - a chapter break, a pause - says nothing, so the next stretch
// along is taken instead, a few times over.
func comparePoint(ctx context.Context, s *audiosample.Sampler, a, b *audiosample.Book, at float64, cfg compareConfig) (comparePointResult, error) {
	var out comparePointResult
	da, db := a.Duration(), b.Duration()
	pos := da * at
	var needle []int16
	for try := 0; ; try++ {
		pcm, err := s.Read(ctx, a, pos, min(cfg.Stretch, da-pos), compareRate)
		if err != nil {
			return out, err
		}
		out.read += float64(len(pcm)) / compareRate
		needle = pcm
		if try >= cfg.Retries || !quiet(pcm, compareWindow) || pos+2*cfg.Stretch > da {
			break
		}
		pos += cfg.Stretch
	}
	out.at = pos

	reach := max(cfg.Reach, cfg.ReachOf*db)
	start := max(0, pos*db/da-reach)
	haystack := make([]int16, 0, int(2*reach*compareRate))
	e := audiosample.NewEnveloper(compareWindow)
	if err := s.Stream(ctx, b, start, 2*reach, compareRate, func(pcm []int16) error {
		e.Write(pcm)
		haystack = append(haystack, pcm...)
		return nil
	}); err != nil {
		return out, err
	}
	out.read += float64(len(haystack)) / compareRate

	found := audiosample.Match(audiosample.Envelope(needle, compareWindow), e.Envelope(), cfg.speeds())
	out.score, out.speed = found.Score, found.Speed
	if found.Score > 0 {
		out.spectral = audiosample.SpectralMatch(needle, haystack, compareRate, found, compareWindow)
	}
	return out, nil
}

// quiet says whether a stretch is mostly silence: the share of its windows
// under a tenth of its loudest window's RMS, by amplitude, is over
// compareQuiet. Speech pauses between phrases, but not for most of 30 s.
func quiet(pcm []int16, window int) bool {
	if len(pcm) < window {
		return true
	}
	var rms []float64
	for i := 0; i+window <= len(pcm); i += window {
		var sum float64
		for _, v := range pcm[i : i+window] {
			sum += float64(v) * float64(v)
		}
		rms = append(rms, math.Sqrt(sum/float64(window)))
	}
	peak := slices.Max(rms)
	if peak < 1 {
		return true
	}
	low := 0
	for _, r := range rms {
		if r < peak/10 {
			low++
		}
	}
	return float64(low)/float64(len(rms)) > compareQuiet
}

// compareFoundAt says whether point i of res was found by both measures.
func compareFoundAt(res compareResult, i int) bool {
	return res.Scores[i] >= compareFound && res.Spectral[i] >= compareSpectral
}

// compareVerdict reads the scores the way the calibration did: same with at
// least four in five points found by both measures, different with at most
// one in five and the envelope's median where other narrators sit, and
// unsure between.
func compareVerdict(res compareResult) (verdict, meaning string) {
	n := len(res.Scores)
	found, heard := 0, 0
	for i := range res.Scores {
		if compareFoundAt(res, i) {
			found++
		}
		if res.Scores[i] >= compareFound {
			heard++
		}
	}
	of := fmt.Sprintf("%d of the %d", found, n)
	if found == 0 {
		of = fmt.Sprintf("none of the %d", n)
	}
	same := int(math.Ceil(0.8 * float64(n)))
	apart := n / 5

	switch {
	case n == 0:
		return "unsure", "Nothing was compared."
	case found >= same:
		return "same", "The same recording: " + of + " stretches of item were found in other, with the same voice at each. One is a duplicate of the other; keep the better copy."
	case heard <= apart && compareMedian(res.Scores) < compareApart:
		return "different", "Different recordings: " + of + " stretches of item were found in other, which is what another narrator or production scores. Not duplicates."
	case heard >= same && found <= apart:
		return "different", "Different recordings: " + fmt.Sprintf("%d of the %d", heard, n) + " stretches line up in rhythm but none in voice, which is what another reader of the same words at the same pace scores, or a re-recording. Not duplicates."
	default:
		return "unsure", "Unsure: " + of + " stretches of item were found in other, too many for another narrator and too few for one recording. Perhaps one narration with part cut or added in one copy (an abridged edition, extra material) or a damaged file; listen where the scores are low."
	}
}

func compareMedian(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := slices.Sorted(slices.Values(xs))
	if len(s)%2 == 1 {
		return s[len(s)/2]
	}
	return (s[len(s)/2-1] + s[len(s)/2]) / 2
}

func roundScore(x float64) float64 { return math.Round(x*100) / 100 }
