//go:build !linux && !windows && !(darwin && cgo)

package transport

import (
	"runtime"

	"github.com/volodymyrsmirnov/x100vi-skill/internal/errs"
	"github.com/volodymyrsmirnov/x100vi-skill/internal/ptp"
)

// Open reports that this build cannot reach a camera.
func Open() (ptp.Transport, error) {
	if runtime.GOOS == "darwin" {
		return nil, errs.New(errs.General, "this build cannot reach a camera: it was built without cgo",
			"On macOS the camera is reached through ImageCaptureCore, which needs a build with CGO_ENABLED=1.")
	}
	return nil, errs.New(errs.General, "talking to a camera is not supported on "+runtime.GOOS,
		"x100vi reaches the camera on macOS, Windows and Linux.")
}
