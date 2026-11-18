package httpapi

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"satsplit/internal/ocr"
	"satsplit/internal/store"
)

// secret stands in for the GPS position and device details a camera writes.
const secret = "GPS 51.5007N 0.1246W iPhone"

func testImage() image.Image {
	img := image.NewRGBA(image.Rect(0, 0, 24, 16))
	for y := 0; y < 16; y++ {
		for x := 0; x < 24; x++ {
			img.Set(x, y, color.RGBA{uint8(x * 10), uint8(y * 15), 128, 255})
		}
	}
	return img
}

func encodeJPEG(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, testImage(), &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func jpegSegment(marker byte, payload []byte) []byte {
	n := len(payload) + 2
	return append([]byte{0xFF, marker, byte(n >> 8), byte(n)}, payload...)
}

// exifPayload is an APP1 EXIF payload with an orientation tag and, after the
// IFD, the secret text standing in for the GPS block.
func exifPayload(orientation int) []byte {
	tiff := []byte{
		'I', 'I', 0x2A, 0x00, 0x08, 0x00, 0x00, 0x00,
		0x01, 0x00,
		0x12, 0x01, 0x03, 0x00, 0x01, 0x00, 0x00, 0x00, byte(orientation), 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00,
	}
	return append(append([]byte("Exif\x00\x00"), tiff...), secret...)
}

// withSegments inserts segments right after the JPEG start marker.
func withSegments(jpg []byte, segments ...[]byte) []byte {
	out := append([]byte(nil), jpg[:2]...)
	for _, s := range segments {
		out = append(out, s...)
	}
	return append(out, jpg[2:]...)
}

// app1Orientation returns the orientation in the first APP1 segment of a
// JPEG, as the OCR step reads it, or 0 when there is none.
func app1Orientation(jpg []byte) int {
	i := 2
	for i+4 <= len(jpg) && jpg[i] == 0xFF {
		marker := jpg[i+1]
		if marker == 0xDA {
			return 0
		}
		n := int(jpg[i+2])<<8 | int(jpg[i+3])
		if marker == 0xE1 {
			return ocr.EXIFOrientation(jpg[i+4 : i+2+n])
		}
		i += 2 + n
	}
	return 0
}

func samePixels(t *testing.T, a, b image.Image) {
	t.Helper()
	if a.Bounds() != b.Bounds() {
		t.Fatalf("bounds %v != %v", a.Bounds(), b.Bounds())
	}
	for y := a.Bounds().Min.Y; y < a.Bounds().Max.Y; y++ {
		for x := a.Bounds().Min.X; x < a.Bounds().Max.X; x++ {
			if a.At(x, y) != b.At(x, y) {
				t.Fatalf("pixel (%d,%d) changed", x, y)
			}
		}
	}
}

func TestStripJPEGMetadata(t *testing.T) {
	plain := encodeJPEG(t)
	original, err := jpeg.Decode(bytes.NewReader(plain))
	if err != nil {
		t.Fatal(err)
	}
	icc := jpegSegment(0xE2, append([]byte("ICC_PROFILE\x00\x01\x01"), "fake-profile"...))

	cases := []struct {
		name            string
		in              []byte
		wantOrientation int
		wantKept        [][]byte
	}{
		{
			name: "exif, xmp, iptc, comment and trailer removed, orientation kept",
			in: append(withSegments(plain,
				jpegSegment(0xE1, exifPayload(6)),
				jpegSegment(0xE1, append([]byte("http://ns.adobe.com/xap/1.0/\x00"), secret...)),
				jpegSegment(0xED, append([]byte("Photoshop 3.0\x00"), secret...)),
				jpegSegment(0xFE, []byte(secret)),
			), secret...),
			wantOrientation: 6,
		},
		{
			name:            "upright photo gets no exif block",
			in:              withSegments(plain, jpegSegment(0xE1, exifPayload(1))),
			wantOrientation: 0,
		},
		{
			name: "icc profile kept, multi-picture index dropped",
			in: withSegments(plain,
				icc,
				jpegSegment(0xE2, append([]byte("MPF\x00"), secret...)),
			),
			wantKept: [][]byte{icc},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, err := stripJPEGMetadata(c.in)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(out, []byte(secret)) {
				t.Fatalf("metadata survived")
			}
			if got := app1Orientation(out); got != c.wantOrientation {
				t.Fatalf("orientation = %d, want %d", got, c.wantOrientation)
			}
			for _, k := range c.wantKept {
				if !bytes.Contains(out, k) {
					t.Fatalf("segment %q was dropped", k[4:16])
				}
			}
			if !bytes.HasSuffix(out, []byte{0xFF, 0xD9}) {
				t.Fatalf("output does not end at the end-of-image marker")
			}
			decoded, err := jpeg.Decode(bytes.NewReader(out))
			if err != nil {
				t.Fatalf("output does not decode: %v", err)
			}
			samePixels(t, original, decoded)
		})
	}
}

