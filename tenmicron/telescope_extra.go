package driver

import (
	"math"
	"time"

	alpacadev "github.com/mikefsq/goalpaca/server"
)

// This file wires the ASCOM Telescope members the base wrapper left as
// BaseTelescope defaults but the lx200/tenmicron library now supports: alt/az
// goto + sync, guide rates, set-park, destination-side-of-pier, plus the
// refraction and pulse-guiding state that the defaults reported incorrectly.

const arcsecPerDeg = 3600.0

// siderealArcsecPerSec is the sidereal tracking rate (arcsec per SI second), used
// to convert ASCOM DeclinationRate (arcsec/s) to the mount's multiples-of-sidereal.
const siderealArcsecPerSec = 15.0410681

// --- Mount geometry (stated explicitly rather than inherited) ---------------
// The driver targets 10Micron GM-series German-equatorial mounts, which report
// apparent (Jnow) coordinates.

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
// "set exact side" primitive (and its :MSfs side numbering is spec-ambiguous), so
// this reads the current pointing side (:pS#) and issues a meridian flip (:FLIP#)
// only when a change is needed — :FLIP# re-points the same coordinates from the
// opposite side, which is the ASCOM SetSideOfPier semantics. It errors (rather than
// guessing a flip) when the current side can't be determined.
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
		return nil // already on the requested side
	case alpacadev.PierUnknown:
		return alpacadev.NewError(alpacadev.ErrNumUnspecified, "cannot determine current pier side")
	default:
		return m.Flip() // flip to the opposite (= requested) side
	}
}

// --- Custom tracking-rate offsets -------------------------------------------
// 10Micron has no read-back for these, so the last-set value is cached (the ASCOM
// convention for rate offsets). ASCOM RightAscensionRate is "seconds of RA per
// sidereal second" — and the mount's :RR rate is "multiples of sidereal added to
// sidereal" — so the two are 1:1. ASCOM DeclinationRate is arcsec/SI-second, so it
// is divided by the sidereal rate to get the mount's multiples-of-sidereal.

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
	if err := m.SetCustomDecRate(arcsecPerSec / siderealArcsecPerSec); err != nil {
		return err
	}
	t.setF(&t.decRate, arcsecPerSec)
	return nil
}

// --- Optics (instrument profile, set via flags) -----------------------------
// The mount cannot report optics; these are configured at startup so ASCOM client
// profiles (NINA, SGP, …) and downstream software can read consistent values.

func (t *Telescope) ApertureDiameter() float64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.apertureDiameter
}
func (t *Telescope) ApertureArea() float64 { t.mu.Lock(); defer t.mu.Unlock(); return t.apertureArea }
func (t *Telescope) FocalLength() float64  { t.mu.Lock(); defer t.mu.Unlock(); return t.focalLength }

// SetOptics configures the instrument-profile optics (metres / m²). When
// areaSqMeters is zero it defaults to the circular aperture area from the diameter.
func (t *Telescope) SetOptics(diameterMeters, areaSqMeters, focalLengthMeters float64) {
	if areaSqMeters == 0 && diameterMeters > 0 {
		r := diameterMeters / 2
		areaSqMeters = math.Pi * r * r
	}
	t.mu.Lock()
	t.apertureDiameter = diameterMeters
	t.apertureArea = areaSqMeters
	t.focalLength = focalLengthMeters
	t.mu.Unlock()
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

func (t *Telescope) guideRate() float64 {
	if m := t.mount(); m != nil {
		if v, err := m.GuideRate(); err == nil { // lib returns arcsec/s
			return t.setF(&t.snap.guideRate, v/arcsecPerDeg)
		}
	}
	return t.getF(&t.snap.guideRate)
}

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
	// One physical guide rate; setting either axis sets both.
	if err := m.SetGuideRate(degPerSec * arcsecPerDeg); err != nil {
		return err
	}
	t.setF(&t.snap.guideRate, degPerSec)
	return nil
}

// --- Refraction (the default reported false; the mount has a refraction model) -

func (t *Telescope) DoesRefraction() bool {
	if m := t.mount(); m != nil {
		if on, err := m.RefractionCorrection(); err == nil {
			return t.setB(&t.snap.doesRefraction, on)
		}
	}
	return t.getB(&t.snap.doesRefraction)
}

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
