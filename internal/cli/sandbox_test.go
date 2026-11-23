package cli

// Agent sandboxes hide USB (the CLI half of tests/test_sandbox.py), and how the command line treats
// whatever the transport reports when connecting (the CLI half of tests/test_transport.py).

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/volodymyrsmirnov/x100vi-skill/internal/errs"
	"github.com/volodymyrsmirnov/x100vi-skill/internal/fakecam"
	"github.com/volodymyrsmirnov/x100vi-skill/internal/ptp"
	"github.com/volodymyrsmirnov/x100vi-skill/internal/sandbox"
)

// clearSandbox makes the test run as if outside any agent sandbox.
func clearSandbox(t *testing.T) {
	for _, v := range []string{"CODEX_SANDBOX", "CODEX_SANDBOX_NETWORK_DISABLED", "X100VI_SANDBOX"} {
		t.Setenv(v, "")
	}
}

// transportOpen is what the real transports do when they find no camera: inside a sandbox the
// sandbox is the reason, outside it the camera is not found.
func transportOpen() (ptp.Transport, error) {
	if err := sandbox.Error(); err != nil {
		return nil, err
	}
	return nil, errs.New(errs.CameraNotFound, "no Fujifilm camera found", "Connect the camera.")
}

func TestDoctorReportsSandboxedAsJSON(t *testing.T) {
	isolate(t)
	clearSandbox(t)
	t.Setenv("X100VI_SANDBOX", "Example")
	r := runWith(t, transportOpen, "doctor", "--json")
	wantCode(t, r, 14)
	j := r.json(t)
	if j["ready"] != false || j["error"] != "sandboxed" {
		t.Errorf("doctor = %v", j)
	}
	mustContain(t, j["message"].(string), "Example sandbox")
	mustContain(t, j["hint"].(string), "not a camera or cable problem")

	r = runWith(t, transportOpen, "doctor")
	wantCode(t, r, 14)
	mustContain(t, r.out, "not ready: USB is not reachable", "outside the sandbox")

	r = runWith(t, transportOpen, "get", "c1")
	wantCode(t, r, 14)
	mustContain(t, r.err, "error: USB is not reachable", "hint: This is not a camera or cable problem")
}

func TestOutsideASandboxItIsStillCameraNotFound(t *testing.T) {
	isolate(t)
	clearSandbox(t)
	r := runWith(t, transportOpen, "doctor", "--json")
	wantCode(t, r, 10)
	j := r.json(t)
	if j["error"] != "camera_not_found" || strings.Contains(j["hint"].(string), "sandbox") {
		t.Errorf("doctor = %v", j)
	}
}

