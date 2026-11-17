package ocr

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/draw"
	_ "image/gif"
	"image/jpeg"
	"image/png"
	"math"
	"os"
)

// PrepOptions controls the local image normalization used before OCR or a
// vision model. Phone photos are often sideways, huge, and low-contrast;
// Tesseract's accuracy swings hard on all three.
type PrepOptions struct {
	MinLongEdge int
	MaxLongEdge int
	Enhance     bool
	JPEGQuality int
}

// PrepareImage decodes src, applies EXIF orientation, rescales, and optionally
// stretches contrast. The returned cleanup removes a temp file. cleanup is
// always non-nil when err is nil; it is a no-op when src is returned as-is.
func PrepareImage(src string, opt PrepOptions) (string, func(), error) {
	data, err := os.ReadFile(src)
	if err != nil {
		return "", nil, err
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return "", nil, err
	}
	orient := jpegOrientation(data)
	img = applyOrientation(img, orient)

	b := img.Bounds()
	long := b.Dx()
	if b.Dy() > long {
		long = b.Dy()
	}
	target := long
	if opt.MaxLongEdge > 0 && long > opt.MaxLongEdge {
		target = opt.MaxLongEdge
	} else if opt.MinLongEdge > 0 && long < opt.MinLongEdge {
		target = opt.MinLongEdge
	}
	if target != long && long > 0 {
		scale := float64(target) / float64(long)
		nw := int(math.Round(float64(b.Dx()) * scale))
		nh := int(math.Round(float64(b.Dy()) * scale))
		if nw < 1 {
			nw = 1
		}
		if nh < 1 {
			nh = 1
		}
		img = resizeBilinear(img, nw, nh)
	}

	var out image.Image = img
	if opt.Enhance {
		gray := image.NewGray(image.Rect(0, 0, img.Bounds().Dx(), img.Bounds().Dy()))
		draw.Draw(gray, gray.Bounds(), img, img.Bounds().Min, draw.Src)
		stretchContrast(gray)
		out = sharpenGray(gray)
	}

	ext := ".png"
	if opt.JPEGQuality > 0 {
		ext = ".jpg"
	}
	f, err := os.CreateTemp("", "satsplit-ocr-*"+ext)
	if err != nil {
		return "", nil, err
	}
	path := f.Name()
	cleanup := func() { _ = os.Remove(path) }
	if opt.JPEGQuality > 0 {
		err = jpeg.Encode(f, out, &jpeg.Options{Quality: opt.JPEGQuality})
	} else {
		err = (&png.Encoder{CompressionLevel: png.BestSpeed}).Encode(f, out)
	}
	closeErr := f.Close()
	if err != nil {
		cleanup()
		return "", nil, err
	}
	if closeErr != nil {
		cleanup()
		return "", nil, closeErr
	}
	return path, cleanup, nil
}

// RotateImageFile rotates a raster image clockwise by 90, 180, or 270 degrees
// and writes a new PNG. Other angles return the original path.
func RotateImageFile(src string, clockwise int) (string, func(), error) {
	noop := func() {}
	clockwise %= 360
	if clockwise < 0 {
		clockwise += 360
	}
	if clockwise == 0 {
		return src, noop, nil
	}
	f, err := os.Open(src)
	if err != nil {
		return "", nil, err
	}
	img, _, err := image.Decode(f)
	f.Close()
	if err != nil {
		return "", nil, err
	}
	switch clockwise {
	case 90:
		img = rotate90CW(img)
	case 180:
		img = rotate180(img)
	case 270:
		img = rotate270CW(img)
	default:
		return src, noop, nil
	}
	out, err := os.CreateTemp("", "satsplit-rot-*.png")
	if err != nil {
		return "", nil, err
	}
	path := out.Name()
	cleanup := func() { _ = os.Remove(path) }
	err = (&png.Encoder{CompressionLevel: png.BestSpeed}).Encode(out, img)
	closeErr := out.Close()
	if err != nil {
		cleanup()
		return "", nil, err
	}
	if closeErr != nil {
		cleanup()
		return "", nil, closeErr
	}
	return path, cleanup, nil
}

func jpegOrientation(data []byte) int {
	if len(data) < 4 || data[0] != 0xFF || data[1] != 0xD8 {
		return 1
	}
	i := 2
	for i+4 < len(data) {
		if data[i] != 0xFF {
			i++
			continue
		}
		for i < len(data) && data[i] == 0xFF {
			i++
		}
		if i >= len(data) {
			return 1
		}
		marker := data[i]
		i++
		if marker == 0xD9 || marker == 0xDA {
			return 1
		}
		if marker == 0x01 || (marker >= 0xD0 && marker <= 0xD7) {
			continue
		}
		if i+2 > len(data) {
			return 1
		}
		segLen := int(data[i])<<8 | int(data[i+1])
		if segLen < 2 || i+segLen > len(data) {
			return 1
		}
		if marker == 0xE1 {
			payload := data[i+2 : i+segLen]
			if o := orientationFromEXIF(payload); o >= 1 && o <= 8 {
				return o
			}
		}
		i += segLen
	}
	return 1
}

func orientationFromEXIF(payload []byte) int {
	if len(payload) < 8 || !bytes.HasPrefix(payload, []byte("Exif\x00\x00")) {
		return 0
	}
	return orientationFromTIFF(payload[6:])
}

