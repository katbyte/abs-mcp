package tools

import (
	"cmp"
	"context"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/katbyte/abs-mcp/sdk/abs"
)

// Incomplete copies: a copy of a book with some of its files missing is not
// a second copy to choose between but a bad one. Joined by title it would be
// a duplicate, but the groups keep copies more than 15% apart in length out,
// as two readings of one book differ that much. The files tell the two
// apart: an incomplete copy's tracks are some of the whole copy's, each the
// same length to the second, where another reading's tracks match none.

const (
	// incompleteTrackSeconds is how close two tracks' lengths must be to be
	// one file copied, not another recording that happens to run as long
	incompleteTrackSeconds = 1.0
	// incompleteMinTracks: one track the length of another could be chance;
	// this many, every one matching in play order, is one rip. Fewer must
	// share the file name or the size to the byte as well
	incompleteMinTracks = 2
	// incompleteMinShare: a short copy holding under a quarter of the long
	// one's tracks must share each file's name or size too. Two tracks can
	// match two of a hundred-track CD rip's by length alone two times in
	// three
	incompleteMinShare = 4
	// incompleteWholeShare: a missing track this near the short copy's whole
	// length is the book again in one file, not what the copy lacks
	incompleteWholeShare = 0.02
	// incompleteRounds is how many times settle reads for incomplete copies
	// and groups again: each round can only find copies the last one left
	incompleteRounds = 4
	// incompleteShowMissing is how many missing file names a row lists
	incompleteShowMissing = 5
)

// dupIncomplete is a copy holding only some of another copy's files.
type dupIncomplete struct {
	Why     string        `json:"why"`
	Items   []itemSummary `json:"items"             jsonschema:"the incomplete copy, then the whole one"`
	Held    int           `json:"tracks_held"       jsonschema:"tracks the incomplete copy has"`
	Whole   int           `json:"tracks_whole"      jsonschema:"tracks the whole copy has"`
	Missing []string      `json:"missing,omitempty" jsonschema:"file names the whole copy has and the incomplete one lacks, the first five"`
}