func TestRulesCoverTheLauncherThatInstallSkillPutUnderACustomCodexHome(t *testing.T) {
	isolate(t)
	clearSandbox(t)
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CODEX_HOME", filepath.Join(tmp, "custom-codex"))
	wantCode(t, runWith(t, boom(t), "install-skill", "--codex"), 0)
	launcher := filepath.Join(tmp, "custom-codex", "skills", "x100vi", "scripts", "x100vi.sh")
	if !exists(launcher) {
		t.Fatalf("%s is missing", launcher)
	}
	r := runWith(t, boom(t), "sandbox-help", "--write-codex-rules", "--json")
	wantCode(t, r, 0)
	rules, err := os.ReadFile(filepath.Join(tmp, "custom-codex", "rules", "x100vi.rules"))
	if err != nil {
		t.Fatal(err)
	}
	quoted := `"` + strings.ReplaceAll(strings.ReplaceAll(launcher, `\`, `\\`), `"`, `\"`) + `"`
	j := r.json(t)
	if get(j, "codex", "written") == nil {
		t.Error("written is null")
	}
	mustContain(t, string(rules), quoted)
	excluded := strs(get(j, "claude_code", "settings", "sandbox", "excludedCommands"))
	if !slices.Contains(excluded, "sh "+launcher+" *") {
		t.Errorf("excludedCommands = %v", excluded)
	}
}

func TestSandboxHelpPrintsAndOnlyWritesWhenAsked(t *testing.T) {
	isolate(t)
	clearSandbox(t)
	codex := filepath.Join(t.TempDir(), "codex")
	t.Setenv("CODEX_HOME", codex)
	target := filepath.Join(codex, "rules", "x100vi.rules")
	r := runWith(t, boom(t), "sandbox-help", "--json")
	wantCode(t, r, 0)
	j := r.json(t)
	if get(j, "codex", "rules_file") != target || get(j, "codex", "written") != nil || j["running_in_sandbox"] != nil {
		t.Errorf("sandbox-help = %v", j)
	}
	if exists(target) {
		t.Error("the rules file was written without --write-codex-rules")
	}
	if get(j, "claude_code", "settings", "sandbox", "excludedCommands") == nil {
		t.Error("no excludedCommands")
	}
	text := runWith(t, boom(t), "sandbox-help").out
	mustContain(t, text, "prefix_rule(", "excludedCommands", "approve", "This command is not sandboxed.")

	r = runWith(t, boom(t), "sandbox-help", "--write-codex-rules", "--json")
	wantCode(t, r, 0)
	if w := get(r.json(t), "codex", "written"); w != target {
		t.Errorf("written = %v", w)
	}
	rules, err := os.ReadFile(target)
	if err != nil || strings.Count(string(rules), "prefix_rule(") < 2 {
		t.Errorf("rules file: %v\n%s", err, rules)
	}
	mustContain(t, runWith(t, boom(t), "sandbox-help", "--write-codex-rules").out, "wrote "+target)
}

func TestSandboxHelpSaysWhichSandboxItRunsIn(t *testing.T) {
	isolate(t)
	clearSandbox(t)
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("CODEX_SANDBOX", "seatbelt")
	r := runWith(t, boom(t), "sandbox-help", "--json")
	wantCode(t, r, 0)
	if v := r.json(t)["running_in_sandbox"]; v != "Codex" {
		t.Errorf("running_in_sandbox = %v", v)
	}
	mustContain(t, runWith(t, boom(t), "sandbox-help").out, "running inside the Codex sandbox")
}

// -- connecting (tests/test_transport.py) ------------------------------------------------------------

// Whatever the transport reports when connecting reaches the user with its own exit code and hint.
func TestConnectionErrorsKeepTheirKind(t *testing.T) {
	isolate(t)
	busy := failing(errs.New(errs.CameraBusy, "macOS could not open the camera: in use", "Quit other apps that use the camera."))
	r := runWith(t, busy, "get", "c1")
	wantCode(t, r, 13)
	mustContain(t, r.err, "error: macOS could not open the camera: in use", "hint: Quit other apps")
	r = runWith(t, busy, "doctor", "--json")
	wantCode(t, r, 13)
	if j := r.json(t); j["ready"] != false || j["error"] != "camera_busy" {
		t.Errorf("doctor = %v", j)
	}
	r = runWith(t, busy, "backup", "--json")
	wantCode(t, r, 13)
	if j := r.json(t); j["error"] != "camera_busy" {
		t.Errorf("backup = %v", j)
	}
}

// An unexpected error (not one written for the user) still exits 1 with an `error:` line.
func TestUnexpectedErrorsExitOne(t *testing.T) {
	isolate(t)
	r := runWith(t, failing(errors.New("libusb exploded")), "get", "c1")
	wantCode(t, r, 1)
	mustContain(t, r.err, "error: libusb exploded")
	r = runWith(t, failing(errors.New("libusb exploded")), "doctor", "--json")
	wantCode(t, r, 1)
	if j := r.json(t); j["error"] != "error" {
		t.Errorf("doctor = %v", j)
	}
}

// The session is closed and the transport released after every command, also when it fails.
func TestTheCameraIsReleasedAfterEveryCommand(t *testing.T) {
	c := newRunner(t)
	wantCode(t, c.run("get", "c1"), 0)
	if !c.fake.Closed || c.fake.SessionOpen {
		t.Errorf("after get: closed = %v, session open = %v", c.fake.Closed, c.fake.SessionOpen)
	}
	wrong := fakecam.NewWith(fakecam.Options{Mode: "card_reader"})
	wantCode(t, c.on(wrong, "set", "c1", "everyday"), 11)
	if !wrong.Closed || wrong.SessionOpen {
		t.Errorf("after a failed set: closed = %v, session open = %v", wrong.Closed, wrong.SessionOpen)
	}
	doctor := fakecam.NewWith(fakecam.Options{Model: "X-T5"})
	wantCode(t, c.on(doctor, "doctor"), 12)
	if !doctor.Closed {
		t.Error("doctor left the transport open")
	}
}
