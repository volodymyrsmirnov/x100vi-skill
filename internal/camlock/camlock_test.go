package camlock

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/volodymyrsmirnov/x100vi-skill/internal/errs"
)

// isolate points the lock at a fresh file, so a real x100vi running alongside is not disturbed.
func isolate(t *testing.T) {
	t.Helper()
	t.Setenv(EnvVar, filepath.Join(t.TempDir(), "camera.lock"))
	saved := Timeout
	t.Cleanup(func() { Timeout = saved })
}

func TestASecondAcquireFailsWhileTheFirstIsHeld(t *testing.T) {
	isolate(t)
	Timeout = 300 * time.Millisecond
	release, err := Acquire()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error)
	go func() {
		r, err := Acquire()
		if r != nil {
			r()
		}
		done <- err
	}()
	err = <-done
	e, ok := errs.As(err)
	if !ok || e.Kind != errs.CameraBusy || e.Message != "another x100vi command is using the camera" ||
		e.Hint != "Wait for it to finish, then run this command again." {
		t.Fatalf("got %v", err)
	}
	release()
	release() // twice is harmless
	again, err := Acquire()
	if err != nil {
		t.Fatal(err)
	}
	again()
}

func TestASecondAcquireWaitsForTheFirstToFinish(t *testing.T) {
	isolate(t)
	Timeout = 10 * time.Second
	release, err := Acquire()
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan error)
	go func() {
		r, err := Acquire()
		if r != nil {
			r()
		}
		got <- err
	}()
	select {
	case err := <-got:
		t.Fatalf("acquired while held: %v", err)
	case <-time.After(400 * time.Millisecond):
	}
	release()
	select {
	case err := <-got:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("never acquired after release")
	}
}

// The lock holds across processes: a child test process cannot take it while this one does.
func TestAnotherProcessCannotTakeTheLock(t *testing.T) {
	if os.Getenv("CAMLOCK_CHILD") == "1" {
		Timeout = 200 * time.Millisecond
		r, err := Acquire()
		if r != nil {
			r()
		}
		if !errs.Is(err, errs.CameraBusy) {
			t.Fatalf("child acquired the lock: %v", err)
		}
		return
	}
	isolate(t)
	release, err := Acquire()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	cmd := exec.Command(os.Args[0], "-test.run=^TestAnotherProcessCannotTakeTheLock$", "-test.count=1")
	cmd.Env = append(os.Environ(), "CAMLOCK_CHILD=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child: %v\n%s", err, out)
	}
}

// The path is fixed per system: TMPDIR and TEMP differ between processes (agents and sandboxes set
// them), and two commands locking different files would not exclude each other.
func TestThePathDoesNotFollowTheTemporaryFolder(t *testing.T) {
	t.Setenv(EnvVar, "")
	for _, v := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(v, t.TempDir())
	}
	want := "/tmp/x100vi-camera.lock"
	if runtime.GOOS == "windows" {
		want = filepath.Join(os.Getenv("ProgramData"), "x100vi", "camera.lock")
		if os.Getenv("ProgramData") == "" {
			want = `C:\ProgramData\x100vi\camera.lock`
		}
	}
	if got := Path(); got != want {
		t.Fatalf("Path() = %q, want %q", got, want)
	}
	for _, c := range []struct{ goos, programData, want string }{
		{"darwin", "", "/tmp/x100vi-camera.lock"},
		{"linux", `D:\Data`, "/tmp/x100vi-camera.lock"},
		{"windows", `D:\Data`, `D:\Data\x100vi\camera.lock`},
		{"windows", "", `C:\ProgramData\x100vi\camera.lock`},
	} {
		if got := defaultPath(c.goos, c.programData); got != c.want {
			t.Errorf("defaultPath(%q, %q) = %q, want %q", c.goos, c.programData, got, c.want)
		}
	}
	t.Setenv(EnvVar, "/somewhere/else.lock")
	if got := Path(); got != "/somewhere/else.lock" {
		t.Fatalf("override ignored: %q", got)
	}
}

// A missing lock file (and its folder) is created, readable and writable by every user despite the umask.
func TestAMissingLockFileIsCreatedForEveryone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "camera.lock")
	t.Setenv(EnvVar, path)
	release, err := Acquire()
	if err != nil {
		t.Fatal(err)
	}
	release()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && st.Mode().Perm() != 0o666 {
		t.Fatalf("mode %v, want 0666", st.Mode().Perm())
	}
}

// An existing lock file is opened without O_CREAT, so a read-only file another user created still works.
func TestAnExistingReadOnlyLockFileIsUsed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("folder permissions work differently on Windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "camera.lock")
	if err := os.WriteFile(path, nil, 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o555); err != nil { // nothing may be created here any more
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	t.Setenv(EnvVar, path)
	release, err := Acquire()
	if err != nil {
		t.Fatal(err)
	}
	release()
}

// Many processes racing to create the file all end up with it open.
func TestConcurrentCreationSucceeds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "camera.lock")
	var wg sync.WaitGroup
	errc := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f, err := openLockFile(path)
			if err == nil {
				f.Close()
			}
			errc <- err
		}()
	}
	wg.Wait()
	close(errc)
	for err := range errc {
		if err != nil {
			t.Fatal(err)
		}
	}
}

// A lock file that cannot be created names its path, or reports the sandbox when one hides the folder.
func TestAPermissionErrorNamesThePathOrTheSandbox(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("folder permissions work differently on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores folder permissions")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	path := filepath.Join(dir, "camera.lock")
	t.Setenv(EnvVar, path)
	for _, v := range []string{"CODEX_SANDBOX", "CODEX_SANDBOX_NETWORK_DISABLED", "X100VI_SANDBOX"} {
		t.Setenv(v, "")
	}

	_, err := Acquire()
	e, ok := errs.As(err)
	if !ok || e.Kind != errs.General || !strings.Contains(e.Message, path) {
		t.Fatalf("got %v", err)
	}
	// No sandbox announced itself, but an agent's sandbox may still be the reason.
	if want := "Check that this account may create and read " + path + ", then run the command again. " +
		"If an AI agent ran this command, it may be sandboxed; run it outside the sandbox."; e.Hint != want {
		t.Fatalf("hint %q", e.Hint)
	}

	t.Setenv("X100VI_SANDBOX", "Test agent")
	_, err = Acquire()
	if !errs.Is(err, errs.Sandboxed) {
		t.Fatalf("got %v, want the sandbox error", err)
	}
}
