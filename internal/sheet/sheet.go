// Package sheet builds a contact sheet: one row per RAF, one labelled column per render, for
// comparing looks at a glance.
package sheet

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	stddraw "image/draw"
	_ "image/gif" // decoders for image.Decode
	"image/jpeg"
	_ "image/png"
	"io"
	"math"
	"os"
	"path/filepath"

	"golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

// Layout, as in the Python version: thumbnails fit in CellLong x CellLong, Pad between everything,
// and a Label band above each thumbnail.
const (
	CellLong = 720
	Pad      = 16
	Label    = 44
	// labelScale enlarges the 7x13 bitmap font to about the 26 px text of the Python sheet.
	labelScale = 2
	quality    = 90
)

var (
	background = color.RGBA{24, 24, 24, 255}
	textColour = color.RGBA{235, 235, 235, 255}
)

// Cell is one labelled image on the sheet.
type Cell struct {
	Label string
	Path  string
}

// ErrEmpty is returned when there is nothing to put on the sheet.
var ErrEmpty = errors.New("nothing to put on the sheet")

type thumb struct {
	label string
	img   image.Image
}

// ContactSheet writes rows of labelled thumbnails to out as a JPEG and returns out. Each image keeps
// its own orientation (EXIF orientation is applied) and aspect ratio. Empty rows are skipped.
func ContactSheet(rows [][]Cell, out string) (string, error) {
	var thumbs [][]thumb
	for _, row := range rows {
		if len(row) == 0 {
			continue
		}
		var line []thumb
		for _, c := range row {
			img, orientation, err := load(c.Path)
			if err != nil {
				return "", err
			}
			// Fitting in a square box first is the same as fitting after turning, and much cheaper.
			line = append(line, thumb{c.Label, orient(fit(img, CellLong), orientation)})
		}
		thumbs = append(thumbs, line)
	}
	if len(thumbs) == 0 {
		return "", ErrEmpty
	}
	colW, cols := 0, 0
	rowH := make([]int, len(thumbs))
	height := Pad
	for r, line := range thumbs {
		cols = max(cols, len(line))
		for _, t := range line {
			b := t.img.Bounds()
			colW = max(colW, b.Dx())
			rowH[r] = max(rowH[r], b.Dy())
		}
		height += rowH[r] + Label + Pad
	}
	width := Pad + cols*(colW+Pad)
	sheet := image.NewRGBA(image.Rect(0, 0, width, height))
	stddraw.Draw(sheet, sheet.Bounds(), image.NewUniform(background), image.Point{}, stddraw.Src)
	y := Pad
	for r, line := range thumbs {
		for c, t := range line {
			x := Pad + c*(colW+Pad)
			drawLabel(sheet, x, y+6, t.label, colW)
			b := t.img.Bounds()
			stddraw.Draw(sheet, image.Rect(x, y+Label, x+b.Dx(), y+Label+b.Dy()), t.img, b.Min, stddraw.Src)
		}
		y += rowH[r] + Label + Pad
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return "", err
	}
	f, err := os.Create(out)
	if err != nil {
		return "", err
	}
	w := bufio.NewWriter(f)
	err = jpeg.Encode(w, sheet, &jpeg.Options{Quality: quality})
	if err == nil {
		err = w.Flush()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", fmt.Errorf("cannot write %s: %w", out, err)
	}
	return out, nil
}

// load decodes an image and reads its EXIF orientation (1 when it has none).
func load(path string) (image.Image, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		return nil, 0, fmt.Errorf("cannot read %s as an image: %w", path, err)
	}
	orientation := 1
	if _, err := f.Seek(0, io.SeekStart); err == nil {
		orientation = exifOrientation(f)
	}
	return img, orientation, nil
}

// fit shrinks an image to fit in long x long, keeping its aspect ratio; it never enlarges.
func fit(img image.Image, long int) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	scale := math.Min(1, math.Min(float64(long)/float64(w), float64(long)/float64(h)))
	nw := max(1, int(math.Round(float64(w)*scale)))
	nh := max(1, int(math.Round(float64(h)*scale)))
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	if nw == w && nh == h {
		stddraw.Draw(dst, dst.Bounds(), img, b.Min, stddraw.Src)
		return dst
	}
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Src, nil)
	return dst
}

