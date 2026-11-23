// Package rawconv is in-camera RAW conversion: develop a RAF with a recipe on the X100VI's own
// processor.
//
// The flow, with the camera in USB RAW CONV./BACKUP RESTORE mode (sequence as documented in
// github.com/zgrimz/fujifilm-ptp-reference, confirmed on the X100VI):
//
//	SendObjectInfo 0x900C + SendObject2 0x900D   upload the RAF as FUP_FILE.dat (format 0xF802)
//	GetDevicePropValue 0xD185                    profile describing the RAF's as-shot settings
//	SetDevicePropValue 0xD185                    patched profile, 629-byte host-write form
//	SetDevicePropValue 0xD183 = 0 | 1            develop: half size | full resolution
//	GetObjectHandles / GetObject / DeleteObject  fetch the image and remove it from the buffer
//
// Profile cell 2 picks the output: JPEG 7, HEIF 18, TIFF 8-bit 9, TIFF 16-bit 11 (each confirmed by
// developing on the X100VI). The body produces TIFF only at full resolution; at half size it returns a
// JPEG.
//
// Sequence and profile layout follow github.com/zgrimz/fujifilm-ptp-reference (docs 05, 06). The
// profile cells hold the same wire values as the custom-register properties in package model.
package rawconv

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/volodymyrsmirnov/x100vi-skill/internal/camera"
	"github.com/volodymyrsmirnov/x100vi-skill/internal/errs"
	"github.com/volodymyrsmirnov/x100vi-skill/internal/model"
	"github.com/volodymyrsmirnov/x100vi-skill/internal/omap"
	"github.com/volodymyrsmirnov/x100vi-skill/internal/ptp"
)

const (
	PropTrigger = 0xD183
	FormatRAF   = 0xF802
	FormatJPEG  = 0x3801
	RAFMagic    = "FUJIFILMCCD-RAW "

	Table     = 0x1FD // cell i lives at Table + 4*i, signed 32-bit little endian
	HostCells = 30    // the host-write form: 30 cells (629 bytes) with numParams = 29
	// CameraCells is the fewest cells a profile the camera hands back may have.
	CameraCells = 28

	CFileType, CExposure, CDR, CDRP, CFilm, CWBSource = 2, 5, 6, 7, 8, 11
	FileTypeJPEG                                      = 7

	// DefaultTimeout is how long Develop waits for the image when given no timeout.
	DefaultTimeout = 90 * time.Second
)

// PollInterval is how often Develop asks for the new object.
var PollInterval = 250 * time.Millisecond

// OutputFormat is one image type the camera can develop to.
type OutputFormat struct {
	Name         string
	FileType     int    // profile cell 2
	ObjectFormat uint16 // PTP ObjectFormat of the developed image
	Extension    string
	FullOnly     bool // the body ignores the file type at half size
	Label        string
}

// Matches checks the image's magic bytes against the format.
func (f OutputFormat) Matches(image []byte) bool {
	switch f.Extension {
	case "jpg":
		return bytes.HasPrefix(image, []byte{0xFF, 0xD8})
	case "heif":
		return len(image) >= 8 && string(image[4:8]) == "ftyp"
	}
	return bytes.HasPrefix(image, []byte("II*\x00")) || bytes.HasPrefix(image, []byte("MM\x00*"))
}

// OutputFormatNames are the output formats in the order shown to people.
var OutputFormatNames = []string{"jpeg", "heif", "tiff", "tiff16"}

// OutputFormats maps a name to its format. Tests may replace an entry.
var OutputFormats = map[string]OutputFormat{
	"jpeg":   {"jpeg", 7, FormatJPEG, "jpg", false, "JPEG"},
	"heif":   {"heif", 18, 0x3812, "heif", false, "HEIF"},
	"tiff":   {"tiff", 9, 0x380D, "tif", true, "8-bit TIFF"},
	"tiff16": {"tiff16", 11, 0x380D, "tif", true, "16-bit TIFF"},
}

type cellKey struct {
	Key  string
	Cell int
}

