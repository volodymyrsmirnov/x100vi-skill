// Package model is the recipe model: what a recipe may contain and how each setting is encoded
// on the wire.
//
// Two targets share one encoding:
//   - "live"  - the current shooting settings
//   - C1..C7  - the custom-setting registers, addressed by writing the register number to 0xD18C
//
// Ranges were measured by having the camera validate values; every label was confirmed against the
// EXIF of photos shot with known recipes (see docs/protocol.md).
package model

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/volodymyrsmirnov/x100vi-skill/internal/errs"
	"github.com/volodymyrsmirnov/x100vi-skill/internal/omap"
)

const (
	SlotSelect = 0xD18C
	SlotName   = 0xD18D
	NameMax    = 25
)

// Slots are the custom-setting registers, C1..C7.
var Slots = []int{1, 2, 3, 4, 5, 6, 7}

// Table is an ordered name -> wire value map (order is the order shown to people).
type Table struct {
	Names []string
	Codes map[string]int
}

func table(pairs ...any) Table {
	t := Table{Codes: map[string]int{}}
	for i := 0; i < len(pairs); i += 2 {
		name := pairs[i].(string)
		t.Names = append(t.Names, name)
		t.Codes[name] = pairs[i+1].(int)
	}
	return t
}

// Name returns the name of a wire value.
func (t Table) Name(code int) (string, bool) {
	for _, n := range t.Names {
		if t.Codes[n] == code {
			return n, true
		}
	}
	return "", false
}

var (
	FilmSimulation = table("provia", 1, "velvia", 2, "astia", 3, "pro_neg_hi", 4, "pro_neg_std", 5,
		"monochrome", 6, "monochrome_ye", 7, "monochrome_r", 8, "monochrome_g", 9, "sepia", 10,
		"classic_chrome", 11, "acros", 12, "acros_ye", 13, "acros_r", 14, "acros_g", 15,
		"eterna", 16, "classic_neg", 17, "eterna_bleach_bypass", 18, "nostalgic_neg", 19, "reala_ace", 20)
	OffWeakStrong  = table("off", 1, "weak", 2, "strong", 3)
	DynamicRange   = table("auto", 0xFFFF, "100", 100, "200", 200, "400", 400)
	DRangePriority = table("off", 0, "weak", 1, "strong", 2, "auto", 0x8000)
	WhiteBalance   = table("auto", 0x0002, "daylight", 0x0004, "incandescent", 0x0006, "underwater", 0x0008,
		"fluorescent_1", 0x8001, "fluorescent_2", 0x8002, "fluorescent_3", 0x8003, "shade", 0x8006,
		"kelvin", 0x8007, "custom_1", 0x8008, "custom_2", 0x8009, "custom_3", 0x800A,
		"auto_white_priority", 0x8020, "auto_ambience_priority", 0x8021)
	ImageQuality = table("raw", 1, "fine", 2, "normal", 3, "fine_raw", 4, "normal_raw", 5)
	ImageSize    = table("s_3:2", 1, "s_16:9", 2, "s_1:1", 3, "m_3:2", 4, "m_16:9", 5, "m_1:1", 6,
		"l_3:2", 7, "l_16:9", 8, "l_1:1", 9, "s_4:3", 10, "s_5:4", 12, "l_4:3", 14, "l_5:4", 16,
		"m_4:3", 18, "m_5:4", 20)
	OnOff      = table("on", 1, "off", 2)
	ColorSpace = table("srgb", 1, "adobe_rgb", 2)
)

// NoiseReduction maps the recipe value -4..+4 to the camera's irregular codes.
var NoiseReduction = map[int]int{-4: 0x8000, -3: 0x7000, -2: 0x4000, -1: 0x3000, 0: 0x2000, 1: 0x1000, 2: 0x0000,
	3: 0x6000, 4: 0x5000}

// MonoSims are the only film simulations that accept monochromatic colour.
var MonoSims = map[int]bool{6: true, 7: true, 8: true, 9: true, 12: true, 13: true, 14: true, 15: true}

