"""Compare a photo's Fujifilm maker notes with a recipe.

Uses exiftool, an implementation independent of this project, so it doubles as ground truth:
a JPEG developed with a recipe (or shot with it) should carry exactly the recipe's settings.
"""
from __future__ import annotations

import json
import re
import shutil
import subprocess
from pathlib import Path

from . import model as m
from .errors import MissingDependencyError, X100VIError

FILM = {
    "provia": "F0/Standard (Provia)", "velvia": "F2/Fujichrome (Velvia)",
    "astia": "F1b/Studio Portrait Smooth Skin Tone (Astia)", "pro_neg_hi": "Pro Neg. Hi",
    "pro_neg_std": "Pro Neg. Std", "classic_chrome": "Classic Chrome", "eterna": "Eterna",
    "classic_neg": "Classic Negative", "eterna_bleach_bypass": "Bleach Bypass",
    "nostalgic_neg": "Nostalgic Neg", "reala_ace": "Reala ACE",
}
BW = {   # black-and-white sims are reported in the Saturation tag
    "monochrome": "None (B&W)", "monochrome_ye": "B&W Yellow Filter", "monochrome_r": "B&W Red Filter",
    "monochrome_g": "B&W Green Filter", "sepia": "B&W Sepia", "acros": "Acros",
    "acros_ye": "Acros Yellow Filter", "acros_r": "Acros Red Filter", "acros_g": "Acros Green Filter",
}
WB = {
    "auto": "Auto", "auto_white_priority": "Auto (white priority)",
    "auto_ambience_priority": "Auto (ambiance priority)", "daylight": "Daylight", "shade": "Cloudy",
    "fluorescent_1": "Daylight Fluorescent", "fluorescent_2": "Day White Fluorescent",
    "fluorescent_3": "White Fluorescent", "incandescent": "Incandescent", "underwater": "Underwater",
    "kelvin": "Kelvin", "custom_1": "Custom", "custom_2": "Custom2", "custom_3": "Custom3",
}
SMOOTH_SKIN_TAG = "FujiFilm_0x104a"   # unnamed in exiftool: 0 off, 32 weak, 64 strong
SMOOTH = {"off": 0, "weak": 32, "strong": 64}


def _number(text) -> float | None:
    match = re.match(r"\s*([+-]?\d+(?:\.\d+)?)", str(text))
    return float(match.group(1)) if match else None


def expected_tags(recipe: dict) -> dict:
    """EXIF tag -> expected value. A trailing '#' compares numerically, '|' lists alternatives."""
    r = m.merged(recipe)
    film = str(r["film_simulation"]).lower()
    code = m.FILM_SIMULATION[film]
    e: dict = {}
    if code in m.NO_COLOUR_SIMS:
        e["Saturation"] = BW[film]
    else:
        e["FilmMode"] = FILM[film]
        e["Saturation#"] = r["color"]
    if code in m.MONO_SIMS:
        mono = recipe.get("monochromatic", {})
        e["BWAdjustment#"] = mono.get("warm_cool", 0)
        e["BWMagentaGreen#"] = mono.get("magenta_green", 0)
    grain = r["grain"]
    e["GrainEffectRoughness"] = str(grain["strength"]).capitalize()
    e["GrainEffectSize"] = "Off" if grain["strength"] == "off" else str(grain.get("size", "small")).capitalize()
    e["ColorChromeEffect"] = str(r["color_chrome_effect"]).capitalize()
    e["ColorChromeFXBlue"] = str(r["color_chrome_fx_blue"]).capitalize()
    e[SMOOTH_SKIN_TAG] = SMOOTH[r["smooth_skin"]]
    wb = r["white_balance"]
    e["WhiteBalance"] = WB[wb["mode"]]
    if wb["mode"] == "kelvin":
        e["ColorTemperature#"] = wb["kelvin"]
    e["WhiteBalanceFineTune"] = f"Red {wb.get('shift_r', 0) * 20:+d}, Blue {wb.get('shift_b', 0) * 20:+d}"
    priority = r["d_range_priority"]
    if priority == "auto":
        e["DRangePriority"] = "Auto"
    elif priority != "off":
        e["DRangePriority"] = "Fixed"
        e["DRangePriorityAuto|DRangePriorityFixed"] = str(priority).capitalize()
    else:
        if str(r["dynamic_range"]) == "auto":
            e["DynamicRangeSetting"] = "Auto"
        else:
            e["DynamicRangeSetting"] = "Manual"
            e["DevelopmentDynamicRange#"] = int(r["dynamic_range"])
        e["HighlightTone#"] = r["highlight"]
        e["ShadowTone#"] = r["shadow"]
    e["Sharpness#"] = r["sharpness"]
    e["NoiseReduction#"] = r["noise_reduction"]
    e["Clarity#"] = r["clarity"]
    return e


def require_exiftool() -> str:
    exe = shutil.which("exiftool")
    if exe is None:
        raise MissingDependencyError(
            "exiftool is not installed",
            "Install it to compare photos with recipes: `brew install exiftool` (macOS), "
            "`sudo apt install libimage-exiftool-perl` (Debian/Ubuntu), or https://exiftool.org (Windows).")
    return exe


def read_tags(photo: str | Path) -> dict:
    exe = require_exiftool()
    if not Path(photo).is_file():
        raise X100VIError(f"{photo} does not exist")
    try:
        out = subprocess.run([exe, "-j", "-u", "-G1", str(photo)], capture_output=True, text=True, check=True)
    except subprocess.CalledProcessError as e:
        raise X100VIError(f"exiftool could not read {photo}: {e.stderr.strip()}") from e
    flat: dict = {}
    for key, value in json.loads(out.stdout)[0].items():
        group, _, name = key.partition(":")
        value = value.strip() if isinstance(value, str) else value
        if group == "FujiFilm" or name not in flat:   # maker notes win over generic EXIF
            flat[name] = value
    return flat


def compare(recipe: dict, photo: str | Path) -> dict:
    """{'checked': n, 'mismatches': [{tag, expected, found}]} for one photo."""
    tags = read_tags(photo)
    if "FilmMode" not in tags and "Saturation" not in tags:
        raise X100VIError(f"{Path(photo).name} carries no Fujifilm recipe metadata",
                          "It may have been edited or exported by software that strips maker notes.")
    mismatches = []
    wanted = expected_tags(recipe)
    for key, want in wanted.items():
        names = key.rstrip("#").split("|")
        found = next((tags[n] for n in names if n in tags), None)
        if key.endswith("#"):
            ok = found is not None and _number(found) == float(want)
        else:
            ok = str(found) == str(want)
        if not ok:
            mismatches.append({"tag": names[0], "expected": want, "found": found})
    return {"photo": str(photo), "checked": len(wanted), "mismatches": mismatches}
