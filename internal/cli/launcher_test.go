package cli

// The POSIX launcher bundled with the skill (skills/x100vi/scripts/x100vi.sh): run X100VI_BIN if set,
// else download this platform's release binary once (checksum verified) into a cache and run that.
// It replaces tests/test_launcher.py, which covered the old uv/pipx installer.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const launcherPath = "../../skills/x100vi/scripts/x100vi.sh"

// launcherPATH is the PATH the launcher runs with: system tools only, like a fresh agent shell.
const launcherPATH = "/usr/bin:/bin:/usr/sbin:/sbin"

type launcher struct {
	t       *testing.T
	script  string
	home    string
	cache   string
	release string // a local release folder, served as file://
	asset   string // this platform's release asset name
}

func newLauncher(t *testing.T) *launcher {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell launcher; Windows uses x100vi.ps1")
	}
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh")
	}
	script, err := filepath.Abs(launcherPath)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	l := &launcher{t: t, script: script, home: filepath.Join(root, "home"), cache: filepath.Join(root, "cache"),
		release: filepath.Join(root, "release")}
	for _, d := range []string{l.home, l.release} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	l.asset = assetName(t)
	return l
}

// assetName mirrors the launcher's choice of release asset for this machine.
func assetName(t *testing.T) string {
	t.Helper()
	uname := func(flag string) string {
		out, err := exec.Command("uname", flag).Output()
		if err != nil {
			t.Skipf("uname %s: %v", flag, err)
		}
		return strings.TrimSpace(string(out))
	}
	switch s := uname("-s"); {
	case s == "Darwin":
		return "x100vi-darwin-universal"
	case s == "Linux":
		switch uname("-m") {
		case "x86_64", "amd64":
			return "x100vi-linux-amd64"
		case "aarch64", "arm64":
			return "x100vi-linux-arm64"
		}
	}
	t.Skip("the launcher has no release build for this platform")
	return ""
}

// releaseURL is the release folder as a file:// URL. It skips the test when the launcher's
// downloader cannot read file:// URLs here (it needs curl; wget has no file:// support).
func (l *launcher) releaseURL() string {
	l.t.Helper()
	u := (&url.URL{Scheme: "file", Path: filepath.ToSlash(l.release)}).String()
	probe := filepath.Join(l.release, "probe.txt")
	if err := os.WriteFile(probe, []byte("ok"), 0o644); err != nil {
		l.t.Fatal(err)
	}
	defer os.Remove(probe)
	got := filepath.Join(l.t.TempDir(), "probe.out")
	cmd := exec.Command("/bin/sh", "-c", `command -v curl >/dev/null 2>&1 || exit 3; curl -fsSL -o "$1" "$2"`,
		"probe", got, u+"/probe.txt")
	cmd.Env = []string{"PATH=" + launcherPATH, "HOME=" + l.home}
	if out, err := cmd.CombinedOutput(); err != nil {
		if code := cmd.ProcessState.ExitCode(); code == 3 {
			l.t.Skip("curl is not on " + launcherPATH + ": the launcher would use wget, which cannot read the file:// test release")
		}
		l.t.Skipf("curl cannot read a file:// URL on this machine (%v: %s), so a local test release cannot be served", err, out)
	}
	return u
}

// fakeTool is a tiny shell script standing in for the x100vi binary.
func fakeTool(version string) []byte {
	return []byte("#!/bin/sh\n" +
		`if [ "${1:-}" = "--version" ]; then echo "x100vi ` + version + `"; exit 0; fi` + "\n" +
		`printf "%s|" "$@"` + "\n" +
		"exit 0\n")
}

