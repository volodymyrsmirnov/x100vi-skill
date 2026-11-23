// Package fakecam is a software X100VI that answers PTP transactions the way the real body was
// measured to. It is a test helper.
//
// It reproduces the behaviours the client has to cope with:
//   - two USB modes (card reader exposes four properties and no settings);
//   - value validation, including the cross-setting locks (D-range priority, B&W sims, RAW-only);
//   - grain Off written as 1 and read back as 6/7; white-balance shift remembered per mode;
//     a Kelvin change zeroing the shift; monochromatic tint staying stored under a colour sim;
//   - naming a never-named register seeds it from the live settings;
//   - RAF upload, the 0xD185 profile, and conversion producing a JPEG, HEIF or (full size only) TIFF
//     object;
//   - the camera's own settings file as object handle 0, read with GetObject and restored with
//     SendObject.
package fakecam

import (
	"bytes"
	_ "embed"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"os"
	"slices"

	"github.com/volodymyrsmirnov/x100vi-skill/internal/model"
	"github.com/volodymyrsmirnov/x100vi-skill/internal/ptp"
)

// PTP response codes the fake answers with.
const (
	OK              = 0x2001
	GeneralError    = 0x2002
	OpNotSupported  = 0x2005
	InvalidHandle   = 0x2009
	PropUnsupported = 0x200A
	InvalidValue    = 0x201C
)

// Mirrors of rawconv constants (fakecam must not import rawconv, so camera tests can use it).
const (
	rafMagic     = "FUJIFILMCCD-RAW "
	profileTable = 0x1FD
)

// AsShotProfile is a real X100VI frame's 0xD185 profile (Classic Chrome, DR200, D-range priority weak).
//
//go:embed testdata/d185_as_shot.bin
var AsShotProfile []byte

// CardReaderProps are the only properties the body lists in USB CARD READER mode.
var CardReaderProps = []uint16{0x5001, 0xD303, 0xD406, 0xD407}

// Factory is recipe key -> factory wire value.
var Factory = map[string]int{
	"d_range_priority": 0, "dynamic_range": 100, "film_simulation": 1, "monochromatic.warm_cool": 0,
	"monochromatic.magenta_green": 0, "grain": 6, "color_chrome_effect": 1, "color_chrome_fx_blue": 1,
	"smooth_skin": 1, "white_balance.mode": 2, "white_balance.kelvin": 10000, "white_balance.shift_r": 0,
	"white_balance.shift_b": 0, "highlight": 0, "shadow": 0, "color": 0, "sharpness": 0,
	"noise_reduction": 0x2000, "clarity": 0, "image_quality": 2, "image_size": 7,
	"lens_modulation_optimizer": 1, "color_space": 1,
}

// MakeSettingsFile is a camera settings file with the real header layout and dummy contents.
// Empty bodyID / model and size 0 take the defaults ("0179FAKE000000000001", "X100VI", 4096).
func MakeSettingsFile(bodyID, modelName string, size int, fill byte) []byte {
	if bodyID == "" {
		bodyID = "0179FAKE000000000001"
	}
	if modelName == "" {
		modelName = "X100VI"
	}
	if size == 0 {
		size = 4096
	}
	head := []byte("FUJIFILMX-BACKUP0100")
	head = append(head, ljust(modelName, 0x20)...)
	head = append(head, ljust(bodyID, 20)...)
	return append(head, bytes.Repeat([]byte{fill}, size-len(head))...)
}

// DefaultSettingsFile is MakeSettingsFile with every default.
func DefaultSettingsFile() []byte { return MakeSettingsFile("", "", 0, 0) }

func ljust(s string, n int) []byte {
	b := []byte(s)
	if len(b) >= n {
		return b
	}
	return append(b, make([]byte, n-len(b))...)
}

// RAFBytes is a file body with a valid RAF header (no image data), enough for upload checks.
func RAFBytes(modelName string, size int) []byte {
	if modelName == "" {
		modelName = "X100VI"
	}
	if size == 0 {
		size = 4096
	}
	head := []byte(rafMagic + "0201" + "FF179503")
	head = append(head, ljust(modelName, 32)...)
	head = append(head, "0131"...)
	return append(head, make([]byte, size-len(head))...)
}

// MakeRAF writes a RAF header file (4096 bytes) to path and returns the path. modelName "" is X100VI.
func MakeRAF(path, modelName string) (string, error) {
	return path, os.WriteFile(path, RAFBytes(modelName, 0), 0o644)
}

