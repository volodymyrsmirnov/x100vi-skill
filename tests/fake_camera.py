"""A software X100VI that answers PTP transactions the way the real body was measured to.

It reproduces the behaviours the client has to cope with:
  * two USB modes (card reader exposes four properties and no settings);
  * value validation, including the cross-setting locks (D-range priority, B&W sims, RAW-only);
  * grain Off written as 1 and read back as 6/7; white-balance shift remembered per mode;
    a Kelvin change zeroing the shift; monochromatic tint staying stored under a colour sim;
  * naming a never-named register seeds it from the live settings;
  * RAF upload, the 0xD185 profile, and conversion producing a JPEG object.
"""
from __future__ import annotations

import io
import json
import struct
from pathlib import Path

from PIL import Image

from x100vi import model as m
from x100vi.ptp import DeviceInfo, Reader, pack_string
from x100vi.rawconv import RAF_MAGIC, TABLE

OK, GENERAL_ERROR, PROP_UNSUPPORTED, INVALID_VALUE, INVALID_HANDLE = 0x2001, 0x2002, 0x200A, 0x201C, 0x2009
AS_SHOT_PROFILE = (Path(__file__).parent / "data" / "d185_as_shot.bin").read_bytes()
CARD_READER_PROPS = [0x5001, 0xD303, 0xD406, 0xD407]
FACTORY = {  # recipe key -> factory wire value
    "d_range_priority": 0, "dynamic_range": 100, "film_simulation": 1, "monochromatic.warm_cool": 0,
    "monochromatic.magenta_green": 0, "grain": 6, "color_chrome_effect": 1, "color_chrome_fx_blue": 1,
    "smooth_skin": 1, "white_balance.mode": 2, "white_balance.kelvin": 10000, "white_balance.shift_r": 0,
    "white_balance.shift_b": 0, "highlight": 0, "shadow": 0, "color": 0, "sharpness": 0,
    "noise_reduction": 0x2000, "clarity": 0, "image_quality": 2, "image_size": 7,
    "lens_modulation_optimizer": 1, "color_space": 1,
}


def make_raf(path: Path, model: str = "X100VI", size: int = 4096) -> Path:
    """A file with a valid RAF header (no image data), enough for upload checks."""
    head = RAF_MAGIC + b"0201" + b"FF179503" + model.encode().ljust(32, b"\x00") + b"0131"
    path.write_bytes(head + bytes(size - len(head)))
    return path


def u16(value: int) -> bytes:
    return struct.pack("<H", value & 0xFFFF)


