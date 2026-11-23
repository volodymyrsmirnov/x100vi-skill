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
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/volodymyrsmirnov/x100vi-skill/internal/camera"
	"github.com/volodymyrsmirnov/x100vi-skill/internal/camera/settingsfile"
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

// withCamera opens the camera on the main thread and runs fn there.
func withCamera(t *testing.T, fn func(*camera.Camera)) {
	t.Helper()
	if os.Getenv("X100VI_HARDWARE") != "1" {
		t.Skip("needs a real X100VI: set X100VI_HARDWARE=1")
	}
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

// Errors inside withCamera are reported with Errorf so the deferred restore still runs on the main thread.
func check(t *testing.T, err error, what string) bool {
	t.Helper()
	if err != nil {
		t.Errorf("%s: %v", what, err)
		return false
	}
	return true
}

func rawRegister(t *testing.T, c *camera.Camera, slot int) map[uint16][]byte {
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
	withCamera(t, func(c *camera.Camera) {
		originalRaw := rawRegister(t, c, slot)
		original, err := c.Read(&slot)
		if !check(t, err, "read slot") {
			return
		}
		liveBefore, err := c.Read(nil)
		if !check(t, err, "read live") {
			return
		}
		func() {
			defer func() {
				check(t, c.ClearWBMemory(slot), "clear WB memory")
				_, err := c.Write(original.ToPlain(), &slot, false)
				check(t, err, "restore")
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
	})
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
		if check(t, err, "heif") && (len(heif) < 500_000 || string(heif[4:8]) != "ftyp") {
			t.Errorf("not a HEIF: %d bytes", len(heif))
		}
		tiff, err := conv.Develop(shot, true, 0, "tiff")
		if check(t, err, "tiff") && (len(tiff) < 100_000_000 || !bytes.HasPrefix(tiff, []byte("II*\x00"))) {
			t.Errorf("not a full TIFF: %d bytes", len(tiff))
		}
	})
}
