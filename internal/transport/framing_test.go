package transport

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"
	"unsafe"

	"github.com/volodymyrsmirnov/x100vi-skill/internal/errs"
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
// transfers. With the queue empty, a read times out at once.
type fakePipe struct {
	writes [][]byte
	reads  [][]byte
	err    error

	longReads int             // BulkIn calls (the transport's normal timeout)
	timeouts  []time.Duration // what each BulkInTimeout call was given
}

func (p *fakePipe) BulkOut(b []byte) (int, error) {
	if p.err != nil {
		return 0, p.err
	}
	p.writes = append(p.writes, append([]byte(nil), b...))
	return len(b), nil
}

func (p *fakePipe) BulkIn(b []byte) (int, error) {
	p.longReads++
	return p.next(b)
}

func (p *fakePipe) BulkInTimeout(b []byte, timeout time.Duration) (int, error) {
	p.timeouts = append(p.timeouts, timeout)
	return p.next(b)
}

func (p *fakePipe) next(b []byte) (int, error) {
	if len(p.reads) == 0 {
		return 0, fmt.Errorf("%w: nothing to read", errBulkTimeout)
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

// OpenSession is transaction 0 and is answered with 0; the requests after it count from 1.
func TestUSBTransactionIDsCount(t *testing.T) {
	pipe := &fakePipe{reads: [][]byte{
		container(ptp.Resp, ptp.RCOK, 1, nil), // GetDeviceInfo before the session
		container(ptp.Resp, ptp.RCOK, 0, nil), // OpenSession
		container(ptp.Resp, ptp.RCOK, 1, nil),
		container(ptp.Resp, ptp.RCOK, 2, nil),
		container(ptp.Resp, ptp.RCOK, 3, nil), // CloseSession
	}}
	f := usbFramer{pipe: pipe, outMPS: 512, inMPS: 512}
	for i, op := range []uint16{ptp.OpGetDeviceInfo, ptp.OpOpenSession, 0x1015, 0x1015, ptp.OpCloseSession} {
		if code, _, _, err := f.transact(op, nil, nil); err != nil || code != ptp.RCOK {
			t.Fatalf("request %d (0x%04X) = %#x %v", i, op, code, err)
		}
	}
	var got []uint32
	for _, w := range pipe.writes {
		h, _ := parseHeader(w)
		got = append(got, h.tid)
	}
	if want := []uint32{1, 0, 1, 2, 3}; !reflect.DeepEqual(got, want) {
		t.Fatalf("transaction ids = %v, want %v", got, want)
	}
	if len(pipe.reads) != 0 {
		t.Fatalf("%d transfers left unread", len(pipe.reads))
	}
}

// A second OpenSession (the session was reopened) starts the count again.
func TestUSBOpenSessionRestartsTheCount(t *testing.T) {
	f, pipe := staleFramer(container(ptp.Resp, ptp.RCSessionOpen, 0, nil), container(ptp.Resp, ptp.RCOK, 1, nil))
	if code, _, _, err := f.transact(ptp.OpOpenSession, []uint32{1}, nil); err != nil || code != ptp.RCSessionOpen {
		t.Fatalf("OpenSession = %#x %v", code, err)
	}
	if code, _, _, err := f.transact(ptp.OpGetDeviceInfo, nil, nil); err != nil || code != ptp.RCOK {
		t.Fatalf("GetDeviceInfo = %#x %v", code, err)
	}
	if h, _ := parseHeader(pipe.writes[0]); h.tid != 0 {
		t.Fatalf("OpenSession sent as transaction %d", h.tid)
	}
	if h, _ := parseHeader(pipe.writes[1]); h.tid != 1 {
		t.Fatalf("request after OpenSession sent as transaction %d", h.tid)
	}
}

// After four transactions, the fifth (id 5) runs while the camera still holds answers to the fourth,
// which was interrupted.
func staleFramer(reads ...[]byte) (*usbFramer, *fakePipe) {
	pipe := &fakePipe{reads: reads}
	return &usbFramer{pipe: pipe, outMPS: 512, inMPS: 512, tid: 4}, pipe
}

func TestUSBMatchingTransactionIDsAreTheAnswer(t *testing.T) {
	payload := []byte("device info")
	f, pipe := staleFramer(container(ptp.Data, 0x1001, 5, payload), container(ptp.Resp, ptp.RCOK, 5, []byte{3, 0, 0, 0}))
	code, params, data, err := f.transact(0x1001, nil, nil)
	if err != nil || code != ptp.RCOK || !reflect.DeepEqual(params, []uint32{3}) || !bytes.Equal(data, payload) {
		t.Fatalf("transact = %#x %v %q %v", code, params, data, err)
	}
	if len(pipe.reads) != 0 {
		t.Fatalf("%d transfers left unread", len(pipe.reads))
	}
}

func TestUSBDiscardsAStaleResponse(t *testing.T) {
	f, pipe := staleFramer(container(ptp.Resp, 0x2019, 4, nil), container(ptp.Resp, ptp.RCOK, 5, nil))
	code, _, data, err := f.transact(0x1016, []uint32{0xD16E}, []byte{1, 0})
	if err != nil || code != ptp.RCOK || data != nil {
		t.Fatalf("transact = %#x %v %v", code, data, err)
	}
	if len(pipe.reads) != 0 {
		t.Fatalf("%d transfers left unread", len(pipe.reads))
	}
}

func TestUSBDiscardsAStaleDataContainer(t *testing.T) {
	stale := container(ptp.Data, 0x1009, 4, bytes.Repeat([]byte{0xEE}, usbReadSize+10)) // spans transfers
	want := []byte{0x01, 0x00}
	for name, reads := range map[string][][]byte{
		"data only": {stale[:usbReadSize], stale[usbReadSize:],
			container(ptp.Data, 0x1015, 5, want), container(ptp.Resp, ptp.RCOK, 5, nil)},
		"data and response": {stale[:usbReadSize], stale[usbReadSize:], container(ptp.Resp, ptp.RCOK, 4, nil),
			container(ptp.Data, 0x1015, 5, want), container(ptp.Resp, ptp.RCOK, 5, nil)},
	} {
		f, pipe := staleFramer(reads...)
		code, _, data, err := f.transact(0x1015, []uint32{0xD16E}, nil)
		if err != nil || code != ptp.RCOK || !bytes.Equal(data, want) {
			t.Fatalf("%s: transact = %#x %d bytes %v", name, code, len(data), err)
		}
		if len(pipe.reads) != 0 {
			t.Fatalf("%s: %d transfers left unread", name, len(pipe.reads))
		}
	}
}

func TestUSBGivesUpOnEndlessStaleContainers(t *testing.T) {
	var reads [][]byte
	for i := 0; i <= maxStaleContainers; i++ {
		reads = append(reads, container(ptp.Resp, ptp.RCOK, 4, nil))
	}
	reads = append(reads, container(ptp.Resp, ptp.RCOK, 5, nil))
	f, _ := staleFramer(reads...)
	if _, _, _, err := f.transact(0x1001, nil, nil); !errors.Is(err, errStaleContainers) {
		t.Fatalf("err = %v", err)
	}
}

// OpenSession expects the answer of transaction 0, and discards answers to other transactions.
func TestUSBOpenSessionIsTransactionZero(t *testing.T) {
	f, pipe := staleFramer(container(ptp.Resp, 0x2019, 4, nil), container(ptp.Resp, ptp.RCOK, 0, nil))
	if code, _, _, err := f.transact(ptp.OpOpenSession, []uint32{1}, nil); err != nil || code != ptp.RCOK {
		t.Fatalf("OpenSession = %#x %v", code, err)
	}
	if h, _ := parseHeader(pipe.writes[0]); h.tid != 0 || h.code != ptp.OpOpenSession {
		t.Fatalf("OpenSession sent as %+v", h)
	}
	if len(pipe.reads) != 0 || f.tid != 0 {
		t.Fatalf("%d transfers left unread, last id %d", len(pipe.reads), f.tid)
	}
}

// Every request other than OpenSession, CloseSession included, takes only the answer with its own id:
// a stale transaction-0 response (an earlier OpenSession) is not the answer.
func TestUSBZeroIDIsNotTheAnswerToOtherRequests(t *testing.T) {
	for _, op := range []uint16{ptp.OpCloseSession, ptp.OpGetDeviceInfo} {
		f, pipe := staleFramer(container(ptp.Resp, ptp.RCOK, 0, nil), container(ptp.Resp, 0x2019, 5, nil))
		code, _, _, err := f.transact(op, nil, nil)
		if err != nil || code != 0x2019 {
			t.Fatalf("0x%04X = %#x %v", op, code, err)
		}
		if h, _ := parseHeader(pipe.writes[0]); h.tid != 5 {
			t.Fatalf("0x%04X sent as transaction %d", op, h.tid)
		}
		if len(pipe.reads) != 0 {
			t.Fatalf("0x%04X: %d transfers left unread", op, len(pipe.reads))
		}
	}
	// Only zero-id answers: CloseSession gives up rather than taking one of them.
	var reads [][]byte
	for i := 0; i <= maxStaleContainers; i++ {
		reads = append(reads, container(ptp.Resp, ptp.RCOK, 0, nil))
	}
	f, _ := staleFramer(reads...)
	if _, _, _, err := f.transact(ptp.OpCloseSession, nil, nil); !errors.Is(err, errStaleContainers) {
		t.Fatalf("CloseSession err = %v", err)
	}
}

// fakeClock is a clock that moves only when told to.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }
func newFakeClock() *fakeClock               { return &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)} }
func timedOut() (int, error)                 { return 0, fmt.Errorf("%w: test", errBulkTimeout) }
func countReads(read func([]byte) (int, error)) (func([]byte) (int, error), *[]int) {
	var sizes []int
	return func(p []byte) (int, error) {
		sizes = append(sizes, len(p))
		return read(p)
	}, &sizes
}