class FakeX100VI:
    def __init__(self, mode: str = "raw_conv", model: str = "X100VI", firmware: str = "1.31", battery: int = 80):
        self.mode, self.model, self.firmware, self.battery = mode, model, firmware, battery
        self.live = dict(FACTORY) | {"image_quality": 1, "dynamic_range": 0xFFFF}   # owner shoots RAW, DR auto
        self.slots = {n: dict(FACTORY) for n in m.SLOTS}
        self.names = dict.fromkeys(m.SLOTS, "")
        self.named_before: set[int] = set()
        self.wb_memory: dict[tuple, tuple[int, int]] = {}
        self.selected = 1
        self.usb_mode = 6                 # 0xD16E: 6 is RAW conv./backup restore
        self.profile: bytes | None = None
        self.objects: dict[int, bytes] = {}
        self.next_handle = 1
        self.stall_conversion = False
        self.closed = False
        self.session_open = False
        self.writes: list[tuple[int, bytes]] = []       # every SetDevicePropValue, in order
        self.uploads = 0
        self._announced = 0
        self._by_live = {f.live: f for f in m.FIELDS}
        self._by_slot = {f.slot: f for f in m.FIELDS}

    # -- transport interface --------------------------------------------------------------
    def close(self) -> None:
        self.closed = True

    def transact(self, op: int, params: tuple = (), data_out: bytes | None = None) -> tuple[int, tuple, bytes]:
        handler = {
            0x1001: self._device_info, 0x1002: self._open, 0x1003: self._close, 0x1007: self._handles,
            0x1008: self._object_info, 0x1009: self._object, 0x100B: self._delete, 0x1015: self._get,
            0x1016: self._set, 0x900C: self._announce, 0x900D: self._upload,
        }.get(op)
        if handler is None:
            return 0x2005, (), b""
        code, data = handler(params, data_out)
        return code, (), data

    def _open(self, params, data):
        self.session_open = True
        return OK, b""

    def _close(self, params, data):
        self.session_open = False
        return OK, b""

    def _props(self) -> list[int]:
        if self.mode != "raw_conv":
            return CARD_READER_PROPS
        return sorted({0x5005, 0x5015, 0xD16E, 0xD183, 0xD185, 0xD18C, 0xD18D, 0xD36A, 0xD36B,
                       *self._by_live, *self._by_slot})

    def _device_info(self, params, data):
        info = DeviceInfo("FUJIFILM", self.model, self.firmware, "FAKE0001",
                          [0x1001, 0x1002, 0x1003, 0x1007, 0x1008, 0x1009, 0x100B, 0x1015, 0x1016, 0x900C, 0x900D],
                          self._props())
        return OK, info.pack()

    # -- properties -----------------------------------------------------------------------
    def _state(self, code: int):
        if code in self._by_live:
            return self.live, self._by_live[code], ("live",)
        return self.slots[self.selected], self._by_slot[code], ("slot", self.selected)

    def _get(self, params, data):
        code = params[0]
        if code not in self._props():
            return PROP_UNSUPPORTED, b""
        if code == 0xD18C:
            return OK, u16(self.selected)
        if code == 0xD18D:
            return OK, pack_string(self.names[self.selected])
        if code == 0xD36B:
            return OK, pack_string(f"{self.battery},0,0")
        if code == 0xD36A:
            return OK, struct.pack("<I", self.battery // 10)
        if code == 0xD16E:
            return OK, u16(self.usb_mode)
        if code == 0xD185:
            if self.profile is None:
                return GENERAL_ERROR, b""
            return OK, self.profile[:len(AS_SHOT_PROFILE)]
        if code == 0xD183:
            return GENERAL_ERROR, b""
        state, f, _ = self._state(code)
        return OK, u16(state[f.key])

    def _set(self, params, data):
        code = params[0]
        if code not in self._props():
            return PROP_UNSUPPORTED, b""
        self.writes.append((code, bytes(data)))
        if code == 0xD18C:
            value = struct.unpack("<H", data)[0]
            if value not in m.SLOTS:
                return INVALID_VALUE, b""
            self.selected = value
            return OK, b""
        if code == 0xD18D:
            name = Reader(data).string()
            if len(name) > m.NAME_MAX:
                return INVALID_VALUE, b""
            if self.selected not in self.named_before:
                self.named_before.add(self.selected)
                self.slots[self.selected].update(self.live)     # the real body's first-naming quirk
            self.names[self.selected] = name
            return OK, b""
        if code == 0xD185:
            if self.profile is None or not TABLE + 4 * 29 <= len(data) <= TABLE + 4 * 30 + 3:
                return GENERAL_ERROR, b""
            self.profile = bytes(data)
            return OK, b""
        if code == 0xD183:
            return self._convert(struct.unpack("<H", data)[0]), b""
        if code in (0xD16E, 0xD36A, 0xD36B):
            return INVALID_VALUE, b""
        state, f, target = self._state(code)
        return self._write_field(state, f, target, struct.unpack("<H", data)[0]), b""

    def _write_field(self, state: dict, f: m.Field, target: tuple, wire: int) -> int:
        film = state["film_simulation"]
        locked = state["d_range_priority"] != 0
        if f.needs == "mono" and film not in m.MONO_SIMS:
            return INVALID_VALUE
        if f.needs == "colour" and film in m.NO_COLOUR_SIMS:
            return INVALID_VALUE
        if f.needs == "kelvin" and state["white_balance.mode"] != m.WHITE_BALANCE["kelvin"]:
            return INVALID_VALUE
        if f.needs == "jpeg" and state["image_quality"] == 1:
            return INVALID_VALUE
        if locked and f.key in ("dynamic_range", "highlight", "shadow"):
            return INVALID_VALUE
        if f.kind == "grain":
            if wire not in (1, 2, 3, 4, 5):
                return INVALID_VALUE
            if wire == 1:
                wire = 7 if state["grain"] in (4, 5, 7) else 6
        elif f.kind == "enum":
            if wire not in f.table.values():
                return INVALID_VALUE
        else:
            value = m.signed16(wire)
            lo, hi, step = round(f.lo * f.scale), round(f.hi * f.scale), round(f.step * f.scale)
            if not lo <= value <= hi or (value - lo) % step:
                return INVALID_VALUE
        if f.key == "white_balance.mode" and wire != state[f.key]:
            self.wb_memory[(*target, state[f.key])] = (state["white_balance.shift_r"], state["white_balance.shift_b"])
            state["white_balance.shift_r"], state["white_balance.shift_b"] = self.wb_memory.get((*target, wire), (0, 0))
        if f.key == "white_balance.kelvin" and wire != state[f.key]:
            state["white_balance.shift_r"] = state["white_balance.shift_b"] = 0
        state[f.key] = wire
        return OK

    # -- RAW conversion -------------------------------------------------------------------
    def _announce(self, params, data):
        r = Reader(data)
        r.take("I")
        if r.take("H") != 0xF802:
            return GENERAL_ERROR, b""
        r.take("H")
        self._announced = r.take("I")
        return OK, b""

    def _upload(self, params, data):
        self.uploads += 1
        ok = (len(data) == self._announced and data[:16] == RAF_MAGIC
              and data[0x1C:0x3C].split(b"\x00")[0] == self.model.encode())
        self.profile = AS_SHOT_PROFILE if ok else None       # the body accepts the upload either way
        return OK, b""

    def _convert(self, trigger: int) -> int:
        if trigger not in (0, 1) or self.profile is None:
            return INVALID_VALUE
        if self.stall_conversion:
            return OK
        count = (len(self.profile) - TABLE) // 4
        cells = [struct.unpack_from("<i", self.profile, TABLE + 4 * i)[0] for i in range(count)]
        payload = json.dumps({"cells": cells, "full": bool(trigger)}).encode()
        buf = io.BytesIO()
        Image.new("RGB", (48, 32) if trigger else (24, 16), (90, 120, 150)).save(buf, "JPEG", comment=payload)
        self.objects[self.next_handle] = buf.getvalue()
        self.next_handle += 1
        return OK

    def _handles(self, params, data):
        handles = sorted(self.objects)
        return OK, struct.pack("<I", len(handles)) + b"".join(struct.pack("<I", h) for h in handles)

    def _object_info(self, params, data):
        blob = self.objects.get(params[0])
        if blob is None:
            return INVALID_HANDLE, b""
        info = struct.pack("<IHHIHIIIIIIIHII", 0x10000001, 0x3801, 0, len(blob), 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0)
        return OK, info + pack_string("DSCF0001.jpg") + pack_string("20261123T080000") + b"\x00\x00"

    def _object(self, params, data):
        blob = self.objects.get(params[0])
        return (OK, blob) if blob is not None else (INVALID_HANDLE, b"")

    def _delete(self, params, data):
        return (OK, b"") if self.objects.pop(params[0], None) is not None else (INVALID_HANDLE, b"")

    # -- helpers for assertions -----------------------------------------------------------
    def slot_value(self, slot: int, key: str):
        return m.decode_field(m.BY_KEY[key], self.slots[slot][key])


def rendered_cells(jpeg: bytes | Path) -> dict:
    """What the fake 'developed': the profile cells it was triggered with (kept in a JPEG comment)."""
    source = io.BytesIO(jpeg) if isinstance(jpeg, bytes) else jpeg
    with Image.open(source) as im:
        return json.loads(im.info["comment"])
