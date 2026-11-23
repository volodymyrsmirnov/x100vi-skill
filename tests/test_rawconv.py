"""The conversion profile and the upload/develop sequence."""
import pytest

from fake_camera import AS_SHOT_PROFILE, make_raf, rendered_cells
from x100vi.errors import ConversionError, RecipeError
from x100vi.rawconv import HOST_CELLS, TABLE, Profile, RawConverter, apply_recipe, describe, inspect_raf

NEGATIVE = {"film_simulation": "reala_ace", "dynamic_range": "400", "grain": {"strength": "weak", "size": "small"},
            "color_chrome_effect": "strong",
            "white_balance": {"mode": "kelvin", "kelvin": 5000, "shift_r": 0, "shift_b": -2},
            "highlight": -1, "shadow": -0.5, "color": 2, "sharpness": -1, "noise_reduction": -4, "clarity": -2}


def as_shot() -> Profile:
    p = Profile(bytearray(AS_SHOT_PROFILE))
    p.validate()
    return p


# -- profile layout (fixture: a real X100VI frame, Classic Chrome, DR200, D-range priority on) --------
def test_reads_the_cameras_as_shot_profile():
    p = as_shot()
    assert (len(p.data), p.cells, p.iop_code) == (625, 29, "FF179503")
    d = describe(p)
    assert (d["film_simulation"], d["dynamic_range"], d["d_range_priority"]) == ("classic_chrome", "200", "weak")
    assert d["white_balance"]["mode"] == "as_shot" and d["highlight"] == -1 and d["exposure_ev"] == 0


def test_host_form_extends_without_moving_cells():
    p = as_shot()
    host = Profile(bytearray(p.host_form()))
    assert len(host.data) == TABLE + 4 * HOST_CELLS == 629
    assert int.from_bytes(host.data[:2], "little") == 29 and host.data[2] == 9
    assert host.values()[:29] == p.values() and host.get(29) == 0 and host.iop_code == "FF179503"


def test_garbage_is_not_patched():
    with pytest.raises(ConversionError):
        Profile(bytearray(700)).validate()
    with pytest.raises(ConversionError):
        Profile(bytearray(40)).validate()


# -- applying a recipe ---------------------------------------------------------------------------
def test_recipe_becomes_profile_cells():
    p, adjustments = apply_recipe(as_shot(), NEGATIVE)
    want = {2: 7, 5: 0, 6: 200, 7: 0, 8: 20, 9: 2, 10: 3, 11: 2, 12: 0x8007, 13: 0, 14: -2, 15: 5000,
            16: -10, 17: -5, 18: 20, 19: -10, 20: 0x8000, 24: 1, 25: 1, 27: -20}
    assert {i: p.get(i) for i in want} == want
    assert (p.get(3), p.get(4), p.get(21), p.get(22), p.get(28)) == (7, 2, 1, 1, 2)     # left as shot
    assert [a.setting for a in adjustments] == ["dynamic_range"]


def test_dynamic_range_above_the_frames_own_is_capped_and_explained():
    _, adjustments = apply_recipe(as_shot(), {"dynamic_range": "400"})
    (adj,) = adjustments
    assert (adj.setting, adj.requested, adj.applied) == ("dynamic_range", "400", "200")
    assert "shot at DR200" in adj.reason
    assert adj.as_dict()["reason"] == adj.reason


def test_dynamic_range_at_or_below_the_frame_is_applied_silently():
    for dr in ("100", "200"):
        p, adjustments = apply_recipe(as_shot(), {"dynamic_range": dr})
        assert p.get(6) == int(dr) and adjustments == []


def test_dynamic_range_auto_is_reported():
    p, adjustments = apply_recipe(as_shot(), {"dynamic_range": "auto"})
    assert p.get(6) == 200 and adjustments[0].requested == "auto"


def test_d_range_priority_from_the_frame_is_replaced_by_the_recipes():
    assert as_shot().get(7) == 1
    assert apply_recipe(as_shot(), {})[0].get(7) == 0
    assert apply_recipe(as_shot(), {"d_range_priority": "strong"})[0].get(7) == 2


def test_priority_on_reports_the_settings_it_makes_the_camera_ignore():
    _, adjustments = apply_recipe(as_shot(), {"d_range_priority": "weak", "highlight": 2, "dynamic_range": "100"})
    assert adjustments[-1].applied == "ignored" and "highlight" in adjustments[-1].setting


def test_priority_auto_cannot_be_previewed():
    with pytest.raises(RecipeError) as e:
        apply_recipe(as_shot(), {"d_range_priority": "auto"})
    assert "weak or strong" in e.value.hint


