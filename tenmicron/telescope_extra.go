package driver

import (
	"math"
	"time"

	alpacadev "github.com/mikefsq/goalpaca/server"
	"github.com/mikefsq/lx200/tenmicron"
)

// ASCOM Telescope members beyond the BaseTelescope defaults: alt/az goto + sync,
// guide rates, set-park, destination-side-of-pier, refraction and pulse-guiding state.

const arcsecPerDeg = 3600.0

// siderealArcsecPerSec is the sidereal tracking rate (arcsec per SI second), used
// to convert ASCOM DeclinationRate (arcsec/s) to the mount's multiples-of-sidereal.
const siderealArcsecPerSec = 15.0410681

// --- Mount geometry ---------------------------------------------------------
// 10Micron GM-series German-equatorial mounts report apparent (Jnow) coordinates.

func (t *Telescope) AlignmentMode() alpacadev.AlignmentMode {
	return alpacadev.AlignGermanPolar
}

func (t *Telescope) EquatorialSystem() alpacadev.EquatorialCoordinateType {
	return alpacadev.EquTopocentric
}

// --- Capabilities now supported ---------------------------------------------

func (t *Telescope) CanSlewAltAz() bool             { return true }
func (t *Telescope) CanSlewAltAzAsync() bool        { return true }
func (t *Telescope) CanSyncAltAz() bool             { return true }
func (t *Telescope) CanSetGuideRates() bool         { return true }
func (t *Telescope) CanSetPark() bool               { return true }
func (t *Telescope) CanSetRightAscensionRate() bool { return true }
func (t *Telescope) CanSetDeclinationRate() bool    { return true }

func (t *Telescope) CanSetPierSide() bool { return true }

// SetSideOfPier forces the mount to the requested pier side. 10Micron has no
// "set exact side" primitive, so this reads the current pointing side (:pS#) and
// issues a meridian flip (:FLIP#) only when a change is needed — :FLIP# re-points the
// same coordinates from the opposite side. Errors when the current side is unknown.
func (t *Telescope) SetSideOfPier(side alpacadev.PierSide) error {
	if side != alpacadev.PierEast && side != alpacadev.PierWest {
		return alpacadev.ErrInvalidValue
	}
	m := t.mount()
	if m == nil {
		return alpacadev.ErrNotConnected
	}
	cur, err := m.PointingState()
	if err != nil {
		return err
	}
	switch alpacadev.PierSide(cur) {
	case side:
		return nil // already on requested side
	case alpacadev.PierUnknown:
		return alpacadev.NewError(alpacadev.ErrNumUnspecified, "cannot determine current pier side")
	default:
		return m.Flip() // flip to requested side
	}
}

// --- Custom tracking-rate offsets -------------------------------------------
// No mount read-back, so the last-set value is cached. ASCOM RightAscensionRate
// (seconds of RA per sidereal second) maps 1:1 to the mount's :RR rate (multiples of
// sidereal). ASCOM DeclinationRate (arcsec/SI-second) is divided by the sidereal rate
// to get the mount's multiples-of-sidereal.

func (t *Telescope) RightAscensionRate() float64 { return t.getF(&t.raRate) }
func (t *Telescope) DeclinationRate() float64    { return t.getF(&t.decRate) }

func (t *Telescope) SetRightAscensionRate(secPerSiderealSec float64) error {
	m := t.mount()
	if m == nil {
		return alpacadev.ErrNotConnected
	}
	if err := m.SetCustomRARate(secPerSiderealSec); err != nil { // 1:1
		return err
	}
	t.setF(&t.raRate, secPerSiderealSec)
	return nil
}

func (t *Telescope) SetDeclinationRate(arcsecPerSec float64) error {
	m := t.mount()
	if m == nil {
		return alpacadev.ErrNotConnected
	}
	// The mount's :RD declination offset only drives the axis while dual-axis tracking
	// is on, so a non-zero Dec rate enables it first. Not auto-disabled on zero (dual-axis
	// tracking is also used for refraction following); toggle via the dualaxistracking Action.
	if arcsecPerSec != 0 {
		if err := m.SetDualAxisTracking(true); err != nil {
			return err
		}
	}
	if err := m.SetCustomDecRate(arcsecPerSec / siderealArcsecPerSec); err != nil {
		return err
	}
	t.setF(&t.decRate, arcsecPerSec)
	return nil
}

// --- Optics (instrument profile, set via flags) -----------------------------
// The mount cannot report optics; these are configured at startup so ASCOM clients
// read consistent values.

func (t *Telescope) ApertureDiameter() float64 { ap, _, _, _, _ := t.opticsStore().Optics(); return ap }
func (t *Telescope) ApertureArea() float64     { _, area, _, _, _ := t.opticsStore().Optics(); return area }
func (t *Telescope) FocalLength() float64      { _, _, fl, _, _ := t.opticsStore().Optics(); return fl }

// SetOptics configures the instrument-profile optics (metres / m²). When
// areaSqMeters is zero it defaults to the circular aperture area from the diameter.
// The guide scope is set to the main scope (OAG default); a separate guide scope is
// configured via the setoptics Action's guider_* fields.
func (t *Telescope) SetOptics(diameterMeters, areaSqMeters, focalLengthMeters float64) {
	if areaSqMeters == 0 && diameterMeters > 0 {
		r := diameterMeters / 2
		areaSqMeters = math.Pi * r * r
	}
	t.opticsStore().SetOptics(diameterMeters, areaSqMeters, focalLengthMeters, diameterMeters, focalLengthMeters)
}

