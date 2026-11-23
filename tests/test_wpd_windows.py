"""Windows Portable Devices: runs on Windows only (CI has no camera, so it checks the COM plumbing)."""
import sys

import pytest

pytestmark = pytest.mark.skipif(sys.platform != "win32", reason="Windows Portable Devices exists only on Windows")


def test_the_wpd_type_libraries_load():
    from x100vi.wpd import _com

    _comtypes, api, types = _com()
    assert hasattr(api, "IPortableDevice") and hasattr(types, "IPortableDeviceValues")


def test_enumeration_runs_and_finds_no_fujifilm_camera_on_a_build_machine():
    from x100vi.errors import CameraNotFoundError
    from x100vi.wpd import WpdTransport

    with pytest.raises(CameraNotFoundError, match="portable devices"):
        WpdTransport()


def test_command_values_can_be_built():
    from x100vi.wpd import CMD_DATA_TO_READ, WpdTransport, _com

    transport = WpdTransport.__new__(WpdTransport)
    transport._comtypes, transport._api, transport._types = _com()
    transport._create = transport._comtypes.client.CreateObject
    transport._inproc = transport._comtypes.CLSCTX_INPROC_SERVER
    values = transport._command(CMD_DATA_TO_READ, 0x1015, (0xD16E,))
    assert values.GetCount() == 4
