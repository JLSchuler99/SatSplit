package httpapi

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"

	"satsplit/internal/ocr"
)

// Receipt photos are shown to everyone with the share link and may be sent to
// a hosted vision model, so they are stored and served without the metadata
// a camera adds: EXIF (which holds GPS position, time and device), XMP, IPTC,
// comments and text chunks. The pixels are not re-encoded.
//
// JPEG orientation is the one exception. Browsers and the OCR step use it to
// turn the photo upright, so it is written back as an EXIF block that holds
// only that tag.

var errMalformedImage = errors.New("image structure could not be read")

// metadataSafe reports whether a stored file of this kind can be served
// without location metadata: either stripImageMetadata cleans it, or the
// format has no standard place for EXIF. TIFF is itself an EXIF container
// and an HEIC whose conversion failed is kept as uploaded, so neither is.
func metadataSafe(kind imageKind) bool {
	switch kind.Ext {
	case ".jpg", ".png", ".webp", ".gif", ".bmp":
		return true
	}
	return false
}

// stripImageMetadata returns data without its metadata. Formats it does not
// clean are returned unchanged; use metadataSafe to tell them apart.
func stripImageMetadata(data []byte, kind imageKind) ([]byte, error) {
	switch kind.Ext {
	case ".jpg":
		return stripJPEGMetadata(data)
	case ".png":
		return stripPNGMetadata(data)
	case ".webp":
		return stripWebPMetadata(data)
	}
	return data, nil
}

