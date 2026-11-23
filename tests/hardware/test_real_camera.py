"""Tests against a real X100VI. Skipped unless X100VI_HARDWARE=1.

    X100VI_HARDWARE=1 uv run pytest tests/hardware -v
    X100VI_HARDWARE=1 X100VI_TEST_RAF=/path/to/shot.RAF uv run pytest tests/hardware -v

The camera must be in USB RAW CONV./BACKUP RESTORE mode. One custom setting is written and then
restored byte for byte: C7 by default, or the one named in X100VI_TEST_SLOT.
"""
import os
import shutil
from pathlib import Path

import pytest

from x100vi import exifcheck, library
from x100vi import model as m
from x100vi.camera import open_camera
from x100vi.rawconv import RawConverter, apply_recipe, describe, inspect_raf

pytestmark = pytest.mark.camera
SLOT = int(os.environ.get("X100VI_TEST_SLOT", "7"))
RAF = os.environ.get("X100VI_TEST_RAF")
PROBE = {"name": "X100VI SKILL TEST", "film_simulation": "classic_neg",
         "grain": {"strength": "strong", "size": "large"},
         "color_chrome_effect": "weak", "color_chrome_fx_blue": "strong", "smooth_skin": "weak",
         "white_balance": {"mode": "kelvin", "kelvin": 5550, "shift_r": 4, "shift_b": -7},
         "dynamic_range": "400", "highlight": -1.5, "shadow": 2.5, "color": -3, "sharpness": 3,
         "noise_reduction": -4, "clarity": -2}


def raw_register(cam, slot):
    cam.session.set_u16(m.SLOT_SELECT, slot)
    return {c: cam.session.prop_raw(c) for c in range(0xD18D, 0xD1A5)}


def test_camera_is_ready():
    with open_camera() as cam:
        cam.require_ready()
        assert cam.info.model == "X100VI" and cam.info.battery_percent is not None


def test_reads_live_and_every_register_as_valid_recipes():
    with open_camera() as cam:
        data = cam.backup()
    for recipe in [data["live"], *data["slots"].values()]:
        m.encode_recipe(recipe)


def test_register_round_trip_and_exact_restore():
    with open_camera() as cam:
        original_raw = raw_register(cam, SLOT)
        original = cam.read(SLOT)
        live_before = cam.read(None)
        try:
            cam.write(PROBE, SLOT)
            got = cam.read(SLOT)
            for key, value in PROBE.items():
                assert got[key] == value, key
        finally:
            cam.clear_wb_memory(SLOT)
            cam.write(original, SLOT)
        assert raw_register(cam, SLOT) == original_raw
        assert cam.read(None) == live_before


@pytest.mark.skipif(not RAF, reason="set X100VI_TEST_RAF to an X100VI .RAF file")
def test_develops_a_raf_and_the_output_carries_the_recipe(tmp_path):
    raf = inspect_raf(RAF)
    recipes = {name: library.starter(name) for name in ("desert-gold", "street-mono")}
    with open_camera() as cam:
        conv = RawConverter(cam)
        shot = conv.upload(Path(RAF).read_bytes())
        assert describe(shot)["film_simulation"] in m.FILM_SIMULATION
        for name, recipe in recipes.items():
            profile, adjustments = apply_recipe(shot, recipe)
            image = conv.develop(profile)
            assert image[:2] == b"\xff\xd8" and len(image) > 500_000
            out = tmp_path / f"{raf.path.stem}__{name}.jpg"
            out.write_bytes(image)
            if shutil.which("exiftool"):
                effective = dict(recipe)
                for adj in adjustments:
                    if adj.setting == "dynamic_range":
                        effective["dynamic_range"] = adj.applied
                assert exifcheck.compare(effective, out)["mismatches"] == []


def test_reads_the_settings_file_twice_identically():
    from x100vi import settingsfile

    with open_camera() as cam:
        first, second = cam.read_settings_file(), cam.read_settings_file()
    header = settingsfile.parse_header(first)
    assert first == second and header.model == "X100VI" and len(first) > 10_000


@pytest.mark.skipif(not RAF, reason="set X100VI_TEST_RAF to an X100VI .RAF file")
def test_develops_heif_and_full_size_tiff():
    with open_camera() as cam:
        conv = RawConverter(cam)
        shot = conv.upload(Path(RAF).read_bytes())
        heif = conv.develop(shot, output="heif")
        assert heif[4:8] == b"ftyp" and len(heif) > 500_000
        tiff = conv.develop(shot, full=True, output="tiff")
        assert tiff[:4] == b"II*\x00" and len(tiff) > 100_000_000
