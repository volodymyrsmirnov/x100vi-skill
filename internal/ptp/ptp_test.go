package ptp_test

import (
	"encoding/binary"
	"strings"
	"testing"

	"github.com/volodymyrsmirnov/x100vi-skill/internal/fakecam"
	"github.com/volodymyrsmirnov/x100vi-skill/internal/ptp"
)

func open(t *testing.T) (*ptp.Session, *fakecam.FakeX100VI) {
	t.Helper()
	fake := fakecam.New()
	session := ptp.NewSession(fake)
	if err := session.Open(); err != nil {
		t.Fatal(err)
	}
	return session, fake
}

// sent counts the transactions with an operation code that reached the camera.
func sent(fake *fakecam.FakeX100VI) map[uint16]int {
	counts := map[uint16]int{}
	fake.Intercept = func(op uint16, params []uint32, data []byte, next func() (uint16, []byte)) (uint16, []byte) {
		counts[op]++
		return next()
	}
	return counts
}

func TestAnObjectIsNeverDownloadedBeforeItIsDescribed(t *testing.T) {
	session, _ := open(t)
	_, _, _, err := session.Transact(ptp.OpGetObject, []uint32{0}, nil)
	if err == nil || !strings.Contains(err.Error(), "before reading its description") {
		t.Fatalf("got %v", err)
	}
	if code, _, _, err := session.Transact(ptp.OpGetObjectInfo, []uint32{0}, nil); err != nil || code != 0x2001 {
		t.Fatalf("GetObjectInfo: 0x%04X %v", code, err)
	}
	if code, _, _, err := session.Transact(ptp.OpGetObject, []uint32{0}, nil); err != nil || code != 0x2001 {
		t.Fatalf("GetObject: 0x%04X %v", code, err)
	}
}

func TestGetObjectNeedsExactlyOneHandle(t *testing.T) {
	session, fake := open(t)
	if _, _, _, err := session.Transact(ptp.OpGetObjectInfo, []uint32{0}, nil); err != nil {
		t.Fatal(err)
	}
	counts := sent(fake)
	for _, params := range [][]uint32{nil, {}, {0, 0}, {0, 1, 2}} {
		if _, _, _, err := session.Transact(ptp.OpGetObject, params, nil); err == nil ||
			!strings.Contains(err.Error(), "instead of one handle") {
			t.Fatalf("%v: got %v", params, err)
		}
	}
	if counts[ptp.OpGetObject] != 0 {
		t.Fatal("a malformed GetObject reached the camera")
	}
}

func TestADeletedObjectMustBeDescribedAgain(t *testing.T) {
	session, fake := open(t)
	fake.Objects[5] = fakecam.Object{Format: 0x3801, Data: []byte("\xff\xd8x")}
	if code, _, _, err := session.Transact(ptp.OpGetObjectInfo, []uint32{5}, nil); err != nil || code != 0x2001 {
		t.Fatalf("0x%04X %v", code, err)
	}
	if _, _, _, err := session.Transact(ptp.OpDeleteObject, []uint32{5}, nil); err != nil {
		t.Fatal(err)
	}
	// the body may reuse the handle for a new object
	fake.Objects[5] = fakecam.Object{Format: 0x3801, Data: []byte("\xff\xd8y")}
	counts := sent(fake)
	if _, _, _, err := session.Transact(ptp.OpGetObject, []uint32{5}, nil); err == nil ||
		!strings.Contains(err.Error(), "before reading its description") {
		t.Fatalf("got %v", err)
	}
	if counts[ptp.OpGetObject] != 0 {
		t.Fatal("GetObject reached the camera")
	}
}

func TestAFailedDescriptionForgetsTheEarlierOne(t *testing.T) {
	session, fake := open(t)
	fake.Objects[5] = fakecam.Object{Format: 0x3801, Data: []byte("\xff\xd8x")}
	if code, _, _, err := session.Transact(ptp.OpGetObjectInfo, []uint32{5}, nil); err != nil || code != 0x2001 {
		t.Fatalf("0x%04X %v", code, err)
	}
	fake.Intercept = func(op uint16, params []uint32, data []byte, next func() (uint16, []byte)) (uint16, []byte) {
		if op == ptp.OpGetObjectInfo {
			return 0x2019, nil // DeviceBusy
		}
		return next()
	}
	if code, _, _, err := session.Transact(ptp.OpGetObjectInfo, []uint32{5}, nil); err != nil || code != 0x2019 {
		t.Fatalf("0x%04X %v", code, err)
	}
	if _, _, _, err := session.Transact(ptp.OpGetObject, []uint32{5}, nil); err == nil ||
		!strings.Contains(err.Error(), "before reading its description") {
		t.Fatalf("got %v", err)
	}
}

func answer(fake *fakecam.FakeX100VI, op uint16, data []byte) {
	fake.Intercept = func(o uint16, params []uint32, d []byte, next func() (uint16, []byte)) (uint16, []byte) {
		if o == op {
			return 0x2001, data
		}
		return next()
	}
}