// Cells maps recipe keys to profile cells, in Python's order.
var Cells = []cellKey{
	{"image_size", 3}, {"image_quality", 4}, {"dynamic_range", 6}, {"film_simulation", 8}, {"grain", 9},
	{"color_chrome_effect", 10}, {"white_balance.mode", 12}, {"white_balance.shift_r", 13},
	{"white_balance.shift_b", 14}, {"white_balance.kelvin", 15}, {"highlight", 16}, {"shadow", 17}, {"color", 18},
	{"sharpness", 19}, {"noise_reduction", 20}, {"lens_modulation_optimizer", 21}, {"color_space", 22},
	{"monochromatic.warm_cool", 23}, {"smooth_skin", 24}, {"color_chrome_fx_blue", 25},
	{"monochromatic.magenta_green", 26}, {"clarity", 27},
}

func cellOf(key string) (int, bool) {
	for _, c := range Cells {
		if c.Key == key {
			return c.Cell, true
		}
	}
	return 0, false
}

// DRPNames are the D-range priority levels a conversion knows, in order. Confirmed from output EXIF.
// "plus" (3) is a level only the converter offers: it compresses tones at least as much as strong.
// Auto has no code; a frame shot with Auto carries the level the camera chose.
var DRPNames = []string{"off", "weak", "strong", "plus"}

// DRPCell maps a D-range priority level to its profile value.
var DRPCell = map[string]int{"off": 0, "weak": 1, "strong": 2, "plus": 3}

// pyG formats a number like Python's {:g}.
func pyG(v float64) string { return strconv.FormatFloat(v, 'g', 6, 64) }

// -- RAF files -----------------------------------------------------------------------------

// RAFInfo describes a RAF file.
type RAFInfo struct {
	Path     string
	Model    string
	Firmware string
	Size     int64
}

func slice(b []byte, lo, hi int) []byte {
	if lo > len(b) {
		return nil
	}
	return b[lo:min(hi, len(b))]
}

func ascii(b []byte) string {
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

// InspectRAF checks that a file is a RAF this camera can develop, before uploading tens of megabytes.
func InspectRAF(path string) (RAFInfo, error) {
	st, err := os.Stat(path)
	if err != nil || !st.Mode().IsRegular() {
		return RAFInfo{}, errs.New(errs.ConversionFailed, path+" does not exist",
			"Give the path to a .RAF file shot on an X100VI.")
	}
	fh, err := os.Open(path)
	if err != nil {
		return RAFInfo{}, err
	}
	defer fh.Close()
	head := make([]byte, 0x40)
	n, err := io.ReadFull(fh, head)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return RAFInfo{}, err
	}
	head = head[:n]
	name := filepath.Base(path)
	if !bytes.HasPrefix(head, []byte(RAFMagic)) {
		kind := "not a Fujifilm RAF"
		if bytes.HasPrefix(head, []byte{0xFF, 0xD8}) {
			kind = "a JPEG"
		}
		return RAFInfo{}, errs.New(errs.ConversionFailed, fmt.Sprintf("%s is %s", name, kind),
			"Previews are developed from RAW: shoot RAW or RAW+JPEG and pass the .RAF file.")
	}
	rafModel := ascii(slice(head, 0x1C, 0x3C))
	firmware := ascii(slice(head, 0x3C, 0x40))
	if rafModel != camera.SupportedModel {
		shot := rafModel
		if shot == "" {
			shot = "different camera"
		}
		return RAFInfo{}, errs.New(errs.ConversionFailed, fmt.Sprintf("%s was shot on a %s, not an X100VI", name, shot),
			"The camera only develops RAFs from its own model.")
	}
	return RAFInfo{Path: path, Model: rafModel, Firmware: firmware, Size: st.Size()}, nil
}

// -- the profile ---------------------------------------------------------------------------

// Profile is a 0xD185 blob with cell access; it keeps every byte it does not understand.
type Profile struct {
	Data []byte
}

// NewProfile wraps a copy of data.
func NewProfile(data []byte) *Profile { return &Profile{Data: append([]byte(nil), data...)} }

// Clone is an independent copy.
func (p *Profile) Clone() *Profile { return NewProfile(p.Data) }

// Cells is the number of whole cells in the blob.
func (p *Profile) Cells() int {
	if len(p.Data) < Table {
		return 0
	}
	return (len(p.Data) - Table) / 4
}

