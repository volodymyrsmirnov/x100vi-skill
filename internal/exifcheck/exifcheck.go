// Package exifcheck compares a photo's Fujifilm maker notes with a recipe.
//
// It uses exiftool, an implementation independent of this project, so it doubles as ground truth:
// a JPEG developed with a recipe (or shot with it) should carry exactly the recipe's settings.
package exifcheck

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/volodymyrsmirnov/x100vi-skill/internal/errs"
	"github.com/volodymyrsmirnov/x100vi-skill/internal/model"
	"github.com/volodymyrsmirnov/x100vi-skill/internal/omap"
)

// Film is the FilmMode tag for each colour film simulation.
var Film = map[string]string{
	"provia": "F0/Standard (Provia)", "velvia": "F2/Fujichrome (Velvia)",
	"astia": "F1b/Studio Portrait Smooth Skin Tone (Astia)", "pro_neg_hi": "Pro Neg. Hi",
	"pro_neg_std": "Pro Neg. Std", "classic_chrome": "Classic Chrome", "eterna": "Eterna",
	"classic_neg": "Classic Negative", "eterna_bleach_bypass": "Bleach Bypass",
	"nostalgic_neg": "Nostalgic Neg", "reala_ace": "Reala ACE",
}

// BW is how black-and-white simulations are reported: in the Saturation tag.
var BW = map[string]string{
	"monochrome": "None (B&W)", "monochrome_ye": "B&W Yellow Filter", "monochrome_r": "B&W Red Filter",
	"monochrome_g": "B&W Green Filter", "sepia": "B&W Sepia", "acros": "Acros",
	"acros_ye": "Acros Yellow Filter", "acros_r": "Acros Red Filter", "acros_g": "Acros Green Filter",
}

// WB is the WhiteBalance tag for each white balance mode.
var WB = map[string]string{
	"auto": "Auto", "auto_white_priority": "Auto (white priority)",
	"auto_ambience_priority": "Auto (ambiance priority)", "daylight": "Daylight", "shade": "Cloudy",
	"fluorescent_1": "Daylight Fluorescent", "fluorescent_2": "Day White Fluorescent",
	"fluorescent_3": "White Fluorescent", "incandescent": "Incandescent", "underwater": "Underwater",
	"kelvin": "Kelvin", "custom_1": "Custom", "custom_2": "Custom2", "custom_3": "Custom3",
}

// SmoothSkinTag is unnamed in exiftool: 0 off, 32 weak, 64 strong.
const SmoothSkinTag = "FujiFilm_0x104a"

// Smooth is the SmoothSkinTag value for each smooth skin level.
var Smooth = map[string]int{"off": 0, "weak": 32, "strong": 64}

var numberRE = regexp.MustCompile(`^\s*([+-]?\d+(?:\.\d+)?)`)

// leadingNumber reads the number a tag value starts with ("+2 (high)" -> 2).
func leadingNumber(v any) (float64, bool) {
	m := numberRE.FindStringSubmatch(pyStr(v))
	if m == nil {
		return 0, false
	}
	n, err := strconv.ParseFloat(m[1], 64)
	return n, err == nil
}

func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	}
	return 0, false
}

// pyStr renders a value like Python's str().
func pyStr(v any) string {
	switch x := v.(type) {
	case nil:
		return "None"
	case string:
		return x
	case bool:
		if x {
			return "True"
		}
		return "False"
	}
	if n, ok := toFloat(v); ok {
		return model.FmtG(n)
	}
	return fmt.Sprint(v)
}

