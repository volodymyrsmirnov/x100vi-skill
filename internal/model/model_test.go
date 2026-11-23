package model

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/volodymyrsmirnov/x100vi-skill/internal/errs"
	"github.com/volodymyrsmirnov/x100vi-skill/internal/omap"
)

// recipe decodes JSON the way recipe files are read, so numbers are float64.
func recipe(t *testing.T, text string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(text), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func wire(t *testing.T, text string, partial bool) map[string]int {
	t.Helper()
	writes, err := EncodeRecipe(recipe(t, text), partial)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]int{}
	for _, w := range writes {
		out[w.Field.Key] = w.Wire
	}
	return out
}

func keys(t *testing.T, text string) []string {
	t.Helper()
	writes, err := EncodeRecipe(recipe(t, text), false)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, w := range writes {
		out = append(out, w.Field.Key)
	}
	return out
}

func index(list []string, s string) int {
	for i, x := range list {
		if x == s {
			return i
		}
	}
	return -1
}

func TestEmptyRecipeIsTheFactoryLook(t *testing.T) {
	w := wire(t, `{}`, false)
	if w["film_simulation"] != 1 || w["dynamic_range"] != 100 || w["grain"] != 1 || w["white_balance.mode"] != 2 {
		t.Errorf("%v", w)
	}
	if w["noise_reduction"] != 0x2000 || w["color_chrome_effect"] != 1 {
		t.Errorf("%v", w)
	}
	if _, ok := w["white_balance.kelvin"]; ok {
		t.Errorf("kelvin written")
	}
	if _, ok := w["monochromatic.warm_cool"]; ok {
		t.Errorf("warm_cool written")
	}
}

func TestSignedAndScaledValues(t *testing.T) {
	w := wire(t, `{"highlight": -1.5, "shadow": 3.5, "color": -4, "clarity": 5,
		"white_balance": {"shift_r": -9, "shift_b": 9}}`, false)
	want := map[string]int{"highlight": 0x10000 - 15, "shadow": 35, "color": 0x10000 - 40, "clarity": 50,
		"white_balance.shift_r": 0x10000 - 9, "white_balance.shift_b": 9}
	for k, v := range want {
		if w[k] != v {
			t.Errorf("%s: %d, want %d", k, w[k], v)
		}
	}
}

func equalValue(a, b any) bool {
	if g, ok := a.(*omap.Map); ok {
		a = g.ToPlain()
	}
	if g, ok := b.(*omap.Map); ok {
		b = g.ToPlain()
	}
	fa, oka := toFloat(a)
	fb, okb := toFloat(b)
	if oka && okb {
		return fa == fb
	}
	return fmt.Sprint(a) == fmt.Sprint(b)
}

func TestEveryValueRoundTrips(t *testing.T) {
	for _, f := range Fields {
		t.Run(f.Key, func(t *testing.T) {
			var values []any
			switch f.Kind {
			case Enum:
				for _, n := range f.Table.Names {
					values = append(values, n)
				}
			case NR:
				for n := -4; n <= 4; n++ {
					values = append(values, float64(n))
				}
			case Grain:
				values = []any{map[string]any{"strength": "weak", "size": "large"},
					map[string]any{"strength": "strong", "size": "small"}}
			default:
				steps := int((f.Hi-f.Lo)/f.Step + 0.5)
				for i := 0; i <= steps; i++ {
					values = append(values, f.Lo+float64(i)*f.Step)
				}
			}
			if len(values) == 0 {
				t.Fatal("no values")
			}
			for _, v := range values {
				code, err := EncodeField(f, v)
				if err != nil {
					t.Fatalf("%v: %v", v, err)
				}
				if back := DecodeField(f, code); !equalValue(back, v) {
					t.Errorf("%v -> 0x%04X -> %v", v, code, back)
				}
			}
		})
	}
}

