// Package library holds the bundled starter recipes, the JSON schema, local state (automatic
// backups) and the embedded agent skill.
package library

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	x100viskill "github.com/volodymyrsmirnov/x100vi-skill"
	"github.com/volodymyrsmirnov/x100vi-skill/internal/errs"
	"github.com/volodymyrsmirnov/x100vi-skill/internal/model"
	"github.com/volodymyrsmirnov/x100vi-skill/internal/omap"
)

const (
	recipesDir = "assets/recipes"
	schemaFile = "assets/recipe.schema.json"
	skillDir   = "skills/x100vi"
)

// LoadRecipe reads a recipe file, or a bundled starter recipe by name, with errors a person can act on.
func LoadRecipe(pathOrName string) (map[string]any, error) {
	info, err := os.Stat(pathOrName)
	if err != nil {
		if filepath.Ext(pathOrName) == "" && isStarter(pathOrName) {
			return Starter(pathOrName)
		}
		return nil, errs.Recipe(fmt.Sprintf("recipe file %s does not exist", pathOrName),
			"Give a path to a .json recipe, or a starter name from `x100vi recipes`.")
	}
	name := filepath.Base(pathOrName)
	if info.IsDir() {
		return nil, errs.Recipe(fmt.Sprintf("%s is a folder, not a recipe file", pathOrName),
			"Give a path to a .json recipe, or a starter name from `x100vi recipes`.")
	}
	raw, err := os.ReadFile(pathOrName)
	if err != nil {
		return nil, errs.Wrap(errs.InvalidRecipe, err, fmt.Sprintf("cannot read %s: %v", name, err), "")
	}
	return decodeRecipe(name, raw)
}

func decodeRecipe(name string, raw []byte) (map[string]any, error) {
	var data any
	if err := json.Unmarshal(raw, &data); err != nil {
		var syn *json.SyntaxError
		if errors.As(err, &syn) {
			line, col := position(raw, syn.Offset)
			return nil, errs.Wrap(errs.InvalidRecipe, err,
				fmt.Sprintf("%s is not valid JSON: %s at line %d, column %d", name, syn.Error(), line, col), "")
		}
		return nil, errs.Wrap(errs.InvalidRecipe, err, fmt.Sprintf("%s is not valid JSON: %v", name, err), "")
	}
	obj, ok := data.(map[string]any)
	if !ok {
		return nil, errs.Recipe(fmt.Sprintf("%s must contain one JSON object", name), "")
	}
	return obj, nil
}

// position turns a json.SyntaxError offset (bytes read before the error) into a 1-based line and column.
func position(raw []byte, offset int64) (line, col int) {
	pos := int(offset) - 1 // the offending byte
	if pos < 0 {
		pos = 0
	}
	if pos > len(raw) {
		pos = len(raw)
	}
	before := raw[:pos]
	line = bytes.Count(before, []byte("\n")) + 1
	col = pos - (bytes.LastIndexByte(before, '\n') + 1) + 1
	return line, col
}

// StarterNames lists the bundled starter recipes, sorted.
func StarterNames() []string {
	entries, err := fs.ReadDir(x100viskill.Assets, recipesDir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if n := e.Name(); strings.HasSuffix(n, ".json") && !e.IsDir() {
			names = append(names, strings.TrimSuffix(n, ".json"))
		}
	}
	sort.Strings(names)
	return names
}

func isStarter(name string) bool {
	for _, n := range StarterNames() {
		if n == name {
			return true
		}
	}
	return false
}

// StarterJSON is a starter recipe's file exactly as bundled (indented, keys in their written order).
func StarterJSON(name string) ([]byte, error) {
	if !isStarter(name) {
		return nil, errs.Recipe(fmt.Sprintf("no starter recipe called %s", model.Quote(name)),
			fmt.Sprintf("Available: %s.", strings.Join(StarterNames(), ", ")))
	}
	return fs.ReadFile(x100viskill.Assets, path.Join(recipesDir, name+".json"))
}

// Starter is a bundled starter recipe, decoded.
func Starter(name string) (map[string]any, error) {
	raw, err := StarterJSON(name)
	if err != nil {
		return nil, err
	}
	return decodeRecipe(name+".json", raw)
}

