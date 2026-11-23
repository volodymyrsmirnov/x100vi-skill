//go:build windows

package transport

// Windows: PTP through Windows Portable Devices, Windows' native camera interface. Windows binds PTP cameras to its own MTP/PTP driver; raw PTP requests go through
// IPortableDevice::SendCommand with the MTP vendor-extension commands, so no extra driver (WinUSB, Zadig)
// is needed and Explorer keeps working. The driver owns the PTP session.
//
// The commands are WPD's documented MTP vendor-extension commands: commands 12-17 and properties 1001-1013 of
// WPD_CATEGORY_MTP_EXT_VENDOR_OPERATIONS (PortableDevice.h).
//
// COM is called through the interfaces' vtables (no cgo). All COM work runs on one OS thread owned by the
// transport, initialised for the multithreaded apartment, so callers may use any goroutine.
//
// Vtable slots, from PortableDeviceApi.idl and PortableDeviceTypes.idl (IUnknown takes 0-2):
//
//	IPortableDeviceManager:               3 GetDevices
//	IPortableDevice:                      3 Open, 4 SendCommand, 5 Content, 6 Capabilities, 7 Cancel, 8 Close
//	IPortableDeviceValues:                3 GetCount, 4 GetAt, 5 SetValue, 6 GetValue, 7 SetStringValue,
//	                                      8 GetStringValue, 9 SetUnsignedIntegerValue,
//	                                      10 GetUnsignedIntegerValue, 11/12 signed, 13 SetUnsignedLargeIntegerValue,
//	                                      14 GetUnsignedLargeIntegerValue, 15/16 signed large, 17/18 float,
//	                                      19 SetErrorValue, 20 GetErrorValue, 21/22 key, 23/24 bool,
//	                                      25/26 IUnknown, 27 SetGuidValue, 28 GetGuidValue, 29 SetBufferValue,
//	                                      30 GetBufferValue, 31/32 IPortableDeviceValues,
//	                                      33 SetIPortableDevicePropVariantCollectionValue,
//	                                      34 GetIPortableDevicePropVariantCollectionValue, ...
//	IPortableDevicePropVariantCollection: 3 GetCount, 4 GetAt, 5 Add, 6 GetType, 7 ChangeType, 8 Clear,
//	                                      9 RemoveAt

import (
	"fmt"
	"regexp"
	"runtime"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/volodymyrsmirnov/x100vi-skill/internal/errs"
	"github.com/volodymyrsmirnov/x100vi-skill/internal/ptp"
	"github.com/volodymyrsmirnov/x100vi-skill/internal/sandbox"
)

var (
	clsidPortableDeviceManager               = mustGUID("{0AF10CEC-2ECD-4B92-9581-34F6AE0637F3}")
	clsidPortableDeviceFTM                   = mustGUID("{F7C0039A-4762-488A-B4B3-760EF9A1BA9B}")
	clsidPortableDeviceValues                = mustGUID("{0C15D503-D017-47CE-9016-7B3F978721CC}")
	clsidPortableDevicePropVariantCollection = mustGUID("{08A99E2F-6D6D-4B80-AF5A-BAF2BCBE4CB9}")
	iidIPortableDeviceManager                = mustGUID("{A1567595-4C2F-4574-A6FA-ECEF917B9A40}")
	iidIPortableDevice                       = mustGUID("{625E2DF8-6392-4CF0-9AD1-3CFA5F17775C}")
	iidIPortableDeviceValues                 = mustGUID("{6848F6F2-3155-4F86-B6F5-263EEEAB3143}")
	iidIPortableDevicePropVariantCollection  = mustGUID("{89B2E422-4F1B-4316-BCEF-A44AFEA83EB3}")

	wpdMTPExt = mustGUID("{4D545058-1A2E-4106-A357-771E0819FC56}") // WPD_CATEGORY_MTP_EXT_VENDOR_OPERATIONS
	wpdCommon = mustGUID("{F0422A9C-5DC8-4440-B5BD-5DF28835658A}") // WPD_CATEGORY_COMMON
	wpdClient = mustGUID("{204D9F0C-2292-4080-9F42-40664E70F859}") // WPD_CLIENT_INFORMATION_PROPERTIES

	procCoCreateInstance = windows.NewLazySystemDLL("ole32.dll").NewProc("CoCreateInstance")
	procPropVariantClear = windows.NewLazySystemDLL("ole32.dll").NewProc("PropVariantClear")

	fujifilmPnPID = regexp.MustCompile(`(?i)vid_0*4cb&`)
)

