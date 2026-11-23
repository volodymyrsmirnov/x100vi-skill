"""The camera: identity checks, and reading / writing recipes with full verification."""
from __future__ import annotations

from contextlib import contextmanager
from dataclasses import dataclass, field

from . import model as m
from . import settingsfile
from .errors import MODE_MENU, RAW_CONV_MODE, PTPError, RecipeError, UnsupportedCameraError, WrongModeError
from .ptp import PTPSession, Transport, open_transport, pack_string

SUPPORTED_MODEL = "X100VI"
TESTED_FIRMWARE = ("1.31",)
PROP_BATTERY = 0xD36B          # "NN,0,0": charge in percent as the camera reports it
PROP_USB_MODE = 0xD16E         # read-only: 5 tether, 6 RAW conv./backup restore, 8 webcam
USB_MODE_RAW_CONV = 6
REGISTER_PROPS = range(0xD18E, 0xD1A5)   # the 23 settings of the selected register; 0xD1A5 is live image size
PROFILE_PROP = 0xD185


@dataclass
class CameraInfo:
    model: str
    firmware: str
    mode: str                      # raw_conv | card_reader | unknown
    battery_percent: int | None = None
    warnings: list[str] = field(default_factory=list)

    @property
    def ready(self) -> bool:
        return self.model == SUPPORTED_MODEL and self.mode == "raw_conv"

    def as_dict(self) -> dict:
        return {"model": self.model, "firmware": self.firmware, "usb_mode": self.mode, "ready": self.ready,
                "battery_percent": self.battery_percent, "warnings": self.warnings}


