"""Agent sandboxes hide USB: the tool must say so, and help the user allow it."""
import json
import os
import shutil
import subprocess
from pathlib import Path

import pytest

from x100vi import cli, sandbox
from x100vi.errors import CameraNotFoundError, SandboxedError
from x100vi.ptp import UsbTransport, detect_sandbox

LAUNCHER = Path(__file__).resolve().parents[1] / "skills" / "x100vi" / "scripts" / "x100vi.sh"


@pytest.mark.parametrize("env, name", [
    ({"CODEX_SANDBOX": "seatbelt"}, "Codex"),
    ({"CODEX_SANDBOX_NETWORK_DISABLED": "1"}, "Codex"),
    ({"X100VI_SANDBOX": "Example"}, "Example"),
    ({}, None),
    ({"CODEX_SANDBOX_NETWORK_DISABLED": "0"}, None),
])
def test_detect_sandbox(env, name):
    assert detect_sandbox(env) == name


def no_usb(monkeypatch, devices=()):
    def find(find_all=False, **kwargs):
        return iter(devices) if find_all else None

    monkeypatch.setattr("usb.core.find", find)


def test_inside_a_sandbox_no_camera_is_reported_as_the_sandbox_not_the_camera(monkeypatch):
    no_usb(monkeypatch)
    monkeypatch.setenv("CODEX_SANDBOX", "seatbelt")
    with pytest.raises(SandboxedError) as e:
        UsbTransport()
    assert "Codex sandbox" in str(e.value) and "not a camera or cable problem" in e.value.hint
    assert e.value.exit_code == 14 and e.value.kind == "sandboxed"


def test_outside_a_sandbox_it_is_still_camera_not_found(monkeypatch):
    for var in ("CODEX_SANDBOX", "CODEX_SANDBOX_NETWORK_DISABLED", "X100VI_SANDBOX"):
        monkeypatch.delenv(var, raising=False)
    no_usb(monkeypatch, devices=[object()])            # other USB devices exist, just no camera
    with pytest.raises(CameraNotFoundError) as e:
        UsbTransport()
    assert "sandbox" not in e.value.hint


def test_when_no_usb_device_at_all_is_visible_the_hint_mentions_sandboxes(monkeypatch):
    for var in ("CODEX_SANDBOX", "CODEX_SANDBOX_NETWORK_DISABLED", "X100VI_SANDBOX"):
        monkeypatch.delenv(var, raising=False)
    no_usb(monkeypatch)
    with pytest.raises(CameraNotFoundError) as e:
        UsbTransport()
    assert "No USB device of any kind is visible" in e.value.hint and "outside the sandbox" in e.value.hint


def test_doctor_reports_sandboxed_as_json(monkeypatch, capsys):
    no_usb(monkeypatch)
    monkeypatch.setenv("CODEX_SANDBOX", "seatbelt")
    assert cli.main(["doctor", "--json"]) == 14
    out = json.loads(capsys.readouterr().out)
    assert out["ready"] is False and out["error"] == "sandboxed"


def test_codex_rules_allow_reads_and_prompt_for_writes():
    rules = sandbox.codex_rules(tools=["x100vi"], launchers=["/home/me/.agents/skills/x100vi/scripts/x100vi.sh"])
    assert rules.count("prefix_rule(") == 4
    assert 'pattern = [["x100vi"], ["doctor", "get", "backup", "preview"' in rules
    assert '[["sh", "bash"], ["/home/me/.agents/skills/x100vi/scripts/x100vi.sh"], ["set", "restore"' in rules
    allow, prompt = rules.split('decision = "allow"'), rules.split('decision = "prompt"')
    assert len(allow) == 3 and len(prompt) == 3
    for block in rules.split("prefix_rule(")[1:]:
        writes = '"set"' in block
        assert ('decision = "prompt"' in block) == writes and ('decision = "allow"' in block) != writes
    assert '"install-skill"' not in rules               # never pre-approved


def test_codex_rules_without_an_installed_skill_and_with_awkward_paths():
    rules = sandbox.codex_rules(tools=["x100vi", 'C:\\Tools\\x "y"\\x100vi.exe'], launchers=[])
    assert rules.count("prefix_rule(") == 2
    assert '"C:\\\\Tools\\\\x \\"y\\"\\\\x100vi.exe"' in rules


def test_launcher_paths_finds_installed_copies(tmp_path, monkeypatch):
    monkeypatch.setenv("CODEX_HOME", str(tmp_path / "no-codex"))
    script = tmp_path / ".agents" / "skills" / "x100vi" / "scripts" / "x100vi.sh"
    script.parent.mkdir(parents=True)
    script.write_text("#!/bin/sh\n")
    assert sandbox.launcher_paths(tmp_path)[0] == str(script)
    assert sandbox.launcher_paths(tmp_path / "nobody") == []


