package sheet

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"
)

func solid(w, h int, c color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, c)
		}
	}
	return img
}

// writeJPEG saves img, with an EXIF Orientation tag when orientation > 0.
func writeJPEG(t *testing.T, path string, img image.Image, orientation int) {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	data := buf.Bytes()
	if orientation > 0 {
		tiff := []byte{'M', 'M', 0, 42, 0, 0, 0, 8, 0, 1, 0x01, 0x12, 0, 3, 0, 0, 0, 1, 0, byte(orientation), 0, 0, 0, 0, 0, 0}
		seg := append([]byte("Exif\x00\x00"), tiff...)
		app1 := []byte{0xFF, 0xE1, 0, 0}
		binary.BigEndian.PutUint16(app1[2:], uint16(len(seg)+2))
		app1 = append(app1, seg...)
		data = append(append(append([]byte{}, data[:2]...), app1...), data[2:]...)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func readJPEG(t *testing.T, path string) image.Image {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := jpeg.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func near(c color.Color, want color.RGBA) bool {
	r, g, b, _ := c.RGBA()
	d := func(a uint32, b uint8) bool { x := int(a>>8) - int(b); return x > -24 && x < 24 }
	return d(r, want.R) && d(g, want.G) && d(b, want.B)
}

var (
	orange = color.RGBA{200, 80, 40, 255}
	blue   = color.RGBA{40, 80, 200, 255}
)

func TestContactSheetKeepsOrientationAndLabelsRows(t *testing.T) {
	dir := t.TempDir()
	wide, tall := filepath.Join(dir, "wide.jpg"), filepath.Join(dir, "tall.jpg")
	writeJPEG(t, wide, solid(300, 200, orange), 0)
	writeJPEG(t, tall, solid(200, 300, blue), 0)
	out, err := ContactSheet([][]Cell{{{"As shot", wide}, {"Recipe", wide}}, {{"As shot", tall}}},
		filepath.Join(dir, "s", "sheet.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	img := readJPEG(t, out)
	// 2 columns of 300 px, rows of 200 and 300 px, each with a label band and padding.
	wantW, wantH := Pad+2*(300+Pad), Pad+(200+Label+Pad)+(300+Label+Pad)
	if b := img.Bounds(); b.Dx() != wantW || b.Dy() != wantH {
		t.Fatalf("sheet is %v, want %dx%d", b.Size(), wantW, wantH)
	}
	row2 := Pad + 200 + Label + Pad + Label
	checks := []struct {
		x, y int
		want color.RGBA
	}{
		{Pad + 150, Pad + Label + 100, orange},           // first tile
		{Pad + 316 + 150, Pad + Label + 100, orange},     // second tile
		{Pad + 100, row2 + 150, blue},                    // tall tile, kept upright
		{Pad + 316 + 150, row2 + 150, background},        // no second tile in row 2
		{Pad + 250, row2 + 150, background},              // tall tile is only 200 wide
		{wantW - 4, wantH - 4, background},               // padding
		{Pad + 316 + 150, Pad + Label + 205, background}, // between rows
	}
	for _, c := range checks {
		if got := img.At(c.x, c.y); !near(got, c.want) {
			t.Errorf("pixel (%d, %d) = %v, want %v", c.x, c.y, got, c.want)
		}
	}
	// some label text is drawn in the band above the first tile
	lit := 0
	for y := Pad; y < Pad+Label; y++ {
		for x := Pad; x < Pad+300; x++ {
			if r, _, _, _ := img.At(x, y).RGBA(); r>>8 > 150 {
				lit++
			}
		}
	}
	if lit < 50 {
		t.Errorf("label band looks empty (%d bright pixels)", lit)
	}
}

func TestContactSheetShrinksAndTurnsImages(t *testing.T) {
	dir := t.TempDir()
	big := filepath.Join(dir, "big.jpg")
	writeJPEG(t, big, solid(1440, 960, orange), 0)
	turned := filepath.Join(dir, "turned.jpg")
	// stored landscape, tagged "rotate 90 clockwise": displays as portrait
	writeJPEG(t, turned, solid(300, 200, blue), 6)
	out, err := ContactSheet([][]Cell{{{"big", big}, {"turned", turned}}}, filepath.Join(dir, "sheet.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	img := readJPEG(t, out)
	if b := img.Bounds(); b.Dx() != Pad+2*(CellLong+Pad) || b.Dy() != Pad+480+Label+Pad {
		t.Fatalf("sheet is %v", b.Size())
	}
	x := Pad + CellLong + Pad
	if !near(img.At(x+100, Pad+Label+250), blue) || !near(img.At(x+250, Pad+Label+100), background) {
		t.Errorf("the EXIF orientation was not applied")
	}
}

func TestOrient(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 3, 2))
	src.SetRGBA(0, 0, orange) // top-left marker
	for o, want := range map[int]image.Point{1: {0, 0}, 2: {2, 0}, 3: {2, 1}, 4: {0, 1}, 5: {0, 0}, 6: {1, 0}, 7: {1, 2}, 8: {0, 2}} {
		got := orient(src, o)
		if o >= 5 && got.Bounds().Dx() != 2 {
			t.Errorf("orientation %d: size %v", o, got.Bounds())
		}
		if !near(got.At(want.X, want.Y), orange) {
			t.Errorf("orientation %d: marker not at %v", o, want)
		}
	}
}

func TestContactSheetNeedsSomething(t *testing.T) {
	dir := t.TempDir()
	if _, err := ContactSheet(nil, filepath.Join(dir, "x.jpg")); !errors.Is(err, ErrEmpty) {
		t.Errorf("nil rows: %v", err)
	}
	if _, err := ContactSheet([][]Cell{{}, {}}, filepath.Join(dir, "x.jpg")); !errors.Is(err, ErrEmpty) {
		t.Errorf("empty rows: %v", err)
	}
	if _, err := ContactSheet([][]Cell{{{"x", filepath.Join(dir, "missing.jpg")}}}, filepath.Join(dir, "x.jpg")); err == nil {
		t.Errorf("missing image")
	}
	if _, err := os.Stat(filepath.Join(dir, "x.jpg")); err == nil {
		t.Errorf("a sheet was written")
	}
}
