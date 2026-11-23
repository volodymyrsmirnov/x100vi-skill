"""The command line end to end, against the fake camera."""
import json
from pathlib import Path

import pytest

from fake_camera import FakeX100VI, make_raf, rendered_cells
from x100vi import cli, library
from x100vi import model as m

GOLD = {"name": "GOLD", "film_simulation": "classic_chrome", "dynamic_range": "400", "clarity": 2,
        "white_balance": {"mode": "daylight", "shift_r": 3, "shift_b": -5}}


# -- doctor ----------------------------------------------------------------------------------------
def test_doctor_ready(run):
    r = run("doctor")
    assert r.code == 0 and "ready" in r.out and "about 80%" in r.out
    assert run("doctor", "--json").json["ready"] is True


def test_doctor_wrong_mode_says_how_to_fix_it(run):
    r = run("doctor", camera=FakeX100VI(mode="card_reader"))
    assert r.code == 11 and "USB CARD READER" in r.out and "CONNECTION MODE" in r.out
    j = run("doctor", "--json", camera=FakeX100VI(mode="card_reader")).json
    assert j["ready"] is False and j["error"] == "wrong_usb_mode" and "hint" in j


def test_doctor_wrong_model(run):
    r = run("doctor", camera=FakeX100VI(model="X-T5"))
    assert r.code == 12 and "X-T5" in r.out


def test_doctor_no_camera(capsys):
    from x100vi.errors import CameraNotFoundError

    def nothing():
        raise CameraNotFoundError("no Fujifilm camera found on USB", "Connect the camera.")

    assert cli.main(["doctor"], transport_factory=nothing) == 10
    assert "not ready: no Fujifilm camera found" in capsys.readouterr().out


# -- get / set -------------------------------------------------------------------------------------
def test_get_prints_json_and_saves_files(run, tmp_path):
    assert run("get", "c2").json["film_simulation"] == "provia"
    assert run("get").json["image_quality"] == "raw"
    target = tmp_path / "out" / "c2.json"
    assert run("get", "C2", "-o", target).code == 0
    assert json.loads(target.read_text())["name"] == ""
    assert set(run("get", "all").json["slots"]) == set("1234567")


def test_set_writes_reads_back_and_advises(run, fake, recipe_file):
    r = run("set", "c3", recipe_file(GOLD), "--json")
    assert r.code == 0
    j = r.json
    assert j["target"] == "C3" and j["stored"]["film_simulation"] == "classic_chrome" and j["stored"]["name"] == "GOLD"
    assert any("ISO 500" in a for a in j["advice"]) and any("Clarity" in a for a in j["advice"])
    assert "ISO" in j["set_on_camera"]
    assert fake.slot_value(3, "film_simulation") == "classic_chrome"


def test_set_refuses_to_overwrite_an_occupied_register(run, fake, recipe_file):
    run("set", "c3", recipe_file(GOLD))
    r = run("set", "c3", recipe_file({"name": "OTHER", "film_simulation": "velvia"}))
    assert r.code == 4 and "already holds 'GOLD'" in r.err and "--overwrite" in r.err
    assert fake.names[3] == "GOLD"


def test_overwrite_saves_the_previous_recipe_first(run, fake, recipe_file, isolated_state):
    run("set", "c3", recipe_file(GOLD))
    r = run("set", "c3", recipe_file({"name": "OTHER", "film_simulation": "velvia"}), "--overwrite", "--json")
    assert r.code == 0 and fake.names[3] == "OTHER"
    saved = json.loads(Path(r.json["previous_saved_to"]).read_text())
    assert saved["name"] == "GOLD" and str(isolated_state) in r.json["previous_saved_to"]


def test_set_live_backs_up_the_previous_live_settings(run, recipe_file):
    j = run("set", "live", recipe_file({"film_simulation": "acros"}), "--json").json
    assert j["target"] == "live" and j["previous_saved_to"]


def test_set_accepts_a_starter_recipe_by_name(run, fake):
    assert run("set", "c1", "desert-gold").code == 0
    assert fake.names[1] == "DESERT GOLD"


def test_dry_run_never_connects(recipe_file, capsys):
    def boom():
        raise AssertionError("must not connect")

    assert cli.main(["set", "c1", str(recipe_file(GOLD)), "--dry-run"], transport_factory=boom) == 0
    assert "would write" in capsys.readouterr().out


@pytest.mark.parametrize("recipe, fragment", [
    ({"film_simulation": "kodachrome"}, "film_simulation"),
    ({"iso": 800}, "unknown setting"),
])
def test_invalid_recipe_fails_before_connecting(recipe, fragment, recipe_file, capsys):
    def boom():
        raise AssertionError("must not connect")

    assert cli.main(["set", "c1", str(recipe_file(recipe))], transport_factory=boom) == 2
    err = capsys.readouterr().err
    assert fragment in err and "hint:" in err


