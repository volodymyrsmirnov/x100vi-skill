package settingsfile_test

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/volodymyrsmirnov/x100vi-skill/internal/camera/settingsfile"
	"github.com/volodymyrsmirnov/x100vi-skill/internal/errs"
	"github.com/volodymyrsmirnov/x100vi-skill/internal/fakecam"
	"github.com/volodymyrsmirnov/x100vi-skill/internal/ptp"
)

func TestHeaderIsParsed(t *testing.T) {
	h, err := settingsfile.ParseHeader(fakecam.MakeSettingsFile("0179ABCDEF0123456789", "", 0, 0), "")
	if err != nil {
		t.Fatal(err)
	}
	if h != (settingsfile.Header{Version: "0100", Model: "X100VI", BodyID: "0179ABCDEF0123456789"}) {
		t.Fatalf("%+v", h)
	}
}

func TestAnythingElseIsNotASettingsFile(t *testing.T) {
	for _, blob := range [][]byte{nil, []byte("FUJIFILMX-BACKUP"), append([]byte(`{"slots": {}}`), make([]byte, 100)...),
		make([]byte, 4096)} {
		_, err := settingsfile.ParseHeader(blob, "")
		e, ok := errs.As(err)
		if !ok || e.Kind != errs.InvalidRecipe || !strings.Contains(e.Message, "not a camera settings file") {
			t.Fatalf("%q: %v", blob[:min(len(blob), 16)], err)
		}
	}
}

func TestBookkeepingBytesAreNotSettings(t *testing.T) {
	a := fakecam.DefaultSettingsFile()
	b := bytes.Clone(a)
	b[0x108] += 2
	b[0x716] = 3
	if d := settingsfile.SettingsDiffer(a, b); len(d) != 0 {
		t.Fatal(d)
	}
	b[0x800] = 1
	if d := settingsfile.SettingsDiffer(a, b); !reflect.DeepEqual(d, []int{0x800}) {
		t.Fatal(d)
	}
	if d := settingsfile.SettingsDiffer(a, append(bytes.Clone(a), 'x')); len(d) == 0 {
		t.Fatal("different lengths compared equal")
	}
}

func TestReadAndWriteAgainstTheFake(t *testing.T) {
	fake := fakecam.New()
	s := ptp.NewSession(fake)
	if err := s.Open(); err != nil {
		t.Fatal(err)
	}
	got, err := settingsfile.Read(s)
	if err != nil || !bytes.Equal(got, fake.SettingsFile) {
		t.Fatalf("read: %v", err)
	}
	next := fakecam.MakeSettingsFile("", "", 0, 5)
	if err := settingsfile.Write(s, next); err != nil {
		t.Fatal(err)
	}
	if d := settingsfile.SettingsDiffer(fake.SettingsFile, next); len(d) != 0 || fake.SettingsWrites != 1 {
		t.Fatal(d, fake.SettingsWrites)
	}
}

func TestReadInCardReaderModeExplainsTheMode(t *testing.T) {
	s := ptp.NewSession(fakecam.NewWith(fakecam.Options{Mode: "card_reader"}))
	_ = s.Open()
	_, err := settingsfile.Read(s)
	e, ok := errs.As(err)
	if !ok || e.Kind != errs.CameraRefused || !strings.Contains(e.Hint, "USB RAW CONV./BACKUP RESTORE") {
		t.Fatal(err)
	}
}
