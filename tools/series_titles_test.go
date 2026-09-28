package tools

import "testing"

// A title that is the series name, or carries it with a number, where the
// folder says what the title is. Light-novel titles that really are
// "Series, Vol. 4" sit in folders without a title segment and are left alone,
// and so is a first book named after its series.
func TestSeriesShapedTitle(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		title   string
		series  []string
		problem string
	}{
		{"Harry Hole 1 (Sean Barrett)", []string{"Harry Hole"}, "series_as_title"},
		{"Beebo Brinker", []string{"Beebo Brinker"}, "series_as_title"},
		{"Jumper Series Bk 1", []string{"Jumper"}, "series_as_title"},
		{"Spice and Wolf, Vol. 1 (Light Novel)", []string{"Spice and Wolf"}, "series_as_title"},
		{"The Bat - Harry Hole Series, Book 1", []string{"Harry Hole"}, "series_in_title"},
		{"Bright Falls 03 - Iris Kelly Doesn't Date", []string{"Bright Falls"}, "series_in_title"},
		{"game changers03: tough guy", []string{"Game Changers"}, "series_in_title"},
		{"Doctor Proctor 1 Doctor Proctor's Fart Powder (Miriam Margolyes)", []string{"Doctor Proctor"}, "series_in_title"},
		{"Steven Gould - [Jumper, #2.5] - Shade", []string{"Jumper"}, "series_in_title"},
		{"Rogue Protocol: The Murderbot Diaries, Book 3", []string{"Murderbot Diaries"}, "series_in_title"},
		{"Consider Phlebas: Culture Series, Book 1", []string{"Culture"}, "series_in_title"},
		{"The Bat", []string{"Harry Hole"}, ""},
		{"Foundation and Empire", []string{"Foundation"}, ""},
		{"2001: A Space Odyssey", []string{"Space Odyssey"}, ""},
		{"The Fall of Hyperion", []string{"Hyperion"}, ""},
		{"Making Money", []string{"Discworld", "Discworld: Moist von Lipwig"}, ""},
		{"Odd Girl Out", nil, ""},
	} {
		if got := seriesShapedTitle(tc.title, tc.series); got != tc.problem {
			t.Errorf("seriesShapedTitle(%q, %v) = %q, want %q", tc.title, tc.series, got, tc.problem)
		}
	}
}
