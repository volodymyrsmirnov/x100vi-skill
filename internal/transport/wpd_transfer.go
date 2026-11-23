package transport

// The data-phase loops of the Windows Portable Devices transport, kept free of COM so they are tested on
// every system. wpd_windows.go implements wpdTransfers with IPortableDevice::SendCommand; the loops here
// decide how much to move, when a transfer is complete, and that every transfer context that was started
// is ended (WPD_COMMAND_MTP_EXT_END_DATA_TRANSFER), whatever goes wrong in between.

import (
	"fmt"

	"github.com/volodymyrsmirnov/x100vi-skill/internal/errs"
)

const (
	defaultChunk = 256 * 1024 // transfer size when the driver suggests none

	// The total size EXECUTE_COMMAND_WITH_DATA_TO_READ reports when the driver does not know it.
	wpdUnknownSize64 = 0xFFFFFFFFFFFFFFFF
	wpdUnknownSize32 = 0xFFFFFFFF

	// maxUnknownRead bounds a read of unknown size, so a driver that never sends a short chunk cannot
	// fill memory. The largest object the tool reads (a RAF) is well under 100 MB.
	maxUnknownRead = 1 << 31
)

// wpdTransfers are the vendor-extension commands a data phase is made of. what names the request for
// error messages. Errors are returned ready for the user (errs.Error).
type wpdTransfers interface {
	// startRead sends EXECUTE_COMMAND_WITH_DATA_TO_READ and returns the transfer context, the chunk size
	// the driver suggests (0: none) and the total size of the data phase. If the driver opened a context,
	// ctx is not empty even when err is not nil, and the caller must end it.
	startRead(what string, op uint16, params []uint32) (ctx string, chunk uint32, total uint64, err error)
	// readChunk sends READ_DATA for up to want bytes. It returns the buffer the driver handed back and
	// NUM_BYTES_READ, or n < 0 when the driver did not report it.
	readChunk(what, ctx string, want uint32) (buf []byte, n int, err error)
	// startWrite sends EXECUTE_COMMAND_WITH_DATA_TO_WRITE for total bytes, with the same contract as
	// startRead.
	startWrite(what string, op uint16, params []uint32, total uint64) (ctx string, chunk uint32, err error)
	// writeChunk sends WRITE_DATA and returns NUM_BYTES_WRITTEN.
	writeChunk(what, ctx string, piece []byte) (written uint32, err error)
	// endTransfer sends END_DATA_TRANSFER and returns the PTP response code.
	endTransfer(what, ctx string) (code uint16, err error)
}

// wpdUnknownSize reports whether a total size from the driver means "not known".
func wpdUnknownSize(total uint64) bool {
	return total == wpdUnknownSize64 || total == wpdUnknownSize32
}

// wpdRead runs a request with an incoming data phase and returns the response code and the data.
func wpdRead(c wpdTransfers, what string, op uint16, params []uint32) (uint16, []byte, error) {
	ctx, chunk, total, err := c.startRead(what, op, params)
	if err != nil {
		return 0, nil, wpdAbort(c, what, ctx, err)
	}
	if chunk == 0 {
		chunk = defaultChunk
	}
	unknown := wpdUnknownSize(total)
	var data []byte
	if !unknown && total < 1<<31 {
		data = make([]byte, 0, total)
	}
	for unknown || uint64(len(data)) < total {
		want := chunk
		if !unknown {
			want = uint32(min(uint64(chunk), total-uint64(len(data))))
		} else if uint64(len(data)) >= maxUnknownRead {
			return 0, nil, wpdAbort(c, what, ctx, errs.NotRespondingErr(fmt.Sprintf(
				"Windows kept sending data for %s past %d bytes", what, len(data))))
		}
		buf, n, err := c.readChunk(what, ctx, want)
		if err != nil {
			return 0, nil, wpdAbort(c, what, ctx, err)
		}
		// The buffer may keep the requested size; NUM_BYTES_READ says how much of it is data.
		if n >= 0 {
			if n > len(buf) {
				return 0, nil, wpdAbort(c, what, ctx, errs.NotRespondingErr(fmt.Sprintf(
					"Windows reported %d bytes read during %s but returned %d", n, what, len(buf))))
			}
			buf = buf[:n]
		}
		data = append(data, buf...)
		if unknown {
			if len(buf) < int(want) {
				break // a short or empty chunk ends a transfer of unknown size
			}
			continue
		}
		if len(buf) == 0 {
			return 0, nil, wpdAbort(c, what, ctx, errs.NotRespondingErr(fmt.Sprintf(
				"the data of %s ended after %d of %d bytes", what, len(data), total)))
		}
	}
	code, err := c.endTransfer(what, ctx)
	return code, data, err
}

// wpdWrite runs a request with an outgoing data phase and returns the response code.
func wpdWrite(c wpdTransfers, what string, op uint16, params []uint32, data []byte) (uint16, error) {
	ctx, chunk, err := c.startWrite(what, op, params, uint64(len(data)))
	if err != nil {
		return 0, wpdAbort(c, what, ctx, err)
	}
	if chunk == 0 {
		chunk = defaultChunk
	}
	for pos := 0; pos < len(data); {
		piece := data[pos:min(pos+int(chunk), len(data))]
		written, err := c.writeChunk(what, ctx, piece)
		if err != nil {
			return 0, wpdAbort(c, what, ctx, err)
		}
		if written == 0 || int(written) > len(piece) {
			return 0, wpdAbort(c, what, ctx, errs.NotRespondingErr(fmt.Sprintf(
				"Windows accepted %d of %d bytes during %s (%d of %d sent so far)", written, len(piece), what, pos, len(data))))
		}
		pos += int(written) // a short write is resent from where it stopped
	}
	return c.endTransfer(what, ctx)
}

// wpdAbort ends the transfer context ctx, if one was started, after cause made the transfer fail. It
// returns cause, mentioning a failure to end the transfer without letting it replace cause.
func wpdAbort(c wpdTransfers, what, ctx string, cause error) error {
	if ctx == "" {
		return cause
	}
	_, endErr := c.endTransfer(what, ctx)
	if endErr == nil {
		return cause
	}
	if e, ok := errs.As(cause); ok {
		wrapped := errs.Wrap(e.Kind, cause, fmt.Sprintf("%s (ending the transfer also failed: %v)", cause, endErr), e.Hint)
		wrapped.Code = e.Code
		return wrapped
	}
	return fmt.Errorf("%w (ending the transfer also failed: %v)", cause, endErr)
}
