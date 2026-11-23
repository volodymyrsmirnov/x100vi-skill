"""The recipe model: what a recipe may contain and how each setting is encoded on the wire.

Two targets share one encoding:
  * "live"  - the current shooting settings
  * C1..C7  - the custom-setting registers, addressed by writing the register number to 0xD18C

Ranges were measured by having the camera validate values; every label was confirmed against
the EXIF of photos shot with known recipes (see docs/protocol.md).
"""
from __future__ import annotations

import difflib
from dataclasses import dataclass

from .errors import RecipeError

SLOT_SELECT = 0xD18C
SLOT_NAME = 0xD18D
NAME_MAX = 25
SLOTS = range(1, 8)

FILM_SIMULATION = {
    "provia": 1, "velvia": 2, "astia": 3, "pro_neg_hi": 4, "pro_neg_std": 5,
    "monochrome": 6, "monochrome_ye": 7, "monochrome_r": 8, "monochrome_g": 9, "sepia": 10,
    "classic_chrome": 11, "acros": 12, "acros_ye": 13, "acros_r": 14, "acros_g": 15,
    "eterna": 16, "classic_neg": 17, "eterna_bleach_bypass": 18, "nostalgic_neg": 19, "reala_ace": 20,
}
MONO_SIMS = {6, 7, 8, 9, 12, 13, 14, 15}   # the only sims that accept monochromatic colour
NO_COLOUR_SIMS = MONO_SIMS | {10}          # the camera rejects any write to `color` for these
GRAIN_CODES = {0xD023, 0xD195}

# Grain is one property: written 1..5, read back 2..7 (Off keeps the last size).
GRAIN_WRITE = {("off", "small"): 1, ("off", "large"): 1, ("weak", "small"): 2, ("strong", "small"): 3,
               ("weak", "large"): 4, ("strong", "large"): 5}
GRAIN_READ = {2: ("weak", "small"), 3: ("strong", "small"), 4: ("weak", "large"), 5: ("strong", "large"),
              6: ("off", "small"), 7: ("off", "large")}

OFF_WEAK_STRONG = {"off": 1, "weak": 2, "strong": 3}
DYNAMIC_RANGE = {"auto": 0xFFFF, "100": 100, "200": 200, "400": 400}
D_RANGE_PRIORITY = {"off": 0, "weak": 1, "strong": 2, "auto": 0x8000}
WHITE_BALANCE = {
    "auto": 0x0002, "daylight": 0x0004, "incandescent": 0x0006, "underwater": 0x0008,
    "fluorescent_1": 0x8001, "fluorescent_2": 0x8002, "fluorescent_3": 0x8003, "shade": 0x8006,
    "kelvin": 0x8007, "custom_1": 0x8008, "custom_2": 0x8009, "custom_3": 0x800A,
    "auto_white_priority": 0x8020, "auto_ambience_priority": 0x8021,
}
NOISE_REDUCTION = {-4: 0x8000, -3: 0x7000, -2: 0x4000, -1: 0x3000, 0: 0x2000, 1: 0x1000, 2: 0x0000,
                   3: 0x6000, 4: 0x5000}
IMAGE_QUALITY = {"raw": 1, "fine": 2, "normal": 3, "fine_raw": 4, "normal_raw": 5}
IMAGE_SIZE = {"s_3:2": 1, "s_16:9": 2, "s_1:1": 3, "m_3:2": 4, "m_16:9": 5, "m_1:1": 6,
              "l_3:2": 7, "l_16:9": 8, "l_1:1": 9, "s_4:3": 10, "s_5:4": 12, "l_4:3": 14, "l_5:4": 16,
              "m_4:3": 18, "m_5:4": 20}
ON_OFF = {"on": 1, "off": 2}
COLOR_SPACE = {"srgb": 1, "adobe_rgb": 2}


@dataclass(frozen=True)
class Field:
    key: str            # dotted path in the recipe JSON
    live: int           # property code for the current shooting settings
    slot: int           # property code inside a custom register
    kind: str           # enum | scaled | int | grain
    table: dict | None = None
    scale: int = 1
    lo: float = 0
    hi: float = 0
    step: float = 1
    needs: str = ""     # precondition enforced when encoding: mono | kelvin | colour | jpeg


