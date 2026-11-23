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
	"time"
	"unsafe"

	"github.com/volodymyrsmirnov/x100vi-skill/internal/errs"
	"github.com/volodymyrsmirnov/x100vi-skill/internal/ptp"
)

const (
	usbReadSize     = 1 << 20 // bytes asked for per bulk IN transfer
	usbChunkPackets = 2048    // a bulk OUT transfer is this many max-size packets (1 MiB at 512 bytes)
	maxEmptyReads   = 16      // zero-length packets tolerated in a row while a container is expected

	// maxStaleContainers is how many containers of earlier transactions are discarded before giving up.
	// An interrupted transaction leaves at most a data container and a response behind.
	maxStaleContainers = 4

	// When the device is opened, what an earlier, interrupted process left in the bulk IN pipe is read
	// and thrown away: reads with a short timeout until the pipe is empty (see drain).
	drainTimeoutMS = 100

	// closeReplyTimeout bounds the wait for CloseSession's reply. The command's work is done by then, so
	// a camera that does not answer the close must not hold the command up for a full transfer timeout.
	closeReplyTimeout = 5 * time.Second
)

// drainLimits bound the startup drain. An interrupted GetObject of a full-size TIFF leaves over 245 MB
// behind; at USB 2.0 speeds that drains in well under ten seconds.
type drainLimits struct {
	bytes int64
	time  time.Duration
}

var defaultDrainLimits = drainLimits{bytes: 1 << 30, time: 60 * time.Second}

var (
	// errStaleContainers is a camera that keeps answering other transactions than the current one.
	errStaleContainers = errors.New("the camera is answering an earlier request")
	// errBulkTimeout is a bulk transfer that ended because its timeout passed. bulkPipe implementations
	// wrap it, so the framer can tell a quiet pipe from a broken one.
	errBulkTimeout = errors.New("bulk transfer timed out")
	// errDrainBudget is a drain that used up its byte or time budget before the pipe went quiet.
	errDrainBudget = errors.New("the camera kept sending data")
)

// linuxHint is what to check on Linux when the camera cannot be reached.
const linuxHint = "On Linux, unmount the camera in the file manager (gvfs) and make sure your user may " +
	"access USB devices (a udev rule for vendor 04cb, or run with sudo)."

// bulkPipe moves bytes over the PTP interface's bulk endpoints. Each call is one bulk transfer and
// returns the number of bytes moved. BulkOut and BulkIn use the transport's normal timeout;
// BulkInTimeout gives up after timeout instead. A transfer that times out returns an error wrapping
// errBulkTimeout.
type bulkPipe interface {
	BulkOut(p []byte) (int, error)
	BulkIn(p []byte) (int, error)
	BulkInTimeout(p []byte, timeout time.Duration) (int, error)
}

// usbFramer runs PTP transactions over a bulkPipe. It numbers them: OpenSession is transaction 0, as
// PTP requires, and the requests after it count from 1.
type usbFramer struct {
	pipe   bulkPipe
	outMPS int    // max packet size of the bulk OUT endpoint
	inMPS  int    // max packet size of the bulk IN endpoint
	tid    uint32 // id of the last transaction
	buf    []byte
	clock  func() time.Time // nil: time.Now; tests replace it
}

func (f *usbFramer) now() time.Time {
	if f.clock != nil {
		return f.clock()
	}
	return time.Now()
}

func (f *usbFramer) packetSize() int {
	if f.inMPS > 0 {
		return f.inMPS
	}
	return 512
}

// nextTID is the transaction id of a new request. PTP sends OpenSession with id 0 and answers it with
// 0; the first request of the session is 1.
func (f *usbFramer) nextTID(op uint16) uint32 {
	if op == ptp.OpOpenSession {
		f.tid = 0
	} else {
		f.tid++
	}
	return f.tid
}