class Camera:
    """A connected camera. Build one with `open_camera()`."""

    def __init__(self, session: PTPSession):
        self.session = session
        self.info = self._identify()

    # -- identity ------------------------------------------------------------------------
    def _identify(self) -> CameraInfo:
        di = self.session.device_info()
        if m.SLOT_SELECT in di.props:
            mode = "raw_conv"
            if PROP_USB_MODE in di.props:
                try:
                    if self.session.get_u16(PROP_USB_MODE) != USB_MODE_RAW_CONV:
                        mode = "unknown"
                except PTPError:
                    pass
        elif len(di.props) <= 8:
            mode = "card_reader"
        else:
            mode = "unknown"
        info = CameraInfo(model=di.model, firmware=di.version, mode=mode)
        if di.model == SUPPORTED_MODEL and di.version not in TESTED_FIRMWARE:
            info.warnings.append(
                f"firmware {di.version} has not been tested (tested: {', '.join(TESTED_FIRMWARE)}); "
                "every write is verified by reading it back, but report anything odd")
        if mode == "raw_conv":
            try:
                info.battery_percent = int(self.session.get_string(PROP_BATTERY).split(",")[0])
            except (PTPError, ValueError, IndexError):
                info.battery_percent = None
            if info.battery_percent is not None and info.battery_percent <= 15:
                info.warnings.append(f"battery is at about {info.battery_percent}%; charge before converting RAFs")
        return info

    def require_ready(self) -> None:
        """Refuse to go on unless this is an X100VI in the one USB mode that exposes settings."""
        if self.info.model != SUPPORTED_MODEL:
            raise UnsupportedCameraError(
                f"this is a {self.info.model or 'camera of unknown model'}, not an X100VI",
                "Only the Fujifilm X100VI is supported; other bodies use different property codes.")
        if self.info.mode != "raw_conv":
            now = {"card_reader": "USB CARD READER", "unknown": "a mode that does not expose settings"}[self.info.mode]
            raise WrongModeError(
                f"the camera is in {now}, so its settings are not reachable",
                f"On the camera: {MODE_MENU}. Then unplug and replug the USB cable. "
                f"(The X100VI has no tether mode; {RAW_CONV_MODE} is the only one that works.)")

    # -- helpers -------------------------------------------------------------------------
    def _code(self, f: m.Field, slot: int | None) -> int:
        return f.live if slot is None else f.slot

    def _select(self, slot: int | None) -> None:
        if slot is None:
            return
        if slot not in m.SLOTS:
            raise RecipeError("custom setting must be C1 to C7")
        # Always select explicitly: right after the camera enters this mode the register
        # properties show the live settings, and a write would copy them into the register.
        self.session.set_u16(m.SLOT_SELECT, slot)

    def _codes(self, slot: int | None) -> list[int]:
        return list(REGISTER_PROPS) if slot is not None else [f.live for f in m.FIELDS]

    def _snapshot(self, slot: int | None) -> dict[int, int]:
        return {c: self.session.get_u16(c) for c in self._codes(slot)}

    def _set_grain_off(self, code: int, size: str) -> int:
        """Grain Off is written as 1 and keeps the last size, so set a size first."""
        self.session.set_u16(code, 2 if size == "small" else 4)
        self.session.set_u16(code, 1)
        return 6 if size == "small" else 7

    # -- reading -------------------------------------------------------------------------
    def read(self, slot: int | None = None) -> dict:
        """Current shooting settings (slot=None) or a custom register, as a recipe."""
        self.require_ready()
        self._select(slot)
        recipe: dict = {}
        if slot is not None:
            recipe["name"] = self.session.get_string(m.SLOT_NAME)
        wire = {f.key: self.session.get_u16(self._code(f, slot)) for f in m.FIELDS}
        film = wire["film_simulation"]
        kelvin = wire["white_balance.mode"] == m.WHITE_BALANCE["kelvin"]
        raw_only = wire["image_quality"] == m.IMAGE_QUALITY["raw"]
        for f in m.FIELDS:
            skip = ((f.needs == "mono" and film not in m.MONO_SIMS) or (f.needs == "kelvin" and not kelvin)
                    or (f.needs == "jpeg" and raw_only) or (f.needs == "colour" and film in m.NO_COLOUR_SIMS))
            if not skip:
                m.put(recipe, f.key, m.decode_field(f, wire[f.key]))
        return recipe

    def backup(self) -> dict:
        self.require_ready()
        return {"camera": self.info.model, "firmware": self.info.firmware, "live": self.read(None),
                "slots": {str(n): self.read(n) for n in m.SLOTS}}

    # -- writing -------------------------------------------------------------------------
    def write(self, recipe: dict, slot: int | None = None, partial: bool = False) -> list[str]:
        """Apply a recipe and verify the whole target afterwards.

        Returns a log of what was written. Settings the recipe did not mean to touch are put
        back if the camera changed them as a side effect; raises RecipeError if the final
        state differs from what was asked.
        """
        self.require_ready()
        writes = m.encode_recipe(recipe, partial)
        self._select(slot)
        before = self._snapshot(slot)
        intended: dict[int, int] = {}
        log: list[str] = []
        if slot is not None and ("name" in recipe or not partial):
            # Name first: naming a factory-blank register was seen to seed its other settings
            # from the live ones, which the writes below then replace.
            self.session.set_raw(m.SLOT_NAME, pack_string(recipe.get("name", "")), "write the register name")
        # Monochromatic tint stays stored (hidden) under a colour sim and can only be written
        # while a B&W sim is selected. A full recipe for a colour sim clears it via Acros.
        film = next((w for f, w in writes if f.key == "film_simulation"), None)
        tint = [self._code(m.BY_KEY[k], slot) for k in ("monochromatic.warm_cool", "monochromatic.magenta_green")]
        if not partial and film is not None and film not in m.MONO_SIMS and any(before[c] for c in tint):
            self.session.set_u16(self._code(m.BY_KEY["film_simulation"], slot), m.FILM_SIMULATION["acros"])
            for c in tint:
                self.session.set_u16(c, 0)
                intended[c] = 0
        for f, wire in writes:
            code = self._code(f, slot)
            try:
                if f.kind == "grain" and wire == 1:
                    expect = self._set_grain_off(code, str(m.get(recipe, "grain.size") or "small").lower())
                else:
                    self.session.set_u16(code, wire)
                    expect = wire
            except PTPError as e:
                raise RecipeError(
                    f"the camera refused {f.key} = {m.get(m.merged(recipe, partial), f.key)!r}",
                    f"{e}. Another setting in this recipe may be locking it.") from e
            intended[code] = expect
            log.append(f.key)

        # Side effects: restore anything the recipe did not mean to change. Kelvin and shift are
        # stored per white-balance mode, so a mode change legitimately swaps them.
        wb_keys = ("white_balance.kelvin", "white_balance.shift_r", "white_balance.shift_b")
        wb_group = {self._code(m.BY_KEY[k], slot) for k in wb_keys}
        wb_changed = any(f.key == "white_balance.mode" for f, _ in writes)
        after = self._snapshot(slot)
        for code, value in after.items():
            if code in intended or value == before[code] or (wb_changed and code in wb_group):
                continue
            try:
                if code in m.GRAIN_CODES and before[code] in (6, 7):
                    self._set_grain_off(code, "small" if before[code] == 6 else "large")
                else:
                    self.session.set_u16(code, before[code])
            except PTPError:
                pass  # reported by the final comparison below if it matters
        final = self._snapshot(slot)
        wrong = [f"0x{c:04X} wanted 0x{v:04X}, camera holds 0x{final[c]:04X}"
                 for c, v in intended.items() if final[c] != v]
        if wrong:
            raise RecipeError("the camera did not keep every setting: " + "; ".join(wrong),
                              "Nothing else was changed. Run `x100vi get` to see the current state.")
        if slot is not None and ("name" in recipe or not partial):
            stored = self.session.get_string(m.SLOT_NAME)
            if stored != recipe.get("name", ""):
                raise RecipeError(f"the camera stored the name as {stored!r}")
        return log

    # -- the camera's own settings file ------------------------------------------------------
    def read_settings_file(self) -> bytes:
        """Every menu setting plus C1-C7, as the body exports it (see settingsfile.py)."""
        self.require_ready()
        return settingsfile.read(self.session)

    def restore_settings_file(self, blob: bytes, allow_other_body: bool = False, current: bytes | None = None) -> None:
        """Write a settings file back after checking it fits this body.

        `current` is the camera's file as just read (pass it when the caller saved a copy first).
        """
        self.require_ready()
        new = settingsfile.parse_header(blob)
        if current is None:
            current = self.read_settings_file()
        here = settingsfile.parse_header(current)
        if new.model != here.model:
            raise RecipeError(f"this settings file is from an {new.model or 'unknown camera'}, not an {here.model}",
                              "Only a file saved from the same camera model can be restored.")
        if len(blob) != len(current):
            raise RecipeError(
                f"this settings file has a different size ({len(blob)} bytes) from the camera's own "
                f"({len(current)} bytes)",
                "It was probably saved under different firmware. Restore it only after updating the camera to "
                "the firmware it was saved with.")
        if new.body_id != here.body_id and not allow_other_body:
            raise RecipeError("this settings file was saved from a different X100VI body",
                              "It also carries that body's network settings. Add --other-body to restore it anyway.")
        settingsfile.write(self.session, blob)

    def clear_wb_memory(self, slot: int) -> None:
        """Zero the shift remembered for every white-balance mode of a register."""
        self._select(slot)
        self.session.set_u16(m.BY_KEY["d_range_priority"].slot, 0)
        for mode, code in m.WHITE_BALANCE.items():
            self.session.set_u16(m.BY_KEY["white_balance.mode"].slot, code)
            if mode == "kelvin":
                self.session.set_u16(m.BY_KEY["white_balance.kelvin"].slot, 10000)
            self.session.set_u16(m.BY_KEY["white_balance.shift_r"].slot, 0)
            self.session.set_u16(m.BY_KEY["white_balance.shift_b"].slot, 0)


@contextmanager
def open_camera(transport: Transport | None = None):
    """Connect, open a PTP session, identify the camera, and always release it afterwards."""
    session = PTPSession(transport if transport is not None else open_transport())
    session.open()
    try:
        yield Camera(session)
    finally:
        session.close()
