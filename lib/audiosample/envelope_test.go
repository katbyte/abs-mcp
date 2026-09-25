package audiosample

import (
	"math"
	"math/cmplx"
	"math/rand/v2"
	"slices"
	"testing"
)

// speech is a stand-in for a narrator: noise shaped by a random run of
// syllables, gaps and pauses, from a fixed seed. Two seeds are two readers;
// one seed is one recording.
func speech(seed uint64, seconds float64, rate int) []int16 {
	return shaped(syllables(seed, seconds), rate, seed+1000)
}

// syllables is a loudness track, one level per millisecond: syllables of
// 80-300 ms at varying strength, short gaps between them, and now and then
// the pause of a sentence's end.
func syllables(seed uint64, seconds float64) []float64 {
	r := rand.New(rand.NewPCG(seed, seed)) //nolint:gosec // a fixed seed: the same made-up reading every run
	n := int(seconds * 1000)
	out := make([]float64, 0, n)
	for len(out) < n {
		level, length := 0.3+0.7*r.Float64(), 80+r.IntN(220)
		for range length {
			out = append(out, level)
		}
		gap := 30 + r.IntN(120)
		if r.IntN(8) == 0 {
			gap = 300 + r.IntN(500)
		}
		for range gap {
			out = append(out, 0.02)
		}
	}
	return out[:n]
}

// shaped is noise at rate whose loudness follows track, one level per
// millisecond: the same track with another noise seed is the same reading
// through another encoder.
func shaped(track []float64, rate int, noise uint64) []int16 {
	r := rand.New(rand.NewPCG(noise, noise)) //nolint:gosec // a fixed seed: the same noise every run
	out := make([]int16, len(track)*rate/1000)
	for i := range out {
		level := track[min(i*1000/rate, len(track)-1)]
		out[i] = int16(max(-32767, min(32767, r.NormFloat64()*6000*level)))
	}
	return out
}

// faster plays pcm at speed by linear interpolation: 1.02 is 2% shorter.
func faster(pcm []int16, speed float64) []int16 {
	var out []int16
	for i := 0; ; i++ {
		p := float64(i) * speed
		j := int(p)
		if j >= len(pcm)-1 {
			return out
		}
		f := p - float64(j)
		out = append(out, int16(float64(pcm[j])*(1-f)+float64(pcm[j+1])*f))
	}
}

// A steady tone's envelope is its RMS, logged: a sine of amplitude A has an
// RMS of A/sqrt(2); and digital silence is a finite floor, not minus infinity.
func TestEnvelopeIsLogRMS(t *testing.T) {
	t.Parallel()

	const rate, window = 4000, 200
	tone := make([]int16, rate) // one second of 400 Hz, ten whole cycles a window
	for i := range tone {
		tone[i] = int16(10000 * math.Sin(2*math.Pi*400*float64(i)/rate))
	}
	env := Envelope(tone, window)
	if len(env) != 20 {
		t.Fatalf("a second at 50 ms windows = %d values, want 20", len(env))
	}
	want := math.Log(10000 / math.Sqrt2)
	for i, v := range env {
		if math.Abs(v-want) > 0.01 {
			t.Errorf("window %d = %.4f, want %.4f", i, v, want)
		}
	}
	if silent := Envelope(make([]int16, 400), window); len(silent) != 2 || math.IsInf(silent[0], 0) || silent[0] > -6 {
		t.Errorf("silence = %v, want two windows at the finite floor", silent)
	}
	if got := Envelope(tone[:199], window); len(got) != 0 {
		t.Errorf("less than a window = %v, want nothing", got)
	}
}

// The FFT agrees with the transform written out longhand, and the inverse
// undoes it.
func TestFFTMatchesTheLonghandTransform(t *testing.T) {
	t.Parallel()

	r := rand.New(rand.NewPCG(1, 2)) //nolint:gosec // a fixed seed: the same input every run
	const n = 64
	x := make([]complex128, n)
	for i := range x {
		x[i] = complex(r.NormFloat64(), r.NormFloat64())
	}
	got := append([]complex128(nil), x...)
	fft(got, false)
	for k := range n {
		var want complex128
		for j := range n {
			want += x[j] * cmplx.Exp(complex(0, -2*math.Pi*float64(j*k)/n))
		}
		if cmplx.Abs(got[k]-want) > 1e-9 {
			t.Fatalf("bin %d = %v, want %v", k, got[k], want)
		}
	}
	fft(got, true)
	for i := range x {
		if cmplx.Abs(got[i]-x[i]) > 1e-12 {
			t.Fatalf("round trip %d = %v, want %v", i, got[i], x[i])
		}
	}
}

