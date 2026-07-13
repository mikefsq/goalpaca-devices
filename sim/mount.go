package driver

import (
	"math"
	"sync"

	alpacadev "github.com/mikefsq/goalpaca/server"
	"github.com/mikefsq/goalpaca/sim"
	"github.com/mikefsq/lx200"
)

// simMount is the Alpaca telescope simulator (goalpaca/sim) coupled to the shared
// guide model: the mount owns the pointing error, so its reported RA/Dec and the sim
// camera's star image are the same quantity. It also exposes a LiveMount seam so the
// same simulated mount drives the fleet's INDI and LX200 front-ends.
type simMount struct {
	*sim.Telescope
	lx     *simMountAdapter
	optics alpacadev.OpticsStore
}

func newSimMount(name string) *simMount {
	st := sim.NewTelescope()
	if name != "" {
		st.DevName = name
	}
	// Start pointed at mid-sky and tracking, off the degenerate pole where PHD2's
	// calibration warns. Sync establishes a target so RA/Dec read cleanly.
	if err := st.SetTargetRightAscension(6.0); err == nil {
		if err := st.SetTargetDeclination(20.0); err == nil {
			_ = st.SyncToTarget()
		}
	}
	_ = st.SetTracking(true)

	// Bind the shared sky to this mount's live state: whether it's tracking, and its
	// guide rate (goalpaca/sim stores guide rate in deg/s; the model wants arcsec/s).
	sky.bind(
		st.Tracking,
		func() (float64, float64) {
			return st.GuideRateRightAscension() * 3600, st.GuideRateDeclination() * 3600
		},
	)
	sky.reset()

	return &simMount{Telescope: st, lx: &simMountAdapter{t: st}}
}

// RightAscension / Declination report the base pointing minus the accumulated guide
// error, so turning tracking off makes the RA run backwards at the sidereal rate —
// the same divergence the camera shows as the star racing across the sensor.
func (s *simMount) RightAscension() float64 {
	e, _ := sky.offset()
	return wrap24h(s.Telescope.RightAscension() - e/54000.0) // arcsec → hours (/3600/15)
}

func (s *simMount) Declination() float64 {
	_, e := sky.offset()
	return s.Telescope.Declination() - e/3600.0 // arcsec → deg
}

// PulseGuide is the single Alpaca guide path: it corrects the shared pointing error
// at the mount's guide rate and advances the underlying sim.Telescope state. Without
// this the star would keep drifting no matter what PHD2 commands over Alpaca.
func (s *simMount) PulseGuide(d alpacadev.GuideDirection, ms int) error {
	pulseSkyAlpaca(d, ms)
	return s.Telescope.PulseGuide(d, ms)
}

// Slewing/syncing establishes a fresh lock, so the guide error resets to zero — which
// also recentres the wrapping star field on the new target.
func (s *simMount) SlewToCoordinatesAsync(ra, dec float64) error {
	err := s.Telescope.SlewToCoordinatesAsync(ra, dec)
	sky.reset()
	return err
}
func (s *simMount) SlewToTargetAsync() error {
	err := s.Telescope.SlewToTargetAsync()
	sky.reset()
	return err
}
func (s *simMount) SyncToCoordinates(ra, dec float64) error {
	err := s.Telescope.SyncToCoordinates(ra, dec)
	sky.reset()
	return err
}
func (s *simMount) SyncToTarget() error {
	err := s.Telescope.SyncToTarget()
	sky.reset()
	return err
}

// LiveMount exposes the simulator as a lx200.Mount for the INDI server and LX200
// bridge — the liveMounter seam the fleet wires those front-ends onto.
func (s *simMount) LiveMount() (lx200.Mount, error) { return s.lx, nil }

// UseOptics + the optics getters let the sim report a configured optical train over
// both Alpaca (ApertureDiameter/FocalLength) and INDI (TELESCOPE_INFO).
func (s *simMount) UseOptics(o alpacadev.OpticsStore) { s.optics = o }

func (s *simMount) ApertureDiameter() float64 {
	if s.optics != nil {
		a, _, _, _, _ := s.optics.Optics()
		return a
	}
	return s.Telescope.ApertureDiameter()
}

func (s *simMount) ApertureArea() float64 {
	if s.optics != nil {
		_, a, _, _, _ := s.optics.Optics()
		return a
	}
	return s.Telescope.ApertureArea()
}