func u16(v int) []byte { return ptp.U16Bytes(v) }

// Object is a PTP object in the fake's buffer.
type Object struct {
	Format uint16
	Data   []byte
}

// PropWrite is one SetDevicePropValue, as received.
type PropWrite struct {
	Code uint16
	Data []byte
}

// Target says which settings a field write lands in.
type Target struct {
	Live bool
	Slot int
}

type wbKey struct {
	Target
	Mode int
}

// FieldHook intercepts a property write that reaches validation. next runs the real behaviour.
// Use it to make the body refuse a value or silently keep the old one.
type FieldHook func(state map[string]int, f model.Field, target Target, wire int, next func() uint16) uint16

// Intercept wraps every transaction. next runs the real behaviour.
type Intercept func(op uint16, params []uint32, data []byte, next func() (uint16, []byte)) (uint16, []byte)

// FakeX100VI is the software camera. It implements ptp.Transport.
type FakeX100VI struct {
	Mode     string // raw_conv | card_reader
	Model    string
	Firmware string
	Battery  int

	Live        map[string]int
	Slots       map[int]map[string]int
	Names       map[int]string
	NamedBefore map[int]bool
	WBMemory    map[wbKey][2]int
	Selected    int
	USBMode     int    // 0xD16E: 6 is RAW conv./backup restore
	Profile     []byte // nil until a RAF the body accepts was uploaded
	Objects     map[uint32]Object
	NextHandle  uint32

	SettingsFile   []byte
	SettingsWrites int

	StallConversion bool
	Closed          bool
	SessionOpen     bool
	Writes          []PropWrite // every SetDevicePropValue, in order
	Uploads         int

	FieldHook FieldHook
	Intercept Intercept

	incoming  *[2]int // (format, size) announced by SendObjectInfo
	announced int
	byLive    map[uint16]model.Field
	bySlot    map[uint16]model.Field
}

// Options picks the body the fake pretends to be; zero values take the defaults
// (raw_conv, X100VI, 1.31, 80%).
type Options struct {
	Mode     string
	Model    string
	Firmware string
	Battery  int
}

// New returns a fake X100VI in RAW conversion mode, firmware 1.31, battery 80%.
func New() *FakeX100VI { return NewWith(Options{}) }

// NewWith returns a fake with some knobs changed.
func NewWith(o Options) *FakeX100VI {
	if o.Mode == "" {
		o.Mode = "raw_conv"
	}
	if o.Model == "" {
		o.Model = "X100VI"
	}
	if o.Firmware == "" {
		o.Firmware = "1.31"
	}
	if o.Battery == 0 {
		o.Battery = 80
	}
	f := &FakeX100VI{
		Mode: o.Mode, Model: o.Model, Firmware: o.Firmware, Battery: o.Battery,
		Live:         maps.Clone(Factory),
		Slots:        map[int]map[string]int{},
		Names:        map[int]string{},
		NamedBefore:  map[int]bool{},
		WBMemory:     map[wbKey][2]int{},
		Selected:     1,
		USBMode:      6,
		Objects:      map[uint32]Object{},
		NextHandle:   1,
		SettingsFile: DefaultSettingsFile(),
		byLive:       map[uint16]model.Field{},
		bySlot:       map[uint16]model.Field{},
	}
	f.Live["image_quality"] = 1 // the owner shoots RAW, DR auto
	f.Live["dynamic_range"] = 0xFFFF
	for _, n := range model.Slots {
		f.Slots[n] = maps.Clone(Factory)
		f.Names[n] = ""
	}
	for _, fld := range model.Fields {
		f.byLive[fld.Live] = fld
		f.bySlot[fld.Slot] = fld
	}
	return f
}

// -- transport interface ----------------------------------------------------------------------

// Close marks the transport closed.
func (f *FakeX100VI) Close() error {
	f.Closed = true
	return nil
}

// Transact answers one PTP transaction.
func (f *FakeX100VI) Transact(op uint16, params []uint32, dataOut []byte) (uint16, []uint32, []byte, error) {
	next := func() (uint16, []byte) { return f.dispatch(op, params, dataOut) }
	var code uint16
	var data []byte
	if f.Intercept != nil {
		code, data = f.Intercept(op, params, dataOut, next)
	} else {
		code, data = next()
	}
	return code, nil, data, nil
}

