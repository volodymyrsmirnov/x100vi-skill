// Package sandbox knows about agent sandboxes: they hide USB devices, so "no camera" inside one says
// nothing about the camera. It also renders the settings that let agents run camera commands outside.
package sandbox

import (
	"os"

	"github.com/volodymyrsmirnov/x100vi-skill/internal/errs"
)

// Detect returns the name of the agent sandbox this process runs in, if it announces one.
// env is nil to read the real environment.
func Detect(env map[string]string) string {
	get := os.Getenv
	if env != nil {
		get = func(k string) string { return env[k] }
	}
	if get("CODEX_SANDBOX") != "" { // e.g. "seatbelt" on macOS
		return "Codex"
	}
	if get("CODEX_SANDBOX_NETWORK_DISABLED") == "1" {
		return "Codex"
	}
	if v := get("X100VI_SANDBOX"); v != "" { // for other agents and for tests
		return v
	}
	return ""
}

// Error is the error to return instead of "no camera" when a sandbox hides the hardware, or nil.
func Error() error {
	name := Detect(nil)
	if name == "" {
		return nil
	}
	return errs.New(errs.Sandboxed,
		"USB is not reachable: this command is running inside the "+name+" sandbox",
		"This is not a camera or cable problem. Run the same command again outside the sandbox "+
			"(ask for approval to run it unsandboxed). `x100vi sandbox-help` shows how to allow it permanently.")
}