// drain throws away what an interrupted process left in the pipe, so the first request reads its own
// answer.
func TestUSBDrainDiscardsStaleData(t *testing.T) {
	stale := container(ptp.Data, 0x1009, 7, bytes.Repeat([]byte{0xEE}, usbReadSize+10))
	pipe := &fakePipe{reads: [][]byte{stale[:usbReadSize], stale[usbReadSize:], {}, container(ptp.Resp, ptp.RCOK, 7, nil)}}
	f := usbFramer{pipe: pipe, outMPS: 512, inMPS: 512}
	discarded, err := f.drain(pipe.BulkIn, defaultDrainLimits)
	if err != nil || discarded != int64(len(stale)+12) {
		t.Fatalf("drain = %d bytes, %v", discarded, err)
	}
	if len(pipe.reads) != 0 || len(pipe.writes) != 0 {
		t.Fatalf("%d transfers left, %d writes", len(pipe.reads), len(pipe.writes))
	}
	pipe.reads = [][]byte{container(ptp.Resp, ptp.RCOK, 0, nil)}
	if code, _, _, err := f.transact(ptp.OpOpenSession, []uint32{1}, nil); err != nil || code != ptp.RCOK {
		t.Fatalf("OpenSession after draining = %#x %v", code, err)
	}
}

