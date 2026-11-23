package ptp_test

import (
	"strings"
	"testing"

	"github.com/volodymyrsmirnov/x100vi-skill/internal/fakecam"
	"github.com/volodymyrsmirnov/x100vi-skill/internal/ptp"
)

func TestAnObjectIsNeverDownloadedBeforeItIsDescribed(t *testing.T) {
	fake := fakecam.New()
	session := ptp.NewSession(fake)
	if err := session.Open(); err != nil {
		t.Fatal(err)
	}
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