// Scan data may hold 0xFF bytes: stuffed as FF 00, or restart markers. They
// must be copied, and a segment after the scan must still be examined.
func TestStripJPEGMetadataScanData(t *testing.T) {
	sos := jpegSegment(0xDA, []byte{0x01, 0x01, 0x00, 0x00, 0x3F, 0x00})
	scan := []byte{0x12, 0xFF, 0x00, 0x34, 0xFF, 0xD0, 0x56}
	in := []byte{0xFF, 0xD8}
	in = append(in, sos...)
	in = append(in, scan...)
	in = append(in, jpegSegment(0xFE, []byte(secret))...)
	in = append(in, 0xFF, 0xFF, 0xD9) // a fill byte before EOI

	out, err := stripJPEGMetadata(in)
	if err != nil {
		t.Fatal(err)
	}
	want := append(append(append([]byte{0xFF, 0xD8}, sos...), scan...), 0xFF, 0xD9)
	if !bytes.Equal(out, want) {
		t.Fatalf("got % x\nwant % x", out, want)
	}
}

func TestStripJPEGMetadataRejectsBrokenFiles(t *testing.T) {
	plain := encodeJPEG(t)
	for name, in := range map[string][]byte{
		"truncated":        plain[:len(plain)/2],
		"bad length":       {0xFF, 0xD8, 0xFF, 0xE1, 0xFF, 0xFF, 0x00},
		"not a jpeg":       []byte("hello"),
		"junk after SOI":   {0xFF, 0xD8, 0x00, 0x00},
		"scan without end": append(append([]byte{0xFF, 0xD8}, jpegSegment(0xDA, []byte{1})...), 0x12, 0x34),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := stripJPEGMetadata(in); err == nil {
				t.Fatalf("expected an error")
			}
		})
	}
}

func pngChunk(typ string, data []byte) []byte {
	out := binary.BigEndian.AppendUint32(nil, uint32(len(data)))
	out = append(out, typ...)
	out = append(out, data...)
	return binary.BigEndian.AppendUint32(out, crc32.ChecksumIEEE(append([]byte(typ), data...)))
}

func TestStripPNGMetadata(t *testing.T) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, testImage()); err != nil {
		t.Fatal(err)
	}
	plain := buf.Bytes()
	// Insert metadata chunks after IHDR (8-byte signature + 25-byte chunk).
	phys := pngChunk("pHYs", []byte{0, 0, 0x0B, 0x13, 0, 0, 0x0B, 0x13, 1})
	var in []byte
	in = append(in, plain[:33]...)
	in = append(in, pngChunk("eXIf", exifPayload(6)[6:])...)
	in = append(in, pngChunk("tEXt", append([]byte("Comment\x00"), secret...))...)
	in = append(in, pngChunk("iTXt", append([]byte("XML:com.adobe.xmp\x00\x00\x00\x00\x00"), secret...))...)
	in = append(in, phys...)
	in = append(in, plain[33:]...)
	in = append(in, secret...) // trailing data

	out, err := stripPNGMetadata(in)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(out, []byte(secret)) {
		t.Fatalf("metadata survived")
	}
	if !bytes.Contains(out, phys) {
		t.Fatalf("pHYs chunk was dropped")
	}
	decoded, err := png.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("output does not decode: %v", err)
	}
	samePixels(t, testImage(), decoded)

	if _, err := stripPNGMetadata(plain[:40]); err == nil {
		t.Fatalf("truncated png: expected an error")
	}
}

func riffChunk(fourcc string, data []byte) []byte {
	out := append([]byte(fourcc), binary.LittleEndian.AppendUint32(nil, uint32(len(data)))...)
	out = append(out, data...)
	if len(data)%2 == 1 {
		out = append(out, 0)
	}
	return out
}

func webpFile(chunks ...[]byte) []byte {
	var body []byte
	for _, c := range chunks {
		body = append(body, c...)
	}
	out := append([]byte("RIFF"), binary.LittleEndian.AppendUint32(nil, uint32(4+len(body)))...)
	out = append(out, "WEBP"...)
	return append(out, body...)
}