func (s *simMount) FocalLength() float64 {
	if s.optics != nil {
		_, _, f, _, _ := s.optics.Optics()
		return f
	}
	return s.Telescope.FocalLength()
}

// simMountAdapter presents the sim.Telescope as a lx200.Mount (+ Guider, OpLocker,
// PierSider) for the INDI/LX200 front-ends. Guiding and slews go through the same
// shared sky as the Alpaca path.
type simMountAdapter struct {
	t    *sim.Telescope
	opMu sync.Mutex
}

func (a *simMountAdapter) RA() (float64, error) {
	e, _ := sky.offset()
	return wrap24h(a.t.RightAscension() - e/54000.0), nil
}
func (a *simMountAdapter) Dec() (float64, error) {
	_, e := sky.offset()
	return a.t.Declination() - e/3600.0, nil
}
func (a *simMountAdapter) SetTargetRA(h float64) (bool, error) {
	return true, a.t.SetTargetRightAscension(h)
}
func (a *simMountAdapter) SetTargetDec(d float64) (bool, error) {
	return true, a.t.SetTargetDeclination(d)
}
func (a *simMountAdapter) SlewToTarget() error {
	err := a.t.SlewToTargetAsync()
	sky.reset()
	return err
}
func (a *simMountAdapter) SyncToTarget() (string, error) {
	err := a.t.SyncToTarget()
	sky.reset()
	return "Matched", err
}
func (a *simMountAdapter) Halt() error               { return a.t.AbortSlew() }
func (a *simMountAdapter) Slewing() (bool, error)    { return a.t.Slewing(), nil }
func (a *simMountAdapter) Tracking() (bool, error)   { return a.t.Tracking(), nil }
func (a *simMountAdapter) SetTracking(on bool) error { return a.t.SetTracking(on) }

// PulseGuide (lx200.Guider): the single INDI/LX200 guide path — same shared sky.
func (a *simMountAdapter) PulseGuide(d lx200.Direction, ms int) error {
	pulseSky(d, ms)
	return a.t.PulseGuide(guideDir(d), ms)
}

// OpLock (lx200.OpLocker) serializes the INDI/LX200 front-ends' multi-step gotos.
func (a *simMountAdapter) OpLock() func() { a.opMu.Lock(); return a.opMu.Unlock }

// PierSide (lx200.PierSider) reports the pier side, substituting East when the sim
// returns Unknown (it has no real pier).
func (a *simMountAdapter) PierSide() (lx200.PierSide, error) {
	s := lx200.PierSide(a.t.SideOfPier())
	if s == lx200.PierUnknown {
		s = lx200.PierEast
	}
	return s, nil
}

// pulseSky / pulseSkyAlpaca map a guide direction to the sign of the pointing-error
// correction: West/South counter a positive RA/Dec error.
func pulseSky(d lx200.Direction, ms int) {
	switch d {
	case lx200.North:
		sky.pulse(0, -1, ms)
	case lx200.South:
		sky.pulse(0, +1, ms)
	case lx200.East:
		sky.pulse(+1, 0, ms)
	case lx200.West:
		sky.pulse(-1, 0, ms)
	}
}

func pulseSkyAlpaca(d alpacadev.GuideDirection, ms int) {
	switch d {
	case alpacadev.GuideNorth:
		sky.pulse(0, -1, ms)
	case alpacadev.GuideSouth:
		sky.pulse(0, +1, ms)
	case alpacadev.GuideEast:
		sky.pulse(+1, 0, ms)
	case alpacadev.GuideWest:
		sky.pulse(-1, 0, ms)
	}
}

func guideDir(d lx200.Direction) alpacadev.GuideDirection {
	switch d {
	case lx200.South:
		return alpacadev.GuideSouth
	case lx200.East:
		return alpacadev.GuideEast
	case lx200.West:
		return alpacadev.GuideWest
	default:
		return alpacadev.GuideNorth
	}
}

func wrap24h(h float64) float64 {
	h = math.Mod(h, 24)
	if h < 0 {
		h += 24
	}
	return h
}

var (
	_ lx200.Mount     = (*simMountAdapter)(nil)
	_ lx200.Guider    = (*simMountAdapter)(nil)
	_ lx200.OpLocker  = (*simMountAdapter)(nil)
	_ lx200.PierSider = (*simMountAdapter)(nil)
)