// NoColourSims reject any write to `color`.
var NoColourSims = map[int]bool{6: true, 7: true, 8: true, 9: true, 10: true, 12: true, 13: true, 14: true, 15: true}

// Grain is one property: written 1..5, read back 2..7 (Off keeps the last size).
type GrainPair struct{ Strength, Size string }

var GrainWrite = map[GrainPair]int{{"off", "small"}: 1, {"off", "large"}: 1, {"weak", "small"}: 2,
	{"strong", "small"}: 3, {"weak", "large"}: 4, {"strong", "large"}: 5}
var GrainRead = map[int]GrainPair{2: {"weak", "small"}, 3: {"strong", "small"}, 4: {"weak", "large"},
	5: {"strong", "large"}, 6: {"off", "small"}, 7: {"off", "large"}}

// Kind of a field's encoding.
const (
	Enum   = "enum"
	Scaled = "scaled"
	Int    = "int"
	Grain  = "grain"
	NR     = "noise_reduction"
)

// Field is one recipe setting and where it lives on the camera.
type Field struct {
	Key   string // dotted path in the recipe JSON
	Live  uint16 // property code for the current shooting settings
	Slot  uint16 // property code inside a custom register
	Kind  string
	Table Table
	Scale int
	Lo    float64
	Hi    float64
	Step  float64
	Needs string // precondition enforced when encoding: mono | kelvin | colour | jpeg
}

// Fields in the order the camera must be written.
var Fields = []Field{
	{Key: "d_range_priority", Live: 0xD02E, Slot: 0xD191, Kind: Enum, Table: DRangePriority},
	{Key: "dynamic_range", Live: 0xD007, Slot: 0xD190, Kind: Enum, Table: DynamicRange},
	{Key: "film_simulation", Live: 0xD001, Slot: 0xD192, Kind: Enum, Table: FilmSimulation},
	{Key: "monochromatic.warm_cool", Live: 0xD104, Slot: 0xD193, Kind: Scaled, Scale: 10, Lo: -18, Hi: 18, Step: 1, Needs: "mono"},
	{Key: "monochromatic.magenta_green", Live: 0xD031, Slot: 0xD194, Kind: Scaled, Scale: 10, Lo: -18, Hi: 18, Step: 1, Needs: "mono"},
	{Key: "grain", Live: 0xD023, Slot: 0xD195, Kind: Grain},
	{Key: "color_chrome_effect", Live: 0xD029, Slot: 0xD196, Kind: Enum, Table: OffWeakStrong},
	{Key: "color_chrome_fx_blue", Live: 0xD030, Slot: 0xD197, Kind: Enum, Table: OffWeakStrong},
	{Key: "smooth_skin", Live: 0xD189, Slot: 0xD198, Kind: Enum, Table: OffWeakStrong},
	{Key: "white_balance.mode", Live: 0x5005, Slot: 0xD199, Kind: Enum, Table: WhiteBalance},
	{Key: "white_balance.kelvin", Live: 0xD017, Slot: 0xD19C, Kind: Int, Scale: 1, Lo: 2500, Hi: 10000, Step: 10, Needs: "kelvin"},
	{Key: "white_balance.shift_r", Live: 0xD00B, Slot: 0xD19A, Kind: Int, Scale: 1, Lo: -9, Hi: 9, Step: 1},
	{Key: "white_balance.shift_b", Live: 0xD00C, Slot: 0xD19B, Kind: Int, Scale: 1, Lo: -9, Hi: 9, Step: 1},
	{Key: "highlight", Live: 0xD320, Slot: 0xD19D, Kind: Scaled, Scale: 10, Lo: -2, Hi: 4, Step: 0.5},
	{Key: "shadow", Live: 0xD321, Slot: 0xD19E, Kind: Scaled, Scale: 10, Lo: -2, Hi: 4, Step: 0.5},
	{Key: "color", Live: 0xD008, Slot: 0xD19F, Kind: Scaled, Scale: 10, Lo: -4, Hi: 4, Step: 1, Needs: "colour"},
	{Key: "sharpness", Live: 0x5015, Slot: 0xD1A0, Kind: Scaled, Scale: 10, Lo: -4, Hi: 4, Step: 1},
	{Key: "noise_reduction", Live: 0xD01C, Slot: 0xD1A1, Kind: NR},
	{Key: "clarity", Live: 0xD032, Slot: 0xD1A2, Kind: Scaled, Scale: 10, Lo: -5, Hi: 5, Step: 1},
	// Housekeeping rather than "look": never defaulted, written only when present.
	{Key: "image_quality", Live: 0xD018, Slot: 0xD18F, Kind: Enum, Table: ImageQuality},
	{Key: "image_size", Live: 0xD1A5, Slot: 0xD18E, Kind: Enum, Table: ImageSize, Needs: "jpeg"},
	{Key: "lens_modulation_optimizer", Live: 0xD34D, Slot: 0xD1A3, Kind: Enum, Table: OnOff},
	{Key: "color_space", Live: 0xD00A, Slot: 0xD1A4, Kind: Enum, Table: ColorSpace},
}

