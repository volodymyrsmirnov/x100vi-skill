"""PTP through macOS ImageCaptureCore, the way FUJIFILM X RAW STUDIO for macOS talks to the camera.

macOS' camera service (ptpcamerad) owns every PTP camera and keeps the PTP session open. Apps reach the
camera through it: ICCameraDevice `requestSendPTPCommand:outData:completion:` takes a complete PTP command
container plus the raw bytes of an outgoing data phase, and hands back the incoming data and the response
container. Nothing has to be stopped, and other apps can stay connected.

Needs PyObjC (pyobjc-framework-ImageCaptureCore), installed with the tool on macOS.
"""
from __future__ import annotations

import struct
import time

from .errors import CameraBusyError, CameraNotFoundError, CameraNotRespondingError, MissingDependencyError
from .ptp import CMD, FUJIFILM_VENDOR_ID, RC_OK, RESP, OP_CloseSession, OP_GetObject, OP_OpenSession, sandbox_error

ICDeviceTypeMaskCamera = 0x00000001
ICDeviceLocationTypeMaskAll = 0x0000FF00
SLOW_OPS = {OP_GetObject, 0x900D, 0x100D}   # downloads and uploads of whole files

_DELEGATE_CLASS = None
_BROWSER = None     # (browser, delegate): one per process; a second browser is never told about devices


def _frameworks():
    try:
        import ImageCaptureCore
        import objc
        from Foundation import NSData, NSDate, NSObject, NSRunLoop
    except ImportError as e:
        raise MissingDependencyError(
            "the macOS camera bridge (PyObjC ImageCaptureCore) is not installed",
            "Reinstall the tool: uv tool install --force git+https://github.com/volodymyrsmirnov/x100vi-skill, "
            "or set X100VI_TRANSPORT=usb to talk to the camera over libusb instead.") from e
    return ImageCaptureCore, objc, NSData, NSDate, NSObject, NSRunLoop


def _delegate_class():
    """One delegate class per process (Objective-C class names are global)."""
    global _DELEGATE_CLASS
    if _DELEGATE_CLASS is None:
        _icc, objc, _data, _date, NSObject, _loop = _frameworks()

        class X100VIICCDelegate(NSObject):
            def init(self):
                self = objc.super(X100VIICCDelegate, self).init()
                if self is not None:
                    self.devices, self.opened, self.open_error, self.removed = [], False, None, False
                return self

            # ICDeviceBrowserDelegate
            def deviceBrowser_didAddDevice_moreComing_(self, browser, device, more):
                self.devices.append(device)

            def deviceBrowser_didRemoveDevice_moreGoing_(self, browser, device, more):
                if device in self.devices:
                    self.devices.remove(device)

            # ICDeviceDelegate
            def device_didOpenSessionWithError_(self, device, error):
                self.opened, self.open_error = True, error

            def device_didCloseSessionWithError_(self, device, error):
                pass

            def didRemoveDevice_(self, device):
                self.removed = True

            # ICCameraDeviceDelegate (required methods; the content catalog is not used)
            def cameraDevice_didAddItems_(self, camera, items):
                pass

            def cameraDevice_didRemoveItems_(self, camera, items):
                pass

            def cameraDevice_didRenameItems_(self, camera, items):
                pass

            def cameraDevice_didReceiveThumbnail_forItem_error_(self, camera, thumbnail, item, error):
                pass

            def cameraDevice_didReceiveMetadata_forItem_error_(self, camera, metadata, item, error):
                pass

            def cameraDevice_didReceivePTPEvent_(self, camera, event):
                pass

            def cameraDeviceDidChangeCapability_(self, camera):
                pass

            def cameraDeviceDidRemoveAccessRestriction_(self, device):
                pass

            def cameraDeviceDidEnableAccessRestriction_(self, device):
                pass

            def deviceDidBecomeReadyWithCompleteContentCatalog_(self, device):
                pass

        _DELEGATE_CLASS = X100VIICCDelegate
    return _DELEGATE_CLASS


