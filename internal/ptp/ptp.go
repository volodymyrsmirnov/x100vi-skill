// Package ptp is a PTP session over a pluggable transport.
//
// A Transport runs one PTP transaction. Real cameras are reached through the operating system's own
// camera service where there is one (ImageCaptureCore on macOS, Windows Portable Devices on Windows) or
// directly over USB (Linux); see package transport. Tests substitute a software camera.
package ptp

import (
	"encoding/binary"
	"fmt"
	"unicode/utf16"

	"github.com/volodymyrsmirnov/x100vi-skill/internal/errs"
)

const (
	FujifilmVendorID = 0x04CB
	Cmd, Data, Resp  = 1, 2, 3
	RCOK             = 0x2001
	RCSessionOpen    = 0x201E

	OpGetDeviceInfo      = 0x1001
	OpOpenSession        = 0x1002
	OpCloseSession       = 0x1003
	OpGetObjectHandles   = 0x1007
	OpGetObjectInfo      = 0x1008
	OpGetObject          = 0x1009
	OpDeleteObject       = 0x100B
	OpSendObjectInfo     = 0x100C
	OpSendObject         = 0x100D
	OpGetDevicePropValue = 0x1015
	OpSetDevicePropValue = 0x1016
	OpVendorSendObjInfo  = 0x900C
	OpVendorSendObject   = 0x900D
)

// Transport runs one PTP transaction: it returns the response code, the response parameters and the
// incoming data (if any). dataOut is nil when the request has no outgoing data phase.
type Transport interface {
	Transact(op uint16, params []uint32, dataOut []byte) (uint16, []uint32, []byte, error)
	Close() error
}

// Reader is a cursor over a little-endian PTP dataset. Reading past the end yields zeros and sets Err.
type Reader struct {
	D   []byte
	P   int
	Err error
}

func NewReader(d []byte) *Reader { return &Reader{D: d} }

func (r *Reader) need(n int) bool {
	if r.P+n > len(r.D) {
		if r.Err == nil {
			r.Err = fmt.Errorf("dataset ends at %d, wanted %d more bytes at %d", len(r.D), n, r.P)
		}
		r.P = len(r.D)
		return false
	}
	return true
}

func (r *Reader) U8() uint8 {
	if !r.need(1) {
		return 0
	}
	v := r.D[r.P]
	r.P++
	return v
}

func (r *Reader) U16() uint16 {
	if !r.need(2) {
		return 0
	}
	v := binary.LittleEndian.Uint16(r.D[r.P:])
	r.P += 2
	return v
}

func (r *Reader) U32() uint32 {
	if !r.need(4) {
		return 0
	}
	v := binary.LittleEndian.Uint32(r.D[r.P:])
	r.P += 4
	return v
}

func (r *Reader) I32() int32 { return int32(r.U32()) }

// String reads a PTP string: a count of UTF-16 units (including the terminator), then the units.
func (r *Reader) String() string {
	n := int(r.U8())
	if n == 0 || !r.need(2*n) {
		return ""
	}
	units := make([]uint16, n)
	for i := range units {
		units[i] = binary.LittleEndian.Uint16(r.D[r.P+2*i:])
	}
	r.P += 2 * n
	for len(units) > 0 && units[len(units)-1] == 0 {
		units = units[:len(units)-1]
	}
	return string(utf16.Decode(units))
}

func (r *Reader) ArrayU16() []uint16 {
	n := r.U32()
	if !r.need(int(n) * 2) {
		return nil
	}
	out := make([]uint16, n)
	for i := range out {
		out[i] = r.U16()
	}
	return out
}

func (r *Reader) ArrayU32() []uint32 {
	n := r.U32()
	if !r.need(int(n) * 4) {
		return nil
	}
	out := make([]uint32, n)
	for i := range out {
		out[i] = r.U32()
	}
	return out
}

// PackString encodes a PTP string.
func PackString(text string) []byte {
	if text == "" {
		return []byte{0}
	}
	units := append(utf16.Encode([]rune(text)), 0)
	out := make([]byte, 1+2*len(units))
	out[0] = byte(len(units))
	for i, u := range units {
		binary.LittleEndian.PutUint16(out[1+2*i:], u)
	}
	return out
}

// U16Bytes is a 16-bit little-endian property value.
func U16Bytes(v int) []byte {
	b := make([]byte, 2)
	binary.LittleEndian.PutUint16(b, uint16(v))
	return b
}

