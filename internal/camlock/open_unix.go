//go:build darwin || linux || freebsd || netbsd || openbsd || dragonfly || solaris

package camlock

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// openExisting opens the lock file read-only without following a symbolic link and without blocking
// (a FIFO put at the path would otherwise hang the open), then requires a regular file.
func openExisting(path string) (*os.File, error) {
	for {
		fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return nil, openError(path, err)
		}
		return regularFile(fd, path)
	}
}

// createNew creates the lock file exclusively (never through a symbolic link) and makes it readable and
// writable by every user despite the umask, through the descriptor so no other file can be changed.
func createNew(path string) (*os.File, error) {
	for {
		fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o666)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return nil, openError(path, err)
		}
		_ = unix.Fchmod(fd, 0o666) // best effort: the lock works anyway
		return regularFile(fd, path)
	}
}

// regularFile checks that fd is a regular file and wraps it; otherwise it closes fd.
func regularFile(fd int, path string) (*os.File, error) {
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		unix.Close(fd)
		return nil, &os.PathError{Op: "fstat", Path: path, Err: err}
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG {
		unix.Close(fd)
		return nil, &unusableError{path: path, what: "is not a regular file"}
	}
	_ = unix.SetNonblock(fd, false) // O_NONBLOCK was only for the open
	return os.NewFile(uintptr(fd), path), nil
}

// openError names the path; a symbolic link refused by O_NOFOLLOW (ELOOP, EMLINK on FreeBSD) is
// reported as such.
func openError(path string, err error) error {
	if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.EMLINK) {
		return &unusableError{path: path, what: "is a symbolic link", cause: err}
	}
	return &os.PathError{Op: "open", Path: path, Err: err}
}

// readOnlyFS reports a file system mounted read-only, which a sandbox may present instead of EACCES.
func readOnlyFS(err error) bool { return errors.Is(err, unix.EROFS) }