func (f *FakeX100VI) dispatch(op uint16, params []uint32, data []byte) (uint16, []byte) {
	p0 := uint32(0)
	if len(params) > 0 {
		p0 = params[0]
	}
	switch op {
	case 0x1001:
		return f.deviceInfo()
	case 0x1002:
		f.SessionOpen = true
		return OK, nil
	case 0x1003:
		f.SessionOpen = false
		return OK, nil
	case 0x1007:
		return f.handles()
	case 0x1008:
		return f.objectInfo(p0)
	case 0x1009:
		return f.object(p0)
	case 0x100B:
		if _, ok := f.Objects[p0]; !ok {
			return InvalidHandle, nil
		}
		delete(f.Objects, p0)
		return OK, nil
	case 0x1015:
		return f.get(uint16(p0))
	case 0x1016:
		return f.set(uint16(p0), data), nil
	case 0x900C:
		return f.announce(data), nil
	case 0x900D:
		return f.upload(data), nil
	case 0x100C:
		return f.sendObjectInfo(params, data), nil
	case 0x100D:
		return f.sendObject(data), nil
	}
	return OpNotSupported, nil
}

// Props are the properties the body lists in its current mode.
func (f *FakeX100VI) Props() []uint16 {
	if f.Mode != "raw_conv" {
		return CardReaderProps
	}
	set := map[uint16]bool{0x5005: true, 0x5015: true, 0xD16E: true, 0xD183: true, 0xD185: true, 0xD18C: true,
		0xD18D: true, 0xD36A: true, 0xD36B: true}
	for c := range f.byLive {
		set[c] = true
	}
	for c := range f.bySlot {
		set[c] = true
	}
	return slices.Sorted(maps.Keys(set))
}

func (f *FakeX100VI) deviceInfo() (uint16, []byte) {
	info := ptp.DeviceInfo{Manufacturer: "FUJIFILM", Model: f.Model, Version: f.Firmware, Serial: "FAKE0001",
		Operations: []uint16{0x1001, 0x1002, 0x1003, 0x1007, 0x1008, 0x1009, 0x100B, 0x100C, 0x100D, 0x1015,
			0x1016, 0x900C, 0x900D},
		Props: f.Props()}
	return OK, info.Pack()
}

// -- properties -------------------------------------------------------------------------------

func (f *FakeX100VI) state(c uint16) (map[string]int, model.Field, Target) {
	if fld, ok := f.byLive[c]; ok {
		return f.Live, fld, Target{Live: true}
	}
	return f.Slots[f.Selected], f.bySlot[c], Target{Slot: f.Selected}
}

func (f *FakeX100VI) get(c uint16) (uint16, []byte) {
	if !slices.Contains(f.Props(), c) {
		return PropUnsupported, nil
	}
	switch c {
	case 0xD18C:
		return OK, u16(f.Selected)
	case 0xD18D:
		return OK, ptp.PackString(f.Names[f.Selected])
	case 0xD36B:
		return OK, ptp.PackString(fmt.Sprintf("%d,0,0", f.Battery))
	case 0xD36A:
		return OK, binary.LittleEndian.AppendUint32(nil, uint32(f.Battery/10))
	case 0xD16E:
		return OK, u16(f.USBMode)
	case 0xD185:
		if f.Profile == nil {
			return GeneralError, nil
		}
		return OK, append([]byte(nil), f.Profile[:min(len(f.Profile), len(AsShotProfile))]...)
	case 0xD183:
		return GeneralError, nil
	}
	st, fld, _ := f.state(c)
	return OK, u16(st[fld.Key])
}

func readU16(data []byte) (int, bool) {
	if len(data) != 2 {
		return 0, false
	}
	return int(binary.LittleEndian.Uint16(data)), true
}