// Get reads cell i (0 if the blob is too short).
func (p *Profile) Get(i int) int {
	at := Table + 4*i
	if at < 0 || at+4 > len(p.Data) {
		return 0
	}
	return int(int32(binary.LittleEndian.Uint32(p.Data[at:])))
}

// Set writes cell i; values from 0x80000000 up are stored as their 32-bit pattern.
func (p *Profile) Set(i int, value int) {
	binary.LittleEndian.PutUint32(p.Data[Table+4*i:], uint32(value))
}

// Values are all cells.
func (p *Profile) Values() []int {
	out := make([]int, p.Cells())
	for i := range out {
		out[i] = p.Get(i)
	}
	return out
}

// IOPCode is the profile's IOP code string (e.g. FF179503).
func (p *Profile) IOPCode() string {
	r := ptp.NewReader(p.Data)
	r.P = 2
	return r.String()
}

// Validate refuses to patch anything that does not look like a profile at the known base.
func (p *Profile) Validate() error {
	if len(p.Data) < Table+4*CameraCells {
		return errs.New(errs.ConversionFailed, fmt.Sprintf("unexpected conversion profile (%d bytes)", len(p.Data)), "")
	}
	dr, film := p.Get(CDR), p.Get(CFilm)
	_, knownFilm := model.FilmSimulation.Name(film)
	if (dr != 100 && dr != 200 && dr != 400) || !knownFilm {
		return errs.New(errs.ConversionFailed,
			fmt.Sprintf("unexpected conversion profile (dynamic range %d, film simulation %d)", dr, film),
			"This firmware may use a different profile layout; nothing was changed.")
	}
	return nil
}

// HostForm is the host-write form: 30 cells, numParams 29, IOPCode length counting its NUL.
func (p *Profile) HostForm() ([]byte, error) {
	want := Table + 4*HostCells
	if len(p.Data) > want {
		return nil, errs.New(errs.ConversionFailed,
			fmt.Sprintf("conversion profile is longer than the known host form (%d bytes)", len(p.Data)), "")
	}
	out := make([]byte, want)
	copy(out, p.Data)
	binary.LittleEndian.PutUint16(out, HostCells-1)
	if len(p.IOPCode()) == 8 {
		out[2] = 9
	}
	return out, nil
}

// Describe is the as-shot settings of the loaded RAF, in recipe vocabulary.
func Describe(p *Profile) *omap.Map {
	out := omap.New()
	for _, c := range Cells {
		if c.Cell < p.Cells() {
			model.Put(out, c.Key, model.DecodeField(model.ByKey[c.Key], p.Get(c.Cell)&0xFFFF))
		}
	}
	if !model.MonoSims[p.Get(CFilm)] {
		out.Delete("monochromatic")
	}
	if p.Get(CWBSource) == 1 {
		// mode and kelvin are placeholders while the RAF's own white balance is in use
		out.Set("white_balance", omap.New().Set("mode", "as_shot").Set("shift_r", p.Get(13)).Set("shift_b", p.Get(14)))
	} else if wb, ok := out.Get("white_balance"); ok {
		if m, isMap := wb.(*omap.Map); isMap {
			if mode, _ := m.Get("mode"); mode != "kelvin" {
				m.Delete("kelvin")
			}
		}
	}
	priority := p.Get(CDRP)
	label := fmt.Sprintf("unknown:%d", priority)
	for name, v := range DRPCell {
		if v == priority {
			label = name
		}
	}
	out.Set("d_range_priority", label)
	out.Set("exposure_ev", math.Round(float64(p.Get(CExposure))/1000*100)/100)
	return out
}

// -- applying a recipe -----------------------------------------------------------------------

// Adjustment is something the render does differently from what the recipe asks, and why.
type Adjustment struct {
	Setting   string
	Requested any
	Applied   any
	Reason    string
}

// AsMap is the --json form.
func (a Adjustment) AsMap() *omap.Map {
	return omap.New().Set("setting", a.Setting).Set("requested", a.Requested).Set("applied", a.Applied).
		Set("reason", a.Reason)
}

func (a Adjustment) String() string {
	return fmt.Sprintf("%s: asked for %v, rendered with %v (%s)", a.Setting, a.Requested, a.Applied, a.Reason)
}

