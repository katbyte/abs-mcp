package tools

import (
	"image"
)

// The "ONLY FROM audible" ribbon: a saturated yellow band running at 45
// degrees across the bottom-right corner of a cover, with the art showing
// again in the corner beyond it. It is the same overlay on every cover that
// has it, so it is found by geometry and colour, not by reading it. The
// image is walked in diagonal coordinates, s = (x + y) / n, where the band
// is a range of s; the band that is most yellow inside and least yellow on
// either side is the candidate, and it counts when it is yellow along its
// whole length, so a cover that happens to be yellow is not a ribbon.

// bannerRegion is where a ribbon was found, in diagonal coordinates.
type bannerRegion struct {
	Centre, HalfWidth float64 // of s = (x + y) / n
	Inside, Outside   float64 // yellow fraction in the band and in the bands beside it
}

// isRibbonYellow is the ribbon's colour: strongly red and green, little
// blue, and not much darker than the ribbon's lemon (the dark text on it
// fails this, which is why the band is judged by fraction, not by all).
func isRibbonYellow(r, g, b uint32) bool {
	r8, g8, b8 := r>>8, g>>8, b>>8
	return r8 >= 170 && g8 >= 160 && b8 <= 130 && g8+40 >= r8
}

// yellowMap reduces an image to a grid of yellow flags for the scan.
func yellowMap(img image.Image, n int) [][]bool {
	b := img.Bounds()
	sw, sh := b.Dx(), b.Dy()
	out := make([][]bool, n)
	for y := range n {
		out[y] = make([]bool, n)
		sy := b.Min.Y + y*sh/n
		for x := range n {
			sx := b.Min.X + x*sw/n
			r, g, bl, _ := img.At(sx, sy).RGBA()
			out[y][x] = isRibbonYellow(r, g, bl)
		}
	}
	return out
}

// diagonalFraction is the yellow fraction of the pixels with s in
// [lo, hi), split into segments along the band from the bottom edge to the
// right edge; the whole and the least segment are returned, so a band that
// is yellow only at one end is not a ribbon.
func diagonalFraction(m [][]bool, lo, hi float64, segments int) (whole, least float64) {
	n := len(m)
	hit := make([]int, segments)
	all := make([]int, segments)
	for y := range n {
		for x := range n {
			s := float64(x+y) / float64(n)
			if s < lo || s >= hi {
				continue
			}
			// position along the band: how far from the bottom edge towards the right edge
			seg := min(segments-1, (x-y+n)*segments/(2*n))
			all[seg]++
			if m[y][x] {
				hit[seg]++
			}
		}
	}
	var h, a int
	least = 1
	for i := range segments {
		h += hit[i]
		a += all[i]
		if all[i] > 0 {
			least = min(least, float64(hit[i])/float64(all[i]))
		}
	}
	if a == 0 {
		return 0, 0
	}
	return float64(h) / float64(a), least
}

// The band has to be this yellow along its whole length, and the art on
// either side of it this much less so.
const (
	ribbonInside  = 0.45 // dark text takes up a fair share of the ribbon
	ribbonLeast   = 0.30
	ribbonOutside = 0.25
)

// findAudibleBanner reports whether a cover carries the ribbon, and where.
func findAudibleBanner(img image.Image) (bannerRegion, bool) {
	const n = 160
	m := yellowMap(img, n)
	var best bannerRegion
	found := false
	for centre := 1.25; centre <= 1.80; centre += 0.025 {
		for half := 0.05; half <= 0.13; half += 0.01 {
			inside, least := diagonalFraction(m, centre-half, centre+half, 4)
			if inside < ribbonInside || least < ribbonLeast {
				continue
			}
			before, _ := diagonalFraction(m, centre-half-0.12, centre-half-0.02, 1)
			after, _ := diagonalFraction(m, centre+half+0.02, min(2, centre+half+0.12), 1)
			outside := max(before, after)
			if outside > ribbonOutside {
				continue
			}
			score := inside - outside
			if !found || score > best.Inside-best.Outside {
				best = bannerRegion{Centre: centre, HalfWidth: half, Inside: inside, Outside: outside}
				found = true
			}
		}
	}
	return best, found
}
