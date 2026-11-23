"""PTP through Windows Portable Devices, the way FUJIFILM X RAW STUDIO for Windows talks to the camera.

Windows binds PTP cameras to its own MTP/PTP driver and exposes them as Portable Devices. Raw PTP
requests go through IPortableDevice::SendCommand with the MTP vendor-extension commands, so no extra
driver (WinUSB, Zadig) is needed and Explorer keeps working. The driver owns the PTP session.

The property keys below are the ones Fujifilm's FTLPTP.dll (WPD build) uses: commands 12-17 and
properties 1001-1013 of WPD_CATEGORY_MTP_EXT_VENDOR_OPERATIONS. Needs comtypes, installed with the tool
on Windows.
"""
from __future__ import annotations

import contextlib
import ctypes
import re

from .errors import CameraBusyError, CameraNotFoundError, CameraNotRespondingError, MissingDependencyError
from .ptp import OP_CloseSession, OP_OpenSession, sandbox_error

MTP_EXT = "{4D545058-1A2E-4106-A357-771E0819FC56}"     # WPD_CATEGORY_MTP_EXT_VENDOR_OPERATIONS
COMMON = "{F0422A9C-5DC8-4440-B5BD-5DF28835658A}"      # WPD_CATEGORY_COMMON
CLIENT = "{204D9F0C-2292-4080-9F42-40664E70F859}"      # WPD_CLIENT_INFORMATION_PROPERTIES

CMD_WITHOUT_DATA, CMD_DATA_TO_READ, CMD_DATA_TO_WRITE, CMD_READ, CMD_WRITE, CMD_END = 12, 13, 14, 15, 16, 17
P_OPERATION_CODE, P_OPERATION_PARAMS, P_RESPONSE_CODE = 1001, 1002, 1003
P_CONTEXT, P_TOTAL_SIZE, P_BYTES_TO_READ, P_BYTES_TO_WRITE = 1006, 1007, 1008, 1010
P_DATA, P_OPTIMAL_BUFFER = 1012, 1013
C_CATEGORY, C_ID, C_HRESULT = 1001, 1002, 1003
CLIENT_QOS, CLIENT_ACCESS = 8, 9
SECURITY_IMPERSONATION = 0x00020000
GENERIC_READ_WRITE = 0x80000000 | 0x40000000
VT_UI4 = 19
DEFAULT_CHUNK = 256 * 1024

# Requests whose answer carries a data phase (all others either send data or have none).
DATA_IN_OPS = {0x1001, 0x1004, 0x1005, 0x1007, 0x1008, 0x1009, 0x100A, 0x1014, 0x1015, 0x101B}
VENDOR_ID = re.compile(r"vid_0*4cb&", re.IGNORECASE)


def _com():
    try:
        import comtypes
        import comtypes.client

        comtypes.client.GetModule("portabledeviceapi.dll")
        comtypes.client.GetModule("portabledevicetypes.dll")
        from comtypes.gen import PortableDeviceApiLib as api
        from comtypes.gen import PortableDeviceTypesLib as types
    except (ImportError, OSError) as e:
        raise MissingDependencyError(
            "Windows Portable Devices is not available to the tool",
            "Reinstall the tool (it needs comtypes), or set X100VI_TRANSPORT=usb to use libusb with the "
            "WinUSB driver instead.") from e
    return comtypes, api, types


