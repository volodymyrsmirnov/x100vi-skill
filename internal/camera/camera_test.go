package camera

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/volodymyrsmirnov/x100vi-skill/internal/errs"
	"github.com/volodymyrsmirnov/x100vi-skill/internal/fakecam"
	"github.com/volodymyrsmirnov/x100vi-skill/internal/model"
	"github.com/volodymyrsmirnov/x100vi-skill/internal/omap"
)

const portraJSON = `{"name": "PORTRA", "film_simulation": "classic_chrome", "grain": {"strength": "strong", "size": "small"},
	"color_chrome_effect": "strong", "color_chrome_fx_blue": "weak",
	"white_balance": {"mode": "kelvin", "kelvin": 5200, "shift_r": 1, "shift_b": -6},
	"dynamic_range": "400", "highlight": 0, "shadow": -2, "color": 2, "sharpness": -2,
	"noise_reduction": -4, "clarity": -2}`

// recipe decodes JSON the way recipes arrive (numbers are float64).
func recipe(t *testing.T, text string) map[string]any {
	t.Helper()
	var r map[string]any
	if err := json.Unmarshal([]byte(text), &r); err != nil {
		t.Fatal(err)
	}
	return r
}

// plain turns a read into comparable JSON-shaped values (numbers become float64).
func plain(t *testing.T, m *omap.Map) map[string]any {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return recipe(t, string(b))
}

func open(t *testing.T, fake *fakecam.FakeX100VI) *Camera {
	t.Helper()
	cam, err := Open(fake)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cam.Close() })
	return cam
}

func setup(t *testing.T) (*Camera, *fakecam.FakeX100VI) {
	fake := fakecam.New()
	return open(t, fake), fake
}

func slot(n int) *int { return &n }

