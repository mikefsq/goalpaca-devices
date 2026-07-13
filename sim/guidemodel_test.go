package driver

import (
	"testing"
	"time"
)

// backdate makes the next compute-on-read advance the model by dt seconds.
func backdate(g *guideModel, dt float64) {
	g.mu.Lock()
	g.lastT = time.Now().Add(-time.Duration(dt * float64(time.Second)))
	g.mu.Unlock()
}

// TestTrackingOffDivergesAtSidereal: with tracking off the RA error runs away at
// ~the sidereal rate — far faster than any guide correction.
func TestTrackingOffDivergesAtSidereal(t *testing.T) {
	g := newGuideModel()
	g.tracking = func() bool { return false }
	backdate(g, 1.0)
	errRA, _ := g.offset()
	// 1 s off-tracking ≈ sidereal (15.04) + polar drift (0.3), well above guiding.
	if errRA < 14 || errRA > 17 {
		t.Errorf("tracking-off RA divergence %.2f arcsec/s, want ~15.3", errRA)
	}
}

// TestTrackingOnStaysSmall: while tracking, only drift + periodic error remain — an
// order of magnitude below the sidereal runaway.
func TestTrackingOnStaysSmall(t *testing.T) {
	g := newGuideModel() // default tracking = on
	backdate(g, 1.0)
	errRA, _ := g.offset()
	if errRA < 0 || errRA > 2 {
		t.Errorf("tracking-on RA error %.2f arcsec over 1s, want < 2 (drift+PE only)", errRA)
	}
}

// TestGuidePulseReducesError: a West pulse counters a positive RA error.
func TestGuidePulseReducesError(t *testing.T) {
	g := newGuideModel()
	g.tracking = func() bool { return false }
	backdate(g, 1.0)
	before, _ := g.offset() // ~15 arcsec of accumulated error
	// A 1 s West pulse at 0.5× sidereal removes ~7.5 arcsec — not enough to fully
	// cancel a sidereal divergence, exactly the runaway the mount's guide rate implies.
	g.pulse(-1, 0, 1000)
	after, _ := g.offset()
	if after >= before {
		t.Errorf("West pulse should reduce RA error: %.2f -> %.2f", before, after)
	}
	if before-after < 6 || before-after > 9 {
		t.Errorf("1s pulse at 0.5×sidereal should remove ~7.5 arcsec, removed %.2f", before-after)
	}
}

// TestResetRelocks: a slew/sync zeroes the accumulated error.
func TestResetRelocks(t *testing.T) {
	g := newGuideModel()
	g.tracking = func() bool { return false }
	backdate(g, 5.0)
	if e, _ := g.offset(); e < 50 {
		t.Fatalf("expected large error before reset, got %.2f", e)
	}
	g.reset()
	if e, _ := g.offset(); e > 1 {
		t.Errorf("reset should re-lock near zero, got %.2f", e)
	}
}
