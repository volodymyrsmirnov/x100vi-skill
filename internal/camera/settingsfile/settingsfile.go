// Package settingsfile is the camera's own settings file: every menu setting plus C1-C7, exported by
// the body itself.
//
// This is the file Fujifilm's backup tools save. In USB RAW CONV./BACKUP RESTORE mode the body serves
// it as PTP object handle 0:
//
//	GetObjectInfo 0x1008 (handle 0)     format 0x5000, size of the file
//	GetObject     0x1009 (handle 0)     the file, starting "FUJIFILMX-BACKUP"
//	SendObjectInfo 0x100C (0, 0)        restore: a 1076-byte ObjectInfo, format 0x5000, size of the file
//	SendObject    0x100D (0)            the file
//
// The tool treats the file as opaque: it checks the header and never edits the contents. The file
// holds the body's ID and network settings, so it should be kept private.
package settingsfile

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"strings"

	"github.com/volodymyrsmirnov/x100vi-skill/internal/errs"
	"github.com/volodymyrsmirnov/x100vi-skill/internal/ptp"
)

const (
	Magic        = "FUJIFILMX-BACKUP"
	ObjectFormat = 0x5000
	Handle       = 0
	InfoSize     = 1076 // the ObjectInfo length sent when restoring (measured to work on the X100VI)
	HeaderSize   = 0x48
	Suffix       = ".dat"
)

// Bookkeeping are bytes the camera updates itself on every restore (firmware 1.31): a 16-bit counter
// at 0x108 that went up by 2, and a flag at 0x716 that went from 1 to 3. They are not settings;
// verification ignores them.
var Bookkeeping = map[int]bool{0x108: true, 0x109: true, 0x716: true}

// SettingsDiffer lists the offsets where two settings files differ, ignoring the camera's own
// bookkeeping bytes. Files of different lengths differ at the shorter length.
func SettingsDiffer(a, b []byte) []int {
	if len(a) != len(b) {
		return []int{min(len(a), len(b))}
	}
	out := []int{}
	for i := range a {
		if a[i] != b[i] && !Bookkeeping[i] {
			out = append(out, i)
		}
	}
	return out
}

// Header is who made a settings file.
type Header struct {
	Version string
	Model   string
	BodyID  string
}

// asciiText decodes a NUL-terminated ASCII field, replacing other bytes like Python's "replace".
func asciiText(b []byte) string {
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	var sb strings.Builder
	for _, c := range b {
		if c < 0x80 {
			sb.WriteByte(c)
		} else {
			sb.WriteRune('�')
		}
	}
	return sb.String()
}

// ParseHeader checks that bytes are a camera settings file and reads who made it. source names the
// bytes in the error ("" means "this file").
func ParseHeader(blob []byte, source string) (Header, error) {
	if source == "" {
		source = "this file"
	}
	if len(blob) < HeaderSize || !bytes.HasPrefix(blob, []byte(Magic)) {
		return Header{}, errs.Recipe(source+" is not a camera settings file",
			"Use a file saved by `x100vi backup --full` (it starts with FUJIFILMX-BACKUP).")
	}
	return Header{Version: asciiText(blob[16:20]), Model: asciiText(blob[20:0x34]),
		BodyID: asciiText(blob[0x34:HeaderSize])}, nil
}

// Read downloads the settings file from the camera.
func Read(s *ptp.Session) ([]byte, error) {
	code, _, info, err := s.Transact(ptp.OpGetObjectInfo, []uint32{Handle}, nil)
	if err != nil {
		return nil, err
	}
	if code != ptp.RCOK {
		return nil, errs.PTP(code, "ask for the camera's settings file",
			"The camera offers its settings file only in USB RAW CONV./BACKUP RESTORE mode.")
	}
	r := ptp.NewReader(info)
	r.U32()
	format, _, size := r.U16(), r.U16(), r.U32()
	if r.Err != nil {
		return nil, errs.Wrap(errs.General, r.Err, "the camera sent an unreadable description of its settings file", "")
	}
	if format != ObjectFormat {
		return nil, errs.New(errs.General,
			fmt.Sprintf("the camera offered object format 0x%04X instead of its settings file", format), "")
	}
	blob, err := s.Check(ptp.OpGetObject, []uint32{Handle}, nil, "download the settings file")
	if err != nil {
		return nil, err
	}
	if len(blob) != int(size) {
		return nil, errs.New(errs.General,
			fmt.Sprintf("the settings file was cut short (%d of %d bytes)", len(blob), size), "Run the command again.")
	}
	if _, err := ParseHeader(blob, "what the camera sent"); err != nil {
		return nil, err
	}
	return blob, nil
}

// Write sends a settings file back to the camera.
func Write(s *ptp.Session, blob []byte) error {
	info := make([]byte, InfoSize)
	binary.LittleEndian.PutUint16(info[4:], ObjectFormat)
	binary.LittleEndian.PutUint32(info[8:], uint32(len(blob)))
	if _, err := s.Check(ptp.OpSendObjectInfo, []uint32{0, 0}, info, "announce the settings file"); err != nil {
		return err
	}
	_, err := s.Check(ptp.OpSendObject, []uint32{0}, blob, "send the settings file")
	return err
}