# Order matters: it is the order the camera must be written in.
FIELDS = [
    Field("d_range_priority", 0xD02E, 0xD191, "enum", D_RANGE_PRIORITY),
    Field("dynamic_range", 0xD007, 0xD190, "enum", DYNAMIC_RANGE),
    Field("film_simulation", 0xD001, 0xD192, "enum", FILM_SIMULATION),
    Field("monochromatic.warm_cool", 0xD104, 0xD193, "scaled", scale=10, lo=-18, hi=18, needs="mono"),
    Field("monochromatic.magenta_green", 0xD031, 0xD194, "scaled", scale=10, lo=-18, hi=18, needs="mono"),
    Field("grain", 0xD023, 0xD195, "grain"),
    Field("color_chrome_effect", 0xD029, 0xD196, "enum", OFF_WEAK_STRONG),
    Field("color_chrome_fx_blue", 0xD030, 0xD197, "enum", OFF_WEAK_STRONG),
    Field("smooth_skin", 0xD189, 0xD198, "enum", OFF_WEAK_STRONG),
    Field("white_balance.mode", 0x5005, 0xD199, "enum", WHITE_BALANCE),
    Field("white_balance.kelvin", 0xD017, 0xD19C, "int", lo=2500, hi=10000, step=10, needs="kelvin"),
    Field("white_balance.shift_r", 0xD00B, 0xD19A, "int", lo=-9, hi=9),
    Field("white_balance.shift_b", 0xD00C, 0xD19B, "int", lo=-9, hi=9),
    Field("highlight", 0xD320, 0xD19D, "scaled", scale=10, lo=-2, hi=4, step=0.5),
    Field("shadow", 0xD321, 0xD19E, "scaled", scale=10, lo=-2, hi=4, step=0.5),
    Field("color", 0xD008, 0xD19F, "scaled", scale=10, lo=-4, hi=4, needs="colour"),
    Field("sharpness", 0x5015, 0xD1A0, "scaled", scale=10, lo=-4, hi=4),
    Field("noise_reduction", 0xD01C, 0xD1A1, "enum", NOISE_REDUCTION),
    Field("clarity", 0xD032, 0xD1A2, "scaled", scale=10, lo=-5, hi=5),
    # Housekeeping rather than "look": never defaulted, written only when present.
    Field("image_quality", 0xD018, 0xD18F, "enum", IMAGE_QUALITY),
    Field("image_size", 0xD16E, 0xD18E, "enum", IMAGE_SIZE, needs="jpeg"),
    Field("lens_modulation_optimizer", 0xD34D, 0xD1A3, "enum", ON_OFF),
    Field("color_space", 0xD00A, 0xD1A4, "enum", COLOR_SPACE),
]
BY_KEY = {f.key: f for f in FIELDS}
OPTIONAL = {"image_quality", "image_size", "lens_modulation_optimizer", "color_space"}
METADATA = {"$schema", "name", "description", "tags", "notes", "source"}   # never sent, except name

DEFAULT_RECIPE: dict = {
    "name": "",
    "film_simulation": "provia",
    "grain": {"strength": "off", "size": "small"},
    "color_chrome_effect": "off",
    "color_chrome_fx_blue": "off",
    "smooth_skin": "off",
    "white_balance": {"mode": "auto", "shift_r": 0, "shift_b": 0},
    "dynamic_range": "100",
    "d_range_priority": "off",
    "highlight": 0, "shadow": 0, "color": 0, "sharpness": 0, "noise_reduction": 0, "clarity": 0,
}
TOP_LEVEL = set(DEFAULT_RECIPE) | OPTIONAL | METADATA | {"monochromatic"}
NESTED = {
    "grain": {"strength", "size"},
    "white_balance": {"mode", "kelvin", "shift_r", "shift_b"},
    "monochromatic": {"warm_cool", "magenta_green"},
}


def get(recipe: dict, dotted: str):
    cur = recipe
    for part in dotted.split("."):
        if not isinstance(cur, dict) or part not in cur:
            return None
        cur = cur[part]
    return cur


def put(recipe: dict, dotted: str, value) -> None:
    parts = dotted.split(".")
    cur = recipe
    for part in parts[:-1]:
        cur = cur.setdefault(part, {})
    cur[parts[-1]] = value