// StarterOrdered is a bundled starter recipe with its keys in the order they are written in the file,
// for printing.
func StarterOrdered(name string) (*omap.Map, error) {
	raw, err := StarterJSON(name)
	if err != nil {
		return nil, err
	}
	v, err := DecodeOrdered(raw)
	if err != nil {
		return nil, err
	}
	m, ok := v.(*omap.Map)
	if !ok {
		return nil, errs.Recipe(fmt.Sprintf("%s.json must contain one JSON object", name), "")
	}
	return m, nil
}

// SchemaJSON is the recipe JSON schema exactly as bundled.
func SchemaJSON() []byte {
	raw, err := fs.ReadFile(x100viskill.Assets, schemaFile)
	if err != nil {
		panic("x100vi: the recipe schema is missing from the binary: " + err.Error())
	}
	return raw
}

// Schema is the recipe JSON schema, decoded.
func Schema() map[string]any {
	var out map[string]any
	if err := json.Unmarshal(SchemaJSON(), &out); err != nil {
		panic("x100vi: the bundled recipe schema is not valid JSON: " + err.Error())
	}
	return out
}

// DecodeOrdered decodes JSON keeping object key order: objects become *omap.Map, arrays []any,
// numbers float64.
func DecodeOrdered(raw []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	v, err := decodeValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err == nil {
		return nil, fmt.Errorf("unexpected data after the JSON value")
	}
	return v, nil
}

func decodeValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			m := omap.New()
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				key, _ := kt.(string)
				v, err := decodeValue(dec)
				if err != nil {
					return nil, err
				}
				m.Set(key, v)
			}
			if _, err := dec.Token(); err != nil { // '}'
				return nil, err
			}
			return m, nil
		case '[':
			list := []any{}
			for dec.More() {
				v, err := decodeValue(dec)
				if err != nil {
					return nil, err
				}
				list = append(list, v)
			}
			if _, err := dec.Token(); err != nil { // ']'
				return nil, err
			}
			return list, nil
		}
		return nil, fmt.Errorf("unexpected %v", t)
	default:
		return tok, nil
	}
}

// StateDir is where automatic backups go: $X100VI_HOME if set; %LOCALAPPDATA%\x100vi on Windows;
// otherwise $XDG_STATE_HOME/x100vi, falling back to ~/.local/state/x100vi.
func StateDir() string {
	return stateDir(runtime.GOOS, os.Getenv)
}

func stateDir(goos string, getenv func(string) string) string {
	if root := getenv("X100VI_HOME"); root != "" {
		return root
	}
	if goos == "windows" && getenv("LOCALAPPDATA") != "" {
		return filepath.Join(getenv("LOCALAPPDATA"), "x100vi")
	}
	base := getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = "."
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "x100vi")
}

// BackupDir is the folder automatic backups are saved in.
func BackupDir() string { return filepath.Join(StateDir(), "backups") }

// newBackupFile creates a new, empty file named after the time and label; it never reuses a name.
func newBackupFile(label, suffix string) (*os.File, string, error) {
	folder := BackupDir()
	if err := os.MkdirAll(folder, 0o755); err != nil {
		return nil, "", errs.Wrap(errs.General, err, fmt.Sprintf("cannot create %s: %v", folder, err), "")
	}
	stamp := time.Now().Format("20060102-150405")
	for n := 1; ; n++ {
		name := fmt.Sprintf("%s-%s%s", stamp, label, suffix)
		if n > 1 {
			name = fmt.Sprintf("%s-%s-%d%s", stamp, label, n, suffix)
		}
		p := filepath.Join(folder, name)
		f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err == nil {
			return f, p, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return nil, "", errs.Wrap(errs.General, err, fmt.Sprintf("cannot write %s: %v", p, err), "")
		}
	}
}

func saveBackup(label, suffix string, data []byte) (string, error) {
	f, p, err := newBackupFile(label, suffix)
	if err != nil {
		return "", err
	}
	_, werr := f.Write(data)
	cerr := f.Close()
	if werr == nil {
		werr = cerr
	}
	if werr != nil {
		return "", errs.Wrap(errs.General, werr, fmt.Sprintf("cannot write %s: %v", p, werr), "")
	}
	return p, nil
}

