package transport

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"
	"unsafe"

	"github.com/volodymyrsmirnov/x100vi-skill/internal/ptp"
)

func TestCommandContainer(t *testing.T) {
	got := commandContainer(0x1015, 7, []uint32{0xD16E, 0xFFFFFFFF})
	want := []byte{20, 0, 0, 0, 1, 0, 0x15, 0x10, 7, 0, 0, 0, 0x6E, 0xD1, 0, 0, 0xFF, 0xFF, 0xFF, 0xFF}
	if !bytes.Equal(got, want) {
		t.Fatalf("got % x\nwant % x", got, want)
	}
	if got := commandContainer(0x1001, 1, nil); len(got) != 12 || got[0] != 12 {
		t.Fatalf("no-parameter command = % x", got)
	}
}

func TestResponseParsing(t *testing.T) {
	resp := append(containerHeader(20, ptp.Resp, ptp.RCOK, 3), 1, 0, 0, 0, 2, 0, 0, 0)
	h, ok := parseHeader(resp)
	if !ok || h.length != 20 || h.ctype != ptp.Resp || h.code != ptp.RCOK || h.tid != 3 {
		t.Fatalf("header = %+v, %v", h, ok)
	}
	if got := responseParams(containerBody(resp, h.length)); !reflect.DeepEqual(got, []uint32{1, 2}) {
		t.Fatalf("params = %v", got)
	}
	// A length shorter than what arrived clips the body; a longer one is clipped to what arrived.
	if got := responseParams(containerBody(resp, 16)); !reflect.DeepEqual(got, []uint32{1}) {
		t.Fatalf("clipped params = %v", got)
	}
	if got := responseParams(containerBody(resp, 1000)); len(got) != 2 {
		t.Fatalf("over-long params = %v", got)
	}
	if got := containerBody(resp, 4); len(got) != 0 {
		t.Fatalf("body of a too-short length = % x", got)
	}
	if _, ok := parseHeader(resp[:11]); ok {
		t.Fatal("parsed a header from 11 bytes")
	}
}

func TestTimeouts(t *testing.T) {
	for op, want := range map[uint16]time.Duration{
		0x1009: 300 * time.Second, 0x900D: 300 * time.Second, 0x100D: 300 * time.Second,
		0x1001: 30 * time.Second, 0x1008: 30 * time.Second, 0x1015: 30 * time.Second,
	} {
		if got := opTimeout(op); got != want {
			t.Errorf("opTimeout(%#x) = %v, want %v", op, got, want)
		}
	}
}

// fakePipe is a USB endpoint pair: it records what is written and answers reads from a queue of
// transfers.
type fakePipe struct {
	writes [][]byte
	reads  [][]byte
	err    error
}

func (p *fakePipe) BulkOut(b []byte) (int, error) {
	if p.err != nil {
		return 0, p.err
	}
	p.writes = append(p.writes, append([]byte(nil), b...))
	return len(b), nil
}

func (p *fakePipe) BulkIn(b []byte) (int, error) {
	if len(p.reads) == 0 {
		return 0, errors.New("timeout")
	}
	next := p.reads[0]
	n := copy(b, next)
	if n < len(next) {
		p.reads[0] = next[n:]
	} else {
		p.reads = p.reads[1:]
	}
	return n, nil
}

func container(ctype, code uint16, tid uint32, payload []byte) []byte {
	return append(containerHeader(uint32(12+len(payload)), ctype, code, tid), payload...)
}

func TestUSBReadsDataThenResponse(t *testing.T) {
	payload := bytes.Repeat([]byte{0xAB}, 3*usbReadSize+100)
	data := container(ptp.Data, 0x1009, 1, payload)
	pipe := &fakePipe{reads: [][]byte{
		data[:5], data[5:], // the header arrives in two transfers
		container(ptp.Resp, ptp.RCOK, 1, []byte{9, 0, 0, 0}),
	}}
	f := usbFramer{pipe: pipe, outMPS: 512, inMPS: 512}
	code, params, got, err := f.transact(0x1009, []uint32{0}, nil)
	if err != nil || code != ptp.RCOK || !reflect.DeepEqual(params, []uint32{9}) || !bytes.Equal(got, payload) {
		t.Fatalf("transact = %#x %v %d bytes %v", code, params, len(got), err)
	}
	if len(pipe.writes) != 1 || !bytes.Equal(pipe.writes[0], commandContainer(0x1009, 1, []uint32{0})) {
		t.Fatalf("writes = %x", pipe.writes)
	}
}

func TestUSBSkipsAZeroLengthPacketBeforeTheResponse(t *testing.T) {
	pipe := &fakePipe{reads: [][]byte{{}, container(ptp.Resp, 0x2019, 1, nil)}}
	f := usbFramer{pipe: pipe, outMPS: 512, inMPS: 512}
	code, params, data, err := f.transact(0x1015, []uint32{0xD16E}, nil)
	if err != nil || code != 0x2019 || len(params) != 0 || data != nil {
		t.Fatalf("transact = %#x %v %v %v", code, params, data, err)
	}
}

