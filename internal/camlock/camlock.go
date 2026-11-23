// Package camlock keeps two x100vi commands from talking to the camera at the same time.
//
// The operating systems' camera services let several processes share one camera, and the custom
// registers are reached by first selecting one (0xD18C): two commands interleaving would write one
// recipe into the other's register. Every camera command therefore holds an exclusive advisory lock on
// one fixed file for its whole session. The path must not depend on per-process settings such as
// TMPDIR or TEMP (agents and sandboxes set those per process), or two commands would lock different
// files: it is /tmp/x100vi-camera.lock on macOS and Linux, and %ProgramData%\x100vi\camera.lock on
// Windows.
package camlock

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/volodymyrsmirnov/x100vi-skill/internal/errs"
	"github.com/volodymyrsmirnov/x100vi-skill/internal/sandbox"
)

// EnvVar overrides the lock file's path, for tests.
const EnvVar = "X100VI_LOCK_FILE"

var (
	// Timeout is how long Acquire waits for another command to finish. Tests shorten it.
	Timeout = 30 * time.Second
	// RetryEvery is how often Acquire tries again while the lock is held.
	RetryEvery = 100 * time.Millisecond
)

// Path is the lock file: X100VI_LOCK_FILE when set, otherwise the fixed path for this system.
func Path() string {
	if p := os.Getenv(EnvVar); p != "" {
		return p
	}
	return defaultPath(runtime.GOOS, os.Getenv("ProgramData"))
}

// defaultPath is the fixed lock path for an operating system. programData is %ProgramData% on Windows.
func defaultPath(goos, programData string) string {
	if goos == "windows" {
		if programData == "" {
			programData = `C:\ProgramData`
		}
		return programData + `\x100vi\camera.lock`
	}
	return "/tmp/x100vi-camera.lock"
}

// openFile opens the lock file; tests replace it to simulate failures.
var openFile = openLockFile

// sandboxedHint is added when the lock file cannot be opened and no sandbox announced itself: sandboxes
// that do not set an environment variable still show up as a folder that cannot be written.
const sandboxedHint = "If an AI agent ran this command, it may be sandboxed; run it outside the sandbox."

// Acquire takes the camera lock, waiting up to Timeout for another x100vi command to release it.
// release gives it back; it is safe to call more than once.
func Acquire() (release func(), err error) {
	path := Path()
	f, err := openFile(path)
	if err != nil {
		var bad *unusableError
		if errors.As(err, &bad) {
			return nil, errs.Wrap(errs.General, err, "the camera lock file "+path+" "+bad.what,
				"Something other than x100vi's lock file is at "+path+". Remove it, then run the command again.")
		}
		if errors.Is(err, fs.ErrPermission) || readOnlyFS(err) {
			if serr := sandbox.Error(); serr != nil {
				return nil, serr
			}
		}
		return nil, errs.Wrap(errs.General, err, "cannot open the camera lock file "+path+": "+err.Error(),
			"Check that this account may create and read "+path+", then run the command again. "+sandboxedHint)
	}
	deadline := time.Now().Add(Timeout)
	for {
		held, err := tryLock(f)
		if err != nil {
			f.Close()
			return nil, errs.Wrap(errs.General, err, "cannot lock "+path+": "+err.Error(),
				"Run the command again; if it keeps happening, delete that file.")
		}
		if !held {
			break
		}
		if !time.Now().Before(deadline) {
			f.Close()
			return nil, errs.New(errs.CameraBusy, "another x100vi command is using the camera",
				"Wait for it to finish, then run this command again.")
		}
		time.Sleep(RetryEvery)
	}
	done := false
	return func() {
		if done {
			return
		}
		done = true
		unlock(f)
		f.Close()
	}, nil
}

// unusableError is something at the lock path that must not be used as the lock file: a symbolic link
// (which could point anywhere) or a file that is not a regular one (a FIFO would block the open).
type unusableError struct {
	path, what string
	cause      error
}

func (e *unusableError) Error() string { return e.path + " " + e.what }
func (e *unusableError) Unwrap() error { return e.cause }

// openLockFile opens the lock file read-only (enough to lock it), creating it only when it is absent.
//
// An existing file is opened without O_CREAT: Linux with fs.protected_regular refuses O_CREAT on a file
// another user owns in a sticky folder such as /tmp, even when it may be read. A missing file is created
// with O_EXCL and made world-readable despite the umask, so other users' commands can open it later;
// if another process creates it first, the open is retried. On Unix neither open follows a symbolic
// link or blocks, and the result must be a regular file.
func openLockFile(path string) (*os.File, error) {
	var err error
	for attempt := 0; attempt < 5; attempt++ {
		var f *os.File
		f, err = openExisting(path)
		if err == nil || !errors.Is(err, fs.ErrNotExist) {
			return f, err
		}
		if err = os.MkdirAll(filepath.Dir(path), 0o777); err != nil {
			return nil, err
		}
		f, err = createNew(path)
		if err == nil {
			return f, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return nil, err
		}
		// Another process created it between the two opens: open the existing file.
	}
	return nil, err
}
