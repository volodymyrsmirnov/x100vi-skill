package hardware

// The hardware tests' helpers, run against the software camera so they are checked without a body.

import (
	"fmt"
	"maps"
	"strings"
	"testing"

	"github.com/volodymyrsmirnov/x100vi-skill/internal/camera"
	"github.com/volodymyrsmirnov/x100vi-skill/internal/fakecam"
	"github.com/volodymyrsmirnov/x100vi-skill/internal/model"
)

// fakeRegister is a register with a recipe in daylight mode and different shifts remembered for other
// modes, including a non-default Kelvin.
func fakeRegister(t *testing.T, slot int) (*camera.Camera, *fakecam.FakeX100VI) {
	t.Helper()
	fake := fakecam.New()
	cam, err := camera.Open(fake)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cam.Close() })
	for _, wb := range []map[string]any{
		{"mode": "shade", "shift_r": 5.0, "shift_b": 1.0},
		{"mode": "kelvin", "kelvin": 6500.0, "shift_r": -2.0, "shift_b": 2.0},
		{"mode": "incandescent", "shift_r": -9.0, "shift_b": 9.0},
		{"mode": "daylight", "shift_r": 3.0, "shift_b": -4.0},
	} {
		recipe := map[string]any{"name": "MINE", "film_simulation": "classic_chrome", "d_range_priority": "weak",
			"white_balance": wb}
		if _, err := cam.Write(recipe, &slot, false); err != nil {
			t.Fatal(err)
		}
	}
	return cam, fake
}

func TestWBMemoryHelpersReadAndRestoreEveryMode(t *testing.T) {
	slot := 6
	cam, fake := fakeRegister(t, slot)
	mem, ok := wbMemory(t, cam, slot)
	if !ok {
		t.Fatal("wbMemory failed")
	}
	if mem["shade"] != (wbRemembered{5, 1, 0}) || mem["kelvin"] != (wbRemembered{0xFFFE, 2, 6500}) ||
		mem["daylight"] != (wbRemembered{3, 0xFFFC, 0}) || mem["auto"] != (wbRemembered{}) {
		t.Fatalf("%+v", mem)
	}
	if err := cam.ClearWBMemory(slot); err != nil {
		t.Fatal(err)
	}
	if !restoreWBMemory(t, cam, slot, mem) {
		t.Fatal("restoreWBMemory failed")
	}
	again, _ := wbMemory(t, cam, slot)
	for _, mode := range model.WhiteBalance.Names {
		if again[mode] != mem[mode] {
			t.Errorf("%s: %+v -> %+v", mode, mem[mode], again[mode])
		}
	}
	if fake.Slots[slot]["white_balance.kelvin"] != 6500 {
		t.Error(fake.Slots[slot]["white_balance.kelvin"])
	}
}

func TestRoundTripRestoresTheRegisterAndItsWBMemoryOnTheFakeCamera(t *testing.T) {
	slot := 6
	cam, fake := fakeRegister(t, slot)
	before := fake.Slots[slot]["white_balance.mode"]
	roundTripAndRestore(t, cam, slot)
	if fake.Slots[slot]["white_balance.mode"] != before || fake.Names[slot] != "MINE" {
		t.Fatal(fake.Slots[slot], fake.Names[slot])
	}
}

// recorder stands in for *testing.T to watch a helper fail without failing this test.
type recorder struct{ errors []string }

func (r *recorder) Helper() {}
func (r *recorder) Errorf(format string, args ...any) {
	r.errors = append(r.errors, fmt.Sprintf(format, args...))
}

// remembered is what a fake register keeps for every white-balance mode: the active mode's shift is in
// the register itself, the others' in the body's memory (absent means zero).
func remembered(fake *fakecam.FakeX100VI, slot int) map[int][2]int {
	st := fake.Slots[slot]
	out := map[int][2]int{}
	for k, v := range fake.WBMemory {
		if !k.Live && k.Slot == slot && v != [2]int{} {
			out[k.Mode] = v
		}
	}
	delete(out, st["white_balance.mode"])
	if v := [2]int{st["white_balance.shift_r"], st["white_balance.shift_b"]}; v != [2]int{} {
		out[st["white_balance.mode"]] = v
	}
	return out
}

