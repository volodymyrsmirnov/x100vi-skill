package hardware

// The hardware tests' helpers, run against the software camera so they are checked without a body.

import (
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