// ByKey finds a field by its recipe key.
var ByKey = func() map[string]Field {
	m := map[string]Field{}
	for _, f := range Fields {
		m[f.Key] = f
	}
	return m
}()

var optional = []string{"image_quality", "image_size", "lens_modulation_optimizer", "color_space"}
var metadata = []string{"$schema", "name", "description", "tags", "notes", "source"}

// DefaultRecipe is the factory look; a full (non-partial) write starts from it.
func DefaultRecipe() map[string]any {
	return map[string]any{
		"name":                 "",
		"film_simulation":      "provia",
		"grain":                map[string]any{"strength": "off", "size": "small"},
		"color_chrome_effect":  "off",
		"color_chrome_fx_blue": "off",
		"smooth_skin":          "off",
		"white_balance":        map[string]any{"mode": "auto", "shift_r": 0.0, "shift_b": 0.0},
		"dynamic_range":        "100",
		"d_range_priority":     "off",
		"highlight":            0.0, "shadow": 0.0, "color": 0.0, "sharpness": 0.0, "noise_reduction": 0.0, "clarity": 0.0,
	}
}

var topLevel = func() map[string]bool {
	m := map[string]bool{"monochromatic": true}
	for k := range DefaultRecipe() {
		m[k] = true
	}
	for _, k := range optional {
		m[k] = true
	}
	for _, k := range metadata {
		m[k] = true
	}
	return m
}()

var nested = map[string][]string{
	"grain":         {"size", "strength"},
	"white_balance": {"kelvin", "mode", "shift_b", "shift_r"},
	"monochromatic": {"magenta_green", "warm_cool"},
}

// Get reads a dotted key from a recipe (map[string]any or *omap.Map); nil when absent.
func Get(recipe any, dotted string) any {
	cur := recipe
	for _, part := range strings.Split(dotted, ".") {
		switch m := cur.(type) {
		case map[string]any:
			v, ok := m[part]
			if !ok {
				return nil
			}
			cur = v
		case *omap.Map:
			v, ok := m.Get(part)
			if !ok {
				return nil
			}
			cur = v
		default:
			return nil
		}
	}
	return cur
}

// Put sets a dotted key in an ordered recipe, creating nested objects as needed.
func Put(recipe *omap.Map, dotted string, value any) {
	parts := strings.Split(dotted, ".")
	cur := recipe
	for _, part := range parts[:len(parts)-1] {
		next, ok := cur.Get(part)
		child, isMap := next.(*omap.Map)
		if !ok || !isMap {
			child = omap.New()
			cur.Set(part, child)
		}
		cur = child
	}
	cur.Set(parts[len(parts)-1], value)
}

// Signed16 reads a 16-bit wire value as two's complement.
func Signed16(v int) int {
	if v >= 0x8000 {
		return v - 0x10000
	}
	return v
}

// Number is a JSON number as an int when it is whole, for output.
func Number(v float64) any {
	if v == math.Trunc(v) && math.Abs(v) < 1e15 {
		return int(v)
	}
	return v
}

