package library

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/volodymyrsmirnov/x100vi-skill/internal/errs"
	"github.com/volodymyrsmirnov/x100vi-skill/internal/model"
	"github.com/volodymyrsmirnov/x100vi-skill/internal/omap"
)

func decode(t *testing.T, text string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(text), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func isolate(t *testing.T) string {
	t.Helper()
	state := filepath.Join(t.TempDir(), "state")
	t.Setenv("X100VI_HOME", state)
	return state
}

func TestStarterRecipesAreValidAndDescribed(t *testing.T) {
	names := StarterNames()
	if len(names) < 8 || !sort.StringsAreSorted(names) {
		t.Fatalf("starter names: %v", names)
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			r, err := Starter(name)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := model.EncodeRecipe(r, false); err != nil {
				t.Fatal(err)
			}
			n, _ := r["name"].(string)
			if n == "" || len(n) > model.NameMax {
				t.Errorf("name %q", n)
			}
			desc, _ := r["description"].(string)
			tags, _ := r["tags"].([]any)
			notes, _ := r["notes"].(string)
			if len(desc) <= 20 || len(tags) == 0 || notes == "" {
				t.Errorf("description/tags/notes missing: %q %v %q", desc, tags, notes)
			}
			if p, ok := r["d_range_priority"]; ok && p == "auto" { // so every starter can be previewed
				t.Errorf("d_range_priority auto")
			}
		})
	}
}

func TestStarterKeepsItsOrderAndBytes(t *testing.T) {
	raw, err := StarterJSON("desert-gold")
	if err != nil {
		t.Fatal(err)
	}
	ordered, err := StarterOrdered("desert-gold")
	if err != nil {
		t.Fatal(err)
	}
	keys := ordered.Keys()
	if keys[0] != "$schema" || keys[len(keys)-1] != "noise_reduction" {
		t.Errorf("order lost: %v", keys)
	}
	if v, _ := ordered.Get("film_simulation"); v != "classic_chrome" {
		t.Errorf("film_simulation %v", v)
	}
	out, err := omap.Indent(ordered)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(out)) != strings.TrimSpace(string(raw)) {
		t.Errorf("re-encoded starter differs from the file:\n%s\n---\n%s", out, raw)
	}
	_, err = Starter("nope")
	e, ok := errs.As(err)
	if !ok || e.Kind != errs.InvalidRecipe || !strings.Contains(e.Message, "'nope'") ||
		!strings.Contains(e.Hint, "desert-gold") {
		t.Errorf("unknown starter: %v", err)
	}
}

func setOf(list any) map[string]bool {
	out := map[string]bool{}
	switch x := list.(type) {
	case []any:
		for _, v := range x {
			out[v.(string)] = true
		}
	case []string:
		for _, v := range x {
			out[v] = true
		}
	case map[string]any:
		for k := range x {
			out[k] = true
		}
	}
	return out
}

func sameSet(t *testing.T, what string, got, want map[string]bool) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s: got %v, want %v", what, got, want)
		return
	}
	for k := range want {
		if !got[k] {
			t.Errorf("%s: got %v, want %v", what, got, want)
			return
		}
	}
}

func TestSchemaMatchesTheModel(t *testing.T) {
	props := Schema()["properties"].(map[string]any)
	// TOP_LEVEL = every field's top-level key + metadata.
	top := map[string]bool{"$schema": true, "name": true, "description": true, "tags": true, "notes": true, "source": true}
	nested := map[string]map[string]bool{"grain": {"strength": true, "size": true}}
	for _, f := range model.Fields {
		head, rest, dotted := strings.Cut(f.Key, ".")
		top[head] = true
		if dotted {
			if nested[head] == nil {
				nested[head] = map[string]bool{}
			}
			nested[head][rest] = true
		}
	}
	for k := range model.DefaultRecipe() {
		if !top[k] {
			t.Errorf("default key %s is not a field", k)
		}
	}
	sameSet(t, "top level", setOf(props), top)
	sameSet(t, "film_simulation", setOf(props["film_simulation"].(map[string]any)["enum"]), setOf(model.FilmSimulation.Names))
	wb := props["white_balance"].(map[string]any)["properties"].(map[string]any)
	sameSet(t, "white_balance.mode", setOf(wb["mode"].(map[string]any)["enum"]), setOf(model.WhiteBalance.Names))
	for _, key := range []string{"white_balance", "grain", "monochromatic"} {
		sameSet(t, key, setOf(props[key].(map[string]any)["properties"]), nested[key])
	}
	for _, key := range []string{"image_quality", "image_size", "lens_modulation_optimizer", "color_space"} {
		sameSet(t, key, setOf(props[key].(map[string]any)["enum"]), setOf(model.ByKey[key].Table.Names))
	}
	for _, key := range []string{"highlight", "shadow", "color", "sharpness", "clarity"} {
		f := model.ByKey[key]
		p := props[key].(map[string]any)
		if p["minimum"] != f.Lo || p["maximum"] != f.Hi {
			t.Errorf("%s: schema %v..%v, model %v..%v", key, p["minimum"], p["maximum"], f.Lo, f.Hi)
		}
	}
	if props["name"].(map[string]any)["maxLength"] != float64(model.NameMax) {
		t.Errorf("name maxLength")
	}
	if !strings.HasPrefix(Schema()["title"].(string), "Fujifilm X100VI") {
		t.Errorf("title")
	}
}