const (
	cmdWithoutData = 12
	cmdDataToRead  = 13
	cmdDataToWrite = 14
	cmdRead        = 15
	cmdWrite       = 16
	cmdEnd         = 17

	pOperationCode   = 1001
	pOperationParams = 1002
	pResponseCode    = 1003
	pContext         = 1006
	pTotalSize       = 1007
	pBytesToRead     = 1008
	pBytesRead       = 1009
	pBytesToWrite    = 1010
	pBytesWritten    = 1011
	pData            = 1012
	pOptimalBuffer   = 1013

	cCategory = 1001
	cID       = 1002
	cHRESULT  = 1003

	clientQOS    = 8
	clientAccess = 9

	securityImpersonation = 0x00020000
	genericReadWrite      = 0x80000000 | 0x40000000
	vtUI4                 = 19

	clsctxInprocServer  = 1
	coinitMultithreaded = 0
)

// dataInOps are the requests whose answer carries a data phase (all others either send data or have none).
var dataInOps = map[uint16]bool{
	0x1001: true, 0x1004: true, 0x1005: true, 0x1007: true, 0x1008: true,
	0x1009: true, 0x100A: true, 0x1014: true, 0x1015: true, 0x101B: true,
}

func mustGUID(s string) windows.GUID {
	g, err := windows.GUIDFromString(s)
	if err != nil {
		panic(err)
	}
	return g
}

// propertyKey is PROPERTYKEY.
type propertyKey struct {
	fmtid windows.GUID
	pid   uint32
}

func mtpKey(pid uint32) *propertyKey    { return &propertyKey{wpdMTPExt, pid} }
func commonKey(pid uint32) *propertyKey { return &propertyKey{wpdCommon, pid} }

// propVariant is PROPVARIANT: 24 bytes on 64-bit Windows (16 on 32-bit; the tail is spare there).
type propVariant struct {
	vt         uint16
	r1, r2, r3 uint16
	val        [2]uint64
}

// hresult is a failed COM call.
type hresult uint32

func (h hresult) Error() string {
	text := strings.TrimSpace(windows.Errno(h).Error())
	if text == "" || strings.HasPrefix(text, "winapi error") {
		return fmt.Sprintf("HRESULT 0x%08X", uint32(h))
	}
	return fmt.Sprintf("HRESULT 0x%08X: %s", uint32(h), text)
}

// comCall calls vtable slot index of the interface obj.
//
//go:uintptrescapes
func comCall(obj unsafe.Pointer, index int, args ...uintptr) error {
	vtbl := *(*unsafe.Pointer)(obj)
	fn := *(*uintptr)(unsafe.Add(vtbl, uintptr(index)*unsafe.Sizeof(uintptr(0))))
	r, _, _ := syscall.SyscallN(fn, append([]uintptr{uintptr(obj)}, args...)...)
	if int32(r) < 0 {
		return hresult(r)
	}
	return nil
}

func release(obj unsafe.Pointer) {
	if obj != nil {
		_ = comCall(obj, 2)
	}
}

func coCreate(clsid, iid *windows.GUID) (unsafe.Pointer, error) {
	var obj unsafe.Pointer
	r, _, _ := procCoCreateInstance.Call(uintptr(unsafe.Pointer(clsid)), 0, clsctxInprocServer,
		uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&obj)))
	if int32(r) < 0 {
		return nil, hresult(r)
	}
	return obj, nil
}

// comThread owns the OS thread all COM calls run on.
type comThread struct{ calls chan func() }

func startCOM() (*comThread, error) {
	t := &comThread{calls: make(chan func())}
	ready := make(chan error)
	go func() {
		runtime.LockOSThread()                                                                            // never unlocked: the thread ends with the goroutine
		if err := windows.CoInitializeEx(0, coinitMultithreaded); err != nil && err != syscall.Errno(1) { // S_FALSE
			ready <- err
			return
		}
		ready <- nil
		for f := range t.calls {
			f()
		}
		windows.CoUninitialize()
	}()
	if err := <-ready; err != nil {
		return nil, err
	}
	return t, nil
}

// do runs f on the COM thread.
func (t *comThread) do(f func() error) error {
	done := make(chan error, 1)
	t.calls <- func() { done <- f() }
	return <-done
}

