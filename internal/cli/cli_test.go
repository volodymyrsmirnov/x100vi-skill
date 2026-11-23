package cli

// The command line end to end, against the fake camera (tests/test_cli.py).

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/volodymyrsmirnov/x100vi-skill/internal/errs"
	"github.com/volodymyrsmirnov/x100vi-skill/internal/fakecam"
	"github.com/volodymyrsmirnov/x100vi-skill/internal/library"
	"github.com/volodymyrsmirnov/x100vi-skill/internal/model"
)

// -- doctor ------------------------------------------------------------------------------------------

func TestDoctorReady(t *testing.T) {
	c := newRunner(t)
	r := c.run("doctor")
	wantCode(t, r, 0)
	mustContain(t, r.out, "ready", "about 80%")
	if j := c.run("doctor", "--json").json(t); j["ready"] != true {
		t.Errorf("ready = %v", j["ready"])
	}
}

func TestDoctorWrongModeSaysHowToFixIt(t *testing.T) {
	c := newRunner(t)
	r := c.on(fakecam.NewWith(fakecam.Options{Mode: "card_reader"}), "doctor")
	wantCode(t, r, 11)
	mustContain(t, r.out, "USB CARD READER", "CONNECTION MODE")
	r = c.on(fakecam.NewWith(fakecam.Options{Mode: "card_reader"}), "doctor", "--json")
	wantCode(t, r, 11)
	j := r.json(t)
	if j["ready"] != false || j["error"] != "wrong_usb_mode" || j["hint"] == nil {
		t.Errorf("doctor --json = %v", j)
	}
}

func TestDoctorWrongModel(t *testing.T) {
	c := newRunner(t)
	r := c.on(fakecam.NewWith(fakecam.Options{Model: "X-T5"}), "doctor")
	wantCode(t, r, 12)
	mustContain(t, r.out, "X-T5")
}

func TestDoctorNoCamera(t *testing.T) {
	isolate(t)
	nothing := failing(errs.New(errs.CameraNotFound, "no Fujifilm camera found on USB", "Connect the camera."))
	r := runWith(t, nothing, "doctor")
	wantCode(t, r, 10)
	mustContain(t, r.out, "not ready: no Fujifilm camera found", "Connect the camera.")
}

// -- get / set ---------------------------------------------------------------------------------------

func TestGetPrintsJSONAndSavesFiles(t *testing.T) {
	c := newRunner(t)
	if v := c.run("get", "c2").json(t)["film_simulation"]; v != "provia" {
		t.Errorf("C2 film_simulation = %v", v)
	}
	if v := c.run("get").json(t)["image_quality"]; v != "raw" {
		t.Errorf("live image_quality = %v", v)
	}
	target := filepath.Join(t.TempDir(), "out", "c2.json")
	wantCode(t, c.run("get", "C2", "-o", target), 0)
	var saved map[string]any
	raw, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &saved); err != nil || saved["name"] != "" {
		t.Errorf("saved C2 = %v (%v)", saved, err)
	}
	slots, _ := c.run("get", "all").json(t)["slots"].(map[string]any)
	var keys []string
	for k := range slots {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if strings.Join(keys, "") != "1234567" {
		t.Errorf("get all slots = %v", keys)
	}
}

func TestSetWritesReadsBackAndAdvises(t *testing.T) {
	c := newRunner(t)
	r := c.run("set", "c3", writeRecipe(t, t.TempDir(), gold, ""), "--json")
	wantCode(t, r, 0)
	j := r.json(t)
	if j["target"] != "C3" || get(j, "stored", "film_simulation") != "classic_chrome" || get(j, "stored", "name") != "GOLD" {
		t.Errorf("set result = %v", j)
	}
	advice := strings.Join(strs(j["advice"]), "\n")
	mustContain(t, advice, "ISO 500", "Clarity")
	mustContain(t, j["set_on_camera"].(string), "ISO")
	if v := c.fake.SlotValue(3, "film_simulation"); v != "classic_chrome" {
		t.Errorf("camera C3 film_simulation = %v", v)
	}
}