// An empty pipe costs one one-packet read.
func TestUSBDrainOfAnEmptyPipeReadsOnePacket(t *testing.T) {
	pipe := &fakePipe{}
	f := usbFramer{pipe: pipe, outMPS: 512, inMPS: 512}
	read, sizes := countReads(pipe.BulkIn)
	if discarded, err := f.drain(read, defaultDrainLimits); err != nil || discarded != 0 {
		t.Fatalf("drain = %d bytes, %v", discarded, err)
	}
	if !reflect.DeepEqual(*sizes, []int{512}) {
		t.Fatalf("reads of %v bytes", *sizes)
	}
}

// staleStream is a camera still sending an object of total bytes from an interrupted GetObject, at
// rate bytes per second of the fake clock, then going quiet. pauseAt makes one large read time out
// once (a camera that stalls briefly; usbfs reports no data for it) when that many bytes have gone.
type staleStream struct {
	clock   *fakeClock
	left    int64
	rate    int64
	pauseAt int64
	sent    int64
	paused  bool
}

func (s *staleStream) read(p []byte) (int, error) {
	if s.pauseAt > 0 && !s.paused && s.sent >= s.pauseAt && len(p) > 512 {
		s.paused = true
		s.clock.advance(drainTimeoutMS * time.Millisecond)
		return timedOut()
	}
	if s.left == 0 {
		s.clock.advance(drainTimeoutMS * time.Millisecond)
		return timedOut()
	}
	n := min(int64(len(p)), s.left)
	s.left -= n
	s.sent += n
	s.clock.advance(time.Duration(n) * time.Second / time.Duration(s.rate))
	return int(n), nil
}

