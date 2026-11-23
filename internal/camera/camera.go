// Package camera is the camera: identity checks, and reading / writing recipes with full verification.
package camera

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/volodymyrsmirnov/x100vi-skill/internal/camera/settingsfile"
	"github.com/volodymyrsmirnov/x100vi-skill/internal/errs"
	"github.com/volodymyrsmirnov/x100vi-skill/internal/model"
	"github.com/volodymyrsmirnov/x100vi-skill/internal/omap"
	"github.com/volodymyrsmirnov/x100vi-skill/internal/ptp"
)

const (
	SupportedModel = "X100VI"
	PropBattery    = 0xD36B // "NN,0,0": charge in percent as the camera reports it
	PropUSBMode    = 0xD16E // read-only: 5 tether, 6 RAW conv./backup restore, 8 webcam
	USBModeRawConv = 6
	ProfileProp    = 0xD185
	// The 23 settings of the selected register are 0xD18E..0xD1A4; 0xD1A5 is the live image size.
	registerFirst = 0xD18E
	registerEnd   = 0xD1A5
)

// TestedFirmware lists the firmware versions the tool was measured against.
var TestedFirmware = []string{"1.31"}

// grainCodes are the grain properties (live and register).
var grainCodes = map[uint16]bool{0xD023: true, 0xD195: true}

// Info is what the camera says about itself.
type Info struct {
	Model          string
	Firmware       string
	Mode           string // raw_conv | card_reader | unknown
	BatteryPercent *int
	Warnings       []string
}

// Ready is true for an X100VI in the one USB mode that exposes settings.
func (i Info) Ready() bool { return i.Model == SupportedModel && i.Mode == "raw_conv" }

// AsMap is the --json form, in Python's key order.
func (i Info) AsMap() *omap.Map {
	var battery any
	if i.BatteryPercent != nil {
		battery = *i.BatteryPercent
	}
	warnings := i.Warnings
	if warnings == nil {
		warnings = []string{}
	}
	return omap.New().Set("model", i.Model).Set("firmware", i.Firmware).Set("usb_mode", i.Mode).
		Set("ready", i.Ready()).Set("battery_percent", battery).Set("warnings", warnings)
}

// Camera is a connected camera. Build one with Open, release it with Close.
type Camera struct {
	Session *ptp.Session
	Info    Info
}

// Open opens a PTP session on the transport and identifies the camera. On error the session and the
// transport are already closed.
func Open(t ptp.Transport) (*Camera, error) {
	s := ptp.NewSession(t)
	if err := s.Open(); err != nil {
		_ = s.Close()
		return nil, err
	}
	c := &Camera{Session: s}
	info, err := c.identify()
	if err != nil {
		_ = s.Close()
		return nil, err
	}
	c.Info = info
	return c, nil
}

// Close closes the session and the transport.
func (c *Camera) Close() error { return c.Session.Close() }

// Use opens the camera, runs fn and always releases the camera afterwards (Python's open_camera).
func Use(t ptp.Transport, fn func(*Camera) error) error {
	c, err := Open(t)
	if err != nil {
		return err
	}
	defer c.Close()
	return fn(c)
}

func refused(err error) bool { return errs.Is(err, errs.CameraRefused) }

func hasProp(props []uint16, code uint16) bool {
	for _, p := range props {
		if p == code {
			return true
		}
	}
	return false
}

// -- identity ----------------------------------------------------------------------------

func (c *Camera) identify() (Info, error) {
	di, err := c.Session.DeviceInfo()
	if err != nil {
		return Info{}, err
	}
	var mode string
	switch {
	case hasProp(di.Props, model.SlotSelect):
		mode = "raw_conv"
		if hasProp(di.Props, PropUSBMode) {
			v, err := c.Session.GetU16(PropUSBMode)
			if err == nil && v != USBModeRawConv {
				mode = "unknown"
			} else if err != nil && !refused(err) {
				return Info{}, err
			}
		}
	case len(di.Props) <= 8:
		mode = "card_reader"
	default:
		mode = "unknown"
	}
	info := Info{Model: di.Model, Firmware: di.Version, Mode: mode, Warnings: []string{}}
	tested := false
	for _, v := range TestedFirmware {
		tested = tested || v == di.Version
	}
	if di.Model == SupportedModel && !tested {
		info.Warnings = append(info.Warnings, fmt.Sprintf(
			"firmware %s has not been tested (tested: %s); "+
				"every write is verified by reading it back, but report anything odd",
			di.Version, strings.Join(TestedFirmware, ", ")))
	}
	if mode == "raw_conv" {
		text, err := c.Session.GetString(PropBattery)
		if err != nil && !refused(err) {
			return Info{}, err
		}
		if err == nil {
			if n, perr := strconv.Atoi(strings.TrimSpace(strings.Split(text, ",")[0])); perr == nil {
				info.BatteryPercent = &n
			}
		}
		if info.BatteryPercent != nil && *info.BatteryPercent <= 15 {
			info.Warnings = append(info.Warnings, fmt.Sprintf(
				"battery is at about %d%%; charge before converting RAFs", *info.BatteryPercent))
		}
	}
	return info, nil
}

