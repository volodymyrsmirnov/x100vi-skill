"""Command line for the X100VI: `x100vi --help`.

Every command prints plain text for people, or one JSON object with --json for agents.
Errors go to stderr as `error:` plus a `hint:` line, with a stable exit code per kind.
"""
from __future__ import annotations

import argparse
import json
import shutil
import sys
from collections.abc import Callable
from pathlib import Path

from . import __version__, library, settingsfile
from . import model as m
from .camera import open_camera
from .errors import MODE_MENU, RecipeError, SlotOccupiedError, X100VIError
from .ptp import Transport
from .sandbox import codex_home

TransportFactory = Callable[[], Transport] | None
ON_BODY = "ISO, exposure compensation, focus and drive settings are not reachable over USB; set them on the camera."


# -- output ------------------------------------------------------------------------------
class Out:
    def __init__(self, as_json: bool):
        self.as_json = as_json

    def emit(self, result: dict, lines: list[str]) -> None:
        if self.as_json:
            print(json.dumps(result, indent=2))
        else:
            print("\n".join(lines))


def _warn_lines(warnings: list[str]) -> list[str]:
    return [f"warning: {w}" for w in warnings]


def recipe_advice(recipe: dict) -> list[str]:
    """Things a recipe implies that a person should know before shooting with it."""
    full = m.merged(recipe, partial=True)
    advice = []
    dr = str(full.get("dynamic_range", ""))
    if dr == "400":
        advice.append("DR400 needs ISO 500 or higher; at lower ISO the camera falls back to a lower dynamic range.")
    elif dr == "200":
        advice.append("DR200 needs ISO 250 or higher; at lower ISO the camera uses DR100.")
    if full.get("clarity"):
        advice.append("Clarity other than 0 adds about a second of processing after every shot.")
    ignored = m.ignored_by_priority(recipe)
    if ignored:
        advice.append(f"d_range_priority is on, so the camera ignores {', '.join(ignored)}.")
    mode = m.get(full, "white_balance.mode")
    if isinstance(mode, str) and mode.startswith("custom_"):
        advice.append(f"{mode} uses whatever custom white balance is stored on this camera.")
    return advice


# -- commands ----------------------------------------------------------------------------
def cmd_doctor(a, out: Out, tf: TransportFactory) -> int:
    try:
        with open_camera(tf() if tf else None) as cam:
            info = cam.info
            problem = None
            try:
                cam.require_ready()
            except X100VIError as e:
                problem = e
    except X100VIError as e:
        out.emit({"ready": False, **e.as_dict()}, [f"not ready: {e}"] + ([f"  {e.hint}"] if e.hint else []))
        return e.exit_code
    result = info.as_dict()
    lines = [f"{info.model} firmware {info.firmware}",
             f"USB mode: {'USB RAW CONV./BACKUP RESTORE' if info.mode == 'raw_conv' else info.mode}"]
    if info.battery_percent is not None:
        lines.append(f"battery: about {info.battery_percent}%")
    lines += _warn_lines(info.warnings)
    if problem is not None:
        result.update(problem.as_dict())
        lines += [f"not ready: {problem}", f"  {problem.hint}"]
        out.emit(result, lines)
        return problem.exit_code
    lines.append("ready")
    out.emit(result, lines)
    return 0


def cmd_get(a, out: Out, tf: TransportFactory) -> int:
    with open_camera(tf() if tf else None) as cam:
        if a.target.lower() == "all":
            data = cam.backup()
        else:
            data = cam.read(m.parse_target(a.target))
    text = json.dumps(data, indent=2)
    if a.output:
        Path(a.output).parent.mkdir(parents=True, exist_ok=True)
        Path(a.output).write_text(text + "\n", encoding="utf-8")
        out.emit({"saved": a.output, "target": a.target}, [f"saved {a.target} to {a.output}"])
    else:
        print(text)
    return 0