// ParseTarget turns "live" into nil and "c3" / "C3" / "3" into 3.
func ParseTarget(text string) (*int, error) {
	t := strings.ToLower(strings.TrimSpace(text))
	if t == "live" {
		return nil, nil
	}
	t = strings.TrimPrefix(t, "c")
	if len(t) == 1 && t[0] >= '1' && t[0] <= '7' {
		n := int(t[0] - '0')
		return &n, nil
	}
	return nil, errs.Recipe(fmt.Sprintf("unknown target %s", quote(text)), "Use live, or a custom setting C1 to C7.")
}

// TargetLabel is "live" or "C3".
func TargetLabel(slot *int) string {
	if slot == nil {
		return "live"
	}
	return fmt.Sprintf("C%d", *slot)
}

// quote renders a value the way Python's repr() would in messages.
func quote(v any) string {
	switch x := v.(type) {
	case string:
		return "'" + x + "'"
	case nil:
		return "None"
	case bool:
		if x {
			return "True"
		}
		return "False"
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64)
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, len(keys))
		for i, k := range keys {
			parts[i] = quote(k) + ": " + quote(x[k])
		}
		return "{" + strings.Join(parts, ", ") + "}"
	default:
		return fmt.Sprint(x)
	}
}

// Quote is quote() for other packages.
func Quote(v any) string { return quote(v) }

// fmtG formats a number like Python's {:g}.
func fmtG(v float64) string { return strconv.FormatFloat(v, 'g', -1, 64) }

// FmtG is fmtG for other packages.
func FmtG(v float64) string { return fmtG(v) }

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

// Suggest returns " Did you mean 'x'?" for the closest option, if any is close enough.
func Suggest(value string, options []string) string {
	best, bestScore := "", 0.0
	v := strings.ToLower(value)
	for _, o := range options {
		if s := similarity(v, o); s > bestScore {
			best, bestScore = o, s
		}
	}
	if bestScore >= 0.5 {
		return fmt.Sprintf(" Did you mean %s?", quote(best))
	}
	return ""
}

// similarity is 2*matches/(len a + len b) over the longest common subsequence, like difflib's ratio.
func similarity(a, b string) float64 {
	if len(a)+len(b) == 0 {
		return 1
	}
	prev := make([]int, len(b)+1)
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		for j := 1; j <= len(b); j++ {
			if a[i-1] == b[j-1] {
				cur[j] = prev[j-1] + 1
			} else if prev[j] >= cur[j-1] {
				cur[j] = prev[j]
			} else {
				cur[j] = cur[j-1]
			}
		}
		prev = cur
	}
	return 2 * float64(prev[len(b)]) / float64(len(a)+len(b))
}

