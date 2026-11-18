package httpapi

import (
	"bytes"
	"io"
	"os"
)

// imageKind is a receipt image format, identified from the file's content.
type imageKind struct {
	Ext         string
	ContentType string
}

// sniffLen is how many leading bytes are needed to identify an image.
const sniffLen = 64

// sniffImage identifies a receipt image by its leading bytes. The file name
// and the type the browser sent are never trusted: a file is only accepted,
// and only served, as one of these image formats. Anything else, such as an
// HTML page named receipt.jpg, is rejected.
func sniffImage(head []byte) (imageKind, bool) {
	switch {
	case bytes.HasPrefix(head, []byte{0xFF, 0xD8, 0xFF}):
		return imageKind{".jpg", "image/jpeg"}, true
	case bytes.HasPrefix(head, []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}):
		return imageKind{".png", "image/png"}, true
	case bytes.HasPrefix(head, []byte("GIF87a")), bytes.HasPrefix(head, []byte("GIF89a")):
		return imageKind{".gif", "image/gif"}, true
	case len(head) >= 12 && bytes.Equal(head[0:4], []byte("RIFF")) && bytes.Equal(head[8:12], []byte("WEBP")):
		return imageKind{".webp", "image/webp"}, true
	case bytes.HasPrefix(head, []byte{'I', 'I', 0x2A, 0x00}), bytes.HasPrefix(head, []byte{'M', 'M', 0x00, 0x2A}):
		return imageKind{".tiff", "image/tiff"}, true
	case isBMPHeader(head):
		return imageKind{".bmp", "image/bmp"}, true
	case isHEICHeader(head):
		return imageKind{".heic", "image/heic"}, true
	}
	return imageKind{}, false
}

// isBMPHeader checks the "BM" magic and the size of the info header that
// follows the 14-byte file header, since two letters alone match plain text.
func isBMPHeader(head []byte) bool {
	if len(head) < 18 || head[0] != 'B' || head[1] != 'M' {
		return false
	}
	switch uint32(head[14]) | uint32(head[15])<<8 | uint32(head[16])<<16 | uint32(head[17])<<24 {
	case 12, 40, 52, 56, 64, 108, 124:
		return true
	}
	return false
}

// isHEICHeader reports whether the bytes start an HEIF container holding an
// HEVC image, the default iPhone photo format.
func isHEICHeader(head []byte) bool {
	if len(head) < 12 || string(head[4:8]) != "ftyp" {
		return false
	}
	switch string(head[8:12]) {
	case "heic", "heix", "hevc", "hevx", "heim", "heis", "mif1", "msf1":
		return true
	}
	return false
}

// readHead returns up to sniffLen leading bytes of r.
func readHead(r io.Reader) ([]byte, error) {
	head := make([]byte, sniffLen)
	n, err := io.ReadFull(r, head)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return nil, err
	}
	return head[:n], nil
}

// sniffImageFile identifies the image stored at path.
func sniffImageFile(path string) (imageKind, bool) {
	f, err := os.Open(path)
	if err != nil {
		return imageKind{}, false
	}
	defer f.Close()
	head, err := readHead(f)
	if err != nil {
		return imageKind{}, false
	}
	return sniffImage(head)
}