func (f *FakeX100VI) set(c uint16, data []byte) uint16 {
	if !slices.Contains(f.Props(), c) {
		return PropUnsupported
	}
	f.Writes = append(f.Writes, PropWrite{c, append([]byte(nil), data...)})
	switch c {
	case 0xD18C:
		v, ok := readU16(data)
		if !ok || v < 1 || v > 7 {
			return InvalidValue
		}
		f.Selected = v
		return OK
	case 0xD18D:
		name := ptp.NewReader(data).String()
		if len(name) > model.NameMax {
			return InvalidValue
		}
		if !f.NamedBefore[f.Selected] {
			f.NamedBefore[f.Selected] = true
			maps.Copy(f.Slots[f.Selected], f.Live) // the real body's first-naming quirk
		}
		f.Names[f.Selected] = name
		return OK
	case 0xD185:
		if f.Profile == nil || len(data) < profileTable+4*29 || len(data) > profileTable+4*30+3 {
			return GeneralError
		}
		f.Profile = append([]byte(nil), data...)
		return OK
	case 0xD183:
		v, ok := readU16(data)
		if !ok {
			return InvalidValue
		}
		return f.convert(v)
	case 0xD16E, 0xD36A, 0xD36B:
		return InvalidValue
	}
	st, fld, target := f.state(c)
	wire, ok := readU16(data)
	if !ok {
		return InvalidValue
	}
	next := func() uint16 { return f.writeField(st, fld, target, wire) }
	if f.FieldHook != nil {
		return f.FieldHook(st, fld, target, wire, next)
	}
	return next()
}

func (f *FakeX100VI) writeField(st map[string]int, fld model.Field, target Target, wire int) uint16 {
	film := st["film_simulation"]
	locked := st["d_range_priority"] != 0
	switch {
	case fld.Needs == "mono" && !model.MonoSims[film],
		fld.Needs == "colour" && model.NoColourSims[film],
		fld.Needs == "kelvin" && st["white_balance.mode"] != model.WhiteBalance.Codes["kelvin"],
		fld.Needs == "jpeg" && st["image_quality"] == 1:
		return InvalidValue
	}
	if locked && (fld.Key == "dynamic_range" || fld.Key == "highlight" || fld.Key == "shadow") {
		return InvalidValue
	}
	switch fld.Kind {
	case model.Grain:
		if wire < 1 || wire > 5 {
			return InvalidValue
		}
		if wire == 1 {
			if g := st["grain"]; g == 4 || g == 5 || g == 7 {
				wire = 7
			} else {
				wire = 6
			}
		}
	case model.Enum:
		if _, ok := fld.Table.Name(wire); !ok {
			return InvalidValue
		}
	case model.NR:
		valid := false
		for _, v := range model.NoiseReduction {
			valid = valid || v == wire
		}
		if !valid {
			return InvalidValue
		}
	default:
		value := model.Signed16(wire)
		scale := float64(fld.Scale)
		lo, hi := int(math.Round(fld.Lo*scale)), int(math.Round(fld.Hi*scale))
		step := int(math.Round(fld.Step * scale))
		if value < lo || value > hi || (value-lo)%step != 0 {
			return InvalidValue
		}
	}
	if fld.Key == "white_balance.mode" && wire != st[fld.Key] {
		f.WBMemory[wbKey{target, st[fld.Key]}] = [2]int{st["white_balance.shift_r"], st["white_balance.shift_b"]}
		mem := f.WBMemory[wbKey{target, wire}]
		st["white_balance.shift_r"], st["white_balance.shift_b"] = mem[0], mem[1]
	}
	if fld.Key == "white_balance.kelvin" && wire != st[fld.Key] {
		st["white_balance.shift_r"], st["white_balance.shift_b"] = 0, 0
	}
	st[fld.Key] = wire
	return OK
}

// -- RAW conversion ---------------------------------------------------------------------------

func (f *FakeX100VI) announce(data []byte) uint16 {
	r := ptp.NewReader(data)
	r.U32()
	if r.U16() != 0xF802 {
		return GeneralError
	}
	r.U16()
	f.announced = int(r.U32())
	return OK
}

func (f *FakeX100VI) upload(data []byte) uint16 {
	f.Uploads++
	ok := len(data) == f.announced && bytes.HasPrefix(data, []byte(rafMagic)) && len(data) >= 0x3C
	if ok {
		head := data[0x1C:0x3C]
		if i := bytes.IndexByte(head, 0); i >= 0 {
			head = head[:i]
		}
		ok = string(head) == f.Model
	}
	if ok { // the body accepts the upload either way
		f.Profile = append([]byte(nil), AsShotProfile...)
	} else {
		f.Profile = nil
	}
	return OK
}

// Rendered is what the fake "developed": the profile cells it was triggered with.
type Rendered struct {
	Cells []int `json:"cells"`
	Full  bool  `json:"full"`
}

