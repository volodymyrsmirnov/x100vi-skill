"""Errors the user can act on. Each carries a hint and maps to a stable exit code."""
from __future__ import annotations

RAW_CONV_MODE = "USB RAW CONV./BACKUP RESTORE"
MODE_MENU = f"MENU > NETWORK/USB SETTING > CONNECTION MODE > {RAW_CONV_MODE}"


class X100VIError(Exception):
    """Base class: `str(error)` is the problem, `hint` is what to do about it."""

    exit_code = 1
    kind = "error"

    def __init__(self, message: str, hint: str = ""):
        super().__init__(message)
        self.hint = hint

    def as_dict(self) -> dict:
        out = {"error": self.kind, "message": str(self)}
        if self.hint:
            out["hint"] = self.hint
        return out


class RecipeError(X100VIError, ValueError):
    """A recipe (or a command argument) is not something the camera can hold."""

    exit_code = 2
    kind = "invalid_recipe"


class SlotOccupiedError(X100VIError):
    exit_code = 4
    kind = "slot_occupied"


class MissingDependencyError(X100VIError):
    exit_code = 5
    kind = "missing_dependency"


class CameraNotFoundError(X100VIError):
    exit_code = 10
    kind = "camera_not_found"


class WrongModeError(X100VIError):
    exit_code = 11
    kind = "wrong_usb_mode"


class UnsupportedCameraError(X100VIError):
    exit_code = 12
    kind = "unsupported_camera"


class CameraBusyError(X100VIError):
    exit_code = 13
    kind = "camera_busy"


class SandboxedError(X100VIError):
    """The command runs inside an agent's sandbox, where USB devices are invisible."""

    exit_code = 14
    kind = "sandboxed"


class CameraNotRespondingError(X100VIError):
    """The camera stopped answering in the middle of a session."""

    exit_code = 15
    kind = "camera_not_responding"

    def __init__(self, message: str, hint: str = ""):
        super().__init__(message, hint or (
            "Switch the camera off and on, reconnect it, and run the command again. If the power switch does "
            "not respond, take the battery out for a few seconds."))


class ConversionError(X100VIError):
    exit_code = 20
    kind = "conversion_failed"


class PTPError(X100VIError):
    """The camera answered a PTP request with something other than OK."""

    exit_code = 30
    kind = "camera_refused"

    def __init__(self, code: int, what: str = "", hint: str = ""):
        self.code = code
        name = RESPONSE_NAMES.get(code, "")
        super().__init__(f"{what}: camera answered 0x{code:04X} {name}".strip(), hint)


RESPONSE_NAMES = {
    0x2001: "OK", 0x2002: "GeneralError", 0x2003: "SessionNotOpen", 0x2005: "OperationNotSupported",
    0x2006: "ParameterNotSupported", 0x2009: "InvalidObjectHandle", 0x200A: "DevicePropNotSupported",
    0x200F: "AccessDenied", 0x2019: "DeviceBusy", 0x201B: "InvalidDevicePropFormat",
    0x201C: "InvalidDevicePropValue", 0x201D: "InvalidParameter", 0x201E: "SessionAlreadyOpen",
}
