package rawconv_test

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/volodymyrsmirnov/x100vi-skill/internal/camera"
	"github.com/volodymyrsmirnov/x100vi-skill/internal/errs"
	"github.com/volodymyrsmirnov/x100vi-skill/internal/fakecam"
	"github.com/volodymyrsmirnov/x100vi-skill/internal/ptp"
	. "github.com/volodymyrsmirnov/x100vi-skill/internal/rawconv"
)

const negativeJSON = `{"film_simulation": "reala_ace", "dynamic_range": "400", "grain": {"strength": "weak", "size": "small"},
	"color_chrome_effect": "strong",
	"white_balance": {"mode": "kelvin", "kelvin": 5000, "shift_r": 0, "shift_b": -2},
	"highlight": -1, "shadow": -0.5, "color": 2, "sharpness": -1, "noise_reduction": -4, "clarity": -2}`

func recipe(t *testing.T, text string) map[string]any {
	t.Helper()
	var r map[string]any
	if err := json.Unmarshal([]byte(text), &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func asShot(t *testing.T) *Profile {
	t.Helper()
	p := NewProfile(fakecam.AsShotProfile)
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	return p
}

func apply(t *testing.T, r map[string]any, ev float64, drp string) (*Profile, []Adjustment) {
	t.Helper()
	p, adj, err := ApplyRecipe(asShot(t), r, ev, drp)
	if err != nil {
		t.Fatal(err)
	}
	return p, adj
}

func wantKind(t *testing.T, err error, kind errs.Kind) *errs.Error {
	t.Helper()
	e, ok := errs.As(err)
	if !ok || e.Kind != kind {
		t.Fatalf("want a %s error, got %v", kind.Name, err)
	}
	return e
}

func settings(adj []Adjustment) []string {
	out := []string{}
	for _, a := range adj {
		out = append(out, a.Setting)
	}
	return out
}

// -- profile layout (fixture: a real X100VI frame, Classic Chrome, DR200, D-range priority on) --------

func TestReadsTheCamerasAsShotProfile(t *testing.T) {
	p := asShot(t)
	if len(p.Data) != 625 || p.Cells() != 29 || p.IOPCode() != "FF179503" {
		t.Fatal(len(p.Data), p.Cells(), p.IOPCode())
	}
	d := Describe(p).ToPlain()
	if d["film_simulation"] != "classic_chrome" || d["dynamic_range"] != "200" || d["d_range_priority"] != "weak" {
		t.Fatal(d)
	}
	wb := d["white_balance"].(map[string]any)
	if wb["mode"] != "as_shot" || d["highlight"] != -1 || d["exposure_ev"] != 0.0 {
		t.Fatal(d)
	}
}

func TestDescribeKeepsPythonKeyOrder(t *testing.T) {
	d := Describe(asShot(t))
	want := []string{"image_size", "image_quality", "dynamic_range", "film_simulation", "grain", "color_chrome_effect",
		"white_balance", "highlight", "shadow", "color", "sharpness", "noise_reduction", "lens_modulation_optimizer",
		"color_space", "smooth_skin", "color_chrome_fx_blue", "clarity", "d_range_priority", "exposure_ev"}
	if !reflect.DeepEqual(d.Keys(), want) {
		t.Fatal(d.Keys())
	}
}

func TestHostFormExtendsWithoutMovingCells(t *testing.T) {
	p := asShot(t)
	form, err := p.HostForm()
	if err != nil {
		t.Fatal(err)
	}
	host := NewProfile(form)
	if want := Table + 4*HostCells; len(host.Data) != want || want != 629 {
		t.Fatal(len(host.Data))
	}
	if binary.LittleEndian.Uint16(host.Data) != 29 || host.Data[2] != 9 {
		t.Fatal(host.Data[:3])
	}
	if !reflect.DeepEqual(host.Values()[:29], p.Values()) || host.Get(29) != 0 || host.IOPCode() != "FF179503" {
		t.Fatal(host.Values())
	}
}

func TestGarbageIsNotPatched(t *testing.T) {
	wantKind(t, NewProfile(make([]byte, 700)).Validate(), errs.ConversionFailed)
	wantKind(t, NewProfile(make([]byte, 40)).Validate(), errs.ConversionFailed)
}

// -- applying a recipe ---------------------------------------------------------------------------

func TestRecipeBecomesProfileCells(t *testing.T) {
	p, adjustments := apply(t, recipe(t, negativeJSON), 0, "")
	want := map[int]int{2: 7, 5: 0, 6: 200, 7: 0, 8: 20, 9: 2, 10: 3, 11: 2, 12: 0x8007, 13: 0, 14: -2, 15: 5000,
		16: -10, 17: -5, 18: 20, 19: -10, 20: 0x8000, 24: 1, 25: 1, 27: -20}
	for i, v := range want {
		if p.Get(i) != v {
			t.Errorf("cell %d: got %d, want %d", i, p.Get(i), v)
		}
	}
	// left as shot
	if got := []int{p.Get(3), p.Get(4), p.Get(21), p.Get(22), p.Get(28)}; !reflect.DeepEqual(got, []int{7, 2, 1, 1, 2}) {
		t.Fatal(got)
	}
	if got := settings(adjustments); !reflect.DeepEqual(got, []string{"dynamic_range"}) {
		t.Fatal(got)
	}
}

func TestDynamicRangeAboveTheFramesOwnIsCappedAndExplained(t *testing.T) {
	_, adjustments := apply(t, map[string]any{"dynamic_range": "400"}, 0, "")
	if len(adjustments) != 1 {
		t.Fatal(adjustments)
	}
	adj := adjustments[0]
	if adj.Setting != "dynamic_range" || adj.Requested != "400" || adj.Applied != "200" {
		t.Fatal(adj)
	}
	if !strings.Contains(adj.Reason, "shot at DR200") {
		t.Fatal(adj.Reason)
	}
	if r, _ := adj.AsMap().Get("reason"); r != adj.Reason {
		t.Fatal(r)
	}
}

func TestDynamicRangeAtOrBelowTheFrameIsAppliedSilently(t *testing.T) {
	for _, dr := range []struct {
		name string
		cell int
	}{{"100", 100}, {"200", 200}} {
		p, adjustments := apply(t, map[string]any{"dynamic_range": dr.name}, 0, "")
		if p.Get(6) != dr.cell || len(adjustments) != 0 {
			t.Fatal(dr, p.Get(6), adjustments)
		}
	}
}

func TestDynamicRangeAutoIsReported(t *testing.T) {
	p, adjustments := apply(t, map[string]any{"dynamic_range": "auto"}, 0, "")
	if p.Get(6) != 200 || adjustments[0].Requested != "auto" {
		t.Fatal(p.Get(6), adjustments)
	}
}

func TestDRangePriorityFromTheFrameIsReplacedByTheRecipes(t *testing.T) {
	if asShot(t).Get(7) != 1 {
		t.Fatal(asShot(t).Get(7))
	}
	if p, _ := apply(t, map[string]any{}, 0, ""); p.Get(7) != 0 {
		t.Fatal(p.Get(7))
	}
	if p, _ := apply(t, map[string]any{"d_range_priority": "strong"}, 0, ""); p.Get(7) != 2 {
		t.Fatal(p.Get(7))
	}
}

func TestPriorityOnReportsTheSettingsItMakesTheCameraIgnore(t *testing.T) {
	_, adjustments := apply(t, recipe(t, `{"d_range_priority": "weak", "highlight": 2, "dynamic_range": "100"}`), 0, "")
	last := adjustments[len(adjustments)-1]
	if last.Applied != "ignored" || !strings.Contains(last.Setting, "highlight") {
		t.Fatal(last)
	}
}

func TestPriorityAutoCannotBePreviewed(t *testing.T) {
	_, _, err := ApplyRecipe(asShot(t), map[string]any{"d_range_priority": "auto"}, 0, "")
	e := wantKind(t, err, errs.InvalidRecipe)
	if !strings.Contains(e.Hint, "--d-range-priority") {
		t.Fatal(e.Hint)
	}
}

func TestPreviewPriorityOverrideIncludingPlus(t *testing.T) {
	p, adj := apply(t, map[string]any{"d_range_priority": "auto", "film_simulation": "velvia"}, 0, "plus")
	if p.Get(7) != 3 || p.Get(8) != 2 {
		t.Fatal(p.Get(7), p.Get(8))
	}
	if adj[0].Setting != "d_range_priority" || adj[0].Requested != "auto" || adj[0].Applied != "plus" {
		t.Fatal(adj[0])
	}
	p, adj = apply(t, map[string]any{"d_range_priority": "weak"}, 0, "off")
	if p.Get(7) != 0 || len(adj) != 1 || adj[0].Applied != "off" {
		t.Fatal(p.Get(7), adj)
	}
	p, adj = apply(t, map[string]any{"d_range_priority": "strong"}, 0, "strong")
	if p.Get(7) != 2 || len(adj) != 0 {
		t.Fatal(p.Get(7), adj)
	}
	_, _, err := ApplyRecipe(asShot(t), map[string]any{}, 0, "auto")
	e := wantKind(t, err, errs.InvalidRecipe)
	if !strings.Contains(e.Message, "not a preview level") {
		t.Fatal(e.Message)
	}
}

func TestDescribeNamesThePlusLevel(t *testing.T) {
	p := asShot(t)
	p.Set(7, 3)
	if got, _ := Describe(p).Get("d_range_priority"); got != "plus" {
		t.Fatal(got)
	}
}

func TestMonochromeRecipeLeavesTheColourCellAlone(t *testing.T) {
	r := recipe(t, `{"film_simulation": "monochrome_g", "white_balance": {"mode": "daylight", "shift_r": -6, "shift_b": -3}}`)
	p, _ := apply(t, r, 0, "")
	got := []int{p.Get(8), p.Get(23), p.Get(26), p.Get(12), p.Get(13), p.Get(14)}
	if !reflect.DeepEqual(got, []int{9, 0, 0, 4, -6, -3}) {
		t.Fatal(got)
	}
	if p.Get(18) != asShot(t).Get(18) {
		t.Fatal(p.Get(18))
	}
}

func TestTheAsShotProfileIsNeverModified(t *testing.T) {
	base := asShot(t)
	before := bytes.Clone(base.Data)
	if _, _, err := ApplyRecipe(base, recipe(t, negativeJSON), 1.0/3, ""); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(base.Data, before) {
		t.Fatal("as-shot profile changed")
	}
}

func TestExposureInThirds(t *testing.T) {
	for _, c := range []struct {
		ev   float64
		cell int
	}{{0, 0}, {1.0 / 3, 333}, {0.33, 333}, {2.0 / 3, 667}, {0.67, 667}, {-1.0 / 3, -333}, {1, 1000}, {-2, -2000}, {3, 3000}} {
		if p, _ := apply(t, map[string]any{}, c.ev, ""); p.Get(5) != c.cell {
			t.Errorf("%v EV: cell %d, want %d", c.ev, p.Get(5), c.cell)
		}
	}
}

func TestExposureOffTheGridIsRefused(t *testing.T) {
	for _, ev := range []float64{0.5, 3.33, -4, 0.1, -2.33, -3} {
		_, _, err := ApplyRecipe(asShot(t), map[string]any{}, ev, "")
		wantKind(t, err, errs.InvalidRecipe)
	}
	_, err := CheckExposure(0.5)
	if e, _ := errs.As(err); e == nil || e.Message != "exposure 0.5 EV is not valid for a conversion" {
		t.Fatal(err)
	}
}

// -- RAF files -----------------------------------------------------------------------------------

func makeRAF(t *testing.T, name, model string) string {
	t.Helper()
	path, err := fakecam.MakeRAF(filepath.Join(t.TempDir(), name), model)
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func TestInspectRAFAcceptsAnX100VIFrame(t *testing.T) {
	info, err := InspectRAF(makeRAF(t, "DSCF0001.RAF", ""))
	if err != nil {
		t.Fatal(err)
	}
	if info.Model != "X100VI" || info.Firmware != "0131" || info.Size != 4096 {
		t.Fatalf("%+v", info)
	}
}

func TestInspectRAFRefusesOtherBodiesJPEGsAndMissingFiles(t *testing.T) {
	_, err := InspectRAF(makeRAF(t, "xt5.RAF", "X-T5"))
	if e := wantKind(t, err, errs.ConversionFailed); !strings.Contains(e.Message, "X-T5") {
		t.Fatal(e.Message)
	}
	dir := t.TempDir()
	jpeg := filepath.Join(dir, "a.jpg")
	if err := os.WriteFile(jpeg, append([]byte{0xFF, 0xD8, 0xFF, 0xE1}, make([]byte, 100)...), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = InspectRAF(jpeg)
	if e := wantKind(t, err, errs.ConversionFailed); !strings.Contains(e.Message, "JPEG") || !strings.Contains(e.Hint, "RAW") {
		t.Fatal(e.Message, e.Hint)
	}
	_, err = InspectRAF(filepath.Join(dir, "missing.RAF"))
	wantKind(t, err, errs.ConversionFailed)
}

// -- the sequence against the fake camera ----------------------------------------------------------

func setup(t *testing.T) (*RawConverter, *fakecam.FakeX100VI, []byte) {
	t.Helper()
	fake := fakecam.New()
	cam, err := camera.Open(fake)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cam.Close() })
	return NewRawConverter(cam), fake, fakecam.RAFBytes("", 0)
}

func upload(t *testing.T, conv *RawConverter, raf []byte) *Profile {
	t.Helper()
	shot, err := conv.Upload(raf)
	if err != nil {
		t.Fatal(err)
	}
	return shot
}

func develop(t *testing.T, conv *RawConverter, p *Profile, full bool, output string) []byte {
	t.Helper()
	img, err := conv.Develop(p, full, 0, output)
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func rendered(t *testing.T, img []byte) ([]int, bool) {
	t.Helper()
	cells, full, err := fakecam.RenderedCells(img)
	if err != nil {
		t.Fatal(err)
	}
	return cells, full
}

func TestUploadThenDevelopReturnsTheCamerasJPEG(t *testing.T) {
	conv, fake, raf := setup(t)
	shot := upload(t, conv, raf)
	if shot.Get(8) != 11 {
		t.Fatal(shot.Get(8))
	}
	profile, _, err := ApplyRecipe(shot, recipe(t, negativeJSON), 0, "")
	if err != nil {
		t.Fatal(err)
	}
	cells, full := rendered(t, develop(t, conv, profile, false, ""))
	if cells[8] != 20 || cells[6] != 200 || full {
		t.Fatal(cells, full)
	}
	if len(fake.Objects) != 0 { // the output is removed from the camera afterwards
		t.Fatal(fake.Objects)
	}
}

func TestOneUploadServesManyRenders(t *testing.T) {
	conv, fake, raf := setup(t)
	shot := upload(t, conv, raf)
	var films []int
	for _, sim := range []string{"velvia", "acros", "reala_ace"} {
		profile, _, err := ApplyRecipe(shot, map[string]any{"film_simulation": sim}, 0, "")
		if err != nil {
			t.Fatal(err)
		}
		cells, _ := rendered(t, develop(t, conv, profile, true, ""))
		films = append(films, cells[8])
	}
	if !reflect.DeepEqual(films, []int{2, 12, 20}) || fake.Uploads != 1 {
		t.Fatal(films, fake.Uploads)
	}
}

func TestARAFTheCameraRejectsIsExplained(t *testing.T) {
	conv, _, _ := setup(t)
	_, err := conv.Upload(fakecam.RAFBytes("X-T5", 0))
	if e := wantKind(t, err, errs.ConversionFailed); !strings.Contains(e.Message, "did not accept") {
		t.Fatal(e.Message)
	}
}

func TestAConversionThatNeverFinishesTimesOutWithoutRetrying(t *testing.T) {
	conv, fake, raf := setup(t)
	shot := upload(t, conv, raf)
	fake.StallConversion = true
	_, err := conv.Develop(shot, false, 300*time.Millisecond, "")
	e := wantKind(t, err, errs.ConversionFailed)
	if !strings.Contains(e.Hint, "Switch the camera off and on") || e.Message != "the camera produced no image within 0.3 s" {
		t.Fatal(e.Message, e.Hint)
	}
	triggers := 0
	for _, w := range fake.Writes {
		if w.Code == 0xD183 {
			triggers++
		}
	}
	if triggers != 1 {
		t.Fatal(triggers)
	}
}

func TestOnlyTheNewObjectIsFetchedAndDeleted(t *testing.T) {
	conv, fake, raf := setup(t)
	// e.g. left behind by an earlier, interrupted run
	fake.Objects[40] = fakecam.Object{Format: 0x3801, Data: []byte("\xff\xd8leftover\xff\xd9")}
	fake.NextHandle = 41
	shot := upload(t, conv, raf)
	if cells, _ := rendered(t, develop(t, conv, shot, false, "")); cells[8] != 11 {
		t.Fatal(cells)
	}
	if len(fake.Objects) != 1 {
		t.Fatal(fake.Objects)
	}
	if _, ok := fake.Objects[40]; !ok {
		t.Fatal("the leftover object was deleted")
	}
}

func TestOutputFormatsSetTheFileTypeCell(t *testing.T) {
	for _, c := range []struct {
		output   string
		full     bool
		magic    string
		filetype int
	}{
		{"jpeg", false, "\xff\xd8", 7}, {"heif", false, "\x00\x00\x00\x18ftyp", 18},
		{"tiff", true, "II*\x00", 9}, {"tiff16", true, "II*\x00", 11},
	} {
		t.Run(c.output, func(t *testing.T) {
			conv, fake, raf := setup(t)
			shot := upload(t, conv, raf)
			img := develop(t, conv, shot, c.full, c.output)
			if !bytes.HasPrefix(img, []byte(c.magic)) {
				t.Fatalf("%q", img[:8])
			}
			if len(fake.Objects) != 0 || shot.Get(2) != 7 { // the as-shot profile is left alone
				t.Fatal(fake.Objects, shot.Get(2))
			}
			cells, full := rendered(t, img)
			if cells[2] != c.filetype || full != c.full {
				t.Fatal(cells[2], full)
			}
		})
	}
}

func TestTIFFNeedsFullResolution(t *testing.T) {
	conv, _, raf := setup(t)
	shot := upload(t, conv, raf)
	_, err := conv.Develop(shot, false, 0, "tiff")
	if e := wantKind(t, err, errs.InvalidRecipe); !strings.Contains(e.Message, "full resolution") {
		t.Fatal(e.Message)
	}
}

func TestUnknownOutputFormatIsRefused(t *testing.T) {
	conv, _, raf := setup(t)
	shot := upload(t, conv, raf)
	_, err := conv.Develop(shot, false, 0, "png")
	e := wantKind(t, err, errs.InvalidRecipe)
	if e.Message != "unknown output format 'png'" || e.Hint != "Use one of: jpeg, heif, tiff, tiff16." {
		t.Fatal(e.Message, e.Hint)
	}
}

func TestABodyThatIgnoresTheFileTypeIsReported(t *testing.T) {
	conv, fake, raf := setup(t)
	shot := upload(t, conv, raf)
	// pretend half-size TIFF were allowed: the body answers with a JPEG, which must not be saved as TIFF
	saved := OutputFormats["tiff"]
	OutputFormats["tiff"] = OutputFormat{Name: "tiff", FileType: 9, ObjectFormat: 0x380D, Extension: "tif", Label: "TIFF"}
	t.Cleanup(func() { OutputFormats["tiff"] = saved })
	_, err := conv.Develop(shot, false, 0, "tiff")
	if e := wantKind(t, err, errs.ConversionFailed); !strings.Contains(e.Message, "other than TIFF") {
		t.Fatal(e.Message)
	}
	if len(fake.Objects) != 0 {
		t.Fatal(fake.Objects)
	}
}

func TestExposureThatIsNotANumberIsRefused(t *testing.T) {
	for _, ev := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		if _, err := CheckExposure(ev); err == nil {
			t.Errorf("%v accepted", ev)
		} else {
			wantKind(t, err, errs.InvalidRecipe)
		}
		_, _, err := ApplyRecipe(asShot(t), map[string]any{}, ev, "")
		wantKind(t, err, errs.InvalidRecipe)
	}
}

func triggers(fake *fakecam.FakeX100VI) int {
	n := 0
	for _, w := range fake.Writes {
		if w.Code == PropTrigger {
			n++
		}
	}
	return n
}

// ops records every operation that reaches the fake, in order, and lets a test answer some itself.
func ops(fake *fakecam.FakeX100VI, answer func(op uint16, params []uint32) (uint16, []byte, bool)) *[]uint16 {
	seen := &[]uint16{}
	fake.Intercept = func(op uint16, params []uint32, data []byte, next func() (uint16, []byte)) (uint16, []byte) {
		*seen = append(*seen, op)
		if answer != nil {
			if code, d, ok := answer(op, params); ok {
				return code, d
			}
		}
		return next()
	}
	return seen
}

func count(seen []uint16, op uint16) int {
	n := 0
	for _, o := range seen {
		if o == op {
			n++
		}
	}
	return n
}

func TestAfterATimeoutTheConverterNeverTriggersAgain(t *testing.T) {
	conv, fake, raf := setup(t)
	shot := upload(t, conv, raf)
	fake.StallConversion = true
	if _, err := conv.Develop(shot, false, 300*time.Millisecond, ""); err == nil {
		t.Fatal("no timeout")
	}
	fake.StallConversion = false // even once the body would answer again
	_, err := conv.Develop(shot, false, 0, "")
	if e := wantKind(t, err, errs.ConversionFailed); !strings.Contains(e.Message, "never finished") {
		t.Fatal(e.Message)
	}
	if n := triggers(fake); n != 1 {
		t.Fatal(n)
	}
}

func TestATruncatedObjectListWhilePollingIsAnError(t *testing.T) {
	conv, fake, raf := setup(t)
	shot := upload(t, conv, raf)
	triggered := false
	seen := ops(fake, func(op uint16, params []uint32) (uint16, []byte, bool) {
		if op == ptp.OpGetObjectHandles && triggered {
			return 0x2001, []byte{2, 0, 0, 0, 1, 0, 0, 0}, true // two handles announced, one sent
		}
		triggered = triggered || (op == ptp.OpSetDevicePropValue && params[0] == PropTrigger)
		return 0, nil, false
	})
	_, err := conv.Develop(shot, false, 2*time.Second, "")
	if err == nil || !strings.Contains(err.Error(), "truncated list") {
		t.Fatal(err)
	}
	if count(*seen, ptp.OpGetObjectInfo)+count(*seen, ptp.OpGetObject)+count(*seen, ptp.OpDeleteObject) != 0 {
		t.Fatal(*seen)
	}
	if _, err := conv.Develop(shot, false, 0, ""); err == nil || triggers(fake) != 1 {
		t.Fatal("triggered again after an unknown outcome", err)
	}
}

func TestABusyAnswerWhilePollingIsAskedAgain(t *testing.T) {
	conv, fake, raf := setup(t)
	shot := upload(t, conv, raf)
	busy := 2
	triggered := false
	ops(fake, func(op uint16, params []uint32) (uint16, []byte, bool) {
		if op == ptp.OpGetObjectHandles && triggered && busy > 0 {
			busy--
			return 0x2019, nil, true // DeviceBusy, no data
		}
		triggered = triggered || (op == ptp.OpSetDevicePropValue && params[0] == PropTrigger)
		return 0, nil, false
	})
	saved := PollInterval
	PollInterval = time.Millisecond
	t.Cleanup(func() { PollInterval = saved })
	if _, err := conv.Develop(shot, false, 0, ""); err != nil || busy != 0 {
		t.Fatal(err, busy)
	}
}

func TestATruncatedResultDescriptionStopsBeforeDownloadOrDelete(t *testing.T) {
	conv, fake, raf := setup(t)
	shot := upload(t, conv, raf)
	seen := ops(fake, func(op uint16, params []uint32) (uint16, []byte, bool) {
		if op == ptp.OpGetObjectInfo {
			info := make([]byte, 52) // the fixed part, but none of the four strings
			binary.LittleEndian.PutUint16(info[4:], 0x3801)
			binary.LittleEndian.PutUint32(info[8:], 10)
			return 0x2001, info, true
		}
		return 0, nil, false
	})
	_, err := conv.Develop(shot, false, 0, "")
	if err == nil || !strings.Contains(err.Error(), "truncated object description") {
		t.Fatal(err)
	}
	if count(*seen, ptp.OpGetObject) != 0 || count(*seen, ptp.OpDeleteObject) != 0 || len(fake.Objects) != 1 {
		t.Fatal(*seen, fake.Objects)
	}
}

func TestAnUnreadableProfileReadbackNeverTriggers(t *testing.T) {
	for name, readback := range map[string][]byte{
		"empty":   {},
		"short":   fakecam.AsShotProfile[:Table+4*20],
		"garbage": make([]byte, len(fakecam.AsShotProfile)),
	} {
		t.Run(name, func(t *testing.T) {
			conv, fake, raf := setup(t)
			shot := upload(t, conv, raf)
			ops(fake, func(op uint16, params []uint32) (uint16, []byte, bool) {
				if op == ptp.OpGetDevicePropValue && params[0] == camera.ProfileProp {
					return 0x2001, readback, true
				}
				return 0, nil, false
			})
			_, err := conv.Develop(shot, false, 0, "")
			if e := wantKind(t, err, errs.ConversionFailed); !strings.Contains(e.Message, "unreadable conversion settings") {
				t.Fatal(e.Message)
			}
			if n := triggers(fake); n != 0 {
				t.Fatal(n)
			}
		})
	}
}

// The read-back after the write must be as long as the as-shot profile (29 cells, 625 bytes): one cell
// short, the comparison would skip the last cell, so nothing is developed.
func TestAProfileReadbackOneCellShortNeverTriggers(t *testing.T) {
	conv, fake, raf := setup(t)
	shot := upload(t, conv, raf)
	if conv.AsShot.Cells() != 29 {
		t.Fatal(conv.AsShot.Cells())
	}
	fake.Intercept = func(op uint16, params []uint32, data []byte, next func() (uint16, []byte)) (uint16, []byte) {
		code, d := next()
		if op == ptp.OpGetDevicePropValue && params[0] == camera.ProfileProp && len(d) >= 4 {
			d = d[:len(d)-4] // the stored profile, last cell removed
		}
		return code, d
	}
	_, err := conv.Develop(shot, false, 0, "")
	if e := wantKind(t, err, errs.ConversionFailed); !strings.Contains(e.Message, "unreadable conversion settings (621 bytes)") {
		t.Fatal(e.Message)
	}
	if n := triggers(fake); n != 0 {
		t.Fatal(n)
	}

	// Without a known as-shot profile, the camera's own form (29 cells) is the minimum.
	conv.AsShot = nil
	if _, err := conv.Develop(shot, false, 0, ""); err == nil || triggers(fake) != 0 {
		t.Fatal("triggered after a short read-back", err)
	}
}

// timingOut is a camera whose transport times out on the first conversion trigger after the body got
// it, or (refuse) answers it with a clean PTP refusal. Later triggers go through.
type timingOut struct {
	*fakecam.FakeX100VI
	refuse bool
	done   bool
}

func (t *timingOut) Transact(op uint16, params []uint32, data []byte) (uint16, []uint32, []byte, error) {
	if op == ptp.OpSetDevicePropValue && len(params) == 1 && params[0] == PropTrigger && !t.done {
		t.done = true
		if t.refuse {
			return 0x2002, nil, nil, nil // GeneralError: the body did not start
		}
		t.FakeX100VI.Transact(op, params, data) // the body received it and may be converting
		return 0, nil, nil, errs.NotRespondingErr("the camera did not answer within 10 s")
	}
	return t.FakeX100VI.Transact(op, params, data)
}

// A trigger whose outcome is unknown (transport timeout) means a conversion may be running: no
// converter, not even a fresh one on the same camera session, triggers again.
func TestATriggerThatTimesOutIsNeverFollowedByAnother(t *testing.T) {
	fake := fakecam.New()
	fake.StallConversion = true
	cam, err := camera.Open(&timingOut{FakeX100VI: fake})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cam.Close() })
	conv := NewRawConverter(cam)
	shot := upload(t, conv, fakecam.RAFBytes("", 0))
	if _, err := conv.Develop(shot, false, 0, ""); !errs.Is(err, errs.NotResponding) {
		t.Fatal(err)
	}
	if !cam.ConversionMayBeRunning {
		t.Fatal("the camera session does not know a conversion may be running")
	}
	fake.StallConversion = false
	again := NewRawConverter(cam)
	shot = upload(t, again, fakecam.RAFBytes("", 0))
	_, err = again.Develop(shot, false, 0, "")
	if e := wantKind(t, err, errs.ConversionFailed); !strings.Contains(e.Message, "never finished") {
		t.Fatal(e.Message)
	}
	if n := triggers(fake); n != 1 {
		t.Fatal("triggers:", n)
	}
}