func (t *comThread) stop() { close(t.calls) }

// values is an IPortableDeviceValues.
type values struct{ p unsafe.Pointer }

func newValues() (*values, error) {
	p, err := coCreate(&clsidPortableDeviceValues, &iidIPortableDeviceValues)
	if err != nil {
		return nil, err
	}
	return &values{p}, nil
}

func (v *values) release() { release(v.p); v.p = nil }

func (v *values) count() (uint32, error) {
	var n uint32
	err := comCall(v.p, 3, uintptr(unsafe.Pointer(&n)))
	return n, err
}

func (v *values) setString(key *propertyKey, s string) error {
	u, err := windows.UTF16PtrFromString(s)
	if err != nil {
		return err
	}
	return comCall(v.p, 7, uintptr(unsafe.Pointer(key)), uintptr(unsafe.Pointer(u)))
}

func (v *values) getString(key *propertyKey) (string, error) {
	var p *uint16
	if err := comCall(v.p, 8, uintptr(unsafe.Pointer(key)), uintptr(unsafe.Pointer(&p))); err != nil {
		return "", err
	}
	defer windows.CoTaskMemFree(unsafe.Pointer(p))
	return windows.UTF16PtrToString(p), nil
}

func (v *values) setUint32(key *propertyKey, n uint32) error {
	return comCall(v.p, 9, uintptr(unsafe.Pointer(key)), uintptr(n))
}

func (v *values) getUint32(key *propertyKey) (uint32, error) {
	var n uint32
	err := comCall(v.p, 10, uintptr(unsafe.Pointer(key)), uintptr(unsafe.Pointer(&n)))
	return n, err
}

func (v *values) setUint64(key *propertyKey, n uint64) error {
	if unsafe.Sizeof(uintptr(0)) == 8 {
		return comCall(v.p, 13, uintptr(unsafe.Pointer(key)), uintptr(n))
	}
	return comCall(v.p, 13, uintptr(unsafe.Pointer(key)), uintptr(uint32(n)), uintptr(uint32(n>>32)))
}

func (v *values) getUint64(key *propertyKey) (uint64, error) {
	var n uint64
	err := comCall(v.p, 14, uintptr(unsafe.Pointer(key)), uintptr(unsafe.Pointer(&n)))
	return n, err
}

func (v *values) getError(key *propertyKey) (int32, error) {
	var hr int32
	err := comCall(v.p, 20, uintptr(unsafe.Pointer(key)), uintptr(unsafe.Pointer(&hr)))
	return hr, err
}

func (v *values) setGUID(key *propertyKey, g windows.GUID) error {
	return comCall(v.p, 27, uintptr(unsafe.Pointer(key)), uintptr(unsafe.Pointer(&g)))
}

func (v *values) getGUID(key *propertyKey) (windows.GUID, error) {
	var g windows.GUID
	err := comCall(v.p, 28, uintptr(unsafe.Pointer(key)), uintptr(unsafe.Pointer(&g)))
	return g, err
}

func (v *values) setBuffer(key *propertyKey, data []byte) error {
	buf := data
	if len(buf) == 0 {
		buf = []byte{0}
	}
	return comCall(v.p, 29, uintptr(unsafe.Pointer(key)), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(data)))
}

func (v *values) getBuffer(key *propertyKey) ([]byte, error) {
	var p unsafe.Pointer
	var n uint32
	if err := comCall(v.p, 30, uintptr(unsafe.Pointer(key)), uintptr(unsafe.Pointer(&p)), uintptr(unsafe.Pointer(&n))); err != nil {
		return nil, err
	}
	if p == nil {
		return nil, nil
	}
	defer windows.CoTaskMemFree(p)
	return append([]byte(nil), unsafe.Slice((*byte)(p), n)...), nil
}

func (v *values) setCollection(key *propertyKey, c *collection) error {
	return comCall(v.p, 33, uintptr(unsafe.Pointer(key)), uintptr(c.p))
}

func (v *values) getCollection(key *propertyKey) (*collection, error) {
	var p unsafe.Pointer
	if err := comCall(v.p, 34, uintptr(unsafe.Pointer(key)), uintptr(unsafe.Pointer(&p))); err != nil {
		return nil, err
	}
	return &collection{p}, nil
}

