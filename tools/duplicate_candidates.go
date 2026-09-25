package tools

import (
	"cmp"
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/katbyte/abs-mcp/lib/abs"
)

// Duplicate candidates: pairs audit_duplicates' keys do not join that are
// probably one recording, found in the zbooks sort (2026-09-24). The keys
// need an asin, an isbn or the same title and author, and a copy scanned in
// beside another rarely has them: the 20th anniversary copy of Ender's Game
// carries its edition in the title, The Saints of Salvation was filed under
// Philip K. Dick, A Planet Called Treason is Treason under the title of its
// later edition with "Treason" left in its album tag. Each is only a lead:
// item_compare_audio says whether the two are one recording.
//
// Other readings of one book are what must stay out, and the evidence is
// the narrator (the field, or the name in brackets on a folder, "Red Prophet
// (Polk)") and the length: two readings of a novel differ by 5-50%, one
// recording re-encoded or split by under 1%, and a copy with an anniversary's
// extra material by 6%.

const (
	// lengths within this share of each other, the same title and author
	// (Protector 2.5%, Saints of Salvation 3%)
	candidateSameLength = 0.04
	// with an edition named in the title or folder, which can add material
	// (Ender's Game, 20th anniversary: 6%)
	candidateEditionLength = 0.10
	// an album tag naming the other's title is a weaker clue than the title
	// itself, so the lengths must agree closely (Treason: 0.7%)
	candidateAlbumLength = 0.01
)

var (
	// a parenthetical or bracketed part of a title or folder
	candidateBrackets = regexp.MustCompile(`[(\[]([^)\]]*)[)\]]`)
	// what names an edition or a format rather than a reader
	candidateLabel = regexp.MustCompile(`(?i)\b(anniversary|full[- ]?cast|cast|dramati[sz](ed|ation)|unabridged|abridged|remaster(ed)?|single[- ]?file|one[- ]file|files?|edition|version|bbc|radio|graphic ?audio|audio ?drama|drama|extended|uncut|complete|retail|mp3|m4b|aac|kbps)\b`)
	// what names neither a reader nor an edition: "(A Novel)", "Various"
	candidateNoName = regexp.MustCompile(`(?i)\b(novel|book|books|series|saga|trilogy|volume|vol|part|collection|stories|omnibus|various|multiple|unknown|anonymous)\b`)
)

// candidateRoles are words in an author or narrator field that are not part
// of the name: "Ken Liu - Translator Baoshu".
var candidateRoles = map[string]bool{
	"translator": true, "translated": true, "narrator": true, "narrated": true, "editor": true, "edited": true,
	"foreword": true, "introduction": true, "read": true, "by": true, "and": true, "with": true,
}

// dupCandidate is two copies that are probably one recording.
type dupCandidate struct {
	Why   string        `json:"why"   jsonschema:"what joins them; confirm with item_compare_audio"`
	Items []itemSummary `json:"items" jsonschema:"the two copies"`
}

// candidateFacts is what the rules read of one item.
type candidateFacts struct {
	title     string     // the title as it names the book: no brackets, no subtitle
	core      string     // title reduced to comparable words, articles and punctuation gone
	author    []string   // the author's name words
	narrators [][]string // each narrator's name words, from the field and the folder's brackets
	labels    []string   // editions and formats named in brackets
}

func candidateFactsOf(s *itemSummary) candidateFacts {
	f := candidateFacts{title: candidateTitle(s.Title), author: candidateWords(s.Author)}
	f.core = candidateCore(s.Title)
	for name := range strings.SplitSeq(s.Narrator, ",") {
		if words := candidateWords(name); len(words) > 0 && !candidateLabel.MatchString(name) && !candidateNoName.MatchString(name) {
			f.narrators = append(f.narrators, words)
		}
	}
	folder := s.Path
	if i := strings.LastIndex(folder, "/"); i >= 0 {
		folder = folder[i+1:]
	}
	seen := map[string]bool{}
	for _, m := range slices.Concat(candidateBrackets.FindAllStringSubmatch(s.Title, -1), candidateBrackets.FindAllStringSubmatch(folder, -1)) {
		for part := range strings.SplitSeq(m[1], ",") {
			part = strings.TrimSpace(part)
			words := candidateWords(part)
			switch {
			case len(words) == 0 || seen[part]:
			case candidateLabel.MatchString(part):
				f.labels = append(f.labels, part)
			// a year, a book number: neither a reader nor an edition
			case strings.ContainsAny(part, "0123456789") || candidateNoName.MatchString(part):
			// the author's own name disambiguates the author, "(Baoshu)",
			// as often as it says the author reads it
			case candidateSubset(words, f.author):
			case len(words) <= 4:
				f.narrators = append(f.narrators, words)
			}
			seen[part] = true
		}
	}
	return f
}