def cmd_set(a, out: Out, tf: TransportFactory) -> int:
    slot = m.parse_target(a.target)
    label = m.target_label(slot)
    recipe = library.load_recipe(a.recipe)
    writes = m.encode_recipe(recipe, a.partial)   # validate before touching the camera
    advice = recipe_advice(recipe)
    if a.dry_run:
        plan = [f.key for f, _ in writes]
        out.emit({"dry_run": True, "target": label, "would_write": plan, "advice": advice},
                 [f"would write {len(plan)} settings to {label}: {library.summarize(recipe)}"]
                 + [f"note: {x}" for x in advice])
        return 0
    with open_camera(tf() if tf else None) as cam:
        cam.require_ready()
        current = cam.read(slot)
        backup_path = None
        if slot is not None and not m.is_blank(current):
            if not a.overwrite:
                held = current.get("name") or "an unnamed recipe"
                raise SlotOccupiedError(
                    f"{label} already holds {held!r} ({library.summarize(current)})",
                    f"Pick an empty custom setting, or add --overwrite to replace it. The current contents "
                    f"are saved to {library.state_dir() / 'backups'} first.")
        if not a.no_backup and (slot is None or not m.is_blank(current)):
            backup_path = library.save_auto_backup(label, current)
        cam.write(recipe, slot, a.partial)
        stored = cam.read(slot)
        warnings = list(cam.info.warnings)
    result = {"target": label, "stored": stored, "previous_saved_to": str(backup_path) if backup_path else None,
              "warnings": warnings, "advice": advice, "set_on_camera": ON_BODY}
    lines = [f"{label} now holds {stored.get('name') or 'the recipe'!r}: {library.summarize(stored)}"
             if slot is not None else f"live settings are now: {library.summarize(stored)}"]
    if backup_path:
        lines.append(f"previous contents saved to {backup_path}")
    lines += _warn_lines(warnings) + [f"note: {x}" for x in advice] + [f"note: {ON_BODY}"]
    out.emit(result, lines)
    return 0


PRIVATE_NOTE = "Keep it private: the file also holds the camera's ID and network settings."


def cmd_backup(a, out: Out, tf: TransportFactory) -> int:
    if a.full:
        with open_camera(tf() if tf else None) as cam:
            blob = cam.read_settings_file()
        header = settingsfile.parse_header(blob)
        if a.output:
            path = Path(a.output)
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(blob)
        else:
            path = library.save_settings_file("camera-settings", blob)
        out.emit({"saved": str(path), "kind": "camera_settings_file", "bytes": len(blob), "model": header.model,
                  "note": PRIVATE_NOTE},
                 [f"saved the camera's settings file ({len(blob)} bytes: every menu setting and C1-C7) to {path}",
                  f"note: {PRIVATE_NOTE}"])
        return 0
    with open_camera(tf() if tf else None) as cam:
        data = cam.backup()
    if a.output:
        path = Path(a.output)
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(json.dumps(data, indent=2) + "\n", encoding="utf-8")
    else:
        path = library.save_auto_backup("all", data)
    names = {f"C{n}": s.get("name") or ("(empty)" if m.is_blank(s) else "(unnamed)") for n, s in data["slots"].items()}
    out.emit({"saved": str(path), "slots": names},
             [f"saved live settings and C1-C7 to {path}"] + [f"  {k}: {v}" for k, v in names.items()])
    return 0


def _restore_settings_file(a, blob: bytes, out: Out, tf: TransportFactory) -> int:
    settingsfile.parse_header(blob, a.file)
    if a.live:
        raise RecipeError("--live does not apply to a camera settings file",
                          "A settings file always restores everything: menus, live settings and C1-C7.")
    if not a.yes:
        raise RecipeError(f"restore would replace every setting on the camera (menus, live settings and C1-C7) "
                          f"with {a.file}", "Add --yes to go ahead. The camera's current settings file is saved first.")
    with open_camera(tf() if tf else None) as cam:
        current = cam.read_settings_file()
        if not settingsfile.settings_differ(current, blob):
            out.emit({"restored": False, "unchanged": True},
                     ["the camera already holds exactly these settings; nothing was written"])
            return 0
        previous = library.save_settings_file("before-restore", current)
        cam.restore_settings_file(blob, a.other_body, current=current)
        mismatch = settingsfile.settings_differ(cam.read_settings_file(), blob)
    if mismatch:
        raise X100VIError(f"the camera accepted the settings file but reads back {len(mismatch)} different bytes",
                          f"Its previous settings are saved in {previous}. Switch the camera off and on, then "
                          "check the settings on the body.")
    out.emit({"restored": True, "verified": True, "previous_saved_to": str(previous)},
             [f"restored every setting from {a.file} and read it back to confirm",
              f"previous settings saved to {previous}"])
    return 0


