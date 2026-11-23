// Package transport connects to the camera. Open picks the operating system's route at build time:
//
//   - macOS: ImageCaptureCore (icc_darwin.go). ptpcamerad owns the camera and its PTP session; requests
//     go through ICCameraDevice requestSendPTPCommand:outData:completion:. The transport must be used
//     from the main goroutine, which this package locks to the main OS thread.
//   - Windows: Windows Portable Devices through COM (wpd_windows.go), with the inbox MTP/PTP driver.
//   - Linux: usbfs (usbfs_linux.go), claiming the camera's still-image interface directly.
//
// This file holds the PTP container framing shared by the transports.
package transport

import (
	"encoding/binary"
	"time"

	"github.com/volodymyrsmirnov/x100vi-skill/internal/ptp"
)

// headerLen is the size of a PTP container header: length, type, code, transaction id.
const headerLen = 12

// Timeouts of one transaction through the operating system's camera service.
const (
	normalTimeout = 30 * time.Second
	slowTimeout   = 300 * time.Second // whole-file downloads and uploads
)

// slowOp reports whether a request moves a whole file (GetObject, the vendor SendObject and SendObject).
func slowOp(op uint16) bool {
	return op == ptp.OpGetObject || op == ptp.OpVendorSendObject || op == ptp.OpSendObject
}

// opTimeout is how long to wait for the answer to a request.
func opTimeout(op uint16) time.Duration {
	if slowOp(op) {
		return slowTimeout
	}
	return normalTimeout
}

// containerHeader builds a container header announcing total bytes (header included).
func containerHeader(total uint32, ctype, code uint16, tid uint32) []byte {
	b := make([]byte, headerLen)
	binary.LittleEndian.PutUint32(b[0:], total)
	binary.LittleEndian.PutUint16(b[4:], ctype)
	binary.LittleEndian.PutUint16(b[6:], code)
	binary.LittleEndian.PutUint32(b[8:], tid)
	return b
}

// commandContainer builds a complete PTP command container.
func commandContainer(op uint16, tid uint32, params []uint32) []byte {
	b := containerHeader(uint32(headerLen+4*len(params)), ptp.Cmd, op, tid)
	for _, p := range params {
		b = binary.LittleEndian.AppendUint32(b, p)
	}
	return b
}

// header is a parsed container header.
type header struct {
	length uint32
	ctype  uint16
	code   uint16
	tid    uint32
}

// parseHeader reads a container header; ok is false when b is shorter than a header.
func parseHeader(b []byte) (h header, ok bool) {
	if len(b) < headerLen {
		return header{}, false
	}
	return header{
		length: binary.LittleEndian.Uint32(b[0:]),
		ctype:  binary.LittleEndian.Uint16(b[4:]),
		code:   binary.LittleEndian.Uint16(b[6:]),
		tid:    binary.LittleEndian.Uint32(b[8:]),
	}, true
}

// containerBody is the payload of a container: the bytes after the header up to its announced
// length, clipped to what was received.
func containerBody(b []byte, length uint32) []byte {
	end := len(b)
	if uint64(length) < uint64(end) {
		end = int(length)
	}
	if end < headerLen {
		end = headerLen
	}
	if end > len(b) {
		return nil
	}
	return b[headerLen:end]
}

// responseParams decodes the parameters of a response container body (whole 32-bit words only).
func responseParams(body []byte) []uint32 {
	n := len(body) / 4
	out := make([]uint32, n)
	for i := range out {
		out[i] = binary.LittleEndian.Uint32(body[4*i:])
	}
	return out
}
