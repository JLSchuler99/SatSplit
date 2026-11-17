package ocr

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func TestJPEGOrientationRotates(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 32, 16))
	for y := 0; y < 16; y++ {
		for x := 0; x < 32; x++ {
			if x < 16 {
				src.SetNRGBA(x, y, color.NRGBA{R: 230, A: 255})
			} else {
				src.SetNRGBA(x, y, color.NRGBA{B: 230, A: 255})
			}
		}
	}

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, src, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	raw := buf.Bytes()
	app1 := exifApp1(6)
	out := make([]byte, 0, len(raw)+len(app1))
	out = append(out, 0xFF, 0xD8)
	out = append(out, app1...)
	out = append(out, raw[2:]...)
	if got := jpegOrientation(out); got != 6 {
		t.Fatalf("orientation = %d", got)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "side.jpg")
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatal(err)
	}
	prepared, cleanup, err := PrepareImage(path, PrepOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	f, err := os.Open(prepared)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	if img.Bounds().Dx() != 16 || img.Bounds().Dy() != 32 {
		t.Fatalf("bounds = %v", img.Bounds())
	}
	r, _, _, _ := img.At(8, 2).RGBA()
	_, _, b, _ := img.At(8, 28).RGBA()
	if r>>8 < 150 || b>>8 < 150 {
		t.Fatalf("pixels rotated the wrong way: topR=%d bottomB=%d", r>>8, b>>8)
	}
}

func TestPrepareImageUpscales(t *testing.T) {
	src := image.NewGray(image.Rect(0, 0, 20, 10))
	for i := range src.Pix {
		if i%2 == 0 {
			src.Pix[i] = 20
		} else {
			src.Pix[i] = 220
		}
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "small.png")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, src); err != nil {
		t.Fatal(err)
	}
	f.Close()

	prepared, cleanup, err := PrepareImage(path, PrepOptions{MinLongEdge: 80, MaxLongEdge: 200, Enhance: true})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	in, err := os.Open(prepared)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	img, err := png.Decode(in)
	if err != nil {
		t.Fatal(err)
	}
	long := img.Bounds().Dx()
	if img.Bounds().Dy() > long {
		long = img.Bounds().Dy()
	}
	if long < 80 {
		t.Fatalf("long edge = %d", long)
	}
}

func exifApp1(orientation byte) []byte {
	// Little-endian TIFF with a single Orientation short.
	tiff := []byte{
		'I', 'I', 0x2A, 0x00,
		0x08, 0x00, 0x00, 0x00,
		0x01, 0x00,
		0x12, 0x01, // tag 0x0112
		0x03, 0x00, // short
		0x01, 0x00, 0x00, 0x00,
		orientation, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00,
	}
	payload := append([]byte("Exif\x00\x00"), tiff...)
	segLen := len(payload) + 2
	out := []byte{0xFF, 0xE1, byte(segLen >> 8), byte(segLen)}
	return append(out, payload...)
}