func TestSetRefusesToOverwriteAnOccupiedRegister(t *testing.T) {
	c := newRunner(t)
	dir := t.TempDir()
	wantCode(t, c.run("set", "c3", writeRecipe(t, dir, gold, "")), 0)
	r := c.run("set", "c3", writeRecipe(t, dir, map[string]any{"name": "OTHER", "film_simulation": "velvia"}, "other.json"))
	wantCode(t, r, 4)
	mustContain(t, r.err, "already holds 'GOLD'", "--overwrite")
	if c.fake.Names[3] != "GOLD" {
		t.Errorf("C3 name = %q", c.fake.Names[3])
	}
}

func TestOverwriteSavesThePreviousRecipeFirst(t *testing.T) {
	c := newRunner(t)
	state := os.Getenv("X100VI_HOME")
	dir := t.TempDir()
	c.run("set", "c3", writeRecipe(t, dir, gold, ""))
	r := c.run("set", "c3", writeRecipe(t, dir, map[string]any{"name": "OTHER", "film_simulation": "velvia"}, "other.json"),
		"--overwrite", "--json")
	wantCode(t, r, 0)
	if c.fake.Names[3] != "OTHER" {
		t.Errorf("C3 name = %q", c.fake.Names[3])
	}
	previous, _ := r.json(t)["previous_saved_to"].(string)
	raw, err := os.ReadFile(previous)
	if err != nil {
		t.Fatal(err)
	}
	var saved map[string]any
	if err := json.Unmarshal(raw, &saved); err != nil || saved["name"] != "GOLD" {
		t.Errorf("saved = %v (%v)", saved, err)
	}
	mustContain(t, previous, state)
}

func TestSetLiveBacksUpThePreviousLiveSettings(t *testing.T) {
	c := newRunner(t)
	j := c.run("set", "live", writeRecipe(t, t.TempDir(), map[string]any{"film_simulation": "acros"}, ""), "--json").json(t)
	if j["target"] != "live" {
		t.Errorf("target = %v", j["target"])
	}
	if p, _ := j["previous_saved_to"].(string); p == "" || !exists(p) {
		t.Errorf("previous_saved_to = %v", j["previous_saved_to"])
	}
}

func TestSetAcceptsAStarterRecipeByName(t *testing.T) {
	c := newRunner(t)
	wantCode(t, c.run("set", "c1", "desert-gold"), 0)
	if c.fake.Names[1] != "DESERT GOLD" {
		t.Errorf("C1 name = %q", c.fake.Names[1])
	}
}

func TestDryRunNeverConnects(t *testing.T) {
	isolate(t)
	r := runWith(t, boom(t), "set", "c1", writeRecipe(t, t.TempDir(), gold, ""), "--dry-run")
	wantCode(t, r, 0)
	mustContain(t, r.out, "would write")
}

func TestInvalidRecipeFailsBeforeConnecting(t *testing.T) {
	for _, tc := range []struct {
		recipe   map[string]any
		fragment string
	}{
		{map[string]any{"film_simulation": "kodachrome"}, "film_simulation"},
		{map[string]any{"iso": 800}, "unknown setting"},
	} {
		t.Run(tc.fragment, func(t *testing.T) {
			isolate(t)
			r := runWith(t, boom(t), "set", "c1", writeRecipe(t, t.TempDir(), tc.recipe, ""))
			wantCode(t, r, 2)
			mustContain(t, r.err, tc.fragment, "hint:")
		})
	}
}

func TestBrokenJSONAndMissingFilesAreExplained(t *testing.T) {
	c := newRunner(t)
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustContain(t, c.run("set", "c1", bad).err, "not valid JSON")
	mustContain(t, c.run("set", "c1", filepath.Join(dir, "nope.json")).err, "does not exist")
}

func TestWrongModeIsAnErrorWithAHint(t *testing.T) {
	c := newRunner(t)
	r := c.on(fakecam.NewWith(fakecam.Options{Mode: "card_reader"}), "set", "c1", writeRecipe(t, t.TempDir(), gold, ""))
	wantCode(t, r, 11)
	mustContain(t, r.err, "hint:", "USB RAW CONV./BACKUP RESTORE")
}

func TestJSONErrorsAreMachineReadable(t *testing.T) {
	c := newRunner(t)
	r := c.run("set", "c9", writeRecipe(t, t.TempDir(), gold, ""), "--json")
	wantCode(t, r, 2)
	if e := r.json(t)["error"]; e != "invalid_recipe" {
		t.Errorf("error = %v", e)
	}
}

// -- backup / restore --------------------------------------------------------------------------------

