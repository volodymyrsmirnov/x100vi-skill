//go:build !(darwin || linux || freebsd || netbsd || openbsd || dragonfly || solaris || windows)

package camlock

import "os"

// No advisory locks here (and no camera transport either): every acquire succeeds.
func tryLock(*os.File) (bool, error) { return false, nil }

func unlock(*os.File) {}