// --- Alt/Az goto + sync (ASCOM order is azimuth, altitude) ------------------

func (t *Telescope) SlewToAltAzAsync(azimuth, altitude float64) error {
	if !validAltAz(azimuth, altitude) {
		return alpacadev.ErrInvalidValue
	}
	m := t.mount()
	if m == nil {
		return alpacadev.ErrNotConnected
	}
	return m.SlewToAltAz(altitude, azimuth) // lib takes (alt, az); :MA# returns once started
}

func (t *Telescope) SlewToAltAz(azimuth, altitude float64) error {
	if err := t.SlewToAltAzAsync(azimuth, altitude); err != nil {
		return err
	}
	return t.waitSlew()
}

func (t *Telescope) SyncToAltAz(azimuth, altitude float64) error {
	if !validAltAz(azimuth, altitude) {
		return alpacadev.ErrInvalidValue
	}
	m := t.mount()
	if m == nil {
		return alpacadev.ErrNotConnected
	}
	_, err := m.SyncToAltAz(altitude, azimuth)
	return err
}

func validAltAz(az, alt float64) bool {
	return az >= 0 && az <= 360 && alt >= -90 && alt <= 90
}

// --- Guide rates (ASCOM deg/s; the mount uses one rate for both axes) --------

func (t *Telescope) GuideRateRightAscension() float64 { return t.guideRate() }
func (t *Telescope) GuideRateDeclination() float64    { return t.guideRate() }

// guideRate is snapshot-served (poller slow set + setGuideRate); no mount I/O per GET.
func (t *Telescope) guideRate() float64 { return t.getF(&t.snap.guideRate) }

func (t *Telescope) SetGuideRateRightAscension(degPerSec float64) error {
	return t.setGuideRate(degPerSec)
}

func (t *Telescope) SetGuideRateDeclination(degPerSec float64) error {
	return t.setGuideRate(degPerSec)
}

func (t *Telescope) setGuideRate(degPerSec float64) error {
	if degPerSec < 0 {
		return alpacadev.ErrInvalidValue
	}
	m := t.mount()
	if m == nil {
		return alpacadev.ErrNotConnected
	}
	// One physical guide rate; setting either axis sets both. Clamp to the mount's
	// supported band [0.1×, 1.0×] sidereal so the stored/reported value matches the wire.
	arcsec := degPerSec * arcsecPerDeg
	if arcsec > tenmicron.GuideRateMaxArcsec {
		arcsec = tenmicron.GuideRateMaxArcsec
	}
	if arcsec < tenmicron.GuideRateMinArcsec {
		arcsec = tenmicron.GuideRateMinArcsec
	}
	if err := m.SetGuideRate(arcsec); err != nil {
		return err
	}
	t.setF(&t.snap.guideRate, arcsec/arcsecPerDeg)
	return nil
}

// --- Refraction (the default reported false; the mount has a refraction model) -

// DoesRefraction is snapshot-served (poller slow set + SetDoesRefraction); no mount I/O.
func (t *Telescope) DoesRefraction() bool { return t.getB(&t.snap.doesRefraction) }

func (t *Telescope) SetDoesRefraction(on bool) error {
	m := t.mount()
	if m == nil {
		return alpacadev.ErrNotConnected
	}
	ok, err := m.SetRefractionCorrection(on)
	if err != nil {
		return err
	}
	if !ok {
		return alpacadev.NewError(alpacadev.ErrNumUnspecified, "mount rejected refraction setting")
	}
	t.setB(&t.snap.doesRefraction, on)
	return nil
}

// --- Set-park + destination-side-of-pier ------------------------------------

func (t *Telescope) SetPark() error {
	m := t.mount()
	if m == nil {
		return alpacadev.ErrNotConnected
	}
	return m.SaveParkPosition()
}

// DestinationSideOfPier predicts the pier side for a target. 10Micron predicts for
// the currently-set target (:GTsid#), so this sets the target to ra/dec first (a
// documented side effect — it also updates TargetRightAscension/Declination).
func (t *Telescope) DestinationSideOfPier(ra, dec float64) (alpacadev.PierSide, error) {
	if ra < 0 || ra >= 24 || dec < -90 || dec > 90 {
		return alpacadev.PierUnknown, alpacadev.ErrInvalidValue
	}
	m := t.mount()
	if m == nil {
		return alpacadev.PierUnknown, alpacadev.ErrNotConnected
	}
	if err := t.SetTargetRightAscension(ra); err != nil {
		return alpacadev.PierUnknown, err
	}
	if err := t.SetTargetDeclination(dec); err != nil {
		return alpacadev.PierUnknown, err
	}
	ps, err := m.DestinationSideOfPier()
	if err != nil {
		return alpacadev.PierUnknown, err
	}
	return alpacadev.PierSide(ps), nil // lx200/alpacadev PierSide share values (-1/0/1)
}

// --- Pulse-guiding state (the default was always false) ----------------------

func (t *Telescope) IsPulseGuiding() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return time.Now().Before(t.pulseUntil)
}
