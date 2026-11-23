"""The camera's own settings file (`backup --full`, `restore FILE.dat`), against the fake camera."""
from pathlib import Path

import pytest

from fake_camera import FakeX100VI, make_settings_file
from x100vi import settingsfile
from x100vi.errors import RecipeError


def test_header_is_parsed():
    h = settingsfile.parse_header(make_settings_file(body_id="0179ABCDEF0123456789"))
    assert (h.version, h.model, h.body_id) == ("0100", "X100VI", "0179ABCDEF0123456789")


@pytest.mark.parametrize("blob", [b"", b"FUJIFILMX-BACKUP", b'{"slots": {}}' + bytes(100), bytes(4096)])
def test_anything_else_is_not_a_settings_file(blob):
    with pytest.raises(RecipeError, match="not a camera settings file"):
        settingsfile.parse_header(blob)


def test_camera_reads_its_settings_file(camera, fake):
    assert camera.read_settings_file() == fake.settings_file


def test_restore_sends_the_file_the_sdk_way(camera, fake):
    new = make_settings_file(fill=7)
    camera.restore_settings_file(new)
    assert not settingsfile.settings_differ(fake.settings_file, new) and fake.settings_writes == 1


def test_bookkeeping_bytes_are_not_settings():
    a = make_settings_file()
    b = bytearray(a)
    b[0x108] += 2
    b[0x716] = 3
    assert settingsfile.settings_differ(a, bytes(b)) == []
    b[0x800] = 1
    assert settingsfile.settings_differ(a, bytes(b)) == [0x800]
    assert settingsfile.settings_differ(a, a + b"x") != []


def test_restore_refuses_another_model_size_or_body(camera, fake):
    original = fake.settings_file
    with pytest.raises(RecipeError, match="X-T5"):
        camera.restore_settings_file(make_settings_file(model="X-T5"))
    with pytest.raises(RecipeError, match="different size"):
        camera.restore_settings_file(make_settings_file(size=8192))
    other = make_settings_file(body_id="0179OTHERBODY0000001", fill=3)
    with pytest.raises(RecipeError, match="different X100VI body"):
        camera.restore_settings_file(other)
    assert fake.settings_file == original and fake.settings_writes == 0
    camera.restore_settings_file(other, allow_other_body=True)
    assert not settingsfile.settings_differ(fake.settings_file, other)


# -- command line ----------------------------------------------------------------------------------
def test_backup_full_saves_the_file(run, fake, tmp_path):
    target = tmp_path / "cam.dat"
    r = run("backup", "--full", "-o", target, "--json")
    assert r.code == 0 and target.read_bytes() == fake.settings_file
    j = r.json
    assert j["kind"] == "camera_settings_file" and j["bytes"] == len(fake.settings_file) and "private" in j["note"]


def test_backup_full_defaults_to_the_state_folder(run, isolated_state):
    r = run("backup", "--full", "--json")
    saved = r.json["saved"]
    assert r.code == 0 and saved.startswith(str(isolated_state / "backups")) and saved.endswith(".dat")


def test_backup_full_in_the_wrong_mode(run, tmp_path):
    r = run("backup", "--full", "-o", tmp_path / "x.dat", camera=FakeX100VI(mode="card_reader"))
    assert r.code == 11 and not (tmp_path / "x.dat").exists()


def test_restore_full_needs_yes_and_saves_the_current_file_first(run, fake, tmp_path, isolated_state):
    original = fake.settings_file
    new = tmp_path / "new.dat"
    new.write_bytes(make_settings_file(fill=9))
    r = run("restore", new)
    assert r.code == 2 and "every setting" in r.err and fake.settings_writes == 0
    r = run("restore", new, "--yes", "--json")
    assert r.code == 0 and r.json["restored"] and r.json["verified"]
    assert not settingsfile.settings_differ(fake.settings_file, new.read_bytes())
    saved = r.json["previous_saved_to"]
    assert saved.startswith(str(isolated_state)) and Path(saved).read_bytes() == original


def test_restore_full_skips_an_identical_file(run, fake, tmp_path):
    same = tmp_path / "same.dat"
    same.write_bytes(fake.settings_file)
    r = run("restore", same, "--yes", "--json")
    assert r.code == 0 and r.json["unchanged"] and fake.settings_writes == 0


def test_restore_full_rejects_live_and_another_body(run, fake, tmp_path):
    other = tmp_path / "other.dat"
    other.write_bytes(make_settings_file(body_id="0179OTHERBODY0000001"))
    assert run("restore", other, "--yes", "--live").code == 2
    r = run("restore", other, "--yes")
    assert r.code == 2 and "--other-body" in r.err and fake.settings_writes == 0
    assert run("restore", other, "--yes", "--other-body").code == 0 and fake.settings_writes == 1


def test_restore_full_fails_loudly_when_the_camera_keeps_other_settings(run, fake, tmp_path, monkeypatch):
    new = tmp_path / "new.dat"
    new.write_bytes(make_settings_file(fill=9))
    original_send = fake._send_object

    def stubborn(params, data):
        code, out = original_send(params, data)
        fake.settings_file = fake.settings_file[:0x800] + b"\x55" + fake.settings_file[0x801:]
        return code, out

    monkeypatch.setattr(fake, "_send_object", stubborn)
    r = run("restore", new, "--yes")
    assert r.code == 1 and "reads back 1 different bytes" in r.err and "before-restore" in r.err
