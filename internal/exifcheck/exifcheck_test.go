package exifcheck

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/volodymyrsmirnov/x100vi-skill/internal/errs"
)

func decode(t *testing.T, text string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(text), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func expected(t *testing.T, recipe string) map[string]any {
	t.Helper()
	e, err := ExpectedTags(decode(t, recipe))
	if err != nil {
		t.Fatal(err)
	}
	return e.ToPlain()
}

func TestExpectedTagsForAColourRecipe(t *testing.T) {
	tags := expected(t, `{"film_simulation": "reala_ace", "dynamic_range": "200", "color": 2,
		"grain": {"strength": "weak", "size": "large"}, "smooth_skin": "strong",
		"white_balance": {"mode": "kelvin", "kelvin": 5000, "shift_r": 1, "shift_b": -2}}`)
	want := map[string]any{
		"FilmMode": "Reala ACE", "Saturation#": 2, "WhiteBalance": "Kelvin", "ColorTemperature#": 5000,
		"WhiteBalanceFineTune": "Red +20, Blue -40", "GrainEffectRoughness": "Weak", "GrainEffectSize": "Large",
		SmoothSkinTag: 64, "DynamicRangeSetting": "Manual", "DevelopmentDynamicRange#": 200,
		"HighlightTone#": 0, "ShadowTone#": 0, "ColorChromeEffect": "Off", "ColorChromeFXBlue": "Off",
		"Sharpness#": 0, "NoiseReduction#": 0, "Clarity#": 0,
	}
	if len(tags) != len(want) {
		t.Errorf("got %d tags, want %d: %v", len(tags), len(want), tags)
	}
	for k, v := range want {
		if tags[k] != v {
			t.Errorf("%s: got %#v, want %#v", k, tags[k], v)
		}
	}
}

func TestExpectedTagsForMonochromeAndPriority(t *testing.T) {
	tags := expected(t, `{"film_simulation": "acros_r", "monochromatic": {"warm_cool": 3},
		"d_range_priority": "strong", "highlight": 2}`)
	if tags["Saturation"] != "Acros Red Filter" || tags["FilmMode"] != nil || tags["Saturation#"] != nil {
		t.Errorf("film: %v", tags)
	}
	if tags["BWAdjustment#"] != 3 || tags["BWMagentaGreen#"] != 0 || tags["DRangePriority"] != "Fixed" {
		t.Errorf("mono/priority: %v", tags)
	}
	if tags["DRangePriorityAuto|DRangePriorityFixed"] != "Strong" {
		t.Errorf("priority level: %v", tags)
	}
	if _, ok := tags["HighlightTone#"]; ok || tags["GrainEffectSize"] != "Off" {
		t.Errorf("tone/grain: %v", tags)
	}
	if _, ok := tags["DynamicRangeSetting"]; ok {
		t.Errorf("dynamic range is not checked under priority")
	}
}

func TestExpectedTagsForPriorityPlusAutoAndSepia(t *testing.T) {
	plus := expected(t, `{"d_range_priority": "plus"}`)
	if plus["DRangePriority"] != "Fixed" || plus["DRangePriorityAuto|DRangePriorityFixed"] != "Plus" {
		t.Errorf("plus: %v", plus)
	}
	auto := expected(t, `{"d_range_priority": "auto", "dynamic_range": "auto"}`)
	if auto["DRangePriority"] != "Auto" || auto["DRangePriorityAuto|DRangePriorityFixed"] != nil {
		t.Errorf("auto priority: %v", auto)
	}
	dr := expected(t, `{"dynamic_range": "auto", "white_balance": {"mode": "shade"}}`)
	if dr["DynamicRangeSetting"] != "Auto" || dr["DevelopmentDynamicRange#"] != nil || dr["WhiteBalance"] != "Cloudy" {
		t.Errorf("auto DR: %v", dr)
	}
	sepia := expected(t, `{"film_simulation": "sepia"}`)
	if sepia["Saturation"] != "B&W Sepia" || sepia["BWAdjustment#"] != nil {
		t.Errorf("sepia is not a monochromatic-colour sim: %v", sepia)
	}
	order, _ := ExpectedTags(map[string]any{})
	if keys := order.Keys(); keys[0] != "FilmMode" || keys[len(keys)-1] != "Clarity#" {
		t.Errorf("order: %v", keys)
	}
	if _, err := ExpectedTags(map[string]any{"film_simulation": "kodachrome"}); !errs.Is(err, errs.InvalidRecipe) {
		t.Errorf("unknown film: %v", err)
	}
}

func fakeTags(t *testing.T, recipe map[string]any) map[string]any {
	t.Helper()
	e, err := ExpectedTags(recipe)
	if err != nil {
		t.Fatal(err)
	}
	tags := map[string]any{}
	for _, k := range e.Keys() {
		v, _ := e.Get(k)
		tags[strings.Split(strings.TrimRight(k, "#"), "|")[0]] = v
	}
	return tags
}

func TestCompareReportsMismatches(t *testing.T) {
	photo := filepath.Join(t.TempDir(), "a.jpg")
	recipe := map[string]any{"film_simulation": "velvia", "color": 2.0}
	tags := fakeTags(t, recipe)
	readTags = func(string) (map[string]any, error) { return tags, nil }
	t.Cleanup(func() { readTags = ReadTags })
	res, err := Compare(recipe, photo)
	if err != nil || len(res.Mismatches) != 0 || res.Checked != 16 || res.Photo != photo {
		t.Fatalf("%+v %v", res, err)
	}
	tags["FilmMode"] = "Classic Chrome"
	tags["Saturation"] = "+1 (medium high)"
	tags["Clarity"] = "0"
	res, _ = Compare(recipe, photo)
	got := map[string]bool{}
	for _, m := range res.Mismatches {
		got[m.Tag] = true
	}
	if len(got) != 2 || !got["FilmMode"] || !got["Saturation"] {
		t.Errorf("mismatches: %+v", res.Mismatches)
	}
	out, _ := json.Marshal(res.Map())
	if !strings.HasPrefix(string(out), `{"photo":`) || !strings.Contains(string(out), `"expected":2,"found":"+1 (medium high)"`) {
		t.Errorf("json: %s", out)
	}
	delete(tags, "FilmMode")
	delete(tags, "Saturation")
	if _, err := Compare(recipe, photo); err == nil || !strings.Contains(err.Error(), "carries no Fujifilm recipe metadata") {
		t.Errorf("stripped photo: %v", err)
	}
}

func TestCompareMissingTagIsAMismatch(t *testing.T) {
	recipe := map[string]any{"d_range_priority": "weak"}
	tags := fakeTags(t, recipe)
	delete(tags, "DRangePriorityAuto")
	tags["DRangePriorityFixed"] = "Weak" // the alternative name is accepted
	readTags = func(string) (map[string]any, error) { return tags, nil }
	t.Cleanup(func() { readTags = ReadTags })
	if res, _ := Compare(recipe, "x.jpg"); len(res.Mismatches) != 0 {
		t.Errorf("alternative tag: %+v", res.Mismatches)
	}
	delete(tags, "Sharpness")
	res, _ := Compare(recipe, "x.jpg")
	if len(res.Mismatches) != 1 || res.Mismatches[0].Tag != "Sharpness" || res.Mismatches[0].Found != nil {
		t.Errorf("missing tag: %+v", res.Mismatches)
	}
	b, _ := json.Marshal(res)
	if !strings.Contains(string(b), `"found":null`) {
		t.Errorf("json: %s", b)
	}
}

func TestRequireExiftool(t *testing.T) {
	old := lookPath
	lookPath = func(string) (string, error) { return "", errors.New("not found") }
	t.Cleanup(func() { lookPath = old })
	_, err := RequireExiftool()
	e, ok := errs.As(err)
	if !ok || e.Kind != errs.MissingDependency || e.Kind.Exit != 5 || e.Message != "exiftool is not installed" ||
		!strings.Contains(e.Hint, "brew install exiftool") {
		t.Errorf("%v", err)
	}
	if _, err := ReadTags("x.jpg"); !errs.Is(err, errs.MissingDependency) {
		t.Errorf("ReadTags without exiftool: %v", err)
	}
}

func TestParseTagsPrefersMakerNotes(t *testing.T) {
	out := `[{"SourceFile": "a.jpg", "IFD0:Saturation": "Normal", "FujiFilm:Saturation": " +2 (high) ",
		"ExifIFD:Sharpness": "Normal", "Other:Sharpness": "Hard", "FujiFilm:FujiFilm_0x104a": 32,
		"FujiFilm:ColorTemperature": 5000}]`
	tags, err := parseTags([]byte(out))
	if err != nil {
		t.Fatal(err)
	}
	if tags["Saturation"] != "+2 (high)" || tags["Sharpness"] != "Normal" || tags[SmoothSkinTag] != 32.0 ||
		tags["SourceFile"] != "a.jpg" {
		t.Errorf("%v", tags)
	}
	if n, ok := leadingNumber(tags["Saturation"]); !ok || n != 2 {
		t.Errorf("leading number %v %v", n, ok)
	}
	if _, ok := leadingNumber("Normal"); ok {
		t.Errorf("no number in Normal")
	}
	if _, err := parseTags([]byte(`{}`)); err == nil {
		t.Errorf("not a list")
	}
}

func TestReadTagsRunsExiftool(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell script as a fake exiftool")
	}
	bin := t.TempDir()
	script := "#!/bin/sh\n" +
		`for last; do :; done` + "\n" +
		`case "$last" in *bad*) echo "Error: File format error" >&2; exit 1 ;; esac` + "\n" +
		`echo '[{"SourceFile": "'"$last"'", "FujiFilm:FilmMode": "Velvia", "args": "'"$*"'"}]'` + "\n"
	if err := os.WriteFile(filepath.Join(bin, "exiftool"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	dir := t.TempDir()
	photo := filepath.Join(dir, "a.jpg")
	os.WriteFile(photo, []byte("x"), 0o644)
	tags, err := ReadTags(photo)
	if err != nil {
		t.Fatal(err)
	}
	if tags["FilmMode"] != "Velvia" || tags["args"] != "-j -u -G1 "+photo {
		t.Errorf("%v", tags)
	}
	bad := filepath.Join(dir, "bad.jpg")
	os.WriteFile(bad, []byte("x"), 0o644)
	if _, err := ReadTags(bad); err == nil || !strings.Contains(err.Error(), "exiftool could not read") ||
		!strings.Contains(err.Error(), "File format error") {
		t.Errorf("failing exiftool: %v", err)
	}
	if _, err := ReadTags(filepath.Join(dir, "missing.jpg")); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("missing photo: %v", err)
	}
}