// CheckExposure turns exposure compensation for a conversion into thirds of a stop (-2..+3 EV).
func CheckExposure(exposureEV float64) (int, error) {
	thirds := math.RoundToEven(exposureEV * 3)
	if math.IsNaN(exposureEV) || math.IsInf(exposureEV, 0) || math.Abs(exposureEV*3-thirds) > 0.05 || thirds < -6 || thirds > 9 {
		return 0, errs.Recipe(fmt.Sprintf("exposure %s EV is not valid for a conversion", pyG(exposureEV)),
			"Use steps of 1/3 EV from -2 to +3, e.g. 0.33, 0.67, 1, -0.33. The camera ignores "+
				"anything outside -2..+3 when developing.")
	}
	return int(thirds), nil
}

func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case string:
		return x != ""
	case bool:
		return x
	case float64:
		return x != 0
	case int:
		return x != 0
	}
	return true
}

// ApplyRecipe returns a patched copy of the as-shot profile, plus every deviation from the recipe.
//
// drpOverride (off, weak, strong, plus) replaces the recipe's own D-range priority for this render
// only; "" means no override.
func ApplyRecipe(asShot *Profile, recipe map[string]any, exposureEV float64, drpOverride string) (*Profile, []Adjustment, error) {
	adjustments := []Adjustment{}
	asked := "off"
	if v := model.Get(recipe, "d_range_priority"); truthy(v) {
		asked = strings.ToLower(fmt.Sprint(v))
	}
	writes, err := model.EncodeRecipe(recipe, false)
	if err != nil {
		return nil, nil, err
	}
	thirds, err := CheckExposure(exposureEV)
	if err != nil {
		return nil, nil, err
	}
	priority := asked
	if drpOverride != "" {
		priority = strings.ToLower(drpOverride)
		if _, ok := DRPCell[priority]; !ok {
			return nil, nil, errs.Recipe(fmt.Sprintf("d_range_priority %s is not a preview level", model.Quote(drpOverride)),
				fmt.Sprintf("Use one of: %s.", strings.Join(DRPNames, ", ")))
		}
		if priority != asked {
			adjustments = append(adjustments, Adjustment{"d_range_priority", asked, priority,
				"chosen with --d-range-priority for this preview; the recipe is unchanged"})
		}
	} else if _, ok := DRPCell[priority]; !ok {
		return nil, nil, errs.Recipe("d_range_priority auto cannot be previewed",
			"The conversion has no encoding for Auto. Add --d-range-priority weak, strong or plus to "+
				"preview a level; the recipe itself can still be saved to the camera with auto.")
	}
	prof := asShot.Clone()
	// A frame shot with D-range priority carries it in its profile; while it is on, the converter
	// ignores dynamic range and tone, so the recipe's value always replaces it.
	prof.Set(CDRP, DRPCell[priority])
	shotDR := asShot.Get(CDR)
	for _, w := range writes {
		cell, ok := cellOf(w.Field.Key)
		if !ok {
			continue
		}
		var value int
		switch {
		case w.Field.Key == "dynamic_range":
			// No Auto encoding exists for a conversion, and a DR above the frame's own renders with
			// a green cast: use the camera's Auto ceiling (200) and cap at the as-shot value.
			want, label := w.Wire, strconv.Itoa(w.Wire)
			if w.Wire == 0xFFFF {
				want, label = 200, "auto"
			}
			if want > shotDR {
				adjustments = append(adjustments, Adjustment{"dynamic_range", label, strconv.Itoa(shotDR),
					fmt.Sprintf("this frame was shot at DR%d; a RAF cannot be developed above its own dynamic "+
						"range. On the camera the recipe will use the full value when ISO allows it", shotDR)})
				want = shotDR
			} else if w.Wire == 0xFFFF {
				adjustments = append(adjustments, Adjustment{"dynamic_range", "auto", "200",
					"Auto has no conversion encoding; 200 is the camera's Auto ceiling"})
			}
			value = want
		case w.Field.Kind == model.Scaled || w.Field.Kind == model.Int:
			value = model.Signed16(w.Wire)
		default:
			value = w.Wire // grain Off is 1 here, the code the camera reports for an as-shot Off
		}
		if cell >= prof.Cells() {
			prof.Data = append(prof.Data, make([]byte, Table+4*(cell+1)-len(prof.Data))...)
		}
		prof.Set(cell, value)
	}
	if priority != "off" {
		var ignored []string
		for _, k := range []string{"dynamic_range", "highlight", "shadow"} {
			if _, ok := recipe[k]; ok {
				ignored = append(ignored, k)
			}
		}
		if len(ignored) > 0 {
			adjustments = append(adjustments, Adjustment{strings.Join(ignored, ", "), "as given", "ignored",
				fmt.Sprintf("d_range_priority is %s, and while it is on the camera ignores dynamic range and tone", priority)})
		}
	}
	prof.Set(CFileType, FileTypeJPEG)
	prof.Set(CWBSource, 2) // take white balance from the profile, not from the RAF
	prof.Set(CExposure, int(math.RoundToEven(float64(thirds)*1000/3)))
	return prof, adjustments, nil
}