def cmd_restore(a, out: Out, tf: TransportFactory) -> int:
    path = Path(a.file)
    if path.is_file():
        with path.open("rb") as fh:
            if fh.read(len(settingsfile.MAGIC)) == settingsfile.MAGIC:
                return _restore_settings_file(a, path.read_bytes(), out, tf)
    data = library.load_recipe(a.file)
    if "slots" not in data or not isinstance(data["slots"], dict):
        raise RecipeError(f"{a.file} is not a backup made by `x100vi backup`",
                          "To write a single recipe use `x100vi set C1 recipe.json`.")
    jobs: list[tuple[int | None, dict]] = []
    for key, recipe in sorted(data["slots"].items()):
        slot = m.parse_target(key)
        m.encode_recipe(recipe)        # validate everything before writing anything
        jobs.append((slot, recipe))
    if a.live:
        if "live" not in data:
            raise RecipeError("this backup has no live settings")
        m.encode_recipe(data["live"])
        jobs.append((None, data["live"]))
    if not a.yes:
        raise RecipeError(
            f"restore would overwrite {', '.join(m.target_label(s) for s, _ in jobs)}",
            "Add --yes to go ahead. The camera's current contents are saved first.")
    with open_camera(tf() if tf else None) as cam:
        cam.require_ready()
        previous = library.save_auto_backup("before-restore", cam.backup())
        for slot, recipe in jobs:
            if slot is not None:
                cam.clear_wb_memory(slot)
            cam.write(recipe, slot)
    done = [m.target_label(s) for s, _ in jobs]
    out.emit({"restored": done, "previous_saved_to": str(previous)},
             [f"restored {', '.join(done)}", f"previous contents saved to {previous}"])
    return 0


def cmd_validate(a, out: Out, tf: TransportFactory) -> int:
    results, lines, failed = [], [], False
    for path in a.files:
        try:
            recipe = library.load_recipe(path)
            m.encode_recipe(recipe)
            advice = recipe_advice(recipe)
            results.append({"file": path, "valid": True, "summary": library.summarize(recipe), "advice": advice})
            lines.append(f"{path}: ok - {library.summarize(recipe)}")
            lines += [f"  note: {x}" for x in advice]
        except X100VIError as e:
            failed = True
            results.append({"file": path, "valid": False, **e.as_dict()})
            lines.append(f"{path}: INVALID - {e}" + (f"\n  {e.hint}" if e.hint else ""))
    out.emit({"results": results}, lines)
    return RecipeError.exit_code if failed else 0


