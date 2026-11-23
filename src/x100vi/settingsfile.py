"""The camera's own settings file: every menu setting plus C1-C7, exported by the body itself.

This is the file Fujifilm's backup tools save. In USB RAW CONV./BACKUP RESTORE mode the body serves it
as PTP object handle 0:

  GetObjectInfo 0x1008 (handle 0)     format 0x5000, size of the file
  GetObject     0x1009 (handle 0)     the file, starting "FUJIFILMX-BACKUP"
  SendObjectInfo 0x100C (0, 0)        restore: a 1076-byte ObjectInfo, format 0x5000, size of the file
  SendObject    0x100D (0)            the file

The tool treats the file as opaque: it checks the header and never edits the contents. The file holds
the body's ID and network settings, so it should be kept private.
"""
from __future__ import annotations

import struct
from dataclasses import dataclass

from .errors import PTPError, RecipeError, X100VIError
from .ptp import RC_OK, OP_GetObject, OP_GetObjectInfo, PTPSession, Reader

MAGIC = b"FUJIFILMX-BACKUP"
OBJECT_FORMAT = 0x5000
HANDLE = 0
INFO_SIZE = 1076           # the ObjectInfo length Fujifilm's SDK sends when restoring
OP_SendObjectInfo, OP_SendObject = 0x100C, 0x100D
HEADER_SIZE = 0x48
SUFFIX = ".dat"
# Bytes the camera updates itself on every restore (firmware 1.31): a 16-bit counter at 0x108 that went up
# by 2, and a flag at 0x716 that went from 1 to 3. They are not settings; verification ignores them.
BOOKKEEPING = frozenset({0x108, 0x109, 0x716})


def settings_differ(a: bytes, b: bytes) -> list[int]:
    """Offsets where two settings files differ, ignoring the camera's own bookkeeping bytes."""
    if len(a) != len(b):
        return [min(len(a), len(b))]
    return [i for i, (x, y) in enumerate(zip(a, b, strict=True)) if x != y and i not in BOOKKEEPING]


@dataclass(frozen=True)
class Header:
    version: str
    model: str
    body_id: str


def parse_header(blob: bytes, source: str = "this file") -> Header:
    """Check that bytes are a camera settings file and read who made it."""
    if len(blob) < HEADER_SIZE or not blob.startswith(MAGIC):
        raise RecipeError(f"{source} is not a camera settings file",
                          "Use a file saved by `x100vi backup --full` (it starts with FUJIFILMX-BACKUP).")

    def text(lo: int, hi: int) -> str:
        return blob[lo:hi].split(b"\x00")[0].decode("ascii", "replace")

    return Header(version=text(16, 20), model=text(20, 0x34), body_id=text(0x34, HEADER_SIZE))


def read(session: PTPSession) -> bytes:
    """Download the settings file from the camera."""
    code, _, info = session.transact(OP_GetObjectInfo, (HANDLE,))
    if code != RC_OK:
        raise PTPError(code, "ask for the camera's settings file",
                       "The camera offers its settings file only in USB RAW CONV./BACKUP RESTORE mode.")
    r = Reader(info)
    r.take("I")
    fmt, _prot, size = r.take("H"), r.take("H"), r.take("I")
    if fmt != OBJECT_FORMAT:
        raise X100VIError(f"the camera offered object format 0x{fmt:04X} instead of its settings file")
    blob = session.check(OP_GetObject, (HANDLE,), what="download the settings file")
    if len(blob) != size:
        raise X100VIError(f"the settings file was cut short ({len(blob)} of {size} bytes)", "Run the command again.")
    parse_header(blob, "what the camera sent")
    return blob


def write(session: PTPSession, blob: bytes) -> None:
    """Send a settings file to the camera, exactly as Fujifilm's SDK does."""
    info = struct.pack("<IHHI", 0, OBJECT_FORMAT, 0, len(blob)).ljust(INFO_SIZE, b"\x00")
    session.check(OP_SendObjectInfo, (0, 0), info, "announce the settings file")
    session.check(OP_SendObject, (0,), blob, "send the settings file")