// drawLabel writes text at (x, top) in the enlarged bitmap font, clipped to width.
func drawLabel(dst *image.RGBA, x, top int, text string, width int) {
	face := basicfont.Face7x13
	m := face.Metrics()
	adv := font.MeasureString(face, text).Ceil()
	h := (m.Ascent + m.Descent).Ceil()
	if adv <= 0 || h <= 0 {
		return
	}
	small := image.NewRGBA(image.Rect(0, 0, adv, h))
	d := font.Drawer{Dst: small, Src: image.NewUniform(textColour), Face: face, Dot: fixed.P(0, m.Ascent.Ceil())}
	d.DrawString(text)
	w := min(adv*labelScale, width)
	r := image.Rect(x, top, x+w, top+h*labelScale)
	// Scale the glyphs up without blurring them, then lay them over the background.
	big := image.NewRGBA(image.Rect(0, 0, adv*labelScale, h*labelScale))
	draw.NearestNeighbor.Scale(big, big.Bounds(), small, small.Bounds(), draw.Src, nil)
	stddraw.Draw(dst, r, big, image.Point{}, stddraw.Over)
}

// orient applies an EXIF orientation (1..8) so the image displays upright.
func orient(img image.Image, o int) image.Image {
	if o < 2 || o > 8 {
		return img
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	ow, oh := w, h
	if o >= 5 {
		ow, oh = h, w
	}
	dst := image.NewRGBA(image.Rect(0, 0, ow, oh))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var dx, dy int
			switch o {
			case 2: // mirror horizontal
				dx, dy = w-1-x, y
			case 3: // rotate 180
				dx, dy = w-1-x, h-1-y
			case 4: // mirror vertical
				dx, dy = x, h-1-y
			case 5: // transpose
				dx, dy = y, x
			case 6: // rotate 90 clockwise
				dx, dy = h-1-y, x
			case 7: // transverse
				dx, dy = h-1-y, w-1-x
			case 8: // rotate 90 counter-clockwise
				dx, dy = y, w-1-x
			}
			dst.Set(dx, dy, img.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return dst
}

// exifOrientation reads the Orientation tag from a JPEG's EXIF segment; 1 when there is none.
func exifOrientation(r io.Reader) int {
	br := bufio.NewReader(r)
	var soi [2]byte
	if _, err := io.ReadFull(br, soi[:]); err != nil || soi != [2]byte{0xFF, 0xD8} {
		return 1
	}
	for {
		var marker [2]byte
		if _, err := io.ReadFull(br, marker[:]); err != nil || marker[0] != 0xFF {
			return 1
		}
		if marker[1] == 0xD8 || (marker[1] >= 0xD0 && marker[1] <= 0xD7) || marker[1] == 0x01 {
			continue // markers without a length
		}
		if marker[1] == 0xDA || marker[1] == 0xD9 { // image data starts: no EXIF before it
			return 1
		}
		var size [2]byte
		if _, err := io.ReadFull(br, size[:]); err != nil {
			return 1
		}
		n := int(binary.BigEndian.Uint16(size[:])) - 2
		if n < 0 {
			return 1
		}
		seg := make([]byte, n)
		if _, err := io.ReadFull(br, seg); err != nil {
			return 1
		}
		if marker[1] == 0xE1 && len(seg) > 6 && string(seg[:6]) == "Exif\x00\x00" {
			return tiffOrientation(seg[6:])
		}
	}
}

func tiffOrientation(t []byte) int {
	if len(t) < 8 {
		return 1
	}
	var bo binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return 1
	}
	ifd := int(bo.Uint32(t[4:8]))
	if ifd+2 > len(t) || ifd < 0 {
		return 1
	}
	count := int(bo.Uint16(t[ifd:]))
	for i := 0; i < count; i++ {
		e := ifd + 2 + i*12
		if e+12 > len(t) {
			return 1
		}
		if bo.Uint16(t[e:]) == 0x0112 {
			return int(bo.Uint16(t[e+8:]))
		}
	}
	return 1
}