def test_broken_json_and_missing_files_are_explained(run, tmp_path):
    bad = tmp_path / "bad.json"
    bad.write_text("{not json")
    assert "not valid JSON" in run("set", "c1", bad).err
    assert "does not exist" in run("set", "c1", tmp_path / "nope.json").err


def test_wrong_mode_is_an_error_with_a_hint(run, recipe_file):
    r = run("set", "c1", recipe_file(GOLD), camera=FakeX100VI(mode="card_reader"))
    assert r.code == 11 and "hint:" in r.err and "USB RAW CONV./BACKUP RESTORE" in r.err


def test_json_errors_are_machine_readable(run, recipe_file):
    r = run("set", "c9", recipe_file(GOLD), "--json")
    assert r.code == 2 and r.json["error"] == "invalid_recipe"


# -- backup / restore ------------------------------------------------------------------------------
def test_backup_then_restore_round_trips(run, fake, recipe_file, tmp_path):
    run("set", "c3", recipe_file(GOLD))
    backup = tmp_path / "all.json"
    assert run("backup", "-o", backup).code == 0
    run("set", "c3", recipe_file({"name": "OTHER", "film_simulation": "velvia"}), "--overwrite")
    run("set", "c5", "street-mono")
    assert "Add --yes" in run("restore", backup).err
    r = run("restore", backup, "--yes", "--json")
    assert r.code == 0 and r.json["restored"] == [f"C{n}" for n in m.SLOTS]
    assert fake.names[3] == "GOLD" and fake.names[5] == ""
    assert fake.slot_value(5, "film_simulation") == "provia"
    assert fake.live["film_simulation"] == 1          # live untouched without --live


def test_restore_rejects_a_single_recipe_and_a_broken_backup(run, recipe_file, tmp_path):
    assert "not a backup" in run("restore", recipe_file(GOLD), "--yes").err
    broken = tmp_path / "broken.json"
    broken.write_text(json.dumps({"slots": {"1": {"film_simulation": "nope"}}}))
    r = run("restore", broken, "--yes")
    assert r.code == 2 and "film_simulation" in r.err


def test_backup_without_a_path_goes_to_the_state_folder(run, isolated_state):
    j = run("backup", "--json").json
    assert str(isolated_state) in j["saved"] and j["slots"]["C1"] == "(empty)"


# -- validate / recipes / schema -------------------------------------------------------------------
def test_validate_reports_each_file(run, recipe_file):
    good, bad = recipe_file(GOLD, "good.json"), recipe_file({"highlight": 9}, "bad.json")
    r = run("validate", good, bad)
    assert r.code == 2 and "good.json: ok" in r.out and "bad.json: INVALID" in r.out
    assert run("validate", good).code == 0


def test_starter_recipes_are_listed_and_printable(run):
    names = [i["name"] for i in run("recipes", "--json").json["recipes"]]
    assert "desert-gold" in names and len(names) >= 8
    assert run("recipes", "desert-gold").json["film_simulation"] == "classic_chrome"
    assert run("recipes", "nope").code == 2


def test_schema_is_printed(run):
    assert run("schema").json["title"].startswith("Fujifilm X100VI")


# -- preview ---------------------------------------------------------------------------------------
def test_preview_renders_every_recipe_and_reports_what_was_not_applied(run, raf, recipe_file, tmp_path):
    out = tmp_path / "previews"
    r = run("preview", raf, "--as-shot", "-r", recipe_file(GOLD, "gold.json"), "street-mono", "-o", out, "--json")
    assert r.code == 0
    j = r.json
    assert [x["recipe"] for x in j["renders"]] == ["as-shot", "gold", "street-mono"]
    gold = j["renders"][1]
    assert gold["not_applied"][0]["setting"] == "dynamic_range" and gold["not_applied"][0]["applied"] == "200"
    assert j["renders"][2]["not_applied"] == []
    assert rendered_cells(out / "DSCF0001__gold.jpg")["cells"][8] == 11
    assert rendered_cells(out / "DSCF0001__street-mono.jpg")["cells"][8] == 14
    assert rendered_cells(out / "DSCF0001__as-shot.jpg")["cells"][7] == 1
    assert j["frames"][0]["as_shot"]["film_simulation"] == "classic_chrome"
    assert (out / "contact-sheet.jpg").is_file() and j["contact_sheet"].endswith("contact-sheet.jpg")


def test_preview_text_output_explains_the_cap_in_words(run, raf, recipe_file, tmp_path):
    r = run("preview", raf, "-r", recipe_file(GOLD), "-o", tmp_path / "p", "--no-sheet")
    assert "not applied as written" in r.out and "shot at DR200" in r.out
    assert not (tmp_path / "p" / "contact-sheet.jpg").exists()


def test_preview_uploads_each_raf_once(run, fake, tmp_path, recipe_file):
    a, b = make_raf(tmp_path / "A.RAF"), make_raf(tmp_path / "B.RAF")
    assert run("preview", a, b, "-r", "everyday", "desert-gold", "-o", tmp_path / "p").code == 0
    assert fake.uploads == 2 and len(list((tmp_path / "p").glob("*__*.jpg"))) == 4


