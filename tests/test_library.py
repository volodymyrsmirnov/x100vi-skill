"""Bundled recipes, the schema, EXIF expectations and the contact sheet."""
import json

import pytest
from PIL import Image

from x100vi import exifcheck, library
from x100vi import model as m
from x100vi.sheet import contact_sheet


@pytest.mark.parametrize("name", library.starter_names())
def test_starter_recipes_are_valid_and_described(name):
    recipe = library.starter(name)
    m.encode_recipe(recipe)
    assert recipe["name"] and len(recipe["name"]) <= m.NAME_MAX
    assert len(recipe["description"]) > 20 and recipe["tags"] and recipe["notes"]
    assert str(recipe.get("d_range_priority", "off")) != "auto"      # so every starter can be previewed


def test_schema_matches_the_model():
    props = library.schema()["properties"]
    assert set(props) == m.TOP_LEVEL
    assert set(props["film_simulation"]["enum"]) == set(m.FILM_SIMULATION)
    assert set(props["white_balance"]["properties"]["mode"]["enum"]) == set(m.WHITE_BALANCE)
    assert set(props["white_balance"]["properties"]) == m.NESTED["white_balance"]
    assert set(props["grain"]["properties"]) == m.NESTED["grain"]
    assert set(props["monochromatic"]["properties"]) == m.NESTED["monochromatic"]
    for key in m.OPTIONAL:
        assert set(props[key]["enum"]) == set(m.BY_KEY[key].table), key
    for key in ("highlight", "shadow", "color", "sharpness", "clarity"):
        f = m.BY_KEY[key]
        assert (props[key]["minimum"], props[key]["maximum"]) == (f.lo, f.hi), key
    assert props["name"]["maxLength"] == m.NAME_MAX


def test_auto_backups_never_overwrite_each_other(isolated_state):
    a = library.save_auto_backup("C1", {"name": "A"})
    b = library.save_auto_backup("C1", {"name": "B"})
    assert a != b and json.loads(a.read_text())["name"] == "A" and a.parent == isolated_state / "backups"


def test_state_dir_honours_overrides(monkeypatch, tmp_path):
    monkeypatch.delenv("X100VI_HOME")
    monkeypatch.setenv("XDG_STATE_HOME", str(tmp_path / "xdg"))
    monkeypatch.setattr("sys.platform", "linux")
    assert library.state_dir() == tmp_path / "xdg" / "x100vi"
    monkeypatch.setattr("sys.platform", "win32")
    monkeypatch.setenv("LOCALAPPDATA", str(tmp_path / "local"))
    assert library.state_dir() == tmp_path / "local" / "x100vi"


def test_skill_is_packaged_with_its_references():
    root = library.skill_source()
    text = (root / "SKILL.md").read_text()
    assert text.startswith("---\nname: x100vi\n") and "description:" in text
    for ref in ("recipe-design.md", "recipe-schema.md", "troubleshooting.md"):
        assert (root / "references" / ref).is_file(), ref
        assert f"references/{ref}" in text
    for launcher in ("x100vi.sh", "x100vi.ps1"):
        assert (root / "scripts" / launcher).is_file(), launcher
        assert f"scripts/{launcher}" in text.replace("\\", "/")
    posix, windows = ((root / "scripts" / n).read_text() for n in ("x100vi.sh", "x100vi.ps1"))
    source = "git+https://github.com/volodymyrsmirnov/x100vi-skill"
    assert source in posix and source in windows            # both launchers install from the same place


def test_expected_exif_tags_for_a_colour_recipe():
    tags = exifcheck.expected_tags({"film_simulation": "reala_ace", "dynamic_range": "200", "color": 2,
                                    "grain": {"strength": "weak", "size": "large"}, "smooth_skin": "strong",
                                    "white_balance": {"mode": "kelvin", "kelvin": 5000, "shift_r": 1, "shift_b": -2}})
    assert tags["FilmMode"] == "Reala ACE" and tags["Saturation#"] == 2
    assert tags["WhiteBalance"] == "Kelvin" and tags["ColorTemperature#"] == 5000
    assert tags["WhiteBalanceFineTune"] == "Red +20, Blue -40"
    assert (tags["GrainEffectRoughness"], tags["GrainEffectSize"]) == ("Weak", "Large")
    assert tags[exifcheck.SMOOTH_SKIN_TAG] == 64 and tags["DevelopmentDynamicRange#"] == 200


def test_expected_exif_tags_for_monochrome_and_priority():
    tags = exifcheck.expected_tags({"film_simulation": "acros_r", "monochromatic": {"warm_cool": 3},
                                    "d_range_priority": "strong", "highlight": 2})
    assert tags["Saturation"] == "Acros Red Filter" and "FilmMode" not in tags
    assert tags["BWAdjustment#"] == 3 and tags["DRangePriority"] == "Fixed"
    assert "HighlightTone#" not in tags and tags["GrainEffectSize"] == "Off"


def test_compare_reports_mismatches(monkeypatch, tmp_path):
    photo = tmp_path / "a.jpg"
    photo.write_bytes(b"x")
    recipe = {"film_simulation": "velvia", "color": 2}
    tags = {k.rstrip("#").split("|")[0]: v for k, v in exifcheck.expected_tags(recipe).items()}
    monkeypatch.setattr(exifcheck, "read_tags", lambda p: dict(tags))
    assert exifcheck.compare(recipe, photo)["mismatches"] == []
    tags["FilmMode"] = "Classic Chrome"
    tags["Saturation"] = "+1 (medium high)"
    bad = exifcheck.compare(recipe, photo)["mismatches"]
    assert {x["tag"] for x in bad} == {"FilmMode", "Saturation"}


def test_contact_sheet_keeps_orientation_and_labels_rows(tmp_path):
    wide, tall = tmp_path / "wide.jpg", tmp_path / "tall.jpg"
    Image.new("RGB", (300, 200), (200, 80, 40)).save(wide)
    Image.new("RGB", (200, 300), (40, 80, 200)).save(tall)
    out = contact_sheet([[("As shot", wide), ("Recipe", wide)], [("As shot", tall)]], tmp_path / "s" / "sheet.jpg")
    with Image.open(out) as im:
        assert im.width > 600 and im.height > 500
    with pytest.raises(ValueError):
        contact_sheet([], tmp_path / "x.jpg")
