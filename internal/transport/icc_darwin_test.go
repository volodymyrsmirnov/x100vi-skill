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

// The close bookkeeping: a close that timed out leaves its device close-pending, the late callback only
// clears that mark, and a callback answers only the close waiting for its device, once. Removal is per
// device: removing one camera neither ends another camera's close nor clears its pending mark.
func TestCloseCallbacksAreConsumedOnce(t *testing.T) {
	if step := iccCloseCheck(); step != 0 {
		t.Fatalf("close bookkeeping check %d failed (see x_close_check in icc_darwin.go)", step)
	}
}