func TestNoiseReductionCodesAreTheCamerasIrregularTable(t *testing.T) {
	want := []int{0x8000, 0x7000, 0x4000, 0x3000, 0x2000, 0x1000, 0x0000, 0x6000, 0x5000}
	for i, n := 0, -4; n <= 4; i, n = i+1, n+1 {
		if NoiseReduction[n] != want[i] {
			t.Errorf("%d: 0x%04X", n, NoiseReduction[n])
		}
	}
}

func TestGrainOffIsWrittenAs1AndReadAs6Or7(t *testing.T) {
	g := ByKey["grain"]
	if code, _ := EncodeField(g, map[string]any{"strength": "off", "size": "large"}); code != 1 {
		t.Errorf("off/large written as %d", code)
	}
	if v := DecodeField(g, 7).(*omap.Map); !equalValue(v, map[string]any{"strength": "off", "size": "large"}) {
		t.Errorf("7 -> %v", v.ToPlain())
	}
	if v := DecodeField(g, 6).(*omap.Map); !equalValue(v, map[string]any{"strength": "off", "size": "small"}) {
		t.Errorf("6 -> %v", v.ToPlain())
	}
}

func TestRejectsWhatTheCameraCannotHold(t *testing.T) {
	cases := []struct{ bad, fragment string }{
		{`{"highlight": 4.5}`, "out of range"},
		{`{"highlight": 0.25}`, "grid"},
		{`{"color": 5}`, "out of range"},
		{`{"clarity": "high"}`, "not a number"},
		{`{"noise_reduction": true}`, "not valid"},
		{`{"dynamic_range": 800}`, "not valid"},
		{`{"white_balance": {"shift_r": 10}}`, "out of range"},
		{`{"white_balance": {"mode": "kelvin"}}`, "no kelvin value"},
		{`{"white_balance": {"mode": "auto", "kelvin": 5000}}`, "only used when"},
		{`{"white_balance": {"mode": "kelvin", "kelvin": 5555}}`, "grid"},
		{`{"white_balance": {"tint": 3}}`, "unknown setting"},
		{`{"monochromatic": {"warm_cool": 3}}`, "monochrome or acros"},
		{`{"film_simulation": "monochrome", "color": 2}`, "does not apply"},
		{`{"image_quality": "raw", "image_size": "l_3:2"}`, "cannot be set while"},
		{`{"image_quality": "RAW", "image_size": "l_3:2"}`, "cannot be set while"},
		{`{"image_quality": "Raw", "image_size": "unknown:0x0004"}`, "cannot be set while"},
		// RAW-only is the wire value 1, however the recipe spells it
		{`{"image_quality": "unknown:0x0001", "image_size": "l_3:2"}`, "cannot be set while"},
		{`{"image_quality": "UNKNOWN:0x1", "image_size": "l_3:2"}`, "cannot be set while"},
		{`{"image_size": "unknown:0x10000"}`, "0xFFFF at most"},
		{`{"film_simulation": "unknown:0xFFFFFFFF"}`, "0xFFFF at most"},
		{`{"film_simulation": "unknown:0x-1"}`, "not a valid code"},
		{`{"film_simulation": "unknown:0x"}`, "not a valid code"},
		{`{"noise_reduction": "unknown:0x10000"}`, "0xFFFF at most"},
		{`{"noise_reduction": "unknown:0x+1"}`, "not a valid code"},
		{`{"name": "` + strings.Repeat("x", 26) + `"}`, "25"},
		{`{"name": "CAFÉ"}`, "ASCII"},
		{`{"grain": "strong"}`, "must be an object"},
		{`{"grain": {"strength": "medium"}}`, "not valid"},
		{`{"iso": 400}`, "unknown setting"},
		// Python's ([], "JSON object") case cannot be expressed: EncodeRecipe takes a map.
	}
	for _, c := range cases {
		_, err := EncodeRecipe(recipe(t, c.bad), false)
		e, ok := errs.As(err)
		if !ok || e.Kind != errs.InvalidRecipe || !strings.Contains(e.Error(), c.fragment) {
			t.Errorf("%s: got %v, want an invalid-recipe error with %q", c.bad, err, c.fragment)
		}
	}
}