// -- the conversion sequence -----------------------------------------------------------------

// RawConverter uploads RAFs to a camera and develops them there.
type RawConverter struct {
	Camera  *camera.Camera
	Session *ptp.Session
	AsShot  *Profile
	stalled bool // a conversion timed out: the body may still be busy, so never trigger another
}

// NewRawConverter works through an open camera.
func NewRawConverter(c *camera.Camera) *RawConverter {
	return &RawConverter{Camera: c, Session: c.Session}
}

// Upload loads a RAF into the camera's conversion buffer and returns its as-shot profile.
func (rc *RawConverter) Upload(raf []byte) (*Profile, error) {
	if err := rc.Camera.RequireReady(); err != nil {
		return nil, err
	}
	if !bytes.HasPrefix(raf, []byte(RAFMagic)) {
		return nil, errs.New(errs.ConversionFailed, "not a RAF file", "")
	}
	info := make([]byte, 52)
	binary.LittleEndian.PutUint16(info[4:], FormatRAF)
	binary.LittleEndian.PutUint32(info[8:], uint32(len(raf)))
	info = append(info, ptp.PackString("FUP_FILE.dat")...)
	info = append(info, 0, 0, 0)
	if _, err := rc.Session.Check(ptp.OpVendorSendObjInfo, []uint32{0, 0, 0}, info, "announce the RAF"); err != nil {
		return nil, err
	}
	if _, err := rc.Session.Check(ptp.OpVendorSendObject, nil, raf, "upload the RAF"); err != nil {
		return nil, err
	}
	data, err := rc.Session.PropRaw(camera.ProfileProp)
	if err != nil {
		if !errs.Is(err, errs.CameraRefused) {
			return nil, err
		}
		return nil, errs.Wrap(errs.ConversionFailed, err, "the camera did not accept this RAF for conversion",
			fmt.Sprintf("It must be an unmodified RAF shot on an X100VI (%s).", err.Error()))
	}
	rc.AsShot = NewProfile(data)
	if err := rc.AsShot.Validate(); err != nil {
		return nil, err
	}
	return rc.AsShot, nil
}