// EncodeField turns a recipe value into a 16-bit wire value, or explains why the camera will not take it.
func EncodeField(f Field, value any) (int, error) {
	switch f.Kind {
	case Enum:
		var key string
		switch x := value.(type) {
		case string:
			key = strings.ToLower(x)
		case bool:
			key = quote(x)
		case float64:
			key = fmtG(x)
		default:
			key = fmt.Sprint(x)
		}
		if strings.HasPrefix(key, "unknown:0x") {
			return unknownCode(f, value, key[len("unknown:0x"):]) // round-trips a code this table has no name for
		}
		code, ok := f.Table.Codes[key]
		if !ok {
			return 0, errs.Recipe(fmt.Sprintf("%s: %s is not valid.%s", f.Key, quote(value), Suggest(key, f.Table.Names)),
				fmt.Sprintf("Allowed values: %s.", strings.Join(f.Table.Names, ", ")))
		}
		return code, nil
	case NR:
		if str, isStr := value.(string); isStr && strings.HasPrefix(strings.ToLower(str), "unknown:0x") {
			return unknownCode(f, value, str[len("unknown:0x"):])
		}
		n, ok := toFloat(value)
		_, isBool := value.(bool)
		if ok && !isBool && n == math.Trunc(n) {
			if code, ok := NoiseReduction[int(n)]; ok {
				return code, nil
			}
		}
		return 0, errs.Recipe(fmt.Sprintf("noise_reduction: %s is not valid.", quote(value)),
			"Allowed values: -4, -3, -2, -1, 0, 1, 2, 3, 4.")
	case Grain:
		m, ok := value.(map[string]any)
		if !ok || m["strength"] == nil {
			return 0, errs.Recipe(fmt.Sprintf("grain: %s is not valid.", quote(value)),
				`Use {"strength": "off|weak|strong", "size": "small|large"}.`)
		}
		size := "small"
		if s, ok := m["size"]; ok {
			size = strings.ToLower(fmt.Sprint(s)) // null is not a size
			if s == nil {
				size = "none"
			}
		}
		pair := GrainPair{strings.ToLower(fmt.Sprint(m["strength"])), size}
		code, ok := GrainWrite[pair]
		if !ok {
			return 0, errs.Recipe(fmt.Sprintf("grain: strength %s / size %s is not valid.", quote(m["strength"]), quote(m["size"])),
				"strength is off, weak or strong; size is small or large.")
		}
		return code, nil
	}
	n, ok := toFloat(value)
	if _, isBool := value.(bool); isBool || !ok {
		return 0, errs.Recipe(fmt.Sprintf("%s: %s is not a number.", f.Key, quote(value)),
			fmt.Sprintf("Use a number from %s to %s.", fmtG(f.Lo), fmtG(f.Hi)))
	}
	if n < f.Lo || n > f.Hi {
		return 0, errs.Recipe(fmt.Sprintf("%s: %s is out of range.", f.Key, fmtG(n)),
			fmt.Sprintf("The camera accepts %s to %s.", fmtG(f.Lo), fmtG(f.Hi)))
	}
	steps := (n - f.Lo) / f.Step
	if math.Abs(steps-math.Round(steps)) > 1e-9 {
		return 0, errs.Recipe(fmt.Sprintf("%s: %s is not on the camera's grid.", f.Key, fmtG(n)),
			fmt.Sprintf("Use steps of %s from %s to %s.", fmtG(f.Step), fmtG(f.Lo), fmtG(f.Hi)))
	}
	return int(math.Round(n*float64(f.Scale))) & 0xFFFF, nil
}

// unknownCode reads the hex digits of an "unknown:0x..." value: a 16-bit code a table has no name for.
func unknownCode(f Field, value any, digits string) (int, error) {
	n, err := strconv.ParseUint(digits, 16, 16)
	if errors.Is(err, strconv.ErrRange) {
		return 0, errs.Recipe(fmt.Sprintf("%s: %s is larger than any camera code (0xFFFF at most)", f.Key, quote(value)),
			"Use the value exactly as `x100vi get` printed it, or a named value.")
	}
	if err != nil {
		return 0, errs.Recipe(fmt.Sprintf("%s: %s is not a valid code", f.Key, quote(value)),
			"Write unknown codes as unknown:0x followed by hex digits, e.g. unknown:0x0012, exactly as `x100vi get` prints them.")
	}
	return int(n), nil
}

// DecodeField turns a wire value into the recipe value (string, number or grain object).
func DecodeField(f Field, wire int) any {
	switch f.Kind {
	case Enum:
		if name, ok := f.Table.Name(wire); ok {
			return name
		}
		return fmt.Sprintf("unknown:0x%04X", wire)
	case NR:
		for v, code := range NoiseReduction {
			if code == wire {
				return v
			}
		}
		return fmt.Sprintf("unknown:0x%04X", wire)
	case Grain:
		g := omap.New()
		if p, ok := GrainRead[wire]; ok {
			return g.Set("strength", p.Strength).Set("size", p.Size)
		}
		if wire == 1 {
			return g.Set("strength", "off").Set("size", "small")
		}
		return g.Set("strength", fmt.Sprintf("unknown:0x%04X", wire)).Set("size", "small")
	}
	return Number(float64(Signed16(wire)) / float64(f.Scale))
}