// RequireReady refuses to go on unless this is an X100VI in the one USB mode that exposes settings.
func (c *Camera) RequireReady() error {
	if c.Info.Model != SupportedModel {
		name := c.Info.Model
		if name == "" {
			name = "camera of unknown model"
		}
		return errs.New(errs.UnsupportedCamera, fmt.Sprintf("this is a %s, not an X100VI", name),
			"Only the Fujifilm X100VI is supported; other bodies use different property codes.")
	}
	if c.Info.Mode != "raw_conv" {
		now := "a mode that does not expose settings"
		if c.Info.Mode == "card_reader" {
			now = "USB CARD READER"
		}
		return errs.New(errs.WrongMode, fmt.Sprintf("the camera is in %s, so its settings are not reachable", now),
			fmt.Sprintf("On the camera: %s. Then unplug and replug the USB cable. "+
				"(The X100VI has no tether mode; %s is the only one that works.)", errs.ModeMenu, errs.RawConvMode))
	}
	return nil
}

// -- helpers -----------------------------------------------------------------------------

func code(f model.Field, slot *int) uint16 {
	if slot == nil {
		return f.Live
	}
	return f.Slot
}

func (c *Camera) selectSlot(slot *int) error {
	if slot == nil {
		return nil
	}
	if *slot < 1 || *slot > 7 {
		return errs.Recipe("custom setting must be C1 to C7", "")
	}
	// Always select explicitly: right after the camera enters this mode the register
	// properties show the live settings, and a write would copy them into the register.
	return c.Session.SetU16(model.SlotSelect, *slot)
}

// codes are the properties a snapshot covers: the 23 register settings, or every live field.
func (c *Camera) codes(slot *int) []uint16 {
	var out []uint16
	if slot != nil {
		for p := uint16(registerFirst); p < registerEnd; p++ {
			out = append(out, p)
		}
		return out
	}
	for _, f := range model.Fields {
		out = append(out, f.Live)
	}
	return out
}

func (c *Camera) snapshot(slot *int) (map[uint16]int, error) {
	out := map[uint16]int{}
	for _, p := range c.codes(slot) {
		v, err := c.Session.GetU16(p)
		if err != nil {
			return nil, err
		}
		out[p] = v
	}
	return out, nil
}

// setGrainOff writes grain Off, which is written as 1 and keeps the last size, so a size goes first.
func (c *Camera) setGrainOff(p uint16, size string) (int, error) {
	first := 4
	if size == "small" {
		first = 2
	}
	if err := c.Session.SetU16(p, first); err != nil {
		return 0, err
	}
	if err := c.Session.SetU16(p, 1); err != nil {
		return 0, err
	}
	if size == "small" {
		return 6, nil
	}
	return 7, nil
}

// -- reading -----------------------------------------------------------------------------

// Read returns the current shooting settings (slot nil) or a custom register, as a recipe.
func (c *Camera) Read(slot *int) (*omap.Map, error) {
	if err := c.RequireReady(); err != nil {
		return nil, err
	}
	if err := c.selectSlot(slot); err != nil {
		return nil, err
	}
	recipe := omap.New()
	if slot != nil {
		name, err := c.Session.GetString(model.SlotName)
		if err != nil {
			return nil, err
		}
		recipe.Set("name", name)
	}
	wire := map[string]int{}
	for _, f := range model.Fields {
		v, err := c.Session.GetU16(code(f, slot))
		if err != nil {
			return nil, err
		}
		wire[f.Key] = v
	}
	film := wire["film_simulation"]
	kelvin := wire["white_balance.mode"] == model.WhiteBalance.Codes["kelvin"]
	rawOnly := wire["image_quality"] == model.ImageQuality.Codes["raw"]
	for _, f := range model.Fields {
		skip := (f.Needs == "mono" && !model.MonoSims[film]) || (f.Needs == "kelvin" && !kelvin) ||
			(f.Needs == "jpeg" && rawOnly) || (f.Needs == "colour" && model.NoColourSims[film])
		if !skip {
			model.Put(recipe, f.Key, model.DecodeField(f, wire[f.Key]))
		}
	}
	return recipe, nil
}