func sum(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// publish puts a release in the release folder: the asset plus a SHA256SUMS that lists sumFor (the
// asset's real checksum when "") for it, next to another platform's entry.
func (l *launcher) publish(tool []byte, sumFor string) {
	l.t.Helper()
	if sumFor == "" {
		sumFor = sum(tool)
	}
	sums := sum([]byte("other")) + "  x100vi-other-platform\n" + sumFor + "  " + l.asset + "\n"
	if err := os.WriteFile(filepath.Join(l.release, l.asset), tool, 0o644); err != nil {
		l.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(l.release, "SHA256SUMS"), []byte(sums), 0o644); err != nil {
		l.t.Fatal(err)
	}
}

// cacheFiles lists everything in the cache folder (which may not exist).
func (l *launcher) cacheFiles() []string {
	entries, _ := os.ReadDir(l.cache)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func (l *launcher) cached() []byte {
	b, _ := os.ReadFile(filepath.Join(l.cache, l.asset))
	return b
}

// run runs `sh x100vi.sh args...` with a clean environment plus extra KEY=VALUE pairs.
func (l *launcher) run(extra []string, args ...string) result {
	l.t.Helper()
	cmd := exec.Command("/bin/sh", append([]string{l.script}, args...)...)
	cmd.Env = append([]string{"PATH=" + launcherPATH, "HOME=" + l.home, "X100VI_CACHE=" + l.cache}, extra...)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	if err != nil {
		if _, ok := err.(*exec.ExitError); !ok {
			l.t.Fatalf("running the launcher: %v", err)
		}
	}
	return result{cmd.ProcessState.ExitCode(), out.String(), errOut.String()}
}

func TestLauncherRunsX100VIBinWithArgumentsAndExitCode(t *testing.T) {
	l := newLauncher(t)
	bin := filepath.Join(t.TempDir(), "my x100vi")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nprintf \"%s|\" \"$@\"; exit 11\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := l.run([]string{"X100VI_BIN=" + bin, "X100VI_RELEASE_URL=file:///nonexistent"}, "preview", "my shot.RAF", "-r", "a b.json")
	wantCode(t, r, 11)
	if r.out != "preview|my shot.RAF|-r|a b.json|" || r.err != "" {
		t.Errorf("stdout %q, stderr %q", r.out, r.err)
	}
	if len(l.cacheFiles()) != 0 {
		t.Errorf("X100VI_BIN still touched the cache: %v", l.cacheFiles())
	}
}

func TestLauncherDownloadsVerifiesCachesAndRuns(t *testing.T) {
	l := newLauncher(t)
	release := "X100VI_RELEASE_URL=" + l.releaseURL()
	tool := fakeTool("1.0.0")
	l.publish(tool, "")
	r := l.run([]string{release}, "doctor", "--json")
	wantCode(t, r, 0)
	if r.out != "doctor|--json|" { // download chatter goes to stderr, stdout stays clean for --json
		t.Errorf("stdout %q", r.out)
	}
	mustContain(t, r.err, "downloading "+l.asset)
	if !bytes.Equal(l.cached(), tool) {
		t.Fatalf("cache holds %v", l.cacheFiles())
	}
	if info, err := os.Stat(filepath.Join(l.cache, l.asset)); err != nil || info.Mode()&0o111 == 0 {
		t.Errorf("cached tool is not executable: %v", err)
	}
	if files := l.cacheFiles(); len(files) != 1 {
		t.Errorf("leftovers in the cache: %v", files)
	}

	// Second run: the cached copy is used, nothing is downloaded (the release is gone).
	if err := os.RemoveAll(l.release); err != nil {
		t.Fatal(err)
	}
	r = l.run([]string{release}, "get", "c1")
	wantCode(t, r, 0)
	if r.out != "get|c1|" || r.err != "" {
		t.Errorf("stdout %q, stderr %q", r.out, r.err)
	}
}

func TestLauncherRefusesAChecksumMismatch(t *testing.T) {
	for _, tc := range []struct{ name, listed string }{
		{"wrong checksum", sum([]byte("something else"))},
		{"not listed", "-"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := newLauncher(t)
			release := "X100VI_RELEASE_URL=" + l.releaseURL()
			l.publish(fakeTool("1.0.0"), tc.listed)
			if tc.listed == "-" {
				if err := os.WriteFile(filepath.Join(l.release, "SHA256SUMS"), []byte(sum(nil)+"  x100vi-other-platform\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			r := l.run([]string{release}, "doctor")
			wantCode(t, r, 127)
			mustContain(t, r.err, "does not match the release checksum", "not installed")
			if r.out != "" {
				t.Errorf("stdout %q", r.out)
			}
			if files := l.cacheFiles(); len(files) != 0 {
				t.Errorf("cache holds %v", files)
			}
		})
	}
}

func TestLauncherExplainsAFailedDownload(t *testing.T) {
	l := newLauncher(t)
	r := l.run([]string{"X100VI_RELEASE_URL=" + l.releaseURL()}, "doctor") // the release folder is empty
	wantCode(t, r, 127)
	mustContain(t, r.err, "could not download the x100vi tool", "hint:", "X100VI_BIN")
	if r.out != "" || len(l.cacheFiles()) != 0 {
		t.Errorf("stdout %q, cache %v", r.out, l.cacheFiles())
	}
}

func TestLauncherCannotDownloadInsideASandbox(t *testing.T) {
	l := newLauncher(t)
	release := "X100VI_RELEASE_URL=" + l.releaseURL()
	l.publish(fakeTool("1.0.0"), "")
	for _, sandboxVar := range []string{"CODEX_SANDBOX=seatbelt", "CODEX_SANDBOX_NETWORK_DISABLED=1", "X100VI_SANDBOX=Example"} {
		r := l.run([]string{release, sandboxVar}, "doctor", "--json")
		wantCode(t, r, 14)
		if r.out != "" {
			t.Errorf("%s: stdout %q", sandboxVar, r.out)
		}
		mustContain(t, r.err, "cannot be downloaded from inside the sandbox", "outside the sandbox")
		if files := l.cacheFiles(); len(files) != 0 {
			t.Errorf("%s: cache holds %v", sandboxVar, files)
		}
	}
	// Once downloaded, the cached tool runs inside the sandbox too.
	wantCode(t, l.run([]string{release}, "--version"), 0)
	r := l.run([]string{release, "CODEX_SANDBOX=seatbelt"}, "doctor", "--json")
	wantCode(t, r, 0)
	if r.out != "doctor|--json|" {
		t.Errorf("stdout %q", r.out)
	}
}

func TestLauncherSelfUpdateDownloadsAgain(t *testing.T) {
	l := newLauncher(t)
	release := "X100VI_RELEASE_URL=" + l.releaseURL()
	l.publish(fakeTool("1.0.0"), "")
	r := l.run([]string{release}, "--version")
	wantCode(t, r, 0)
	if r.out != "x100vi 1.0.0\n" {
		t.Fatalf("stdout %q", r.out)
	}
	newer := fakeTool("2.0.0")
	l.publish(newer, "")
	if r := l.run([]string{release}, "--version"); r.out != "x100vi 1.0.0\n" { // cached: no update by itself
		t.Errorf("without --self-update: stdout %q", r.out)
	}
	r = l.run([]string{release}, "--self-update")
	wantCode(t, r, 0)
	if r.out != "x100vi 2.0.0\n" {
		t.Errorf("stdout %q", r.out)
	}
	mustContain(t, r.err, "downloading")
	if !bytes.Equal(l.cached(), newer) {
		t.Error("the cache still holds the old tool")
	}
	if files := l.cacheFiles(); len(files) != 1 {
		t.Errorf("leftovers in the cache: %v", files)
	}

	// A self-update that fails verification keeps the working copy.
	l.publish(fakeTool("3.0.0"), sum([]byte("tampered")))
	r = l.run([]string{release}, "--self-update")
	wantCode(t, r, 127)
	if !bytes.Equal(l.cached(), newer) {
		t.Error("a failed self-update replaced the cached tool")
	}
}

func TestLauncherUsesNoHereDocuments(t *testing.T) {
	// A here-document makes the shell create a temp file, which a command sandbox refuses.
	text, err := os.ReadFile(launcherPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(text), "<<") {
		t.Error("x100vi.sh uses a here-document")
	}
}

// The PowerShell launcher cannot run here, so its download is checked by reading it: every temporary
// file is per process and removed in a finally block, and the binary still lands in the cache by a
// forced move.
func TestPowerShellLauncherDownloadsToPerProcessFiles(t *testing.T) {
	body, err := os.ReadFile("../../skills/x100vi/scripts/x100vi.ps1")
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, want := range []string{"$PID", "NewGuid()", `$tmp = "$Tool.$id.download"`, `"SHA256SUMS.$id"`,
		"} finally {", "Remove-Item -Force -ErrorAction SilentlyContinue -LiteralPath $tmp, $sums",
		"Move-Item -Force $tmp $Tool"} {
		if !strings.Contains(text, want) {
			t.Errorf("x100vi.ps1 lacks %q", want)
		}
	}
	if strings.Contains(text, `"$Tool.download"`) || strings.Contains(text, `Join-Path $Cache "SHA256SUMS"`) {
		t.Error("x100vi.ps1 still uses a shared temporary file")
	}
	if strings.Index(text, "Move-Item -Force $tmp $Tool") > strings.Index(text, "} finally {") {
		t.Error("the move into the cache must happen inside the try block")
	}
}
