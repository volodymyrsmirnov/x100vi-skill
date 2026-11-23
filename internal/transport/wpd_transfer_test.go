package transport

import (
	"bytes"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/volodymyrsmirnov/x100vi-skill/internal/errs"
	"github.com/volodymyrsmirnov/x100vi-skill/internal/ptp"
)

// fakeWPD is a Windows Portable Devices driver for the data-phase loops. Reads answer from chunks; a
// chunk longer than requested is clipped, and pad makes the returned buffer keep the requested size
// with NUM_BYTES_READ telling how much of it is data, as the real driver may.
type fakeWPD struct {
	total     uint64
	chunk     uint32
	chunks    [][]byte
	pad       bool
	noCountAt int // NUM_BYTES_READ missing from this read (1-based; 0: never)
	overCount int // NUM_BYTES_READ larger than the buffer, from this read on (1-based; 0: never)
	overWant  int // a buffer and NUM_BYTES_READ larger than requested, at this read (1-based; 0: never)

	startCtx   string
	startErr   error
	readErrAt  int // fail this read (1-based; 0: never)
	writeErrAt int
	written    func(piece []byte) uint32
	endErr     error

	wants  []uint32
	reads  int
	writes [][]byte
	ends   []string
}

func (f *fakeWPD) startRead(what string, op uint16, params []uint32) (string, uint32, uint64, error) {
	return f.startCtx, f.chunk, f.total, f.startErr
}

func (f *fakeWPD) readChunk(what, ctx string, want uint32) ([]byte, uint32, error) {
	f.reads++
	f.wants = append(f.wants, want)
	if f.reads == f.readErrAt {
		return nil, 0, errs.NotRespondingErr("read failed")
	}
	var part []byte
	if len(f.chunks) > 0 {
		part, f.chunks = f.chunks[0], f.chunks[1:]
	}
	part = part[:min(len(part), int(want))]
	buf := part
	if f.pad {
		buf = make([]byte, want)
		copy(buf, part)
	}
	if f.reads == f.noCountAt {
		// What wpdTransport.readChunk does when the results lack the count.
		return nil, 0, errNoByteCount(what, errors.New("element not found"))
	}
	n := uint32(len(part))
	if f.overCount > 0 && f.reads >= f.overCount {
		n = uint32(len(buf) + 1)
	}
	if f.reads == f.overWant {
		// A buffer that holds more than was asked for, all of it claimed as data.
		buf = append(append([]byte(nil), buf...), make([]byte, int(want)+5-len(buf))...)
		n = uint32(len(buf))
	}
	return buf, n, nil
}

func (f *fakeWPD) startWrite(what string, op uint16, params []uint32, total uint64) (string, uint32, error) {
	f.total = total
	return f.startCtx, f.chunk, f.startErr
}

func (f *fakeWPD) writeChunk(what, ctx string, piece []byte) (uint32, error) {
	f.writes = append(f.writes, append([]byte(nil), piece...))
	if len(f.writes) == f.writeErrAt {
		return 0, errs.NotRespondingErr("write failed")
	}
	if f.written != nil {
		return f.written(piece), nil
	}
	return uint32(len(piece)), nil
}

func (f *fakeWPD) endTransfer(what, ctx string) (uint16, error) {
	f.ends = append(f.ends, ctx)
	if f.endErr != nil {
		return 0, f.endErr
	}
	return ptp.RCOK, nil
}

func pattern(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i * 7)
	}
	return b
}

// split cuts b into pieces of size n.
func split(b []byte, n int) [][]byte {
	var out [][]byte
	for len(b) > n {
		out, b = append(out, b[:n]), b[n:]
	}
	return append(out, b)
}

func checkEndedOnce(t *testing.T, f *fakeWPD) {
	t.Helper()
	if len(f.ends) != 1 || f.ends[0] != f.startCtx {
		t.Fatalf("END_DATA_TRANSFER sent for %q, want once for %q", f.ends, f.startCtx)
	}
}