// drain reads and discards what is waiting in the bulk IN pipe, left by an earlier process that was
// interrupted mid-transaction, so the first request does not take it for its answer. read is a bulk IN
// transfer with a short timeout.
//
// The pipe is empty when a read of one packet times out. Data is read in large transfers, but when one
// of those times out it may have taken part of the data with it (usbfs does not say how much arrived
// before the timeout), so a one-packet read confirms the pipe is quiet before drain believes it.
//
// It returns the bytes thrown away and nil once the pipe is empty; errDrainBudget when the byte or time
// budget of lim ran out first; or the first error other than a timeout. Only nil means the next
// request will read its own answer.
func (f *usbFramer) drain(read func(p []byte) (int, error), lim drainLimits) (int64, error) {
	size := max(f.inMPS, usbReadSize)
	if len(f.buf) < size {
		f.buf = make([]byte, size)
	}
	start := f.now()
	var discarded int64
	probe := true // the next read is one packet: normally the pipe is empty, and that is all it takes
	for {
		if discarded >= lim.bytes || f.now().Sub(start) >= lim.time {
			return discarded, fmt.Errorf("%w: %d bytes discarded in %v, and more is coming",
				errDrainBudget, discarded, f.now().Sub(start).Round(time.Millisecond))
		}
		want := size
		if probe {
			want = f.packetSize()
		}
		n, err := read(f.buf[:want])
		if errors.Is(err, errBulkTimeout) {
			if probe {
				return discarded, nil
			}
			probe = true
			continue
		}
		if err != nil {
			return discarded, err
		}
		discarded += int64(n)
		probe = false
	}
}

// clearStale drains the bulk IN pipe before the first request. When the pipe cannot be emptied (the
// budget ran out, or the transfer failed), no request may be sent: the answer to it would be lost among
// the old data. clearStale then calls release, which frees the interface and closes the device, and
// returns the error to report.
func (f *usbFramer) clearStale(read func(p []byte) (int, error), lim drainLimits, release func()) error {
	if _, err := f.drain(read, lim); err != nil {
		release()
		return errs.Wrap(errs.NotResponding, err,
			"the camera is still sending data from an earlier, interrupted command",
			"Switch the camera off and on, reconnect, then run the command again.")
	}
	return nil
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
	tid := f.nextTID(op)
	if err := f.write(commandContainer(op, tid, params)); err != nil {
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
		head := append(containerHeader(uint32(total), ptp.Data, op, tid), dataOut[:first]...)
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
	// CloseSession comes after the command's work: a camera that does not answer it gets
	// closeReplyTimeout in all, not a full transfer timeout per read.
	var deadline time.Time
	if op == ptp.OpCloseSession {
		deadline = f.now().Add(closeReplyTimeout)
	}
	var dataIn []byte
	stale := 0
	for {
		h, body, err := f.readContainer(deadline)
		if err != nil {
			return 0, nil, nil, err
		}
		if h.tid != tid {
			// Left over from an earlier transaction that was interrupted (a timeout, a killed process):
			// it is not the answer to this one.
			stale++
			if stale > maxStaleContainers {
				return 0, nil, nil, fmt.Errorf("%w: %d containers in a row belonged to other transactions "+
					"(last: type %d, code 0x%04X, transaction %d; expected %d)",
					errStaleContainers, stale, h.ctype, h.code, h.tid, tid)
			}
			continue
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
// length has. With a deadline (not zero), every transfer gives up when it passes.
func (f *usbFramer) readContainer(deadline time.Time) (header, []byte, error) {
	size := max(f.inMPS, usbReadSize)
	if len(f.buf) < size {
		f.buf = make([]byte, size)
	}
	var acc []byte
	empty := 0
	read := func() error {
		var n int
		var err error
		if deadline.IsZero() {
			n, err = f.pipe.BulkIn(f.buf[:size])
		} else if left := deadline.Sub(f.now()); left > 0 {
			n, err = f.pipe.BulkInTimeout(f.buf[:size], left)
		} else {
			err = fmt.Errorf("%w: no complete reply in time", errBulkTimeout)
		}
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