func TestLoadRecipe(t *testing.T) {
	dir := t.TempDir()
	write := func(name, text string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	r, err := LoadRecipe(write("good.json", `{"film_simulation": "velvia", "color": 2}`))
	if err != nil || r["film_simulation"] != "velvia" || r["color"] != 2.0 {
		t.Fatalf("good: %v %v", r, err)
	}
	r, err = LoadRecipe("everyday") // a starter name
	if err != nil || r["name"] == nil {
		t.Fatalf("starter: %v %v", r, err)
	}
	cases := []struct{ path, want, hint string }{
		{filepath.Join(dir, "missing.json"), "missing.json does not exist", "starter name"},
		{"no-such-starter", "recipe file no-such-starter does not exist", "x100vi recipes"},
		{write("bad.json", "{\n  \"color\": 2,\n}"), "bad.json is not valid JSON: ", ""},
		{write("list.json", `[1, 2]`), "list.json must contain one JSON object", ""},
	}
	for _, c := range cases {
		_, err := LoadRecipe(c.path)
		e, ok := errs.As(err)
		if !ok || e.Kind != errs.InvalidRecipe || !strings.Contains(e.Message, c.want) || !strings.Contains(e.Hint, c.hint) {
			t.Errorf("%s: %v", c.path, err)
		}
	}
	_, err = LoadRecipe(filepath.Join(dir, "bad.json"))
	if !strings.Contains(err.Error(), "at line 3, column 1") {
		t.Errorf("position: %v", err)
	}
}

func TestPosition(t *testing.T) {
	raw := []byte("{\n  \"a\": x\n}")
	var v any
	err := json.Unmarshal(raw, &v)
	syn := err.(*json.SyntaxError)
	if line, col := position(raw, syn.Offset); line != 2 || col != 8 {
		t.Errorf("line %d column %d", line, col)
	}
}

func TestAutoBackupsNeverOverwriteEachOther(t *testing.T) {
	state := isolate(t)
	a, err := SaveAutoBackup("C1", map[string]any{"name": "A"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := SaveAutoBackup("C1", map[string]any{"name": "B"})
	if err != nil {
		t.Fatal(err)
	}
	if a == b || filepath.Dir(a) != filepath.Join(state, "backups") {
		t.Fatalf("%s %s", a, b)
	}
	raw, _ := os.ReadFile(a)
	if string(raw) != "{\n  \"name\": \"A\"\n}\n" {
		t.Errorf("content %q", raw)
	}
	if !strings.HasSuffix(b, "-C1-2.json") {
		t.Errorf("second backup in the same second: %s", b)
	}
	d, err := SaveSettingsFile("camera-settings", []byte{1, 2, 3})
	if err != nil || !strings.HasSuffix(d, "-camera-settings.dat") {
		t.Fatalf("%s %v", d, err)
	}
	if raw, _ := os.ReadFile(d); string(raw) != "\x01\x02\x03" {
		t.Errorf("settings file %q", raw)
	}
}

func TestStateDirHonoursOverrides(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"XDG_STATE_HOME": filepath.Join(tmp, "xdg")}
	get := func(k string) string { return env[k] }
	if got := stateDir("linux", get); got != filepath.Join(tmp, "xdg", "x100vi") {
		t.Errorf("xdg: %s", got)
	}
	env["LOCALAPPDATA"] = filepath.Join(tmp, "local")
	if got := stateDir("windows", get); got != filepath.Join(tmp, "local", "x100vi") {
		t.Errorf("windows: %s", got)
	}
	if got := stateDir("linux", get); got != filepath.Join(tmp, "xdg", "x100vi") {
		t.Errorf("LOCALAPPDATA only counts on Windows: %s", got)
	}
	env["X100VI_HOME"] = filepath.Join(tmp, "home")
	if got := stateDir("windows", get); got != filepath.Join(tmp, "home") {
		t.Errorf("X100VI_HOME: %s", got)
	}
	delete(env, "X100VI_HOME")
	delete(env, "XDG_STATE_HOME")
	home, _ := os.UserHomeDir()
	if got := stateDir("darwin", get); got != filepath.Join(home, ".local", "state", "x100vi") {
		t.Errorf("default: %s", got)
	}
}

func TestSkillIsEmbeddedWithItsReferences(t *testing.T) {
	root := SkillFS()
	raw, err := fs.ReadFile(root, "SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if !strings.HasPrefix(text, "---\nname: x100vi\n") || !strings.Contains(text, "description:") {
		t.Errorf("front matter")
	}
	for _, ref := range []string{"recipe-design.md", "recipe-schema.md", "troubleshooting.md"} {
		if _, err := fs.Stat(root, "references/"+ref); err != nil {
			t.Error(err)
		}
		if !strings.Contains(text, "references/"+ref) {
			t.Errorf("SKILL.md does not link %s", ref)
		}
	}
	for _, launcher := range []string{"x100vi.sh", "x100vi.ps1"} {
		body, err := fs.ReadFile(root, "scripts/"+launcher)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(strings.ReplaceAll(text, "\\", "/"), "scripts/"+launcher) {
			t.Errorf("SKILL.md does not mention %s", launcher)
		}
		// both launchers fetch the tool from the same project
		if !strings.Contains(string(body), "github.com/volodymyrsmirnov/x100vi-skill") {
			t.Errorf("%s does not name the project", launcher)
		}
	}
}

func TestWriteSkill(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "skills", "x100vi")
	if err := WriteSkill(dst); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dst, "SKILL.md"))
	if err != nil || !strings.Contains(string(raw), "name: x100vi") {
		t.Fatalf("%v", err)
	}
	info, err := os.Stat(filepath.Join(dst, "scripts", "x100vi.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o100 == 0 && os.PathSeparator == '/' {
		t.Errorf("launcher is not executable: %v", info.Mode())
	}
	if err := WriteSkill(dst); err != nil { // reinstalling over our own copy works
		t.Fatal(err)
	}
}