func TestWPDReadOfKnownSize(t *testing.T) {
	payload := pattern(2500)
	for _, tc := range []struct {
		name string
		pad  bool
	}{{"exact buffers", false}, {"padded buffers", true}} {
		f := &fakeWPD{startCtx: "ctx", total: uint64(len(payload)), chunk: 1000, chunks: split(payload, 1000),
			pad: tc.pad}
		code, data, err := wpdRead(f, "request 0x1009", 0x1009, []uint32{1})
		if err != nil || code != ptp.RCOK || !bytes.Equal(data, payload) {
			t.Fatalf("%s: %#x, %d bytes, %v", tc.name, code, len(data), err)
		}
		if want := []uint32{1000, 1000, 500}; !slices.Equal(f.wants, want) {
			t.Errorf("%s: asked for %v, want %v", tc.name, f.wants, want)
		}
		checkEndedOnce(t, f)
	}
}

func TestWPDReadToleratesShortChunksOfAKnownSize(t *testing.T) {
	payload := pattern(1000)
	f := &fakeWPD{startCtx: "ctx", total: 1000, chunk: 1000, chunks: split(payload, 300), pad: true}
	_, data, err := wpdRead(f, "request 0x1009", 0x1009, nil)
	if err != nil || !bytes.Equal(data, payload) {
		t.Fatalf("%d bytes, %v", len(data), err)
	}
	if want := []uint32{1000, 700, 400, 100}; !slices.Equal(f.wants, want) {
		t.Errorf("asked for %v, want %v", f.wants, want)
	}
}

func TestWPDReadOfZeroBytesReadsNothing(t *testing.T) {
	f := &fakeWPD{startCtx: "ctx", total: 0}
	code, data, err := wpdRead(f, "request 0x1015", 0x1015, nil)
	if err != nil || code != ptp.RCOK || len(data) != 0 || f.reads != 0 {
		t.Fatalf("%#x %d bytes %v, %d reads", code, len(data), err, f.reads)
	}
	checkEndedOnce(t, f)
}

func TestWPDReadOfUnknownSizeStopsAtAShortChunk(t *testing.T) {
	for _, total := range []uint64{0xFFFFFFFFFFFFFFFF, 0xFFFFFFFF} {
		for _, tc := range []struct {
			name   string
			chunks [][]byte
			want   []byte
			reads  int
		}{
			{"short last chunk", split(pattern(2500), 1000), pattern(2500), 3},
			{"empty last chunk", append(split(pattern(2000), 1000), []byte{}), pattern(2000), 3},
			{"empty at once", [][]byte{{}}, nil, 1},
		} {
			for _, pad := range []bool{false, true} {
				f := &fakeWPD{startCtx: "ctx", total: total, chunk: 1000, chunks: tc.chunks, pad: pad}
				code, data, err := wpdRead(f, "request 0x1009", 0x1009, nil)
				if err != nil || code != ptp.RCOK || !bytes.Equal(data, tc.want) || f.reads != tc.reads {
					t.Fatalf("total %#x, %s, pad %v: %#x %d bytes %v after %d reads", total, tc.name, pad, code,
						len(data), err, f.reads)
				}
				for _, w := range f.wants {
					if w != 1000 {
						t.Fatalf("total %#x, %s: asked for %v", total, tc.name, f.wants)
					}
				}
				checkEndedOnce(t, f)
			}
		}
	}
}

func TestWPDReadOfKnownSizeFailsWhenTheDataStops(t *testing.T) {
	f := &fakeWPD{startCtx: "ctx", total: 2500, chunk: 1000, chunks: [][]byte{pattern(1000), {}}}
	_, data, err := wpdRead(f, "request 0x1009", 0x1009, nil)
	if !errs.Is(err, errs.NotResponding) || !strings.Contains(err.Error(), "1000 of 2500") || data != nil {
		t.Fatalf("err = %v, %d bytes", err, len(data))
	}
	checkEndedOnce(t, f)
}