func (f *FakeX100VI) convert(trigger int) uint16 {
	if (trigger != 0 && trigger != 1) || f.Profile == nil {
		return InvalidValue
	}
	if f.StallConversion {
		return OK
	}
	count := (len(f.Profile) - profileTable) / 4
	cells := make([]int, count)
	for i := range cells {
		cells[i] = int(int32(binary.LittleEndian.Uint32(f.Profile[profileTable+4*i:])))
	}
	payload, _ := json.Marshal(Rendered{cells, trigger == 1})
	w, h := 24, 16
	if trigger == 1 {
		w, h = 48, 32
	}
	var format uint16
	var blob []byte
	switch filetype := cells[2]; {
	case filetype == 18: // HEIF at either size
		format, blob = 0x3812, append([]byte("\x00\x00\x00\x18ftypheix"), payload...)
	case (filetype == 9 || filetype == 11) && trigger == 1: // TIFF only at full size, as on the body
		format, blob = 0x380D, makeTIFF(w, h, string(payload))
	default: // JPEG, and TIFF requests at half size
		format, blob = 0x3801, makeJPEG(w, h, payload)
	}
	f.Objects[f.NextHandle] = Object{format, blob}
	f.NextHandle++
	return OK
}

func (f *FakeX100VI) handles() (uint16, []byte) {
	keys := slices.Sorted(maps.Keys(f.Objects))
	out := binary.LittleEndian.AppendUint32(nil, uint32(len(keys)))
	for _, h := range keys {
		out = binary.LittleEndian.AppendUint32(out, h)
	}
	return OK, out
}

func (f *FakeX100VI) objectInfo(handle uint32) (uint16, []byte) {
	if handle == 0 && f.Mode == "raw_conv" { // the settings file: a bare 56-byte ObjectInfo
		info := make([]byte, 56)
		binary.LittleEndian.PutUint16(info[4:], 0x5000)
		binary.LittleEndian.PutUint32(info[8:], uint32(len(f.SettingsFile)))
		return OK, info
	}
	obj, ok := f.Objects[handle]
	if !ok {
		return InvalidHandle, nil
	}
	info := make([]byte, 52)
	binary.LittleEndian.PutUint32(info, 0x10000001)
	binary.LittleEndian.PutUint16(info[4:], obj.Format)
	binary.LittleEndian.PutUint32(info[8:], uint32(len(obj.Data)))
	info = append(info, ptp.PackString("DSCF0001.jpg")...)
	info = append(info, ptp.PackString("20261123T080000")...)
	return OK, append(info, 0, 0)
}

func (f *FakeX100VI) object(handle uint32) (uint16, []byte) {
	if handle == 0 && f.Mode == "raw_conv" {
		return OK, append([]byte(nil), f.SettingsFile...)
	}
	obj, ok := f.Objects[handle]
	if !ok {
		return InvalidHandle, nil
	}
	return OK, append([]byte(nil), obj.Data...)
}

func (f *FakeX100VI) sendObjectInfo(params []uint32, data []byte) uint16 {
	r := ptp.NewReader(data)
	r.U32()
	format := r.U16()
	r.U16()
	size := r.U32()
	if format != 0x5000 || len(data) != 1076 || len(params) != 2 || params[0] != 0 || params[1] != 0 {
		return GeneralError
	}
	f.incoming = &[2]int{int(format), int(size)}
	return OK
}

func (f *FakeX100VI) sendObject(data []byte) uint16 {
	announced := f.incoming
	f.incoming = nil
	if announced == nil || len(data) != announced[1] {
		return GeneralError
	}
	if !bytes.HasPrefix(data, []byte("FUJIFILMX-BACKUP")) || len(data) != len(f.SettingsFile) {
		return GeneralError
	}
	stored := append([]byte(nil), data...)
	stored[0x108] += 2 // the body bumps its own counter and flag on restore
	stored[0x716] = 3
	f.SettingsFile = stored
	f.SettingsWrites++
	return OK
}

// -- helpers for assertions -------------------------------------------------------------------

// SlotValue is a register setting in recipe vocabulary.
func (f *FakeX100VI) SlotValue(slot int, key string) any {
	return model.DecodeField(model.ByKey[key], f.Slots[slot][key])
}

// WriteCodes lists the property codes of every SetDevicePropValue so far.
func (f *FakeX100VI) WriteCodes() []uint16 {
	out := make([]uint16, len(f.Writes))
	for i, w := range f.Writes {
		out[i] = w.Code
	}
	return out
}
