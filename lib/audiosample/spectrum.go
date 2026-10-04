package audiosample

import (
	"math"
)

// The envelope says where two stretches line up and how alike their rhythm
// is; it says nothing of the voice. Two readers with the same pacing can
// correlate in loudness alone. The spectrum at the aligned spot settles it:
// a recording re-encoded, resampled or played a little faster keeps the same
// energy in the same frequency bands frame by frame, and another voice saying
// the same words does not, whatever its rhythm.

const (
	spectrumLowHz  = 100.0 // below this is hum and rumble, not voice
	spectrumShare  = 0.45  // the top band ends at this share of the rate, under Nyquist and the resampler's roll-off
	spectrumBands  = 16    // bands spaced evenly in pitch between the two
	spectrumFrameS = 0.064 // seconds a frame covers, rounded up to a power of two of samples
	spectrumChunkS = 8.0   // seconds of needle aligned on its own, so a speed found to a quarter percent does not drift it off
	spectrumFloor  = 1e-3  // keeps a silent band finite
)

// Spectrogram is the log energy of pcm in each of bands frequency bands, a
// row per frame: frames of frame samples every hop samples, Hann windowed,
// the bands spaced evenly in pitch from spectrumLowHz up to spectrumShare of
// the rate. Loudness is not taken out here; SpectralScore does that per band.
func Spectrogram(pcm []int16, rate, frame, hop, bands int) [][]float64 {
	if frame <= 0 || hop <= 0 || bands <= 0 || len(pcm) < frame {
		return nil
	}
	size := 1
	for size < frame {
		size <<= 1
	}
	hann := make([]float64, frame)
	for i := range hann {
		hann[i] = 0.5 - 0.5*math.Cos(2*math.Pi*float64(i)/float64(frame-1))
	}
	// bin b is b*rate/size Hz; the band edges in bins, spaced evenly in log
	// frequency
	edges := make([]int, bands+1)
	lo, hi := math.Log(spectrumLowHz), math.Log(spectrumShare*float64(rate))
	for b := range edges {
		hz := math.Exp(lo + (hi-lo)*float64(b)/float64(bands))
		edges[b] = int(math.Round(hz * float64(size) / float64(rate)))
	}
	for b := 1; b < len(edges); b++ {
		edges[b] = max(edges[b], edges[b-1]+1) // every band at least one bin
	}

	buf := make([]complex128, size)
	var out [][]float64
	for start := 0; start+frame <= len(pcm); start += hop {
		clear(buf)
		for i := range frame {
			buf[i] = complex(float64(pcm[start+i])*hann[i], 0)
		}
		fft(buf, false)
		row := make([]float64, bands)
		for b := range bands {
			var e float64
			for k := edges[b]; k < edges[b+1] && k < size/2; k++ {
				re, im := real(buf[k]), imag(buf[k])
				e += re*re + im*im
			}
			row[b] = math.Log(e/float64(frame) + spectrumFloor)
		}
		out = append(out, row)
	}

	return out
}

// SpectralScore says how alike two spectrograms of the same length are: in
// each band the energy over time is scaled to mean 0 and deviation 1, which
// takes out loudness and the encoder's or the microphone's colouring, and the
// score is the mean of the bands' correlations. 1 is the same shape in every
// band, 0 no relation. A band flat in either is left out; when all are, the
// score is 0.
func SpectralScore(a, b [][]float64) float64 {
	n := min(len(a), len(b))
	if n < 2 || len(a[0]) == 0 {
		return 0
	}
	bands := len(a[0])
	var sum float64
	counted := 0
	x, y := make([]float64, n), make([]float64, n)
	for band := range bands {
		for i := range n {
			x[i], y[i] = a[i][band], b[i][band]
		}
		nx, ny := normalise(x), normalise(y)
		if nx == nil || ny == nil {
			continue
		}
		var r float64
		for i := range n {
			r += nx[i] * ny[i]
		}
		sum += r / float64(n)
		counted++
	}
	if counted == 0 {
		return 0
	}

	return sum / float64(counted)
}

// SpectralMatch is the spectral score of needle against the stretch of
// haystack that Match found for it, both PCM at rate: found.At is in
// envelope windows of window samples, and found.Speed is the speed the
// needle was played at to fit. The needle is taken a chunk at a time, each
// chunk lined up on its own within a window either side of where the
// envelope put it, as the envelope's window is coarser than a frame and a
// speed found to a quarter of a percent drifts half a frame over a chunk.
// The score is the mean over the chunks.
func SpectralMatch(needle, haystack []int16, rate int, found Found, window int) float64 {
	if rate <= 0 || window <= 0 || len(needle) == 0 || len(haystack) == 0 {
		return 0
	}
	frame := 1
	for frame < int(spectrumFrameS*float64(rate)) {
		frame <<= 1
	}
	hop := frame / 4
	played := stretchPCM(needle, found.Speed)
	chunk := min(int(spectrumChunkS*float64(rate)), len(played))
	if chunk < 2*frame {
		return 0
	}

	var sum float64
	chunks := 0
	for c := 0; c+chunk <= len(played); c += chunk {
		want := Spectrogram(played[c:c+chunk], rate, frame, hop, spectrumBands)
		best := 0.0
		at := found.At*window + c
		for d := -window; d <= window; d += hop / 2 {
			start := at + d
			if start < 0 || start+chunk > len(haystack) {
				continue
			}
			got := Spectrogram(haystack[start:start+chunk], rate, frame, hop, spectrumBands)
			best = max(best, SpectralScore(want, got))
		}
		sum += best
		chunks++
	}
	if chunks == 0 {
		return 0
	}

	return sum / float64(chunks)
}

// stretchPCM plays pcm at speed by linear interpolation: 1.02 is 2% shorter.
// A crude resampler, but the bands are a fifth of an octave wide and the
// speeds within a few percent, so what it aliases is far above them.
func stretchPCM(pcm []int16, speed float64) []int16 {
	if speed <= 0 || speed == 1 {
		return pcm
	}
	out := make([]int16, 0, int(float64(len(pcm))/speed)+1)
	for i := 0; ; i++ {
		p := float64(i) * speed
		j := int(p)
		if j >= len(pcm)-1 {
			break
		}
		f := p - float64(j)
		out = append(out, int16(float64(pcm[j])*(1-f)+float64(pcm[j+1])*f))
	}
	return out
}
