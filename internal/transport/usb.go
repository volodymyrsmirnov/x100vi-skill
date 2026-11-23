package transport

// Direct USB: PTP containers over the camera's still-image interface (used on Linux). The framing,
// the descriptor parsing and the sysfs lookup live here, without build tags, so they are tested on
// every system; usbfs_linux.go only moves bytes.

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unsafe"

	"github.com/volodymyrsmirnov/x100vi-skill/internal/ptp"
)

const (
	usbReadSize     = 1 << 20 // bytes asked for per bulk IN transfer
	usbChunkPackets = 2048    // a bulk OUT transfer is this many max-size packets (1 MiB at 512 bytes)
	maxEmptyReads   = 16      // zero-length packets tolerated in a row while a container is expected
)

// linuxHint is what to check on Linux when the camera cannot be reached.
const linuxHint = "On Linux, unmount the camera in the file manager (gvfs) and make sure your user may " +
	"access USB devices (a udev rule for vendor 04cb, or run with sudo)."

// bulkPipe moves bytes over the PTP interface's bulk endpoints. Each call is one bulk transfer and
// returns the number of bytes moved.
type bulkPipe interface {
	BulkOut(p []byte) (int, error)
	BulkIn(p []byte) (int, error)
}

// usbFramer runs PTP transactions over a bulkPipe.
type usbFramer struct {
	pipe   bulkPipe
	outMPS int // max packet size of the bulk OUT endpoint
	inMPS  int // max packet size of the bulk IN endpoint
	tid    uint32
	buf    []byte
}

func (f *usbFramer) write(p []byte) error {
	n, err := f.pipe.BulkOut(p)
	if err != nil {
		return err
	}
	if n != len(p) {
		return fmt.Errorf("short write: %d of %d bytes", n, len(p))
	}
	return nil
}

// transact runs one transaction. Errors are the pipe's own; the caller explains them.
func (f *usbFramer) transact(op uint16, params []uint32, dataOut []byte) (uint16, []uint32, []byte, error) {
	f.tid++
	if err := f.write(commandContainer(op, f.tid, params)); err != nil {
		return 0, nil, nil, err
	}
	if dataOut != nil {
		// One data container sent in bulk-sized pieces (a RAF is tens of MB). A short packet ends the
		// transfer, so a zero-length one follows an exact multiple of the packet size.
		mps := f.outMPS
		if mps <= 0 {
			mps = 512
		}
		total := headerLen + len(dataOut)
		chunk := mps * usbChunkPackets
		first := min(len(dataOut), chunk-headerLen)
		head := append(containerHeader(uint32(total), ptp.Data, op, f.tid), dataOut[:first]...)
		if err := f.write(head); err != nil {
			return 0, nil, nil, err
		}
		for pos := first; pos < len(dataOut); pos += chunk {
			if err := f.write(dataOut[pos:min(pos+chunk, len(dataOut))]); err != nil {
				return 0, nil, nil, err
			}
		}
		if total%mps == 0 {
			if err := f.write([]byte{}); err != nil {
				return 0, nil, nil, err
			}
		}
	}
	var dataIn []byte
	for {
		h, body, err := f.readContainer()
		if err != nil {
			return 0, nil, nil, err
		}
		switch h.ctype {
		case ptp.Data:
			dataIn = body
		case ptp.Resp:
			return h.code, responseParams(body), dataIn, nil
		}
	}
}

// readContainer reads one container: transfers until a header has arrived, then until its announced
// length has.
func (f *usbFramer) readContainer() (header, []byte, error) {
	size := max(f.inMPS, usbReadSize)
	if len(f.buf) < size {
		f.buf = make([]byte, size)
	}
	var acc []byte
	empty := 0
	read := func() error {
		n, err := f.pipe.BulkIn(f.buf[:size])
		if err != nil {
			return err
		}
		if n == 0 {
			empty++
			if empty > maxEmptyReads {
				return errors.New("the camera keeps sending empty packets")
			}
			return nil
		}
		empty = 0
		acc = append(acc, f.buf[:n]...)
		return nil
	}
	for len(acc) < headerLen {
		if err := read(); err != nil {
			return header{}, nil, err
		}
	}
	h, _ := parseHeader(acc)
	if want := int(min(uint64(h.length), 1<<30)); cap(acc) < want {
		grown := make([]byte, len(acc), want)
		copy(grown, acc)
		acc = grown
	}
	for uint64(len(acc)) < uint64(h.length) {
		if err := read(); err != nil {
			return header{}, nil, err
		}
	}
	return h, containerBody(acc, h.length), nil
}

// ptpInterface is the camera's still-image interface and its bulk endpoints.
type ptpInterface struct {
	number, alt   uint8
	epIn, epOut   uint8
	inMPS, outMPS int
}

var (
	errNoPTPInterface  = errors.New("no still-image interface")
	errNoBulkEndpoints = errors.New("no bulk endpoints")
	errBadDescriptors  = errors.New("unreadable USB descriptors")
)

