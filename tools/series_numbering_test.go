package tools

import (
	"testing"

	"github.com/katbyte/abs-mcp/lib/abs"
)

func numbered(id, path, series, seq string) *abs.Item {
	it := &abs.Item{ID: id, MediaType: "book", RelPath: path}
	it.Media.Metadata.Title = path
	it.Media.Metadata.Series = abs.SeriesRefs{{Name: series, Sequence: seq}}
	return it
}

func TestNumberingPadding(t *testing.T) {
	c := newNumberingCollector()
	// #1, #02, #3: three books, so no leading zero, and the "02" is reported
	c.add(numbered("d1", "Darksword/Forging the Darksword", "Darksword", "1"))
	c.add(numbered("d2", "Darksword/Doom of the Darksword", "Darksword", "02"))
	c.add(numbered("d3", "Darksword/Triumph of the Darksword", "Darksword", "3"))
	// twelve books: two digits, so the bare "9" and "2.5" are reported and "02.5" is fine
	for _, seq := range []string{"01", "02", "03", "9", "10", "11", "12", "2.5", "02.5"} {
		c.add(numbered("w"+seq, "Wheel/"+seq, "The Wheel of Time", seq))
	}
	// a number that is not a number is left alone
	c.add(numbered("p", "Wheel/Prequel", "The Wheel of Time", "Prequel"))

	got := map[string]string{}
	for _, f := range c.findings() {
		if f.Problem == "padding" {
			got[f.ID] = f.Suggest
		}
	}
	want := map[string]string{
		"d2":   "Darksword #2",
		"w9":   "The Wheel of Time #09",
		"w2.5": "The Wheel of Time #02.5",
	}
	for id, s := range want {
		if got[id] != s {
			t.Errorf("%s: suggest %q, want %q", id, got[id], s)
		}
		delete(got, id)
	}
	for id, s := range got {
		t.Errorf("%s reported as padding (%s), should not be", id, s)
	}

	if w := c.padWidth(seriesKey("Darksword")); w != 1 {
		t.Errorf("Darksword width %d, want 1", w)
	}
	if w := c.padWidth(seriesKey("The Wheel of Time")); w != 2 {
		t.Errorf("Wheel of Time width %d, want 2", w)
	}
	if s := c.styleNumber(seriesKey("The Wheel of Time"), "0.5"); s != "00.5" {
		t.Errorf("styleNumber(0.5) = %q, want 00.5", s)
	}
}
