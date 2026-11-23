// Package cli is the command line: `x100vi --help`.
//
// Every command prints plain text for people, or one JSON object with --json for agents. Errors go to
// stderr as `error:` plus a `hint:` line, with a stable exit code per kind.
package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/volodymyrsmirnov/x100vi-skill/internal/camera"
	"github.com/volodymyrsmirnov/x100vi-skill/internal/camera/settingsfile"
	"github.com/volodymyrsmirnov/x100vi-skill/internal/camlock"
	"github.com/volodymyrsmirnov/x100vi-skill/internal/errs"
	"github.com/volodymyrsmirnov/x100vi-skill/internal/exifcheck"
	"github.com/volodymyrsmirnov/x100vi-skill/internal/library"
	"github.com/volodymyrsmirnov/x100vi-skill/internal/model"
	"github.com/volodymyrsmirnov/x100vi-skill/internal/omap"
	"github.com/volodymyrsmirnov/x100vi-skill/internal/ptp"
	"github.com/volodymyrsmirnov/x100vi-skill/internal/rawconv"
	"github.com/volodymyrsmirnov/x100vi-skill/internal/sandbox"
	"github.com/volodymyrsmirnov/x100vi-skill/internal/sheet"
	"github.com/volodymyrsmirnov/x100vi-skill/internal/transport"
)

// Version is set at build time (-ldflags "-X main.version=...").
var Version = "dev"

const onBody = "ISO, exposure compensation, focus and drive settings are not reachable over USB; set them on the camera."
const privateNote = "Keep it private: the file also holds the camera's ID and network settings."

// Factory connects to a camera; tests pass a software camera.
type Factory func() (ptp.Transport, error)

// Out prints a result as text or as one JSON object.
type Out struct {
	JSON   bool
	Stdout io.Writer
}

// Emit prints the result (JSON mode) or the lines (text mode).
func (o *Out) Emit(result any, lines []string) error {
	if o.JSON {
		return o.Print(result)
	}
	_, err := fmt.Fprintln(o.Stdout, strings.Join(lines, "\n"))
	return err
}

// Print writes any value as indented JSON.
func (o *Out) Print(v any) error {
	b, err := omap.Indent(v)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(o.Stdout, string(b))
	return err
}

type env struct {
	out     *Out
	factory Factory
}

// camera connects, runs fn and disconnects, holding the cross-process camera lock throughout so another
// x100vi command cannot select a different register in the middle of this one.
func (e *env) camera(fn func(*camera.Camera) error) error {
	release, err := camlock.Acquire()
	if err != nil {
		return err
	}
	defer release()
	t, err := e.factory()
	if err != nil {
		return err
	}
	return camera.Use(t, fn)
}

func warnLines(warnings []string) []string {
	out := make([]string, 0, len(warnings))
	for _, w := range warnings {
		out = append(out, "warning: "+w)
	}
	return out
}

func prefixed(prefix string, items []string) []string {
	out := make([]string, 0, len(items))
	for _, x := range items {
		out = append(out, prefix+x)
	}
	return out
}

func strList(items []string) []any {
	out := make([]any, len(items))
	for i, s := range items {
		out[i] = s
	}
	return out
}

// RecipeAdvice lists things a recipe implies that a person should know before shooting with it.
func RecipeAdvice(recipe map[string]any) []string {
	full := model.Merged(recipe, true)
	var advice []string
	switch fmt.Sprint(full["dynamic_range"]) {
	case "400":
		advice = append(advice, "DR400 needs ISO 500 or higher; at lower ISO the camera falls back to a lower dynamic range.")
	case "200":
		advice = append(advice, "DR200 needs ISO 250 or higher; at lower ISO the camera uses DR100.")
	}
	if c, ok := full["clarity"].(float64); ok && c != 0 {
		advice = append(advice, "Clarity other than 0 adds about a second of processing after every shot.")
	}
	if ignored := model.IgnoredByPriority(recipe); len(ignored) > 0 {
		advice = append(advice, fmt.Sprintf("d_range_priority is on, so the camera ignores %s.", strings.Join(ignored, ", ")))
	}
	if mode, ok := model.Get(full, "white_balance.mode").(string); ok && strings.HasPrefix(mode, "custom_") {
		advice = append(advice, mode+" uses whatever custom white balance is stored on this camera.")
	}
	return advice
}

// -- commands ------------------------------------------------------------------------------------

func (e *env) doctor(a *Args) error {
	release, err := camlock.Acquire()
	if err != nil {
		return e.notReady(err)
	}
	defer release()
	t, err := e.factory()
	if err != nil {
		return e.notReady(err)
	}
	cam, err := camera.Open(t)
	if err != nil {
		return e.notReady(err)
	}
	defer cam.Close()
	info := cam.Info
	result := info.AsMap()
	mode := info.Mode
	if mode == "raw_conv" {
		mode = errs.RawConvMode
	}
	lines := []string{fmt.Sprintf("%s firmware %s", info.Model, info.Firmware), "USB mode: " + mode}
	if info.BatteryPercent != nil {
		lines = append(lines, fmt.Sprintf("battery: about %d%%", *info.BatteryPercent))
	}
	lines = append(lines, warnLines(info.Warnings)...)
	if err := cam.RequireReady(); err != nil {
		p, ok := errs.As(err)
		if !ok {
			return err
		}
		for _, k := range []string{"error", "message", "hint"} {
			if v, ok := p.AsMap().Get(k); ok {
				result.Set(k, v)
			}
		}
		lines = append(lines, "not ready: "+p.Message, "  "+p.Hint)
		if err := e.out.Emit(result, lines); err != nil {
			return err
		}
		return quiet{p.Kind.Exit}
	}
	lines = append(lines, "ready")
	return e.out.Emit(result, lines)
}

