from __future__ import annotations

import json
import os
from dataclasses import dataclass

import pytest

from fake_camera import FakeX100VI, make_raf
from x100vi import cli
from x100vi.camera import open_camera

HARDWARE = os.environ.get("X100VI_HARDWARE") == "1"


def pytest_collection_modifyitems(config, items):
    skip = pytest.mark.skip(reason="needs a real X100VI: set X100VI_HARDWARE=1")
    for item in items:
        if "camera" in item.keywords and not HARDWARE:
            item.add_marker(skip)


@pytest.fixture(autouse=True)
def isolated_state(tmp_path, monkeypatch):
    """Automatic backups go to a temp folder, never to the developer's real state directory."""
    monkeypatch.setenv("X100VI_HOME", str(tmp_path / "state"))
    return tmp_path / "state"


@pytest.fixture
def fake():
    return FakeX100VI()


@pytest.fixture
def camera(fake):
    with open_camera(fake) as cam:
        yield cam


@pytest.fixture
def raf(tmp_path):
    return make_raf(tmp_path / "DSCF0001.RAF")


@dataclass
class Result:
    code: int
    out: str
    err: str

    @property
    def json(self) -> dict:
        return json.loads(self.out)


@pytest.fixture
def run(fake, capsys):
    """Run the CLI against the fake camera: run('set', 'C1', path) -> Result."""

    def _run(*argv, camera=None) -> Result:
        target = camera if camera is not None else fake
        code = cli.main([str(a) for a in argv], transport_factory=lambda: target)
        captured = capsys.readouterr()
        return Result(code, captured.out, captured.err)

    return _run


@pytest.fixture
def recipe_file(tmp_path):
    def _write(recipe: dict, name: str = "recipe.json"):
        path = tmp_path / name
        path.write_text(json.dumps(recipe))
        return path

    return _write
