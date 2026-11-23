// Package errs holds the errors a user can act on. Each has a kind, a stable exit code and a hint.
package errs

import (
	"errors"
	"fmt"
)

const (
	RawConvMode = "USB RAW CONV./BACKUP RESTORE"
	ModeMenu    = "MENU > NETWORK/USB SETTING > CONNECTION MODE > " + RawConvMode
)

// Kind identifies a class of error; its exit code is stable and documented in the skill.
type Kind struct {
	Name string
	Exit int
}

var (
	General           = Kind{"error", 1}
	InvalidRecipe     = Kind{"invalid_recipe", 2}
	SlotOccupied      = Kind{"slot_occupied", 4}
	MissingDependency = Kind{"missing_dependency", 5}
	CameraNotFound    = Kind{"camera_not_found", 10}
	WrongMode         = Kind{"wrong_usb_mode", 11}
	UnsupportedCamera = Kind{"unsupported_camera", 12}
	CameraBusy        = Kind{"camera_busy", 13}
	Sandboxed         = Kind{"sandboxed", 14}
	NotResponding     = Kind{"camera_not_responding", 15}
	ConversionFailed  = Kind{"conversion_failed", 20}
	CameraRefused     = Kind{"camera_refused", 30}
)

// Error is a problem with a message and a hint written for the user.
type Error struct {
	Kind    Kind
	Message string
	Hint    string
	Code    uint16 // PTP response code, for CameraRefused
	cause   error
}

func (e *Error) Error() string { return e.Message }
func (e *Error) Unwrap() error { return e.cause }

// AsMap is the --json form of the error.
func (e *Error) AsMap() map[string]any {
	out := map[string]any{"error": e.Kind.Name, "message": e.Message}
	if e.Hint != "" {
		out["hint"] = e.Hint
	}
	return out
}

// New builds an error of a kind.
func New(kind Kind, message, hint string) *Error {
	return &Error{Kind: kind, Message: message, Hint: hint}
}

// Wrap builds an error of a kind that keeps its cause.
func Wrap(kind Kind, cause error, message, hint string) *Error {
	return &Error{Kind: kind, Message: message, Hint: hint, cause: cause}
}

// Recipe is the common invalid-recipe error.
func Recipe(message, hint string) *Error { return New(InvalidRecipe, message, hint) }

// NotRespondingErr explains a camera that stopped answering, with the power-cycle advice.
func NotRespondingErr(message string) *Error {
	return New(NotResponding, message, "Switch the camera off and on, reconnect it, and run the command again. "+
		"If the power switch does not respond, take the battery out for a few seconds.")
}

// ResponseNames are the standard PTP response codes, for messages.
var ResponseNames = map[uint16]string{
	0x2001: "OK", 0x2002: "GeneralError", 0x2003: "SessionNotOpen", 0x2005: "OperationNotSupported",
	0x2006: "ParameterNotSupported", 0x2009: "InvalidObjectHandle", 0x200A: "DevicePropNotSupported",
	0x200F: "AccessDenied", 0x2019: "DeviceBusy", 0x201B: "InvalidDevicePropFormat",
	0x201C: "InvalidDevicePropValue", 0x201D: "InvalidParameter", 0x201E: "SessionAlreadyOpen",
}

// PTP is a camera that answered a request with something other than OK.
func PTP(code uint16, what, hint string) *Error {
	msg := fmt.Sprintf("%s: camera answered 0x%04X %s", what, code, ResponseNames[code])
	if what == "" {
		msg = fmt.Sprintf("camera answered 0x%04X %s", code, ResponseNames[code])
	}
	e := New(CameraRefused, trimSpace(msg), hint)
	e.Code = code
	return e
}

func trimSpace(s string) string {
	for len(s) > 0 && s[len(s)-1] == ' ' {
		s = s[:len(s)-1]
	}
	return s
}

// As returns the *Error inside err, if any.
func As(err error) (*Error, bool) {
	var e *Error
	ok := errors.As(err, &e)
	return e, ok
}

// Is reports whether err is an *Error of the given kind.
func Is(err error, kind Kind) bool {
	e, ok := As(err)
	return ok && e.Kind == kind
}
