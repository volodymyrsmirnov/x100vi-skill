package fakecam

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"sort"

	"golang.org/x/image/tiff"
)

const tagImageDescription = 270

func solid(w, h int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	c := color.RGBA{90, 120, 150, 255}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	return img
}

// makeJPEG encodes a small JPEG and inserts the payload as a COM segment right after SOI.
func makeJPEG(w, h int, comment []byte) []byte {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, solid(w, h), nil); err != nil {
		panic(err)
	}
	enc := buf.Bytes()
	seg := []byte{0xFF, 0xFE, 0, 0}
	binary.BigEndian.PutUint16(seg[2:], uint16(len(comment)+2))
	seg = append(seg, comment...)
	out := append([]byte{}, enc[:2]...)
	out = append(out, seg...)
	return append(out, enc[2:]...)
}

// makeTIFF encodes a small TIFF and adds the payload as its ImageDescription tag: the encoder's IFD is
// copied to the end of the file with the extra entry, and the header is pointed at the copy.
func makeTIFF(w, h int, description string) []byte {
	var buf bytes.Buffer
	if err := tiff.Encode(&buf, solid(w, h), nil); err != nil {
		panic(err)
	}
	out := buf.Bytes()
	le := binary.LittleEndian
	ifd := int(le.Uint32(out[4:]))
	n := int(le.Uint16(out[ifd:]))
	entries := make([][]byte, 0, n+1)
	for i := 0; i < n; i++ {
		entries = append(entries, append([]byte(nil), out[ifd+2+12*i:ifd+2+12*i+12]...))
	}
	text := append([]byte(description), 0)
	textAt := len(out)
	out = append(out, text...)
	if len(out)%2 == 1 {
		out = append(out, 0)
	}
	desc := make([]byte, 12)
	le.PutUint16(desc[0:], tagImageDescription)
	le.PutUint16(desc[2:], 2) // ASCII
	le.PutUint32(desc[4:], uint32(len(text)))
	le.PutUint32(desc[8:], uint32(textAt))
	entries = append(entries, desc)
	sort.Slice(entries, func(i, j int) bool { return le.Uint16(entries[i]) < le.Uint16(entries[j]) })
	newIFD := len(out)
	out = le.AppendUint16(out, uint16(len(entries)))
	for _, e := range entries {
		out = append(out, e...)
	}
	out = le.AppendUint32(out, 0)
	le.PutUint32(out[4:], uint32(newIFD))
	return out
}

// jpegComment returns the first COM segment of a JPEG.
func jpegComment(b []byte) ([]byte, error) {
	if len(b) < 4 || b[0] != 0xFF || b[1] != 0xD8 {
		return nil, errors.New("not a JPEG")
	}
	p := 2
	for p+4 <= len(b) {
		if b[p] != 0xFF {
			return nil, errors.New("JPEG segment structure is broken")
		}
		marker := b[p+1]
		if marker == 0xD8 || marker == 0x01 || (marker >= 0xD0 && marker <= 0xD7) || marker == 0xFF {
			p += 2
			continue
		}
		if marker == 0xDA || marker == 0xD9 {
			break
		}
		size := int(binary.BigEndian.Uint16(b[p+2:]))
		if size < 2 || p+2+size > len(b) {
			return nil, errors.New("JPEG segment runs past the end")
		}
		if marker == 0xFE {
			return b[p+4 : p+2+size], nil
		}
		p += 2 + size
	}
	return nil, errors.New("JPEG has no comment")
}

// tiffDescription returns the ImageDescription of a TIFF's first IFD.
func tiffDescription(b []byte) ([]byte, error) {
	if len(b) < 8 {
		return nil, errors.New("not a TIFF")
	}
	var order binary.ByteOrder
	switch string(b[:4]) {
	case "II*\x00":
		order = binary.LittleEndian
	case "MM\x00*":
		order = binary.BigEndian
	default:
		return nil, errors.New("not a TIFF")
	}
	ifd := int(order.Uint32(b[4:]))
	if ifd+2 > len(b) {
		return nil, errors.New("TIFF IFD is past the end")
	}
	n := int(order.Uint16(b[ifd:]))
	for i := 0; i < n; i++ {
		e := ifd + 2 + 12*i
		if e+12 > len(b) {
			break
		}
		if order.Uint16(b[e:]) != tagImageDescription {
			continue
		}
		count := int(order.Uint32(b[e+4:]))
		var val []byte
		if count <= 4 {
			val = b[e+8 : e+8+count]
		} else {
			at := int(order.Uint32(b[e+8:]))
			if at+count > len(b) {
				return nil, errors.New("TIFF ImageDescription is past the end")
			}
			val = b[at : at+count]
		}
		return bytes.TrimRight(val, "\x00"), nil
	}
	return nil, errors.New("TIFF has no ImageDescription")
}

// RenderedCells is what the fake "developed": the profile cells it was triggered with and whether
// it was a full-size render. It reads the payload the fake keeps in a JPEG comment, a TIFF
// ImageDescription, or after the fake HEIF header.
func RenderedCells(img []byte) ([]int, bool, error) {
	var payload []byte
	var err error
	switch {
	case bytes.HasPrefix(img, []byte{0xFF, 0xD8}):
		payload, err = jpegComment(img)
	case bytes.HasPrefix(img, []byte("II*\x00")) || bytes.HasPrefix(img, []byte("MM\x00*")):
		payload, err = tiffDescription(img)
	case bytes.HasPrefix(img, []byte("\x00\x00\x00\x18ftypheix")):
		payload = img[12:]
	default:
		err = errors.New("not an image the fake camera makes")
	}
	if err != nil {
		return nil, false, err
	}
	var r Rendered
	if err := json.Unmarshal(payload, &r); err != nil {
		return nil, false, fmt.Errorf("fake render payload: %w", err)
	}
	return r.Cells, r.Full, nil
}

// RenderedCellsFile is RenderedCells for an image saved to disk.
func RenderedCellsFile(path string) ([]int, bool, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, false, err
	}
	return RenderedCells(b)
}