// stripImageMetadataFile rewrites the image at path without its metadata.
func stripImageMetadataFile(path string) error {
	kind, ok := sniffImageFile(path)
	if !ok {
		return errNotAnImage
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	clean, err := stripImageMetadata(data, kind)
	if err != nil {
		return err
	}
	if bytes.Equal(clean, data) {
		return nil
	}
	tmp := path + ".clean"
	if err := os.WriteFile(tmp, clean, 0o644); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// stripJPEGMetadata keeps the segments needed to decode and colour the
// image (JFIF, ICC profile, Adobe and every non-APP marker) and drops all
// other APPn segments and comments. Anything after the end-of-image marker,
// such as the extra pictures and gain maps phones append, is dropped too.
func stripJPEGMetadata(data []byte) ([]byte, error) {
	if len(data) < 4 || data[0] != 0xFF || data[1] != 0xD8 {
		return nil, fmt.Errorf("jpeg: %w: no start marker", errMalformedImage)
	}
	out := make([]byte, 0, len(data))
	out = append(out, 0xFF, 0xD8)
	// The orientation block goes after any leading JFIF segment, which by
	// that standard must come first.
	insertAt := len(out)
	leading := true
	orientation := 0

	i := 2
	for {
		if i >= len(data) || data[i] != 0xFF {
			return nil, fmt.Errorf("jpeg: %w: expected a marker at byte %d", errMalformedImage, i)
		}
		for i < len(data) && data[i] == 0xFF {
			i++ // fill bytes
		}
		if i >= len(data) {
			return nil, fmt.Errorf("jpeg: %w: truncated", errMalformedImage)
		}
		marker := data[i]
		i++

		if marker == 0xD9 {
			out = append(out, 0xFF, 0xD9)
			break
		}
		if marker == 0x01 || (marker >= 0xD0 && marker <= 0xD7) {
			out = append(out, 0xFF, marker)
			leading = false
			continue
		}
		if i+2 > len(data) {
			return nil, fmt.Errorf("jpeg: %w: truncated", errMalformedImage)
		}
		segLen := int(data[i])<<8 | int(data[i+1])
		if segLen < 2 || i+segLen > len(data) {
			return nil, fmt.Errorf("jpeg: %w: bad segment length", errMalformedImage)
		}
		segment := data[i : i+segLen]
		payload := segment[2:]
		i += segLen

		if marker == 0xE1 && orientation == 0 {
			orientation = ocr.EXIFOrientation(payload)
		}
		if !keepJPEGSegment(marker, payload) {
			continue
		}
		out = append(out, 0xFF, marker)
		out = append(out, segment...)
		if leading && marker == 0xE0 {
			insertAt = len(out)
		} else {
			leading = false
		}

		if marker == 0xDA {
			// Entropy-coded data runs until a marker that is neither a
			// stuffed 0xFF00 nor a restart marker.
			start := i
			for {
				if i+1 >= len(data) {
					return nil, fmt.Errorf("jpeg: %w: scan data truncated", errMalformedImage)
				}
				if data[i] == 0xFF {
					next := data[i+1]
					if next == 0x00 || (next >= 0xD0 && next <= 0xD7) {
						i += 2
						continue
					}
					break
				}
				i++
			}
			out = append(out, data[start:i]...)
		}
	}

	if orientation > 1 {
		block := exifOrientationSegment(orientation)
		out = append(out[:insertAt], append(block, out[insertAt:]...)...)
	}
	return out, nil
}

// keepJPEGSegment reports whether a segment is needed to show the image.
func keepJPEGSegment(marker byte, payload []byte) bool {
	switch {
	case marker == 0xE0: // JFIF
		return true
	case marker == 0xE2: // ICC profile; other APP2 data (such as MPF) is dropped
		return bytes.HasPrefix(payload, []byte("ICC_PROFILE\x00"))
	case marker == 0xEE: // Adobe colour transform
		return true
	case marker >= 0xE0 && marker <= 0xEF: // EXIF, XMP, IPTC and the rest
		return false
	case marker == 0xFE: // comment
		return false
	}
	return true
}

// exifOrientationSegment builds an APP1 EXIF segment whose only entry is
// the orientation tag.
func exifOrientationSegment(orientation int) []byte {
	tiff := []byte{
		'M', 'M', 0x00, 0x2A, // big-endian TIFF header
		0x00, 0x00, 0x00, 0x08, // IFD0 offset
		0x00, 0x01, // one entry
		0x01, 0x12, // tag: Orientation
		0x00, 0x03, // type: SHORT
		0x00, 0x00, 0x00, 0x01, // count
		0x00, byte(orientation), 0x00, 0x00, // value, left-justified
		0x00, 0x00, 0x00, 0x00, // no next IFD
	}
	payload := append([]byte("Exif\x00\x00"), tiff...)
	segLen := len(payload) + 2
	seg := []byte{0xFF, 0xE1, byte(segLen >> 8), byte(segLen)}
	return append(seg, payload...)
}

var pngSignature = []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}

// stripPNGMetadata drops the EXIF, text and timestamp chunks and anything
// after IEND. Chunks are copied whole, so their CRCs stay valid.
func stripPNGMetadata(data []byte) ([]byte, error) {
	if !bytes.HasPrefix(data, pngSignature) {
		return nil, fmt.Errorf("png: %w: no signature", errMalformedImage)
	}
	out := make([]byte, 0, len(data))
	out = append(out, pngSignature...)
	i := len(pngSignature)
	for {
		if i+8 > len(data) {
			return nil, fmt.Errorf("png: %w: truncated", errMalformedImage)
		}
		length := int(binary.BigEndian.Uint32(data[i : i+4]))
		end := i + 12 + length
		if length < 0 || end > len(data) || end < i {
			return nil, fmt.Errorf("png: %w: bad chunk length", errMalformedImage)
		}
		typ := string(data[i+4 : i+8])
		switch typ {
		case "eXIf", "tEXt", "zTXt", "iTXt", "tIME":
		default:
			out = append(out, data[i:end]...)
		}
		i = end
		if typ == "IEND" {
			return out, nil
		}
	}
}

// VP8X feature flags that announce metadata chunks.
const (
	webpFlagEXIF = 0x08
	webpFlagXMP  = 0x04
)

// stripWebPMetadata drops the EXIF and XMP chunks, clears the flags that
// announce them, and fixes the RIFF size.
func stripWebPMetadata(data []byte) ([]byte, error) {
	if len(data) < 12 || string(data[0:4]) != "RIFF" || string(data[8:12]) != "WEBP" {
		return nil, fmt.Errorf("webp: %w: no RIFF header", errMalformedImage)
	}
	riffEnd := 8 + int(binary.LittleEndian.Uint32(data[4:8]))
	if riffEnd > len(data) || riffEnd < 12 {
		return nil, fmt.Errorf("webp: %w: bad RIFF size", errMalformedImage)
	}
	out := make([]byte, 0, len(data))
	out = append(out, data[:12]...)
	i := 12
	for i < riffEnd {
		if i+8 > riffEnd {
			return nil, fmt.Errorf("webp: %w: truncated chunk header", errMalformedImage)
		}
		fourcc := string(data[i : i+4])
		size := int(binary.LittleEndian.Uint32(data[i+4 : i+8]))
		end := i + 8 + size + size%2 // chunks are padded to an even length
		if size < 0 || end > riffEnd || end < i {
			return nil, fmt.Errorf("webp: %w: bad chunk size", errMalformedImage)
		}
		switch fourcc {
		case "EXIF", "XMP ":
		case "VP8X":
			chunk := append([]byte(nil), data[i:end]...)
			if size > 0 {
				chunk[8] &^= webpFlagEXIF | webpFlagXMP
			}
			out = append(out, chunk...)
		default:
			out = append(out, data[i:end]...)
		}
		i = end
	}
	binary.LittleEndian.PutUint32(out[4:8], uint32(len(out)-8))
	return out, nil
}