def cmd_preview(a, out: Out, tf: TransportFactory) -> int:
    from . import exifcheck
    from .rawconv import OUTPUT_FORMATS, Profile, RawConverter, apply_recipe, describe, inspect_raf
    from .sheet import contact_sheet

    fmt = OUTPUT_FORMATS[a.format]
    if fmt.full_only and not a.full:
        raise RecipeError(f"{fmt.label} previews need --full",
                          "The camera makes TIFF only at full resolution (about 125 MB per image for 8-bit, "
                          "245 MB for 16-bit). Add --full, or use --format jpeg or heif for half-size previews.")
    rafs = [inspect_raf(p) for p in a.rafs]
    recipes: list[tuple[str, dict]] = []
    for ref in a.recipe:
        recipe = library.load_recipe(ref)
        m.encode_recipe(recipe)
        if str(m.get(recipe, "d_range_priority") or "off").lower() == "auto":
            raise RecipeError(f"{ref}: d_range_priority auto cannot be previewed",
                              "Preview with weak or strong; the recipe can still be saved to the camera with auto.")
        label = Path(ref).stem
        while label in [lbl for lbl, _ in recipes]:
            label += "-2"
        recipes.append((label, recipe))
    if not recipes and not a.as_shot:
        raise RecipeError("nothing to render", "Give --recipe FILE [...] and/or --as-shot.")
    if a.verify:
        exifcheck.require_exiftool()   # fail before connecting, with install help
    out_dir = Path(a.out)
    out_dir.mkdir(parents=True, exist_ok=True)
    renders, frames, rows, lines = [], [], [], []
    with open_camera(tf() if tf else None) as cam:
        cam.require_ready()
        warnings = list(cam.info.warnings)
        conv = RawConverter(cam)
        for raf in rafs:
            as_shot = conv.upload(raf.path.read_bytes())    # upload once, develop many
            shot = describe(as_shot)
            priority = shot["d_range_priority"]
            lines.append(f"{raf.path.name}: shot as {shot['film_simulation']}, DR{shot['dynamic_range']}"
                         + (f", D-range priority {priority}" if priority != "off" else ""))
            row = []
            jobs: list[tuple[str, dict | None]] = ([("as-shot", None)] if a.as_shot else []) + recipes
            for label, recipe in jobs:
                if recipe is None:
                    profile, adjustments = Profile(bytearray(as_shot.data)), []
                else:
                    profile, adjustments = apply_recipe(as_shot, recipe, a.ev)
                image = conv.develop(profile, full=a.full, timeout_s=a.timeout, output=fmt.name)
                target = out_dir / f"{raf.path.stem}__{label}.{fmt.extension}"
                target.write_bytes(image)
                entry = {"raf": str(raf.path), "recipe": label, "file": str(target),
                         "not_applied": [adj.as_dict() for adj in adjustments]}
                lines.append(f"  {target}")
                lines += [f"    not applied as written - {adj}" for adj in adjustments]
                if a.verify and recipe is not None:
                    effective = dict(recipe)
                    for adj in adjustments:
                        if adj.setting == "dynamic_range":
                            effective["dynamic_range"] = adj.applied
                    check = exifcheck.compare(effective, target)
                    entry["verified"] = {"checked": check["checked"], "mismatches": check["mismatches"]}
                    matched = check["checked"] - len(check["mismatches"])
                    lines.append(f"    verified {matched}/{check['checked']} settings in the output"
                                 + "".join(f"\n    MISMATCH {x['tag']}: expected {x['expected']}, found {x['found']}"
                                           for x in check["mismatches"]))
                renders.append(entry)
                title = "As shot" if recipe is None else (recipe.get("name") or label)
                row.append((title, target))
            rows.append(row)
            frames.append({"raf": str(raf.path), "as_shot": shot})
    sheet = None
    if a.sheet and sum(len(r) for r in rows) > 1:
        if fmt.name == "jpeg":
            sheet = contact_sheet(rows, out_dir / "contact-sheet.jpg")
            lines.append(f"contact sheet: {sheet}")
        else:
            lines.append(f"no contact sheet: it is made only for JPEG previews, not {fmt.label}")
    lines = _warn_lines(warnings) + lines
    if a.ev:
        lines.append(f"exposure compensation {a.ev:+g} EV applied to every recipe render")
    out.emit({"renders": renders, "frames": frames, "contact_sheet": str(sheet) if sheet else None,
              "format": fmt.name, "exposure_ev": a.ev, "full_resolution": a.full, "warnings": warnings}, lines)
    return 0


def cmd_verify(a, out: Out, tf: TransportFactory) -> int:
    from . import exifcheck

    recipe = library.load_recipe(a.recipe)
    m.encode_recipe(recipe)
    check = exifcheck.compare(recipe, a.photo)
    ok = not check["mismatches"]
    lines = [f"{a.photo}: {check['checked'] - len(check['mismatches'])}/{check['checked']} settings match {a.recipe}"]
    lines += [f"  {x['tag']}: recipe expects {x['expected']}, photo has {x['found']}" for x in check["mismatches"]]
    out.emit({**check, "matches": ok}, lines)
    return 0 if ok else 3


def cmd_recipes(a, out: Out, tf: TransportFactory) -> int:
    if a.name:
        print(json.dumps(library.starter(a.name), indent=2))
        return 0
    items = []
    for name in library.starter_names():
        r = library.starter(name)
        items.append({"name": name, "description": r.get("description", ""), "tags": r.get("tags", []),
                      "summary": library.summarize(r)})
    out.emit({"recipes": items},
             [f"{i['name']}: {i['description']}\n    {i['summary']}" for i in items])
    return 0


def cmd_schema(a, out: Out, tf: TransportFactory) -> int:
    print(json.dumps(library.schema(), indent=2))
    return 0


