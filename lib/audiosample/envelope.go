package audiosample

import (
	"math"
	"slices"
)

// Envelope is a stretch's loudness over time: the natural log of the RMS of
// each window of samples (200 samples at 4 kHz is 50 ms). Speech rises and
// falls with every syllable and pause, and that pattern survives a new
// encoding, a new bitrate and a small change of speed, where the samples
// themselves do not; two narrators reading the same words do not share it.
func Envelope(pcm []int16, window int) []float64 {
	e := NewEnveloper(window)
	e.Write(pcm)
	return e.Envelope()
}

// Enveloper builds an Envelope a chunk of samples at a time, for a stretch
// read with Stream.
type Enveloper struct {
	window int
	sum    float64
	n      int
	env    []float64
}

// NewEnveloper starts an envelope of the given window.
func NewEnveloper(window int) *Enveloper { return &Enveloper{window: window} }

// Write adds samples to the envelope.
func (e *Enveloper) Write(pcm []int16) {
	if e.window <= 0 {
		return
	}
	for _, v := range pcm {
		e.sum += float64(v) * float64(v)
		e.n++
		if e.n == e.window {
			// the floor keeps digital silence finite: log(1e-3), well under any voice
			e.env = append(e.env, math.Log(math.Sqrt(e.sum/float64(e.window)+1e-6)))
			e.sum, e.n = 0, 0
		}
	}
}

// Envelope is the envelope of the whole windows written so far.
func (e *Enveloper) Envelope() []float64 { return e.env }

// Speeds is n speed factors spread evenly from lo to hi, for Match.
func Speeds(lo, hi float64, n int) []float64 {
	if n < 2 {
		return []float64{(lo + hi) / 2}
	}
	out := make([]float64, n)
	for i := range out {
		out[i] = lo + (hi-lo)*float64(i)/float64(n-1)
	}
	return out
}

// Found is where a needle fits a haystack best.
type Found struct {
	// Score is the correlation of the needle with the stretch of haystack it
	// fits: 1 is the same shape, 0 no relation.
	Score float64
	// At is where that stretch starts, in haystack samples.
	At int
	// Speed is the speed the needle was played at to fit: 1.02 means the
	// haystack runs 2% faster than the needle's recording.
	Speed float64
}

// Match finds where needle best fits in haystack, both envelopes, trying the
// needle played at each speed. The score at each offset is the normalised
// cross-correlation (Pearson's r of the needle against the stretch under it),
// so a copy recorded louder or quieter scores as high as the original. The
// correlation at every offset comes from one FFT of the haystack and two per
// speed, which is what makes searching an hour of envelope for 30 seconds of
// it, at 25 speeds, take a fraction of a second rather than minutes.
func Match(needle, haystack, speeds []float64) Found {
	best := Found{Speed: 1}
	if len(needle) < 2 || len(haystack) < 2 {
		return best
	}

	// the haystack's own mean is taken out first: r does not change, and the
	// running sums below stay small
	var mean float64
	for _, v := range haystack {
		mean += v
	}
	mean /= float64(len(haystack))
	h := make([]float64, len(haystack))
	sums := make([]float64, len(h)+1)  // running sum, for each window's mean
	sums2 := make([]float64, len(h)+1) // and of squares, for its variance
	for i, v := range haystack {
		h[i] = v - mean
		sums[i+1] = sums[i] + h[i]
		sums2[i+1] = sums2[i] + h[i]*h[i]
	}

	size := 1
	for size < len(h) {
		size <<= 1
	}
	hf := make([]complex128, size)
	for i, v := range h {
		hf[i] = complex(v, 0)
	}
	fft(hf, false)
	buf := make([]complex128, size)

	for _, speed := range speeds {
		s := normalise(stretch(needle, speed))
		n := len(s)
		if n < 2 || n > len(h) {
			continue
		}
		// the correlation at every offset at once: the product of the
		// haystack's transform with the conjugate of the needle's. The
		// needle is zero past n, so offsets up to len(h)-n never wrap
		clear(buf)
		for i, v := range s {
			buf[i] = complex(v, 0)
		}
		fft(buf, false)
		for i := range buf {
			buf[i] = hf[i] * complex(real(buf[i]), -imag(buf[i]))
		}
		fft(buf, true)

		for k := 0; k+n <= len(h); k++ {
			m := (sums[k+n] - sums[k]) / float64(n)
			v := (sums2[k+n]-sums2[k])/float64(n) - m*m
			if v <= 1e-9 {
				continue // a flat stretch: nothing to correlate with
			}
			// the needle has mean 0 and deviation 1, so its dot product with
			// the stretch is n times their covariance
			r := real(buf[k]) / (float64(n) * math.Sqrt(v))
			if r > best.Score {
				best = Found{Score: min(r, 1), At: k, Speed: speed}
			}
		}
	}

	return best
}

// stretch resamples x as if played at speed: 1.02 takes every 1.02nd sample,
// by linear interpolation, and so is 2% shorter.
func stretch(x []float64, speed float64) []float64 {
	if speed <= 0 {
		return nil
	}
	out := make([]float64, 0, int(float64(len(x))/speed)+1)
	for i := 0; ; i++ {
		p := float64(i) * speed
		j := int(p)
		if j >= len(x)-1 {
			break
		}
		f := p - float64(j)
		out = append(out, x[j]*(1-f)+x[j+1]*f)
	}
	return out
}

// normalise scales x to mean 0 and standard deviation 1, or nil when it is
// flat: silence matches nothing.
func normalise(x []float64) []float64 {
	if len(x) == 0 {
		return nil
	}
	var mean float64
	for _, v := range x {
		mean += v
	}
	mean /= float64(len(x))
	var sq float64
	for _, v := range x {
		sq += (v - mean) * (v - mean)
	}
	sd := math.Sqrt(sq / float64(len(x)))
	if sd < 1e-6 {
		return nil
	}
	out := slices.Clone(x)
	for i := range out {
		out[i] = (out[i] - mean) / sd
	}
	return out
}
