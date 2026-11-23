package cli

// Checks that run before connecting, unique preview paths, ordered JSON errors and the camera lock.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/volodymyrsmirnov/x100vi-skill/internal/camlock"
	"github.com/volodymyrsmirnov/x100vi-skill/internal/errs"
)

// jsonKeys lists the top-level keys of one JSON object in the order they were written.
func jsonKeys(t *testing.T, text string) []string {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(text))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		t.Fatalf("not a JSON object: %v %v\n%s", tok, err, text)
	}
	var keys []string
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, tok.(string))
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			t.Fatal(err)
		}
	}
	return keys
}

func TestJSONErrorsKeepTheirKeyOrder(t *testing.T) {
	isolate(t)
	r := runWith(t, boom(t), "set", "c9", writeRecipe(t, t.TempDir(), gold, ""), "--json")
	wantCode(t, r, 2)
	if got := jsonKeys(t, r.out); !reflect.DeepEqual(got, []string{"error", "message", "hint"}) {
		t.Fatalf("keys %v in\n%s", got, r.out)
	}
	m := errs.New(errs.General, "no hint", "").AsMap()
	if !reflect.DeepEqual(m.Keys(), []string{"error", "message"}) {
		t.Fatal(m.Keys())
	}
}

func TestMissingOptionValueIsAUsageError(t *testing.T) {
	isolate(t)
	r := runWith(t, boom(t), "get", "c1", "--output", "--json")
	wantCode(t, r, 2)
	mustContain(t, r.err, "--output needs a value")
	r = runWith(t, boom(t), "preview", "a.RAF", "-r", "everyday", "--out", "--full")
	wantCode(t, r, 2)
	mustContain(t, r.err, "--out needs a value")
	r = runWith(t, boom(t), "preview", "a.RAF", "-r", "everyday", "--ev", "-1", "--d-range-priority", "--full")
	wantCode(t, r, 2)
	mustContain(t, r.err, "--d-range-priority needs a value")
}

func TestVerifyRequiresARecipe(t *testing.T) {
	isolate(t)
	r := runWith(t, boom(t), "verify", "photo.jpg")
	wantCode(t, r, 2)
	mustContain(t, r.err, "the following arguments are required: -r/--recipe")
	r = runWith(t, boom(t), "verify")
	wantCode(t, r, 2)
	mustContain(t, r.err, "the following arguments are required: photo, -r/--recipe")
	r = runWith(t, boom(t), "verify", "--help")
	wantCode(t, r, 0)
	mustContain(t, r.out, "usage: x100vi verify [options] -r RECIPE photo")
}

func TestGetChecksTheTargetBeforeConnecting(t *testing.T) {
	isolate(t)
	r := runWith(t, boom(t), "get", "c9")
	wantCode(t, r, 2)
	mustContain(t, r.err, "unknown target 'c9'")
	c := newRunner(t)
	wantCode(t, c.run("get", "ALL"), 0)
	wantCode(t, c.run("get", "C2"), 0)
}

func TestRestoreSlotsMustBeCustomSettingsListedOnce(t *testing.T) {
	isolate(t)
	dir := t.TempDir()
	live := writeRecipe(t, dir, map[string]any{"slots": map[string]any{"1": gold, "live": gold}}, "live.json")
	r := runWith(t, boom(t), "restore", live, "--yes")
	wantCode(t, r, 2)
	mustContain(t, r.err, `every key under "slots"`, "not 'live'", "--live")
	twice := writeRecipe(t, dir, map[string]any{"slots": map[string]any{"3": gold, "C3": gold}}, "twice.json")
	r = runWith(t, boom(t), "restore", twice, "--yes")
	wantCode(t, r, 2)
	mustContain(t, r.err, "lists C3 twice")
	r = runWith(t, boom(t), "restore", writeRecipe(t, dir, map[string]any{"slots": map[string]any{"c3": gold, "C3": gold}}, "case.json"), "--yes")
	wantCode(t, r, 2)
	mustContain(t, r.err, "lists C3 twice")
}

