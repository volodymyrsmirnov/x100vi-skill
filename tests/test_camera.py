"""Reading and writing recipes against the fake camera."""
import pytest

from fake_camera import FakeX100VI
from x100vi import model as m
from x100vi.camera import open_camera
from x100vi.errors import RecipeError, UnsupportedCameraError, WrongModeError

PORTRA = {"name": "PORTRA", "film_simulation": "classic_chrome", "grain": {"strength": "strong", "size": "small"},
          "color_chrome_effect": "strong", "color_chrome_fx_blue": "weak",
          "white_balance": {"mode": "kelvin", "kelvin": 5200, "shift_r": 1, "shift_b": -6},
          "dynamic_range": "400", "highlight": 0, "shadow": -2, "color": 2, "sharpness": -2,
          "noise_reduction": -4, "clarity": -2}


def test_identity_and_battery(camera):
    assert camera.info.as_dict() == {"model": "X100VI", "firmware": "1.31", "usb_mode": "raw_conv",
                                     "ready": True, "battery_percent": 80, "warnings": []}


def test_card_reader_mode_is_refused_with_the_menu_path():
    with open_camera(FakeX100VI(mode="card_reader")) as cam:
        assert cam.info.mode == "card_reader" and not cam.info.ready
        with pytest.raises(WrongModeError) as e:
            cam.read(1)
        assert "USB CARD READER" in str(e.value)
        assert "CONNECTION MODE" in e.value.hint and "USB RAW CONV./BACKUP RESTORE" in e.value.hint


def test_other_models_are_refused():
    with open_camera(FakeX100VI(model="X-T5")) as cam, pytest.raises(UnsupportedCameraError) as e:
        cam.write({}, 1)
    assert "X-T5" in str(e.value)


def test_untested_firmware_and_low_battery_warn_but_work():
    with open_camera(FakeX100VI(firmware="1.40", battery=9)) as cam:
        assert any("1.40" in w for w in cam.info.warnings)
        assert any("battery" in w for w in cam.info.warnings)
        cam.write({"film_simulation": "velvia"}, 2)
        assert cam.read(2)["film_simulation"] == "velvia"


def test_session_is_closed_even_when_a_command_fails(fake):
    with pytest.raises(RecipeError), open_camera(fake) as cam:
        cam.write({"color": 99}, 1)
    assert fake.closed and not fake.session_open


def test_write_then_read_round_trips(camera):
    camera.write(PORTRA, 3)
    got = camera.read(3)
    for key, value in PORTRA.items():
        assert got[key] == value, key


def test_factory_register_reads_as_blank(camera):
    assert m.is_blank(camera.read(5))


def test_write_always_selects_the_register_first(camera, fake):
    fake.selected = 6
    camera.write({"name": "A", "film_simulation": "velvia"}, 2)
    assert fake.slot_value(2, "film_simulation") == "velvia"
    assert fake.slot_value(6, "film_simulation") == "provia"
    assert fake.writes[0][0] == m.SLOT_SELECT


def test_first_naming_quirk_does_not_leak_live_settings_into_the_register(camera, fake):
    assert fake.live["image_quality"] == 1          # the owner shoots RAW ...
    camera.write({"name": "A", "film_simulation": "velvia"}, 2)
    assert fake.slot_value(2, "image_quality") == "fine"    # ... but the register keeps its own quality
    assert fake.slot_value(2, "dynamic_range") == "100"


def test_grain_off_keeps_the_requested_size(camera, fake):
    camera.write({"grain": {"strength": "strong", "size": "small"}}, 1)
    camera.write({"grain": {"strength": "off", "size": "large"}}, 1)
    assert fake.slots[1]["grain"] == 7
    assert camera.read(1)["grain"] == {"strength": "off", "size": "large"}