func TestUSBGivesUpOnEndlessEmptyPackets(t *testing.T) {
	reads := make([][]byte, maxEmptyReads+2)
	for i := range reads {
		reads[i] = []byte{}
	}
	f := usbFramer{pipe: &fakePipe{reads: reads}, outMPS: 512, inMPS: 512}
	if _, _, _, err := f.transact(0x1001, nil, nil); err == nil {
		t.Fatal("no error")
	}
}

func TestUSBSendsDataInChunksWithZeroLengthPacket(t *testing.T) {
	for _, tc := range []struct {
		size int
		zlp  bool
	}{
		{size: 500, zlp: true}, // 512 bytes in all
		{size: 501, zlp: false},
		{size: 3*512*usbChunkPackets + 500, zlp: true},
		{size: 512*usbChunkPackets - 12, zlp: true}, // exactly one chunk
		{size: 0, zlp: false},
	} {
		data := make([]byte, tc.size)
		for i := range data {
			data[i] = byte(i)
		}
		pipe := &fakePipe{reads: [][]byte{container(ptp.Resp, ptp.RCOK, 1, nil)}}
		f := usbFramer{pipe: pipe, outMPS: 512, inMPS: 512}
		if _, _, _, err := f.transact(0x900D, nil, data); err != nil {
			t.Fatalf("size %d: %v", tc.size, err)
		}
		writes := pipe.writes[1:] // after the command
		last := writes[len(writes)-1]
		if gotZLP := len(last) == 0; gotZLP != tc.zlp {
			t.Errorf("size %d: zero-length packet %v, want %v", tc.size, gotZLP, tc.zlp)
		}
		var joined []byte
		for i, w := range writes {
			if len(w) > 512*usbChunkPackets {
				t.Errorf("size %d: write %d is %d bytes", tc.size, i, len(w))
			}
			if i < len(writes)-1 && len(w)%512 != 0 && len(w) != 0 {
				t.Errorf("size %d: write %d of %d bytes is not a whole number of packets", tc.size, i, len(w))
			}
			joined = append(joined, w...)
		}
		h, _ := parseHeader(joined)
		if h.ctype != ptp.Data || h.code != 0x900D || h.tid != 1 || int(h.length) != 12+tc.size ||
			!bytes.Equal(joined[12:], data) {
			t.Errorf("size %d: data container %+v, %d bytes", tc.size, h, len(joined))
		}
	}
}

func TestUSBTransactionIDsCount(t *testing.T) {
	pipe := &fakePipe{reads: [][]byte{container(ptp.Resp, ptp.RCOK, 1, nil), container(ptp.Resp, ptp.RCOK, 2, nil)}}
	f := usbFramer{pipe: pipe, outMPS: 512, inMPS: 512}
	f.transact(0x1002, []uint32{1}, nil)
	f.transact(0x1001, nil, nil)
	if h, _ := parseHeader(pipe.writes[1]); h.tid != 2 {
		t.Fatalf("second transaction id = %d", h.tid)
	}
}

func TestUSBReportsPipeErrors(t *testing.T) {
	f := usbFramer{pipe: &fakePipe{err: errors.New("broken")}, outMPS: 512, inMPS: 512}
	if _, _, _, err := f.transact(0x1001, nil, nil); err == nil || err.Error() != "broken" {
		t.Fatalf("err = %v", err)
	}
}

// descriptors builds a usbfs descriptor blob: a device descriptor and the given configurations.
func descriptors(configs ...[]byte) []byte {
	dev := []byte{18, 1, 0x00, 0x02, 0, 0, 0, 64, 0xCB, 0x04, 0x05, 0x03, 0, 1, 1, 2, 3, byte(len(configs))}
	for _, c := range configs {
		dev = append(dev, c...)
	}
	return dev
}

func config(value byte, parts ...[]byte) []byte {
	var body []byte
	for _, p := range parts {
		body = append(body, p...)
	}
	c := []byte{9, 2, 0, 0, 1, value, 0, 0xC0, 50}
	binary.LittleEndian.PutUint16(c[2:], uint16(9+len(body)))
	return append(c, body...)
}

func iface(number, alt, class, endpoints byte) []byte {
	return []byte{9, 4, number, alt, endpoints, class, 1, 1, 0}
}

func endpoint(addr, attrs byte, mps uint16) []byte {
	return []byte{7, 5, addr, attrs, byte(mps), byte(mps >> 8), 0}
}

