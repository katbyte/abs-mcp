package audiosample

import "math"

// fft transforms x in place, whose length must be a power of two: the
// discrete Fourier transform, or with inverse the inverse transform, scaled
// by 1/len(x) so the two undo each other. Iterative radix-2 Cooley-Tukey;
// the standard library has none, and a few hundred transforms of 2^17 points
// a comparison is well within what this does in a second.
func fft(x []complex128, inverse bool) {
	n := len(x)
	if n < 2 {
		return
	}
	if n&(n-1) != 0 {
		panic("fft: length is not a power of two") // a caller's mistake, not data
	}

	// bit-reversed order, so each pass combines neighbours in place: j
	// counts up with its bits reversed as i counts up
	for i, j := 1, 0; i < n; i++ {
		bit := n >> 1
		for ; j&bit != 0; bit >>= 1 {
			j ^= bit
		}
		j ^= bit
		if i < j {
			x[i], x[j] = x[j], x[i]
		}
	}

	// the twiddles for the largest pass; smaller passes stride through them
	sign := -1.0
	if inverse {
		sign = 1
	}
	tw := make([]complex128, n/2)
	for k := range tw {
		sin, cos := math.Sincos(sign * 2 * math.Pi * float64(k) / float64(n))
		tw[k] = complex(cos, sin)
	}
	for size := 2; size <= n; size <<= 1 {
		half, step := size/2, n/size
		for start := 0; start < n; start += size {
			for k := range half {
				a, b := x[start+k], x[start+k+half]*tw[k*step]
				x[start+k], x[start+k+half] = a+b, a-b
			}
		}
	}

	if inverse {
		scale := complex(1/float64(n), 0)
		for i := range x {
			x[i] *= scale
		}
	}
}