def test_launcher_paths_follow_a_custom_codex_home(tmp_path, monkeypatch):
    custom = tmp_path / "elsewhere" / "codex"
    script = custom / "skills" / "x100vi" / "scripts" / "x100vi.sh"
    script.parent.mkdir(parents=True)
    script.write_text("#!/bin/sh\n")
    monkeypatch.setenv("CODEX_HOME", str(custom))
    assert sandbox.launcher_paths(tmp_path / "home") == [str(script)]
    monkeypatch.delenv("CODEX_HOME")
    assert sandbox.launcher_paths(tmp_path / "home") == []


def test_rules_cover_the_launcher_that_install_skill_put_under_a_custom_codex_home(run, tmp_path, monkeypatch):
    monkeypatch.setenv("HOME", str(tmp_path / "home"))
    monkeypatch.setenv("USERPROFILE", str(tmp_path / "home"))
    monkeypatch.setenv("CODEX_HOME", str(tmp_path / "custom-codex"))
    assert run("install-skill", "--codex").code == 0
    launcher = tmp_path / "custom-codex" / "skills" / "x100vi" / "scripts" / "x100vi.sh"
    assert launcher.is_file()
    r = run("sandbox-help", "--write-codex-rules", "--json")
    rules = (tmp_path / "custom-codex" / "rules" / "x100vi.rules").read_text()
    assert r.json["codex"]["written"] and f'"{launcher}"' in rules
    assert f"sh {launcher} *" in r.json["claude_code"]["settings"]["sandbox"]["excludedCommands"]


def test_claude_settings_fragment():
    settings = sandbox.claude_settings(tools=["x100vi"], launchers=["/home/me/.claude/skills/x100vi/scripts/x100vi.sh"])
    assert settings == {"sandbox": {"excludedCommands": [
        "x100vi *", "sh /home/me/.claude/skills/x100vi/scripts/x100vi.sh *"]}}


def test_sandbox_help_prints_and_only_writes_when_asked(run, tmp_path, monkeypatch):
    monkeypatch.setenv("CODEX_HOME", str(tmp_path / "codex"))
    r = run("sandbox-help", "--json")
    target = tmp_path / "codex" / "rules" / "x100vi.rules"
    assert r.code == 0 and r.json["codex"]["rules_file"] == str(target) and r.json["codex"]["written"] is None
    assert not target.exists()
    assert "excludedCommands" in r.json["claude_code"]["settings"]["sandbox"]
    text = run("sandbox-help").out
    assert "prefix_rule(" in text and "excludedCommands" in text and "approve" in text
    r = run("sandbox-help", "--write-codex-rules", "--json")
    assert r.json["codex"]["written"] == str(target) and target.read_text().count("prefix_rule(") >= 2


@pytest.mark.skipif(shutil.which("codex") is None, reason="needs the Codex CLI to check the rules with its own parser")
def test_generated_rules_are_accepted_by_codex(tmp_path):
    launcher = "/home/me/.agents/skills/x100vi/scripts/x100vi.sh"
    rules = tmp_path / "x100vi.rules"
    rules.write_text(sandbox.codex_rules(tools=["x100vi"], launchers=[launcher]))

    def decision(*argv):
        out = subprocess.run(["codex", "execpolicy", "check", "--rules", str(rules), "--", *argv],
                             capture_output=True, text=True, check=True).stdout
        return json.loads(out).get("decision")

    assert decision("x100vi", "doctor", "--json") == "allow"
    assert decision("sh", launcher, "preview", "a.RAF", "-r", "b.json") == "allow"
    assert decision("sh", launcher, "set", "C1", "a.json") == "prompt"
    assert decision("x100vi", "restore", "a.json", "--yes") == "prompt"
    assert decision("sh", "/tmp/other.sh", "doctor") is None
    assert decision("x100vi", "install-skill", "--claude") is None


@pytest.mark.skipif(shutil.which("sh") is None or os.name == "nt", reason="POSIX shell launcher")
def test_launcher_explains_that_it_cannot_install_inside_a_sandbox(tmp_path):
    (tmp_path / "bin").mkdir()
    env = {"PATH": f"{tmp_path / 'bin'}:/usr/bin:/bin", "HOME": str(tmp_path), "CODEX_SANDBOX": "seatbelt"}
    r = subprocess.run(["sh", str(LAUNCHER), "doctor", "--json"], capture_output=True, text=True, env=env)
    assert r.returncode == 14 and r.stdout == ""
    assert "cannot be installed from inside the sandbox" in r.stderr and "outside the sandbox" in r.stderr