func TestBackupThenRestoreRoundTrips(t *testing.T) {
	c := newRunner(t)
	dir := t.TempDir()
	c.run("set", "c3", writeRecipe(t, dir, gold, ""))
	backup := filepath.Join(dir, "all.json")
	wantCode(t, c.run("backup", "-o", backup), 0)
	c.run("set", "c3", writeRecipe(t, dir, map[string]any{"name": "OTHER", "film_simulation": "velvia"}, "other.json"), "--overwrite")
	c.run("set", "c5", "street-mono")
	mustContain(t, c.run("restore", backup).err, "Add --yes")
	r := c.run("restore", backup, "--yes", "--json")
	wantCode(t, r, 0)
	var want []string
	for _, n := range model.Slots {
		want = append(want, "C"+string(rune('0'+n)))
	}
	if got := strs(r.json(t)["restored"]); !reflect.DeepEqual(got, want) {
		t.Errorf("restored = %v, want %v", got, want)
	}
	if c.fake.Names[3] != "GOLD" || c.fake.Names[5] != "" {
		t.Errorf("names = %v", c.fake.Names)
	}
	if v := c.fake.SlotValue(5, "film_simulation"); v != "provia" {
		t.Errorf("C5 film_simulation = %v", v)
	}
	if v := c.fake.Live["film_simulation"]; v != 1 { // live untouched without --live
		t.Errorf("live film_simulation = %v", v)
	}
}

func TestRestoreRejectsASingleRecipeAndABrokenBackup(t *testing.T) {
	c := newRunner(t)
	dir := t.TempDir()
	mustContain(t, c.run("restore", writeRecipe(t, dir, gold, ""), "--yes").err, "not a backup")
	broken := writeRecipe(t, dir, map[string]any{"slots": map[string]any{"1": map[string]any{"film_simulation": "nope"}}}, "broken.json")
	r := c.run("restore", broken, "--yes")
	wantCode(t, r, 2)
	mustContain(t, r.err, "film_simulation")
	if len(c.fake.Writes) != 0 {
		t.Errorf("the camera was written: %v", c.fake.WriteCodes())
	}
}

func TestBackupWithoutAPathGoesToTheStateFolder(t *testing.T) {
	c := newRunner(t)
	j := c.run("backup", "--json").json(t)
	mustContain(t, j["saved"].(string), os.Getenv("X100VI_HOME"))
	if v := get(j, "slots", "C1"); v != "(empty)" {
		t.Errorf("C1 = %v", v)
	}
}

// -- validate / recipes / schema ---------------------------------------------------------------------

func TestValidateReportsEachFile(t *testing.T) {
	isolate(t)
	dir := t.TempDir()
	good := writeRecipe(t, dir, gold, "good.json")
	bad := writeRecipe(t, dir, map[string]any{"highlight": 9}, "bad.json")
	r := runWith(t, boom(t), "validate", good, bad)
	wantCode(t, r, 2)
	mustContain(t, r.out, "good.json: ok", "bad.json: INVALID")
	wantCode(t, runWith(t, boom(t), "validate", good), 0)
}

func TestStarterRecipesAreListedAndPrintable(t *testing.T) {
	isolate(t)
	var names []string
	for _, item := range runWith(t, boom(t), "recipes", "--json").json(t)["recipes"].([]any) {
		names = append(names, item.(map[string]any)["name"].(string))
	}
	if !slices.Contains(names, "desert-gold") || len(names) < 8 {
		t.Errorf("starter names = %v", names)
	}
	if v := runWith(t, boom(t), "recipes", "desert-gold").json(t)["film_simulation"]; v != "classic_chrome" {
		t.Errorf("desert-gold film_simulation = %v", v)
	}
	wantCode(t, runWith(t, boom(t), "recipes", "nope"), 2)
}

func TestSchemaIsPrinted(t *testing.T) {
	isolate(t)
	title, _ := runWith(t, boom(t), "schema").json(t)["title"].(string)
	if !strings.HasPrefix(title, "Fujifilm X100VI") {
		t.Errorf("schema title = %q", title)
	}
}

// -- preview -----------------------------------------------------------------------------------------

