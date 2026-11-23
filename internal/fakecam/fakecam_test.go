package fakecam

import (
	"bytes"
	"image/jpeg"
	"reflect"
	"testing"

	"golang.org/x/image/tiff"
)

func TestJPEGCarriesItsPayloadAndStillDecodes(t *testing.T) {
	img := makeJPEG(24, 16, []byte(`{"cells":[1,-2,7],"full":false}`))
	cells, full, err := RenderedCells(img)
	if err != nil || !reflect.DeepEqual(cells, []int{1, -2, 7}) || full {
		t.Fatal(cells, full, err)
	}
	decoded, err := jpeg.Decode(bytes.NewReader(img))
	if err != nil || decoded.Bounds().Dx() != 24 || decoded.Bounds().Dy() != 16 {
		t.Fatal(err)
	}
}

func TestTIFFCarriesItsPayloadAsImageDescriptionAndStillDecodes(t *testing.T) {
	img := makeTIFF(48, 32, `{"cells":[0,0,11],"full":true}`)
	if !bytes.HasPrefix(img, []byte("II*\x00")) {
		t.Fatalf("%q", img[:4])
	}
	cells, full, err := RenderedCells(img)
	if err != nil || !reflect.DeepEqual(cells, []int{0, 0, 11}) || !full {
		t.Fatal(cells, full, err)
	}
	decoded, err := tiff.Decode(bytes.NewReader(img))
	if err != nil || decoded.Bounds().Dx() != 48 || decoded.Bounds().Dy() != 32 {
		t.Fatal(err)
	}
}

func TestHEIFPayloadIsReadable(t *testing.T) {
	cells, full, err := RenderedCells(append([]byte("\x00\x00\x00\x18ftypheix"), `{"cells":[0,0,18],"full":true}`...))
	if err != nil || cells[2] != 18 || !full {
		t.Fatal(cells, full, err)
	}
}

func TestSettingsFileAndRAFLayout(t *testing.T) {
	sf := MakeSettingsFile("0179ABCDEF0123456789", "X-T5", 0, 9)
	if len(sf) != 4096 || !bytes.HasPrefix(sf, []byte("FUJIFILMX-BACKUP0100X-T5\x00")) || sf[4095] != 9 {
		t.Fatalf("%q", sf[:0x48])
	}
	if string(sf[0x34:0x48]) != "0179ABCDEF0123456789" {
		t.Fatalf("%q", sf[0x34:0x48])
	}
	raf := RAFBytes("", 0)
	if len(raf) != 4096 || string(raf[0x1C:0x22]) != "X100VI" || string(raf[0x3C:0x40]) != "0131" {
		t.Fatalf("%q", raf[:0x40])
	}
	if len(AsShotProfile) != 625 {
		t.Fatal(len(AsShotProfile))
	}
}

func TestCardReaderModeListsFourProperties(t *testing.T) {
	f := NewWith(Options{Mode: "card_reader"})
	if len(f.Props()) != 4 {
		t.Fatal(f.Props())
	}
	if code, _ := f.get(0xD18C); code != PropUnsupported {
		t.Fatalf("0x%04X", code)
	}
}