func (e *env) notReady(err error) error {
	p, ok := errs.As(err)
	if !ok {
		return err
	}
	result := omap.New().Set("ready", false)
	for _, k := range []string{"error", "message", "hint"} {
		if v, ok := p.AsMap().Get(k); ok {
			result.Set(k, v)
		}
	}
	lines := []string{"not ready: " + p.Message}
	if p.Hint != "" {
		lines = append(lines, "  "+p.Hint)
	}
	if err := e.out.Emit(result, lines); err != nil {
		return err
	}
	return quiet{p.Kind.Exit}
}

// quiet is an exit code whose explanation was already printed.
type quiet struct{ code int }

func (q quiet) Error() string { return fmt.Sprintf("exit %d", q.code) }

func (e *env) get(a *Args) error {
	target := a.Pos("target")
	all := strings.ToLower(strings.TrimSpace(target)) == "all"
	var slot *int
	if !all { // a bad target fails before connecting
		var err error
		if slot, err = model.ParseTarget(target); err != nil {
			return err
		}
	}
	var data any
	err := e.camera(func(cam *camera.Camera) error {
		if all {
			b, err := cam.Backup()
			data = b
			return err
		}
		r, err := cam.Read(slot)
		data = r
		return err
	})
	if err != nil {
		return err
	}
	text, err := omap.Indent(data)
	if err != nil {
		return err
	}
	if out := a.Str("output"); out != "" {
		if err := writeFile(out, append(text, '\n')); err != nil {
			return err
		}
		return e.out.Emit(omap.New().Set("saved", out).Set("target", target), []string{fmt.Sprintf("saved %s to %s", target, out)})
	}
	_, err = fmt.Fprintln(e.out.Stdout, string(text))
	return err
}

func writeFile(path string, data []byte) error {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(path, data, 0o644)
}

func (e *env) set(a *Args) error {
	slot, err := model.ParseTarget(a.Pos("target"))
	if err != nil {
		return err
	}
	label := model.TargetLabel(slot)
	recipe, err := library.LoadRecipe(a.Pos("recipe"))
	if err != nil {
		return err
	}
	writes, err := model.EncodeRecipe(recipe, a.Bool("partial")) // validate before touching the camera
	if err != nil {
		return err
	}
	advice := RecipeAdvice(recipe)
	if a.Bool("dry-run") {
		plan := make([]string, len(writes))
		for i, w := range writes {
			plan[i] = w.Field.Key
		}
		return e.out.Emit(omap.New().Set("dry_run", true).Set("target", label).Set("would_write", strList(plan)).
			Set("advice", strList(advice)),
			append([]string{fmt.Sprintf("would write %d settings to %s: %s", len(plan), label, library.Summarize(recipe))},
				prefixed("note: ", advice)...))
	}
	var stored *omap.Map
	var backupPath string
	var warnings []string
	err = e.camera(func(cam *camera.Camera) error {
		if err := cam.RequireReady(); err != nil {
			return err
		}
		current, err := cam.Read(slot)
		if err != nil {
			return err
		}
		plain := current.ToPlain()
		if slot != nil && !model.IsBlank(plain) && !a.Bool("overwrite") {
			held, _ := plain["name"].(string)
			if held == "" {
				held = "an unnamed recipe"
			} else {
				held = model.Quote(held)
			}
			return errs.New(errs.SlotOccupied, fmt.Sprintf("%s already holds %s (%s)", label, held, library.Summarize(plain)),
				fmt.Sprintf("Pick an empty custom setting, or add --overwrite to replace it. The current contents are saved to %s first.",
					library.BackupDir()))
		}
		if !a.Bool("no-backup") && (slot == nil || !model.IsBlank(plain)) {
			if backupPath, err = library.SaveAutoBackup(label, current); err != nil {
				return err
			}
		}
		if _, err := cam.Write(recipe, slot, a.Bool("partial")); err != nil {
			return err
		}
		if stored, err = cam.Read(slot); err != nil {
			return err
		}
		warnings = cam.Info.Warnings
		return nil
	})
	if err != nil {
		return err
	}
	var previous any
	if backupPath != "" {
		previous = backupPath
	}
	result := omap.New().Set("target", label).Set("stored", stored).Set("previous_saved_to", previous).
		Set("warnings", strList(warnings)).Set("advice", strList(advice)).Set("set_on_camera", onBody)
	plain := stored.ToPlain()
	var lines []string
	if slot != nil {
		name, _ := plain["name"].(string)
		shown := "the recipe"
		if name != "" {
			shown = name
		}
		lines = append(lines, fmt.Sprintf("%s now holds %s: %s", label, model.Quote(shown), library.Summarize(plain)))
	} else {
		lines = append(lines, "live settings are now: "+library.Summarize(plain))
	}
	if backupPath != "" {
		lines = append(lines, "previous contents saved to "+backupPath)
	}
	lines = append(lines, warnLines(warnings)...)
	lines = append(lines, prefixed("note: ", advice)...)
	lines = append(lines, "note: "+onBody)
	return e.out.Emit(result, lines)
}