func TestWPDReadEndsTheTransferOnEveryFailure(t *testing.T) {
	sizeErr := errs.NotRespondingErr("size query failed")
	for _, tc := range []struct {
		name string
		f    *fakeWPD
		want string
	}{
		{"size query", &fakeWPD{startCtx: "ctx", startErr: sizeErr}, "size query failed"},
		{"chunk", &fakeWPD{startCtx: "ctx", total: 3000, chunk: 1000, chunks: split(pattern(3000), 1000), readErrAt: 2},
			"read failed"},
		{"byte count", &fakeWPD{startCtx: "ctx", total: 3000, chunk: 1000, chunks: split(pattern(3000), 1000), overCount: 2},
			"1001 bytes read"},
		{"byte count past the request", &fakeWPD{startCtx: "ctx", total: 3000, chunk: 1000,
			chunks: split(pattern(3000), 1000), overWant: 2}, "1005 bytes read during request 0x1009, more than the 1000"},
		{"byte count past the request, last chunk", &fakeWPD{startCtx: "ctx", total: 2500, chunk: 1000,
			chunks: split(pattern(2500), 1000), pad: true, overWant: 3}, "505 bytes read during request 0x1009, more than the 500"},
		{"byte count past the request, unknown size", &fakeWPD{startCtx: "ctx", total: wpdUnknownSize64, chunk: 1000,
			chunks: split(pattern(3000), 1000), overWant: 1}, "more than the 1000 asked for"},
		{"unknown size chunk", &fakeWPD{startCtx: "ctx", total: wpdUnknownSize64, chunk: 1000,
			chunks: split(pattern(3000), 1000), readErrAt: 3}, "read failed"},
		{"no byte count", &fakeWPD{startCtx: "ctx", total: 3000, chunk: 1000, chunks: split(pattern(3000), 1000),
			noCountAt: 2}, "did not report how many bytes it read"},
		{"no byte count, unknown size", &fakeWPD{startCtx: "ctx", total: wpdUnknownSize64, chunk: 1000,
			chunks: split(pattern(3000), 1000), pad: true, noCountAt: 1}, "did not report how many bytes it read"},
	} {
		_, data, err := wpdRead(tc.f, "request 0x1009", 0x1009, nil)
		if !errs.Is(err, errs.NotResponding) || !strings.Contains(err.Error(), tc.want) || data != nil {
			t.Errorf("%s: err = %v, %d bytes", tc.name, err, len(data))
			continue
		}
		checkEndedOnce(t, tc.f)
	}
}

// A READ_DATA without NUM_BYTES_READ fails the request: the buffer may keep the requested size, so
// its length says nothing about how much of it is data.
func TestWPDReadWithoutAByteCountFails(t *testing.T) {
	f := &fakeWPD{startCtx: "ctx", total: 2500, chunk: 1000, chunks: split(pattern(2500), 1000), pad: true,
		noCountAt: 1}
	code, data, err := wpdRead(f, "request 0x1009", 0x1009, nil)
	e, ok := errs.As(err)
	if !ok || e.Kind != errs.NotResponding || e.Hint == "" || code != 0 || data != nil ||
		!strings.Contains(err.Error(), "request 0x1009") || !strings.Contains(err.Error(), "element not found") {
		t.Fatalf("%#x, %d bytes, err = %v", code, len(data), err)
	}
	if f.reads != 1 {
		t.Fatalf("%d reads after the failed one, want none", f.reads-1)
	}
	checkEndedOnce(t, f)
}

func TestWPDReadThatNeverStartedEndsNothing(t *testing.T) {
	f := &fakeWPD{startErr: errs.NotRespondingErr("refused")}
	if _, _, err := wpdRead(f, "request 0x1009", 0x1009, nil); err != f.startErr {
		t.Fatalf("err = %v", err)
	}
	if len(f.ends) != 0 {
		t.Fatalf("ended %v", f.ends)
	}
}