// capitalize is Python's str.capitalize().
func capitalize(s string) string {
	s = strings.ToLower(s)
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// num is a recipe number for output: an int when whole. Missing values count as 0.
func num(v any) any {
	if v == nil {
		return 0
	}
	if n, ok := toFloat(v); ok {
		return model.Number(n)
	}
	return v
}

func intOf(v any) int {
	n, _ := toFloat(v)
	return int(n)
}

func plain(v any) map[string]any {
	switch x := v.(type) {
	case map[string]any:
		return x
	case *omap.Map:
		return x.ToPlain()
	}
	return map[string]any{}
}

func lower(v any) string { return strings.ToLower(pyStr(v)) }

// ExpectedTags maps each EXIF tag to the value a photo made with the recipe should carry, in the
// order they are checked. A trailing '#' on a key compares numerically; '|' lists alternative tag
// names. The recipe should already be valid (model.EncodeRecipe); `d_range_priority` may also be
// "plus", the converter-only level (exiftool: Fixed / Plus).
func ExpectedTags(recipe map[string]any) (*omap.Map, error) {
	flat := map[string]any{}
	for k, v := range recipe {
		if m, ok := v.(*omap.Map); ok {
			v = m.ToPlain()
		}
		flat[k] = v
	}
	r := model.Merged(flat, false)
	film := lower(r["film_simulation"])
	code, ok := model.FilmSimulation.Codes[film]
	if !ok {
		return nil, errs.Recipe(fmt.Sprintf("film_simulation: %s is not valid.", model.Quote(r["film_simulation"])), "")
	}
	e := omap.New()
	if model.NoColourSims[code] {
		e.Set("Saturation", BW[film])
	} else {
		e.Set("FilmMode", Film[film])
		e.Set("Saturation#", num(r["color"]))
	}
	if model.MonoSims[code] {
		mono := plain(recipe["monochromatic"])
		e.Set("BWAdjustment#", num(mono["warm_cool"]))
		e.Set("BWMagentaGreen#", num(mono["magenta_green"]))
	}
	grain := plain(r["grain"])
	strength := lower(grain["strength"])
	e.Set("GrainEffectRoughness", capitalize(strength))
	if strength == "off" {
		e.Set("GrainEffectSize", "Off")
	} else {
		size, ok := grain["size"]
		if !ok || size == nil {
			size = "small"
		}
		e.Set("GrainEffectSize", capitalize(pyStr(size)))
	}
	e.Set("ColorChromeEffect", capitalize(pyStr(r["color_chrome_effect"])))
	e.Set("ColorChromeFXBlue", capitalize(pyStr(r["color_chrome_fx_blue"])))
	smooth, ok := Smooth[lower(r["smooth_skin"])]
	if !ok {
		return nil, errs.Recipe(fmt.Sprintf("smooth_skin: %s is not valid.", model.Quote(r["smooth_skin"])), "")
	}
	e.Set(SmoothSkinTag, smooth)
	wb := plain(r["white_balance"])
	mode := lower(wb["mode"])
	wbTag, ok := WB[mode]
	if !ok {
		return nil, errs.Recipe(fmt.Sprintf("white_balance.mode: %s is not valid.", model.Quote(wb["mode"])), "")
	}
	e.Set("WhiteBalance", wbTag)
	if mode == "kelvin" {
		e.Set("ColorTemperature#", num(wb["kelvin"]))
	}
	e.Set("WhiteBalanceFineTune", fmt.Sprintf("Red %+d, Blue %+d", intOf(wb["shift_r"])*20, intOf(wb["shift_b"])*20))
	switch priority := lower(r["d_range_priority"]); priority {
	case "auto":
		e.Set("DRangePriority", "Auto")
	case "off":
		if lower(r["dynamic_range"]) == "auto" {
			e.Set("DynamicRangeSetting", "Auto")
		} else {
			dr, err := strconv.ParseFloat(pyStr(r["dynamic_range"]), 64)
			if err != nil {
				return nil, errs.Recipe(fmt.Sprintf("dynamic_range: %s is not valid.", model.Quote(r["dynamic_range"])), "")
			}
			e.Set("DynamicRangeSetting", "Manual")
			e.Set("DevelopmentDynamicRange#", int(dr))
		}
		e.Set("HighlightTone#", num(r["highlight"]))
		e.Set("ShadowTone#", num(r["shadow"]))
	default: // weak, strong, plus
		e.Set("DRangePriority", "Fixed")
		e.Set("DRangePriorityAuto|DRangePriorityFixed", capitalize(priority))
	}
	e.Set("Sharpness#", num(r["sharpness"]))
	e.Set("NoiseReduction#", num(r["noise_reduction"]))
	e.Set("Clarity#", num(r["clarity"]))
	return e, nil
}

// lookPath is exec.LookPath, replaceable in tests.
var lookPath = exec.LookPath

// RequireExiftool returns the exiftool executable, or a MissingDependency error with install help.
func RequireExiftool() (string, error) {
	exe, err := lookPath("exiftool")
	if err != nil {
		return "", errs.New(errs.MissingDependency, "exiftool is not installed",
			"Install it to compare photos with recipes: `brew install exiftool` (macOS), "+
				"`sudo apt install libimage-exiftool-perl` (Debian/Ubuntu), or https://exiftool.org (Windows).")
	}
	return exe, nil
}

// ReadTags runs exiftool on a photo and returns its tags by name. Fujifilm maker notes win over
// generic EXIF tags of the same name; otherwise the first group exiftool lists wins.
func ReadTags(photo string) (map[string]any, error) {
	exe, err := RequireExiftool()
	if err != nil {
		return nil, err
	}
	if info, err := os.Stat(photo); err != nil || info.IsDir() {
		return nil, errs.New(errs.General, fmt.Sprintf("%s does not exist", photo), "")
	}
	cmd := exec.Command(exe, "-j", "-u", "-G1", photo)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			return nil, errs.Wrap(errs.General, err, fmt.Sprintf("exiftool could not read %s: %v", photo, err), "")
		}
		return nil, errs.Wrap(errs.General, err,
			fmt.Sprintf("exiftool could not read %s: %s", photo, strings.TrimSpace(stderr.String())), "")
	}
	return parseTags(stdout.Bytes())
}