// collection is an IPortableDevicePropVariantCollection.
type collection struct{ p unsafe.Pointer }

func newCollection() (*collection, error) {
	p, err := coCreate(&clsidPortableDevicePropVariantCollection, &iidIPortableDevicePropVariantCollection)
	if err != nil {
		return nil, err
	}
	return &collection{p}, nil
}

func (c *collection) release() { release(c.p); c.p = nil }

func (c *collection) count() (uint32, error) {
	var n uint32
	err := comCall(c.p, 3, uintptr(unsafe.Pointer(&n)))
	return n, err
}

// addUint32 appends a VT_UI4 value.
func (c *collection) addUint32(n uint32) error {
	pv := propVariant{vt: vtUI4}
	pv.val[0] = uint64(n)
	return comCall(c.p, 5, uintptr(unsafe.Pointer(&pv)))
}

// uint32At reads a VT_UI4 value.
func (c *collection) uint32At(i uint32) (uint32, error) {
	var pv propVariant
	if err := comCall(c.p, 4, uintptr(i), uintptr(unsafe.Pointer(&pv))); err != nil {
		return 0, err
	}
	defer procPropVariantClear.Call(uintptr(unsafe.Pointer(&pv)))
	if pv.vt != vtUI4 {
		return 0, fmt.Errorf("value %d has type %d, not VT_UI4", i, pv.vt)
	}
	return uint32(pv.val[0]), nil
}

// newCommand builds the parameters of a vendor-extension command.
func newCommand(command uint32) (*values, error) {
	v, err := newValues()
	if err != nil {
		return nil, err
	}
	if err = v.setGUID(commonKey(cCategory), wpdMTPExt); err == nil {
		err = v.setUint32(commonKey(cID), command)
	}
	if err != nil {
		v.release()
		return nil, err
	}
	return v, nil
}

// newOpCommand builds the parameters of a command that starts a PTP request.
func newOpCommand(command uint32, op uint16, params []uint32) (*values, error) {
	v, err := newCommand(command)
	if err != nil {
		return nil, err
	}
	c, err := newCollection()
	if err == nil {
		defer c.release()
		for _, p := range params {
			if err = c.addUint32(p); err != nil {
				break
			}
		}
	}
	if err == nil {
		err = v.setUint32(mtpKey(pOperationCode), uint32(op))
	}
	if err == nil {
		err = v.setCollection(mtpKey(pOperationParams), c)
	}
	if err != nil {
		v.release()
		return nil, err
	}
	return v, nil
}

// findWPDDevice returns the PnP id of the first Fujifilm device, or "".
func findWPDDevice() (string, error) {
	manager, err := coCreate(&clsidPortableDeviceManager, &iidIPortableDeviceManager)
	if err != nil {
		return "", err
	}
	defer release(manager)
	var count uint32
	if err := comCall(manager, 3, 0, uintptr(unsafe.Pointer(&count))); err != nil {
		return "", err
	}
	if count == 0 {
		return "", nil
	}
	ids := make([]*uint16, count)
	if err := comCall(manager, 3, uintptr(unsafe.Pointer(&ids[0])), uintptr(unsafe.Pointer(&count))); err != nil {
		return "", err
	}
	found := ""
	for _, p := range ids[:min(int(count), len(ids))] {
		if p == nil {
			continue
		}
		id := windows.UTF16PtrToString(p)
		windows.CoTaskMemFree(unsafe.Pointer(p))
		if found == "" && fujifilmPnPID.MatchString(id) {
			found = id
		}
	}
	return found, nil
}

type wpdTransport struct {
	com    *comThread
	device unsafe.Pointer
}

// Open connects to the first Fujifilm camera among Windows' portable devices.
func Open() (ptp.Transport, error) {
	com, err := startCOM()
	if err != nil {
		return nil, errs.Wrap(errs.MissingDependency, err, fmt.Sprintf("Windows could not start COM (%v)", err), "")
	}
	t := &wpdTransport{com: com}
	if err := com.do(t.open); err != nil {
		com.stop()
		return nil, err
	}
	return t, nil
}

