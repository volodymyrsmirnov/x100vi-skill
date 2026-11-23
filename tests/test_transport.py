"""Choosing how to reach the camera, and the session-level safety guard."""
import pytest

from fake_camera import FakeX100VI
from x100vi import ptp
from x100vi.errors import CameraBusyError, CameraNotFoundError, MissingDependencyError, X100VIError
from x100vi.ptp import OP_GetObject, OP_GetObjectInfo, PTPSession


class Stub:
    def __init__(self, name, error=None):
        self.name, self.error = name, error

    def __call__(self):
        if self.error:
            raise self.error
        return self.name


@pytest.fixture
def stubs(monkeypatch):
    import x100vi.icc
    import x100vi.wpd

    made = {}

    def install(icc=None, wpd=None, usb=None):
        monkeypatch.setattr(x100vi.icc, "IccTransport", Stub("icc", icc))
        monkeypatch.setattr(x100vi.wpd, "WpdTransport", Stub("wpd", wpd))
        monkeypatch.setattr(ptp, "UsbTransport", Stub("usb", usb))
        return made

    return install


@pytest.mark.parametrize(("platform", "expected"), [("darwin", "icc"), ("win32", "wpd"), ("linux", "usb")])
def test_auto_uses_the_system_camera_service(monkeypatch, stubs, platform, expected):
    stubs()
    monkeypatch.setattr(ptp.sys, "platform", platform)
    assert ptp.open_transport("auto") == expected


def test_environment_variable_and_explicit_choice(monkeypatch, stubs):
    stubs()
    monkeypatch.setattr(ptp.sys, "platform", "darwin")
    monkeypatch.setenv("X100VI_TRANSPORT", "usb")
    assert ptp.open_transport() == "usb"
    assert ptp.open_transport("wpd") == "wpd"
    with pytest.raises(X100VIError, match="unknown transport"):
        ptp.open_transport("bluetooth")


def test_auto_falls_back_to_libusb_when_the_bridge_is_missing(monkeypatch, stubs):
    stubs(icc=MissingDependencyError("no PyObjC"))
    monkeypatch.setattr(ptp.sys, "platform", "darwin")
    assert ptp.open_transport("auto") == "usb"


def test_auto_reports_the_native_error_when_libusb_fails_too(monkeypatch, stubs):
    stubs(wpd=CameraNotFoundError("no camera via WPD"), usb=CameraNotFoundError("no camera via libusb"))
    monkeypatch.setattr(ptp.sys, "platform", "win32")
    with pytest.raises(CameraNotFoundError, match="via WPD"):
        ptp.open_transport("auto")


def test_auto_does_not_fall_back_when_the_camera_is_busy(monkeypatch, stubs):
    stubs(icc=CameraBusyError("in use"))
    monkeypatch.setattr(ptp.sys, "platform", "darwin")
    with pytest.raises(CameraBusyError):
        ptp.open_transport("auto")


def test_an_object_is_never_downloaded_before_it_is_described():
    session = PTPSession(FakeX100VI())
    session.open()
    with pytest.raises(X100VIError, match="before reading its description"):
        session.transact(OP_GetObject, (0,))
    assert session.transact(OP_GetObjectInfo, (0,))[0] == 0x2001
    assert session.transact(OP_GetObject, (0,))[0] == 0x2001
