// Package camlock keeps two x100vi commands from talking to the camera at the same time.
//
// The operating systems' camera services let several processes share one camera, and the custom
// registers are reached by first selecting one (0xD18C): two commands interleaving would write one
// recipe into the other's register. Every camera command therefore holds an exclusive advisory lock on
// a file in the temporary folder for its whole session.
package camlock

import (
	"os"
	"path/filepath"
	"time"

	"github.com/volodymyrsmirnov/x100vi-skill/internal/errs"
)

// FileName is the lock file's name inside os.TempDir().
const FileName = "x100vi-camera.lock"

var (
	// Timeout is how long Acquire waits for another command to finish. Tests shorten it.
	Timeout = 30 * time.Second
	// RetryEvery is how often Acquire tries again while the lock is held.
	RetryEvery = 100 * time.Millisecond
)

// Path is the lock file.
func Path() string { return filepath.Join(os.TempDir(), FileName) }

// Acquire takes the camera lock, waiting up to Timeout for another x100vi command to release it.
// release gives it back; it is safe to call more than once.
func Acquire() (release func(), err error) {
	path := Path()
	// Read-only is enough to lock, and lets another user's command open a file this one created.
	f, err := os.OpenFile(path, os.O_RDONLY|os.O_CREATE, 0o666)
	if err != nil {
		return nil, errs.Wrap(errs.General, err, "cannot open the camera lock file "+path,
			"Check that the temporary folder is writable, then run the command again.")
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