func (t *wpdTransport) open() error {
	id, err := findWPDDevice()
	if err != nil {
		return errs.Wrap(errs.MissingDependency, err, "Windows Portable Devices is not available to the tool",
			fmt.Sprintf("Listing portable devices failed (%v). Windows Portable Devices is part of desktop "+
				"Windows; check that this installation includes it.", err))
	}
	if id == "" {
		if err := sandbox.Error(); err != nil {
			return err
		}
		return errs.New(errs.CameraNotFound, "no Fujifilm camera found among Windows' portable devices",
			"Connect the camera with a data-capable USB-C cable and switch it on. It should appear in "+
				"Explorer under This PC.")
	}
	info, err := newValues()
	if err == nil {
		defer info.release()
		if err = info.setUint32(&propertyKey{wpdClient, clientQOS}, securityImpersonation); err == nil {
			err = info.setUint32(&propertyKey{wpdClient, clientAccess}, genericReadWrite)
		}
	}
	var device unsafe.Pointer
	if err == nil {
		device, err = coCreate(&clsidPortableDeviceFTM, &iidIPortableDevice)
	}
	if err == nil {
		var u *uint16
		if u, err = windows.UTF16PtrFromString(id); err == nil {
			err = comCall(device, 3, uintptr(unsafe.Pointer(u)), uintptr(info.p))
		}
	}
	if err != nil {
		release(device)
		return errs.Wrap(errs.CameraBusy, err, fmt.Sprintf("Windows could not open the camera (%v)", err),
			"Close other apps that use the camera, then try again.")
	}
	t.device = device
	return nil
}

func notResponding(what string, err error) error {
	e := errs.NotRespondingErr(fmt.Sprintf("Windows reported an error during %s (%v)", what, err))
	return errs.Wrap(e.Kind, err, e.Message, e.Hint)
}

// send runs one command and checks the HRESULT the driver reports for it. The caller releases the results.
func (t *wpdTransport) send(params *values, what string) (*values, error) {
	var p unsafe.Pointer
	if err := comCall(t.device, 4, 0, uintptr(params.p), uintptr(unsafe.Pointer(&p))); err != nil {
		return nil, notResponding(what, err)
	}
	results := &values{p}
	hr, err := results.getError(commonKey(cHRESULT))
	if err != nil {
		results.release()
		return nil, notResponding(what, err)
	}
	if hr < 0 {
		results.release()
		return nil, errs.NotRespondingErr(fmt.Sprintf("Windows reported error 0x%08X during %s", uint32(hr), what))
	}
	return results, nil
}

// sendNew builds a command with build, sends it and releases the parameters.
func (t *wpdTransport) sendNew(what string, build func() (*values, error)) (*values, error) {
	params, err := build()
	if err != nil {
		return nil, notResponding(what, err)
	}
	defer params.release()
	return t.send(params, what)
}

func (t *wpdTransport) Transact(op uint16, params []uint32, dataOut []byte) (uint16, []uint32, []byte, error) {
	if op == ptp.OpOpenSession || op == ptp.OpCloseSession {
		return ptp.RCOK, nil, nil, nil // the Windows driver owns the session
	}
	if t.device == nil {
		return 0, nil, nil, errs.NotRespondingErr("the camera connection is closed")
	}
	what := fmt.Sprintf("request 0x%04X", op)
	var code uint16
	var data []byte
	err := t.com.do(func() error {
		var err error
		switch {
		case dataOut != nil:
			code, err = wpdWrite(t, what, op, params, dataOut)
		case dataInOps[op]:
			code, data, err = wpdRead(t, what, op, params)
		default:
			var results *values
			results, err = t.sendNew(what, func() (*values, error) { return newOpCommand(cmdWithoutData, op, params) })
			if err == nil {
				code, err = t.responseCode(results, what)
				results.release()
			}
		}
		return err
	})
	if err != nil {
		return 0, nil, nil, err
	}
	return code, nil, data, nil
}

func (t *wpdTransport) responseCode(results *values, what string) (uint16, error) {
	code, err := results.getUint32(mtpKey(pResponseCode))
	if err != nil {
		return 0, notResponding(what, err)
	}
	return uint16(code), nil
}

// chunkSize is the transfer size the driver suggests, or 0 for none.
func chunkSize(results *values) uint32 {
	if size, err := results.getUint32(mtpKey(pOptimalBuffer)); err == nil {
		return size
	}
	return 0
}

// begin sends the command that opens a data transfer and returns its results and context. The caller
// releases the results.
func (t *wpdTransport) begin(params *values, what string) (*values, string, error) {
	results, err := t.send(params, what)
	if err != nil {
		return nil, "", err
	}
	ctx, err := results.getString(mtpKey(pContext))
	if err != nil {
		results.release()
		return nil, "", notResponding(what, err)
	}
	return results, ctx, nil
}