class WpdTransport:
    """A Fujifilm camera reached through Windows Portable Devices."""

    name = "Windows Portable Devices"

    def __init__(self):
        self._comtypes, self._api, self._types = _com()
        comtypes = self._comtypes
        comtypes.CoInitialize()
        self._create = comtypes.client.CreateObject
        self._inproc = comtypes.CLSCTX_INPROC_SERVER
        device_id = self._find()
        if device_id is None:
            raise sandbox_error() or CameraNotFoundError(
                "no Fujifilm camera found among Windows' portable devices",
                "Connect the camera with a data-capable USB-C cable and switch it on. It should appear in "
                "Explorer under This PC.")
        info = self._values()
        info.SetUnsignedIntegerValue(self._key(CLIENT, CLIENT_QOS), SECURITY_IMPERSONATION)
        info.SetUnsignedIntegerValue(self._key(CLIENT, CLIENT_ACCESS), GENERIC_READ_WRITE)
        self.device = self._create(self._api.PortableDevice, clsctx=self._inproc, interface=self._api.IPortableDevice)
        try:
            self.device.Open(device_id, info)
        except comtypes.COMError as e:
            raise CameraBusyError(f"Windows could not open the camera ({e})",
                                  "Close other apps that use the camera, then try again.") from e

    # -- COM helpers ------------------------------------------------------------------------
    def _key(self, fmtid: str, pid: int):
        key = self._api._tagpropertykey()
        key.fmtid = self._comtypes.GUID(fmtid)
        key.pid = pid
        return key

    def _values(self):
        return self._create(self._types.PortableDeviceValues, clsctx=self._inproc,
                            interface=self._types.IPortableDeviceValues)

    def _find(self) -> str | None:
        manager = self._create(self._api.PortableDeviceManager, clsctx=self._inproc,
                               interface=self._api.IPortableDeviceManager)
        count = ctypes.c_ulong(0)
        manager.GetDevices(ctypes.POINTER(ctypes.c_wchar_p)(), ctypes.pointer(count))
        if not count.value:
            return None
        ids = (ctypes.c_wchar_p * count.value)()
        manager.GetDevices(ctypes.cast(ids, ctypes.POINTER(ctypes.c_wchar_p)), ctypes.pointer(count))
        return next((i for i in ids[:count.value] if i and VENDOR_ID.search(i)), None)

    def _command(self, command: int, op: int | None = None, params: tuple = ()):
        values = self._values()
        values.SetGuidValue(self._key(COMMON, C_CATEGORY), self._comtypes.GUID(MTP_EXT))
        values.SetUnsignedIntegerValue(self._key(COMMON, C_ID), command)
        if op is not None:
            values.SetUnsignedIntegerValue(self._key(MTP_EXT, P_OPERATION_CODE), op)
            collection = self._create(self._types.PortableDevicePropVariantCollection, clsctx=self._inproc,
                                      interface=self._types.IPortableDevicePropVariantCollection)
            for p in params:
                pv = self._types.tag_inner_PROPVARIANT()
                ctypes.memset(ctypes.addressof(pv), 0, ctypes.sizeof(pv))
                pv.vt = VT_UI4
                ctypes.memmove(ctypes.addressof(pv) + 8, ctypes.byref(ctypes.c_ulong(p & 0xFFFFFFFF)), 4)
                collection.Add(pv)
            values.SetIPortableDevicePropVariantCollectionValue(self._key(MTP_EXT, P_OPERATION_PARAMS), collection)
        return values

    def _send(self, values, what: str):
        try:
            results = self.device.SendCommand(0, values)
            hr = results.GetErrorValue(self._key(COMMON, C_HRESULT))
        except self._comtypes.COMError as e:
            raise CameraNotRespondingError(f"Windows reported an error during {what} ({e})") from e
        if hr < 0:
            raise CameraNotRespondingError(f"Windows reported error 0x{hr & 0xFFFFFFFF:08X} during {what}")
        return results

    def _buffer(self, data: bytes):
        return (ctypes.c_ubyte * max(len(data), 1)).from_buffer_copy(data or b"\0"), len(data)

    # -- PTP --------------------------------------------------------------------------------
    def transact(self, op: int, params: tuple = (), data_out: bytes | None = None) -> tuple[int, tuple, bytes]:
        if op in (OP_OpenSession, OP_CloseSession):
            return 0x2001, (), b""         # the Windows driver owns the session
        what = f"request 0x{op:04X}"
        if data_out is not None:
            return self._write(op, params, data_out, what)
        if op in DATA_IN_OPS:
            return self._read(op, params, what)
        results = self._send(self._command(CMD_WITHOUT_DATA, op, params), what)
        return results.GetUnsignedIntegerValue(self._key(MTP_EXT, P_RESPONSE_CODE)), (), b""

    def _chunk(self, results) -> int:
        try:
            size = results.GetUnsignedIntegerValue(self._key(MTP_EXT, P_OPTIMAL_BUFFER))
        except self._comtypes.COMError:
            size = 0
        return size or DEFAULT_CHUNK

    def _read(self, op: int, params: tuple, what: str) -> tuple[int, tuple, bytes]:
        start = self._send(self._command(CMD_DATA_TO_READ, op, params), what)
        context = start.GetStringValue(self._key(MTP_EXT, P_CONTEXT))
        total = start.GetUnsignedLargeIntegerValue(self._key(MTP_EXT, P_TOTAL_SIZE))
        chunk = self._chunk(start)
        parts, got = [], 0
        while got < total:
            want = min(chunk, total - got)
            values = self._command(CMD_READ)
            values.SetStringValue(self._key(MTP_EXT, P_CONTEXT), context)
            values.SetUnsignedIntegerValue(self._key(MTP_EXT, P_BYTES_TO_READ), want)
            buf, size = self._buffer(bytes(want))
            values.SetBufferValue(self._key(MTP_EXT, P_DATA), buf, size)
            results = self._send(values, what)
            pointer, length = results.GetBufferValue(self._key(MTP_EXT, P_DATA))
            try:
                part = ctypes.string_at(pointer, length)
            finally:
                ctypes.windll.ole32.CoTaskMemFree(pointer)
            if not part:
                break
            parts.append(part)
            got += len(part)
        return self._end(context, what), (), b"".join(parts)

    def _write(self, op: int, params: tuple, data: bytes, what: str) -> tuple[int, tuple, bytes]:
        values = self._command(CMD_DATA_TO_WRITE, op, params)
        values.SetUnsignedLargeIntegerValue(self._key(MTP_EXT, P_TOTAL_SIZE), len(data))
        start = self._send(values, what)
        context = start.GetStringValue(self._key(MTP_EXT, P_CONTEXT))
        chunk = self._chunk(start)
        view = memoryview(data)
        for pos in range(0, len(data), chunk):
            piece = view[pos:pos + chunk].tobytes()
            values = self._command(CMD_WRITE)
            values.SetStringValue(self._key(MTP_EXT, P_CONTEXT), context)
            values.SetUnsignedIntegerValue(self._key(MTP_EXT, P_BYTES_TO_WRITE), len(piece))
            buf, size = self._buffer(piece)
            values.SetBufferValue(self._key(MTP_EXT, P_DATA), buf, size)
            self._send(values, what)
        return self._end(context, what), (), b""

    def _end(self, context: str, what: str) -> int:
        values = self._command(CMD_END)
        values.SetStringValue(self._key(MTP_EXT, P_CONTEXT), context)
        results = self._send(values, what)
        return results.GetUnsignedIntegerValue(self._key(MTP_EXT, P_RESPONSE_CODE))

    def close(self) -> None:
        with contextlib.suppress(Exception):   # the device may already be gone
            self.device.Close()