// Backup reads the live settings and every register.
func (c *Camera) Backup() (*omap.Map, error) {
	if err := c.RequireReady(); err != nil {
		return nil, err
	}
	live, err := c.Read(nil)
	if err != nil {
		return nil, err
	}
	slots := omap.New()
	for _, n := range model.Slots {
		r, err := c.Read(&n)
		if err != nil {
			return nil, err
		}
		slots.Set(strconv.Itoa(n), r)
	}
	return omap.New().Set("camera", c.Info.Model).Set("firmware", c.Info.Firmware).Set("live", live).
		Set("slots", slots), nil
}

// -- writing -----------------------------------------------------------------------------

func grainSize(recipe map[string]any) string {
	v := model.Get(recipe, "grain.size")
	if v == nil || v == "" {
		return "small"
	}
	return strings.ToLower(fmt.Sprint(v))
}

// Write applies a recipe and verifies the whole target afterwards.
//
// It returns a log of what was written. Settings the recipe did not mean to touch are put back if the
// camera changed them as a side effect; it fails with an invalid-recipe error if the final state
// differs from what was asked.
func (c *Camera) Write(recipe map[string]any, slot *int, partial bool) ([]string, error) {
	if err := c.RequireReady(); err != nil {
		return nil, err
	}
	writes, err := model.EncodeRecipe(recipe, partial)
	if err != nil {
		return nil, err
	}
	if err := c.selectSlot(slot); err != nil {
		return nil, err
	}
	before, err := c.snapshot(slot)
	if err != nil {
		return nil, err
	}
	var order []uint16 // intended, in first-written order
	intended := map[uint16]int{}
	intend := func(p uint16, v int) {
		if _, ok := intended[p]; !ok {
			order = append(order, p)
		}
		intended[p] = v
	}
	log := []string{}
	_, hasName := recipe["name"]
	name, _ := recipe["name"].(string)
	if slot != nil && (hasName || !partial) {
		// Name first: naming a factory-blank register was seen to seed its other settings
		// from the live ones, which the writes below then replace.
		if err := c.Session.SetRaw(model.SlotName, ptp.PackString(name), "write the register name"); err != nil {
			return nil, err
		}
	}
	// Monochromatic tint stays stored (hidden) under a colour sim and can only be written
	// while a B&W sim is selected. A full recipe for a colour sim clears it via Acros.
	film, haveFilm := 0, false
	for _, w := range writes {
		if w.Field.Key == "film_simulation" {
			film, haveFilm = w.Wire, true
			break
		}
	}
	tint := []uint16{code(model.ByKey["monochromatic.warm_cool"], slot), code(model.ByKey["monochromatic.magenta_green"], slot)}
	if !partial && haveFilm && !model.MonoSims[film] && (before[tint[0]] != 0 || before[tint[1]] != 0) {
		if err := c.Session.SetU16(code(model.ByKey["film_simulation"], slot), model.FilmSimulation.Codes["acros"]); err != nil {
			return nil, err
		}
		for _, p := range tint {
			if err := c.Session.SetU16(p, 0); err != nil {
				return nil, err
			}
			intend(p, 0)
		}
	}
	for _, w := range writes {
		p := code(w.Field, slot)
		var expect int
		var werr error
		if w.Field.Kind == model.Grain && w.Wire == 1 {
			expect, werr = c.setGrainOff(p, grainSize(recipe))
		} else {
			werr = c.Session.SetU16(p, w.Wire)
			expect = w.Wire
		}
		if werr != nil {
			if !refused(werr) {
				return nil, werr
			}
			return nil, errs.Wrap(errs.InvalidRecipe, werr,
				fmt.Sprintf("the camera refused %s = %s", w.Field.Key, model.Quote(model.Get(model.Merged(recipe, partial), w.Field.Key))),
				werr.Error()+". Another setting in this recipe may be locking it.")
		}
		intend(p, expect)
		log = append(log, w.Field.Key)
	}

	// Side effects: restore anything the recipe did not mean to change. Kelvin and shift are
	// stored per white-balance mode, so a mode change legitimately swaps them.
	wbGroup := map[uint16]bool{}
	for _, k := range []string{"white_balance.kelvin", "white_balance.shift_r", "white_balance.shift_b"} {
		wbGroup[code(model.ByKey[k], slot)] = true
	}
	wbChanged := false
	for _, w := range writes {
		wbChanged = wbChanged || w.Field.Key == "white_balance.mode"
	}
	after, err := c.snapshot(slot)
	if err != nil {
		return nil, err
	}
	for _, p := range c.codes(slot) {
		_, isIntended := intended[p]
		if isIntended || after[p] == before[p] || (wbChanged && wbGroup[p]) {
			continue
		}
		var rerr error
		if grainCodes[p] && (before[p] == 6 || before[p] == 7) {
			size := "large"
			if before[p] == 6 {
				size = "small"
			}
			_, rerr = c.setGrainOff(p, size)
		} else {
			rerr = c.Session.SetU16(p, before[p])
		}
		if rerr != nil && !refused(rerr) {
			return nil, rerr
		} // a refusal is reported by the final comparison below if it matters
	}
	final, err := c.snapshot(slot)
	if err != nil {
		return nil, err
	}
	var wrong []string
	for _, p := range order {
		if final[p] != intended[p] {
			wrong = append(wrong, fmt.Sprintf("0x%04X wanted 0x%04X, camera holds 0x%04X", p, intended[p], final[p]))
		}
	}
	if len(wrong) > 0 {
		return nil, errs.Recipe("the camera did not keep every setting: "+strings.Join(wrong, "; "),
			"Nothing else was changed. Run `x100vi get` to see the current state.")
	}
	if slot != nil && (hasName || !partial) {
		stored, err := c.Session.GetString(model.SlotName)
		if err != nil {
			return nil, err
		}
		if stored != name {
			return nil, errs.Recipe(fmt.Sprintf("the camera stored the name as %s", model.Quote(stored)), "")
		}
	}
	return log, nil
}