class IccTransport:
    """A Fujifilm camera reached through ImageCaptureCore."""

    name = "macOS ImageCaptureCore"

    def __init__(self, find_timeout_s: float = 6.0, timeout_s: float = 30.0, slow_timeout_s: float = 300.0):
        icc, _objc, self._NSData, self._NSDate, _obj, self._NSRunLoop = _frameworks()
        self.timeout_s, self.slow_timeout_s = timeout_s, slow_timeout_s
        self.tid = 0
        global _BROWSER
        if _BROWSER is None:
            delegate = _delegate_class().alloc().init()
            browser = icc.ICDeviceBrowser.alloc().init()
            browser.setDelegate_(delegate)
            browser.setBrowsedDeviceTypeMask_(ICDeviceTypeMaskCamera | ICDeviceLocationTypeMaskAll)
            browser.start()
            _BROWSER = (browser, delegate)
        self.browser, self.delegate = _BROWSER
        self.delegate.opened, self.delegate.open_error, self.delegate.removed = False, None, False
        self.camera = None
        self._spin(lambda: self._find() is not None, find_timeout_s)
        camera = self._find()
        if camera is None:
            raise sandbox_error() or CameraNotFoundError(
                "no Fujifilm camera found",
                "Connect the camera with a data-capable USB-C cable and switch it on. If it is connected and "
                "switched on, switch it off and on once: it may have stopped responding.")
        camera.setDelegate_(self.delegate)
        camera.requestOpenSession()
        if not self._spin(lambda: self.delegate.opened, 15):
            raise CameraNotRespondingError("macOS could not open the camera within 15 s")
        if self.delegate.open_error is not None:
            reason = self.delegate.open_error.localizedDescription()
            raise CameraBusyError(f"macOS could not open the camera: {reason}",
                                  "Quit other apps that use the camera, then try again.")
        self.camera = camera

    def _find(self):
        for device in list(self.delegate.devices):
            try:
                speaks_ptp = hasattr(device, "requestSendPTPCommand_outData_completion_")
                if device.usbVendorID() == FUJIFILM_VENDOR_ID and speaks_ptp:
                    return device
            except AttributeError:
                continue
        return None

    def _spin(self, done, timeout_s: float) -> bool:
        end = time.monotonic() + timeout_s
        loop = self._NSRunLoop.currentRunLoop()
        while not done():
            if time.monotonic() >= end:
                return False
            loop.runMode_beforeDate_("kCFRunLoopDefaultMode", self._NSDate.dateWithTimeIntervalSinceNow_(0.02))
        return True

    def transact(self, op: int, params: tuple = (), data_out: bytes | None = None) -> tuple[int, tuple, bytes]:
        if op in (OP_OpenSession, OP_CloseSession):
            return RC_OK, (), b""        # ptpcamerad owns the session
        if self.delegate.removed:
            raise CameraNotRespondingError("the camera was disconnected")
        self.tid = (self.tid + 1) & 0xFFFFFFFF
        command = struct.pack("<IHHI", 12 + 4 * len(params), CMD, op, self.tid)
        command += b"".join(struct.pack("<I", p & 0xFFFFFFFF) for p in params)
        out = data_out or b""
        box: dict = {}

        def finished(data, response, error):
            box["result"] = (bytes(data) if data else b"", bytes(response) if response else b"", error)

        self.camera.requestSendPTPCommand_outData_completion_(
            self._NSData.dataWithBytes_length_(command, len(command)),
            self._NSData.dataWithBytes_length_(out, len(out)), finished)
        limit = self.slow_timeout_s if op in SLOW_OPS else self.timeout_s
        if not self._spin(lambda: "result" in box, limit):
            raise CameraNotRespondingError(f"the camera did not answer request 0x{op:04X} within {limit:g} s")
        data, response, error = box["result"]
        if error is not None:
            raise CameraNotRespondingError(f"macOS reported an error for request 0x{op:04X}: "
                                           f"{error.localizedDescription()}")
        if len(response) < 12:
            raise CameraNotRespondingError(f"the camera sent no response to request 0x{op:04X}")
        length, ctype, code, _tid = struct.unpack_from("<IHHI", response)
        if ctype != RESP:
            raise CameraNotRespondingError(f"unexpected reply type {ctype} to request 0x{op:04X}")
        body = response[12:max(12, min(length, len(response)))]
        count = len(body) // 4
        return code, struct.unpack(f"<{count}I", body[:count * 4]), data

    def close(self) -> None:
        if self.camera is not None:
            self.camera.requestCloseSession()
            self._spin(lambda: False, 0.2)
            self.camera = None
