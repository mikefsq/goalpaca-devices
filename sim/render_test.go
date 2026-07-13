package driver

import (
	"encoding/binary"
	"testing"
)

// brightestPixel returns the peak ADU in a 16-bit mono frame.
func brightestPixel(buf []byte) uint16 {
	var m uint16
	for i := 0; i+1 < len(buf); i += 2 {
		if v := binary.LittleEndian.Uint16(buf[i:]); v > m {
			m = v
		}
	}
	return m
}

// TestRenderTileHasStars: a fresh frame (zero offset) shows the bright anchor star
// at centre plus background elsewhere.
func TestRenderTileHasStars(t *testing.T) {
	w, h := baseW, baseH
	buf := renderTile(w, h, 0, 0, w, h, 0, 0, 0, 0, nil)
	center := binary.LittleEndian.Uint16(buf[((h/2)*w+w/2)*2:])
	if center < 30000 {
		t.Errorf("anchor star missing at centre: %d ADU", center)
	}
	if corner := binary.LittleEndian.Uint16(buf[0:]); corner > starBG+100 {
		t.Errorf("background too bright: %d", corner)
	}
}

// TestToroidalWrapKeepsStars: even after the field drifts by many frame widths (as
// with tracking off), the window still shows stars — it wraps, never goes empty.
func TestToroidalWrapKeepsStars(t *testing.T) {
	w, h := baseW, baseH
	for _, win := range []float64{0, 2000, tileW - 10, tileW + 500, 5 * tileW} {
		buf := renderTile(w, h, 0, 0, w, h, win, 0, 0, 0, nil)
		if peak := brightestPixel(buf); peak < 5000 {
			t.Errorf("window at winX=%.0f is nearly empty (peak %d); wrap failed", win, peak)
		}
	}
}

// TestCameraProjectsPointingError: as the mount's pointing error grows (tracking
// off), the sim camera's star field shifts across the sensor.
func TestCameraProjectsPointingError(t *testing.T) {
	cam := newSimCamera("t", 200, 0, 0, 0, 0)
	// Frame the full sensor at two different pointing errors and confirm the field
	// content differs (stars moved). Drive the shared sky directly for determinism.
	sky.reset()
	sky.tracking = func() bool { return true }
	a := cam.renderFrame(0, 0, baseW, baseH)
	sky.mu.Lock()
	sky.errRA = 300 // arcsec ≈ 50px at ~6"/px
	sky.mu.Unlock()
	b := cam.renderFrame(0, 0, baseW, baseH)
	same := true
	for i := range a {
		if a[i] != b[i] {
			same = false
			break
		}
	}
	if same {
		t.Error("star field did not move when pointing error changed")
	}
}
