// Package driver provides the simulated ASCOM Alpaca devices (sim-*) for a
// no-hardware herd: a full set of standalone device sims plus a coupled guide
// loop (sim-telescope + sim-camera sharing one simulated sky) so PHD2 can
// calibrate and guide against them.
package driver

import (
	"math"
	"sync"
	"time"
)

// sky is the single physical source of truth for the simulated closed guide loop:
// the mount's accumulated pointing error (arc-seconds) relative to where the guide
// star was locked. It is driven by the mount's own state — the sky rotating past a
// mount that isn't tracking, periodic error, and polar-misalignment drift — and
// reduced by guide pulses at the mount's guide rate. The camera projects this error
// onto the sensor and the mount reports it as an RA/Dec offset, so the reported
// pointing and the guide-star image are the same quantity and can never disagree.
//
// Package-level because the sim mount and sim camera are separate registry devices
// that must share one sky.
var sky = newGuideModel()

// siderealArcsecPerSec is the sky's apparent rotation rate on the RA axis. With
// tracking off the mount holds a fixed hour angle while the sky turns, so the star
// (and the reported RA) diverge at this rate — far faster than any guide correction.
const siderealArcsecPerSec = 15.041

type guideModel struct {
	mu sync.Mutex

	// Pointing error (arc-seconds): current pointing minus the locked pointing.
	// errRA > 0 means the mount lags the star eastward; the camera renders the star
	// displaced by +errRA (scaled to px) and the mount reports RA reduced by errRA.
	errRA, errDec float64
	lastT         time.Time
	peClock       float64 // seconds of *tracked* time, for the periodic-error phase

	// Mount coupling, read live (compute-on-read, mirroring sim.Telescope's own
	// settle model). Defaults assume tracking on at half-sidereal guiding until a
	// sim mount binds, so a camera-only herd still shows a gently drifting star.
	tracking  func() bool
	guideRate func() (ra, dec float64) // arcsec/s

	// Residual tracking errors present even while tracking well:
	driftRA, driftDec float64 // polar-misalignment drift, arcsec/s
	peAmp, pePeriod   float64 // periodic error peAmp·sin(2π·peClock/pePeriod), arcsec / s
}

func newGuideModel() *guideModel {
	return &guideModel{
		lastT:     time.Now(),
		tracking:  func() bool { return true },
		guideRate: func() (float64, float64) { return 0.5 * siderealArcsecPerSec, 0.5 * siderealArcsecPerSec },
		driftRA:   0.30, // arcsec/s — gentle polar-alignment drift for PHD2 to trend out
		driftDec:  0.10,
		peAmp:     4.0, // arcsec peak periodic error (something to correct)
		pePeriod:  480, // s worm period
	}
}

// bind wires the model to a mount's live state. Called when the sim mount is
// constructed; until then the defaults apply.
func (g *guideModel) bind(tracking func() bool, guideRate func() (float64, float64)) {
	g.mu.Lock()
	g.tracking, g.guideRate = tracking, guideRate
	g.mu.Unlock()
}

// advanceLocked integrates the pointing error to now. Caller holds mu.
func (g *guideModel) advanceLocked() {
	now := time.Now()
	dt := now.Sub(g.lastT).Seconds()
	g.lastT = now
	if dt <= 0 {
		return
	}
	tracking := g.tracking()
	// The sky rotates past the mount at the sidereal rate; tracking cancels it. With
	// tracking off the full rate leaks into the RA error — the runaway divergence.
	if !tracking {
		g.errRA += siderealArcsecPerSec * dt
	}
	// Polar-misalignment drift is present whether or not the RA drive is running.
	g.errRA += g.driftRA * dt
	g.errDec += g.driftDec * dt
	// Periodic error is a worm-gear artefact: it only advances while tracking, and
	// enters as the derivative of peAmp·sin(2π t/P) so guide pulses can fight it.
	if tracking && g.pePeriod > 0 {
		g.peClock += dt
		w := 2 * math.Pi / g.pePeriod
		g.errRA += g.peAmp * w * math.Cos(w*g.peClock) * dt
	}
}

// offset advances the model and returns the current pointing error (arcsec).
func (g *guideModel) offset() (ra, dec float64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.advanceLocked()
	return g.errRA, g.errDec
}

// pulse applies a guide correction: the mount slews at its guide rate for ms in the
// given axis, reducing the pointing error toward the lock. West/South counter a
// positive RA/Dec error.
func (g *guideModel) pulse(raSign, decSign float64, ms int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.advanceLocked()
	raRate, decRate := g.guideRate()
	d := float64(ms) / 1000
	g.errRA += raSign * raRate * d
	g.errDec += decSign * decRate * d
}

// reset re-locks at the current pointing (zero error). The mount calls this on a
// slew/sync so a new target starts centred — which also recentres the star field.
func (g *guideModel) reset() {
	g.mu.Lock()
	g.errRA, g.errDec, g.peClock = 0, 0, 0
	g.lastT = time.Now()
	g.mu.Unlock()
}