def signed16(value: int) -> int:
    return value - 0x10000 if value >= 0x8000 else value


def _suggest(value, options) -> str:
    close = difflib.get_close_matches(str(value).lower(), [str(o) for o in options], n=1, cutoff=0.5)
    return f" Did you mean {close[0]!r}?" if close else ""


def parse_target(text: str) -> int | None:
    """'live' -> None, 'c3' / 'C3' / '3' -> 3."""
    t = str(text).strip().lower()
    if t == "live":
        return None
    if t.startswith("c"):
        t = t[1:]
    if t.isdigit() and int(t) in SLOTS:
        return int(t)
    raise RecipeError(f"unknown target {text!r}", "Use live, or a custom setting C1 to C7.")


def target_label(slot: int | None) -> str:
    return "live" if slot is None else f"C{slot}"


def encode_field(f: Field, value) -> int:
    """Recipe value -> 16-bit wire value. Raises RecipeError when outside the camera's range."""
    if f.kind == "enum":
        key = value if f.key == "noise_reduction" else str(value).lower()
        if isinstance(key, bool):
            key = str(key)
        if isinstance(key, str) and key.startswith("unknown:0x"):
            return int(key[8:], 16)   # round-trips a code this table has no name for
        if key not in f.table:
            options = ", ".join(str(k) for k in f.table)
            raise RecipeError(f"{f.key}: {value!r} is not valid.{_suggest(value, f.table)}",
                              f"Allowed values: {options}.")
        return f.table[key]
    if f.kind == "grain":
        if not isinstance(value, dict) or "strength" not in value:
            raise RecipeError(f"grain: {value!r} is not valid.",
                              'Use {"strength": "off|weak|strong", "size": "small|large"}.')
        pair = (str(value["strength"]).lower(), str(value.get("size", "small")).lower())
        if pair not in GRAIN_WRITE:
            raise RecipeError(f"grain: strength {value['strength']!r} / size {value.get('size')!r} is not valid.",
                              "strength is off, weak or strong; size is small or large.")
        return GRAIN_WRITE[pair]
    if isinstance(value, bool) or not isinstance(value, (int, float)):
        raise RecipeError(f"{f.key}: {value!r} is not a number.", f"Use a number from {f.lo:g} to {f.hi:g}.")
    if not f.lo <= value <= f.hi:
        raise RecipeError(f"{f.key}: {value:g} is out of range.", f"The camera accepts {f.lo:g} to {f.hi:g}.")
    steps = (value - f.lo) / f.step
    if abs(steps - round(steps)) > 1e-9:
        raise RecipeError(f"{f.key}: {value:g} is not on the camera's grid.",
                          f"Use steps of {f.step:g} from {f.lo:g} to {f.hi:g}.")
    return round(value * f.scale) & 0xFFFF


def decode_field(f: Field, wire: int):
    if f.kind == "enum":
        for name, code in f.table.items():
            if code == wire:
                return name
        return f"unknown:0x{wire:04X}"
    if f.kind == "grain":
        if wire in GRAIN_READ:
            strength, size = GRAIN_READ[wire]
            return {"strength": strength, "size": size}
        if wire == 1:
            return {"strength": "off", "size": "small"}
        return {"strength": f"unknown:0x{wire:04X}", "size": "small"}
    value = signed16(wire) / f.scale
    return int(value) if value == int(value) else value


def _check_shape(recipe) -> None:
    if not isinstance(recipe, dict):
        raise RecipeError("a recipe must be a JSON object")
    unknown = sorted(set(recipe) - TOP_LEVEL)
    if unknown:
        hint = "".join(f" {k!r}:{_suggest(k, TOP_LEVEL)}" for k in unknown).strip()
        raise RecipeError(f"unknown setting(s): {', '.join(unknown)}",
                          (hint + " " if "Did you mean" in hint else "")
                          + "ISO, exposure compensation, focus and drive settings cannot be set over USB.")
    for key, allowed in NESTED.items():
        if key in recipe:
            if not isinstance(recipe[key], dict):
                raise RecipeError(f"{key} must be an object with {', '.join(sorted(allowed))}")
            extra = sorted(set(recipe[key]) - allowed)
            if extra:
                raise RecipeError(f"unknown setting(s) in {key}: {', '.join(extra)}",
                                  f"Allowed: {', '.join(sorted(allowed))}.")
    name = recipe.get("name", "")
    if not isinstance(name, str):
        raise RecipeError("name must be text")
    if len(name) > NAME_MAX:
        raise RecipeError(f"name is {len(name)} characters; the camera allows {NAME_MAX}",
                          f"Shorten it, e.g. {name[:NAME_MAX]!r}.")
    if not all(32 <= ord(ch) < 127 for ch in name):
        raise RecipeError("name may only contain plain ASCII letters, digits, spaces and punctuation")