func TestSummaryIsReadable(t *testing.T) {
	cases := []struct{ recipe, want string }{
		{`{"name": "GOLD", "film_simulation": "classic_chrome", "dynamic_range": "400", "clarity": 2,
		   "white_balance": {"mode": "daylight", "shift_r": 3, "shift_b": -5}}`,
			"classic_chrome, WB daylight R+3 B-5, DR400, clarity +2"},
		{`{}`, "provia, WB auto, DR100"},
		{`{"white_balance": {"mode": "kelvin", "kelvin": 5500, "shift_b": 2}, "d_range_priority": "strong",
		   "grain": {"strength": "weak"}, "color_chrome_effect": "strong", "smooth_skin": "weak",
		   "highlight": -1.5, "shadow": 0.5, "noise_reduction": -4, "sharpness": 0}`,
			"provia, WB 5500K R+0 B+2, DR priority strong, grain weak/small, CCE strong, skin weak, H -1.5, S +0.5, NR -4"},
		{`{"film_simulation": "acros_r", "monochromatic": {"warm_cool": 2}, "color_chrome_fx_blue": "weak",
		   "grain": {"strength": "strong", "size": "large"}, "dynamic_range": "auto"}`,
			"acros_r, WB auto, DRauto, grain strong/large, FX blue weak"},
	}
	for _, c := range cases {
		if got := Summarize(decode(t, c.recipe)); got != c.want {
			t.Errorf("got  %q\nwant %q", got, c.want)
		}
	}
	// a recipe read from the camera carries grain as an ordered map
	r := map[string]any{"grain": omap.New().Set("strength", "weak").Set("size", "large")}
	if got := Summarize(r); got != "provia, WB auto, DR100, grain weak/large" {
		t.Errorf("ordered grain: %q", got)
	}
}