// candidateTitle is a title as it names the book: brackets, disc and part
// markers and a subtitle set aside.
func candidateTitle(title string) string {
	t := pathStrip(title)
	if i := strings.Index(t, ":"); i > 0 {
		t = t[:i]
	}
	return strings.TrimSpace(t)
}

// candidateCore reduces a title to the words compared: "Ender's Game (20th
// Anniversary full cast)" and "Ender's Game" are both "enders game", "The
// Redemption of Time: The Three-Body Problem, Book 4" is "redemption of
// time". A leading article goes too: folders drop it as often as not.
func candidateCore(title string) string {
	core := pathWords(candidateTitle(title))
	for _, article := range []string{"the ", "a ", "an "} {
		if rest, ok := strings.CutPrefix(core, article); ok && rest != "" {
			return rest
		}
	}
	return core
}

// candidateWords is the name words of an author or narrator: lower-case, no
// punctuation, no initials or role words.
func candidateWords(name string) []string {
	var out []string
	for w := range strings.FieldsSeq(norm(name)) {
		if len(w) > 1 && !candidateRoles[w] {
			out = append(out, w)
		}
	}
	return out
}

// candidateSubset reports whether every word of part is in whole, and part
// has some: "O'Brien" is Connor O'Brien, and Baoshu is "Ken Liu - Translator
// Baoshu".
func candidateSubset(part, whole []string) bool {
	if len(part) == 0 {
		return false
	}
	for _, w := range part {
		if !slices.Contains(whole, w) {
			return false
		}
	}
	return true
}

// sameAuthor reports whether one author's name is within the other's.
func (f *candidateFacts) sameAuthor(o *candidateFacts) bool {
	return candidateSubset(f.author, o.author) || candidateSubset(o.author, f.author)
}

// otherReader reports whether both copies name their reader and no name of
// one is within a name of the other: another reading, not another copy.
func (f *candidateFacts) otherReader(o *candidateFacts) bool {
	if len(f.narrators) == 0 || len(o.narrators) == 0 {
		return false
	}
	for _, a := range f.narrators {
		for _, b := range o.narrators {
			if candidateSubset(a, b) || candidateSubset(b, a) {
				return false
			}
		}
	}
	return true
}

// candidateApart is how far apart two lengths are, as a share of the longer;
// 1 when either is unknown.
func candidateApart(a, b int) float64 {
	if a <= 0 || b <= 0 {
		return 1
	}
	return float64(max(a, b)-min(a, b)) / float64(max(a, b))
}