// -- the camera's own settings file ------------------------------------------------------

// ReadSettingsFile downloads every menu setting plus C1-C7, as the body exports it.
func (c *Camera) ReadSettingsFile() ([]byte, error) {
	if err := c.RequireReady(); err != nil {
		return nil, err
	}
	return settingsfile.Read(c.Session)
}

// RestoreSettingsFile writes a settings file back after checking it fits this body. current is the
// camera's file as just read (pass it when the caller saved a copy first), or nil to read it here.
func (c *Camera) RestoreSettingsFile(blob []byte, allowOtherBody bool, current []byte) error {
	if err := c.RequireReady(); err != nil {
		return err
	}
	newHead, err := settingsfile.ParseHeader(blob, "")
	if err != nil {
		return err
	}
	if current == nil {
		if current, err = c.ReadSettingsFile(); err != nil {
			return err
		}
	}
	here, err := settingsfile.ParseHeader(current, "")
	if err != nil {
		return err
	}
	if newHead.Model != here.Model {
		from := newHead.Model
		if from == "" {
			from = "unknown camera"
		}
		return errs.Recipe(fmt.Sprintf("this settings file is from an %s, not an %s", from, here.Model),
			"Only a file saved from the same camera model can be restored.")
	}
	if len(blob) != len(current) {
		return errs.Recipe(fmt.Sprintf("this settings file has a different size (%d bytes) from the camera's own "+
			"(%d bytes)", len(blob), len(current)),
			"It was probably saved under different firmware. Restore it only after updating the camera to "+
				"the firmware it was saved with.")
	}
	if newHead.BodyID != here.BodyID && !allowOtherBody {
		return errs.Recipe("this settings file was saved from a different X100VI body",
			"It also carries that body's network settings. Add --other-body to restore it anyway.")
	}
	return settingsfile.Write(c.Session, blob)
}

// ClearWBMemory zeroes the shift remembered for every white-balance mode of a register.
func (c *Camera) ClearWBMemory(slot int) error {
	if err := c.selectSlot(&slot); err != nil {
		return err
	}
	if err := c.Session.SetU16(model.ByKey["d_range_priority"].Slot, 0); err != nil {
		return err
	}
	for _, mode := range model.WhiteBalance.Names {
		if err := c.Session.SetU16(model.ByKey["white_balance.mode"].Slot, model.WhiteBalance.Codes[mode]); err != nil {
			return err
		}
		if mode == "kelvin" {
			if err := c.Session.SetU16(model.ByKey["white_balance.kelvin"].Slot, 10000); err != nil {
				return err
			}
		}
		if err := c.Session.SetU16(model.ByKey["white_balance.shift_r"].Slot, 0); err != nil {
			return err
		}
		if err := c.Session.SetU16(model.ByKey["white_balance.shift_b"].Slot, 0); err != nil {
			return err
		}
	}
	return nil
}
