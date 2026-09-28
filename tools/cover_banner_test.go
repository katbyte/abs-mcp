package tools

import (
	"image"
	"image/color"
	"testing"
)

func TestFindAudibleBanner(t *testing.T) {
	t.Parallel()

	if _, found := findAudibleBanner(ribboned(600, 0)); !found {
		t.Error("the ribbon was not found")
	}
	if region, found := findAudibleBanner(ribboned(300, 0.9)); !found || region.Centre < 1.4 || region.Centre > 1.65 {
		t.Errorf("the ribbon on a small cover: found=%v at %v", found, region)
	}
	if region, found := findAudibleBanner(artwork(600, 0)); found {
		t.Errorf("plain art was read as a ribbon: %v", region)
	}
	lemon := image.NewRGBA(image.Rect(0, 0, 400, 400))
	for y := range 400 {
		for x := range 400 {
			lemon.Set(x, y, color.RGBA{250, 230, 40, 255})
		}
	}
	if region, found := findAudibleBanner(lemon); found {
		t.Errorf("an all-yellow cover was read as a ribbon: %v", region)
	}
}
