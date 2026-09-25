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
// same title is not - but the rise and fall of the voice can. This is the
// check from the zbooks sort (2026-09-24), which told 86 same-title pairs
// apart: same recordings scored 0.75-0.99 at every point, other narrators
// 0.35-0.6 and never much above 0.65 at any one.

const (
	compareRate    = 4000  // Hz: loudness needs no more, and it keeps the decoding cheap
	compareWindow  = 200   // samples: 50 ms, about a syllable
	compareNeedle  = 30.0  // seconds of the first item taken at each point
	compareReach   = 600.0 // seconds searched either side of the same place in the second, at least
	compareReachOf = 0.05  // or this share of the second's length, when that is more
	compareFound   = 0.7   // a point scoring this or more found its stretch
	compareSame    = 4     // points found for the same recording
	compareShort   = 100.0 // seconds: a shorter book leaves stretches under 10 s, too short to tell
)

var (
	// where the stretches are taken, as shares of the first item's length.
	// Not the start alone: publishers' intros are the same across readings
	comparePoints = []float64{0.1, 0.3, 0.5, 0.7, 0.9}
	// one copy playing up to 3% faster or slower: without this a copy 3%
	// longer drifts off its match by mid-book (0.14 where it scores 0.99)
	compareSpeeds = audiosample.Speeds(0.97, 1.03, 25)
)

type compareSide struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Author   string `json:"author,omitempty"`
	Path     string `json:"path,omitempty"`
	Duration int    `json:"duration_s"`
}

type compareOut struct {
	Verdict   string      `json:"verdict"      jsonschema:"same, different or unsure"`
	Meaning   string      `json:"meaning"`
	Scores    []float64   `json:"scores"       jsonschema:"how well 30 s of item taken at 10, 30, 50, 70 and 90% of it was found in other near the same place, 1 being a perfect fit: same recordings score 0.75-0.99, another narrator 0.35-0.65"`
	Median    float64     `json:"median"`
	Item      compareSide `json:"item"`
	Other     compareSide `json:"other"`
	AudioRead int         `json:"audio_read_s" jsonschema:"seconds of audio decoded, both items together"`
	BytesRead int64       `json:"bytes_read"   jsonschema:"bytes of audio files fetched from the server"`
}

func registerCompareAudio(r *registry) {
	client := r.client

	type compareIn struct {
		itemRef
		Other string `json:"other" jsonschema:"the item to compare it with: library item id, or its title"`
	}
	add(r, readTool, &mcp.Tool{
		Name: "item_compare_audio",
		Description: "Tell whether two books are the same recording, whatever their titles, tags and files say, by listening: 30 seconds of item at 10, 30, 50, 70 and 90% of its length are searched for in other near the same place, by the rise and fall of the voice, allowing for one copy playing up to 3% faster. " +
			"same when at least 4 of the 5 are found: a duplicate, however it was split, encoded or filed. Another narrator reading the same book is different. " +
			"It reads audio over the network: at each point 30 s of item and 20 minutes of other or a tenth of it, whichever is more, so half of a long book, a hundred megabytes or more and a minute or two. It is for the candidate pairs audit_duplicates lists, not for a sweep of the library. Needs ffmpeg where abs-mcp runs.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in compareIn) (*mcp.CallToolResult, compareOut, error) {
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
		if d := bookA.Duration(); d < compareShort {
			return nil, compareOut{}, fmt.Errorf("%q is %s long, too short to compare by ear: the stretches taken from it would be under 10 seconds", a.Title(), fmtDuration(d))
		}

		sampler, err := audiosample.New(ctx, client)
		if err != nil {
			if errors.Is(err, audiosample.ErrNoFFmpeg) {
				return nil, compareOut{}, errors.New("item_compare_audio needs ffmpeg, which is not installed where abs-mcp runs (not on its PATH): install it there, or run abs-mcp where it is")
			}
			return nil, compareOut{}, err
		}
		defer func() { _ = sampler.Close() }()

		scores, read, err := compareBooks(ctx, sampler, bookA, bookB)
		if err != nil {
			return nil, compareOut{}, err
		}
		out := compareOut{
			Scores:    make([]float64, len(scores)),
			Median:    roundScore(compareMedian(scores)),
			Item:      compareSide{ID: a.ID, Title: a.Title(), Author: a.Media.Metadata.AuthorDisplay(), Path: a.RelPath, Duration: wholeSec(bookA.Duration())},
			Other:     compareSide{ID: b.ID, Title: b.Title(), Author: b.Media.Metadata.AuthorDisplay(), Path: b.RelPath, Duration: wholeSec(bookB.Duration())},
			AudioRead: wholeSec(read),
			BytesRead: sampler.BytesRead(),
		}
		for i, s := range scores {
			out.Scores[i] = roundScore(s)
		}
		out.Verdict, out.Meaning = compareVerdict(scores)

		return nil, out, nil
	})
}

// compareBooks scores each point of a against b, the points at once, and
// says how many seconds of audio that took.
func compareBooks(ctx context.Context, s *audiosample.Sampler, a, b *audiosample.Book) (scores []float64, read float64, err error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	scores = make([]float64, len(comparePoints))
	seconds := make([]float64, len(comparePoints))
	errs := make([]error, len(comparePoints))
	var wg sync.WaitGroup
	for i, at := range comparePoints {
		wg.Go(func() {
			scores[i], seconds[i], errs[i] = comparePoint(ctx, s, a, b, at)
			if errs[i] != nil {
				cancel() // one failed read fails the comparison; stop the rest
			}
		})
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return nil, 0, err
	}
	for _, sec := range seconds {
		read += sec
	}

	return scores, read, nil
}

// comparePoint takes 30 s of a at share at of its length and finds it in b,
// searching around the same share of b.
func comparePoint(ctx context.Context, s *audiosample.Sampler, a, b *audiosample.Book, at float64) (score, read float64, err error) {
	envelope := func(book *audiosample.Book, start, length float64) ([]float64, error) {
		e := audiosample.NewEnveloper(compareWindow)
		if err := s.Stream(ctx, book, start, length, compareRate, func(pcm []int16) error {
			e.Write(pcm)
			read += float64(len(pcm)) / compareRate
			return nil
		}); err != nil {
			return nil, err
		}
		return e.Envelope(), nil
	}

	da, db := a.Duration(), b.Duration()
	pos := da * at
	needle, err := envelope(a, pos, min(compareNeedle, da-pos))
	if err != nil {
		return 0, 0, err
	}
	reach := max(compareReach, compareReachOf*db)
	haystack, err := envelope(b, max(0, pos*db/da-reach), 2*reach)
	if err != nil {
		return 0, 0, err
	}

	return audiosample.Match(needle, haystack, compareSpeeds).Score, read, nil
}

// compareVerdict reads the scores the way the zbooks calibration did: same
// with at least 4 of the 5 points found, different with at most one and the
// middle score where other narrators sit, and unsure between.
func compareVerdict(scores []float64) (verdict, meaning string) {
	found := 0
	for _, s := range scores {
		if s >= compareFound {
			found++
		}
	}
	of := fmt.Sprintf("%d of the %d", found, len(scores))
	if found == 0 {
		of = fmt.Sprintf("none of the %d", len(scores))
	}

	switch {
	case found >= compareSame:
		return "same", "The same recording: " + of + " stretches of item were found in other. One is a duplicate of the other; keep the better copy."
	case found <= 1 && compareMedian(scores) < 0.65:
		return "different", "Different recordings: " + of + " stretches of item were found in other, which is what another narrator or production scores. Not duplicates."
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
