"""PTP session over a pluggable transport.

`Transport` is one method: run a PTP transaction. `UsbTransport` speaks to a real camera
with pyusb; tests substitute a fake camera that implements the same method.
Only standard PTP requests and generic vendor transactions live here.
"""
from __future__ import annotations

import os
import struct
import subprocess
import sys
import time
from dataclasses import dataclass, field
from typing import Protocol

from .errors import CameraBusyError, CameraNotFoundError, PTPError

FUJIFILM_VENDOR_ID = 0x04CB
CMD, DATA, RESP = 1, 2, 3
RC_OK = 0x2001

OP_GetDeviceInfo, OP_OpenSession, OP_CloseSession = 0x1001, 0x1002, 0x1003
OP_GetObjectHandles, OP_GetObjectInfo, OP_GetObject, OP_DeleteObject = 0x1007, 0x1008, 0x1009, 0x100B
OP_GetDevicePropValue, OP_SetDevicePropValue = 0x1015, 0x1016

U16, STR = 0x0004, 0xFFFF
_FORMATS = {0x0001: "b", 0x0002: "B", 0x0003: "h", 0x0004: "H", 0x0005: "i", 0x0006: "I"}


class Reader:
    """Cursor over a little-endian PTP dataset."""

    def __init__(self, data: bytes, pos: int = 0):
        self.d, self.p = data, pos

    def take(self, fmt: str):
        value = struct.unpack_from("<" + fmt, self.d, self.p)[0]
        self.p += struct.calcsize("<" + fmt)
        return value

    def string(self) -> str:
        n = self.take("B")
        raw = self.d[self.p:self.p + 2 * n]
        self.p += 2 * n
        return raw.decode("utf-16-le").rstrip("\x00")

    def array(self, fmt: str) -> list:
        return [self.take(fmt) for _ in range(self.take("I"))]

    def value(self, dtype: int):
        return self.string() if dtype == STR else self.take(_FORMATS[dtype])


def pack_string(text: str) -> bytes:
    if not text:
        return b"\x00"
    return bytes([len(text) + 1]) + (text + "\x00").encode("utf-16-le")


def pack_value(dtype: int, value) -> bytes:
    return pack_string(str(value)) if dtype == STR else struct.pack("<" + _FORMATS[dtype], int(value))


@dataclass
class DeviceInfo:
    manufacturer: str = ""
    model: str = ""
    version: str = ""
    serial: str = ""
    operations: list[int] = field(default_factory=list)
    props: list[int] = field(default_factory=list)

    @classmethod
    def parse(cls, data: bytes) -> DeviceInfo:
        r = Reader(data)
        r.take("H"), r.take("I"), r.take("H")
        r.string()
        r.take("H")
        info = cls()
        info.operations = r.array("H")
        r.array("H")
        info.props = r.array("H")
        r.array("H"), r.array("H")
        info.manufacturer, info.model, info.version, info.serial = r.string(), r.string(), r.string(), r.string()
        return info

    def pack(self) -> bytes:
        def arr(values):
            return struct.pack("<I", len(values)) + b"".join(struct.pack("<H", v) for v in values)

        out = struct.pack("<HIH", 100, 0x0E, 100) + pack_string("fujifilm.co.jp: 1.0; ") + struct.pack("<H", 0)
        out += arr(self.operations) + arr([]) + arr(self.props) + arr([]) + arr([])
        return out + b"".join(pack_string(s) for s in (self.manufacturer, self.model, self.version, self.serial))


class Transport(Protocol):
    def transact(self, op: int, params: tuple = (), data_out: bytes | None = None) -> tuple[int, tuple, bytes]:
        """Run one PTP transaction and return (response code, response params, data in)."""

    def close(self) -> None: ...


