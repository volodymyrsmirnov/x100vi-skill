"""Windows Portable Devices: runs on Windows only (CI has no camera, so it checks the COM plumbing)."""
import sys

import pytest

pytestmark = pytest.mark.skipif(sys.platform != "win32", reason="Windows Portable Devices exists only on Windows")


@pytest.fixture
def wpd():
    from x100vi.wpd import WpdTransport, _com

    transport = WpdTransport.__new__(WpdTransport)
    transport._comtypes, transport._api, transport._types = _com()
    transport._comtypes.CoInitialize()
    transport._create = transport._comtypes.client.CreateObject
    transport._inproc = transport._comtypes.CLSCTX_INPROC_SERVER
    return transport


def test_the_wpd_type_libraries_load():
    from x100vi.wpd import _com

    _comtypes, api, types = _com()
    assert hasattr(api, "IPortableDevice") and hasattr(types, "IPortableDeviceValues")


def test_enumeration_runs_and_finds_no_fujifilm_camera_on_a_build_machine():
    from x100vi.errors import CameraNotFoundError
    from x100vi.wpd import WpdTransport

    with pytest.raises(CameraNotFoundError, match="portable devices"):
        WpdTransport()


def test_command_values_carry_everything_the_transport_sends(wpd):
    from x100vi.wpd import (
        C_ID,
        CMD_DATA_TO_READ,
        CMD_READ,
        COMMON,
        MTP_EXT,
        P_BYTES_TO_READ,
        P_CONTEXT,
        P_DATA,
        P_OPERATION_CODE,
        P_TOTAL_SIZE,
    )

    values = wpd._command(CMD_DATA_TO_READ, 0x1015, (0xD16E, 7))
    assert values.GetCount() == 4
    assert values.GetUnsignedIntegerValue(wpd._key(COMMON, C_ID)) == CMD_DATA_TO_READ
    assert values.GetUnsignedIntegerValue(wpd._key(MTP_EXT, P_OPERATION_CODE)) == 0x1015

    values = wpd._command(CMD_READ)
    values.SetStringValue(wpd._key(MTP_EXT, P_CONTEXT), "ctx-1")
    values.SetUnsignedIntegerValue(wpd._key(MTP_EXT, P_BYTES_TO_READ), 4096)
    values.SetUnsignedLargeIntegerValue(wpd._key(MTP_EXT, P_TOTAL_SIZE), 250_000_000)
    wpd._set_buffer(values, P_DATA, b"\x01\x02\x03PTP")
    assert values.GetStringValue(wpd._key(MTP_EXT, P_CONTEXT)) == "ctx-1"
    assert values.GetUnsignedLargeIntegerValue(wpd._key(MTP_EXT, P_TOTAL_SIZE)) == 250_000_000
    assert wpd._get_buffer(values, P_DATA) == b"\x01\x02\x03PTP"


def test_operation_parameters_are_stored_as_ui4(wpd):
    from x100vi.wpd import CMD_WITHOUT_DATA, MTP_EXT, P_OPERATION_PARAMS

    values = wpd._command(CMD_WITHOUT_DATA, 0x100B, (0xFFFFFFFF, 2))
    collection = values.GetIPortableDevicePropVariantCollectionValue(wpd._key(MTP_EXT, P_OPERATION_PARAMS))
    assert collection.GetCount() == 2