func TestPreviewRendersEveryRecipeAndReportsWhatWasNotApplied(t *testing.T) {
	c := newRunner(t)
	dir := t.TempDir()
	raf := makeRAF(t, filepath.Join(dir, "DSCF0001.RAF"), "")
	out := filepath.Join(dir, "previews")
	r := c.run("preview", raf, "--as-shot", "-r", writeRecipe(t, dir, gold, "gold.json"), "street-mono", "-o", out, "--json")
	wantCode(t, r, 0)
	j := r.json(t)
	renders := j["renders"].([]any)
	var labels []string
	for _, x := range renders {
		labels = append(labels, x.(map[string]any)["recipe"].(string))
	}
	if !reflect.DeepEqual(labels, []string{"as-shot", "gold", "street-mono"}) {
		t.Errorf("renders = %v", labels)
	}
	goldNA := renders[1].(map[string]any)["not_applied"].([]any)
	if len(goldNA) == 0 || get(goldNA[0], "setting") != "dynamic_range" || get(goldNA[0], "applied") != "200" {
		t.Errorf("gold not_applied = %v", goldNA)
	}
	if na := renders[2].(map[string]any)["not_applied"].([]any); len(na) != 0 {
		t.Errorf("street-mono not_applied = %v", na)
	}
	if c, _ := cells(t, filepath.Join(out, "DSCF0001__gold.jpg")); c[8] != 11 {
		t.Errorf("gold cell 8 = %d", c[8])
	}
	if c, _ := cells(t, filepath.Join(out, "DSCF0001__street-mono.jpg")); c[8] != 14 {
		t.Errorf("street-mono cell 8 = %d", c[8])
	}
	if c, _ := cells(t, filepath.Join(out, "DSCF0001__as-shot.jpg")); c[7] != 1 {
		t.Errorf("as-shot cell 7 = %d", c[7])
	}
	if v := get(j["frames"].([]any)[0], "as_shot", "film_simulation"); v != "classic_chrome" {
		t.Errorf("as-shot film_simulation = %v", v)
	}
	sheet, _ := j["contact_sheet"].(string)
	if !exists(filepath.Join(out, "contact-sheet.jpg")) || !strings.HasSuffix(sheet, "contact-sheet.jpg") {
		t.Errorf("contact sheet = %v", j["contact_sheet"])
	}
}

func TestPreviewTextOutputExplainsTheCapInWords(t *testing.T) {
	c := newRunner(t)
	dir := t.TempDir()
	raf := makeRAF(t, filepath.Join(dir, "DSCF0001.RAF"), "")
	r := c.run("preview", raf, "-r", writeRecipe(t, dir, gold, ""), "-o", filepath.Join(dir, "p"), "--no-sheet")
	wantCode(t, r, 0)
	mustContain(t, r.out, "not applied as written", "shot at DR200")
	if exists(filepath.Join(dir, "p", "contact-sheet.jpg")) {
		t.Error("--no-sheet made a contact sheet")
	}
}

func TestPreviewUploadsEachRAFOnce(t *testing.T) {
	c := newRunner(t)
	dir := t.TempDir()
	a, b := makeRAF(t, filepath.Join(dir, "A.RAF"), ""), makeRAF(t, filepath.Join(dir, "B.RAF"), "")
	wantCode(t, c.run("preview", a, b, "-r", "everyday", "desert-gold", "-o", filepath.Join(dir, "p")), 0)
	rendered, _ := filepath.Glob(filepath.Join(dir, "p", "*__*.jpg"))
	if c.fake.Uploads != 2 || len(rendered) != 4 {
		t.Errorf("uploads = %d, renders = %v", c.fake.Uploads, rendered)
	}
}

func TestPreviewExposureAndFullResolution(t *testing.T) {
	c := newRunner(t)
	dir := t.TempDir()
	raf := makeRAF(t, filepath.Join(dir, "DSCF0001.RAF"), "")
	wantCode(t, c.run("preview", raf, "-r", "everyday", "--ev", "0.67", "--full", "-o", filepath.Join(dir, "p"), "--no-sheet"), 0)
	got, full := cells(t, filepath.Join(dir, "p", "DSCF0001__everyday.jpg"))
	if got[5] != 667 || !full {
		t.Errorf("cell 5 = %d, full = %v", got[5], full)
	}
}