// A full-size TIFF left behind by an interrupted GetObject is well past the old 64 MiB bound: drain
// reads all of it, and the next request gets its own answer.
func TestUSBDrainEmptiesALargeStaleObject(t *testing.T) {
	const total = 250 << 20 // over 245 MB
	for _, pause := range []int64{0, 70 << 20} {
		clock := newFakeClock()
		s := &staleStream{clock: clock, left: total, rate: 35 << 20, pauseAt: pause} // USB 2.0 speed
		pipe := &fakePipe{}
		f := usbFramer{pipe: pipe, outMPS: 512, inMPS: 512, clock: clock.now}
		read, sizes := countReads(s.read)
		discarded, err := f.drain(read, defaultDrainLimits)
		if err != nil || discarded != total {
			t.Fatalf("pause at %d: drain = %d bytes, %v", pause, discarded, err)
		}
		// It ends with a large read that times out and a one-packet read that confirms it.
		n := len(*sizes)
		if (*sizes)[n-2] != usbReadSize || (*sizes)[n-1] != 512 {
			t.Fatalf("pause at %d: last reads %v", pause, (*sizes)[n-2:])
		}
		if len(pipe.writes) != 0 {
			t.Fatalf("pause at %d: %d writes while draining", pause, len(pipe.writes))
		}
		pipe.reads = [][]byte{container(ptp.Resp, ptp.RCOK, 0, nil)}
		if code, _, _, err := f.transact(ptp.OpOpenSession, []uint32{1}, nil); err != nil || code != ptp.RCOK {
			t.Fatalf("pause at %d: OpenSession after draining = %#x %v", pause, code, err)
		}
	}
}

// A pipe that never goes quiet stops at the byte budget or the time budget.
func TestUSBDrainStopsAtItsBudget(t *testing.T) {
	for _, tc := range []struct {
		name    string
		size    int           // bytes per read
		perRead time.Duration // clock time per read
	}{
		{"bytes (fast camera)", usbReadSize, 20 * time.Millisecond}, // 1 GiB after 20 s
		{"time (slow camera)", usbReadSize, 100 * time.Millisecond}, // 60 s after 600 MiB
		{"time (zero-length packets)", 0, 100 * time.Millisecond},   // nothing, forever
		{"time (single packets)", 512, 1 * time.Millisecond},        // 60 000 tiny reads
	} {
		clock := newFakeClock()
		calls := 0
		read := func(p []byte) (int, error) {
			calls++
			clock.advance(tc.perRead)
			return min(len(p), tc.size), nil
		}
		f := usbFramer{outMPS: 512, inMPS: 512, clock: clock.now}
		discarded, err := f.drain(read, defaultDrainLimits)
		if !errors.Is(err, errDrainBudget) {
			t.Fatalf("%s: drain = %d bytes, %v", tc.name, discarded, err)
		}
		elapsed := clock.t.Sub(newFakeClock().t)
		if discarded > defaultDrainLimits.bytes+usbReadSize || elapsed > defaultDrainLimits.time+tc.perRead {
			t.Fatalf("%s: drained %d bytes in %v (%d reads)", tc.name, discarded, elapsed, calls)
		}
		if discarded < defaultDrainLimits.bytes && elapsed < defaultDrainLimits.time {
			t.Fatalf("%s: stopped early: %d bytes in %v", tc.name, discarded, elapsed)
		}
	}
}

// clearStale lets the first request through only when the pipe is empty. Otherwise it releases the
// device before any request is sent and reports a camera that is not responding.
func TestUSBClearStale(t *testing.T) {
	broken := errors.New("no such device")
	for _, tc := range []struct {
		name  string
		read  func(clock *fakeClock) func([]byte) (int, error)
		cause error
	}{
		{"empty pipe", func(*fakeClock) func([]byte) (int, error) {
			return func([]byte) (int, error) { return timedOut() }
		}, nil},
		{"budget exhausted", func(c *fakeClock) func([]byte) (int, error) {
			return func(p []byte) (int, error) { c.advance(time.Second); return len(p), nil }
		}, errDrainBudget},
		{"transport error at once", func(*fakeClock) func([]byte) (int, error) {
			return func([]byte) (int, error) { return 0, broken }
		}, broken},
		{"transport error while draining", func(*fakeClock) func([]byte) (int, error) {
			calls := 0
			return func(p []byte) (int, error) {
				if calls++; calls > 3 {
					return 0, broken
				}
				return len(p), nil
			}
		}, broken},
	} {
		clock := newFakeClock()
		pipe := &fakePipe{}
		f := usbFramer{pipe: pipe, outMPS: 512, inMPS: 512, clock: clock.now}
		released := 0
		err := f.clearStale(tc.read(clock), defaultDrainLimits, func() { released++ })
		if len(pipe.writes) != 0 {
			t.Fatalf("%s: %d requests sent", tc.name, len(pipe.writes))
		}
		if tc.cause == nil {
			if err != nil || released != 0 {
				t.Fatalf("%s: err = %v, released %d times", tc.name, err, released)
			}
			continue
		}
		e, ok := errs.As(err)
		if !ok || e.Kind != errs.NotResponding || !errors.Is(err, tc.cause) || released != 1 ||
			e.Message != "the camera is still sending data from an earlier, interrupted command" ||
			e.Hint != "Switch the camera off and on, reconnect, then run the command again." {
			t.Fatalf("%s: err = %v (%#v), released %d times", tc.name, err, err, released)
		}
	}
}

