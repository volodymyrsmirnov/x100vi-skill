//go:build windows

package transport

// Windows Portable Devices on a machine without a camera (CI): these check the COM plumbing, which
// also checks the class ids, interface ids and vtable slots the transport relies on.

import (
	"bytes"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/volodymyrsmirnov/x100vi-skill/internal/errs"
)

func clearSandbox(t *testing.T) {
	for _, k := range []string{"CODEX_SANDBOX", "CODEX_SANDBOX_NETWORK_DISABLED", "X100VI_SANDBOX"} {
		t.Setenv(k, "")
	}
}

// onCOM runs f on a fresh COM thread.
func onCOM(t *testing.T, f func() error) {
	t.Helper()
	com, err := startCOM()
	if err != nil {
		t.Fatalf("startCOM: %v", err)
	}
	defer com.stop()
	if err := com.do(f); err != nil {
		t.Fatal(err)
	}
}

func TestEnumerationFindsNoFujifilmCamera(t *testing.T) {
	clearSandbox(t)
	tr, err := Open()
	if err == nil {
		tr.Close()
		t.Skip("a Fujifilm camera is connected to this machine")
	}
	if !errs.Is(err, errs.CameraNotFound) || !strings.Contains(err.Error(), "portable devices") {
		t.Fatalf("Open() = %v (%#v), want camera_not_found among portable devices", err, err)
	}
}

func TestCommandValuesCarryEverythingTheTransportSends(t *testing.T) {
	onCOM(t, func() error {
		v, err := newOpCommand(cmdDataToRead, 0x1015, []uint32{0xD16E, 7})
		if err != nil {
			return err
		}
		defer v.release()
		if n, err := v.count(); err != nil || n != 4 {
			t.Errorf("count = %d, %v; want 4", n, err)
		}
		if g, err := v.getGUID(commonKey(cCategory)); err != nil || g != wpdMTPExt {
			t.Errorf("category = %v, %v", g, err)
		}
		if id, err := v.getUint32(commonKey(cID)); err != nil || id != cmdDataToRead {
			t.Errorf("command id = %d, %v", id, err)
		}
		if op, err := v.getUint32(mtpKey(pOperationCode)); err != nil || op != 0x1015 {
			t.Errorf("operation code = %#x, %v", op, err)
		}

		r, err := newCommand(cmdRead)
		if err != nil {
			return err
		}
		defer r.release()
		payload := []byte("\x01\x02\x03PTP")
		for _, err := range []error{
			r.setString(mtpKey(pContext), "ctx-1"),
			r.setUint32(mtpKey(pBytesToRead), 4096),
			r.setUint64(mtpKey(pTotalSize), 250_000_000),
			r.setBuffer(mtpKey(pData), payload),
		} {
			if err != nil {
				return err
			}
		}
		if s, err := r.getString(mtpKey(pContext)); err != nil || s != "ctx-1" {
			t.Errorf("context = %q, %v", s, err)
		}
		if n, err := r.getUint32(mtpKey(pBytesToRead)); err != nil || n != 4096 {
			t.Errorf("bytes to read = %d, %v", n, err)
		}
		if n, err := r.getUint64(mtpKey(pTotalSize)); err != nil || n != 250_000_000 {
			t.Errorf("total size = %d, %v", n, err)
		}
		if b, err := r.getBuffer(mtpKey(pData)); err != nil || !bytes.Equal(b, payload) {
			t.Errorf("buffer = %x, %v", b, err)
		}
		if n, err := r.count(); err != nil || n != 6 {
			t.Errorf("count = %d, %v; want 6", n, err)
		}
		return nil
	})
}

func TestOperationParametersAreStoredAsUI4(t *testing.T) {
	onCOM(t, func() error {
		v, err := newOpCommand(cmdWithoutData, 0x100B, []uint32{0xFFFFFFFF, 2})
		if err != nil {
			return err
		}
		defer v.release()
		c, err := v.getCollection(mtpKey(pOperationParams))
		if err != nil {
			return err
		}
		defer c.release()
		if n, err := c.count(); err != nil || n != 2 {
			t.Errorf("parameter count = %d, %v; want 2", n, err)
		}
		for i, want := range []uint32{0xFFFFFFFF, 2} {
			if got, err := c.uint32At(uint32(i)); err != nil || got != want {
				t.Errorf("parameter %d = %#x, %v; want %#x", i, got, err, want)
			}
		}
		return nil
	})
}

func TestAnErrorValueReadsBack(t *testing.T) {
	onCOM(t, func() error {
		v, err := newValues()
		if err != nil {
			return err
		}
		defer v.release()
		// SetErrorValue is slot 19, next to GetErrorValue (20), which send relies on.
		if err := comCall(v.p, 19, uintptr(unsafe.Pointer(commonKey(cHRESULT))), uintptr(0x80070057)); err != nil {
			return err
		}
		if hr, err := v.getError(commonKey(cHRESULT)); err != nil || uint32(hr) != 0x80070057 {
			t.Errorf("HRESULT = %#x, %v", uint32(hr), err)
		}
		return nil
	})
}

func TestAnUnknownDeviceFailsToOpenWithoutCrashing(t *testing.T) {
	onCOM(t, func() error {
		device, err := coCreate(&clsidPortableDeviceFTM, &iidIPortableDevice)
		if err != nil {
			return err
		}
		defer release(device)
		info, err := newValues()
		if err != nil {
			return err
		}
		defer info.release()
		params, err := newOpCommand(cmdWithoutData, 0x1001, nil)
		if err != nil {
			return err
		}
		defer params.release()
		tr := &wpdTransport{device: device}
		if _, err := tr.send(params, "request 0x1001"); err == nil {
			t.Error("SendCommand on a device that is not open succeeded")
		}
		id, _ := windows.UTF16PtrFromString(`\\?\usb#vid_0000&pid_0000#x100vi-test#{6ac27878-a6fa-4155-ba85-f98f491d4f33}`)
		if err := comCall(device, 3, uintptr(unsafe.Pointer(id)), uintptr(info.p)); err == nil {
			t.Error("Open of a device that does not exist succeeded")
			_ = comCall(device, 8)
		}
		return nil
	})
}
