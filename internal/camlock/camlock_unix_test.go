//go:build darwin || linux || freebsd || netbsd || openbsd || dragonfly || solaris

package camlock

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/volodymyrsmirnov/x100vi-skill/internal/errs"
	"golang.org/x/sys/unix"
)

func clearSandboxEnv(t *testing.T) {
	t.Helper()
	for _, v := range []string{"CODEX_SANDBOX", "CODEX_SANDBOX_NETWORK_DISABLED", "X100VI_SANDBOX"} {
		t.Setenv(v, "")
	}
}

// A FIFO at the lock path must not hang the command (a blocking open waits for a writer forever): the
// open fails at once, naming the path.
func TestAFIFOAtTheLockPathFailsPromptly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "camera.lock")
	if err := unix.Mkfifo(path, 0o666); err != nil {
		t.Skip("no FIFOs here:", err)
	}
	t.Setenv(EnvVar, path)
	done := make(chan error, 1)
	go func() {
		r, err := Acquire()
		if r != nil {
			r()
		}
		done <- err
	}()
	select {
	case err := <-done:
		e, ok := errs.As(err)
		if !ok || e.Kind != errs.General || e.Message != "the camera lock file "+path+" is not a regular file" ||
			!strings.Contains(e.Hint, "Remove it") {
			t.Fatalf("got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("opening a FIFO as the lock file hung")
	}
}

// A symbolic link at the lock path is refused, never followed: whatever it points to is neither opened
// nor created nor has its permissions changed.
func TestASymbolicLinkAtTheLockPathIsRefused(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "camera.lock")
	for _, target := range []string{filepath.Join(dir, "elsewhere"), filepath.Join(dir, "missing")} {
		os.Remove(path)
		if strings.HasSuffix(target, "elsewhere") {
			if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Symlink(target, path); err != nil {
			t.Fatal(err)
		}
		t.Setenv(EnvVar, path)
		r, err := Acquire()
		if r != nil {
			r()
		}
		e, ok := errs.As(err)
		if !ok || e.Kind != errs.General || e.Message != "the camera lock file "+path+" is a symbolic link" {
			t.Fatalf("%s: got %v", target, err)
		}
		st, statErr := os.Stat(target)
		switch {
		case strings.HasSuffix(target, "missing") && !os.IsNotExist(statErr):
			t.Fatalf("the link's target was created: %v", statErr)
		case strings.HasSuffix(target, "elsewhere") && (statErr != nil || st.Mode().Perm() != 0o600):
			t.Fatalf("the link's target was changed: %v %v", st.Mode(), statErr)
		}
	}
}

// A read-only file system is how some sandboxes hide a folder: inside a known sandbox it is reported as
// the sandbox (exit 14), like a permission error.
func TestAReadOnlyFileSystemInsideASandboxIsTheSandboxError(t *testing.T) {
	isolate(t)
	clearSandboxEnv(t)
	saved := openFile
	t.Cleanup(func() { openFile = saved })
	openFile = func(path string) (*os.File, error) {
		return nil, &fs.PathError{Op: "open", Path: path, Err: syscall.EROFS}
	}
	t.Setenv("X100VI_SANDBOX", "Test agent")
	_, err := Acquire()
	e, ok := errs.As(err)
	if !ok || e.Kind != errs.Sandboxed || e.Kind.Exit != 14 {
		t.Fatalf("got %v, want the sandbox error", err)
	}

	// Outside a known sandbox it names the path and still says an agent's sandbox may be the cause.
	t.Setenv("X100VI_SANDBOX", "")
	_, err = Acquire()
	e, ok = errs.As(err)
	if !ok || e.Kind != errs.General || !strings.Contains(e.Message, Path()) || !strings.HasSuffix(e.Hint, sandboxedHint) {
		t.Fatalf("got %v", err)
	}
}
