//go:build linux

package transport

// Linux: the camera's still-image interface claimed through usbfs, with no libusb and no cgo.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/volodymyrsmirnov/x100vi-skill/internal/errs"
	"github.com/volodymyrsmirnov/x100vi-skill/internal/ptp"
	"github.com/volodymyrsmirnov/x100vi-skill/internal/sandbox"
)

const usbTimeoutMS = 60000

// Open connects to the first Fujifilm camera on USB.
func Open() (ptp.Transport, error) { return openUSBFS("/sys", "/dev/bus/usb") }

type usbfsTransport struct {
	f      *os.File
	fd     uintptr
	intf   ptpInterface
	framer usbFramer
	closed bool
}

func openUSBFS(sysRoot, devRoot string) (*usbfsTransport, error) {
	dev, anyVisible, err := findUSB(sysRoot, ptp.FujifilmVendorID)
	if err != nil || dev == nil {
		if serr := sandbox.Error(); serr != nil {
			return nil, serr
		}
		hint := "Connect the camera with a data-capable USB-C cable, switch it on, and make sure it is " +
			"not asleep. " + linuxHint
		if !anyVisible {
			hint += " No USB device of any kind is visible: if an AI agent ran this command, it may be " +
				"sandboxed, in which case it must be run again outside the sandbox."
		}
		return nil, errs.New(errs.CameraNotFound, "no Fujifilm camera found on USB", hint)
	}
	node := dev.node(devRoot)
	f, err := os.OpenFile(node, os.O_RDWR, 0)
	if err != nil {
		if errors.Is(err, unix.EACCES) || errors.Is(err, unix.EPERM) {
			return nil, errs.Wrap(errs.CameraBusy, err,
				fmt.Sprintf("no permission to open the camera's USB device %s", node),
				"Make sure your user may access USB devices: add a udev rule for vendor 04cb (for example "+
					`SUBSYSTEM=="usb", ATTR{idVendor}=="04cb", MODE="0660", GROUP="plugdev"), replug the camera, `+
					"or run with sudo.")
		}
		return nil, errs.Wrap(errs.CameraNotFound, err, fmt.Sprintf("cannot open the camera's USB device %s (%v)", node, err),
			"Connect the camera with a data-capable USB-C cable, switch it on, and replug it. "+linuxHint)
	}
	t := &usbfsTransport{f: f, fd: f.Fd()}
	blob, err := io.ReadAll(f)
	if err == nil {
		t.intf, err = parseDescriptors(blob, dev.activeConfig)
	}
	if err != nil {
		f.Close()
		if errors.Is(err, errNoBulkEndpoints) {
			return nil, errs.New(errs.CameraNotFound, "the camera's PTP interface has no bulk endpoints", "")
		}
		return nil, errs.Wrap(errs.CameraNotFound, err, "the camera does not present a PTP interface",
			"It may be in USB WEBCAM mode or still starting up. Check CONNECTION MODE and replug.")
	}
	if err := t.claim(); err != nil {
		f.Close()
		return nil, errs.Wrap(errs.CameraBusy, err,
			fmt.Sprintf("cannot take control of the camera's USB interface (%v)", err),
			"Wait for any other x100vi command to finish, and quit FUJIFILM X RAW STUDIO, X Acquire, "+
				"Image Capture, Photos, Lightroom or any other app that talks to the camera, then try again. "+linuxHint)
	}
	t.framer = usbFramer{pipe: t, outMPS: t.intf.outMPS, inMPS: t.intf.inMPS}
	// An earlier process that was interrupted mid-transaction may have left data or a response in the
	// pipe; throw it away before the first request.
	t.framer.drain(func(p []byte) (int, error) { return t.bulkTimeout(t.intf.epIn, p, drainTimeoutMS) })
	return t, nil
}

func (t *usbfsTransport) ioctl(req uintptr, arg unsafe.Pointer) (int, error) {
	for {
		r, _, e := unix.Syscall(unix.SYS_IOCTL, t.fd, req, uintptr(arg))
		runtime.KeepAlive(t.f)
		if e == unix.EINTR {
			continue
		}
		if e != 0 {
			return int(r), e
		}
		return int(r), nil
	}
}

// claim detaches a kernel driver from the still-image interface and claims it, retrying for a while
// as the Python tool does (another process may be about to let go).
func (t *usbfsTransport) claim() error {
	var last error
	for attempt := 0; attempt < 8; attempt++ {
		if attempt > 0 {
			time.Sleep(300 * time.Millisecond)
		}
		if last = t.detachKernelDriver(); last != nil {
			continue
		}
		ifno := uint32(t.intf.number)
		if _, last = t.ioctl(usbdevfsClaimInterface, unsafe.Pointer(&ifno)); last == nil {
			return nil
		}
	}
	return last
}

// detachKernelDriver disconnects a kernel driver bound to the interface. No driver is fine, and an
// interface held by another usbfs user (libgphoto2, another x100vi) is left alone: claiming it then
// fails with EBUSY.
func (t *usbfsTransport) detachKernelDriver() error {
	gd := usbdevfsGetDriver{iface: uint32(t.intf.number)}
	if _, err := t.ioctl(usbdevfsGetDriverReq, unsafe.Pointer(&gd)); err != nil {
		if errors.Is(err, unix.ENODATA) {
			return nil
		}
		return err
	}
	name := string(gd.driver[:])
	if i := strings.IndexByte(name, 0); i >= 0 {
		name = name[:i]
	}
	if name == "usbfs" {
		return nil
	}
	cmd := usbdevfsIoctl{ifno: int32(t.intf.number), code: int32(usbdevfsDisconnect)}
	if _, err := t.ioctl(usbdevfsIoctlReq, unsafe.Pointer(&cmd)); err != nil && !errors.Is(err, unix.ENODATA) {
		return err
	}
	return nil
}

func (t *usbfsTransport) bulk(ep uint8, p []byte) (int, error) {
	return t.bulkTimeout(ep, p, usbTimeoutMS)
}

func (t *usbfsTransport) bulkTimeout(ep uint8, p []byte, timeoutMS uint32) (int, error) {
	xfer := usbdevfsBulkTransfer{ep: uint32(ep), len: uint32(len(p)), timeout: timeoutMS}
	if len(p) > 0 {
		xfer.data = unsafe.Pointer(&p[0])
	}
	n, err := t.ioctl(usbdevfsBulk, unsafe.Pointer(&xfer))
	runtime.KeepAlive(p)
	return n, err
}

func (t *usbfsTransport) BulkOut(p []byte) (int, error) { return t.bulk(t.intf.epOut, p) }
func (t *usbfsTransport) BulkIn(p []byte) (int, error)  { return t.bulk(t.intf.epIn, p) }

func (t *usbfsTransport) Transact(op uint16, params []uint32, dataOut []byte) (uint16, []uint32, []byte, error) {
	if t.closed {
		return 0, nil, nil, errs.NotRespondingErr("the camera connection is closed")
	}
	code, rparams, data, err := t.framer.transact(op, params, dataOut)
	if err != nil {
		e := errs.NotRespondingErr(fmt.Sprintf("USB transfer failed during request 0x%04X (%v)", op, err))
		return 0, nil, nil, errs.Wrap(e.Kind, err, e.Message, e.Hint)
	}
	return code, rparams, data, nil
}

func (t *usbfsTransport) Close() error {
	if t.closed {
		return nil
	}
	t.closed = true
	ifno := uint32(t.intf.number)
	_, _ = t.ioctl(usbdevfsReleaseInterface, unsafe.Pointer(&ifno))
	return t.f.Close()
}