func TestWPDReadKeepsTheOriginalErrorWhenEndingFails(t *testing.T) {
	f := &fakeWPD{startCtx: "ctx", total: 2000, chunk: 1000, chunks: split(pattern(2000), 1000), readErrAt: 1,
		endErr: errors.New("device gone")}
	_, _, err := wpdRead(f, "request 0x1009", 0x1009, nil)
	e, ok := errs.As(err)
	if !ok || e.Kind != errs.NotResponding || e.Hint == "" ||
		!strings.HasPrefix(err.Error(), "read failed") || !strings.Contains(err.Error(), "device gone") {
		t.Fatalf("err = %v (%#v)", err, err)
	}
	checkEndedOnce(t, f)
}

func TestWPDWriteSendsEverythingAndResendsShortWrites(t *testing.T) {
	payload := pattern(2500)
	f := &fakeWPD{startCtx: "ctx", chunk: 1000, written: func(p []byte) uint32 { return uint32(min(len(p), 600)) }}
	code, err := wpdWrite(f, "request 0x900D", 0x900D, nil, payload)
	if err != nil || code != ptp.RCOK || f.total != 2500 {
		t.Fatalf("%#x %v, total %d", code, err, f.total)
	}
	var sent []byte
	for _, w := range f.writes {
		sent = append(sent, w[:min(len(w), 600)]...)
	}
	if !bytes.Equal(sent, payload) {
		t.Fatalf("sent %d bytes in %d writes", len(sent), len(f.writes))
	}
	checkEndedOnce(t, f)
}

func TestWPDWriteEndsTheTransferOnEveryFailure(t *testing.T) {
	for _, tc := range []struct {
		name string
		f    *fakeWPD
		want string
	}{
		{"start", &fakeWPD{startCtx: "ctx", startErr: errs.NotRespondingErr("start failed")}, "start failed"},
		{"chunk", &fakeWPD{startCtx: "ctx", chunk: 1000, writeErrAt: 2}, "write failed"},
		{"nothing written", &fakeWPD{startCtx: "ctx", chunk: 1000, written: func([]byte) uint32 { return 0 }},
			"accepted 0 of 1000"},
		{"too much written", &fakeWPD{startCtx: "ctx", chunk: 1000, written: func(p []byte) uint32 { return uint32(len(p) + 1) }},
			"accepted 1001 of 1000"},
	} {
		_, err := wpdWrite(tc.f, "request 0x900D", 0x900D, nil, pattern(2500))
		if !errs.Is(err, errs.NotResponding) || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v", tc.name, err)
			continue
		}
		checkEndedOnce(t, tc.f)
	}
}

func TestWPDWriteKeepsTheOriginalErrorWhenEndingFails(t *testing.T) {
	f := &fakeWPD{startCtx: "ctx", chunk: 1000, writeErrAt: 1, endErr: errs.NotRespondingErr("end failed")}
	_, err := wpdWrite(f, "request 0x900D", 0x900D, nil, pattern(10))
	if !errs.Is(err, errs.NotResponding) || !strings.HasPrefix(err.Error(), "write failed") ||
		!strings.Contains(err.Error(), "end failed") {
		t.Fatalf("err = %v", err)
	}
	checkEndedOnce(t, f)
}

func TestWPDAbortWithoutAContextOrAnErrsCause(t *testing.T) {
	f := &fakeWPD{endErr: errors.New("end failed")}
	cause := errors.New("plain")
	if err := wpdAbort(f, "x", "", cause); err != cause || len(f.ends) != 0 {
		t.Fatalf("no context: %v, ended %v", err, f.ends)
	}
	err := wpdAbort(f, "x", "ctx", cause)
	if !errors.Is(err, cause) || !strings.Contains(err.Error(), "end failed") {
		t.Fatalf("plain cause: %v", err)
	}
}