def cmd_install_skill(a, out: Out, tf: TransportFactory) -> int:
    source = library.skill_source()
    targets: list[Path] = []
    if a.claude:
        targets.append(Path.home() / ".claude" / "skills" / "x100vi")
    if a.codex:
        targets.append(codex_home() / "skills" / "x100vi")
    if a.dir:
        targets.append(Path(a.dir).expanduser() / "x100vi")
    if not targets:
        raise RecipeError("say where to install the skill",
                          "Use --claude (~/.claude/skills), --codex (~/.codex/skills) and/or --dir SKILLS_FOLDER.")
    done = []
    for target in targets:
        if target.is_symlink() or target.exists():
            marker = target / "SKILL.md"
            ours = target.is_symlink() or (marker.is_file() and "name: x100vi" in marker.read_text(encoding="utf-8"))
            if not ours and not a.force:
                raise X100VIError(f"{target} exists and is not this skill", "Remove it, or add --force to replace it.")
            if target.is_symlink() or target.is_file():
                target.unlink()
            else:
                shutil.rmtree(target)
        target.parent.mkdir(parents=True, exist_ok=True)
        linked = False
        if a.link:
            try:
                target.symlink_to(source, target_is_directory=True)
                linked = True
            except OSError:   # Windows without the symlink privilege
                pass
        if not linked:
            shutil.copytree(source, target)
        done.append(str(target))
    out.emit({"installed": done, "linked": a.link},
             [f"skill {'linked' if a.link else 'installed'} at {p}" for p in done]
             + ["Start a new Claude Code or Codex session to pick it up."])
    return 0


def cmd_sandbox_help(a, out: Out, tf: TransportFactory) -> int:
    from . import sandbox
    from .ptp import detect_sandbox

    rules = sandbox.codex_rules()
    rules_path = sandbox.codex_rules_path()
    settings = sandbox.claude_settings()
    written = None
    if a.write_codex_rules:
        rules_path.parent.mkdir(parents=True, exist_ok=True)
        rules_path.write_text(rules, encoding="utf-8")
        written = str(rules_path)
    inside = detect_sandbox()
    result = {"running_in_sandbox": inside,
              "codex": {"rules_file": str(rules_path), "rules": rules, "written": written},
              "claude_code": {"settings_file": "~/.claude/settings.json", "settings": settings}}
    lines = [
        "Agents can run commands in a sandbox that hides USB devices. Camera commands must run outside it.",
        f"This command is running inside the {inside} sandbox." if inside else "This command is not sandboxed.",
        "",
        "In the moment: approve the agent's request to run the x100vi command outside the sandbox.",
        "",
        f"Codex, permanently: save the rules below as {rules_path} and restart Codex",
        "  (or run `x100vi sandbox-help --write-codex-rules`). Reading and previewing then run without",
        "  a prompt; `set` and `restore` still ask before each run.",
        "",
        rules,
        "Claude Code (only if you turned its sandbox on): merge this into ~/.claude/settings.json:",
        "",
        json.dumps(settings, indent=2),
    ]
    if written:
        lines = [f"wrote {written}; restart Codex to load it", "", *lines]
    out.emit(result, lines)
    return 0


