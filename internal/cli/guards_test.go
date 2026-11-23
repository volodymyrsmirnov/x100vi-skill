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
	"github.com/volodymyrsmirnov/x100vi-skill/internal/fakecam"
	"github.com/volodymyrsmirnov/x100vi-skill/internal/rawconv"
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

// The same key written twice in the JSON text is a duplicate too, although decoding keeps only one: it
// is refused before connecting, with --yes or without.
func TestRestoreRefusesALiteralDuplicateSlotKey(t *testing.T) {
	isolate(t)
	dir := t.TempDir()
	g, err := json.Marshal(gold)
	if err != nil {
		t.Fatal(err)
	}
	other, _ := json.Marshal(map[string]any{"name": "OTHER", "film_simulation": "velvia"})
	for name, text := range map[string]string{
		"twice":       `{"live": ` + string(g) + `, "slots": {"C1": ` + string(g) + `, "C2": ` + string(g) + `, "C1": ` + string(other) + `}}`,
		"slots twice": `{"slots": {"C1": ` + string(g) + `}, "slots": {"C2": ` + string(g) + `}}`,
	} {
		path := filepath.Join(dir, strings.ReplaceAll(name, " ", "-")+".json")
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		for _, extra := range [][]string{{"--yes"}, {}} {
			r := runWith(t, boom(t), append([]string{"restore", path}, extra...)...)
			wantCode(t, r, 2)
			if name == "twice" {
				mustContain(t, r.err, "lists C1 twice (as 'C1' and 'C1')")
			} else {
				mustContain(t, r.err, `has "slots" twice`)
			}
		}
	}
	// keys that are not custom settings are still explained by restore itself
	path := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(path, []byte(`{"slots": {"C9": `+string(g)+`, "C9": `+string(g)+`}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	r := runWith(t, boom(t), "restore", path, "--yes")
	wantCode(t, r, 2)
	mustContain(t, r.err, "unknown target 'C9'")
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

// An unknown command is reported as JSON when --json comes before or after it, and as text when --json
// only follows "--" (where it is not an option).
func TestUnknownCommandErrorIsJSONWithJSONOnEitherSide(t *testing.T) {
	isolate(t)
	for _, argv := range [][]string{{"--json", "gte"}, {"gte", "--json"}, {"gte", "c1", "--json"}} {
		r := runWith(t, boom(t), argv...)
		wantCode(t, r, errs.InvalidRecipe.Exit)
		j := r.json(t)
		if j["error"] != "invalid_recipe" || !strings.Contains(j["message"].(string), "unknown command 'gte'") ||
			!strings.Contains(j["message"].(string), "Did you mean 'get'?") {
			t.Fatalf("%v: %v", argv, j)
		}
		mustContain(t, r.err, "error: unknown command 'gte'")
	}
	r := runWith(t, boom(t), "gte", "--", "--json")
	wantCode(t, r, errs.InvalidRecipe.Exit)
	if r.out != "" {
		t.Fatalf("--json after -- printed JSON: %q", r.out)
	}
	mustContain(t, r.err, "unknown command 'gte'")
}

// foldsUnicode reports whether dir's file system treats the composed and decomposed spellings of a
// name as one file (APFS and HFS+ on macOS do).
func foldsUnicode(t *testing.T, dir, composed, decomposed string) bool {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, composed), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(filepath.Join(dir, composed))
	a, errA := os.Stat(filepath.Join(dir, composed))
	b, errB := os.Stat(filepath.Join(dir, decomposed))
	return errA == nil && errB == nil && os.SameFile(a, b)
}

// Two RAFs named café.RAF, one spelled composed and one decomposed, in two folders: on macOS both
// output names are one file, so one render would silently replace the other. The second gets -2.
// Where the file system keeps the spellings apart, a stat that folds them stands in for macOS.
func TestPreviewNamesThatAreOneFileOnlyToTheFileSystemAreKeptApart(t *testing.T) {
	c := newRunner(t)
	dir := t.TempDir()
	composed, decomposed := "caf\u00e9", "cafe\u0301" // one name to macOS, two byte strings
	if !foldsUnicode(t, dir, composed+".probe", decomposed+".probe") {
		saved := statOutput
		statOutput = func(name string) (os.FileInfo, error) { // the composed spelling's file, when there is one
			if fi, err := os.Stat(strings.ReplaceAll(name, decomposed, composed)); err == nil {
				return fi, nil
			}
			return os.Stat(name)
		}
		t.Cleanup(func() { statOutput = saved })
	}
	var rafs []string
	for i, stem := range []string{composed, decomposed} {
		sub := filepath.Join(dir, []string{"a", "b"}[i])
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		rafs = append(rafs, makeRAF(t, filepath.Join(sub, stem+".RAF"), ""))
	}
	out := filepath.Join(dir, "p")
	r := c.run(append([]string{"preview"}, append(rafs, "--as-shot", "-o", out, "--json", "--no-sheet")...)...)
	wantCode(t, r, 0)
	renders := r.json(t)["renders"].([]any)
	if len(renders) != 2 {
		t.Fatal(renders)
	}
	first, second := get(renders[0], "file").(string), get(renders[1], "file").(string)
	if filepath.Base(first) != composed+"__as-shot.jpg" || filepath.Base(second) != decomposed+"__as-shot-2.jpg" {
		t.Fatalf("%q %q", filepath.Base(first), filepath.Base(second))
	}
	a, errA := statOutput(first)
	b, errB := statOutput(second)
	if errA != nil || errB != nil || os.SameFile(a, b) || a.Size() == 0 || b.Size() == 0 {
		t.Fatalf("renders share a file or are empty: %v %v", errA, errB)
	}
	// the decomposed spelling without a suffix was not left behind as an empty file
	entries, err := os.ReadDir(out)
	if err != nil || len(entries) != 2 {
		t.Fatalf("%v %v", entries, err)
	}
}

// Output files are created before connecting; when the command fails, those it created and never
// wrote are removed again, while files that were already there and finished renders are kept.
func TestPreviewRemovesUnwrittenOutputFilesOnFailure(t *testing.T) {
	isolate(t)
	dir := t.TempDir()
	raf := makeRAF(t, filepath.Join(dir, "DSCF0001.RAF"), "")
	out := filepath.Join(dir, "p")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(out, "DSCF0001__everyday.jpg") // from an earlier run
	if err := os.WriteFile(old, []byte("earlier"), 0o644); err != nil {
		t.Fatal(err)
	}

	// no camera: nothing was rendered, so nothing new is left behind and the old file is untouched
	r := runWith(t, failing(errs.New(errs.CameraNotFound, "no camera", "")), "preview", raf, "--as-shot",
		"-r", "everyday", "-o", out, "--no-sheet")
	wantCode(t, r, errs.CameraNotFound.Exit)
	entries, _ := os.ReadDir(out)
	if len(entries) != 1 {
		t.Fatalf("left behind: %v", entries)
	}
	if b, _ := os.ReadFile(old); string(b) != "earlier" {
		t.Fatalf("an existing file was changed: %q", b)
	}

	// the second conversion fails: the first render stays, the second file (which existed) stays as it
	// was, and a third that was only reserved is removed
	fake := fakecam.New()
	triggers := 0
	fake.Intercept = func(op uint16, params []uint32, data []byte, next func() (uint16, []byte)) (uint16, []byte) {
		if op == 0x1016 && len(params) == 1 && params[0] == rawconv.PropTrigger {
			if triggers++; triggers == 2 {
				return fakecam.GeneralError, nil
			}
		}
		return next()
	}
	r = runWith(t, cameraFactory(fake), "preview", raf, "--as-shot", "-r", "everyday", "street-mono", "-o", out, "--no-sheet")
	if r.code == 0 {
		t.Fatal("the failed conversion did not fail the command")
	}
	if !exists(filepath.Join(out, "DSCF0001__as-shot.jpg")) {
		t.Fatal("the finished render was removed")
	}
	if b, _ := os.ReadFile(old); string(b) != "earlier" {
		t.Fatalf("an existing file was changed or removed: %q", b)
	}
	if exists(filepath.Join(out, "DSCF0001__street-mono.jpg")) {
		t.Fatal("an unused reserved file was left behind")
	}
}