func (e *env) backup(a *Args) error {
	if a.Bool("full") {
		var blob []byte
		err := e.camera(func(cam *camera.Camera) error {
			var err error
			blob, err = cam.ReadSettingsFile()
			return err
		})
		if err != nil {
			return err
		}
		header, err := settingsfile.ParseHeader(blob, "")
		if err != nil {
			return err
		}
		path := a.Str("output")
		if path != "" {
			if err := writeFile(path, blob); err != nil {
				return err
			}
		} else if path, err = library.SaveSettingsFile("camera-settings", blob); err != nil {
			return err
		}
		return e.out.Emit(omap.New().Set("saved", path).Set("kind", "camera_settings_file").Set("bytes", len(blob)).
			Set("model", header.Model).Set("note", privateNote),
			[]string{fmt.Sprintf("saved the camera's settings file (%d bytes: every menu setting and C1-C7) to %s", len(blob), path),
				"note: " + privateNote})
	}
	var data *omap.Map
	if err := e.camera(func(cam *camera.Camera) error {
		var err error
		data, err = cam.Backup()
		return err
	}); err != nil {
		return err
	}
	path := a.Str("output")
	if path != "" {
		text, err := omap.Indent(data)
		if err != nil {
			return err
		}
		if err := writeFile(path, append(text, '\n')); err != nil {
			return err
		}
	} else {
		var err error
		if path, err = library.SaveAutoBackup("all", data); err != nil {
			return err
		}
	}
	names := omap.New()
	lines := []string{"saved live settings and C1-C7 to " + path}
	slots, _ := data.Get("slots")
	if sm, ok := slots.(*omap.Map); ok {
		for _, n := range sm.Keys() {
			v, _ := sm.Get(n)
			plain := map[string]any{}
			if m, ok := v.(*omap.Map); ok {
				plain = m.ToPlain()
			}
			name, _ := plain["name"].(string)
			if name == "" {
				name = "(unnamed)"
				if model.IsBlank(plain) {
					name = "(empty)"
				}
			}
			names.Set("C"+n, name)
			lines = append(lines, fmt.Sprintf("  C%s: %s", n, name))
		}
	}
	return e.out.Emit(omap.New().Set("saved", path).Set("slots", names), lines)
}

func (e *env) restoreSettingsFile(a *Args, file string, blob []byte) error {
	if _, err := settingsfile.ParseHeader(blob, file); err != nil {
		return err
	}
	if a.Bool("live") {
		return errs.Recipe("--live does not apply to a camera settings file",
			"A settings file always restores everything: menus, live settings and C1-C7.")
	}
	if !a.Bool("yes") {
		return errs.Recipe(fmt.Sprintf("restore would replace every setting on the camera (menus, live settings and C1-C7) with %s", file),
			"Add --yes to go ahead. The camera's current settings file is saved first.")
	}
	var previous string
	var mismatch []int
	unchanged := false
	err := e.camera(func(cam *camera.Camera) error {
		current, err := cam.ReadSettingsFile()
		if err != nil {
			return err
		}
		if len(settingsfile.SettingsDiffer(current, blob)) == 0 {
			unchanged = true
			return nil
		}
		if previous, err = library.SaveSettingsFile("before-restore", current); err != nil {
			return err
		}
		if err := cam.RestoreSettingsFile(blob, a.Bool("other-body"), current); err != nil {
			return err
		}
		after, err := cam.ReadSettingsFile()
		if err != nil {
			return err
		}
		mismatch = settingsfile.SettingsDiffer(after, blob)
		return nil
	})
	if err != nil {
		return err
	}
	if unchanged {
		return e.out.Emit(omap.New().Set("restored", false).Set("unchanged", true),
			[]string{"the camera already holds exactly these settings; nothing was written"})
	}
	if len(mismatch) > 0 {
		return errs.New(errs.General, fmt.Sprintf("the camera accepted the settings file but reads back %d different bytes", len(mismatch)),
			fmt.Sprintf("Its previous settings are saved in %s. Switch the camera off and on, then check the settings on the body.", previous))
	}
	return e.out.Emit(omap.New().Set("restored", true).Set("verified", true).Set("previous_saved_to", previous),
		[]string{fmt.Sprintf("restored every setting from %s and read it back to confirm", file), "previous settings saved to " + previous})
}