// A read failing partway through the WB snapshot must not run the destructive cleanup: no remembered
// shift or Kelvin changes, the walk's own changes (active mode, D-range priority) are put back, and the
// test fails.
func TestAFailedWBSnapshotChangesNothingRemembered(t *testing.T) {
	slot := 6
	cam, fake := fakeRegister(t, slot)
	settingsBefore := maps.Clone(fake.Slots[slot])
	memoryBefore := remembered(fake, slot)
	writesBefore := len(fake.Writes)
	fake.Intercept = func(op uint16, params []uint32, data []byte, next func() (uint16, []byte)) (uint16, []byte) {
		// The Kelvin read of the snapshot walk (D-range priority off, kelvin mode selected); the reads
		// before the walk see the register's own D-range priority.
		if op == 0x1015 && len(params) == 1 && uint16(params[0]) == propKelvin &&
			fake.Slots[fake.Selected]["d_range_priority"] == 0 {
			return fakecam.GeneralError, nil
		}
		return next()
	}

	rec := &recorder{}
	roundTripAndRestore(rec, cam, slot)

	if len(rec.errors) == 0 {
		t.Fatal("a failed snapshot did not fail the test")
	}
	for _, w := range fake.Writes[writesBefore:] {
		switch w.Code {
		case model.SlotSelect, propWBMode, propDRP:
		default:
			t.Errorf("wrote 0x%04X % x after a failed snapshot", w.Code, w.Data)
		}
	}
	if got := remembered(fake, slot); !maps.Equal(got, memoryBefore) {
		t.Errorf("remembered shifts changed: %v -> %v", memoryBefore, got)
	}
	if !maps.Equal(fake.Slots[slot], settingsBefore) {
		t.Errorf("register changed (Kelvin, active mode or D-range priority):\n%v\n%v", settingsBefore, fake.Slots[slot])
	}
	if fake.Slots[slot]["white_balance.kelvin"] != 6500 {
		t.Errorf("Kelvin %d", fake.Slots[slot]["white_balance.kelvin"])
	}
}

// A body that acknowledges a WB-mode selection without making it would have the walk read another
// mode's shift: every selection is read back, and a mismatch fails the walk (which puts back what it
// changed: nothing further to report here).
func TestTheWBWalkReadsEverySelectionBack(t *testing.T) {
	slot := 6
	cam, fake := fakeRegister(t, slot)
	before := maps.Clone(fake.Slots[slot])
	shade := model.WhiteBalance.Codes["shade"]
	fake.FieldHook = func(state map[string]int, f model.Field, target fakecam.Target, wire int, next func() uint16) uint16 {
		if f.Key == "white_balance.mode" && !target.Live && wire == shade {
			return fakecam.OK // acknowledged, not applied
		}
		return next()
	}
	rec := &recorder{}
	if _, ok := wbMemory(rec, cam, slot); ok {
		t.Fatal("the walk accepted a selection the register did not make")
	}
	if len(rec.errors) != 1 || !strings.Contains(rec.errors[0], "selected WB shade (0x8006), but the register reads back 0x") {
		t.Fatalf("%q", rec.errors)
	}
	if !maps.Equal(fake.Slots[slot], before) {
		t.Fatalf("register not put back:\n%v\n%v", before, fake.Slots[slot])
	}
}

// When the walk's own changes cannot be put back, the failure names exactly what the register holds
// instead of claiming everything was put back.
func TestAFailedRollbackSaysWhatWasNotPutBack(t *testing.T) {
	slot := 6
	cam, fake := fakeRegister(t, slot)
	modeWrites := 0
	fake.FieldHook = func(state map[string]int, f model.Field, target fakecam.Target, wire int, next func() uint16) uint16 {
		if target.Live {
			return next()
		}
		switch {
		case f.Key == "white_balance.mode":
			if modeWrites++; modeWrites > 1 { // the first selection (auto) works, then the mode is stuck
				return fakecam.OK
			}
		case f.Key == "d_range_priority" && wire != 0:
			return fakecam.OK // priority can be turned off but not back on
		}
		return next()
	}
	rec := &recorder{}
	roundTripAndRestore(rec, cam, slot)
	all := strings.Join(rec.errors, "\n")
	want := "after the failed WB snapshot walk, C6 could not be put back: " +
		"active WB mode (wanted 0x0004, register holds 0x0002); D-range priority (wanted 0x0001, register holds 0x0000)"
	if !strings.Contains(all, want) || !strings.Contains(all, "selected WB daylight (0x0004), but the register reads back 0x0002") {
		t.Fatalf("errors:\n%s", all)
	}
	if strings.Contains(all, "were put back") {
		t.Fatalf("claims the changes were put back:\n%s", all)
	}
	if fake.Slots[slot]["white_balance.mode"] != model.WhiteBalance.Codes["auto"] || fake.Slots[slot]["d_range_priority"] != 0 {
		t.Fatal("the fake did not behave as set up", fake.Slots[slot])
	}
}
