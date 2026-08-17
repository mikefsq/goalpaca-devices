package ptpcam

import (
	"fmt"

	"github.com/mikefsq/ptp"
)

// Live view, with no transport of its own.
//
// ASCOM has no live-view concept, and the preview geometry (640x480 on an X-T5)
// does not match the sensor, so this cannot go through the image members without
// misdescribing the device. It previously had a plain-HTTP route, which was not
// Alpaca and has been removed; reaching it now needs Action, the standard
// extension point, which this driver does not yet implement.

// LiveFrame returns one preview frame as JPEG, exactly as the camera produced it.
func (c *Camera) LiveFrame() ([]byte, error) {
	lv, ok := c.liveViewer()
	if !ok {
		return nil, fmt.Errorf("ptpcam: this camera has no live view")
	}
	// Starting it is vendor-specific — Fujifilm needs an explicit start, Sony
	// brings the preview up as a side effect of being in a shooting state — so
	// it is reached by assertion rather than through the shared interface.
	if s, ok := c.Body.(interface{ StartLiveView() error }); ok {
		s.StartLiveView()
	}
	frame, err := lv.LiveFrame()
	if err != nil {
		return nil, fmt.Errorf("ptpcam: live view: %w", err)
	}
	if len(frame) == 0 {
		return nil, fmt.Errorf("ptpcam: the camera has no preview frame waiting")
	}
	return frame, nil
}

func (c *Camera) liveViewer() (ptp.LiveViewer, bool) {
	c.mu.Lock()
	body := c.Body
	c.mu.Unlock()
	lv, ok := body.(ptp.LiveViewer)
	return lv, ok
}