func TestTyposGetASuggestion(t *testing.T) {
	_, err := EncodeRecipe(recipe(t, `{"film_simulation": "clasic_chrome"}`), false)
	if err == nil || !strings.Contains(err.Error(), "classic_chrome") {
		t.Errorf("value typo: %v", err)
	}
	_, err = EncodeRecipe(recipe(t, `{"film_simulaton": "velvia"}`), false)
	if e, ok := errs.As(err); !ok || !strings.Contains(e.Hint, "film_simulation") {
		t.Errorf("key typo: %v", err)
	}
}

func TestUnknownSettingExplainsWhatUSBCannotDo(t *testing.T) {
	_, err := EncodeRecipe(recipe(t, `{"iso": 400}`), false)
	if e, ok := errs.As(err); !ok || !strings.Contains(e.Hint, "ISO") {
		t.Errorf("%v", err)
	}
}

func TestValuesAreCaseInsensitive(t *testing.T) {
	w := wire(t, `{"film_simulation": "Classic_Chrome", "grain": {"strength": "Weak", "size": "LARGE"}}`, false)
	if w["grain"] != 4 || w["film_simulation"] != 11 {
		t.Errorf("%v", w)
	}
}

func TestMonoAndKelvinPreconditions(t *testing.T) {
	w := wire(t, `{"film_simulation": "acros", "monochromatic": {"warm_cool": -3, "magenta_green": 2},
		"white_balance": {"mode": "kelvin", "kelvin": 5500}}`, false)
	if w["monochromatic.warm_cool"] != 0x10000-30 || w["monochromatic.magenta_green"] != 20 {
		t.Errorf("%v", w)
	}
	if w["white_balance.kelvin"] != 5500 {
		t.Errorf("%v", w)
	}
	if w := wire(t, `{"film_simulation": "acros"}`, false); w["monochromatic.warm_cool"] != 0 {
		t.Errorf("warm_cool not defaulted")
	} else if _, ok := w["monochromatic.warm_cool"]; !ok {
		t.Errorf("warm_cool not written")
	}
	if _, ok := wire(t, `{"film_simulation": "sepia"}`, false)["color"]; ok {
		t.Errorf("color written for sepia")
	}
}

func TestWriteOrderRespectsTheCamerasLocks(t *testing.T) {
	k := keys(t, `{"film_simulation": "acros", "white_balance": {"mode": "kelvin", "kelvin": 3000, "shift_r": 2}}`)
	if index(k, "film_simulation") >= index(k, "monochromatic.warm_cool") {
		t.Errorf("%v", k)
	}
	if !(index(k, "white_balance.mode") < index(k, "white_balance.kelvin") &&
		index(k, "white_balance.kelvin") < index(k, "white_balance.shift_r")) {
		t.Errorf("%v", k)
	}
	writes, _ := EncodeRecipe(recipe(t, `{"d_range_priority": "strong"}`), false)
	first, last := writes[0], writes[len(writes)-1]
	if first.Field.Key != "d_range_priority" || first.Wire != 0 || last.Field.Key != "d_range_priority" || last.Wire != 2 {
		t.Errorf("first %v/%d, last %v/%d", first.Field.Key, first.Wire, last.Field.Key, last.Wire)
	}
	k = keys(t, `{"image_quality": "fine", "image_size": "l_3:2"}`)
	if index(k, "image_quality") >= index(k, "image_size") {
		t.Errorf("%v", k)
	}
}

func TestPartialTouchesOnlyWhatIsGiven(t *testing.T) {
	w := wire(t, `{"film_simulation": "reala_ace"}`, true)
	if len(w) != 1 || w["film_simulation"] != 20 {
		t.Errorf("%v", w)
	}
}

func TestHousekeepingSettingsAreNeverDefaulted(t *testing.T) {
	w := wire(t, `{}`, false)
	for _, k := range []string{"image_quality", "image_size", "lens_modulation_optimizer", "color_space"} {
		if _, ok := w[k]; ok {
			t.Errorf("%s written", k)
		}
	}
}

