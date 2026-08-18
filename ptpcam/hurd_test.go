package ptpcam

import (
	"testing"

	"github.com/mikefsq/ptp"
	"github.com/mikefsq/ptp/usb"
)

// TestAttachmentPresent: the alive probe's judgement tells continued presence
// from a replug by the attachment identity, and falls back to the vendor and
// serial filter alone where a side has none.
func TestAttachmentPresent(t *testing.T) {
	fuji := uint16(ptp.Fujifilm)
	devs := []usb.DeviceInfo{
		{VID: fuji, Serial: "A1", Attachment: 0x1000},
		{VID: fuji, Serial: "B2"},
	}
	cases := []struct {
		name              string
		serial            string
		open              uint64
		present, replaced bool
	}{
		{"same attachment", "A1", 0x1000, true, false},
		{"replugged", "A1", 0x0fff, false, true},
		{"absent", "C3", 0x1000, false, false},
		{"open side has no attachment", "A1", 0, true, false},
		{"enumeration has no attachment", "B2", 0x0fff, true, false},
		{"any serial, replugged body only", "", 0x0fff, true, false},
	}
	for _, tc := range cases {
		p, r := attachmentPresent(devs, "fuji", tc.serial, tc.open)
		if p != tc.present || r != tc.replaced {
			t.Errorf("%s: present %v replaced %v, want %v %v", tc.name, p, r, tc.present, tc.replaced)
		}
	}
}