def test_monochrome_recipe_leaves_the_colour_cell_alone():
    recipe = {"film_simulation": "monochrome_g", "white_balance": {"mode": "daylight", "shift_r": -6, "shift_b": -3}}
    p, _ = apply_recipe(as_shot(), recipe)
    assert (p.get(8), p.get(23), p.get(26), p.get(12), p.get(13), p.get(14)) == (9, 0, 0, 4, -6, -3)
    assert p.get(18) == as_shot().get(18)


def test_the_as_shot_profile_is_never_modified():
    base = as_shot()
    before = bytes(base.data)
    apply_recipe(base, NEGATIVE, 1 / 3)
    assert bytes(base.data) == before


@pytest.mark.parametrize("ev, cell", [
    (0, 0), (1 / 3, 333), (0.33, 333), (2 / 3, 667), (0.67, 667), (-1 / 3, -333), (1, 1000), (-3, -3000)])
def test_exposure_in_thirds(ev, cell):
    assert apply_recipe(as_shot(), {}, ev)[0].get(5) == cell


@pytest.mark.parametrize("ev", [0.5, 3.33, -4, 0.1])
def test_exposure_off_the_grid_is_refused(ev):
    with pytest.raises(RecipeError):
        apply_recipe(as_shot(), {}, ev)


# -- RAF files -----------------------------------------------------------------------------------
def test_inspect_raf_accepts_an_x100vi_frame(raf):
    info = inspect_raf(raf)
    assert (info.model, info.firmware, info.size) == ("X100VI", "0131", 4096)


def test_inspect_raf_refuses_other_bodies_jpegs_and_missing_files(tmp_path):
    with pytest.raises(ConversionError) as e:
        inspect_raf(make_raf(tmp_path / "xt5.RAF", model="X-T5"))
    assert "X-T5" in str(e.value)
    jpeg = tmp_path / "a.jpg"
    jpeg.write_bytes(b"\xff\xd8\xff\xe1" + bytes(100))
    with pytest.raises(ConversionError) as e:
        inspect_raf(jpeg)
    assert "JPEG" in str(e.value) and "RAW" in e.value.hint
    with pytest.raises(ConversionError):
        inspect_raf(tmp_path / "missing.RAF")


# -- the sequence against the fake camera ----------------------------------------------------------
def test_upload_then_develop_returns_the_cameras_jpeg(camera, fake, raf):
    conv = RawConverter(camera)
    shot = conv.upload(raf.read_bytes())
    assert shot.get(8) == 11
    profile, _ = apply_recipe(shot, NEGATIVE)
    cells = rendered_cells(conv.develop(profile))
    assert cells["cells"][8] == 20 and cells["cells"][6] == 200 and cells["full"] is False
    assert fake.objects == {}            # the output is removed from the camera afterwards


def test_one_upload_serves_many_renders(camera, fake, raf):
    conv = RawConverter(camera)
    shot = conv.upload(raf.read_bytes())
    films = []
    for sim in ("velvia", "acros", "reala_ace"):
        profile, _ = apply_recipe(shot, {"film_simulation": sim})
        films.append(rendered_cells(conv.develop(profile, full=True))["cells"][8])
    assert films == [2, 12, 20] and fake.uploads == 1


def test_a_raf_the_camera_rejects_is_explained(camera, tmp_path):
    foreign = make_raf(tmp_path / "foreign.RAF", model="X-T5").read_bytes()
    with pytest.raises(ConversionError) as e:
        RawConverter(camera).upload(foreign)
    assert "did not accept" in str(e.value)


def test_a_conversion_that_never_finishes_times_out_without_retrying(camera, fake, raf):
    conv = RawConverter(camera)
    shot = conv.upload(raf.read_bytes())
    fake.stall_conversion = True
    with pytest.raises(ConversionError) as e:
        conv.develop(shot, timeout_s=0.3)
    assert "Switch the camera off and on" in e.value.hint
    triggers = [w for w in fake.writes if w[0] == 0xD183]
    assert len(triggers) == 1


def test_only_the_new_object_is_fetched_and_deleted(camera, fake, raf):
    fake.objects[40] = b"\xff\xd8leftover\xff\xd9"   # e.g. left behind by an earlier, interrupted run
    fake.next_handle = 41
    conv = RawConverter(camera)
    shot = conv.upload(raf.read_bytes())
    assert rendered_cells(conv.develop(shot))["cells"][8] == 11
    assert list(fake.objects) == [40]