def _usb_backend():
    """libusb from the bundled wheel when available, else whatever the system provides."""
    import usb.backend.libusb1

    override = os.environ.get("X100VI_LIBUSB")
    if override:
        return usb.backend.libusb1.get_backend(find_library=lambda _name: override)
    try:
        import libusb_package

        backend = libusb_package.get_libusb1_backend()
        if backend is not None:
            return backend
    except ImportError:
        pass
    for path in ("/opt/homebrew/lib/libusb-1.0.dylib", "/usr/local/lib/libusb-1.0.dylib"):
        if os.path.exists(path):
            return usb.backend.libusb1.get_backend(find_library=lambda _name, p=path: p)
    return None


_PLATFORM_HINT = {
    "darwin": "On macOS nothing else is needed.",
    "linux": "On Linux, unmount the camera in the file manager (gvfs) and make sure your user may "
             "access USB devices (a udev rule for vendor 04cb, or run with sudo).",
    "win32": "On Windows the camera must use the WinUSB driver: install it for the camera with Zadig "
             "(https://zadig.akeo.ie). While WinUSB is installed, Explorer and X RAW STUDIO cannot see the "
             "camera; uninstall the driver in Device Manager to switch back.",
}


class UsbTransport:
    """PTP containers over the camera's still-image USB interface."""

    def __init__(self, timeout_ms: int = 60000):
        import usb.core
        import usb.util

        self._usb, self._util = usb.core, usb.util
        self.timeout = timeout_ms
        self.tid = 0
        dev = usb.core.find(backend=_usb_backend(), idVendor=FUJIFILM_VENDOR_ID)
        if dev is None:
            raise CameraNotFoundError(
                "no Fujifilm camera found on USB",
                "Connect the camera with a data-capable USB-C cable, switch it on, and make sure it is "
                "not asleep. " + _PLATFORM_HINT.get(sys.platform, _PLATFORM_HINT["linux"]))
        self.dev = dev
        try:
            cfg = dev.get_active_configuration()
            self.intf = next(i for i in cfg if i.bInterfaceClass == 6)
        except (StopIteration, usb.core.USBError) as e:
            raise CameraNotFoundError(
                "the camera does not present a PTP interface",
                "It may be in USB WEBCAM mode or still starting up. Check CONNECTION MODE and replug.") from e
        endpoints = list(self.intf)

        def pick(kind, direction):
            for e in endpoints:
                if (usb.util.endpoint_type(e.bmAttributes) == kind
                        and usb.util.endpoint_direction(e.bEndpointAddress) == direction):
                    return e
            raise CameraNotFoundError("the camera's PTP interface has no bulk endpoints")

        self.ep_out = pick(usb.util.ENDPOINT_TYPE_BULK, usb.util.ENDPOINT_OUT)
        self.ep_in = pick(usb.util.ENDPOINT_TYPE_BULK, usb.util.ENDPOINT_IN)
        self._claim()

    def _claim(self) -> None:
        last: Exception | None = None
        for _ in range(8):
            if sys.platform == "darwin":
                # macOS' ptpcamerad claims every PTP camera; it is restarted on demand.
                subprocess.run(["pkill", "-9", "-x", "ptpcamerad"], check=False,
                               stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            try:
                if sys.platform.startswith("linux") and self.dev.is_kernel_driver_active(self.intf.bInterfaceNumber):
                    self.dev.detach_kernel_driver(self.intf.bInterfaceNumber)
                self._util.claim_interface(self.dev, self.intf.bInterfaceNumber)
                return
            except (self._usb.USBError, NotImplementedError) as e:
                last = e
                time.sleep(0.3)
        raise CameraBusyError(
            f"cannot take control of the camera's USB interface ({last})",
            "Quit FUJIFILM X RAW STUDIO, X Acquire, Image Capture, Photos, Lightroom or any other app "
            "that talks to the camera, then try again. " + _PLATFORM_HINT.get(sys.platform, _PLATFORM_HINT["linux"]))

    def _read_container(self) -> tuple[int, int, bytes]:
        size = max(self.ep_in.wMaxPacketSize, 1 << 20)
        buf = bytes(self.ep_in.read(size, self.timeout))
        while len(buf) < 12:
            buf += bytes(self.ep_in.read(size, self.timeout))
        length, ctype, code, _tid = struct.unpack_from("<IHHI", buf)
        chunks = [buf]
        got = len(buf)
        while got < length:
            part = bytes(self.ep_in.read(size, self.timeout))
            chunks.append(part)
            got += len(part)
        return ctype, code, b"".join(chunks)[12:length]

    def transact(self, op: int, params: tuple = (), data_out: bytes | None = None) -> tuple[int, tuple, bytes]:
        self.tid = (self.tid + 1) & 0xFFFFFFFF
        payload = b"".join(struct.pack("<I", p & 0xFFFFFFFF) for p in params)
        self.ep_out.write(struct.pack("<IHHI", 12 + len(payload), CMD, op, self.tid) + payload, self.timeout)
        if data_out is not None:
            # One data container sent in bulk-sized pieces (a RAF is tens of MB). A short packet
            # ends the transfer, so a zero-length one follows an exact multiple of the packet size.
            mps = self.ep_out.wMaxPacketSize
            total = 12 + len(data_out)
            chunk = mps * 2048
            first = min(len(data_out), chunk - 12)
            self.ep_out.write(struct.pack("<IHHI", total, DATA, op, self.tid) + data_out[:first], self.timeout)
            view = memoryview(data_out)
            for pos in range(first, len(data_out), chunk):
                self.ep_out.write(view[pos:pos + chunk].tobytes(), self.timeout)
            if total % mps == 0:
                self.ep_out.write(b"", self.timeout)
        data_in = b""
        while True:
            ctype, code, body = self._read_container()
            if ctype == DATA:
                data_in = body
            elif ctype == RESP:
                count = len(body) // 4
                return code, struct.unpack(f"<{count}I", body[:count * 4]), data_in

    def close(self) -> None:
        try:
            self._util.release_interface(self.dev, self.intf.bInterfaceNumber)
        finally:
            self._util.dispose_resources(self.dev)


class PTPSession:
    """Standard PTP requests on top of a transport."""

    def __init__(self, transport: Transport):
        self.transport = transport
        self._open = False

    def transact(self, op: int, params: tuple = (), data_out: bytes | None = None) -> tuple[int, tuple, bytes]:
        return self.transport.transact(op, params, data_out)

    def check(self, op: int, params: tuple = (), data_out: bytes | None = None, what: str = "") -> bytes:
        code, _params, data = self.transact(op, params, data_out)
        if code != RC_OK:
            raise PTPError(code, what or f"request 0x{op:04X}")
        return data

    def open(self) -> None:
        code, _, _ = self.transact(OP_OpenSession, (1,))
        if code not in (RC_OK, 0x201E):
            raise PTPError(code, "OpenSession")
        self._open = True

    def close(self) -> None:
        try:
            if self._open:
                self.transact(OP_CloseSession)
        finally:
            self._open = False
            self.transport.close()

    def device_info(self) -> DeviceInfo:
        return DeviceInfo.parse(self.check(OP_GetDeviceInfo, what="GetDeviceInfo"))

    def prop_raw(self, code: int) -> bytes:
        return self.check(OP_GetDevicePropValue, (code,), what=f"read property 0x{code:04X}")

    def get_u16(self, code: int) -> int:
        return struct.unpack_from("<H", self.prop_raw(code))[0]

    def get_string(self, code: int) -> str:
        return Reader(self.prop_raw(code)).string()

    def set_raw(self, code: int, data: bytes, what: str = "") -> None:
        self.check(OP_SetDevicePropValue, (code,), data, what or f"write property 0x{code:04X}")

    def set_u16(self, code: int, value: int) -> None:
        self.set_raw(code, struct.pack("<H", value & 0xFFFF))

    def handles(self) -> list[int]:
        return Reader(self.check(OP_GetObjectHandles, (0xFFFFFFFF, 0, 0), what="GetObjectHandles")).array("I")

    def __enter__(self) -> PTPSession:
        self.open()
        return self

    def __exit__(self, *exc) -> None:
        self.close()