func TestATruncatedTextValueIsAnError(t *testing.T) {
	session, fake := open(t)
	answer(fake, ptp.OpGetDevicePropValue, []byte{5, 'a', 0})
	if _, err := session.GetString(0xD18D); err == nil || !strings.Contains(err.Error(), "truncated text value") {
		t.Fatalf("got %v", err)
	}
	answer(fake, ptp.OpGetDevicePropValue, ptp.PackString("Street"))
	if got, err := session.GetString(0xD18D); err != nil || got != "Street" {
		t.Fatal(got, err)
	}
}

func TestATruncatedHandleListIsAnError(t *testing.T) {
	session, fake := open(t)
	short := binary.LittleEndian.AppendUint32(nil, 3)
	short = binary.LittleEndian.AppendUint32(short, 7)
	answer(fake, ptp.OpGetObjectHandles, short)
	if _, err := session.Handles(); err == nil || !strings.Contains(err.Error(), "truncated list") {
		t.Fatalf("got %v", err)
	}
	answer(fake, ptp.OpGetObjectHandles, nil)
	if _, err := session.Handles(); err == nil {
		t.Fatal("an empty dataset was taken as an empty list")
	}
}

func TestObjectInfoParsesWholeOrNotAtAll(t *testing.T) {
	session, fake := open(t)
	fake.Objects[9] = fakecam.Object{Format: 0x3801, Data: make([]byte, 1234)}
	d, err := session.Check(ptp.OpGetObjectInfo, []uint32{9}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	info, err := ptp.ParseObjectInfo(d)
	if err != nil || info.Format != 0x3801 || info.Size != 1234 || info.Filename != "DSCF0001.jpg" {
		t.Fatalf("%+v %v", info, err)
	}
	for _, n := range []int{0, 11, 51, 52, len(d) - 1} {
		if _, err := ptp.ParseObjectInfo(d[:n]); err == nil {
			t.Errorf("%d of %d bytes parsed", n, len(d))
		}
	}
}

// A description that does not parse completely does not count: the object stays undownloadable.
func TestATruncatedDescriptionDoesNotCountAsDescribed(t *testing.T) {
	session, fake := open(t)
	fake.Objects[5] = fakecam.Object{Format: 0x3801, Data: []byte("\xff\xd8x")}
	info := make([]byte, 52) // the fixed part, but none of the four strings
	binary.LittleEndian.PutUint16(info[4:], 0x3801)
	answer(fake, ptp.OpGetObjectInfo, info)
	if code, _, _, err := session.Transact(ptp.OpGetObjectInfo, []uint32{5}, nil); err != nil || code != 0x2001 {
		t.Fatalf("0x%04X %v", code, err)
	}
	counts := sent(fake)
	if _, _, _, err := session.Transact(ptp.OpGetObject, []uint32{5}, nil); err == nil ||
		!strings.Contains(err.Error(), "before reading its description") {
		t.Fatalf("got %v", err)
	}
	if counts[ptp.OpGetObject] != 0 {
		t.Fatal("GetObject reached the camera")
	}
}

// Handles belong to a session: opening or closing one forgets every description.
func TestOpeningOrClosingTheSessionForgetsDescriptions(t *testing.T) {
	for _, how := range []string{"close", "open"} {
		t.Run(how, func(t *testing.T) {
			session, fake := open(t)
			fake.Objects[5] = fakecam.Object{Format: 0x3801, Data: []byte("\xff\xd8x")}
			if code, _, _, err := session.Transact(ptp.OpGetObjectInfo, []uint32{5}, nil); err != nil || code != 0x2001 {
				t.Fatalf("0x%04X %v", code, err)
			}
			if how == "close" {
				if err := session.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if err := session.Open(); err != nil {
				t.Fatal(err)
			}
			counts := sent(fake)
			if _, _, _, err := session.Transact(ptp.OpGetObject, []uint32{5}, nil); err == nil ||
				!strings.Contains(err.Error(), "before reading its description") {
				t.Fatalf("got %v", err)
			}
			if counts[ptp.OpGetObject] != 0 {
				t.Fatal("GetObject reached the camera")
			}
		})
	}
}

// OpenSession and CloseSession sent through Check (not only through Open and Close) forget every
// description too: handles belong to a session.
func TestSessionOperationsSentThroughCheckForgetDescriptions(t *testing.T) {
	for _, op := range []uint16{ptp.OpOpenSession, ptp.OpCloseSession} {
		session, fake := open(t)
		if _, err := session.Check(ptp.OpGetObjectInfo, []uint32{0}, nil, "describe"); err != nil {
			t.Fatal(err)
		}
		var params []uint32
		if op == ptp.OpOpenSession {
			params = []uint32{1}
		}
		if _, err := session.Check(op, params, nil, "session"); err != nil {
			t.Fatal(err)
		}
		counts := sent(fake)
		_, err := session.Check(ptp.OpGetObject, []uint32{0}, nil, "download")
		if err == nil || !strings.Contains(err.Error(), "before reading its description") || counts[ptp.OpGetObject] != 0 {
			t.Fatalf("0x%04X: %v, sent %v", op, err, counts)
		}
	}
}