// incomplete reads the files of each pair of copies groups kept apart for
// their lengths alone, and reports the shorter one when every track it has
// is one of the longer one's: the same rip with files missing. A copy is one
// row, against the copy with the most tracks it is a part of. A record whose
// folder is gone or holds nothing playable has only stale files to show, and
// is audit_issues' to report, so a pair with one is passed over. The items
// are fetched whole a batch at a time, and only the ones in such a pair.
func (d *dupCollector) incomplete(ctx context.Context, client *abs.Client) ([]dupIncomplete, error) {
	pairs := slices.DeleteFunc(slices.Clone(d.apart), func(p dupPair) bool {
		a, b := &d.items[p.i], &d.items[p.j]
		return a.Missing || a.Invalid || b.Missing || b.Invalid || d.excluded[p.i] || d.excluded[p.j]
	})
	need := map[string]bool{}
	for _, p := range pairs {
		need[d.items[p.i].ID], need[d.items[p.j].ID] = true, true
	}
	ids := make([]string, 0, len(need))
	for id := range need {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	// each book fetched once however often settle reads it
	if d.tracks == nil {
		d.tracks = map[string][]abs.AudioFile{}
	}
	ids = slices.DeleteFunc(ids, func(id string) bool { _, ok := d.tracks[id]; return ok })
	for chunk := range slices.Chunk(ids, embedBatchSize) {
		items, err := client.ItemsBatch(ctx, chunk)
		if err != nil {
			return nil, err
		}
		for k := range items {
			d.tracks[items[k].ID] = playedTracks(&items[k])
		}
	}
	tracks := d.tracks

	best := map[string]dupIncomplete{} // short copy -> its row against the fullest copy it is part of
	for _, p := range pairs {
		short, long := d.items[p.i], d.items[p.j]
		if short.Duration > long.Duration {
			short, long = long, short
		}
		held, missing, ok := heldTracks(tracks[short.ID], tracks[long.ID])
		if !ok {
			continue
		}
		whole := len(tracks[long.ID])
		if prev, seen := best[short.ID]; seen && cmp.Or(cmp.Compare(prev.Whole, whole), cmp.Compare(prev.Items[1].Duration, long.Duration), strings.Compare(long.ID, prev.Items[1].ID)) >= 0 {
			continue
		}
		row := dupIncomplete{
			Why: fmt.Sprintf("%s; each of this copy's %d tracks is one of the other copy's %d, in play order, the same length to the second, and the other %d are missing: not a second copy but an incomplete one",
				map[string]string{"title": "the same title and author", "asin": "the same asin", "isbn": "the same isbn"}[p.by], held, whole, whole-held),
			Items: []itemSummary{short, long},
			Held:  held,
			Whole: whole,
		}
		row.Missing = missing[:min(len(missing), incompleteShowMissing)]
		best[short.ID] = row
	}
	out := make([]dupIncomplete, 0, len(best))
	for _, row := range best {
		out = append(out, row)
	}
	slices.SortFunc(out, func(a, b dupIncomplete) int {
		return cmp.Or(strings.Compare(a.Items[0].Title, b.Items[0].Title), strings.Compare(a.Items[0].ID, b.Items[0].ID))
	})
	return out, nil
}

// playedTracks is an item's audio files in play order, the excluded ones left
// out.
func playedTracks(it *abs.Item) []abs.AudioFile {
	var out []abs.AudioFile
	for _, f := range it.Media.AudioFiles {
		if !f.Exclude {
			out = append(out, f)
		}
	}
	slices.SortStableFunc(out, func(a, b abs.AudioFile) int { return cmp.Compare(a.Index, b.Index) })
	return out
}

// heldTracks matches the short copy's tracks, in play order, to the long
// copy's, in play order, each to the next of the same length to the second:
// the same rip with files missing keeps its order. It reports whether every
// one matched and the long copy has more, and when few tracks match, or few
// of the long copy's, that each shares its file's name or size as well;
// then how many were held and the long copy's tracks left over, in play
// order. Taking the first match along is as good as any: if the tracks fit
// in order at all, they fit that way.
func heldTracks(short, long []abs.AudioFile) (held int, missing []string, ok bool) {
	if len(short) == 0 || len(short) >= len(long) {
		return 0, nil, false
	}
	strict := len(short) < incompleteMinTracks || len(short)*incompleteMinShare < len(long)
	used := make([]bool, len(long))
	next := 0
	for i := range short {
		s := &short[i]
		match := -1
		for j := next; j < len(long); j++ {
			if math.Abs(s.Duration-long[j].Duration) <= incompleteTrackSeconds && (!strict || sameFile(s, &long[j])) {
				match = j
				break
			}
		}
		if match < 0 {
			return 0, nil, false
		}
		used[match] = true
		next = match + 1
		held++
	}
	// a long copy that plays the book twice - the short one's tracks and
	// one file of them all - holds nothing the short one lacks. Only that
	// shape: one track of two missing is as long as the one held, too
	var total float64
	for i := range short {
		total += short[i].Duration
	}
	if unused := slices.Index(used, false); len(short) >= incompleteMinTracks && len(long)-len(short) == 1 &&
		math.Abs(long[unused].Duration-total) <= incompleteWholeShare*total {
		return 0, nil, false
	}
	for j, l := range long {
		if !used[j] {
			missing = append(missing, l.Metadata.Filename)
		}
	}
	return held, missing, true
}

// sameFile reports whether two tracks share a file name or a size to the
// byte: what one track alone needs beside its length to be the same file.
func sameFile(a, b *abs.AudioFile) bool {
	if a.Metadata.Filename != "" && a.Metadata.Filename == b.Metadata.Filename {
		return true
	}
	return a.Metadata.Size > 0 && a.Metadata.Size == b.Metadata.Size
}

// settle groups the copies, reads the pairs far apart for incomplete ones,
// and groups again without them, until no new one turns up: taking a bad
// copy out of a group can join copies it held apart, and leave another
// pair to read. With each copy read against its group's fullest copy, the
// first round finds every one seen so far; the rounds after are a net for
// a shape not yet seen.
func (d *dupCollector) settle(ctx context.Context, client *abs.Client) ([]dupGroup, []dupIncomplete, error) {
	groups := d.groups()
	found := []dupIncomplete{}
	reported := map[string]bool{}
	for range incompleteRounds {
		rows, err := d.incomplete(ctx, client)
		if err != nil {
			return nil, nil, err
		}
		fresh := false
		for _, row := range rows {
			if !reported[row.Items[0].ID] {
				reported[row.Items[0].ID] = true
				found = append(found, row)
				fresh = true
			}
		}
		if !fresh {
			break
		}
		d.exclude(reported)
		groups = d.groups()
	}
	slices.SortFunc(found, func(a, b dupIncomplete) int {
		return cmp.Or(strings.Compare(a.Items[0].Title, b.Items[0].Title), strings.Compare(a.Items[0].ID, b.Items[0].ID))
	})
	return groups, found, nil
}