func TestUnknownCodesRoundTrip(t *testing.T) {
	f := ByKey["image_size"]
	if v := DecodeField(f, 0x000B); v != "unknown:0x000B" {
		t.Errorf("%v", v)
	}
	if code, err := EncodeField(f, "unknown:0x000B"); err != nil || code != 0x000B {
		t.Errorf("%d %v", code, err)
	}
	if code, err := EncodeField(f, "UNKNOWN:0xFFFF"); err != nil || code != 0xFFFF {
		t.Errorf("%d %v", code, err)
	}
	if code, err := EncodeField(ByKey["noise_reduction"], "unknown:0x9000"); err != nil || code != 0x9000 {
		t.Errorf("%d %v", code, err)
	}
}

func TestParseTarget(t *testing.T) {
	cases := []struct {
		text string
		slot int // 0 for live
	}{{"live", 0}, {"LIVE", 0}, {"c3", 3}, {"C7", 7}, {"1", 1}}
	for _, c := range cases {
		slot, err := ParseTarget(c.text)
		if err != nil {
			t.Errorf("%s: %v", c.text, err)
			continue
		}
		if (c.slot == 0) != (slot == nil) || (slot != nil && *slot != c.slot) {
			t.Errorf("%s: %v", c.text, slot)
		}
		if got := TargetLabel(slot); (c.slot == 0 && got != "live") || (c.slot != 0 && got != fmt.Sprintf("C%d", c.slot)) {
			t.Errorf("label %s", got)
		}
	}
}

func TestParseTargetRejects(t *testing.T) {
	for _, text := range []string{"c0", "c8", "8", "slot1", ""} {
		if _, err := ParseTarget(text); !errs.Is(err, errs.InvalidRecipe) {
			t.Errorf("%q: %v", text, err)
		}
	}
}

func TestIsBlank(t *testing.T) {
	cases := []struct {
		text  string
		blank bool
	}{
		{`{"name": "", "film_simulation": "provia"}`, true},
		{`{"name": "X"}`, false},
		{`{"name": "", "film_simulation": "velvia"}`, false},
		{`{"name": "", "white_balance": {"mode": "auto", "shift_r": 1, "shift_b": 0}}`, false},
	}
	for _, c := range cases {
		if got := IsBlank(recipe(t, c.text)); got != c.blank {
			t.Errorf("%s: %v", c.text, got)
		}
	}
}

func TestIgnoredByPriority(t *testing.T) {
	got := IgnoredByPriority(recipe(t, `{"d_range_priority": "weak", "highlight": 1, "dynamic_range": "200"}`))
	if strings.Join(got, ",") != "dynamic_range,highlight" {
		t.Errorf("%v", got)
	}
	if got := IgnoredByPriority(recipe(t, `{"highlight": 1}`)); len(got) != 0 {
		t.Errorf("%v", got)
	}
}

// A film given by its code is checked like the same film given by name: "unknown:0x000C" is Acros.
func TestFilmChecksUseTheEncodedCode(t *testing.T) {
	for _, film := range []string{"acros", "unknown:0x000C", "UNKNOWN:0x000c"} {
		_, err := EncodeRecipe(recipe(t, `{"film_simulation": "`+film+`", "color": 2}`), false)
		e, ok := errs.As(err)
		if !ok || e.Kind != errs.InvalidRecipe || !strings.Contains(e.Message, "color does not apply") {
			t.Errorf("%s with color 2: %v", film, err)
		}
		w := wire(t, `{"film_simulation": "`+film+`", "monochromatic": {"warm_cool": 3}}`, false)
		if w["monochromatic.warm_cool"] != 30 || w["monochromatic.magenta_green"] != 0 {
			t.Errorf("%s: tint %v", film, w)
		}
		if _, ok := w["color"]; ok {
			t.Errorf("%s: color written", film)
		}
	}
	// a colour film by code still refuses a tint
	if _, err := EncodeRecipe(recipe(t, `{"film_simulation": "unknown:0x0001", "monochromatic": {"warm_cool": 3}}`), false); err == nil {
		t.Error("tint accepted under provia given by code")
	}
}