func (e *env) restore(a *Args) error {
	file := a.Pos("file")
	raw, readErr := os.ReadFile(file)
	if readErr == nil && bytes.HasPrefix(raw, []byte(settingsfile.Magic)) {
		return e.restoreSettingsFile(a, file, raw)
	}
	data, err := library.LoadRecipe(file)
	if err != nil {
		return err
	}
	slots, ok := data["slots"].(map[string]any)
	if !ok {
		return errs.Recipe(file+" is not a backup made by `x100vi backup`", "To write a single recipe use `x100vi set C1 recipe.json`.")
	}
	if readErr == nil {
		if err := repeatedSlotKey(file, raw); err != nil { // decoding kept only one of them
			return err
		}
	}
	type job struct {
		slot   *int
		recipe map[string]any
	}
	var jobs []job
	keys := make([]string, 0, len(slots))
	for k := range slots {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	seen := map[int]string{}
	for _, k := range keys { // "1".."7" as `backup` writes them, or any target spelling ("C3")
		slot, err := model.ParseTarget(k)
		if err != nil {
			return err
		}
		if slot == nil { // "live" is not a custom setting; live settings are under "live" (--live)
			return errs.Recipe(fmt.Sprintf("every key under \"slots\" in %s must be a custom setting C1 to C7, not %s",
				file, model.Quote(k)),
				"Live settings belong under \"live\" at the top of the backup; restore them with --live.")
		}
		if other, dup := seen[*slot]; dup {
			return errs.Recipe(fmt.Sprintf("%s lists %s twice (as %s and %s)", file, model.TargetLabel(slot), model.Quote(other), model.Quote(k)),
				"Keep one entry per custom setting.")
		}
		seen[*slot] = k
		recipe, ok := slots[k].(map[string]any)
		if !ok {
			return errs.Recipe(fmt.Sprintf("%s in %s is not a recipe", model.TargetLabel(slot), file), "")
		}
		if _, err := model.EncodeRecipe(recipe, false); err != nil { // validate everything before writing anything
			return err
		}
		jobs = append(jobs, job{slot, recipe})
	}
	if a.Bool("live") {
		live, ok := data["live"].(map[string]any)
		if !ok {
			return errs.Recipe("this backup has no live settings", "")
		}
		if _, err := model.EncodeRecipe(live, false); err != nil {
			return err
		}
		jobs = append(jobs, job{nil, live})
	}
	labels := make([]string, len(jobs))
	for i, j := range jobs {
		labels[i] = model.TargetLabel(j.slot)
	}
	if !a.Bool("yes") {
		return errs.Recipe("restore would overwrite "+strings.Join(labels, ", "),
			"Add --yes to go ahead. The camera's current contents are saved first.")
	}
	var previous string
	err = e.camera(func(cam *camera.Camera) error {
		if err := cam.RequireReady(); err != nil {
			return err
		}
		snapshot, err := cam.Backup()
		if err != nil {
			return err
		}
		if previous, err = library.SaveAutoBackup("before-restore", snapshot); err != nil {
			return err
		}
		for _, j := range jobs {
			if j.slot != nil {
				if err := cam.ClearWBMemory(*j.slot); err != nil {
					return err
				}
			}
			if _, err := cam.Write(j.recipe, j.slot, false); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	return e.out.Emit(omap.New().Set("restored", strList(labels)).Set("previous_saved_to", previous),
		[]string{"restored " + strings.Join(labels, ", "), "previous contents saved to " + previous})
}

// repeatedSlotKey refuses a backup whose top-level "slots" object names a custom setting twice. Keys are
// compared as targets ("C1", "c1" and "1" are one), and the same key written twice counts too: decoding
// the JSON into a map silently keeps only the last of those. Anything that is not well-formed is left to
// the checks that follow.
func repeatedSlotKey(file string, raw []byte) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil
	}
	sawSlots := false
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return nil
		}
		if key != "slots" {
			var skip json.RawMessage
			if dec.Decode(&skip) != nil {
				return nil
			}
			continue
		}
		if sawSlots {
			return errs.Recipe(fmt.Sprintf("%s has \"slots\" twice", file), "Keep one \"slots\" object with one entry per custom setting.")
		}
		sawSlots = true
		if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
			return nil
		}
		seen := map[int]string{}
		for dec.More() {
			tok, err := dec.Token()
			if err != nil {
				return nil
			}
			k, _ := tok.(string)
			var skip json.RawMessage
			if dec.Decode(&skip) != nil {
				return nil
			}
			slot, err := model.ParseTarget(k)
			if err != nil || slot == nil {
				continue // reported with its own message by restore
			}
			if other, dup := seen[*slot]; dup {
				return errs.Recipe(fmt.Sprintf("%s lists %s twice (as %s and %s)", file, model.TargetLabel(slot),
					model.Quote(other), model.Quote(k)), "Keep one entry per custom setting.")
			}
			seen[*slot] = k
		}
		if _, err := dec.Token(); err != nil { // the closing brace of "slots"
			return nil
		}
	}
	return nil
}

func (e *env) validate(a *Args) error {
	var results []any
	var lines []string
	failed := false
	for _, path := range a.PosList("files") {
		recipe, err := library.LoadRecipe(path)
		if err == nil {
			_, err = model.EncodeRecipe(recipe, false)
		}
		if err != nil {
			p, ok := errs.As(err)
			if !ok {
				return err
			}
			failed = true
			r := omap.New().Set("file", path).Set("valid", false)
			for _, k := range []string{"error", "message", "hint"} {
				if v, ok := p.AsMap().Get(k); ok {
					r.Set(k, v)
				}
			}
			results = append(results, r)
			line := fmt.Sprintf("%s: INVALID - %s", path, p.Message)
			if p.Hint != "" {
				line += "\n  " + p.Hint
			}
			lines = append(lines, line)
			continue
		}
		advice := RecipeAdvice(recipe)
		results = append(results, omap.New().Set("file", path).Set("valid", true).Set("summary", library.Summarize(recipe)).
			Set("advice", strList(advice)))
		lines = append(lines, fmt.Sprintf("%s: ok - %s", path, library.Summarize(recipe)))
		lines = append(lines, prefixed("  note: ", advice)...)
	}
	if err := e.out.Emit(omap.New().Set("results", results), lines); err != nil {
		return err
	}
	if failed {
		return quiet{errs.InvalidRecipe.Exit}
	}
	return nil
}

