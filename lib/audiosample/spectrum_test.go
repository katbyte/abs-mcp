package audiosample

import (
	"math"
	"math/rand/v2"
	"testing"
)

// voiced is a stand-in for a voice: the syllables of speech, each sung at
// its own pitch with harmonics, so the spectrum changes from syllable to
// syllable the way a voice's does. Two seeds are two readers whose rhythm
// and pitches both differ; one seed through another noise seed is the same
// recording through another encoder.
func voiced(seed uint64, seconds float64, noise uint64) []int16 {
	const rate = 4000
	r := rand.New(rand.NewPCG(seed, seed))    //nolint:gosec // a fixed seed: the same made-up reading every run
	nr := rand.New(rand.NewPCG(noise, noise)) //nolint:gosec // a fixed seed: the same noise every run
	out := make([]int16, int(seconds*float64(rate)))
	for i := 0; i < len(out); {
		level, length := 0.3+0.7*r.Float64(), (80+r.IntN(220))*rate/1000
		pitch := 90 + 200*r.Float64()
		for k := 0; k < length && i < len(out); k, i = k+1, i+1 {
			t := float64(k) / float64(rate)
			var v float64
			for h := 1.0; h*pitch < 0.45*float64(rate); h++ {
				v += math.Sin(2*math.Pi*h*pitch*t) / h
			}
			out[i] = int16(max(-32767, min(32767, 4000*level*v+nr.NormFloat64()*200)))
		}
		gap := (30 + r.IntN(120)) * rate / 1000
		if r.IntN(8) == 0 {
			gap = (300 + r.IntN(500)) * rate / 1000
		}
		for k := 0; k < gap && i < len(out); k, i = k+1, i+1 {
			out[i] = int16(nr.NormFloat64() * 200)
		}
	}
	return out
}

// A tone's energy lands in the band holding its frequency, and a frame of
// silence is the floor in every band.
func TestSpectrogramPutsAToneInItsBand(t *testing.T) {
	t.Parallel()

	const rate, frame, hop, bands = 4000, 256, 64, 16
	pcm := make([]int16, rate)
	for i := range pcm {
		pcm[i] = int16(10000 * math.Sin(2*math.Pi*1000*float64(i)/rate))
	}
	rows := Spectrogram(pcm, rate, frame, hop, bands)
	if want := (len(pcm)-frame)/hop + 1; len(rows) != want {
		t.Fatalf("%d frames, want %d", len(rows), want)
	}
	// 1000 Hz sits log-evenly between 100 and 1800: band 16*ln(10)/ln(18) = 12.7
	loudest := 0
	for b := range rows[5] {
		if rows[5][b] > rows[5][loudest] {
			loudest = b
		}
	}
	if loudest != 12 {
		t.Errorf("1 kHz is loudest in band %d, want 12: %v", loudest, rows[5])
	}

	flat := Spectrogram(make([]int16, rate), rate, frame, hop, bands)
	for b, v := range flat[0] {
		if math.Abs(v-math.Log(spectrumFloor)) > 1e-9 {
			t.Errorf("silence in band %d = %v, want the floor %v", b, v, math.Log(spectrumFloor))
		}
	}

	if Spectrogram(pcm[:100], rate, frame, hop, bands) != nil {
		t.Error("a stretch shorter than a frame has a spectrogram, want none")
	}
}

// The score is 1 for a spectrogram against itself, however it is scaled, and
// near 0 for two unrelated ones; flat bands are left out.
func TestSpectralScoreIsTheMeanBandCorrelation(t *testing.T) {
	t.Parallel()

	const rate, frame, hop, bands = 4000, 256, 64, 16
	a := Spectrogram(voiced(1, 10, 1), rate, frame, hop, bands)
	if s := SpectralScore(a, a); math.Abs(s-1) > 1e-9 {
		t.Errorf("against itself: %v, want 1", s)
	}
	louder := make([][]float64, len(a))
	for i := range a {
		louder[i] = make([]float64, len(a[i]))
		for b := range a[i] {
			louder[i][b] = a[i][b] + 3 // 20 dB up in every band
		}
	}
	if s := SpectralScore(a, louder); math.Abs(s-1) > 1e-9 {
		t.Errorf("against itself louder: %v, want 1", s)
	}
	if s := SpectralScore(a, Spectrogram(voiced(2, 10, 2), rate, frame, hop, bands)); math.Abs(s) > 0.25 {
		t.Errorf("against another reader: %v, want near 0", s)
	}
	if s := SpectralScore(a, Spectrogram(make([]int16, 10*rate), rate, frame, hop, bands)); s != 0 {
		t.Errorf("against silence: %v, want 0", s)
	}
	if s := SpectralScore(nil, a); s != 0 {
		t.Errorf("against nothing: %v, want 0", s)
	}
}

// At the spot the envelope found, the same recording through another
// encoder, and 2% faster at the speed that undoes it, scores high; another
// reader, however well the envelope lined it up, scores low.
func TestSpectralMatchTellsARecordingFromAnother(t *testing.T) {
	t.Parallel()

	const rate, window, seconds = 4000, 200, 240
	a := voiced(7, seconds, 1)
	needle := a[100*rate : 130*rate]
	speeds := Speeds(0.97, 1.03, 25)

	again := voiced(7, seconds, 2)
	found := Match(Envelope(needle, window), Envelope(again, window), speeds)
	if s := SpectralMatch(needle, again, rate, found, window); s < 0.8 {
		t.Errorf("another encoding at %+v: spectral %v, want over 0.8", found, s)
	}

	quick := stretchPCM(voiced(7, seconds, 3), 1.02)
	found = Match(Envelope(needle, window), Envelope(quick, window), speeds)
	if s := SpectralMatch(needle, quick, rate, found, window); s < 0.7 {
		t.Errorf("2%% faster at %+v: spectral %v, want over 0.7", found, s)
	}

	other := voiced(8, seconds, 4)
	found = Match(Envelope(needle, window), Envelope(other, window), speeds)
	if s := SpectralMatch(needle, other, rate, found, window); s > 0.35 {
		t.Errorf("another reader at %+v: spectral %v, want under 0.35", found, s)
	}

	if s := SpectralMatch(needle[:100], again, rate, found, window); s != 0 {
		t.Errorf("a needle shorter than two frames: %v, want 0", s)
	}
}