// DeviceInfo is the part of the PTP DeviceInfo dataset the tool uses.
type DeviceInfo struct {
	Manufacturer, Model, Version, Serial string
	Operations, Props                    []uint16
}

// ParseDeviceInfo decodes a GetDeviceInfo dataset.
func ParseDeviceInfo(d []byte) (DeviceInfo, error) {
	r := NewReader(d)
	r.U16()
	r.U32()
	r.U16()
	_ = r.String()
	r.U16()
	var info DeviceInfo
	info.Operations = r.ArrayU16()
	r.ArrayU16()
	info.Props = r.ArrayU16()
	r.ArrayU16()
	r.ArrayU16()
	info.Manufacturer, info.Model, info.Version, info.Serial = r.String(), r.String(), r.String(), r.String()
	return info, r.Err
}

// Pack builds a DeviceInfo dataset (used by the software camera).
func (d DeviceInfo) Pack() []byte {
	var out []byte
	u16 := func(v uint16) { out = binary.LittleEndian.AppendUint16(out, v) }
	u32 := func(v uint32) { out = binary.LittleEndian.AppendUint32(out, v) }
	arr := func(vs []uint16) {
		u32(uint32(len(vs)))
		for _, v := range vs {
			u16(v)
		}
	}
	u16(100)
	u32(0x0000000E)
	u16(100)
	out = append(out, PackString("fujifilm.co.jp: 1.0;")...)
	u16(0)
	arr(d.Operations)
	arr(nil)
	arr(d.Props)
	arr(nil)
	arr(nil)
	for _, s := range []string{d.Manufacturer, d.Model, d.Version, d.Serial} {
		out = append(out, PackString(s)...)
	}
	return out
}

// Session is a PTP session on top of a transport.
type Session struct {
	T         Transport
	open      bool
	described map[uint32]bool
}

func NewSession(t Transport) *Session { return &Session{T: t, described: map[uint32]bool{}} }

// Transact runs one transaction. It refuses to download an object before its description was read:
// asking an X100VI for an object it has not described first (seen with handle 0, the settings file)
// can hang the body until its battery is removed. A handle counts as described only once a reply to
// GetObjectInfo parsed completely (ParseObjectInfo); it stops counting once it is deleted, once reading
// its description fails, and whenever the session is opened or closed (handles belong to a session).
func (s *Session) Transact(op uint16, params []uint32, dataOut []byte) (uint16, []uint32, []byte, error) {
	switch op {
	case OpGetObject:
		if len(params) != 1 {
			return 0, nil, nil, errs.New(errs.General,
				fmt.Sprintf("refusing to download an object with %d parameters instead of one handle", len(params)),
				"This is a bug in the tool; please report it.")
		}
		if !s.described[params[0]] {
			return 0, nil, nil, errs.New(errs.General,
				fmt.Sprintf("refusing to download object %d before reading its description", params[0]),
				"This is a bug in the tool; please report it.")
		}
	case OpDeleteObject:
		switch {
		case len(params) == 0 || params[0] == 0xFFFFFFFF: // all objects
			clear(s.described)
		default:
			delete(s.described, params[0])
		}
	}
	code, rparams, data, err := s.T.Transact(op, params, dataOut)
	if op == OpGetObjectInfo && len(params) > 0 {
		if err == nil && code == RCOK && objectInfoParses(data) {
			s.described[params[0]] = true
		} else {
			delete(s.described, params[0])
		}
	}
	return code, rparams, data, err
}

func objectInfoParses(d []byte) bool {
	_, err := ParseObjectInfo(d)
	return err == nil
}

// Check runs a transaction and turns a non-OK response into an error.
func (s *Session) Check(op uint16, params []uint32, dataOut []byte, what string) ([]byte, error) {
	code, _, data, err := s.Transact(op, params, dataOut)
	if err != nil {
		return nil, err
	}
	if code != RCOK {
		if what == "" {
			what = fmt.Sprintf("request 0x%04X", op)
		}
		return nil, errs.PTP(code, what, "")
	}
	return data, nil
}

// Open opens the PTP session (transports that own the session answer OK without sending it).
func (s *Session) Open() error {
	clear(s.described)
	code, _, _, err := s.Transact(OpOpenSession, []uint32{1}, nil)
	if err != nil {
		return err
	}
	if code != RCOK && code != RCSessionOpen {
		return errs.PTP(code, "OpenSession", "")
	}
	s.open = true
	return nil
}

