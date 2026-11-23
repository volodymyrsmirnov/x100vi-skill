"""In-camera RAW conversion: develop a RAF with a recipe on the X100VI's own processor.

This is the FUJIFILM X RAW STUDIO flow (camera in USB RAW CONV./BACKUP RESTORE mode):

  SendObjectInfo 0x900C + SendObject2 0x900D   upload the RAF as FUP_FILE.dat (format 0xF802)
  GetDevicePropValue 0xD185                    profile describing the RAF's as-shot settings
  SetDevicePropValue 0xD185                    patched profile, 629-byte host-write form
  SetDevicePropValue 0xD183 = 0 | 1            develop: half size | full resolution
  GetObjectHandles / GetObject / DeleteObject  fetch the JPEG and remove it from the buffer

Sequence and profile layout follow github.com/zgrimz/fujifilm-ptp-reference (docs 05, 06).
The profile cells hold the same wire values as the custom-register properties in model.py.
"""
from __future__ import annotations

import struct
import time
from dataclasses import dataclass
from pathlib import Path

from . import model as m
from .camera import PROFILE_PROP, SUPPORTED_MODEL, Camera
from .errors import ConversionError, PTPError, RecipeError
from .ptp import RC_OK, OP_DeleteObject, OP_GetObject, OP_GetObjectInfo, Reader, pack_string

OP_SendObjectInfo = 0x900C
OP_SendObject2 = 0x900D
PROP_TRIGGER = 0xD183
FORMAT_RAF = 0xF802
FORMAT_JPEG = 0x3801
RAF_MAGIC = b"FUJIFILMCCD-RAW "

TABLE = 0x1FD        # cell i lives at TABLE + 4*i, signed 32-bit little endian
HOST_CELLS = 30      # X RAW STUDIO always writes 30 cells (629 bytes) with numParams = 29

# profile cell <- recipe key
CELL = {
    "image_size": 3, "image_quality": 4, "dynamic_range": 6, "film_simulation": 8, "grain": 9,
    "color_chrome_effect": 10, "white_balance.mode": 12, "white_balance.shift_r": 13,
    "white_balance.shift_b": 14, "white_balance.kelvin": 15, "highlight": 16, "shadow": 17, "color": 18,
    "sharpness": 19, "noise_reduction": 20, "lens_modulation_optimizer": 21, "color_space": 22,
    "monochromatic.warm_cool": 23, "smooth_skin": 24, "color_chrome_fx_blue": 25,
    "monochromatic.magenta_green": 26, "clarity": 27,
}
C_FILETYPE, C_EXPOSURE, C_DR, C_DRP, C_FILM, C_WB_SOURCE = 2, 5, 6, 7, 8, 11
FILETYPE_JPEG = 7
DRP_CELL = {"off": 0, "weak": 1, "strong": 2}   # confirmed from output EXIF; Auto has no known code


@dataclass
class RafInfo:
    path: Path
    model: str
    firmware: str
    size: int


def inspect_raf(path: str | Path) -> RafInfo:
    """Check that a file is a RAF this camera can develop, before uploading tens of megabytes."""
    p = Path(path)
    if not p.is_file():
        raise ConversionError(f"{p} does not exist", "Give the path to a .RAF file shot on an X100VI.")
    with p.open("rb") as fh:
        head = fh.read(0x40)
    if head[:16] != RAF_MAGIC:
        kind = "a JPEG" if head[:2] == b"\xff\xd8" else "not a Fujifilm RAF"
        raise ConversionError(f"{p.name} is {kind}",
                              "Previews are developed from RAW: shoot RAW or RAW+JPEG and pass the .RAF file.")
    raf_model = head[0x1C:0x3C].split(b"\x00")[0].decode("ascii", "replace")
    firmware = head[0x3C:0x40].decode("ascii", "replace")
    if raf_model != SUPPORTED_MODEL:
        raise ConversionError(f"{p.name} was shot on a {raf_model or 'different camera'}, not an X100VI",
                              "The camera only develops RAFs from its own model.")
    return RafInfo(p, raf_model, firmware, p.stat().st_size)


