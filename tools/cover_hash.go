package tools

import (
	"image"
	"math"
	"math/bits"
	"slices"
)

// Perceptual hashes for cover art: is the picture on disk the same picture
// the store shows, whatever its size, compression or a shaved border? Two
// hashes are computed and both distances reported. pHash decides: the image
// is reduced to 32x32 grey, put through a discrete cosine transform, and the
// 64 lowest frequencies (the DC term aside) are compared with their median,
// so what survives is the broad shape of light and dark that a resize or a
// JPEG cannot change. dHash is the second opinion: 9x8 grey, each pixel
// against its right-hand neighbour. Neither is fast, and neither needs to be:
// a cover is hashed once, and a library holds a thousand of them.

// coverHash is one image's two hashes.
type coverHash struct {
	P uint64 // pHash: DCT of a 32x32 reduction, 64 low frequencies against their median
	D uint64 // dHash: 9x8 reduction, each pixel against its right-hand neighbour
}

// hashImage computes both hashes of an image.
func hashImage(img image.Image) coverHash {
	return coverHash{P: phash(img), D: dhash(img)}
}

// distance is how many bits two hashes differ in, for each hash.
func (h coverHash) distance(o coverHash) (p, d int) {
	return bits.OnesCount64(h.P ^ o.P), bits.OnesCount64(h.D ^ o.D)
}

// The distances at which two covers are read as one picture. A pHash within
// ten bits is the same image resized or recompressed; sixteen and more is
// another picture. dHash is looser about crops and tighter about colour, so
// it is only allowed to confirm.
const (
	samePictureP = 10
	samePictureD = 10
)

// samePicture reports whether two hashes are close enough to be one picture:
// pHash within its threshold, or pHash close to it and dHash agreeing.
func (h coverHash) samePicture(o coverHash) bool {
	p, d := h.distance(o)
	return p <= samePictureP || (p <= samePictureP+4 && d <= samePictureD)
}

// grayReduce box-filters an image down to w by h grey values in 0..255: every
// source pixel lands in exactly one cell and each cell is the mean of its
// pixels, so a 3000-pixel scan and a 300-pixel thumbnail reduce to the same
// grid. Luma is the usual 0.299 0.587 0.114 mix.
func grayReduce(img image.Image, w, h int) [][]float64 {
	b := img.Bounds()
	sw, sh := b.Dx(), b.Dy()
	sum := make([][]float64, h)
	n := make([][]float64, h)
	for y := range sum {
		sum[y] = make([]float64, w)
		n[y] = make([]float64, w)
	}
	if sw == 0 || sh == 0 {
		return sum
	}
	for sy := range sh {
		ty := min(sy*h/sh, h-1)
		for sx := range sw {
			tx := min(sx*w/sw, w-1)
			r, g, bl, _ := img.At(b.Min.X+sx, b.Min.Y+sy).RGBA()
			luma := (0.299*float64(r) + 0.587*float64(g) + 0.114*float64(bl)) / 257 // 16-bit channels to 0..255
			sum[ty][tx] += luma
			n[ty][tx]++
		}
	}
	for y := range sum {
		for x := range sum[y] {
			if n[y][x] > 0 {
				sum[y][x] /= n[y][x]
			}
		}
	}
	return sum
}

// dct2 is the two-dimensional DCT-II of a square grid, computed the plain
// way: every output coefficient is a double sum over every input cell. For
// a 32x32 grid that is a million multiplications, which is nothing.
func dct2(m [][]float64) [][]float64 {
	n := len(m)
	out := make([][]float64, n)
	cosTable := make([][]float64, n)
	for k := range cosTable {
		cosTable[k] = make([]float64, n)
		for i := range cosTable[k] {
			cosTable[k][i] = math.Cos(math.Pi * float64(k) * (2*float64(i) + 1) / (2 * float64(n)))
		}
	}
	for u := range n {
		out[u] = make([]float64, n)
		for v := range n {
			var s float64
			for y := range n {
				for x := range n {
					s += m[y][x] * cosTable[u][y] * cosTable[v][x]
				}
			}
			out[u][v] = s
		}
	}
	return out
}

// phash is the DCT hash: the 8x8 lowest frequencies of a 32x32 reduction,
// each bit saying whether that coefficient is above the median of the 64.
// The DC term at (0,0) is average brightness, which a scan's exposure sets,
// so it is left out of the median and always written as its own bit against
// the median too, which is the standard formulation.
func phash(img image.Image) uint64 {
	g := grayReduce(img, 32, 32)
	d := dct2(g)
	low := make([]float64, 0, 64)
	for u := range 8 {
		for v := range 8 {
			if u == 0 && v == 0 {
				continue
			}
			low = append(low, d[u][v])
		}
	}
	sorted := slices.Clone(low)
	slices.Sort(sorted)
	median := (sorted[len(sorted)/2-1] + sorted[len(sorted)/2]) / 2
	var h uint64
	for u := range 8 {
		for v := range 8 {
			h <<= 1
			if d[u][v] > median {
				h |= 1
			}
		}
	}
	return h
}

// dhash is the difference hash: a 9x8 reduction, one bit per pixel saying
// whether it is brighter than the pixel to its right.
func dhash(img image.Image) uint64 {
	g := grayReduce(img, 9, 8)
	var h uint64
	for y := range 8 {
		for x := range 8 {
			h <<= 1
			if g[y][x] > g[y][x+1] {
				h |= 1
			}
		}
	}
	return h
}