func checkShape(recipe map[string]any) error {
	var unknown []string
	for k := range recipe {
		if !topLevel[k] {
			unknown = append(unknown, k)
		}
	}
	sort.Strings(unknown)
	if len(unknown) > 0 {
		names := make([]string, 0, len(topLevel))
		for k := range topLevel {
			names = append(names, k)
		}
		sort.Strings(names)
		var hints []string
		for _, k := range unknown {
			if s := Suggest(k, names); s != "" {
				hints = append(hints, quote(k)+":"+s)
			}
		}
		hint := "ISO, exposure compensation, focus and drive settings cannot be set over USB."
		if len(hints) > 0 {
			hint = strings.Join(hints, " ") + " " + hint
		}
		return errs.Recipe("unknown setting(s): "+strings.Join(unknown, ", "), hint)
	}
	for _, key := range []string{"grain", "white_balance", "monochromatic"} {
		allowed := nested[key]
		v, ok := recipe[key]
		if !ok {
			continue
		}
		m, isMap := v.(map[string]any)
		if !isMap {
			return errs.Recipe(fmt.Sprintf("%s must be an object with %s", key, strings.Join(allowed, ", ")), "")
		}
		var extra []string
		for k := range m {
			if !contains(allowed, k) {
				extra = append(extra, k)
			}
		}
		sort.Strings(extra)
		if len(extra) > 0 {
			return errs.Recipe(fmt.Sprintf("unknown setting(s) in %s: %s", key, strings.Join(extra, ", ")),
				fmt.Sprintf("Allowed: %s.", strings.Join(allowed, ", ")))
		}
	}
	name, ok := recipe["name"]
	if !ok {
		return nil
	}
	s, isStr := name.(string)
	if !isStr {
		return errs.Recipe("name must be text", "")
	}
	for _, r := range s {
		if r < 32 || r >= 127 {
			return errs.Recipe("name may only contain plain ASCII letters, digits, spaces and punctuation", "")
		}
	}
	if len(s) > NameMax {
		return errs.Recipe(fmt.Sprintf("name is %d characters; the camera allows %d", len(s), NameMax),
			fmt.Sprintf("Shorten it, e.g. %s.", quote(s[:NameMax])))
	}
	return nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func copyMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		if child, ok := v.(map[string]any); ok {
			out[k] = copyMap(child)
		} else {
			out[k] = v
		}
	}
	return out
}

// Plain returns the recipe with any ordered (*omap.Map) objects inside it turned into plain maps, so a
// recipe read from the camera can be validated, merged and compared like one read from a file.
func Plain(recipe map[string]any) map[string]any {
	out := make(map[string]any, len(recipe))
	for k, v := range recipe {
		switch x := v.(type) {
		case *omap.Map:
			out[k] = x.ToPlain()
		case map[string]any:
			out[k] = Plain(x)
		default:
			out[k] = v
		}
	}
	return out
}

// Merged is the recipe with factory defaults filled in (unless partial).
func Merged(recipe map[string]any, partial bool) map[string]any {
	recipe = Plain(recipe)
	out := map[string]any{}
	if !partial {
		out = DefaultRecipe()
	}
	for k, v := range recipe {
		child, isMap := v.(map[string]any)
		base, baseIsMap := out[k].(map[string]any)
		switch {
		case isMap && baseIsMap:
			for ck, cv := range child {
				base[ck] = cv
			}
		case isMap:
			out[k] = copyMap(child)
		default:
			out[k] = v
		}
	}
	return out
}

// Write is one property write: the field and its wire value.
type Write struct {
	Field Field
	Wire  int
}

// filmCode is the film simulation's encoded wire value, so the mono / no-colour checks treat a code
// spelled "unknown:0x000C" exactly like "acros". ok is false when there is no film or it does not encode.
func filmCode(full map[string]any) (int, bool) {
	v := Get(full, "film_simulation")
	if v == nil {
		return 0, false
	}
	code, err := EncodeField(ByKey["film_simulation"], v)
	return code, err == nil
}