// parseTags flattens exiftool's `-j -G1` output for the first file, keeping its key order so the
// first group listed wins a tie, as in the Python version.
func parseTags(out []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(out))
	fail := func(err error) (map[string]any, error) {
		return nil, errs.Wrap(errs.General, err, fmt.Sprintf("exiftool output could not be read: %v", err), "")
	}
	for _, want := range []json.Delim{'[', '{'} {
		tok, err := dec.Token()
		if err != nil {
			return fail(err)
		}
		if tok != want {
			return fail(fmt.Errorf("expected %v, got %v", want, tok))
		}
	}
	flat := map[string]any{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return fail(err)
		}
		key, _ := tok.(string)
		var value any
		if err := dec.Decode(&value); err != nil {
			return fail(err)
		}
		if s, ok := value.(string); ok {
			value = strings.TrimSpace(s)
		}
		group, name, found := strings.Cut(key, ":")
		if !found {
			group, name = "", key
		}
		if _, seen := flat[name]; group == "FujiFilm" || !seen { // maker notes win over generic EXIF
			flat[name] = value
		}
	}
	return flat, nil
}

// Mismatch is one tag whose value differs from what the recipe expects.
type Mismatch struct {
	Tag      string `json:"tag"`
	Expected any    `json:"expected"`
	Found    any    `json:"found"`
}

// Result is the outcome of comparing one photo with a recipe.
type Result struct {
	Photo      string     `json:"photo"`
	Checked    int        `json:"checked"`
	Mismatches []Mismatch `json:"mismatches"`
}

// Map is the result as an ordered JSON object (photo, checked, mismatches).
func (r Result) Map() *omap.Map {
	list := make([]any, len(r.Mismatches))
	for i, m := range r.Mismatches {
		list[i] = omap.New().Set("tag", m.Tag).Set("expected", m.Expected).Set("found", m.Found)
	}
	return omap.New().Set("photo", r.Photo).Set("checked", r.Checked).Set("mismatches", list)
}

// readTags is ReadTags, replaceable in tests.
var readTags = ReadTags

// Compare checks one photo's maker notes against a recipe.
func Compare(recipe map[string]any, photo string) (Result, error) {
	tags, err := readTags(photo)
	if err != nil {
		return Result{}, err
	}
	_, film := tags["FilmMode"]
	_, sat := tags["Saturation"]
	if !film && !sat {
		return Result{}, errs.New(errs.General, filepath.Base(photo)+" carries no Fujifilm recipe metadata",
			"It may have been edited or exported by software that strips maker notes.")
	}
	wanted, err := ExpectedTags(recipe)
	if err != nil {
		return Result{}, err
	}
	res := Result{Photo: photo, Checked: wanted.Len(), Mismatches: []Mismatch{}}
	for _, key := range wanted.Keys() {
		want, _ := wanted.Get(key)
		names := strings.Split(strings.TrimRight(key, "#"), "|")
		var found any
		for _, n := range names {
			if v, ok := tags[n]; ok {
				found = v
				break
			}
		}
		var ok bool
		if strings.HasSuffix(key, "#") {
			got, isNum := leadingNumber(found)
			w, _ := toFloat(want)
			ok = found != nil && isNum && got == w
		} else {
			ok = pyStr(found) == pyStr(want)
		}
		if !ok {
			res.Mismatches = append(res.Mismatches, Mismatch{Tag: names[0], Expected: want, Found: found})
		}
	}
	return res, nil
}
