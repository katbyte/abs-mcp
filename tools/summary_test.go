package tools

import (
	"testing"

	"github.com/katbyte/abs-mcp/lib/abs"
)

func TestSummariseItem(t *testing.T) {
	t.Parallel()

	it := &abs.Item{
		ID: "i1", MediaType: "book", RelPath: "Frank Herbert/Dune", AddedAt: 1_700_000_000_000, Size: 500 << 20,
		Media: abs.Media{
			Metadata:  abs.Metadata{Title: "Dune", AuthorName: "Frank Herbert", NarratorName: "Scott Brick", SeriesName: "Dune #1", PublishedYear: "1965", ASIN: "B0"},
			CoverPath: "/x/cover.jpg", Duration: 75_000.4, NumTracks: 3, NumChapters: 40, Tags: []string{"sf"},
		},
		UserMediaProgress: &abs.MediaProgress{Progress: 0.5, CurrentTime: 37_500, ID: "p1"},
	}
	s := summarize(it)
	if s.Title != "Dune" || s.Author != "Frank Herbert" || s.Narrator != "Scott Brick" || s.Year != "1965" {
		t.Errorf("summary = %+v", s)
	}
	if len(s.Series) != 1 || s.Series[0] != "Dune #1" {
		t.Errorf("series = %v", s.Series)
	}
	if s.Duration != 75_000 || s.Size != 500<<20 || s.Tracks != 3 || s.Chapters != 40 || s.NoCover {
		t.Errorf("facts = %+v", s)
	}
	if s.Progress == nil || s.Progress.Percent != 50 || s.Progress.ProgressID != "p1" {
		t.Errorf("progress = %+v", s.Progress)
	}
	if s.Added != "2023-11-14" {
		t.Errorf("added = %q", s.Added)
	}
}

// progressOf is the projection every progress-bearing tool goes through, and
// nil (no progress record) is a normal state rather than an error.
func TestProgressOf(t *testing.T) {
	t.Parallel()

	if got := progressOf(nil); got != nil {
		t.Errorf("progressOf(nil) = %v, want nil", got)
	}

	got := progressOf(&abs.MediaProgress{
		ID: "p1", Progress: 0.256, CurrentTime: 3725.6, IsFinished: false,
		LastUpdate: 1_700_000_000_000, HideFromContinueListening: true,
	})
	if got == nil {
		t.Fatal("progressOf returned nil for a real record")
	}
	if got.Percent != 26 {
		t.Errorf("percent = %d, want 26 (rounded)", got.Percent)
	}
	if got.CurrentTime != 3726 {
		t.Errorf("current_time_s = %d, want 3726 (rounded)", got.CurrentTime)
	}
	if !got.Hidden || got.ProgressID != "p1" {
		t.Errorf("hidden/id did not carry through: %+v", got)
	}
}
