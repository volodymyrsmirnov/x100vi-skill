"""Recipe validation and wire encoding (no camera)."""
import pytest

from x100vi import model as m
from x100vi.errors import RecipeError


def wire(recipe, partial=False):
    return {f.key: w for f, w in m.encode_recipe(recipe, partial)}


def test_empty_recipe_is_the_factory_look():
    w = wire({})
    assert (w["film_simulation"], w["dynamic_range"], w["grain"], w["white_balance.mode"]) == (1, 100, 1, 2)
    assert w["noise_reduction"] == 0x2000 and w["color_chrome_effect"] == 1
    assert "white_balance.kelvin" not in w and "monochromatic.warm_cool" not in w


def test_signed_and_scaled_values():
    w = wire({"highlight": -1.5, "shadow": 3.5, "color": -4, "clarity": 5,
              "white_balance": {"shift_r": -9, "shift_b": 9}})
    assert w["highlight"] == 0x10000 - 15 and w["shadow"] == 35
    assert w["color"] == 0x10000 - 40 and w["clarity"] == 50
    assert w["white_balance.shift_r"] == 0x10000 - 9 and w["white_balance.shift_b"] == 9


@pytest.mark.parametrize("f", m.FIELDS, ids=lambda f: f.key)
def test_every_value_round_trips(f):
    if f.kind == "enum":
        values = list(f.table)
    elif f.kind == "grain":
        values = [{"strength": "weak", "size": "large"}, {"strength": "strong", "size": "small"}]
    else:
        steps = round((f.hi - f.lo) / f.step)
        values = [f.lo + i * f.step for i in range(steps + 1)]
    for value in values:
        assert m.decode_field(f, m.encode_field(f, value)) == value


def test_noise_reduction_codes_are_the_cameras_irregular_table():
    assert [m.NOISE_REDUCTION[n] for n in range(-4, 5)] == [
        0x8000, 0x7000, 0x4000, 0x3000, 0x2000, 0x1000, 0x0000, 0x6000, 0x5000]


def test_grain_off_is_written_as_1_and_read_as_6_or_7():
    g = m.BY_KEY["grain"]
    assert m.encode_field(g, {"strength": "off", "size": "large"}) == 1
    assert m.decode_field(g, 7) == {"strength": "off", "size": "large"}
    assert m.decode_field(g, 6) == {"strength": "off", "size": "small"}


@pytest.mark.parametrize("bad, fragment", [
    ({"highlight": 4.5}, "out of range"),
    ({"highlight": 0.25}, "grid"),
    ({"color": 5}, "out of range"),
    ({"clarity": "high"}, "not a number"),
    ({"noise_reduction": True}, "not valid"),
    ({"dynamic_range": 800}, "not valid"),
    ({"white_balance": {"shift_r": 10}}, "out of range"),
    ({"white_balance": {"mode": "kelvin"}}, "no kelvin value"),
    ({"white_balance": {"mode": "auto", "kelvin": 5000}}, "only used when"),
    ({"white_balance": {"mode": "kelvin", "kelvin": 5555}}, "grid"),
    ({"white_balance": {"tint": 3}}, "unknown setting"),
    ({"monochromatic": {"warm_cool": 3}}, "monochrome or acros"),
    ({"film_simulation": "monochrome", "color": 2}, "does not apply"),
    ({"image_quality": "raw", "image_size": "l_3:2"}, "cannot be set while"),
    ({"name": "x" * 26}, "25"),
    ({"name": "CAFÉ"}, "ASCII"),
    ({"grain": "strong"}, "must be an object"),
    ({"grain": {"strength": "medium"}}, "not valid"),
    ({"iso": 400}, "unknown setting"),
    ([], "JSON object"),
])
def test_rejects_what_the_camera_cannot_hold(bad, fragment):
    with pytest.raises(RecipeError) as e:
        m.encode_recipe(bad)
    assert fragment in str(e.value)


def test_typos_get_a_suggestion():
    with pytest.raises(RecipeError) as e:
        m.encode_recipe({"film_simulation": "clasic_chrome"})
    assert "classic_chrome" in str(e.value)
    with pytest.raises(RecipeError) as e:
        m.encode_recipe({"film_simulaton": "velvia"})
    assert "film_simulation" in e.value.hint


def test_unknown_setting_explains_what_usb_cannot_do():
    with pytest.raises(RecipeError) as e:
        m.encode_recipe({"iso": 400})
    assert "ISO" in e.value.hint


def test_values_are_case_insensitive():
    assert wire({"film_simulation": "Classic_Chrome", "grain": {"strength": "Weak", "size": "LARGE"}})["grain"] == 4


def test_mono_and_kelvin_preconditions():
    w = wire({"film_simulation": "acros", "monochromatic": {"warm_cool": -3, "magenta_green": 2},
              "white_balance": {"mode": "kelvin", "kelvin": 5500}})
    assert w["monochromatic.warm_cool"] == 0x10000 - 30 and w["monochromatic.magenta_green"] == 20
    assert w["white_balance.kelvin"] == 5500
    assert wire({"film_simulation": "acros"})["monochromatic.warm_cool"] == 0
    assert "color" not in wire({"film_simulation": "sepia"})


def test_write_order_respects_the_cameras_locks():
    recipe = {"film_simulation": "acros", "white_balance": {"mode": "kelvin", "kelvin": 3000, "shift_r": 2}}
    keys = [f.key for f, _ in m.encode_recipe(recipe)]
    assert keys.index("film_simulation") < keys.index("monochromatic.warm_cool")
    assert keys.index("white_balance.mode") < keys.index("white_balance.kelvin") < keys.index("white_balance.shift_r")
    order = [(f.key, w) for f, w in m.encode_recipe({"d_range_priority": "strong"})]
    assert order[0] == ("d_range_priority", 0) and order[-1] == ("d_range_priority", 2)
    keys = [f.key for f, _ in m.encode_recipe({"image_quality": "fine", "image_size": "l_3:2"})]
    assert keys.index("image_quality") < keys.index("image_size")


def test_partial_touches_only_what_is_given():
    assert wire({"film_simulation": "reala_ace"}, partial=True) == {"film_simulation": 20}


def test_housekeeping_settings_are_never_defaulted():
    assert not m.OPTIONAL & set(wire({}))


def test_unknown_codes_round_trip():
    f = m.BY_KEY["image_size"]
    assert m.decode_field(f, 0x000B) == "unknown:0x000B"
    assert m.encode_field(f, "unknown:0x000B") == 0x000B


@pytest.mark.parametrize("text, slot", [("live", None), ("LIVE", None), ("c3", 3), ("C7", 7), ("1", 1)])
def test_parse_target(text, slot):
    assert m.parse_target(text) == slot


@pytest.mark.parametrize("text", ["c0", "c8", "8", "slot1", ""])
def test_parse_target_rejects(text):
    with pytest.raises(RecipeError):
        m.parse_target(text)


def test_is_blank():
    assert m.is_blank({"name": "", "film_simulation": "provia"})
    assert not m.is_blank({"name": "X"})
    assert not m.is_blank({"name": "", "film_simulation": "velvia"})
    assert not m.is_blank({"name": "", "white_balance": {"mode": "auto", "shift_r": 1, "shift_b": 0}})


def test_ignored_by_priority():
    recipe = {"d_range_priority": "weak", "highlight": 1, "dynamic_range": "200"}
    assert m.ignored_by_priority(recipe) == ["dynamic_range", "highlight"]
    assert m.ignored_by_priority({"highlight": 1}) == []
