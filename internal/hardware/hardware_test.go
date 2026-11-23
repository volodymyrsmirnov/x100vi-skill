// Package hardware tests the tool against a real X100VI. Skipped unless X100VI_HARDWARE=1.
//
//	X100VI_HARDWARE=1 go test ./internal/hardware -v
//	X100VI_HARDWARE=1 X100VI_TEST_RAF=/path/to/shot.RAF go test ./internal/hardware -v
//
// The camera must be in USB RAW CONV./BACKUP RESTORE mode. One custom setting is written and then
// restored byte for byte: C7 by default, or the one named in X100VI_TEST_SLOT.
//
// On macOS the camera may only be used from the main thread (ImageCaptureCore delivers its callbacks
// there), while Go runs tests on other goroutines, so every camera call goes through onMain.
package hardware

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/volodymyrsmirnov/x100vi-skill/internal/camera"
	"github.com/volodymyrsmirnov/x100vi-skill/internal/camera/settingsfile"
	"github.com/volodymyrsmirnov/x100vi-skill/internal/camlock"
	"github.com/volodymyrsmirnov/x100vi-skill/internal/exifcheck"
	"github.com/volodymyrsmirnov/x100vi-skill/internal/library"
	"github.com/volodymyrsmirnov/x100vi-skill/internal/model"
	"github.com/volodymyrsmirnov/x100vi-skill/internal/rawconv"
	"github.com/volodymyrsmirnov/x100vi-skill/internal/transport"
)

var mainCalls = make(chan func())

func TestMain(m *testing.M) {
	done := make(chan int)
	go func() { done <- m.Run() }()
	for {
		select {
		case f := <-mainCalls:
			f()
		case code := <-done:
			os.Exit(code)
		}
	}
}

// withCamera opens the camera on the main thread and runs fn there, holding the camera lock for the
// whole session so an x100vi command run meanwhile cannot select another register in the middle of it.
func withCamera(t *testing.T, fn func(*camera.Camera)) {
	t.Helper()
	if os.Getenv("X100VI_HARDWARE") != "1" {
		t.Skip("needs a real X100VI: set X100VI_HARDWARE=1")
	}
	release, err := camlock.Acquire()
	if err != nil {
		t.Fatalf("camera lock: %v", err)
	}
	defer release()
	var openErr error
	finished := make(chan struct{})
	panicked := make(chan any, 1)
	mainCalls <- func() {
		defer close(finished)
		defer func() {
			if r := recover(); r != nil {
				panicked <- r
			}
		}()
		tr, err := transport.Open()
		if err != nil {
			openErr = err
			return
		}
		openErr = camera.Use(tr, func(c *camera.Camera) error {
			fn(c)
			return nil
		})
	}
	<-finished
	select {
	case r := <-panicked:
		panic(r)
	default:
	}
	if openErr != nil {
		t.Fatalf("camera: %v", openErr)
	}
}

func testSlot() int {
	if n, err := strconv.Atoi(os.Getenv("X100VI_TEST_SLOT")); err == nil && n >= 1 && n <= 7 {
		return n
	}
	return 7
}

var probe = map[string]any{
	"name": "X100VI SKILL TEST", "film_simulation": "classic_neg",
	"grain":               map[string]any{"strength": "strong", "size": "large"},
	"color_chrome_effect": "weak", "color_chrome_fx_blue": "strong", "smooth_skin": "weak",
	"white_balance": map[string]any{"mode": "kelvin", "kelvin": 5550.0, "shift_r": 4.0, "shift_b": -7.0},
	"dynamic_range": "400", "highlight": -1.5, "shadow": 2.5, "color": -3.0, "sharpness": 3.0,
	"noise_reduction": -4.0, "clarity": -2.0,
}

// reporter is the part of *testing.T the camera helpers use, so the fake-camera tests can watch them fail.
type reporter interface {
	Helper()
	Errorf(format string, args ...any)
}

// Errors inside withCamera are reported with Errorf so the deferred restore still runs on the main thread.
func check(t reporter, err error, what string) bool {
	t.Helper()
	if err != nil {
		t.Errorf("%s: %v", what, err)
		return false
	}
	return true
}