# -- wiring ------------------------------------------------------------------------------
def build_parser() -> argparse.ArgumentParser:
    common = argparse.ArgumentParser(add_help=False)
    common.add_argument("--json", action="store_true", help="print one JSON object instead of text")
    p = argparse.ArgumentParser(
        prog="x100vi", parents=[common],
        description="Read, write and preview film-simulation recipes on a Fujifilm X100VI over USB.",
        epilog=f"The camera must be in: {MODE_MENU}")
    p.add_argument("--version", action="version", version=f"x100vi {__version__}")
    sub = p.add_subparsers(dest="command", required=True, metavar="COMMAND")

    def add(name, func, help_text, **kw):
        sp = sub.add_parser(name, help=help_text, description=help_text, parents=[common], **kw)
        sp.set_defaults(func=func)
        return sp

    add("doctor", cmd_doctor, "check that the camera is connected, supported and in the right USB mode")

    sp = add("get", cmd_get, "print a recipe from the camera as JSON")
    sp.add_argument("target", nargs="?", default="live", help="live, C1..C7, or all (default: live)")
    sp.add_argument("-o", "--output", help="write to this file instead of printing")

    sp = add("set", cmd_set, "write a recipe to the live settings or to a custom setting")
    sp.add_argument("target", help="live or C1..C7")
    sp.add_argument("recipe", help="recipe .json file, or the name of a starter recipe")
    sp.add_argument("--overwrite", action="store_true", help="replace a custom setting that is not empty")
    sp.add_argument("--partial", action="store_true", help="change only the settings in the file")
    sp.add_argument("--dry-run", action="store_true", help="validate and show the plan without connecting")
    sp.add_argument("--no-backup", action="store_true", help="do not save the previous contents first")

    sp = add("backup", cmd_backup, "save the live settings and C1-C7 to a JSON file, or (--full) every setting")
    sp.add_argument("-o", "--output", help="file to write (default: a timestamped file in the state folder)")
    sp.add_argument("--full", action="store_true",
                    help="save the camera's own settings file instead: every menu setting plus C1-C7")

    sp = add("restore", cmd_restore,
             "write a backup back to the camera: C1-C7 from a JSON backup, or everything from a --full file")
    sp.add_argument("file")
    sp.add_argument("--live", action="store_true", help="JSON backups: also restore the live shooting settings")
    sp.add_argument("--yes", action="store_true", help="confirm overwriting the camera's settings")
    sp.add_argument("--other-body", action="store_true",
                    help="allow a settings file saved from a different X100VI body")

    sp = add("validate", cmd_validate, "check recipe files without a camera")
    sp.add_argument("files", nargs="+")

    sp = add("preview", cmd_preview, "develop RAF files with recipes on the camera's own processor")
    sp.add_argument("rafs", nargs="+", metavar="RAF", help=".RAF files shot on an X100VI")
    sp.add_argument("-r", "--recipe", nargs="+", default=[], metavar="RECIPE", help="recipe files or starter names")
    sp.add_argument("--as-shot", action="store_true", help="also render each RAF with its own settings")
    sp.add_argument("-o", "--out", default="x100vi-previews", help="output folder (default: ./x100vi-previews)")
    sp.add_argument("--full", action="store_true", help="full resolution instead of half size")
    sp.add_argument("--format", choices=["jpeg", "heif", "tiff", "tiff16"], default="jpeg",
                    help="output file type (default jpeg); tiff and tiff16 need --full")
    sp.add_argument("--ev", type=float, default=0.0, help="exposure compensation in 1/3 EV steps, -3..+3")
    sp.add_argument("--no-sheet", dest="sheet", action="store_false", help="do not build a contact sheet")
    sp.add_argument("--verify", action="store_true",
                    help="check each output's EXIF against its recipe (needs exiftool)")
    sp.add_argument("--timeout", type=float, default=90.0, help=argparse.SUPPRESS)

    sp = add("verify", cmd_verify, "compare a photo's recorded settings with a recipe (needs exiftool)")
    sp.add_argument("photo")
    sp.add_argument("-r", "--recipe", required=True)

    sp = add("recipes", cmd_recipes, "list the bundled starter recipes, or print one")
    sp.add_argument("name", nargs="?")

    add("schema", cmd_schema, "print the recipe JSON schema")

    sp = add("sandbox-help", cmd_sandbox_help,
             "show how to let an agent run camera commands outside its sandbox (USB is hidden inside one)")
    sp.add_argument("--write-codex-rules", action="store_true",
                    help="write the Codex rules file (~/.codex/rules/x100vi.rules)")

    sp = add("install-skill", cmd_install_skill, "install the agent skill for Claude Code and/or Codex")
    sp.add_argument("--claude", action="store_true", help="install into ~/.claude/skills")
    sp.add_argument("--codex", action="store_true", help="install into ~/.codex/skills")
    sp.add_argument("--dir", help="install into this skills folder")
    sp.add_argument("--link", action="store_true", help="symlink instead of copying (for development)")
    sp.add_argument("--force", action="store_true", help="replace an existing folder that is not this skill")
    return p


def main(argv: list[str] | None = None, transport_factory: TransportFactory = None) -> int:
    args = build_parser().parse_args(argv)
    out = Out(args.json)
    try:
        return args.func(args, out, transport_factory)
    except X100VIError as e:
        if args.json:
            print(json.dumps(e.as_dict(), indent=2))
        print(f"error: {e}", file=sys.stderr)
        if e.hint:
            print(f"hint: {e.hint}", file=sys.stderr)
        return e.exit_code
    except KeyboardInterrupt:
        print("interrupted", file=sys.stderr)
        return 130


if __name__ == "__main__":
    sys.exit(main())