def merged(recipe: dict, partial: bool = False) -> dict:
    """The recipe with factory defaults filled in (unless partial)."""
    out: dict = {} if partial else {k: (dict(v) if isinstance(v, dict) else v) for k, v in DEFAULT_RECIPE.items()}
    for key, value in recipe.items():
        if isinstance(value, dict) and isinstance(out.get(key), dict):
            out[key].update(value)
        else:
            out[key] = dict(value) if isinstance(value, dict) else value
    return out


def encode_recipe(recipe: dict, partial: bool = False) -> list[tuple[Field, int]]:
    """Validate a recipe and return the ordered property writes for it.

    With partial=False every missing look setting takes its factory default, so a recipe file
    always produces the same look. With partial=True only the given settings are written.
    """
    _check_shape(recipe)
    full = merged(recipe, partial)
    film = FILM_SIMULATION.get(str(get(full, "film_simulation")).lower())
    wb = get(full, "white_balance.mode")
    writes: list[tuple[Field, int]] = []
    for f in FIELDS:
        value = get(full, f.key)
        if f.needs == "colour" and film in NO_COLOUR_SIMS:
            if get(recipe, f.key) not in (None, 0):
                raise RecipeError("color does not apply to monochrome, acros or sepia film simulations",
                                  "Remove `color`, or use monochromatic.warm_cool / magenta_green to tint.")
            continue
        if value is None and f.needs == "mono" and not partial and film in MONO_SIMS:
            value = 0
        if value is None:
            continue
        if f.needs == "mono" and film not in MONO_SIMS:
            raise RecipeError(f"{f.key} needs a monochrome or acros film simulation in the same recipe",
                              "Pick monochrome* or acros*, or remove `monochromatic`.")
        if f.needs == "kelvin" and wb != "kelvin":
            raise RecipeError("white_balance.kelvin is only used when white_balance.mode is kelvin",
                              'Set "mode": "kelvin", or remove `kelvin`.')
        if f.needs == "jpeg" and get(full, "image_quality") == "raw":
            raise RecipeError("image_size cannot be set while image_quality is raw",
                              "Use fine, normal, fine_raw or normal_raw, or remove image_size.")
        writes.append((f, encode_field(f, value)))
    if wb == "kelvin" and get(full, "white_balance.kelvin") is None:
        raise RecipeError("white_balance.mode is kelvin but no kelvin value is given",
                          'Add "kelvin": 2500..10000 (steps of 10).')
    # D-range priority locks dynamic range and tone: clear it first, set the real value last.
    drp = next(((f, w) for f, w in writes if f.key == "d_range_priority"), None)
    if drp and drp[1] != 0:
        writes = [(drp[0], 0)] + [x for x in writes if x[0].key != "d_range_priority"] + [drp]
    return writes


def ignored_by_priority(recipe: dict) -> list[str]:
    """Settings a recipe asks for that D-range priority makes the camera ignore."""
    if get(recipe, "d_range_priority") in (None, "off"):
        return []
    return [k for k in ("dynamic_range", "highlight", "shadow") if k in recipe]


def is_blank(recipe: dict) -> bool:
    """True when a register still holds the factory look and no name."""
    if recipe.get("name"):
        return False
    for key, default in DEFAULT_RECIPE.items():
        if key == "name":
            continue
        value = recipe.get(key, default)
        if isinstance(default, dict):
            if any(value.get(k, v) != v for k, v in default.items()):
                return False
        elif str(value) != str(default):
            return False
    return True