func rawRegister(t reporter, c *camera.Camera, slot int) map[uint16][]byte {
	t.Helper()
	out := map[uint16][]byte{}
	if !check(t, c.Session.SetU16(model.SlotSelect, slot), "select") {
		return out
	}
	for code := uint16(0xD18D); code < 0xD1A5; code++ {
		b, err := c.Session.PropRaw(code)
		if check(t, err, "read register") {
			out[code] = b
		}
	}
	return out
}

func TestCameraIsReady(t *testing.T) {
	withCamera(t, func(c *camera.Camera) {
		check(t, c.RequireReady(), "ready")
		if c.Info.Model != "X100VI" || c.Info.BatteryPercent == nil {
			t.Errorf("unexpected identity: %+v", c.Info)
		}
	})
}

func TestReadsLiveAndEveryRegisterAsValidRecipes(t *testing.T) {
	withCamera(t, func(c *camera.Camera) {
		b, err := c.Backup()
		if !check(t, err, "backup") {
			return
		}
		plain := b.ToPlain()
		recipes := []any{plain["live"]}
		for _, r := range plain["slots"].(map[string]any) {
			recipes = append(recipes, r)
		}
		for _, r := range recipes {
			_, err := model.EncodeRecipe(r.(map[string]any), false)
			check(t, err, "encode")
		}
	})
}

func TestRegisterRoundTripAndExactRestore(t *testing.T) {
	slot := testSlot()
	withCamera(t, func(c *camera.Camera) { roundTripAndRestore(t, c, slot) })
}

// wbRemembered is what a register keeps for one white-balance mode: its shift, and for the kelvin mode
// also the colour temperature.
type wbRemembered struct{ ShiftR, ShiftB, Kelvin int }

var (
	propDRP    = model.ByKey["d_range_priority"].Slot
	propWBMode = model.ByKey["white_balance.mode"].Slot
	propShiftR = model.ByKey["white_balance.shift_r"].Slot
	propShiftB = model.ByKey["white_balance.shift_b"].Slot
	propKelvin = model.ByKey["white_balance.kelvin"].Slot
)

// wbMemory reads what the register remembers for every white-balance mode by selecting each mode in
// turn, the way ClearWBMemory walks them (D-range priority off first). After a complete walk the
// register's active mode and D-range priority are changed: re-apply the register's recipe afterwards.
// If the walk fails partway it puts back only what it changed itself (the active mode, then D-range
// priority) and reports false; selecting a mode leaves every remembered shift and the Kelvin as they were.
func wbMemory(t reporter, c *camera.Camera, slot int) (map[string]wbRemembered, bool) {
	t.Helper()
	if !check(t, c.Session.SetU16(model.SlotSelect, slot), "select") {
		return nil, false
	}
	activeDRP, errD := c.Session.GetU16(propDRP)
	activeMode, errM := c.Session.GetU16(propWBMode)
	if !check(t, errors.Join(errD, errM), "read the active WB mode and D-range priority") {
		return nil, false
	}
	complete := false
	defer func() {
		if complete {
			return
		}
		check(t, c.Session.SetU16(model.SlotSelect, slot), "select to put back the WB mode")
		check(t, c.Session.SetU16(propWBMode, activeMode), "put back the active WB mode")
		check(t, c.Session.SetU16(propDRP, activeDRP), "put back D-range priority")
	}()
	if !check(t, c.Session.SetU16(propDRP, 0), "D-range priority off") {
		return nil, false
	}
	out := map[string]wbRemembered{}
	for _, mode := range model.WhiteBalance.Names {
		if !check(t, c.Session.SetU16(propWBMode, model.WhiteBalance.Codes[mode]), "select WB "+mode) {
			return nil, false
		}
		var m wbRemembered
		var errR, errB, errK error
		m.ShiftR, errR = c.Session.GetU16(propShiftR)
		m.ShiftB, errB = c.Session.GetU16(propShiftB)
		if mode == "kelvin" {
			m.Kelvin, errK = c.Session.GetU16(propKelvin)
		}
		if !check(t, errors.Join(errR, errB, errK), "read WB "+mode) {
			return nil, false
		}
		out[mode] = m
	}
	complete = true
	return out, true
}