func mustWrite(t *testing.T, cam *Camera, r map[string]any, s *int, partial bool) {
	t.Helper()
	if _, err := cam.Write(r, s, partial); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func mustRead(t *testing.T, cam *Camera, s *int) map[string]any {
	t.Helper()
	got, err := cam.Read(s)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return plain(t, got)
}

func wantKind(t *testing.T, err error, kind errs.Kind) *errs.Error {
	t.Helper()
	e, ok := errs.As(err)
	if !ok || e.Kind != kind {
		t.Fatalf("want a %s error, got %v", kind.Name, err)
	}
	return e
}

func TestIdentityAndBattery(t *testing.T) {
	cam, _ := setup(t)
	b, _ := json.Marshal(cam.Info.AsMap())
	want := `{"model":"X100VI","firmware":"1.31","usb_mode":"raw_conv","ready":true,"battery_percent":80,"warnings":[]}`
	if string(b) != want {
		t.Fatalf("got %s", b)
	}
}

func TestCardReaderModeIsRefusedWithTheMenuPath(t *testing.T) {
	err := Use(fakecam.NewWith(fakecam.Options{Mode: "card_reader"}), func(cam *Camera) error {
		if cam.Info.Mode != "card_reader" || cam.Info.Ready() {
			t.Fatalf("info %+v", cam.Info)
		}
		_, err := cam.Read(slot(1))
		e := wantKind(t, err, errs.WrongMode)
		if !strings.Contains(e.Message, "USB CARD READER") {
			t.Fatal(e.Message)
		}
		if !strings.Contains(e.Hint, "CONNECTION MODE") || !strings.Contains(e.Hint, "USB RAW CONV./BACKUP RESTORE") {
			t.Fatal(e.Hint)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestOtherModelsAreRefused(t *testing.T) {
	cam := open(t, fakecam.NewWith(fakecam.Options{Model: "X-T5"}))
	_, err := cam.Write(map[string]any{}, slot(1), false)
	e := wantKind(t, err, errs.UnsupportedCamera)
	if !strings.Contains(e.Message, "X-T5") {
		t.Fatal(e.Message)
	}
}

func TestUntestedFirmwareAndLowBatteryWarnButWork(t *testing.T) {
	cam := open(t, fakecam.NewWith(fakecam.Options{Firmware: "1.40", Battery: 9}))
	var fw, bat bool
	for _, w := range cam.Info.Warnings {
		fw = fw || strings.Contains(w, "1.40")
		bat = bat || strings.Contains(w, "battery")
	}
	if !fw || !bat {
		t.Fatalf("warnings %v", cam.Info.Warnings)
	}
	mustWrite(t, cam, map[string]any{"film_simulation": "velvia"}, slot(2), false)
	if got := mustRead(t, cam, slot(2))["film_simulation"]; got != "velvia" {
		t.Fatal(got)
	}
}

func TestSessionIsClosedEvenWhenACommandFails(t *testing.T) {
	fake := fakecam.New()
	err := Use(fake, func(cam *Camera) error {
		_, err := cam.Write(map[string]any{"color": 99.0}, slot(1), false)
		return err
	})
	wantKind(t, err, errs.InvalidRecipe)
	if !fake.Closed || fake.SessionOpen {
		t.Fatalf("closed %v, session open %v", fake.Closed, fake.SessionOpen)
	}
}

func TestOpenClosesTheSessionWhenIdentifyFails(t *testing.T) {
	fake := fakecam.New()
	fake.Intercept = func(op uint16, params []uint32, data []byte, next func() (uint16, []byte)) (uint16, []byte) {
		if op == 0x1001 {
			return 0x2002, nil
		}
		return next()
	}
	if _, err := Open(fake); err == nil {
		t.Fatal("expected an error")
	}
	if !fake.Closed || fake.SessionOpen {
		t.Fatalf("closed %v, session open %v", fake.Closed, fake.SessionOpen)
	}
}

func TestWriteThenReadRoundTrips(t *testing.T) {
	cam, _ := setup(t)
	portra := recipe(t, portraJSON)
	mustWrite(t, cam, portra, slot(3), false)
	got := mustRead(t, cam, slot(3))
	for key, value := range portra {
		if !reflect.DeepEqual(got[key], value) {
			t.Errorf("%s: got %v, want %v", key, got[key], value)
		}
	}
}

func TestReadKeepsPythonKeyOrder(t *testing.T) {
	cam, _ := setup(t)
	mustWrite(t, cam, recipe(t, portraJSON), slot(3), false)
	got, err := cam.Read(slot(3))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"name", "d_range_priority", "dynamic_range", "film_simulation", "grain", "color_chrome_effect",
		"color_chrome_fx_blue", "smooth_skin", "white_balance", "highlight", "shadow", "color", "sharpness",
		"noise_reduction", "clarity", "image_quality", "image_size", "lens_modulation_optimizer", "color_space"}
	if !reflect.DeepEqual(got.Keys(), want) {
		t.Fatalf("keys %v", got.Keys())
	}
	wb, _ := got.Get("white_balance")
	if k := wb.(*omap.Map).Keys(); !reflect.DeepEqual(k, []string{"mode", "kelvin", "shift_r", "shift_b"}) {
		t.Fatalf("white_balance keys %v", k)
	}
}

func TestFactoryRegisterReadsAsBlank(t *testing.T) {
	cam, _ := setup(t)
	got, err := cam.Read(slot(5))
	if err != nil {
		t.Fatal(err)
	}
	if !model.IsBlank(got.ToPlain()) {
		t.Fatalf("not blank: %v", got.ToPlain())
	}
}

func TestWriteAlwaysSelectsTheRegisterFirst(t *testing.T) {
	cam, fake := setup(t)
	fake.Selected = 6
	mustWrite(t, cam, map[string]any{"name": "A", "film_simulation": "velvia"}, slot(2), false)
	if fake.SlotValue(2, "film_simulation") != "velvia" || fake.SlotValue(6, "film_simulation") != "provia" {
		t.Fatal(fake.SlotValue(2, "film_simulation"), fake.SlotValue(6, "film_simulation"))
	}
	if fake.Writes[0].Code != model.SlotSelect {
		t.Fatalf("first write 0x%04X", fake.Writes[0].Code)
	}
}

func TestFirstNamingQuirkDoesNotLeakLiveSettingsIntoTheRegister(t *testing.T) {
	cam, fake := setup(t)
	if fake.Live["image_quality"] != 1 { // the owner shoots RAW ...
		t.Fatal(fake.Live["image_quality"])
	}
	mustWrite(t, cam, map[string]any{"name": "A", "film_simulation": "velvia"}, slot(2), false)
	if fake.SlotValue(2, "image_quality") != "fine" { // ... but the register keeps its own quality
		t.Fatal(fake.SlotValue(2, "image_quality"))
	}
	if fake.SlotValue(2, "dynamic_range") != "100" {
		t.Fatal(fake.SlotValue(2, "dynamic_range"))
	}
}

func TestGrainOffKeepsTheRequestedSize(t *testing.T) {
	cam, fake := setup(t)
	mustWrite(t, cam, recipe(t, `{"grain": {"strength": "strong", "size": "small"}}`), slot(1), false)
	mustWrite(t, cam, recipe(t, `{"grain": {"strength": "off", "size": "large"}}`), slot(1), false)
	if fake.Slots[1]["grain"] != 7 {
		t.Fatal(fake.Slots[1]["grain"])
	}
	got := mustRead(t, cam, slot(1))["grain"]
	if !reflect.DeepEqual(got, map[string]any{"strength": "off", "size": "large"}) {
		t.Fatal(got)
	}
}

func TestKelvinChangeDoesNotLoseTheShift(t *testing.T) {
	cam, _ := setup(t)
	wb := `{"mode": "kelvin", "kelvin": 5500, "shift_r": 9, "shift_b": -9}`
	mustWrite(t, cam, recipe(t, `{"white_balance": `+wb+`}`), slot(1), false)
	if got := mustRead(t, cam, slot(1))["white_balance"]; !reflect.DeepEqual(got, recipe(t, wb)) {
		t.Fatal(got)
	}
}

func TestDRangePriorityCanBeSetAndCleared(t *testing.T) {
	cam, _ := setup(t)
	mustWrite(t, cam, map[string]any{"d_range_priority": "strong"}, slot(1), false)
	if got := mustRead(t, cam, slot(1))["d_range_priority"]; got != "strong" {
		t.Fatal(got)
	}
	mustWrite(t, cam, recipe(t, `{"dynamic_range": "400", "highlight": 2, "shadow": -1}`), slot(1), false)
	got := mustRead(t, cam, slot(1))
	if got["d_range_priority"] != "off" || got["dynamic_range"] != "400" || got["highlight"] != 2.0 || got["shadow"] != -1.0 {
		t.Fatal(got)
	}
}

func TestMonochromeRecipeSkipsColourAndSetsTint(t *testing.T) {
	cam, _ := setup(t)
	mustWrite(t, cam, recipe(t, `{"film_simulation": "acros_r", "monochromatic": {"warm_cool": -5, "magenta_green": 7}}`), slot(4), false)
	got := mustRead(t, cam, slot(4))
	if !reflect.DeepEqual(got["monochromatic"], map[string]any{"warm_cool": -5.0, "magenta_green": 7.0}) {
		t.Fatal(got["monochromatic"])
	}
	if _, ok := got["color"]; ok {
		t.Fatal("color present")
	}
}

func TestHiddenMonoTintIsClearedByAFullColourRecipe(t *testing.T) {
	cam, fake := setup(t)
	mustWrite(t, cam, recipe(t, `{"film_simulation": "acros", "monochromatic": {"warm_cool": 9, "magenta_green": -9}}`), slot(4), false)
	mustWrite(t, cam, map[string]any{"film_simulation": "velvia"}, slot(4), false)
	if fake.Slots[4]["monochromatic.warm_cool"] != 0 || fake.Slots[4]["monochromatic.magenta_green"] != 0 {
		t.Fatal(fake.Slots[4])
	}
	if fake.SlotValue(4, "film_simulation") != "velvia" {
		t.Fatal(fake.SlotValue(4, "film_simulation"))
	}
}

func TestPartialWriteChangesOnlyTheGivenSettings(t *testing.T) {
	cam, _ := setup(t)
	mustWrite(t, cam, recipe(t, portraJSON), slot(3), false)
	mustWrite(t, cam, map[string]any{"film_simulation": "reala_ace"}, slot(3), true)
	got := mustRead(t, cam, slot(3))
	if got["film_simulation"] != "reala_ace" || got["name"] != "PORTRA" || got["color"] != 2.0 {
		t.Fatal(got)
	}
}

func TestLiveSettingsCanBeWrittenAndRegistersStayPut(t *testing.T) {
	cam, _ := setup(t)
	mustWrite(t, cam, recipe(t, `{"film_simulation": "classic_neg", "color": -2}`), nil, false)
	live := mustRead(t, cam, nil)
	if _, named := live["name"]; live["film_simulation"] != "classic_neg" || live["color"] != -2.0 || named {
		t.Fatal(live)
	}
	for _, n := range model.Slots {
		got, err := cam.Read(slot(n))
		if err != nil {
			t.Fatal(err)
		}
		if !model.IsBlank(got.ToPlain()) {
			t.Fatalf("C%d not blank", n)
		}
	}
}

func TestLiveReadOmitsImageSizeWhileShootingRawOnly(t *testing.T) {
	cam, _ := setup(t)
	live := mustRead(t, cam, nil)
	if live["image_quality"] != "raw" {
		t.Fatal(live["image_quality"])
	}
	if _, ok := live["image_size"]; ok {
		t.Fatal("image_size present")
	}
}

func TestLiveImageSizeIsItsOwnPropertyNotTheUSBModeIndicator(t *testing.T) {
	cam, fake := setup(t)
	if model.ByKey["image_size"].Live != 0xD1A5 {
		t.Fatal("image_size live code")
	}
	mustWrite(t, cam, map[string]any{"image_quality": "fine", "image_size": "m_3:2"}, nil, true)
	if fake.Live["image_size"] != 4 || mustRead(t, cam, nil)["image_size"] != "m_3:2" {
		t.Fatal(fake.Live["image_size"])
	}
	if fake.USBMode != 6 {
		t.Fatal(fake.USBMode)
	}
	for _, c := range fake.WriteCodes() {
		if c == 0xD16E {
			t.Fatal("wrote the USB mode")
		}
	}
	for n, s := range fake.Slots { // registers untouched
		if s["image_size"] != 7 {
			t.Fatalf("C%d image_size %d", n, s["image_size"])
		}
	}
}

func TestRegisterSnapshotDoesNotIncludeTheLiveImageSize(t *testing.T) {
	cam, _ := setup(t)
	codes := cam.codes(slot(3))
	if len(codes) != 23 {
		t.Fatal(len(codes))
	}
	for _, c := range codes {
		if c == 0xD1A5 {
			t.Fatal("0xD1A5 in the register snapshot")
		}
	}
}

func TestACameraReportingAnotherUSBModeIsNotTreatedAsReady(t *testing.T) {
	fake := fakecam.New()
	fake.USBMode = 5
	cam := open(t, fake)
	if cam.Info.Mode != "unknown" || cam.Info.Ready() {
		t.Fatalf("%+v", cam.Info)
	}
}

func TestInvalidRecipeWritesNothing(t *testing.T) {
	cam, fake := setup(t)
	before := len(fake.Writes)
	_, err := cam.Write(map[string]any{"film_simulation": "kodachrome"}, slot(1), false)
	wantKind(t, err, errs.InvalidRecipe)
	if len(fake.Writes) != before {
		t.Fatal("something was written")
	}
}

func TestASettingTheCameraRefusesIsReportedByName(t *testing.T) {
	cam, fake := setup(t)
	fake.FieldHook = func(state map[string]int, f model.Field, target fakecam.Target, wire int, next func() uint16) uint16 {
		if f.Key == "clarity" {
			return 0x201C
		}
		return next()
	}
	_, err := cam.Write(map[string]any{"clarity": 3.0}, slot(1), false)
	e := wantKind(t, err, errs.InvalidRecipe)
	if !strings.Contains(e.Message, "clarity") {
		t.Fatal(e.Message)
	}
	if e.Message != "the camera refused clarity = 3" ||
		e.Hint != "write property 0xD1A2: camera answered 0x201C InvalidDevicePropValue. Another setting in this recipe may be locking it." {
		t.Fatalf("%q / %q", e.Message, e.Hint)
	}
}

func TestASettingTheCameraSilentlyChangesIsCaught(t *testing.T) {
	cam, fake := setup(t)
	fake.FieldHook = func(state map[string]int, f model.Field, target fakecam.Target, wire int, next func() uint16) uint16 {
		code := next()
		if f.Key == "sharpness" {
			state["sharpness"] = 0 // accepts the write, keeps the old value
		}
		return code
	}
	_, err := cam.Write(map[string]any{"sharpness": 2.0}, slot(1), false)
	e := wantKind(t, err, errs.InvalidRecipe)
	if !strings.Contains(e.Message, "did not keep") {
		t.Fatal(e.Message)
	}
}

func TestBackupContainsLiveAndAllRegisters(t *testing.T) {
	cam, _ := setup(t)
	mustWrite(t, cam, recipe(t, portraJSON), slot(3), false)
	data, err := cam.Backup()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(data.Keys(), []string{"camera", "firmware", "live", "slots"}) {
		t.Fatal(data.Keys())
	}
	p := plain(t, data)
	slots := p["slots"].(map[string]any)
	if len(slots) != 7 {
		t.Fatal(len(slots))
	}
	for _, n := range []string{"1", "2", "3", "4", "5", "6", "7"} {
		if _, ok := slots[n]; !ok {
			t.Fatal("missing slot", n)
		}
	}
	if slots["3"].(map[string]any)["name"] != "PORTRA" || p["live"].(map[string]any)["image_quality"] != "raw" {
		t.Fatal(p)
	}
	all := []any{p["live"]}
	for _, s := range slots {
		all = append(all, s)
	}
	for _, r := range all { // a backup is always restorable
		if _, err := model.EncodeRecipe(r.(map[string]any), false); err != nil {
			t.Fatalf("backup not restorable: %v", err)
		}
	}
}

func TestClearWBMemoryZeroesEveryMode(t *testing.T) {
	cam, fake := setup(t)
	mustWrite(t, cam, recipe(t, `{"white_balance": {"mode": "daylight", "shift_r": 3, "shift_b": -4}}`), slot(2), false)
	mustWrite(t, cam, recipe(t, `{"white_balance": {"mode": "shade", "shift_r": 5, "shift_b": 1}}`), slot(2), false)
	if err := cam.ClearWBMemory(2); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, cam, recipe(t, `{"white_balance": {"mode": "daylight"}}`), slot(2), true)
	got := mustRead(t, cam, slot(2))["white_balance"].(map[string]any)
	if got["shift_r"] != 0.0 || got["shift_b"] != 0.0 {
		t.Fatal(got)
	}
	if fake.Slots[2]["d_range_priority"] != 0 {
		t.Fatal("d_range_priority")
	}
}

// The owner shoots RAW at medium size; naming a never-named register seeds it from the live settings,
// so the register briefly holds RAW + medium. Restoring image size first is refused while quality is
// RAW: the second pass must put it back once quality is restored.
func TestSeedingABlankRegisterFromRawLiveSettingsIsUndone(t *testing.T) {
	cam, fake := setup(t)
	fake.Live["image_quality"] = model.ImageQuality.Codes["raw"]
	fake.Live["image_size"] = model.ImageSize.Codes["m_3:2"]
	mustWrite(t, cam, recipe(t, `{"name": "STREET", "film_simulation": "acros"}`), slot(5), false)
	if !fake.NamedBefore[5] {
		t.Fatal("the register was not named, so the quirk was not exercised")
	}
	if fake.SlotValue(5, "image_quality") != "fine" || fake.SlotValue(5, "image_size") != "l_3:2" {
		t.Fatal(fake.SlotValue(5, "image_quality"), fake.SlotValue(5, "image_size"))
	}
	if fake.SlotValue(5, "dynamic_range") != "100" || fake.SlotValue(5, "film_simulation") != "acros" {
		t.Fatal(fake.SlotValue(5, "dynamic_range"), fake.SlotValue(5, "film_simulation"))
	}
}

func TestASideEffectTheCameraWillNotUndoIsReportedByName(t *testing.T) {
	cam, fake := setup(t)
	fake.Live["image_quality"] = model.ImageQuality.Codes["raw"]
	fake.Live["image_size"] = model.ImageSize.Codes["m_3:2"]
	fake.FieldHook = func(state map[string]int, f model.Field, target fakecam.Target, wire int, next func() uint16) uint16 {
		if f.Key == "image_size" && !target.Live {
			return 0x201C // never accepts a size for the register
		}
		return next()
	}
	_, err := cam.Write(recipe(t, `{"name": "STREET", "film_simulation": "acros"}`), slot(5), false)
	e := wantKind(t, err, errs.InvalidRecipe)
	want := "the camera did not put back every setting the recipe left alone: " +
		"image_size (0xD18E) was 0x0007, camera holds 0x0004"
	if e.Message != want || !strings.Contains(e.Hint, "x100vi get C5") {
		t.Fatalf("%q / %q", e.Message, e.Hint)
	}
}