func (e *env) preview(a *Args) (retErr error) {
	fmtName := a.Str("format")
	format := rawconv.OutputFormats[fmtName]
	full := a.Bool("full")
	if format.FullOnly && !full {
		return errs.Recipe(format.Label+" previews need --full",
			"The camera makes TIFF only at full resolution (about 125 MB per image for 8-bit, "+
				"245 MB for 16-bit). Add --full, or use --format jpeg or heif for half-size previews.")
	}
	var rafs []rawconv.RAFInfo
	for _, p := range a.PosList("rafs") {
		info, err := rawconv.InspectRAF(p)
		if err != nil {
			return err
		}
		rafs = append(rafs, info)
	}
	drp := a.Str("d-range-priority")
	type named struct {
		label  string
		recipe map[string]any
	}
	var recipes []named
	for _, ref := range a.List("recipe") {
		recipe, err := library.LoadRecipe(ref)
		if err != nil {
			return err
		}
		if _, err := model.EncodeRecipe(recipe, false); err != nil {
			return err
		}
		if p, _ := model.Get(recipe, "d_range_priority").(string); drp == "" && strings.ToLower(p) == "auto" {
			return errs.Recipe(ref+": d_range_priority auto cannot be previewed",
				"Add --d-range-priority weak, strong or plus to preview a level; the recipe can still be saved to the camera with auto.")
		}
		recipes = append(recipes, named{strings.TrimSuffix(filepath.Base(ref), filepath.Ext(ref)), recipe})
	}
	if len(recipes) == 0 && !a.Bool("as-shot") {
		return errs.Recipe("nothing to render", "Give --recipe FILE [...] and/or --as-shot.")
	}
	// Every output path is fixed before connecting and unique for the whole batch: the as-shot render
	// keeps "as-shot", and a recipe or RAF whose name is taken gets -2, -3, ... (compared without
	// case, as macOS and Windows file systems do).
	jobs := recipes
	if a.Bool("as-shot") {
		jobs = append([]named{{"as-shot", nil}}, recipes...)
	}
	labelsTaken := map[string]bool{}
	for i := range jobs {
		jobs[i].label = unique(jobs[i].label, labelsTaken)
	}
	outDir := a.Str("out")
	ev := a.Float("ev")
	if _, err := rawconv.CheckExposure(ev); err != nil { // fail before connecting
		return err
	}
	if a.Bool("verify") {
		if _, err := exifcheck.RequireExiftool(); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	// Each output file is also created now, in order, so the file system itself says when two names
	// are one file: macOS treats the composed and decomposed spellings of "café" as the same name,
	// which no string comparison here catches. A path that turns out to be an earlier one's file gets
	// the next suffix. Files this command created and never wrote are removed again if it fails.
	stemsTaken, pathsTaken := map[string]bool{}, map[string]bool{}
	paths := make([][]string, len(rafs))
	var outputs []*reservedFile
	written := map[string]bool{}
	defer func() {
		if retErr == nil {
			return
		}
		for _, r := range outputs {
			if r.created && !written[r.path] {
				_ = os.Remove(r.path)
			}
		}
	}()
	for i, raf := range rafs {
		stem := unique(strings.TrimSuffix(filepath.Base(raf.Path), filepath.Ext(raf.Path)), stemsTaken)
		for _, job := range jobs {
			base := stem + "__" + job.label
			for attempt := 0; ; attempt++ {
				path := filepath.Join(outDir, unique(base, pathsTaken)+"."+format.Extension)
				r, clash, err := reserveOutput(path, outputs)
				if err != nil {
					return err
				}
				if !clash {
					outputs = append(outputs, r)
					paths[i] = append(paths[i], path)
					break
				}
				if attempt > len(outputs) { // every earlier file was hit: the file system is not making sense
					return errs.New(errs.General, "cannot find a free name for "+path,
						"Use an empty folder for --out and run the command again.")
				}
			}
		}
	}
	timeout := time.Duration(a.Float("timeout") * float64(time.Second))
	var renders, frames []any
	var rows [][]sheet.Cell
	var lines, warnings []string
	err := e.camera(func(cam *camera.Camera) error {
		if err := cam.RequireReady(); err != nil {
			return err
		}
		warnings = cam.Info.Warnings
		conv := rawconv.NewRawConverter(cam)
		for i, raf := range rafs {
			data, err := os.ReadFile(raf.Path)
			if err != nil {
				return err
			}
			asShot, err := conv.Upload(data) // upload once, develop many
			if err != nil {
				return err
			}
			shot := rawconv.Describe(asShot)
			film, _ := shot.Get("film_simulation")
			dr, _ := shot.Get("dynamic_range")
			priority, _ := shot.Get("d_range_priority")
			line := fmt.Sprintf("%s: shot as %v, DR%v", filepath.Base(raf.Path), film, dr)
			if priority != "off" {
				line += fmt.Sprintf(", D-range priority %v", priority)
			}
			lines = append(lines, line)
			var row []sheet.Cell
			for j, job := range jobs {
				profile := asShot.Clone()
				var adjustments []rawconv.Adjustment
				if job.recipe != nil {
					if profile, adjustments, err = rawconv.ApplyRecipe(asShot, job.recipe, ev, drp); err != nil {
						return err
					}
				}
				image, err := conv.Develop(profile, full, timeout, format.Name)
				if err != nil {
					return err
				}
				target := paths[i][j]
				if err := os.WriteFile(target, image, 0o644); err != nil {
					return err
				}
				written[target] = true
				notApplied := make([]any, len(adjustments))
				for i, adj := range adjustments {
					notApplied[i] = adj.AsMap()
				}
				entry := omap.New().Set("raf", raf.Path).Set("recipe", job.label).Set("file", target).Set("not_applied", notApplied)
				lines = append(lines, "  "+target)
				for _, adj := range adjustments {
					lines = append(lines, "    not applied as written - "+adj.String())
				}
				if a.Bool("verify") && job.recipe != nil {
					effective := map[string]any{}
					for k, v := range job.recipe {
						effective[k] = v
					}
					for _, adj := range adjustments {
						if adj.Setting == "dynamic_range" || adj.Setting == "d_range_priority" {
							effective[adj.Setting] = adj.Applied
						}
					}
					check, err := exifcheck.Compare(effective, target)
					if err != nil {
						return err
					}
					mism := check.Map()
					m, _ := mism.Get("mismatches")
					entry.Set("verified", omap.New().Set("checked", check.Checked).Set("mismatches", m))
					line := fmt.Sprintf("    verified %d/%d settings in the output", check.Checked-len(check.Mismatches), check.Checked)
					for _, x := range check.Mismatches {
						line += fmt.Sprintf("\n    MISMATCH %s: expected %v, found %v", x.Tag, x.Expected, x.Found)
					}
					lines = append(lines, line)
				}
				renders = append(renders, entry)
				title := "As shot"
				if job.recipe != nil {
					title = job.label
					if n, _ := job.recipe["name"].(string); n != "" {
						title = n
					}
				}
				row = append(row, sheet.Cell{Label: title, Path: target})
			}
			rows = append(rows, row)
			frames = append(frames, omap.New().Set("raf", raf.Path).Set("as_shot", shot))
		}
		return nil
	})
	if err != nil {
		return err
	}
	var sheetPath any
	count := 0
	for _, r := range rows {
		count += len(r)
	}
	if !a.Bool("no-sheet") && count > 1 {
		if format.Name == "jpeg" {
			p, err := sheet.ContactSheet(rows, filepath.Join(outDir, "contact-sheet.jpg"))
			if err != nil {
				return err
			}
			sheetPath = p
			lines = append(lines, "contact sheet: "+p)
		} else {
			lines = append(lines, "no contact sheet: it is made only for JPEG previews, not "+format.Label)
		}
	}
	lines = append(warnLines(warnings), lines...)
	if ev != 0 {
		lines = append(lines, fmt.Sprintf("exposure compensation %+g EV applied to every recipe render", ev))
	}
	var drpOut any
	if drp != "" {
		drpOut = drp
		lines = append(lines, "D-range priority "+drp+" used for every recipe render")
	}
	return e.out.Emit(omap.New().Set("renders", renders).Set("frames", frames).Set("contact_sheet", sheetPath).
		Set("format", format.Name).Set("exposure_ev", ev).Set("d_range_priority", drpOut).
		Set("full_resolution", full).Set("warnings", strList(warnings)), lines)
}

// statOutput is os.Stat; tests replace it to stand in for a file system that folds Unicode spellings.
var statOutput = os.Stat

// reservedFile is a preview output created before connecting.
type reservedFile struct {
	path    string
	created bool // this command created it (it did not exist), so it is removed again if left unused
}

// reserveOutput creates path, keeping an existing file's contents (the render overwrites it later), and
// reports clash when the file system resolves path to the file of an earlier reservation. A file
// created only to find that out is removed again.
func reserveOutput(path string, earlier []*reservedFile) (r *reservedFile, clash bool, err error) {
	created := true
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, fs.ErrExist) {
		created = false
		f, err = os.OpenFile(path, os.O_WRONLY|os.O_CREATE, 0o644)
	}
	if err != nil {
		return nil, false, errs.Wrap(errs.General, err, "cannot create "+path+": "+err.Error(),
			"Check that the output folder (--out) can be written to, then run the command again.")
	}
	f.Close()
	undo := func() {
		if created {
			_ = os.Remove(path)
		}
	}
	mine, err := statOutput(path)
	if err != nil {
		undo()
		return nil, false, err
	}
	for _, e := range earlier {
		theirs, err := statOutput(e.path)
		if err != nil {
			undo()
			return nil, false, err
		}
		if os.SameFile(mine, theirs) {
			undo()
			return nil, true, nil
		}
	}
	return &reservedFile{path: path, created: created}, false, nil
}