func TestStripWebPMetadata(t *testing.T) {
	const flagAlpha = 0x10
	vp8x := []byte{flagAlpha | webpFlagEXIF | webpFlagXMP, 0, 0, 0, 23, 0, 0, 15, 0, 0}
	pixels := riffChunk("VP8L", []byte("pixels-odd"+"x")) // odd length, padded
	in := webpFile(
		riffChunk("VP8X", vp8x),
		pixels,
		riffChunk("EXIF", exifPayload(6)[6:]),
		riffChunk("XMP ", []byte(secret+"!")), // odd length, padded
	)
	in = append(in, secret...) // trailing data outside the RIFF

	out, err := stripWebPMetadata(in)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(out, []byte(secret)) {
		t.Fatalf("metadata survived")
	}
	wantVP8X := append([]byte(nil), vp8x...)
	wantVP8X[0] = flagAlpha
	want := webpFile(riffChunk("VP8X", wantVP8X), pixels)
	if !bytes.Equal(out, want) {
		t.Fatalf("got  % x\nwant % x", out, want)
	}

	if _, err := stripWebPMetadata(in[:30]); err == nil {
		t.Fatalf("truncated webp: expected an error")
	}
}

// The share link exposes the photo to every guest, so what they download
// must not say where it was taken, including for receipts saved before
// uploads were cleaned.
func TestReceiptImageServedWithoutMetadata(t *testing.T) {
	s, st := newTestServer(t)
	dir := t.TempDir()
	jpgPath := filepath.Join(dir, "old.jpg")
	if err := os.WriteFile(jpgPath, withSegments(encodeJPEG(t), jpegSegment(0xE1, exifPayload(8))), 0o644); err != nil {
		t.Fatal(err)
	}
	tiffPath := filepath.Join(dir, "scan.tiff")
	if err := os.WriteFile(tiffPath, append([]byte{'I', 'I', 0x2A, 0x00}, secret...), 0o644); err != nil {
		t.Fatal(err)
	}
	newReceipt := func(token, path string) {
		t.Helper()
		if _, err := st.CreateReceipt(store.Receipt{
			Token: token, HostToken: token + "-host", ImagePath: path,
			LightningAddress: "host@pay.example", ReceiptFilename: "r", CurrencyCode: "USD",
			CreatedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	newReceipt("jpgguest", jpgPath)
	newReceipt("tiffguest", tiffPath)

	get := func(token string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		s.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/receipt/"+token+"/image", nil))
		return rec
	}

	for _, token := range []string{"jpgguest", "jpgguest-host"} {
		rec := get(token)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d", token, rec.Code)
		}
		body := rec.Body.Bytes()
		if bytes.Contains(body, []byte(secret)) {
			t.Fatalf("%s: photo served with its metadata", token)
		}
		if got := app1Orientation(body); got != 8 {
			t.Fatalf("%s: orientation = %d, want 8", token, got)
		}
		if _, err := jpeg.Decode(bytes.NewReader(body)); err != nil {
			t.Fatalf("%s: served photo does not decode: %v", token, err)
		}
	}

	if rec := get("tiffguest"); rec.Code != http.StatusNotFound {
		t.Fatalf("guest got a TIFF it cannot be cleaned of metadata: status %d", rec.Code)
	}
	if rec := get("tiffguest-host"); rec.Code != http.StatusOK {
		t.Fatalf("host could not see their own TIFF: status %d", rec.Code)
	}
}

func TestPersistReceiptStripsMetadata(t *testing.T) {
	s, _ := newTestServer(t)
	upload := func(t *testing.T, data []byte) (string, error) {
		t.Helper()
		var body bytes.Buffer
		mw := multipart.NewWriter(&body)
		fw, err := mw.CreateFormFile("receipt", "receipt.jpg")
		if err != nil {
			t.Fatal(err)
		}
		fw.Write(data)
		mw.Close()
		req := httptest.NewRequest(http.MethodPost, "/", &body)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		file, header, err := req.FormFile("receipt")
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		return s.persistReceiptToDisk(file, header, "tok")
	}

	path, err := upload(t, withSegments(encodeJPEG(t), jpegSegment(0xE1, exifPayload(6))))
	if err != nil {
		t.Fatal(err)
	}
	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(stored, []byte(secret)) {
		t.Fatalf("stored photo kept its metadata")
	}
	if got := app1Orientation(stored); got != 6 {
		t.Fatalf("stored orientation = %d, want 6", got)
	}

	plain := encodeJPEG(t)
	if _, err := upload(t, plain[:len(plain)/2]); err != errNotAnImage {
		t.Fatalf("truncated jpeg: err = %v, want errNotAnImage", err)
	}
	if _, err := os.Stat(filepath.Join(s.uploadDir, "tok.jpg")); !os.IsNotExist(err) {
		t.Fatalf("refused upload was left on disk: %v", err)
	}
}
