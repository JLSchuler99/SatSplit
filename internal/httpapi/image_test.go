package httpapi

import "testing"

func TestSniffImage(t *testing.T) {
	pad := func(b []byte) []byte { return append(b, make([]byte, 32)...) }
	bmp := pad([]byte{'B', 'M', 0, 0, 0, 0, 0, 0, 0, 0, 54, 0, 0, 0, 40, 0, 0, 0})
	cases := []struct {
		name string
		head []byte
		ext  string
		ok   bool
	}{
		{"jpeg", pad([]byte{0xFF, 0xD8, 0xFF, 0xE0}), ".jpg", true},
		{"png", pad([]byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}), ".png", true},
		{"gif", pad([]byte("GIF89a")), ".gif", true},
		{"webp", pad([]byte("RIFF\x00\x00\x00\x00WEBPVP8 ")), ".webp", true},
		{"tiff", pad([]byte{'I', 'I', 0x2A, 0x00}), ".tiff", true},
		{"bmp", bmp, ".bmp", true},
		{"heic", pad([]byte("\x00\x00\x00\x18ftypheic")), ".heic", true},
		{"html", pad([]byte("<!doctype html><script>alert(1)</script>")), "", false},
		{"html named like bmp", pad([]byte("BM<html><script>alert(1)</script>")), "", false},
		{"svg", pad([]byte(`<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)">`)), "", false},
		{"riff but not webp", pad([]byte("RIFF\x00\x00\x00\x00WAVEfmt ")), "", false},
		{"empty", nil, "", false},
	}
	for _, c := range cases {
		kind, ok := sniffImage(c.head)
		if ok != c.ok || kind.Ext != c.ext {
			t.Errorf("%s: got (%q, %v), want (%q, %v)", c.name, kind.Ext, ok, c.ext, c.ok)
		}
	}
}