// The methods below implement wpdTransfers (wpd_transfer.go), which runs the data-phase loops.
var _ wpdTransfers = (*wpdTransport)(nil)

func (t *wpdTransport) startRead(what string, op uint16, params []uint32) (string, uint32, uint64, error) {
	cmd, err := newOpCommand(cmdDataToRead, op, params)
	if err != nil {
		return "", 0, 0, notResponding(what, err)
	}
	defer cmd.release()
	results, ctx, err := t.begin(cmd, what)
	if err != nil {
		return "", 0, 0, err
	}
	defer results.release()
	total, err := results.getUint64(mtpKey(pTotalSize))
	if err != nil {
		return ctx, 0, 0, notResponding(what, err) // the context is open: the caller ends it
	}
	return ctx, chunkSize(results), total, nil
}

func (t *wpdTransport) readChunk(what, ctx string, want uint32) ([]byte, uint32, error) {
	results, err := t.sendNew(what, func() (*values, error) {
		v, err := newCommand(cmdRead)
		if err != nil {
			return nil, err
		}
		if err = v.setString(mtpKey(pContext), ctx); err == nil {
			if err = v.setUint32(mtpKey(pBytesToRead), want); err == nil {
				err = v.setBuffer(mtpKey(pData), make([]byte, want))
			}
		}
		if err != nil {
			v.release()
			return nil, err
		}
		return v, nil
	})
	if err != nil {
		return nil, 0, err
	}
	defer results.release()
	buf, err := results.getBuffer(mtpKey(pData))
	if err != nil {
		return nil, 0, notResponding(what, err)
	}
	// NUM_BYTES_READ is required in READ_DATA results; without it the data length is unknown, since the
	// buffer may keep the requested size. The caller ends the transfer.
	n, err := results.getUint32(mtpKey(pBytesRead))
	if err != nil {
		return nil, 0, errNoByteCount(what, err)
	}
	return buf, n, nil
}

func (t *wpdTransport) startWrite(what string, op uint16, params []uint32, total uint64) (string, uint32, error) {
	cmd, err := newOpCommand(cmdDataToWrite, op, params)
	if err == nil {
		if err = cmd.setUint64(mtpKey(pTotalSize), total); err != nil {
			cmd.release()
		}
	}
	if err != nil {
		return "", 0, notResponding(what, err)
	}
	defer cmd.release()
	results, ctx, err := t.begin(cmd, what)
	if err != nil {
		return "", 0, err
	}
	defer results.release()
	return ctx, chunkSize(results), nil
}

func (t *wpdTransport) writeChunk(what, ctx string, piece []byte) (uint32, error) {
	results, err := t.sendNew(what, func() (*values, error) {
		v, err := newCommand(cmdWrite)
		if err != nil {
			return nil, err
		}
		if err = v.setString(mtpKey(pContext), ctx); err == nil {
			if err = v.setUint32(mtpKey(pBytesToWrite), uint32(len(piece))); err == nil {
				err = v.setBuffer(mtpKey(pData), piece)
			}
		}
		if err != nil {
			v.release()
			return nil, err
		}
		return v, nil
	})
	if err != nil {
		return 0, err
	}
	defer results.release()
	written, err := results.getUint32(mtpKey(pBytesWritten))
	if err != nil {
		return 0, notResponding(what, err)
	}
	return written, nil
}

func (t *wpdTransport) endTransfer(what, ctx string) (uint16, error) {
	results, err := t.sendNew(what, func() (*values, error) {
		v, err := newCommand(cmdEnd)
		if err != nil {
			return nil, err
		}
		if err = v.setString(mtpKey(pContext), ctx); err != nil {
			v.release()
			return nil, err
		}
		return v, nil
	})
	if err != nil {
		return 0, err
	}
	defer results.release()
	return t.responseCode(results, what)
}

// Close closes the device; errors are ignored, since the device may already be gone.
func (t *wpdTransport) Close() error {
	if t.com == nil {
		return nil
	}
	_ = t.com.do(func() error {
		if t.device != nil {
			_ = comCall(t.device, 8)
			release(t.device)
			t.device = nil
		}
		return nil
	})
	t.com.stop()
	t.com = nil
	return nil
}