// CloseSession's reply gets closeReplyTimeout in all, not the transport's full timeout per read.
func TestUSBCloseSessionDoesNotWaitLong(t *testing.T) {
	// No reply at all: one short read, which times out.
	clock := newFakeClock()
	pipe := &fakePipe{}
	f := &usbFramer{pipe: pipe, outMPS: 512, inMPS: 512, tid: 4, clock: clock.now}
	if _, _, _, err := f.transact(ptp.OpCloseSession, nil, nil); !errors.Is(err, errBulkTimeout) {
		t.Fatalf("no reply: err = %v", err)
	}
	if pipe.longReads != 0 || !reflect.DeepEqual(pipe.timeouts, []time.Duration{closeReplyTimeout}) {
		t.Fatalf("no reply: %d full-timeout reads, short reads %v", pipe.longReads, pipe.timeouts)
	}

	// A camera that trickles an answer to another transaction: the reads share one deadline.
	clock = newFakeClock()
	trickle := &trickPipe{clock: clock, data: container(ptp.Data, 0x1009, 3, make([]byte, 4096))}
	f = &usbFramer{pipe: trickle, outMPS: 512, inMPS: 512, tid: 4, clock: clock.now}
	start := clock.t
	if _, _, _, err := f.transact(ptp.OpCloseSession, nil, nil); !errors.Is(err, errBulkTimeout) {
		t.Fatalf("trickle: err = %v", err)
	}
	if elapsed := clock.t.Sub(start); elapsed > closeReplyTimeout {
		t.Fatalf("trickle: waited %v", elapsed)
	}
	for i, d := range trickle.timeouts {
		if d <= 0 || d > closeReplyTimeout-time.Duration(i)*time.Second {
			t.Fatalf("trickle: read %d given %v (all: %v)", i, d, trickle.timeouts)
		}
	}

	// A matching reply is taken as before; other requests keep the full timeout.
	pipe = &fakePipe{reads: [][]byte{container(ptp.Resp, ptp.RCOK, 0, nil), container(ptp.Resp, ptp.RCOK, 5, nil)}}
	f = &usbFramer{pipe: pipe, outMPS: 512, inMPS: 512, tid: 4}
	if code, _, _, err := f.transact(ptp.OpCloseSession, nil, nil); err != nil || code != ptp.RCOK {
		t.Fatalf("matching reply: %#x %v", code, err)
	}
	if pipe.longReads != 0 || len(pipe.timeouts) != 2 {
		t.Fatalf("matching reply: %d full-timeout reads, short reads %v", pipe.longReads, pipe.timeouts)
	}
	pipe.reads = [][]byte{container(ptp.Resp, ptp.RCOK, 6, nil)}
	if code, _, _, err := f.transact(ptp.OpGetDeviceInfo, nil, nil); err != nil || code != ptp.RCOK || pipe.longReads != 1 {
		t.Fatalf("next request: %#x %v, %d full-timeout reads", code, err, pipe.longReads)
	}
}

// trickPipe sends data a few bytes per read, a second of the fake clock apiece.
type trickPipe struct {
	fakePipe
	clock *fakeClock
	data  []byte
}

func (p *trickPipe) BulkInTimeout(b []byte, timeout time.Duration) (int, error) {
	p.timeouts = append(p.timeouts, timeout)
	if timeout < time.Second {
		p.clock.advance(timeout)
		return timedOut()
	}
	p.clock.advance(time.Second)
	n := copy(b, p.data[:min(len(p.data), 8)])
	p.data = p.data[n:]
	return n, nil
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