// parseDescriptors finds the still-image interface (class 6) in the descriptors a usbfs device file
// returns: the device descriptor, then every configuration with its interfaces and endpoints. The
// configuration whose bConfigurationValue is activeConfig is used, or the first one.
func parseDescriptors(blob []byte, activeConfig int) (ptpInterface, error) {
	if len(blob) < 18 || blob[1] != 1 {
		return ptpInterface{}, errBadDescriptors
	}
	numConfigs := int(blob[17])
	pos := int(blob[0])
	var first, active []byte
	for i := 0; i < numConfigs && pos+9 <= len(blob); i++ {
		if blob[pos+1] != 2 {
			return ptpInterface{}, errBadDescriptors
		}
		total := int(binary.LittleEndian.Uint16(blob[pos+2:]))
		if total < 9 {
			return ptpInterface{}, errBadDescriptors
		}
		cfg := blob[pos:min(pos+total, len(blob))]
		if first == nil {
			first = cfg
		}
		if active == nil && int(cfg[5]) == activeConfig {
			active = cfg
		}
		pos += total
	}
	chosen := active
	if chosen == nil {
		chosen = first
	}
	if chosen == nil {
		return ptpInterface{}, errBadDescriptors
	}
	var found *ptpInterface
	var in, out bool
walk:
	for p := int(chosen[0]); p+2 <= len(chosen); {
		length, kind := int(chosen[p]), chosen[p+1]
		if length < 2 || p+length > len(chosen) {
			break
		}
		d := chosen[p : p+length]
		p += length
		switch {
		case kind == 4 && length >= 9: // interface
			if found != nil {
				break walk // the endpoints of the still-image interface end here
			}
			if d[5] == 6 {
				found = &ptpInterface{number: d[2], alt: d[3]}
			}
		case kind == 5 && length >= 7 && found != nil: // endpoint
			addr, attrs := d[2], d[3]
			mps := int(binary.LittleEndian.Uint16(d[4:]) & 0x7FF)
			if attrs&3 != 2 { // not bulk
				continue
			}
			if addr&0x80 != 0 && !in {
				found.epIn, found.inMPS, in = addr, mps, true
			} else if addr&0x80 == 0 && !out {
				found.epOut, found.outMPS, out = addr, mps, true
			}
		}
	}
	if found == nil {
		return ptpInterface{}, errNoPTPInterface
	}
	if !in || !out {
		return ptpInterface{}, errNoBulkEndpoints
	}
	return *found, nil
}

// usbDevice is a USB device found in sysfs.
type usbDevice struct {
	sysPath      string
	bus, dev     int
	activeConfig int
}

// node is the usbfs device file.
func (d usbDevice) node(devRoot string) string {
	return filepath.Join(devRoot, fmt.Sprintf("%03d", d.bus), fmt.Sprintf("%03d", d.dev))
}

// findUSB looks in sysRoot (normally /sys) for the first device of a vendor. anyVisible is false
// when no USB device of any kind is listed.
func findUSB(sysRoot string, vendor uint16) (dev *usbDevice, anyVisible bool, err error) {
	matches, err := filepath.Glob(filepath.Join(sysRoot, "bus", "usb", "devices", "*", "idVendor"))
	if err != nil {
		return nil, false, err
	}
	want := fmt.Sprintf("%04x", vendor)
	for _, m := range matches {
		raw, rerr := os.ReadFile(m)
		if rerr != nil {
			continue
		}
		anyVisible = true
		if !strings.EqualFold(strings.TrimSpace(string(raw)), want) {
			continue
		}
		dir := filepath.Dir(m)
		bus, berr := readInt(filepath.Join(dir, "busnum"))
		num, derr := readInt(filepath.Join(dir, "devnum"))
		if berr != nil || derr != nil {
			continue
		}
		active, _ := readInt(filepath.Join(dir, "bConfigurationValue")) // empty when unconfigured
		return &usbDevice{sysPath: dir, bus: bus, dev: num, activeConfig: active}, true, nil
	}
	return nil, anyVisible, nil
}

func readInt(path string) (int, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(raw)))
}

// usbfs ioctl requests (linux/usbdevice_fs.h), encoded the generic way (x86, arm, arm64, riscv).

type usbdevfsBulkTransfer struct {
	ep      uint32
	len     uint32
	timeout uint32 // milliseconds
	data    unsafe.Pointer
}

type usbdevfsIoctl struct {
	ifno int32
	code int32
	data unsafe.Pointer
}

type usbdevfsGetDriver struct {
	iface  uint32
	driver [256]byte
}

func ioc(dir, nr, size uintptr) uintptr { return dir<<30 | size<<16 | 'U'<<8 | nr }

const (
	iocNone  = 0
	iocWrite = 1
	iocRead  = 2
)

var (
	usbdevfsBulk             = ioc(iocRead|iocWrite, 2, unsafe.Sizeof(usbdevfsBulkTransfer{}))
	usbdevfsGetDriverReq     = ioc(iocWrite, 8, unsafe.Sizeof(usbdevfsGetDriver{}))
	usbdevfsClaimInterface   = ioc(iocRead, 15, 4)
	usbdevfsReleaseInterface = ioc(iocRead, 16, 4)
	usbdevfsIoctlReq         = ioc(iocRead|iocWrite, 18, unsafe.Sizeof(usbdevfsIoctl{}))
	usbdevfsDisconnect       = ioc(iocNone, 22, 0)
)
