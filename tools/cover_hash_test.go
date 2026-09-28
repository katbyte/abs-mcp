package tools

import (
	"image/jpeg"
	"strings"
	"testing"
)

// The hashes have to read one picture as one picture at any size and
// through a JPEG, and two pictures as two. The pictures are drawn from a
// formula over unit coordinates, so the same picture at 800 and 300 pixels
// is the same picture and not a resampling of it.
func TestCoverHashSamePictureAcrossSizes(t *testing.T) {
	t.Parallel()

	big, small := hashImage(artwork(800, 0)), hashImage(artwork(300, 0))
	if p, d := big.distance(small); !big.samePicture(small) {
		t.Errorf("the same picture at 800 and 300 pixels is %d/%d bits apart", p, d)
	}
	rough, err := jpeg.Decode(strings.NewReader(encodeJPEG(t, artwork(500, 0), 30)))
	if err != nil {
		t.Fatal(err)
	}
	if p, d := big.distance(hashImage(rough)); !big.samePicture(hashImage(rough)) {
		t.Errorf("the same picture through a rough JPEG is %d/%d bits apart", p, d)
	}
	other := hashImage(artwork(600, 0.9))
	if p, d := big.distance(other); big.samePicture(other) || p < 16 {
		t.Errorf("a different picture is only %d/%d bits apart", p, d)
	}
	if big != hashImage(artwork(800, 0)) {
		t.Error("hashing is not deterministic")
	}
}