// EncodeRecipe validates a recipe and returns the ordered property writes for it.
//
// Without partial every missing look setting takes its factory default, so a recipe file always
// produces the same look. With partial only the given settings are written.
func EncodeRecipe(recipe map[string]any, partial bool) ([]Write, error) {
	recipe = Plain(recipe)
	if err := checkShape(recipe); err != nil {
		return nil, err
	}
	full := Merged(recipe, partial)
	film, _ := filmCode(full)
	wb, _ := Get(full, "white_balance.mode").(string)
	// RAW-only is decided on the encoded value, so "unknown:0x0001" counts as raw too.
	rawOnly := false
	if q := Get(full, "image_quality"); q != nil {
		if wire, err := EncodeField(ByKey["image_quality"], q); err == nil && wire == ImageQuality.Codes["raw"] {
			rawOnly = true
		}
	}
	var writes []Write
	for _, f := range Fields {
		value := Get(full, f.Key)
		if f.Needs == "colour" && NoColourSims[film] {
			given := Get(recipe, f.Key)
			if n, ok := toFloat(given); given != nil && !(ok && n == 0) {
				return nil, errs.Recipe("color does not apply to monochrome, acros or sepia film simulations",
					"Remove `color`, or use monochromatic.warm_cool / magenta_green to tint.")
			}
			continue
		}
		if value == nil && f.Needs == "mono" && !partial && MonoSims[film] {
			value = 0.0
		}
		if value == nil {
			continue
		}
		if f.Needs == "mono" && !MonoSims[film] {
			return nil, errs.Recipe(f.Key+" needs a monochrome or acros film simulation in the same recipe",
				"Pick monochrome* or acros*, or remove `monochromatic`.")
		}
		if f.Needs == "kelvin" && wb != "kelvin" {
			return nil, errs.Recipe("white_balance.kelvin is only used when white_balance.mode is kelvin",
				`Set "mode": "kelvin", or remove `+"`kelvin`.")
		}
		if f.Needs == "jpeg" && rawOnly {
			return nil, errs.Recipe("image_size cannot be set while image_quality is raw",
				"Use fine, normal, fine_raw or normal_raw, or remove image_size.")
		}
		wire, err := EncodeField(f, value)
		if err != nil {
			return nil, err
		}
		writes = append(writes, Write{f, wire})
	}
	if wb == "kelvin" && Get(full, "white_balance.kelvin") == nil {
		return nil, errs.Recipe("white_balance.mode is kelvin but no kelvin value is given",
			`Add "kelvin": 2500..10000 (steps of 10).`)
	}
	// D-range priority locks dynamic range and tone: clear it first, set the real value last.
	for i, w := range writes {
		if w.Field.Key == "d_range_priority" && w.Wire != 0 {
			rest := append(append([]Write{}, writes[:i]...), writes[i+1:]...)
			writes = append(append([]Write{{w.Field, 0}}, rest...), w)
			break
		}
	}
	return writes, nil
}

// IgnoredByPriority lists settings a recipe asks for that D-range priority makes the camera ignore.
func IgnoredByPriority(recipe map[string]any) []string {
	p := Get(recipe, "d_range_priority")
	if p == nil || p == "off" {
		return []string{}
	}
	out := []string{}
	for _, k := range []string{"dynamic_range", "highlight", "shadow"} {
		if _, ok := recipe[k]; ok {
			out = append(out, k)
		}
	}
	return out
}

// IsBlank is true when a register still holds the factory look and no name.
func IsBlank(recipe map[string]any) bool {
	recipe = Plain(recipe)
	if name, _ := recipe["name"].(string); name != "" {
		return false
	}
	for key, def := range DefaultRecipe() {
		if key == "name" {
			continue
		}
		value, ok := recipe[key]
		if !ok {
			value = def
		}
		if dm, isMap := def.(map[string]any); isMap {
			vm, _ := value.(map[string]any)
			for k, dv := range dm {
				v, ok := vm[k]
				if !ok {
					v = dv
				}
				if !same(v, dv) {
					return false
				}
			}
		} else if !same(value, def) {
			return false
		}
	}
	return true
}

func same(a, b any) bool {
	fa, oka := toFloat(a)
	fb, okb := toFloat(b)
	if oka && okb {
		return fa == fb
	}
	return fmt.Sprint(a) == fmt.Sprint(b)
}
