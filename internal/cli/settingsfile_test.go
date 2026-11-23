package cli

// The camera's own settings file from the command line: `backup --full`, `restore FILE.dat`
// (the CLI half of tests/test_settingsfile.py).

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/volodymyrsmirnov/x100vi-skill/internal/camera/settingsfile"
	"github.com/volodymyrsmirnov/x100vi-skill/internal/fakecam"
)

func writeBytes(t *testing.T, path string, data []byte) string {
	t.Helper()
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestBackupFullSavesTheFile(t *testing.T) {
	c := newRunner(t)
	target := filepath.Join(t.TempDir(), "cam.dat")
	r := c.run("backup", "--full", "-o", target, "--json")
	wantCode(t, r, 0)
	saved, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(saved, c.fake.SettingsFile) {
		t.Errorf("saved file differs from the camera's (%v)", err)
	}
	j := r.json(t)
	if j["kind"] != "camera_settings_file" || j["bytes"] != float64(len(c.fake.SettingsFile)) || j["model"] != "X100VI" {
		t.Errorf("backup --full = %v", j)
	}
	mustContain(t, j["note"].(string), "private")
	mustContain(t, c.run("backup", "--full", "-o", target).out, "every menu setting", "Keep it private")
}

func TestBackupFullDefaultsToTheStateFolder(t *testing.T) {
	c := newRunner(t)
	r := c.run("backup", "--full", "--json")
	wantCode(t, r, 0)
	saved, _ := r.json(t)["saved"].(string)
	if !strings.HasPrefix(saved, filepath.Join(os.Getenv("X100VI_HOME"), "backups")) || !strings.HasSuffix(saved, ".dat") {
		t.Errorf("saved = %q", saved)
	}
}

func TestBackupFullInTheWrongMode(t *testing.T) {
	c := newRunner(t)
	target := filepath.Join(t.TempDir(), "x.dat")
	r := c.on(fakecam.NewWith(fakecam.Options{Mode: "card_reader"}), "backup", "--full", "-o", target)
	wantCode(t, r, 11)
	if exists(target) {
		t.Error("a file was written")
	}
}

func TestRestoreFullNeedsYesAndSavesTheCurrentFileFirst(t *testing.T) {
	c := newRunner(t)
	original := c.fake.SettingsFile
	newFile := writeBytes(t, filepath.Join(t.TempDir(), "new.dat"), fakecam.MakeSettingsFile("", "", 0, 9))
	r := c.run("restore", newFile)
	wantCode(t, r, 2)
	mustContain(t, r.err, "every setting")
	if c.fake.SettingsWrites != 0 {
		t.Fatal("written without --yes")
	}
	r = c.run("restore", newFile, "--yes", "--json")
	wantCode(t, r, 0)
	j := r.json(t)
	if j["restored"] != true || j["verified"] != true {
		t.Errorf("restore = %v", j)
	}
	want, _ := os.ReadFile(newFile)
	if d := settingsfile.SettingsDiffer(c.fake.SettingsFile, want); len(d) != 0 {
		t.Errorf("camera differs from the file at %v", d)
	}
	saved, _ := j["previous_saved_to"].(string)
	if !strings.HasPrefix(saved, os.Getenv("X100VI_HOME")) {
		t.Errorf("previous_saved_to = %q", saved)
	}
	if prev, err := os.ReadFile(saved); err != nil || !bytes.Equal(prev, original) {
		t.Errorf("the saved previous file is not the camera's old one (%v)", err)
	}
}

func TestRestoreFullSkipsAnIdenticalFile(t *testing.T) {
	c := newRunner(t)
	same := writeBytes(t, filepath.Join(t.TempDir(), "same.dat"), c.fake.SettingsFile)
	r := c.run("restore", same, "--yes", "--json")
	wantCode(t, r, 0)
	if j := r.json(t); j["unchanged"] != true || j["restored"] != false {
		t.Errorf("restore = %v", j)
	}
	if c.fake.SettingsWrites != 0 {
		t.Error("an identical file was written")
	}
}

func TestRestoreFullRejectsLiveAndAnotherBody(t *testing.T) {
	c := newRunner(t)
	other := writeBytes(t, filepath.Join(t.TempDir(), "other.dat"), fakecam.MakeSettingsFile("0179OTHERBODY0000001", "", 0, 0))
	wantCode(t, c.run("restore", other, "--yes", "--live"), 2)
	r := c.run("restore", other, "--yes")
	wantCode(t, r, 2)
	mustContain(t, r.err, "--other-body")
	if c.fake.SettingsWrites != 0 {
		t.Fatal("a file from another body was written")
	}
	wantCode(t, c.run("restore", other, "--yes", "--other-body"), 0)
	if c.fake.SettingsWrites != 1 {
		t.Errorf("settings writes = %d", c.fake.SettingsWrites)
	}
}

func TestRestoreFullFailsLoudlyWhenTheCameraKeepsOtherSettings(t *testing.T) {
	c := newRunner(t)
	newFile := writeBytes(t, filepath.Join(t.TempDir(), "new.dat"), fakecam.MakeSettingsFile("", "", 0, 9))
	c.fake.Intercept = func(op uint16, params []uint32, data []byte, next func() (uint16, []byte)) (uint16, []byte) {
		code, out := next()
		if op == 0x100D { // SendObject: the body stores the file but keeps one setting of its own
			c.fake.SettingsFile[0x800] = 0x55
		}
		return code, out
	}
	r := c.run("restore", newFile, "--yes")
	wantCode(t, r, 1)
	mustContain(t, r.err, "reads back 1 different bytes", "before-restore")
}
