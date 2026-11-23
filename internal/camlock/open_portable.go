//go:build !(darwin || linux || freebsd || netbsd || openbsd || dragonfly || solaris)

package camlock

import "os"

// openExisting opens the lock file read-only (enough to lock it).
func openExisting(path string) (*os.File, error) { return os.OpenFile(path, os.O_RDONLY, 0) }

// createNew creates the lock file exclusively and makes it writable by every user.
func createNew(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|os.O_CREATE|os.O_EXCL, 0o666)
	if err == nil {
		_ = os.Chmod(path, 0o666) // best effort: the lock works anyway
	}
	return f, err
}

// readOnlyFS: such systems report a read-only volume through their own codes, handled as permission
// errors or not at all.
func readOnlyFS(error) bool { return false }