func joinInts(v []int) string {
	parts := make([]string, len(v))
	for i, n := range v {
		parts[i] = strconv.Itoa(n)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// Develop writes a profile, verifies the camera stored it, develops, and returns the image bytes.
// timeout <= 0 means DefaultTimeout; output "" means jpeg. After a timeout it never triggers again:
// the body may still be converting.
func (rc *RawConverter) Develop(profile *Profile, full bool, timeout time.Duration, output string) ([]byte, error) {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	if output == "" {
		output = "jpeg"
	}
	format, ok := OutputFormats[output]
	if !ok {
		return nil, errs.Recipe(fmt.Sprintf("unknown output format %s", model.Quote(output)),
			fmt.Sprintf("Use one of: %s.", strings.Join(OutputFormatNames, ", ")))
	}
	if format.FullOnly && !full {
		return nil, errs.Recipe(format.Label+" output needs full resolution",
			"The camera produces TIFF only at full size: add --full.")
	}
	if rc.stalled {
		return nil, errs.New(errs.ConversionFailed, "an earlier conversion on this connection never finished",
			"Switch the camera off and on (remove the battery briefly if it will not switch off), reconnect, "+
				"then run the command again.")
	}
	prof := profile.Clone()
	prof.Set(CFileType, format.FileType)
	want, err := prof.HostForm()
	if err != nil {
		return nil, err
	}
	if err := rc.Session.SetRaw(camera.ProfileProp, want, "send the conversion settings"); err != nil {
		return nil, err
	}
	stored, err := rc.Session.PropRaw(camera.ProfileProp)
	if err != nil {
		return nil, err
	}
	back, sent := NewProfile(stored), NewProfile(want)
	if back.Validate() != nil || back.Cells() < CameraCells {
		// An empty or foreign readback would pass the comparison below, which only covers the cells
		// both forms have.
		return nil, errs.New(errs.ConversionFailed,
			fmt.Sprintf("the camera answered with unreadable conversion settings (%d bytes)", len(stored)),
			"Nothing was developed. Power-cycle the camera and try again.")
	}
	var diff []int
	for i := 0; i < min(back.Cells(), sent.Cells()); i++ {
		if back.Get(i) != sent.Get(i) {
			diff = append(diff, i)
		}
	}
	if len(diff) > 0 {
		return nil, errs.New(errs.ConversionFailed,
			fmt.Sprintf("the camera stored different conversion settings (cells %s)", joinInts(diff)),
			"Nothing was developed. Power-cycle the camera and try again.")
	}
	handles, err := rc.Session.Handles()
	if err != nil {
		return nil, err
	}
	before := map[uint32]bool{}
	for _, h := range handles {
		before[h] = true
	}
	trigger := 0
	if full {
		trigger = 1
	}
	if err := rc.Session.SetRaw(PropTrigger, ptp.U16Bytes(trigger), "start the conversion"); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(timeout)
	var handle uint32
	found := false
	for !found && time.Now().Before(deadline) {
		code, _, data, err := rc.Session.Transact(ptp.OpGetObjectHandles, []uint32{0xFFFFFFFF, 0, 0}, nil)
		if err != nil {
			rc.stalled = true // the conversion may still be running
			return nil, err
		}
		if code == ptp.RCOK { // anything else: the body is still busy, ask again
			now, err := ptp.ParseHandles(data)
			if err != nil {
				rc.stalled = true // the conversion may still be running
				return nil, err
			}
			for _, h := range now {
				if !before[h] {
					handle, found = h, true
					break
				}
			}
		}
		if !found {
			time.Sleep(PollInterval)
		}
	}
	if !found {
		// Never trigger again after a timeout: the body may still be converting.
		rc.stalled = true
		return nil, errs.New(errs.ConversionFailed,
			fmt.Sprintf("the camera produced no image within %s s", pyG(timeout.Seconds())),
			"Do not retry yet. Switch the camera off and on (remove the battery briefly if it "+
				"will not switch off), reconnect, then run the command again.")
	}
	desc, err := rc.Session.Check(ptp.OpGetObjectInfo, []uint32{handle}, nil, "read the result's description")
	if err != nil {
		return nil, err
	}
	// The whole description must parse before the object is downloaded or deleted.
	info, err := ptp.ParseObjectInfo(desc)
	if err != nil {
		return nil, err
	}
	gotFormat, size := info.Format, info.Size
	image, gerr := rc.Session.Check(ptp.OpGetObject, []uint32{handle}, nil, "download the result")
	// remove only the object this conversion produced
	_, _, _, _ = rc.Session.Transact(ptp.OpDeleteObject, []uint32{handle}, nil)
	if gerr != nil {
		return nil, gerr
	}
	if len(image) != int(size) {
		return nil, errs.New(errs.ConversionFailed,
			fmt.Sprintf("the result was cut short (%d of %d bytes)", len(image), size), "Run the command again.")
	}
	if gotFormat != format.ObjectFormat || !format.Matches(image) {
		return nil, errs.New(errs.ConversionFailed,
			fmt.Sprintf("the camera returned something other than %s (format 0x%04X)", format.Label, gotFormat),
			"Try --format jpeg; this firmware may not offer that output.")
	}
	return image, nil
}