// restoreWBMemory puts back what wbMemory read. A Kelvin change zeroes the shift, so the temperature
// goes first; switching modes stores the current shift for the mode being left, so each mode's shift is
// written while that mode is selected. The active mode is left wherever the walk ends.
func restoreWBMemory(t reporter, c *camera.Camera, slot int, mem map[string]wbRemembered) bool {
	t.Helper()
	if !check(t, c.Session.SetU16(model.SlotSelect, slot), "select") ||
		!check(t, c.Session.SetU16(propDRP, 0), "D-range priority off") {
		return false
	}
	ok := true
	for _, mode := range model.WhiteBalance.Names {
		m, known := mem[mode]
		if !known {
			continue
		}
		if !check(t, c.Session.SetU16(propWBMode, model.WhiteBalance.Codes[mode]), "select WB "+mode) {
			ok = false
			continue
		}
		if mode == "kelvin" {
			ok = check(t, c.Session.SetU16(propKelvin, m.Kelvin), "restore kelvin") && ok
		}
		ok = check(t, c.Session.SetU16(propShiftR, m.ShiftR), "restore shift R of "+mode) && ok
		ok = check(t, c.Session.SetU16(propShiftB, m.ShiftB), "restore shift B of "+mode) && ok
	}
	return ok
}

// roundTripAndRestore writes the probe recipe to a register, checks it, and puts the register back
// exactly: its settings byte for byte and what it remembers for every white-balance mode.
//
// Nothing is written until a complete snapshot of the WB memory exists: the cleanup erases every
// remembered shift (ClearWBMemory) and relies on that snapshot to put them back.
func roundTripAndRestore(t reporter, c *camera.Camera, slot int) {
	t.Helper()
	if !check(t, c.RequireReady(), "ready") { // before the first selector write
		return
	}
	originalRaw := rawRegister(t, c, slot)
	original, err := c.Read(&slot)
	if !check(t, err, "read slot") {
		return
	}
	liveBefore, err := c.Read(nil)
	if !check(t, err, "read live") {
		return
	}
	reapply := func(what string) bool {
		_, err := c.Write(original.ToPlain(), &slot, false)
		return check(t, err, what)
	}
	memBefore, ok := wbMemory(t, c, slot)
	if !ok {
		t.Errorf("no complete snapshot of the WB memory: nothing was written, and the snapshot walk's own " +
			"changes (active WB mode, D-range priority) were put back")
		return
	}
	// A complete snapshot exists: only now arm the destructive cleanup and write the probe.
	func() {
		defer func() {
			check(t, c.ClearWBMemory(slot), "clear WB memory")
			reapply("restore")
			restoreWBMemory(t, c, slot, memBefore)
			reapply("re-apply the original recipe after restoring WB memory") // the original active mode
		}()
		if _, err := c.Write(probe, &slot, false); !check(t, err, "write probe") {
			return
		}
		got, err := c.Read(&slot)
		if !check(t, err, "read back") {
			return
		}
		plain := got.ToPlain()
		for key, want := range probe {
			if model.Quote(model.Plain(map[string]any{"v": plain[key]})["v"]) != model.Quote(want) {
				t.Errorf("%s: got %v, want %v", key, plain[key], want)
			}
		}
	}()
	memAfter, ok := wbMemory(t, c, slot)
	reapply("re-apply the original recipe after reading WB memory")
	if ok {
		for _, mode := range model.WhiteBalance.Names {
			if memAfter[mode] != memBefore[mode] {
				t.Errorf("WB memory of %s not restored: %+v -> %+v", mode, memBefore[mode], memAfter[mode])
			}
		}
	}
	after := rawRegister(t, c, slot)
	for code, b := range originalRaw {
		if !bytes.Equal(after[code], b) {
			t.Errorf("register 0x%04X not restored: % x -> % x", code, b, after[code])
		}
	}
	liveAfter, err := c.Read(nil)
	if check(t, err, "read live after") {
		a, _ := liveAfter.MarshalJSON()
		b, _ := liveBefore.MarshalJSON()
		if !bytes.Equal(a, b) {
			t.Errorf("live settings changed:\n%s\n%s", b, a)
		}
	}
}

