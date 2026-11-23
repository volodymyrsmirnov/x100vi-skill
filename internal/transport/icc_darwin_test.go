//go:build darwin && cgo

package transport

import (
	"strings"
	"testing"

	"github.com/volodymyrsmirnov/x100vi-skill/internal/errs"
)

// Tests run off the main thread, where ImageCaptureCore's callbacks never arrive: Open must say so at
// once instead of waiting for a camera. (This also means the test never touches a connected camera.)
func TestOpenOffTheMainThreadFailsFast(t *testing.T) {
	tr, err := Open()
	if err == nil {
		tr.Close()
		t.Fatal("Open succeeded off the main thread")
	}
	if !errs.Is(err, errs.General) || !strings.Contains(err.Error(), "main goroutine") {
		t.Fatalf("err = %v", err)
	}
}