@dataclass
class Profile:
    """A 0xD185 blob with cell access; keeps every byte it does not understand."""

    data: bytearray

    @property
    def cells(self) -> int:
        return (len(self.data) - TABLE) // 4

    def get(self, i: int) -> int:
        return struct.unpack_from("<i", self.data, TABLE + 4 * i)[0]

    def set(self, i: int, value: int) -> None:
        if value >= 0x80000000:
            value -= 1 << 32
        struct.pack_into("<i", self.data, TABLE + 4 * i, value)

    def values(self) -> list[int]:
        return [self.get(i) for i in range(self.cells)]

    @property
    def iop_code(self) -> str:
        return Reader(bytes(self.data), 2).string()

    def validate(self) -> None:
        """Refuse to patch anything that does not look like a profile at the known base."""
        if len(self.data) < TABLE + 4 * 28:
            raise ConversionError(f"unexpected conversion profile ({len(self.data)} bytes)")
        if self.get(C_DR) not in (100, 200, 400) or self.get(C_FILM) not in m.FILM_SIMULATION.values():
            raise ConversionError(
                f"unexpected conversion profile (dynamic range {self.get(C_DR)}, film simulation {self.get(C_FILM)})",
                "This firmware may use a different profile layout; nothing was changed.")

    def host_form(self) -> bytes:
        """The form X RAW STUDIO writes: 30 cells, numParams 29, IOPCode length counting its NUL."""
        out = bytearray(self.data)
        want = TABLE + 4 * HOST_CELLS
        if len(out) > want:
            raise ConversionError(f"conversion profile is longer than the known host form ({len(out)} bytes)")
        out += bytes(want - len(out))
        struct.pack_into("<H", out, 0, HOST_CELLS - 1)
        if len(self.iop_code) == 8:
            out[2] = 9
        return bytes(out)


def describe(profile: Profile) -> dict:
    """As-shot settings of the loaded RAF, in recipe vocabulary."""
    out: dict = {}
    for key, cell in CELL.items():
        if cell < profile.cells:
            m.put(out, key, m.decode_field(m.BY_KEY[key], profile.get(cell) & 0xFFFF))
    film = profile.get(C_FILM)
    if film not in m.MONO_SIMS:
        out.pop("monochromatic", None)
    if profile.get(C_WB_SOURCE) == 1:
        # mode and kelvin are placeholders while the RAF's own white balance is in use
        out["white_balance"] = {"mode": "as_shot", "shift_r": profile.get(13), "shift_b": profile.get(14)}
    elif out.get("white_balance", {}).get("mode") != "kelvin":
        out.get("white_balance", {}).pop("kelvin", None)
    priority = profile.get(C_DRP)
    out["d_range_priority"] = {v: k for k, v in DRP_CELL.items()}.get(priority, f"unknown:{priority}")
    out["exposure_ev"] = round(profile.get(C_EXPOSURE) / 1000, 2)
    return out


@dataclass
class Adjustment:
    """Something the render does differently from what the recipe asks, and why."""

    setting: str
    requested: object
    applied: object
    reason: str

    def as_dict(self) -> dict:
        return {"setting": self.setting, "requested": self.requested, "applied": self.applied, "reason": self.reason}

    def __str__(self) -> str:
        return f"{self.setting}: asked for {self.requested}, rendered with {self.applied} ({self.reason})"


def apply_recipe(as_shot: Profile, recipe: dict, exposure_ev: float = 0.0) -> tuple[Profile, list[Adjustment]]:
    """Return a patched copy of the as-shot profile, plus every deviation from the recipe."""
    adjustments: list[Adjustment] = []
    priority = str(m.get(recipe, "d_range_priority") or "off").lower()
    writes = m.encode_recipe(recipe)
    if priority not in DRP_CELL:
        raise RecipeError("d_range_priority auto cannot be previewed",
                          "The conversion has no encoding for Auto. Preview with weak or strong instead; "
                          "the recipe itself can still be saved to the camera with auto.")
    prof = Profile(bytearray(as_shot.data))
    # A frame shot with D-range priority carries it in its profile; while it is on, the converter
    # ignores dynamic range and tone, so the recipe's value always replaces it.
    prof.set(C_DRP, DRP_CELL[priority])
    shot_dr = as_shot.get(C_DR)
    for f, wire in writes:
        cell = CELL.get(f.key)
        if cell is None:
            continue
        if f.key == "dynamic_range":
            # No Auto encoding exists for a conversion, and a DR above the frame's own renders with
            # a green cast: use the camera's Auto ceiling (200) and cap at the as-shot value.
            want = 200 if wire == 0xFFFF else wire
            label = "auto" if wire == 0xFFFF else str(wire)
            if want > shot_dr:
                adjustments.append(Adjustment(
                    "dynamic_range", label, str(shot_dr),
                    f"this frame was shot at DR{shot_dr}; a RAF cannot be developed above its own dynamic "
                    "range. On the camera the recipe will use the full value when ISO allows it"))
                want = shot_dr
            elif wire == 0xFFFF:
                adjustments.append(Adjustment("dynamic_range", "auto", "200",
                                              "Auto has no conversion encoding; 200 is the camera's Auto ceiling"))
            value = want
        elif f.kind in ("scaled", "int"):
            value = m.signed16(wire)
        else:
            value = wire   # grain Off is 1 here, the code the camera reports for an as-shot Off
        if cell >= prof.cells:
            prof.data += bytes(TABLE + 4 * (cell + 1) - len(prof.data))
        prof.set(cell, value)
    if priority != "off":
        ignored = [k for k in ("dynamic_range", "highlight", "shadow") if k in recipe]
        if ignored:
            adjustments.append(Adjustment(
                ", ".join(ignored), "as given", "ignored",
                f"d_range_priority is {priority}, and while it is on the camera ignores dynamic range and tone"))
    prof.set(C_FILETYPE, FILETYPE_JPEG)
    prof.set(C_WB_SOURCE, 2)   # take white balance from the profile, not from the RAF
    thirds = round(exposure_ev * 3)
    if abs(exposure_ev * 3 - thirds) > 0.05 or not -9 <= thirds <= 9:
        raise RecipeError(f"exposure {exposure_ev:g} EV is not valid",
                          "Use steps of 1/3 EV from -3 to +3, e.g. 0.33, 0.67, 1, -0.33.")
    prof.set(C_EXPOSURE, round(thirds * 1000 / 3))
    return prof, adjustments