func needRAF(t *testing.T) string {
	raf := os.Getenv("X100VI_TEST_RAF")
	if raf == "" {
		t.Skip("set X100VI_TEST_RAF to an X100VI .RAF file")
	}
	return raf
}

func TestDevelopsARAFAndTheOutputCarriesTheRecipe(t *testing.T) {
	if os.Getenv("X100VI_HARDWARE") != "1" {
		t.Skip("needs a real X100VI: set X100VI_HARDWARE=1")
	}
	path := needRAF(t)
	info, err := rawconv.InspectRAF(path)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	withCamera(t, func(c *camera.Camera) {
		conv := rawconv.NewRawConverter(c)
		shot, err := conv.Upload(data)
		if !check(t, err, "upload") {
			return
		}
		if film, _ := rawconv.Describe(shot).Get("film_simulation"); film == nil {
			t.Errorf("no film simulation in the as-shot profile")
		}
		for _, name := range []string{"desert-gold", "street-mono"} {
			recipe, err := library.Starter(name)
			if !check(t, err, "starter") {
				return
			}
			profile, adjustments, err := rawconv.ApplyRecipe(shot, recipe, 0, "")
			if !check(t, err, "apply") {
				return
			}
			image, err := conv.Develop(profile, false, 0, "jpeg")
			if !check(t, err, "develop") {
				return
			}
			if !bytes.HasPrefix(image, []byte{0xFF, 0xD8}) || len(image) < 500_000 {
				t.Errorf("%s: not a full JPEG (%d bytes)", name, len(image))
			}
			out := filepath.Join(dir, info.Model+"__"+name+".jpg")
			if !check(t, os.WriteFile(out, image, 0o644), "write") {
				return
			}
			if _, err := exec.LookPath("exiftool"); err == nil {
				effective := map[string]any{}
				for k, v := range recipe {
					effective[k] = v
				}
				for _, adj := range adjustments {
					if adj.Setting == "dynamic_range" {
						effective["dynamic_range"] = adj.Applied
					}
				}
				res, err := exifcheck.Compare(effective, out)
				if check(t, err, "exif") && len(res.Mismatches) > 0 {
					t.Errorf("%s: EXIF mismatches %+v", name, res.Mismatches)
				}
			}
		}
	})
}

func TestReadsTheSettingsFileTwiceIdentically(t *testing.T) {
	withCamera(t, func(c *camera.Camera) {
		first, err := c.ReadSettingsFile()
		if !check(t, err, "first read") {
			return
		}
		second, err := c.ReadSettingsFile()
		if !check(t, err, "second read") {
			return
		}
		h, err := settingsfile.ParseHeader(first, "")
		if check(t, err, "header") && (!bytes.Equal(first, second) || h.Model != "X100VI" || len(first) < 10_000) {
			t.Errorf("settings file: %d / %d bytes, model %q", len(first), len(second), h.Model)
		}
	})
}

func TestDevelopsHEIFAndFullSizeTIFF(t *testing.T) {
	if os.Getenv("X100VI_HARDWARE") != "1" {
		t.Skip("needs a real X100VI: set X100VI_HARDWARE=1")
	}
	data, err := os.ReadFile(needRAF(t))
	if err != nil {
		t.Fatal(err)
	}
	withCamera(t, func(c *camera.Camera) {
		conv := rawconv.NewRawConverter(c)
		shot, err := conv.Upload(data)
		if !check(t, err, "upload") {
			return
		}
		heif, err := conv.Develop(shot, false, 0, "heif")
		if !check(t, err, "heif") {
			return // never start another conversion after one failed: the body may still be busy
		}
		if len(heif) < 500_000 || string(heif[4:8]) != "ftyp" {
			t.Errorf("not a HEIF: %d bytes", len(heif))
		}
		tiff, err := conv.Develop(shot, true, 0, "tiff")
		if check(t, err, "tiff") && (len(tiff) < 100_000_000 || !bytes.HasPrefix(tiff, []byte("II*\x00"))) {
			t.Errorf("not a full TIFF: %d bytes", len(tiff))
		}
	})
}