func orientationFromTIFF(tiff []byte) int {
	if len(tiff) < 8 {
		return 0
	}
	var bo binary.ByteOrder
	switch string(tiff[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return 0
	}
	if bo.Uint16(tiff[2:4]) != 42 {
		return 0
	}
	offset := int(bo.Uint32(tiff[4:8]))
	if offset < 0 || offset+2 > len(tiff) {
		return 0
	}
	n := int(bo.Uint16(tiff[offset : offset+2]))
	entry := offset + 2
	for i := 0; i < n; i++ {
		if entry+12 > len(tiff) {
			return 0
		}
		tag := bo.Uint16(tiff[entry : entry+2])
		typ := bo.Uint16(tiff[entry+2 : entry+4])
		if tag == 0x0112 && typ == 3 {
			return int(bo.Uint16(tiff[entry+8 : entry+10]))
		}
		entry += 12
	}
	return 0
}

func applyOrientation(img image.Image, orientation int) image.Image {
	switch orientation {
	case 2:
		return flipH(img)
	case 3:
		return rotate180(img)
	case 4:
		return flipV(img)
	case 5:
		return rotate270CW(flipH(img))
	case 6:
		return rotate90CW(img)
	case 7:
		return rotate90CW(flipH(img))
	case 8:
		return rotate270CW(img)
	default:
		return img
	}
}

func rotate90CW(src image.Image) *image.NRGBA {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	dst := image.NewNRGBA(image.Rect(0, 0, h, w))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dst.Set(h-1-y, x, src.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return dst
}

func rotate270CW(src image.Image) *image.NRGBA {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	dst := image.NewNRGBA(image.Rect(0, 0, h, w))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dst.Set(y, w-1-x, src.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return dst
}

func rotate180(src image.Image) *image.NRGBA {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dst.Set(w-1-x, h-1-y, src.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return dst
}

func flipH(src image.Image) *image.NRGBA {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dst.Set(w-1-x, y, src.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return dst
}

func flipV(src image.Image) *image.NRGBA {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dst.Set(x, h-1-y, src.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return dst
}

func resizeBilinear(src image.Image, nw, nh int) *image.NRGBA {
	dst := image.NewNRGBA(image.Rect(0, 0, nw, nh))
	sb := src.Bounds()
	sw, sh := float64(sb.Dx()), float64(sb.Dy())
	for y := 0; y < nh; y++ {
		sy := (float64(y)+0.5)*sh/float64(nh) - 0.5
		for x := 0; x < nw; x++ {
			sx := (float64(x)+0.5)*sw/float64(nw) - 0.5
			dst.SetNRGBA(x, y, sampleBilinear(src, sb, sx, sy))
		}
	}
	return dst
}

func sampleBilinear(src image.Image, b image.Rectangle, sx, sy float64) color.NRGBA {
	x0 := int(math.Floor(sx))
	y0 := int(math.Floor(sy))
	x1 := x0 + 1
	y1 := y0 + 1
	dx := sx - float64(x0)
	dy := sy - float64(y0)
	c00 := nrgbaAt(src, b, x0, y0)
	c10 := nrgbaAt(src, b, x1, y0)
	c01 := nrgbaAt(src, b, x0, y1)
	c11 := nrgbaAt(src, b, x1, y1)
	blend := func(a, b, c, d uint8) uint8 {
		v := (1-dx)*(1-dy)*float64(a) + dx*(1-dy)*float64(b) + (1-dx)*dy*float64(c) + dx*dy*float64(d)
		if v < 0 {
			v = 0
		}
		if v > 255 {
			v = 255
		}
		return uint8(v + 0.5)
	}
	return color.NRGBA{
		R: blend(c00.R, c10.R, c01.R, c11.R),
		G: blend(c00.G, c10.G, c01.G, c11.G),
		B: blend(c00.B, c10.B, c01.B, c11.B),
		A: blend(c00.A, c10.A, c01.A, c11.A),
	}
}

func nrgbaAt(src image.Image, b image.Rectangle, x, y int) color.NRGBA {
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}
	if x >= b.Dx() {
		x = b.Dx() - 1
	}
	if y >= b.Dy() {
		y = b.Dy() - 1
	}
	r, g, bl, a := src.At(b.Min.X+x, b.Min.Y+y).RGBA()
	return color.NRGBA{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(bl >> 8), A: uint8(a >> 8)}
}

func stretchContrast(g *image.Gray) {
	if len(g.Pix) == 0 {
		return
	}
	var hist [256]int
	for _, p := range g.Pix {
		hist[p]++
	}
	total := len(g.Pix)
	loTarget := total / 100
	if loTarget < 1 {
		loTarget = 1
	}
	hiTarget := total - loTarget
	lo, hi := 0, 255
	acc := 0
	for i, c := range hist {
		acc += c
		if acc >= loTarget {
			lo = i
			break
		}
	}
	acc = 0
	for i, c := range hist {
		acc += c
		if acc >= hiTarget {
			hi = i
			break
		}
	}
	if hi <= lo+8 {
		return
	}
	span := hi - lo
	for i, p := range g.Pix {
		v := (int(p) - lo) * 255 / span
		if v < 0 {
			v = 0
		}
		if v > 255 {
			v = 255
		}
		g.Pix[i] = uint8(v)
	}
}

func sharpenGray(g *image.Gray) *image.Gray {
	b := g.Bounds()
	w, h := b.Dx(), b.Dy()
	out := image.NewGray(b)
	at := func(x, y int) int {
		if x < 0 {
			x = 0
		}
		if y < 0 {
			y = 0
		}
		if x >= w {
			x = w - 1
		}
		if y >= h {
			y = h - 1
		}
		return int(g.Pix[y*g.Stride+x])
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := at(x, y)
			blur := (at(x-1, y-1) + at(x, y-1) + at(x+1, y-1) +
				at(x-1, y) + c + at(x+1, y) +
				at(x-1, y+1) + at(x, y+1) + at(x+1, y+1)) / 9
			v := c + (c-blur)*2/3
			if v < 0 {
				v = 0
			}
			if v > 255 {
				v = 255
			}
			out.Pix[y*out.Stride+x] = uint8(v)
		}
	}
	return out
}