// Close closes the session and the transport.
func (s *Session) Close() error {
	clear(s.described)
	if s.open {
		s.open = false
		_, _, _, _ = s.Transact(OpCloseSession, nil, nil)
	}
	return s.T.Close()
}

func (s *Session) DeviceInfo() (DeviceInfo, error) {
	d, err := s.Check(OpGetDeviceInfo, nil, nil, "GetDeviceInfo")
	if err != nil {
		return DeviceInfo{}, err
	}
	info, perr := ParseDeviceInfo(d)
	if perr != nil {
		return info, errs.Wrap(errs.General, perr, "the camera sent an unreadable device description", "")
	}
	return info, nil
}

func (s *Session) PropRaw(code uint16) ([]byte, error) {
	return s.Check(OpGetDevicePropValue, []uint32{uint32(code)}, nil, fmt.Sprintf("read property 0x%04X", code))
}

func (s *Session) GetU16(code uint16) (int, error) {
	d, err := s.PropRaw(code)
	if err != nil {
		return 0, err
	}
	if len(d) < 2 {
		return 0, errs.New(errs.General, fmt.Sprintf("property 0x%04X: short answer (%d bytes)", code, len(d)), "")
	}
	return int(binary.LittleEndian.Uint16(d)), nil
}

func (s *Session) GetString(code uint16) (string, error) {
	d, err := s.PropRaw(code)
	if err != nil {
		return "", err
	}
	r := NewReader(d)
	s2 := r.String()
	if r.Err != nil {
		return "", errs.New(errs.General, fmt.Sprintf("property 0x%04X: the camera sent a truncated text value", code),
			"Run the command again; if it keeps happening, switch the camera off and on.")
	}
	return s2, nil
}

func (s *Session) SetRaw(code uint16, data []byte, what string) error {
	if what == "" {
		what = fmt.Sprintf("write property 0x%04X", code)
	}
	_, err := s.Check(OpSetDevicePropValue, []uint32{uint32(code)}, data, what)
	return err
}

func (s *Session) SetU16(code uint16, value int) error { return s.SetRaw(code, U16Bytes(value), "") }

func (s *Session) Handles() ([]uint32, error) {
	d, err := s.Check(OpGetObjectHandles, []uint32{0xFFFFFFFF, 0, 0}, nil, "GetObjectHandles")
	if err != nil {
		return nil, err
	}
	return ParseHandles(d)
}

// ParseHandles decodes a GetObjectHandles dataset and refuses a truncated one.
func ParseHandles(d []byte) ([]uint32, error) {
	r := NewReader(d)
	handles := r.ArrayU32()
	if r.Err != nil {
		return nil, errs.Wrap(errs.General, r.Err, "the camera sent a truncated list of objects",
			"Run the command again; if it keeps happening, switch the camera off and on.")
	}
	return handles, nil
}

// ObjectInfo is a PTP ObjectInfo dataset.
type ObjectInfo struct {
	StorageID                              uint32
	Format, Protection                     uint16
	Size                                   uint32
	ThumbFormat                            uint16
	ThumbSize, ThumbWidth, ThumbHeight     uint32
	Width, Height, BitDepth, Parent        uint32
	AssociationType                        uint16
	AssociationDesc, SequenceNumber        uint32
	Filename, Captured, Modified, Keywords string
}

// ParseObjectInfo decodes a whole ObjectInfo dataset and refuses a truncated one.
func ParseObjectInfo(d []byte) (ObjectInfo, error) {
	r := NewReader(d)
	var o ObjectInfo
	o.StorageID, o.Format, o.Protection, o.Size = r.U32(), r.U16(), r.U16(), r.U32()
	o.ThumbFormat, o.ThumbSize, o.ThumbWidth, o.ThumbHeight = r.U16(), r.U32(), r.U32(), r.U32()
	o.Width, o.Height, o.BitDepth, o.Parent = r.U32(), r.U32(), r.U32(), r.U32()
	o.AssociationType, o.AssociationDesc, o.SequenceNumber = r.U16(), r.U32(), r.U32()
	o.Filename, o.Captured, o.Modified, o.Keywords = r.String(), r.String(), r.String(), r.String()
	if r.Err != nil {
		return ObjectInfo{}, errs.Wrap(errs.General, r.Err, "the camera sent a truncated object description",
			"Run the command again; if it keeps happening, switch the camera off and on.")
	}
	return o, nil
}