// unique returns name, or name-2, name-3, ... if that is already taken (ignoring case), and takes it.
func unique(name string, taken map[string]bool) string {
	out := name
	for n := 2; taken[strings.ToLower(out)]; n++ {
		out = fmt.Sprintf("%s-%d", name, n)
	}
	taken[strings.ToLower(out)] = true
	return out
}

func (e *env) verify(a *Args) error {
	recipe, err := library.LoadRecipe(a.Str("recipe"))
	if err != nil {
		return err
	}
	if _, err := model.EncodeRecipe(recipe, false); err != nil {
		return err
	}
	photo := a.Pos("photo")
	check, err := exifcheck.Compare(recipe, photo)
	if err != nil {
		return err
	}
	ok := len(check.Mismatches) == 0
	lines := []string{fmt.Sprintf("%s: %d/%d settings match %s", photo, check.Checked-len(check.Mismatches), check.Checked, a.Str("recipe"))}
	for _, x := range check.Mismatches {
		lines = append(lines, fmt.Sprintf("  %s: recipe expects %v, photo has %v", x.Tag, x.Expected, x.Found))
	}
	if err := e.out.Emit(check.Map().Set("matches", ok), lines); err != nil {
		return err
	}
	if !ok {
		return quiet{3}
	}
	return nil
}

func (e *env) recipes(a *Args) error {
	if name := a.Pos("name"); name != "" {
		r, err := library.StarterOrdered(name)
		if err != nil {
			return err
		}
		return e.out.Print(r)
	}
	var items []any
	var lines []string
	for _, name := range library.StarterNames() {
		r, err := library.Starter(name)
		if err != nil {
			return err
		}
		desc, _ := r["description"].(string)
		tags, _ := r["tags"].([]any)
		if tags == nil {
			tags = []any{}
		}
		summary := library.Summarize(r)
		items = append(items, omap.New().Set("name", name).Set("description", desc).Set("tags", tags).Set("summary", summary))
		lines = append(lines, fmt.Sprintf("%s: %s\n    %s", name, desc, summary))
	}
	return e.out.Emit(omap.New().Set("recipes", items), lines)
}

func (e *env) schema(a *Args) error {
	v, err := library.DecodeOrdered(library.SchemaJSON())
	if err != nil {
		return err
	}
	return e.out.Print(v)
}

func (e *env) installSkill(a *Args) error {
	home, _ := os.UserHomeDir()
	var targets []string
	if a.Bool("claude") {
		targets = append(targets, filepath.Join(home, ".claude", "skills", "x100vi"))
	}
	if a.Bool("codex") {
		targets = append(targets, filepath.Join(sandbox.CodexHome(), "skills", "x100vi"))
	}
	if d := a.Str("dir"); d != "" {
		if d == "~" || strings.HasPrefix(d, "~/") || strings.HasPrefix(d, "~"+string(filepath.Separator)) {
			d = filepath.Join(home, d[1:])
		}
		targets = append(targets, filepath.Join(d, "x100vi"))
	}
	if len(targets) == 0 {
		return errs.Recipe("say where to install the skill",
			"Use --claude (~/.claude/skills), --codex (~/.codex/skills) and/or --dir SKILLS_FOLDER.")
	}
	var done []string
	for _, target := range targets {
		if info, err := os.Lstat(target); err == nil {
			marker, readErr := os.ReadFile(filepath.Join(target, "SKILL.md"))
			ours := info.Mode()&os.ModeSymlink != 0 || (readErr == nil && bytes.Contains(marker, []byte("name: x100vi")))
			if !ours && !a.Bool("force") {
				return errs.New(errs.General, target+" exists and is not this skill", "Remove it, or add --force to replace it.")
			}
			if err := os.RemoveAll(target); err != nil {
				return err
			}
		}
		if err := library.WriteSkill(target); err != nil {
			return err
		}
		done = append(done, target)
	}
	lines := prefixed("skill installed at ", done)
	lines = append(lines, "Start a new Claude Code or Codex session to pick it up.")
	return e.out.Emit(omap.New().Set("installed", strList(done)), lines)
}