func TestPreviewPathsAreUniqueForTheWholeBatch(t *testing.T) {
	c := newRunner(t)
	dir := t.TempDir()
	for _, sub := range []string{"day1", "day2"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	a := makeRAF(t, filepath.Join(dir, "day1", "DSCF0001.RAF"), "")
	b := makeRAF(t, filepath.Join(dir, "day2", "DSCF0001.RAF"), "")
	asShot := writeRecipe(t, dir, gold, "as-shot.json") // a recipe that happens to be called as-shot
	out := filepath.Join(dir, "p")
	r := c.run("preview", a, b, "--as-shot", "-r", asShot, "everyday", "-o", out, "--json")
	wantCode(t, r, 0)
	j := r.json(t)
	seen := map[string]bool{}
	var files []string
	for _, x := range j["renders"].([]any) {
		f := get(x, "file").(string)
		if seen[strings.ToLower(f)] {
			t.Fatalf("%s written twice", f)
		}
		seen[strings.ToLower(f)] = true
		files = append(files, filepath.Base(f))
	}
	want := []string{
		"DSCF0001__as-shot.jpg", "DSCF0001__as-shot-2.jpg", "DSCF0001__everyday.jpg",
		"DSCF0001-2__as-shot.jpg", "DSCF0001-2__as-shot-2.jpg", "DSCF0001-2__everyday.jpg",
	}
	if !reflect.DeepEqual(files, want) {
		t.Fatalf("files %v", files)
	}
	// each file holds its own render: the as-shot one keeps the frame's film, the recipe one is gold's
	if got, _ := cells(t, filepath.Join(out, "DSCF0001__as-shot.jpg")); got[7] != 1 {
		t.Errorf("as-shot cell 7 = %d", got[7])
	}
	if got, _ := cells(t, filepath.Join(out, "DSCF0001-2__as-shot-2.jpg")); got[6] != 200 || got[7] != 0 {
		t.Errorf("recipe as-shot cells = %v", got[6:8])
	}
	if !exists(filepath.Join(out, "contact-sheet.jpg")) {
		t.Error("no contact sheet")
	}
}

func TestPreviewLabelsCollidingOnlyInCaseAreKeptApart(t *testing.T) {
	c := newRunner(t)
	dir := t.TempDir()
	raf := makeRAF(t, filepath.Join(dir, "DSCF0001.RAF"), "")
	upper := writeRecipe(t, dir, gold, "Gold.json")
	if err := os.MkdirAll(filepath.Join(dir, "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	lower := writeRecipe(t, filepath.Join(dir, "x"), gold, "gold.json")
	r := c.run("preview", raf, "-r", upper, lower, "-o", filepath.Join(dir, "p"), "--json", "--no-sheet")
	wantCode(t, r, 0)
	renders := r.json(t)["renders"].([]any)
	if a, b := get(renders[0], "file"), get(renders[1], "file"); strings.EqualFold(a.(string), b.(string)) {
		t.Fatal(a, b)
	}
}

// holdLock takes this test's camera lock (see isolate) as another x100vi command would, with a short
// wait for this test.
func holdLock(t *testing.T) {
	t.Helper()
	saved := camlock.Timeout
	camlock.Timeout = 200 * time.Millisecond
	t.Cleanup(func() { camlock.Timeout = saved })
	release, err := camlock.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
}

func TestACameraCommandWaitsForAnotherOneAndThenGivesUp(t *testing.T) {
	isolate(t)
	holdLock(t)
	r := runWith(t, boom(t), "get", "c1")
	wantCode(t, r, errs.CameraBusy.Exit)
	mustContain(t, r.err, "another x100vi command is using the camera", "Wait for it to finish")
	r = runWith(t, boom(t), "doctor", "--json")
	wantCode(t, r, errs.CameraBusy.Exit)
	if j := r.json(t); j["ready"] != false || j["error"] != "camera_busy" {
		t.Fatalf("doctor --json = %v", j)
	}
}

func TestTheCameraLockIsReleasedAfterACommand(t *testing.T) {
	c := newRunner(t)
	wantCode(t, c.run("get", "c1"), 0)
	wantCode(t, c.run("doctor"), 0)
	saved := camlock.Timeout
	camlock.Timeout = 0
	t.Cleanup(func() { camlock.Timeout = saved })
	release, err := camlock.Acquire()
	if err != nil {
		t.Fatalf("the lock was kept: %v", err)
	}
	release()
}

// --json after the command name is an option in token order: `--output --json C2` is missing the
// output file, and must never save the live settings to a file named C2.
func TestJSONAfterAValueOptionIsAMissingValue(t *testing.T) {
	isolate(t)
	dir := t.TempDir()
	t.Chdir(dir)
	r := runWith(t, boom(t), "get", "--output", "--json", "C2")
	wantCode(t, r, 2)
	mustContain(t, r.err, "--output needs a value")
	if j := r.json(t); j["error"] != "invalid_recipe" { // the caller asked for JSON, so the error is JSON
		t.Fatalf("%v", j)
	}
	if exists(filepath.Join(dir, "C2")) {
		t.Fatal("saved to a file named C2")
	}
}

// --json before the command name and --json after it both select JSON output.
func TestJSONBeforeOrAfterTheCommand(t *testing.T) {
	c := newRunner(t)
	target := filepath.Join(t.TempDir(), "c2.json")
	for _, argv := range [][]string{
		{"--json", "get", "c2", "-o", target},
		{"get", "c2", "-o", target, "--json"},
		{"get", "--json", "c2", "-o", target},
	} {
		r := c.run(argv...)
		wantCode(t, r, 0)
		if j := r.json(t); j["saved"] != target || j["target"] != "c2" {
			t.Fatalf("%v: %v", argv, j)
		}
	}
	r := c.run("get", "c2", "-o", target)
	wantCode(t, r, 0)
	mustContain(t, r.out, "saved c2 to "+target)
	if r := c.run("--json", "doctor"); r.json(t)["ready"] != true {
		t.Fatal(r.out)
	}
}

// Every CLI test locks a file of its own, never the real camera lock a running x100vi holds.
func TestCLITestsUseTheirOwnCameraLock(t *testing.T) {
	isolate(t)
	if p := camlock.Path(); !strings.Contains(p, t.Name()) {
		t.Fatalf("lock file %s is not this test's", p)
	}
}

// Tests that do not call isolate still lock the test process's own file (see TestMain).
func TestTheCLIPackageNeverLocksTheRealFile(t *testing.T) {
	if p := camlock.Path(); p == "/tmp/x100vi-camera.lock" || !strings.Contains(p, "x100vi-cli-test") {
		t.Fatalf("lock file %s", p)
	}
}