// Match's FFT gives the correlation the definition gives: Pearson's r of the
// stretched needle against every stretch of the haystack, the best of them.
func TestMatchIsTheLonghandCorrelation(t *testing.T) {
	t.Parallel()

	r := rand.New(rand.NewPCG(3, 4)) //nolint:gosec // a fixed seed: the same input every run
	haystack := make([]float64, 400)
	for i := range haystack {
		haystack[i] = r.NormFloat64()
	}
	needle := slices.Clone(haystack[123:173]) // planted at 123, with noise on it
	for i := range needle {
		needle[i] += 0.5 * r.NormFloat64()
	}
	speeds := []float64{0.97, 1, 1.03}

	want := Found{Speed: 1}
	for _, sp := range speeds {
		s := stretch(needle, sp)
		for k := 0; k+len(s) <= len(haystack); k++ {
			if rv := pearson(s, haystack[k:k+len(s)]); rv > want.Score {
				want = Found{Score: rv, At: k, Speed: sp}
			}
		}
	}
	got := Match(needle, haystack, speeds)
	if math.Abs(got.Score-want.Score) > 1e-9 || got.At != want.At || got.Speed != want.Speed {
		t.Errorf("Match = %+v, want %+v", got, want)
	}
	if want.At != 123 {
		t.Errorf("the planted stretch was found at %d, want 123", want.At)
	}
}

func pearson(a, b []float64) float64 {
	var ma, mb float64
	for i := range a {
		ma += a[i]
		mb += b[i]
	}
	ma /= float64(len(a))
	mb /= float64(len(b))
	var sab, saa, sbb float64
	for i := range a {
		sab += (a[i] - ma) * (b[i] - mb)
		saa += (a[i] - ma) * (a[i] - ma)
		sbb += (b[i] - mb) * (b[i] - mb)
	}
	return sab / math.Sqrt(saa*sbb)
}

// The method on made-up speech: 30 s of one recording is found in another
// encoding of it, and in a copy playing 2% faster at the speed that undoes
// it, scoring like the same recordings of the zbooks sort (0.75-0.99); in
// another reader's recording it scores like another narrator (under 0.65).
// Flat audio matches nothing.
func TestMatchFindsARecordingAndNotAnother(t *testing.T) {
	t.Parallel()

	const rate, window, seconds = 4000, 200, 300
	track := syllables(7, seconds)
	a := shaped(track, rate, 1)
	needle := Envelope(a[100*rate:130*rate], window)
	speeds := Speeds(0.97, 1.03, 25)

	again := Match(needle, Envelope(shaped(track, rate, 2), window), speeds)
	if again.Score < 0.9 || again.At < 1990 || again.At > 2010 || again.Speed != 1 {
		t.Errorf("another encoding: %+v, want over 0.9 at 100 s (2000) at speed 1", again)
	}

	quick := Match(needle, Envelope(faster(shaped(track, rate, 3), 1.02), window), speeds)
	if quick.Score < 0.85 || math.Abs(quick.Speed-1.02) > 0.006 || math.Abs(float64(quick.At)-2000/1.02) > 10 {
		t.Errorf("2%% faster: %+v, want over 0.85 at speed 1.02 near 1961", quick)
	}

	other := Match(needle, Envelope(speech(8, seconds, rate), window), speeds)
	if other.Score > 0.6 {
		t.Errorf("another reader: %+v, want under 0.6", other)
	}

	if flat := Match(make([]float64, 600), Envelope(a, window), speeds); flat.Score != 0 {
		t.Errorf("a flat needle: %+v, want no match", flat)
	}
}

func TestSpeedsAreEvenlySpread(t *testing.T) {
	t.Parallel()

	s := Speeds(0.97, 1.03, 25)
	if len(s) != 25 || s[0] != 0.97 || math.Abs(s[12]-1) > 1e-12 || math.Abs(s[24]-1.03) > 1e-12 {
		t.Errorf("Speeds = %v, want 25 from 0.97 through 1 to 1.03", s)
	}
}