func TestParseDescriptorsFindsTheStillImageInterface(t *testing.T) {
	blob := descriptors(config(1,
		iface(0, 0, 0xFF, 1), endpoint(0x81, 2, 512), // a vendor interface first
		iface(1, 0, 6, 3),
		[]byte{5, 0x24, 1, 2, 3},  // a class-specific descriptor
		endpoint(0x83, 3, 8),      // interrupt IN
		endpoint(0x02, 2, 512),    // bulk OUT
		endpoint(0x84, 2, 0x1200), // bulk IN; the high bits are extra transactions
		iface(2, 0, 6, 1), endpoint(0x85, 2, 64),
	))
	got, err := parseDescriptors(blob, 1)
	want := ptpInterface{number: 1, epIn: 0x84, epOut: 0x02, inMPS: 0x200, outMPS: 512}
	if err != nil || got != want {
		t.Fatalf("got %+v, %v; want %+v", got, err, want)
	}
}

func TestParseDescriptorsUsesTheActiveConfiguration(t *testing.T) {
	blob := descriptors(
		config(1, iface(0, 0, 0xFF, 0)),
		config(2, iface(3, 0, 6, 2), endpoint(0x81, 2, 64), endpoint(0x01, 2, 64)),
	)
	got, err := parseDescriptors(blob, 2)
	if err != nil || got.number != 3 || got.inMPS != 64 {
		t.Fatalf("active config: %+v, %v", got, err)
	}
	if _, err := parseDescriptors(blob, 0); !errors.Is(err, errNoPTPInterface) {
		t.Fatalf("first config: %v", err)
	}
}

func TestParseDescriptorsErrors(t *testing.T) {
	if _, err := parseDescriptors(descriptors(config(1, iface(0, 0, 6, 1), endpoint(0x81, 2, 512))), 1); !errors.Is(err, errNoBulkEndpoints) {
		t.Errorf("missing OUT endpoint: %v", err)
	}
	if _, err := parseDescriptors(descriptors(config(1, iface(0, 0, 14, 0))), 1); !errors.Is(err, errNoPTPInterface) {
		t.Errorf("webcam: %v", err)
	}
	if _, err := parseDescriptors([]byte{1, 2, 3}, 1); !errors.Is(err, errBadDescriptors) {
		t.Errorf("garbage: %v", err)
	}
	broken := descriptors(config(1, iface(0, 0, 6, 0)))
	broken[18+9] = 0 // a zero-length descriptor must not loop forever
	if _, err := parseDescriptors(broken, 1); err == nil {
		t.Error("zero-length descriptor accepted")
	}
}

func TestFindUSBInSysfs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sysfs device names contain ':', which Windows does not allow in file names")
	}
	root := t.TempDir()
	add := func(name string, files map[string]string) {
		dir := filepath.Join(root, "bus", "usb", "devices", name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for k, v := range files {
			if err := os.WriteFile(filepath.Join(dir, k), []byte(v), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	dev, any, err := findUSB(root, ptp.FujifilmVendorID)
	if dev != nil || any || err != nil {
		t.Fatalf("empty sysfs: %v %v %v", dev, any, err)
	}
	add("usb1", map[string]string{"idVendor": "1d6b\n", "busnum": "1\n", "devnum": "1\n"})
	add("1-1:1.0", map[string]string{"bInterfaceClass": "06\n"})
	if dev, any, _ = findUSB(root, ptp.FujifilmVendorID); dev != nil || !any {
		t.Fatalf("hub only: %v %v", dev, any)
	}
	add("1-1", map[string]string{"idVendor": "04cb\n", "busnum": "1\n", "devnum": "7\n", "bConfigurationValue": "1\n"})
	dev, _, err = findUSB(root, ptp.FujifilmVendorID)
	if err != nil || dev == nil || dev.bus != 1 || dev.dev != 7 || dev.activeConfig != 1 {
		t.Fatalf("camera: %+v %v", dev, err)
	}
	if got := dev.node("/dev/bus/usb"); got != filepath.Join("/dev/bus/usb", "001", "007") {
		t.Fatalf("node = %s", got)
	}
}

func TestUsbfsIoctlNumbers(t *testing.T) {
	if unsafe.Sizeof(uintptr(0)) != 8 {
		t.Skip("values below are for 64-bit systems")
	}
	for name, c := range map[string][2]uintptr{
		"USBDEVFS_BULK":             {usbdevfsBulk, 0xC0185502},
		"USBDEVFS_GETDRIVER":        {usbdevfsGetDriverReq, 0x41045508},
		"USBDEVFS_CLAIMINTERFACE":   {usbdevfsClaimInterface, 0x8004550F},
		"USBDEVFS_RELEASEINTERFACE": {usbdevfsReleaseInterface, 0x80045510},
		"USBDEVFS_IOCTL":            {usbdevfsIoctlReq, 0xC0105512},
		"USBDEVFS_DISCONNECT":       {usbdevfsDisconnect, 0x5516},
	} {
		if c[0] != c[1] {
			t.Errorf("%s = %#x, want %#x", name, c[0], c[1])
		}
	}
}