func (e *env) sandboxHelp(a *Args) error {
	home, _ := os.UserHomeDir()
	tools, launchers := sandbox.ToolPaths(), sandbox.LauncherPaths(home)
	rules := sandbox.CodexRules(tools, launchers)
	rulesPath := sandbox.CodexRulesPath()
	settings := sandbox.ClaudeSettings(tools, launchers)
	var written any
	if a.Bool("write-codex-rules") {
		p, err := sandbox.WriteCodexRules(rules)
		if err != nil {
			return err
		}
		written = p
	}
	inside := sandbox.Detect(nil)
	var insideOut any
	if inside != "" {
		insideOut = inside
	}
	result := omap.New().Set("running_in_sandbox", insideOut).
		Set("codex", omap.New().Set("rules_file", rulesPath).Set("rules", rules).Set("written", written)).
		Set("claude_code", omap.New().Set("settings_file", "~/.claude/settings.json").Set("settings", settings))
	settingsJSON, _ := json.MarshalIndent(settings, "", "  ")
	status := "This command is not sandboxed."
	if inside != "" {
		status = "This command is running inside the " + inside + " sandbox."
	}
	lines := []string{
		"Agents can run commands in a sandbox that hides USB devices. Camera commands must run outside it.",
		status, "",
		"In the moment: approve the agent's request to run the x100vi command outside the sandbox.", "",
		fmt.Sprintf("Codex, permanently: save the rules below as %s and restart Codex", rulesPath),
		"  (or run `x100vi sandbox-help --write-codex-rules`). Reading and previewing then run without",
		"  a prompt; `set` and `restore` still ask before each run.", "",
		rules,
		"Claude Code (only if you turned its sandbox on): merge this into ~/.claude/settings.json:", "",
		string(settingsJSON),
	}
	if written != nil {
		lines = append([]string{fmt.Sprintf("wrote %v; restart Codex to load it", written), ""}, lines...)
	}
	return e.out.Emit(result, lines)
}

// -- wiring --------------------------------------------------------------------------------------

// jsonOption is on every command. The parser handles it in order with the other tokens, so a value
// option directly before it (`--output --json`) is missing its value rather than taking "--json" away.
var jsonOption = option{long: "--json", help: "print one JSON object instead of text"}

func commands() []*command {
	cmds := []*command{
		{name: "doctor", help: "check that the camera is connected, supported and in the right USB mode"},
		{name: "get", help: "print a recipe from the camera as JSON",
			args:    []positional{{name: "target", opt: true, def: "live", help: "live, C1..C7, or all (default: live)"}},
			options: []option{{long: "--output", short: "-o", kind: optString, help: "write to this file instead of printing"}}},
		{name: "set", help: "write a recipe to the live settings or to a custom setting",
			args: []positional{{name: "target", help: "live or C1..C7"},
				{name: "recipe", help: "recipe .json file, or the name of a starter recipe"}},
			options: []option{
				{long: "--overwrite", help: "replace a custom setting that is not empty"},
				{long: "--partial", help: "change only the settings in the file"},
				{long: "--dry-run", help: "validate and show the plan without connecting"},
				{long: "--no-backup", help: "do not save the previous contents first"}}},
		{name: "backup", help: "save the live settings and C1-C7 to a JSON file, or (--full) every setting",
			options: []option{
				{long: "--output", short: "-o", kind: optString, help: "file to write (default: a timestamped file in the state folder)"},
				{long: "--full", help: "save the camera's own settings file instead: every menu setting plus C1-C7"}}},
		{name: "restore", help: "write a backup back to the camera: C1-C7 from a JSON backup, or everything from a --full file",
			args: []positional{{name: "file", help: "a JSON backup or a camera settings file"}},
			options: []option{
				{long: "--live", help: "JSON backups: also restore the live shooting settings"},
				{long: "--yes", help: "confirm overwriting the camera's settings"},
				{long: "--other-body", help: "allow a settings file saved from a different X100VI body"}}},
		{name: "validate", help: "check recipe files without a camera",
			args: []positional{{name: "files", many: true, metavar: "FILE"}}},
		{name: "preview", help: "develop RAF files with recipes on the camera's own processor",
			args: []positional{{name: "rafs", many: true, metavar: "RAF", help: ".RAF files shot on an X100VI"}},
			options: []option{
				{long: "--recipe", short: "-r", kind: optList, metavar: "RECIPE", help: "recipe files or starter names"},
				{long: "--as-shot", help: "also render each RAF with its own settings"},
				{long: "--out", short: "-o", kind: optString, def: "x100vi-previews", help: "output folder (default: ./x100vi-previews)"},
				{long: "--full", help: "full resolution instead of half size"},
				{long: "--format", kind: optString, def: "jpeg", choices: rawconv.OutputFormatNames,
					help: "output file type (default jpeg); tiff and tiff16 need --full"},
				{long: "--ev", kind: optFloat, def: 0.0, help: "exposure compensation in 1/3 EV steps, -2..+3"},
				{long: "--d-range-priority", kind: optString, choices: []string{"off", "weak", "strong", "plus"},
					help: "render with this D-range priority instead of the recipe's (plus: a level beyond strong that only the converter offers)"},
				{long: "--no-sheet", help: "do not build a contact sheet"},
				{long: "--verify", help: "check each output's EXIF against its recipe (needs exiftool)"},
				{long: "--timeout", kind: optFloat, def: 90.0, hidden: true}}},
		{name: "verify", help: "compare a photo's recorded settings with a recipe (needs exiftool)",
			args:    []positional{{name: "photo"}},
			options: []option{{long: "--recipe", short: "-r", kind: optString, required: true, help: "recipe file or starter name"}}},
		{name: "recipes", help: "list the bundled starter recipes, or print one",
			args: []positional{{name: "name", opt: true}}},
		{name: "schema", help: "print the recipe JSON schema"},
		{name: "sandbox-help", help: "show how to let an agent run camera commands outside its sandbox (USB is hidden inside one)",
			options: []option{{long: "--write-codex-rules", help: "write the Codex rules file (~/.codex/rules/x100vi.rules)"}}},
		{name: "install-skill", help: "install the agent skill for Claude Code and/or Codex",
			options: []option{
				{long: "--claude", help: "install into ~/.claude/skills"},
				{long: "--codex", help: "install into ~/.codex/skills"},
				{long: "--dir", kind: optString, help: "install into this skills folder"},
				{long: "--force", help: "replace an existing folder that is not this skill"}}},
	}
	for _, c := range cmds {
		c.options = append(c.options, jsonOption)
	}
	return cmds
}