// SaveAutoBackup writes content as indented JSON to a new file in the backups folder and returns its path.
func SaveAutoBackup(label string, content any) (string, error) {
	data, err := omap.Indent(content)
	if err != nil {
		return "", err
	}
	return saveBackup(label, ".json", append(data, '\n'))
}

// SaveSettingsFile keeps a copy of the camera's own settings file in the backups folder.
func SaveSettingsFile(label string, blob []byte) (string, error) {
	return saveBackup(label, ".dat", blob)
}

// SkillFS is the agent skill folder (SKILL.md, references/, scripts/) embedded in the binary.
func SkillFS() fs.FS {
	sub, err := fs.Sub(x100viskill.Skill, skillDir)
	if err != nil {
		panic("x100vi: the skill files are missing from the binary: " + err.Error())
	}
	return sub
}

// WriteSkill copies the embedded skill folder into dst (created if needed). Launcher scripts are
// made executable. dst must not hold anything else worth keeping: existing files are overwritten.
func WriteSkill(dst string) error {
	src := SkillFS()
	return fs.WalkDir(src, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		target := filepath.Join(dst, filepath.FromSlash(p))
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := fs.ReadFile(src, p)
		if err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if strings.HasSuffix(p, ".sh") {
			mode = 0o755
		}
		return os.WriteFile(target, data, mode)
	})
}

// Summarize is one line a person can scan: film simulation plus the settings that differ from default.
// Nested objects may be map[string]any or *omap.Map.
func Summarize(recipe map[string]any) string {
	full := model.Merged(plainMap(recipe), false)
	parts := []string{pyStr(full["film_simulation"])}
	wb, _ := full["white_balance"].(map[string]any)
	var wbText string
	if pyStr(wb["mode"]) == "kelvin" {
		wbText = pyStr(wb["kelvin"]) + "K"
	} else {
		wbText = pyStr(wb["mode"])
	}
	if truthy(wb["shift_r"]) || truthy(wb["shift_b"]) {
		wbText += fmt.Sprintf(" R%s B%s", signed(wb["shift_r"]), signed(wb["shift_b"]))
	}
	parts = append(parts, "WB "+wbText)
	if pyStr(full["d_range_priority"]) != "off" {
		parts = append(parts, "DR priority "+pyStr(full["d_range_priority"]))
	} else {
		parts = append(parts, "DR"+pyStr(full["dynamic_range"]))
	}
	grain, _ := full["grain"].(map[string]any)
	if pyStr(grain["strength"]) != "off" {
		size, ok := grain["size"]
		if !ok {
			size = "small"
		}
		parts = append(parts, fmt.Sprintf("grain %s/%s", pyStr(grain["strength"]), pyStr(size)))
	}
	for _, kl := range [][2]string{{"color_chrome_effect", "CCE"}, {"color_chrome_fx_blue", "FX blue"}, {"smooth_skin", "skin"}} {
		if v := pyStr(full[kl[0]]); v != "off" {
			parts = append(parts, kl[1]+" "+v)
		}
	}
	for _, kl := range [][2]string{{"highlight", "H"}, {"shadow", "S"}, {"color", "color"}, {"sharpness", "sharp"},
		{"noise_reduction", "NR"}, {"clarity", "clarity"}} {
		if truthy(full[kl[0]]) {
			parts = append(parts, kl[1]+" "+signed(full[kl[0]]))
		}
	}
	return strings.Join(parts, ", ")
}

// plainMap turns nested *omap.Map values into map[string]any so model.Merged can merge them.
func plainMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		switch x := v.(type) {
		case *omap.Map:
			out[k] = x.ToPlain()
		case map[string]any:
			out[k] = plainMap(x)
		default:
			out[k] = v
		}
	}
	return out
}

func number(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	}
	return 0, false
}

// truthy is Python truthiness for the scalar values a recipe holds.
func truthy(v any) bool {
	if n, ok := number(v); ok {
		return n != 0
	}
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	}
	return true
}

// signed renders a number like Python's {:+g}; missing values count as 0.
func signed(v any) string {
	if v == nil {
		return "+0"
	}
	n, ok := number(v)
	if !ok {
		return pyStr(v)
	}
	if n >= 0 {
		return "+" + model.FmtG(n)
	}
	return model.FmtG(n)
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
	if n, ok := number(v); ok {
		return model.FmtG(n)
	}
	return fmt.Sprint(v)
}
