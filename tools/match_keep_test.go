package tools

import (
	"testing"

	"github.com/katbyte/abs-mcp/lib/abs"
)

func TestParseKeep(t *testing.T) {
	t.Parallel()

	got, err := parseKeep([]string{"Series", "narrator", "series", "Tags"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0] != "series" || got[1] != "narrators" || got[2] != "tags" {
		t.Errorf("parseKeep = %v", got)
	}
	if _, err := parseKeep([]string{"cover"}); err == nil {
		t.Error("cover accepted: a match does not write it through metadata")
	}
}

func TestKeptUpdate(t *testing.T) {
	t.Parallel()

	before := &abs.Item{ID: "i1", Media: abs.Media{Tags: []string{"lgbtq"}, Metadata: abs.Metadata{
		Title:  "Mrs. S .mp3",
		Series: abs.SeriesRefs{{ID: "s1", Name: "Nemesis", Sequence: "2"}},
	}}}
	upd := keptUpdate(before, []string{"series", "title", "genres", "tags"})
	md := upd.Metadata
	if len(md.Series) != 1 || md.Series[0].Name != "Nemesis" || md.Series[0].Sequence != "2" {
		t.Errorf("series = %+v", md.Series)
	}
	if md.Title == nil || *md.Title != "Mrs. S .mp3" {
		t.Errorf("title = %v", md.Title)
	}
	// an empty kept list is sent as [] so the provider's value is cleared
	if md.Genres == nil || len(md.Genres) != 0 {
		t.Errorf("genres = %#v, want an empty list", md.Genres)
	}
	if len(upd.Tags) != 1 || upd.Tags[0] != "lgbtq" {
		t.Errorf("tags = %v", upd.Tags)
	}
	// fields not kept are left alone
	if md.Subtitle != nil || md.Narrators != nil || md.Description != nil {
		t.Errorf("unkept fields set: %+v", md)
	}
}