func (e *env) handler(name string) func(*Args) error {
	return map[string]func(*Args) error{
		"doctor": e.doctor, "get": e.get, "set": e.set, "backup": e.backup, "restore": e.restore,
		"validate": e.validate, "preview": e.preview, "verify": e.verify, "recipes": e.recipes,
		"schema": e.schema, "sandbox-help": e.sandboxHelp, "install-skill": e.installSkill,
	}[name]
}

func mainHelp(cmds []*command) string {
	var b strings.Builder
	b.WriteString("usage: x100vi [--json] [--version] COMMAND ...\n\n")
	b.WriteString("Read, write and preview film-simulation recipes on a Fujifilm X100VI over USB.\n\ncommands:\n")
	for _, c := range cmds {
		fmt.Fprintf(&b, "  %-15s %s\n", c.name, c.help)
	}
	b.WriteString("\nRun `x100vi COMMAND --help` for a command's options.\n")
	b.WriteString("The camera must be in: " + errs.ModeMenu + "\n")
	return b.String()
}

// Main runs the command line and returns the exit code.
func Main(argv []string, factory Factory, stdout, stderr io.Writer) int {
	if factory == nil {
		factory = transport.Open
	}
	cmds := commands()
	// Only --json before the command name is global; after it, each command's parser handles --json in
	// token order like any other option.
	asJSON := false
	rest := argv
	for len(rest) > 0 && rest[0] == "--json" {
		asJSON = true
		rest = rest[1:]
	}
	out := &Out{JSON: asJSON, Stdout: stdout}
	fail := func(err error) int {
		if q, ok := err.(quiet); ok {
			return q.code
		}
		p, ok := errs.As(err)
		if !ok {
			p = errs.Wrap(errs.General, err, err.Error(), "")
		}
		if asJSON {
			_ = out.Print(p.AsMap())
		}
		fmt.Fprintln(stderr, "error: "+p.Message)
		if p.Hint != "" {
			fmt.Fprintln(stderr, "hint: "+p.Hint)
		}
		return p.Kind.Exit
	}
	if len(rest) == 0 {
		fmt.Fprint(stderr, mainHelp(cmds))
		return fail(errs.Recipe("a command is required", "Run `x100vi --help` for the list."))
	}
	switch rest[0] {
	case "-h", "--help", "help":
		fmt.Fprint(stdout, mainHelp(cmds))
		return 0
	case "--version", "-V":
		fmt.Fprintln(stdout, "x100vi "+Version)
		return 0
	}
	var cmd *command
	for _, c := range cmds {
		if c.name == rest[0] {
			cmd = c
		}
	}
	if cmd == nil {
		// A caller that asked for JSON anywhere on the line gets this error as JSON too
		// (`x100vi gte --json` like `x100vi --json gte`). The tokens are only scanned, not reordered.
		if asksForJSON(rest[1:]) {
			asJSON, out.JSON = true, true
		}
		return fail(errs.Recipe(fmt.Sprintf("unknown command %s.%s", model.Quote(rest[0]), model.Suggest(rest[0], names(cmds))),
			"Run `x100vi --help` for the list."))
	}
	for _, tok := range rest[1:] {
		if tok == "-h" || tok == "--help" {
			fmt.Fprint(stdout, cmd.helpText())
			return 0
		}
	}
	args, err := cmd.parse(rest[1:])
	if err != nil {
		// The command line is wrong, but a caller that asked for JSON still gets the error as JSON.
		if asksForJSON(rest[1:]) {
			asJSON, out.JSON = true, true
		}
		return fail(err)
	}
	if args.Bool("json") {
		asJSON, out.JSON = true, true
	}
	e := &env{out: out, factory: factory}
	if err := e.handler(cmd.name)(args); err != nil {
		return fail(err)
	}
	return 0
}

// asksForJSON reports whether a command's tokens include --json as an option (before any "--").
func asksForJSON(tokens []string) bool {
	for _, tok := range tokens {
		if tok == "--" {
			return false
		}
		if tok == "--json" {
			return true
		}
	}
	return false
}

func names(cmds []*command) []string {
	out := make([]string, len(cmds))
	for i, c := range cmds {
		out[i] = c.name
	}
	return out
}