class RawConverter:
    def __init__(self, camera: Camera):
        self.camera = camera
        self.session = camera.session
        self.as_shot: Profile | None = None

    def upload(self, raf: bytes) -> Profile:
        """Load a RAF into the camera's conversion buffer and return its as-shot profile."""
        self.camera.require_ready()
        if raf[:16] != RAF_MAGIC:
            raise ConversionError("not a RAF file")
        info = struct.pack("<IHHIHIIIIIIIHII", 0, FORMAT_RAF, 0, len(raf), 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0)
        info += pack_string("FUP_FILE.dat") + b"\x00\x00\x00"
        self.session.check(OP_SendObjectInfo, (0, 0, 0), info, "announce the RAF")
        self.session.check(OP_SendObject2, (), raf, "upload the RAF")
        try:
            data = self.session.prop_raw(PROFILE_PROP)
        except PTPError as e:
            raise ConversionError(
                "the camera did not accept this RAF for conversion",
                f"It must be an unmodified RAF shot on an X100VI ({e}).") from e
        self.as_shot = Profile(bytearray(data))
        self.as_shot.validate()
        return self.as_shot

    def develop(self, profile: Profile, full: bool = False, timeout_s: float = 90.0) -> bytes:
        """Write a profile, verify the camera stored it, develop, and return the JPEG bytes."""
        want = profile.host_form()
        self.session.set_raw(PROFILE_PROP, want, "send the conversion settings")
        back = Profile(bytearray(self.session.prop_raw(PROFILE_PROP)))
        sent = Profile(bytearray(want))
        diff = [i for i in range(min(back.cells, sent.cells)) if back.get(i) != sent.get(i)]
        if diff:
            raise ConversionError(f"the camera stored different conversion settings (cells {diff})",
                                  "Nothing was developed. Power-cycle the camera and try again.")
        before = set(self.session.handles())
        self.session.set_raw(PROP_TRIGGER, struct.pack("<H", 1 if full else 0), "start the conversion")
        deadline = time.monotonic() + timeout_s
        handle = None
        while time.monotonic() < deadline:
            code, _, data = self.session.transact(0x1007, (0xFFFFFFFF, 0, 0))
            if code == RC_OK:
                new = [h for h in Reader(data).array("I") if h not in before]
                if new:
                    handle = new[0]
                    break
            time.sleep(0.25)
        if handle is None:
            # Never trigger again after a timeout: the body may still be converting.
            raise ConversionError(
                f"the camera produced no image within {timeout_s:g} s",
                "Do not retry yet. Switch the camera off and on (remove the battery briefly if it "
                "will not switch off), reconnect, then run the command again.")
        r = Reader(self.session.check(OP_GetObjectInfo, (handle,), what="read the result's description"))
        _storage, fmt, _prot, size = r.take("I"), r.take("H"), r.take("H"), r.take("I")
        image = self.session.check(OP_GetObject, (handle,), what="download the result")
        if len(image) != size:
            raise ConversionError(f"the result was cut short ({len(image)} of {size} bytes)", "Run the command again.")
        if fmt != FORMAT_JPEG or image[:2] != b"\xff\xd8":
            raise ConversionError(f"the camera returned something other than a JPEG (format 0x{fmt:04X})")
        self.session.transact(OP_DeleteObject, (handle,))   # remove only the object this conversion produced
        return image