def test_kelvin_change_does_not_lose_the_shift(camera):
    camera.write({"white_balance": {"mode": "kelvin", "kelvin": 5500, "shift_r": 9, "shift_b": -9}}, 1)
    assert camera.read(1)["white_balance"] == {"mode": "kelvin", "kelvin": 5500, "shift_r": 9, "shift_b": -9}


def test_d_range_priority_can_be_set_and_cleared(camera):
    camera.write({"d_range_priority": "strong"}, 1)
    assert camera.read(1)["d_range_priority"] == "strong"
    camera.write({"dynamic_range": "400", "highlight": 2, "shadow": -1}, 1)
    got = camera.read(1)
    assert (got["d_range_priority"], got["dynamic_range"], got["highlight"], got["shadow"]) == ("off", "400", 2, -1)


def test_monochrome_recipe_skips_colour_and_sets_tint(camera):
    camera.write({"film_simulation": "acros_r", "monochromatic": {"warm_cool": -5, "magenta_green": 7}}, 4)
    got = camera.read(4)
    assert got["monochromatic"] == {"warm_cool": -5, "magenta_green": 7}
    assert "color" not in got


def test_hidden_mono_tint_is_cleared_by_a_full_colour_recipe(camera, fake):
    camera.write({"film_simulation": "acros", "monochromatic": {"warm_cool": 9, "magenta_green": -9}}, 4)
    camera.write({"film_simulation": "velvia"}, 4)
    assert fake.slots[4]["monochromatic.warm_cool"] == 0 and fake.slots[4]["monochromatic.magenta_green"] == 0
    assert fake.slot_value(4, "film_simulation") == "velvia"


def test_partial_write_changes_only_the_given_settings(camera):
    camera.write(PORTRA, 3)
    camera.write({"film_simulation": "reala_ace"}, 3, partial=True)
    got = camera.read(3)
    assert got["film_simulation"] == "reala_ace" and got["name"] == "PORTRA" and got["color"] == 2


def test_live_settings_can_be_written_and_registers_stay_put(camera, fake):
    camera.write({"film_simulation": "classic_neg", "color": -2}, None)
    live = camera.read(None)
    assert live["film_simulation"] == "classic_neg" and live["color"] == -2 and "name" not in live
    assert all(m.is_blank(camera.read(n)) for n in m.SLOTS)


def test_live_read_omits_image_size_while_shooting_raw_only(camera):
    assert camera.read(None)["image_quality"] == "raw"
    assert "image_size" not in camera.read(None)


def test_invalid_recipe_writes_nothing(camera, fake):
    before = len(fake.writes)
    with pytest.raises(RecipeError):
        camera.write({"film_simulation": "kodachrome"}, 1)
    assert len(fake.writes) == before


def test_a_setting_the_camera_refuses_is_reported_by_name(camera, fake, monkeypatch):
    real = fake._write_field
    monkeypatch.setattr(fake, "_write_field", lambda state, f, target, wire:
                        0x201C if f.key == "clarity" else real(state, f, target, wire))
    with pytest.raises(RecipeError) as e:
        camera.write({"clarity": 3}, 1)
    assert "clarity" in str(e.value)


def test_a_setting_the_camera_silently_changes_is_caught(camera, fake, monkeypatch):
    real = fake._write_field

    def sticky(state, f, target, wire):
        code = real(state, f, target, wire)
        if f.key == "sharpness":
            state["sharpness"] = 0            # accepts the write, keeps the old value
        return code

    monkeypatch.setattr(fake, "_write_field", sticky)
    with pytest.raises(RecipeError) as e:
        camera.write({"sharpness": 2}, 1)
    assert "did not keep" in str(e.value)


def test_backup_contains_live_and_all_registers(camera):
    camera.write(PORTRA, 3)
    data = camera.backup()
    assert set(data["slots"]) == {str(n) for n in m.SLOTS}
    assert data["slots"]["3"]["name"] == "PORTRA" and data["live"]["image_quality"] == "raw"
    for recipe in [*data["slots"].values(), data["live"]]:
        m.encode_recipe(recipe)       # a backup is always restorable