// A clean refusal of the trigger means nothing started: a later conversion may go ahead.
func TestARefusedTriggerDoesNotBlockTheNextConversion(t *testing.T) {
	fake := fakecam.New()
	cam, err := camera.Open(&timingOut{FakeX100VI: fake, refuse: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cam.Close() })
	conv := NewRawConverter(cam)
	shot := upload(t, conv, fakecam.RAFBytes("", 0))
	if _, err := conv.Develop(shot, false, 0, ""); !errs.Is(err, errs.CameraRefused) {
		t.Fatal(err)
	}
	if cam.ConversionMayBeRunning {
		t.Fatal("a refused trigger marked a conversion as running")
	}
	develop(t, NewRawConverter(cam), shot, false, "")
}

// After a timeout, a fresh converter on the same camera session is refused too.
func TestAfterATimeoutAFreshConverterNeverTriggersAgain(t *testing.T) {
	conv, fake, raf := setup(t)
	shot := upload(t, conv, raf)
	fake.StallConversion = true
	if _, err := conv.Develop(shot, false, 300*time.Millisecond, ""); err == nil {
		t.Fatal("no timeout")
	}
	fake.StallConversion = false
	if _, err := NewRawConverter(conv.Camera).Develop(shot, false, 0, ""); err == nil || triggers(fake) != 1 {
		t.Fatal("a fresh converter triggered again", err)
	}
}