func TestPreviewChecksFilesAndRecipesBeforeConnecting(t *testing.T) {
	isolate(t)
	dir := t.TempDir()
	t.Chdir(dir) // the default output folder is created before connecting
	foreign := makeRAF(t, filepath.Join(dir, "xt5.RAF"), "X-T5")
	r := runWith(t, boom(t), "preview", foreign, "-r", "everyday")
	wantCode(t, r, 20)
	mustContain(t, r.err, "X-T5")
	good := makeRAF(t, filepath.Join(dir, "ok.RAF"), "")
	r = runWith(t, boom(t), "preview", good)
	wantCode(t, r, 2)
	mustContain(t, r.err, "nothing to render")
	auto := writeRecipe(t, dir, map[string]any{"d_range_priority": "auto"}, "")
	r = runWith(t, boom(t), "preview", good, "-r", auto)
	wantCode(t, r, 2)
	mustContain(t, r.err, "cannot be previewed")
}

func TestPreviewInTheWrongMode(t *testing.T) {
	c := newRunner(t)
	dir := t.TempDir()
	raf := makeRAF(t, filepath.Join(dir, "DSCF0001.RAF"), "")
	r := c.on(fakecam.NewWith(fakecam.Options{Mode: "card_reader"}), "preview", raf, "-r", "everyday", "-o", filepath.Join(dir, "p"))
	wantCode(t, r, 11)
	mustContain(t, r.err, "CONNECTION MODE")
}

func TestPreviewVerifyNeedsExiftool(t *testing.T) {
	c := newRunner(t)
	dir := t.TempDir()
	raf := makeRAF(t, filepath.Join(dir, "DSCF0001.RAF"), "")
	t.Setenv("PATH", t.TempDir()) // no exiftool anywhere
	r := c.run("preview", raf, "-r", "everyday", "--verify", "-o", filepath.Join(dir, "p"))
	wantCode(t, r, 5)
	mustContain(t, r.err, "exiftool is not installed")
}

