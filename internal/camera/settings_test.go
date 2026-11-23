package camera

import (
	"bytes"
	"strings"
	"testing"

	"github.com/volodymyrsmirnov/x100vi-skill/internal/camera/settingsfile"
	"github.com/volodymyrsmirnov/x100vi-skill/internal/errs"
	"github.com/volodymyrsmirnov/x100vi-skill/internal/fakecam"
)

func TestCameraReadsItsSettingsFile(t *testing.T) {
	cam, fake := setup(t)
	got, err := cam.ReadSettingsFile()
	if err != nil || !bytes.Equal(got, fake.SettingsFile) {
		t.Fatalf("read: %v", err)
	}
}

func TestRestoreSendsTheFileTheSDKWay(t *testing.T) {
	cam, fake := setup(t)
	next := fakecam.MakeSettingsFile("", "", 0, 7)
	if err := cam.RestoreSettingsFile(next, false, nil); err != nil {
		t.Fatal(err)
	}
	if d := settingsfile.SettingsDiffer(fake.SettingsFile, next); len(d) != 0 || fake.SettingsWrites != 1 {
		t.Fatal(d, fake.SettingsWrites)
	}
}

func TestRestoreRefusesAnotherModelSizeOrBody(t *testing.T) {
	cam, fake := setup(t)
	original := bytes.Clone(fake.SettingsFile)
	refuse := func(blob []byte, want string) {
		t.Helper()
		err := cam.RestoreSettingsFile(blob, false, nil)
		e := wantKind(t, err, errs.InvalidRecipe)
		if !strings.Contains(e.Message, want) {
			t.Fatalf("%q does not mention %q", e.Message, want)
		}
	}
	refuse(fakecam.MakeSettingsFile("", "X-T5", 0, 0), "X-T5")
	refuse(fakecam.MakeSettingsFile("", "", 8192, 0), "different size")
	other := fakecam.MakeSettingsFile("0179OTHERBODY0000001", "", 0, 3)
	refuse(other, "different X100VI body")
	if !bytes.Equal(fake.SettingsFile, original) || fake.SettingsWrites != 0 {
		t.Fatal("the camera's file changed")
	}
	if err := cam.RestoreSettingsFile(other, true, nil); err != nil {
		t.Fatal(err)
	}
	if d := settingsfile.SettingsDiffer(fake.SettingsFile, other); len(d) != 0 {
		t.Fatal(d)
	}
}

func TestRestoreUsesTheCurrentFileWhenGiven(t *testing.T) {
	cam, fake := setup(t)
	current := bytes.Clone(fake.SettingsFile)
	reads := 0
	fake.Intercept = func(op uint16, params []uint32, data []byte, next func() (uint16, []byte)) (uint16, []byte) {
		if op == 0x1009 {
			reads++
		}
		return next()
	}
	if err := cam.RestoreSettingsFile(fakecam.MakeSettingsFile("", "", 0, 2), false, current); err != nil {
		t.Fatal(err)
	}
	if reads != 0 || fake.SettingsWrites != 1 {
		t.Fatal(reads, fake.SettingsWrites)
	}
}

func TestSettingsFileNeedsRawConvMode(t *testing.T) {
	cam := open(t, fakecam.NewWith(fakecam.Options{Mode: "card_reader"}))
	_, err := cam.ReadSettingsFile()
	wantKind(t, err, errs.WrongMode)
}