def test_preview_exposure_and_full_resolution(run, raf, tmp_path):
    run("preview", raf, "-r", "everyday", "--ev", "0.67", "--full", "-o", tmp_path / "p", "--no-sheet")
    cells = rendered_cells(tmp_path / "p" / "DSCF0001__everyday.jpg")
    assert cells["cells"][5] == 667 and cells["full"] is True


def test_preview_checks_files_and_recipes_before_connecting(tmp_path, recipe_file, capsys):
    def boom():
        raise AssertionError("must not connect")

    foreign = make_raf(tmp_path / "xt5.RAF", model="X-T5")
    assert cli.main(["preview", str(foreign), "-r", "everyday"], transport_factory=boom) == 20
    assert "X-T5" in capsys.readouterr().err
    good = make_raf(tmp_path / "ok.RAF")
    assert cli.main(["preview", str(good)], transport_factory=boom) == 2
    assert "nothing to render" in capsys.readouterr().err
    auto = recipe_file({"d_range_priority": "auto"})
    assert cli.main(["preview", str(good), "-r", str(auto)], transport_factory=boom) == 2
    assert "cannot be previewed" in capsys.readouterr().err


def test_preview_in_the_wrong_mode(run, raf, tmp_path):
    r = run("preview", raf, "-r", "everyday", "-o", tmp_path / "p", camera=FakeX100VI(mode="card_reader"))
    assert r.code == 11 and "CONNECTION MODE" in r.err


def test_preview_verify_needs_exiftool(run, raf, tmp_path, monkeypatch):
    monkeypatch.setattr("shutil.which", lambda name: None)
    r = run("preview", raf, "-r", "everyday", "--verify", "-o", tmp_path / "p")
    assert r.code == 5 and "exiftool is not installed" in r.err


# -- install-skill ---------------------------------------------------------------------------------
def test_install_skill_copies_into_a_skills_folder(run, tmp_path):
    r = run("install-skill", "--dir", tmp_path / "skills", "--json")
    assert r.code == 0
    skill = tmp_path / "skills" / "x100vi" / "SKILL.md"
    assert skill.is_file() and "name: x100vi" in skill.read_text()
    assert run("install-skill", "--dir", tmp_path / "skills").code == 0        # reinstalling replaces our own copy


def test_install_skill_does_not_clobber_a_foreign_folder(run, tmp_path):
    other = tmp_path / "skills" / "x100vi"
    other.mkdir(parents=True)
    (other / "notes.txt").write_text("mine")
    r = run("install-skill", "--dir", tmp_path / "skills")
    assert r.code == 1 and "not this skill" in r.err and (other / "notes.txt").exists()


def test_install_skill_needs_a_destination(run):
    assert "say where" in run("install-skill").err


def test_home_targets(run, tmp_path, monkeypatch):
    monkeypatch.setenv("HOME", str(tmp_path))
    monkeypatch.setenv("USERPROFILE", str(tmp_path))
    monkeypatch.delenv("CODEX_HOME", raising=False)
    assert run("install-skill", "--claude", "--codex").code == 0
    assert (tmp_path / ".claude" / "skills" / "x100vi" / "SKILL.md").is_file()
    assert (tmp_path / ".codex" / "skills" / "x100vi" / "SKILL.md").is_file()


def test_recipe_advice_is_specific():
    assert cli.recipe_advice({"dynamic_range": "200"})[0].startswith("DR200 needs ISO 250")
    assert any("custom white balance" in a for a in cli.recipe_advice({"white_balance": {"mode": "custom_1"}}))
    assert any("ignores highlight" in a for a in cli.recipe_advice({"d_range_priority": "weak", "highlight": 1}))
    assert cli.recipe_advice({"film_simulation": "velvia"}) == []


def test_summary_is_readable():
    assert library.summarize(GOLD) == "classic_chrome, WB daylight R+3 B-5, DR400, clarity +2"


def test_preview_formats(run, raf, tmp_path):
    r = run("preview", raf, "-r", "everyday", "desert-gold", "--format", "heif", "-o", tmp_path / "h", "--json")
    assert r.code == 0 and r.json["format"] == "heif" and r.json["contact_sheet"] is None
    names = sorted(p.name for p in (tmp_path / "h").iterdir())
    assert names == ["DSCF0001__desert-gold.heif", "DSCF0001__everyday.heif"]
    r = run("preview", raf, "-r", "everyday", "--format", "tiff16", "--full", "-o", tmp_path / "t")
    assert r.code == 0 and rendered_cells(tmp_path / "t" / "DSCF0001__everyday.tif")["cells"][2] == 11


def test_preview_tiff_without_full_fails_before_connecting(raf, capsys):
    def boom():
        raise AssertionError("must not connect")

    assert cli.main(["preview", str(raf), "-r", "everyday", "--format", "tiff"], transport_factory=boom) == 2
    assert "--full" in capsys.readouterr().err
