"""The launcher bundled with the skill: use an installed tool, else install it once."""
import os
import shutil
import stat
import subprocess
from pathlib import Path

import pytest

LAUNCHER = Path(__file__).resolve().parents[1] / "skills" / "x100vi" / "scripts" / "x100vi.sh"
pytestmark = pytest.mark.skipif(shutil.which("sh") is None or os.name == "nt", reason="POSIX shell launcher")


def stub(folder: Path, name: str, body: str) -> Path:
    folder.mkdir(parents=True, exist_ok=True)
    path = folder / name
    path.write_text("#!/bin/sh\n" + body)
    path.chmod(path.stat().st_mode | stat.S_IEXEC)
    return path


def run(*args, path: Path, home: Path, **env):
    full_env = {"PATH": f"{path}:/usr/bin:/bin", "HOME": str(home), **env}
    return subprocess.run(["sh", str(LAUNCHER), *args], capture_output=True, text=True, env=full_env)


def test_uses_the_tool_already_on_path(tmp_path):
    stub(tmp_path / "bin", "x100vi", 'echo "ran: $*"\n')
    stub(tmp_path / "bin", "uv", "echo SHOULD-NOT-RUN >&2; exit 9\n")
    r = run("doctor", "--json", path=tmp_path / "bin", home=tmp_path)
    assert r.returncode == 0 and r.stdout == "ran: doctor --json\n" and r.stderr == ""


def test_passes_arguments_with_spaces_and_the_exit_code_through(tmp_path):
    stub(tmp_path / "bin", "x100vi", 'printf "%s|" "$@"; exit 11\n')
    r = run("preview", "my shot.RAF", "-r", "a b.json", path=tmp_path / "bin", home=tmp_path)
    assert r.returncode == 11 and r.stdout == "preview|my shot.RAF|-r|a b.json|"


def test_installs_with_uv_on_first_use_and_keeps_stdout_clean(tmp_path):
    tools = tmp_path / "uvtools"
    stub(tmp_path / "bin", "uv", f'''
if [ "$1 $2 $3" = "tool dir --bin" ]; then echo "{tools}"; exit 0; fi
echo "uv $*" >> "{tmp_path}/uv.log"
echo "Installed 1 executable: x100vi"
mkdir -p "{tools}"
printf '#!/bin/sh\\necho READY-JSON\\n' > "{tools}/x100vi"
chmod +x "{tools}/x100vi"
''')
    r = run("doctor", "--json", path=tmp_path / "bin", home=tmp_path)
    assert r.returncode == 0, r.stderr
    assert r.stdout == "READY-JSON\n"                          # installer chatter went to stderr
    assert "installing the x100vi tool with uv" in r.stderr and "Installed 1 executable" in r.stderr
    log = (tmp_path / "uv.log").read_text()
    assert log == "uv tool install git+https://github.com/volodymyrsmirnov/x100vi-skill\n"
    r2 = run("--version", path=tmp_path / "bin", home=tmp_path)   # second run: no reinstall
    assert r2.returncode == 0 and (tmp_path / "uv.log").read_text() == log


def test_install_source_can_be_overridden(tmp_path):
    tools = tmp_path / "uvtools"
    stub(tmp_path / "bin", "uv", f'''
if [ "$1 $2 $3" = "tool dir --bin" ]; then echo "{tools}"; exit 0; fi
echo "uv $*" >> "{tmp_path}/uv.log"
mkdir -p "{tools}"; printf '#!/bin/sh\\necho ok\\n' > "{tools}/x100vi"; chmod +x "{tools}/x100vi"
''')
    r = run("--version", path=tmp_path / "bin", home=tmp_path, X100VI_SOURCE="/src/checkout")
    assert r.returncode == 0 and "uv tool install /src/checkout" in (tmp_path / "uv.log").read_text()


def test_falls_back_to_pipx(tmp_path):
    stub(tmp_path / "bin", "pipx", f'''
echo "pipx $*" >> "{tmp_path}/pipx.log"
mkdir -p "{tmp_path}/.local/bin"
printf '#!/bin/sh\\necho from-pipx\\n' > "{tmp_path}/.local/bin/x100vi"; chmod +x "{tmp_path}/.local/bin/x100vi"
''')
    r = run("--version", path=tmp_path / "bin", home=tmp_path)
    assert r.returncode == 0 and r.stdout == "from-pipx\n"
    assert "pipx install git+https://github.com/volodymyrsmirnov/x100vi-skill" in (tmp_path / "pipx.log").read_text()


def test_without_uv_or_pipx_it_explains_what_to_install(tmp_path):
    (tmp_path / "bin").mkdir()
    r = run("doctor", path=tmp_path / "bin", home=tmp_path)
    assert r.returncode == 127 and r.stdout == ""
    assert "neither uv nor pipx" in r.stderr and "hint:" in r.stderr and "docs.astral.sh/uv" in r.stderr


def test_self_update_reinstalls(tmp_path):
    stub(tmp_path / "bin", "x100vi", 'echo "x100vi 9.9.9"\n')
    stub(tmp_path / "bin", "uv", f'echo "uv $*" >> "{tmp_path}/uv.log"\n')
    r = run("--self-update", path=tmp_path / "bin", home=tmp_path)
    assert r.returncode == 0 and r.stdout == "x100vi 9.9.9\n"
    assert "uv tool install --force --reinstall git+https://" in (tmp_path / "uv.log").read_text()


def test_a_failed_install_stops_with_the_installers_error(tmp_path):
    stub(tmp_path / "bin", "uv", 'if [ "$2" = "dir" ]; then exit 1; fi; echo "network unreachable" >&2; exit 2\n')
    r = run("doctor", path=tmp_path / "bin", home=tmp_path)
    assert r.returncode == 2 and "network unreachable" in r.stderr and r.stdout == ""