func TestPreviewFormats(t *testing.T) {
	c := newRunner(t)
	dir := t.TempDir()
	raf := makeRAF(t, filepath.Join(dir, "DSCF0001.RAF"), "")
	r := c.run("preview", raf, "-r", "everyday", "desert-gold", "--format", "heif", "-o", filepath.Join(dir, "h"), "--json")
	wantCode(t, r, 0)
	j := r.json(t)
	if j["format"] != "heif" || j["contact_sheet"] != nil {
		t.Errorf("format = %v, contact_sheet = %v", j["format"], j["contact_sheet"])
	}
	entries, err := os.ReadDir(filepath.Join(dir, "h"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if !reflect.DeepEqual(names, []string{"DSCF0001__desert-gold.heif", "DSCF0001__everyday.heif"}) {
		t.Errorf("files = %v", names)
	}
	wantCode(t, c.run("preview", raf, "-r", "everyday", "--format", "tiff16", "--full", "-o", filepath.Join(dir, "t")), 0)
	if got, _ := cells(t, filepath.Join(dir, "t", "DSCF0001__everyday.tif")); got[2] != 11 {
		t.Errorf("tiff16 cell 2 = %d", got[2])
	}
}

func TestPreviewTIFFWithoutFullFailsBeforeConnecting(t *testing.T) {
	isolate(t)
	raf := makeRAF(t, filepath.Join(t.TempDir(), "DSCF0001.RAF"), "")
	r := runWith(t, boom(t), "preview", raf, "-r", "everyday", "--format", "tiff")
	wantCode(t, r, 2)
	mustContain(t, r.err, "--full")
}

func TestPreviewWithADRangePriorityOverride(t *testing.T) {
	c := newRunner(t)
	dir := t.TempDir()
	raf := makeRAF(t, filepath.Join(dir, "DSCF0001.RAF"), "")
	auto := writeRecipe(t, dir, map[string]any{"name": "AUTO DRP", "d_range_priority": "auto"}, "auto.json")
	r := c.run("preview", raf, "-r", auto, "--d-range-priority", "plus", "-o", filepath.Join(dir, "p"), "--json")
	wantCode(t, r, 0)
	j := r.json(t)
	if j["d_range_priority"] != "plus" {
		t.Errorf("d_range_priority = %v", j["d_range_priority"])
	}
	if got, _ := cells(t, filepath.Join(dir, "p", "DSCF0001__auto.jpg")); got[7] != 3 {
		t.Errorf("cell 7 = %d", got[7])
	}
	na := j["renders"].([]any)[0].(map[string]any)["not_applied"].([]any)
	if len(na) == 0 || get(na[0], "applied") != "plus" {
		t.Errorf("not_applied = %v", na)
	}
}

func TestPreviewExposureOutsideTheConverterRangeFailsBeforeConnecting(t *testing.T) {
	isolate(t)
	t.Chdir(t.TempDir())
	raf := makeRAF(t, filepath.Join(t.TempDir(), "DSCF0001.RAF"), "")
	r := runWith(t, boom(t), "preview", raf, "-r", "everyday", "--ev", "-3")
	wantCode(t, r, 2)
	mustContain(t, r.err, "-2 to +3")
}

// -- install-skill -----------------------------------------------------------------------------------

func TestInstallSkillCopiesIntoASkillsFolder(t *testing.T) {
	isolate(t)
	skills := filepath.Join(t.TempDir(), "skills")
	r := runWith(t, boom(t), "install-skill", "--dir", skills, "--json")
	wantCode(t, r, 0)
	skill := filepath.Join(skills, "x100vi", "SKILL.md")
	text, err := os.ReadFile(skill)
	if err != nil || !strings.Contains(string(text), "name: x100vi") {
		t.Errorf("SKILL.md: %v", err)
	}
	if got := strs(r.json(t)["installed"]); !reflect.DeepEqual(got, []string{filepath.Join(skills, "x100vi")}) {
		t.Errorf("installed = %v", got)
	}
	launcher, err := os.Stat(filepath.Join(skills, "x100vi", "scripts", "x100vi.sh"))
	if err != nil || launcher.Mode()&0o100 == 0 {
		t.Errorf("launcher is missing or not executable: %v", err)
	}
	wantCode(t, runWith(t, boom(t), "install-skill", "--dir", skills), 0) // reinstalling replaces our own copy
}

func TestInstallSkillDoesNotClobberAForeignFolder(t *testing.T) {
	isolate(t)
	skills := filepath.Join(t.TempDir(), "skills")
	other := filepath.Join(skills, "x100vi")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "notes.txt"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := runWith(t, boom(t), "install-skill", "--dir", skills)
	wantCode(t, r, 1)
	mustContain(t, r.err, "not this skill")
	if !exists(filepath.Join(other, "notes.txt")) {
		t.Error("the foreign folder was removed")
	}
	wantCode(t, runWith(t, boom(t), "install-skill", "--dir", skills, "--force"), 0)
	if exists(filepath.Join(other, "notes.txt")) || !exists(filepath.Join(other, "SKILL.md")) {
		t.Error("--force did not replace the folder")
	}
}

func TestInstallSkillNeedsADestination(t *testing.T) {
	isolate(t)
	mustContain(t, runWith(t, boom(t), "install-skill").err, "say where")
}

// The skill is embedded in the binary, so there is nothing to symlink: --link is gone.
func TestInstallSkillHasNoLinkOption(t *testing.T) {
	isolate(t)
	r := runWith(t, boom(t), "install-skill", "--dir", t.TempDir(), "--link")
	wantCode(t, r, 2)
	mustContain(t, r.err, "--link")
}

func TestHomeTargets(t *testing.T) {
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CODEX_HOME", "")
	wantCode(t, runWith(t, boom(t), "install-skill", "--claude", "--codex"), 0)
	for _, p := range []string{".claude/skills/x100vi/SKILL.md", ".codex/skills/x100vi/SKILL.md"} {
		if !exists(filepath.Join(home, filepath.FromSlash(p))) {
			t.Errorf("%s is missing", p)
		}
	}
}

func TestRecipeAdviceIsSpecific(t *testing.T) {
	if a := RecipeAdvice(map[string]any{"dynamic_range": "200"}); len(a) == 0 || !strings.HasPrefix(a[0], "DR200 needs ISO 250") {
		t.Errorf("DR200 advice = %v", a)
	}
	a := RecipeAdvice(map[string]any{"white_balance": map[string]any{"mode": "custom_1"}})
	mustContain(t, strings.Join(a, "\n"), "custom white balance")
	a = RecipeAdvice(map[string]any{"d_range_priority": "weak", "highlight": 1.0})
	mustContain(t, strings.Join(a, "\n"), "ignores highlight")
	if a := RecipeAdvice(map[string]any{"film_simulation": "velvia"}); len(a) != 0 {
		t.Errorf("velvia advice = %v", a)
	}
}

func TestSummaryIsReadable(t *testing.T) {
	if s := library.Summarize(gold); s != "classic_chrome, WB daylight R+3 B-5, DR400, clarity +2" {
		t.Errorf("summary = %q", s)
	}
}

// -- the command line itself (argparse behaviour) ----------------------------------------------------

func TestUsage(t *testing.T) {
	isolate(t)
	r := runWith(t, boom(t))
	wantCode(t, r, 2)
	mustContain(t, r.err, "usage: x100vi", "a command is required")
	r = runWith(t, boom(t), "--help")
	wantCode(t, r, 0)
	mustContain(t, r.out, "commands:", "install-skill", errs.ModeMenu)
	r = runWith(t, boom(t), "--version")
	wantCode(t, r, 0)
	mustContain(t, r.out, "x100vi "+Version)
	r = runWith(t, boom(t), "preview", "--help")
	wantCode(t, r, 0)
	mustContain(t, r.out, "usage: x100vi preview", "--d-range-priority", "--format {jpeg,heif,tiff,tiff16}")
	if strings.Contains(r.out, "--timeout") {
		t.Error("the hidden --timeout option is in the help")
	}
	r = runWith(t, boom(t), "dcotor")
	wantCode(t, r, 2)
	mustContain(t, r.err, "unknown command", "doctor")
	r = runWith(t, boom(t), "set", "c1")
	wantCode(t, r, 2)
	mustContain(t, r.err, "required: recipe")
	r = runWith(t, boom(t), "preview", "a.RAF", "--format", "png")
	wantCode(t, r, 2)
	mustContain(t, r.err, "invalid choice")
	r = runWith(t, boom(t), "get", "c1", "extra")
	wantCode(t, r, 2)
	mustContain(t, r.err, "unrecognized arguments: extra")
}

func TestJSONFlagWorksBeforeTheCommand(t *testing.T) {
	c := newRunner(t)
	if j := c.run("--json", "doctor").json(t); j["ready"] != true {
		t.Errorf("ready = %v", j["ready"])
	}
}

func TestOptionsTakeInlineValuesAndNegativeNumbers(t *testing.T) {
	c := newRunner(t)
	dir := t.TempDir()
	raf := makeRAF(t, filepath.Join(dir, "DSCF0001.RAF"), "")
	r := c.run("preview", raf, "--recipe=everyday", "--ev", "-1", "--out="+filepath.Join(dir, "p"), "--json")
	wantCode(t, r, 0)
	if j := r.json(t); j["exposure_ev"] != -1.0 {
		t.Errorf("exposure_ev = %v", j["exposure_ev"])
	}
	if !exists(filepath.Join(dir, "p", "DSCF0001__everyday.jpg")) {
		t.Error("render missing")
	}
}

// Python parses every key of a backup's "slots" with parse_target, so C-prefixed keys restore too.
func TestRestoreAcceptsSlotKeysSpelledLikeTargets(t *testing.T) {
	c := newRunner(t)
	backup := writeRecipe(t, t.TempDir(), map[string]any{"slots": map[string]any{"C3": gold}}, "hand.json")
	r := c.run("restore", backup, "--yes", "--json")
	wantCode(t, r, 0)
	if got := strs(r.json(t)["restored"]); !reflect.DeepEqual(got, []string{"C3"}) {
		t.Errorf("restored = %v", got)
	}
	if c.fake.Names[3] != "GOLD" {
		t.Errorf("C3 name = %q", c.fake.Names[3])
	}
	bad := writeRecipe(t, t.TempDir(), map[string]any{"slots": map[string]any{"9": gold}}, "bad.json")
	r = c.run("restore", bad, "--yes")
	wantCode(t, r, 2)
	mustContain(t, r.err, "unknown target")
}

// --dir ~ is the home folder, as Path.expanduser makes it.
func TestInstallSkillExpandsTheHomeFolder(t *testing.T) {
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Chdir(t.TempDir())
	wantCode(t, runWith(t, boom(t), "install-skill", "--dir", "~"), 0)
	wantCode(t, runWith(t, boom(t), "install-skill", "--dir", "~/skills"), 0)
	for _, p := range []string{"x100vi/SKILL.md", "skills/x100vi/SKILL.md"} {
		if !exists(filepath.Join(home, filepath.FromSlash(p))) {
			t.Errorf("~/%s is missing", p)
		}
	}
}
