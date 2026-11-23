"""Bundled starter recipes, the JSON schema, local state, and locating the packaged skill."""
from __future__ import annotations

import json
import os
import sys
import time
from importlib import resources
from pathlib import Path

from . import model as m
from .errors import RecipeError, X100VIError


def load_recipe(path: str | Path) -> dict:
    """Read a recipe file, or a bundled starter recipe by name, with errors a person can act on."""
    p = Path(path)
    if not p.exists():
        if p.suffix == "" and str(path) in starter_names():
            return starter(str(path))
        raise RecipeError(f"recipe file {path} does not exist",
                          "Give a path to a .json recipe, or a starter name from `x100vi recipes`.")
    try:
        data = json.loads(p.read_text(encoding="utf-8"))
    except json.JSONDecodeError as e:
        raise RecipeError(f"{p.name} is not valid JSON: {e.msg} at line {e.lineno}, column {e.colno}") from e
    if not isinstance(data, dict):
        raise RecipeError(f"{p.name} must contain one JSON object")
    return data


def _starter_dir():
    return resources.files("x100vi") / "recipes"


def starter_names() -> list[str]:
    return sorted(p.name[:-5] for p in _starter_dir().iterdir() if p.name.endswith(".json"))


def starter(name: str) -> dict:
    entry = _starter_dir() / f"{name}.json"
    if not entry.is_file():
        raise RecipeError(f"no starter recipe called {name!r}", f"Available: {', '.join(starter_names())}.")
    return json.loads(entry.read_text(encoding="utf-8"))


def schema() -> dict:
    return json.loads((resources.files("x100vi") / "recipe.schema.json").read_text(encoding="utf-8"))


def state_dir() -> Path:
    """Where automatic backups go.

    $X100VI_HOME if set; %LOCALAPPDATA%\\x100vi on Windows; otherwise $XDG_STATE_HOME/x100vi,
    falling back to ~/.local/state/x100vi.
    """
    root = os.environ.get("X100VI_HOME")
    if root:
        return Path(root)
    if sys.platform == "win32" and os.environ.get("LOCALAPPDATA"):
        return Path(os.environ["LOCALAPPDATA"]) / "x100vi"
    return Path(os.environ.get("XDG_STATE_HOME") or Path.home() / ".local" / "state") / "x100vi"


def save_auto_backup(label: str, content: dict) -> Path:
    folder = state_dir() / "backups"
    folder.mkdir(parents=True, exist_ok=True)
    stamp = time.strftime("%Y%m%d-%H%M%S")
    path = folder / f"{stamp}-{label}.json"
    n = 1
    while path.exists():
        n += 1
        path = folder / f"{stamp}-{label}-{n}.json"
    path.write_text(json.dumps(content, indent=2) + "\n", encoding="utf-8")
    return path


def skill_source() -> Path:
    """The skill folder: inside an installed wheel, or in a source checkout."""
    packaged = Path(str(resources.files("x100vi") / "_skill"))
    if (packaged / "SKILL.md").is_file():
        return packaged
    checkout = Path(__file__).resolve().parents[2] / "skills" / "x100vi"
    if (checkout / "SKILL.md").is_file():
        return checkout
    raise X100VIError("the skill files are missing from this installation",
                      "Reinstall from https://github.com/volodymyrsmirnov/x100vi-skill.")


def summarize(recipe: dict) -> str:
    """One line a person can scan: film simulation plus the settings that differ from default."""
    full = m.merged(recipe)
    parts = [str(full["film_simulation"])]
    wb = full["white_balance"]
    wb_text = f"{wb['kelvin']}K" if wb["mode"] == "kelvin" else str(wb["mode"])
    if wb.get("shift_r") or wb.get("shift_b"):
        wb_text += f" R{wb.get('shift_r', 0):+d} B{wb.get('shift_b', 0):+d}"
    parts.append(f"WB {wb_text}")
    if full["d_range_priority"] != "off":
        parts.append(f"DR priority {full['d_range_priority']}")
    else:
        parts.append(f"DR{full['dynamic_range']}")
    if full["grain"]["strength"] != "off":
        parts.append(f"grain {full['grain']['strength']}/{full['grain'].get('size', 'small')}")
    for key, label in (("color_chrome_effect", "CCE"), ("color_chrome_fx_blue", "FX blue"), ("smooth_skin", "skin")):
        if full[key] != "off":
            parts.append(f"{label} {full[key]}")
    for key, label in (("highlight", "H"), ("shadow", "S"), ("color", "color"), ("sharpness", "sharp"),
                       ("noise_reduction", "NR"), ("clarity", "clarity")):
        if full.get(key):
            parts.append(f"{label} {full[key]:+g}")
    return ", ".join(parts)