// candidates finds the pairs the keys did not join that are probably one
// recording. Titles and lengths come from the listing already swept; the
// album rule reads the first file's tags, fetched only for the pairs by one
// author whose lengths already agree, a batch at a time.
func (d *dupCollector) candidates(ctx context.Context, client *abs.Client, groups []dupGroup) ([]dupCandidate, error) {
	group := map[string]int{} // item id -> its group, for the pairs already joined
	for g := range groups {
		for _, it := range groups[g].Items {
			group[it.ID] = g + 1
		}
	}
	joined := func(i, j int) bool {
		gi, gj := group[d.items[i].ID], group[d.items[j].ID]
		return gi != 0 && gi == gj
	}
	facts := make([]candidateFacts, len(d.items))
	var books []int
	for i := range d.items {
		if d.items[i].Type == "book" {
			facts[i] = candidateFactsOf(&d.items[i])
			books = append(books, i)
		}
	}

	var out []dupCandidate
	pair := func(i, j int, why string) {
		out = append(out, dupCandidate{Why: why + "; confirm with item_compare_audio", Items: []itemSummary{d.items[i], d.items[j]}})
	}

	// the same title, once brackets and subtitles are set aside
	byCore := map[string][]int{}
	for _, i := range books {
		if facts[i].core != "" {
			byCore[facts[i].core] = append(byCore[facts[i].core], i)
		}
	}
	for _, members := range byCore {
		for x, i := range members {
			for _, j := range members[x+1:] {
				if joined(i, j) {
					continue
				}
				if why := d.titleCandidate(i, j, &facts[i], &facts[j]); why != "" {
					pair(i, j, why)
				}
			}
		}
	}

	// one's album tag the other's title: same author, lengths within 1%
	byLength := slices.Clone(books)
	slices.SortFunc(byLength, func(a, b int) int { return cmp.Compare(d.items[a].Duration, d.items[b].Duration) })
	type albumPair struct{ i, j int }
	var pending []albumPair
	need := map[string]bool{}
	for x, i := range byLength {
		for _, j := range byLength[x+1:] {
			if candidateApart(d.items[i].Duration, d.items[j].Duration) > candidateAlbumLength {
				break
			}
			fi, fj := &facts[i], &facts[j]
			if fi.core == fj.core || !fi.sameAuthor(fj) || fi.otherReader(fj) || joined(i, j) {
				continue
			}
			pending = append(pending, albumPair{min(i, j), max(i, j)})
			need[d.items[i].ID], need[d.items[j].ID] = true, true
		}
	}
	albums := map[string]string{}
	ids := make([]string, 0, len(need))
	for id := range need {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for chunk := range slices.Chunk(ids, embedBatchSize) {
		items, err := client.ItemsBatch(ctx, chunk)
		if err != nil {
			return nil, err
		}
		for k := range items {
			albums[items[k].ID] = candidateAlbum(&items[k])
		}
	}
	for _, p := range pending {
		ai, aj := albums[d.items[p.i].ID], albums[d.items[p.j].ID]
		apart := fmt.Sprintf("%.1f%%", 100*candidateApart(d.items[p.i].Duration, d.items[p.j].Duration))
		switch {
		case ai != "" && candidateCore(ai) == facts[p.j].core:
			pair(p.i, p.j, fmt.Sprintf("the album tag of the first, %q, is the second's title; same author, lengths %s apart", ai, apart))
		case aj != "" && candidateCore(aj) == facts[p.i].core:
			pair(p.i, p.j, fmt.Sprintf("the album tag of the second, %q, is the first's title; same author, lengths %s apart", aj, apart))
		}
	}

	slices.SortFunc(out, func(a, b dupCandidate) int {
		return cmp.Or(
			strings.Compare(strings.ToLower(a.Items[0].Title), strings.ToLower(b.Items[0].Title)),
			strings.Compare(a.Items[0].ID, b.Items[0].ID),
			strings.Compare(a.Items[1].ID, b.Items[1].ID))
	})
	return out, nil
}

// titleCandidate says why two items with the same core title are probably
// one recording, or "" when they are not: another reader named on each, or
// lengths too far apart for the evidence.
func (d *dupCollector) titleCandidate(i, j int, fi, fj *candidateFacts) string {
	if fi.otherReader(fj) {
		return ""
	}
	a, b := d.items[i], d.items[j]
	apart := candidateApart(a.Duration, b.Duration)
	labels := slices.Concat(fi.labels, fj.labels)
	sameAuthor := fi.sameAuthor(fj)

	var why string
	title := fi.title
	if len(fj.title) < len(title) {
		title = fj.title
	}
	switch {
	case sameAuthor && apart <= candidateSameLength:
		why = fmt.Sprintf("the same title, %q, and author; lengths %.1f%% apart", title, 100*apart)
	case sameAuthor && len(labels) > 0 && apart <= candidateEditionLength:
		why = fmt.Sprintf("the same title, %q, and author, one marked %q; lengths %.1f%% apart, as an edition's added material makes them", title, labels[0], 100*apart)
	case !sameAuthor && apart <= candidateSameLength:
		why = fmt.Sprintf("the same title, %q, under another author (%s, %s); lengths %.1f%% apart", title, cmp.Or(a.Author, "none"), cmp.Or(b.Author, "none"), 100*apart)
	default:
		return ""
	}
	if a.Tracks > 0 && b.Tracks > 0 && (a.Tracks == 1) != (b.Tracks == 1) {
		why += fmt.Sprintf("; one is a single file, the other %d", max(a.Tracks, b.Tracks))
	}
	return why
}

// candidateAlbum is the album tag of an item's first audio file.
func candidateAlbum(it *abs.Item) string {
	files := slices.Clone(it.Media.AudioFiles)
	slices.SortStableFunc(files, func(a, b abs.AudioFile) int { return cmp.Compare(a.Index, b.Index) })
	for _, f := range files {
		if !f.Exclude {
			return strings.TrimSpace(f.MetaTags["tagAlbum"])
		}
	}
	return ""
}
