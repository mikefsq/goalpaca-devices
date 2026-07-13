// Package driver is the ASCOM Alpaca Telescope device for Rainbow Astro RST
// harmonic mounts (RST-135/300), over the lx200/rst protocol library
// (USB-serial). It is served standalone by cmd/rst and hosted by alpacahurd.
package driver

import (
	"context"
	"log"
	"math"
	"sync"
	"time"

	alpacadev "github.com/mikefsq/goalpaca/server"
	lx200 "github.com/mikefsq/lx200"
	"github.com/mikefsq/lx200/rst"
)

const (
	maxAxisRate = 6.0 // advertised MoveAxis ceiling (deg/s); snapped to a preset
	slewTimeout = 3 * time.Minute
	acquirePoll = 3 * time.Second
	monitorPoll = 2 * time.Second

	// A single failed health-check read is not a lost mount: on the shared USB-serial
	// line a reply can be delayed or collide with an async completion token, and the
	// full teardown+reconnect drops every front-end (Alpaca clients and the LX200
	// bridge) for ~10s. So re-probe a few times before declaring the mount lost.
	healthRetries  = 3
	healthRetryGap = 500 * time.Millisecond
)

// snapshot caches the last good value of each error-free getter, returned when
// the mount is unreachable or a live read fails.
type snapshot struct {
	ra, dec, alt, az, lst             float64
	pier                              alpacadev.PierSide
	slewing, tracking, atPark, atHome bool
}

// Telescope is the Rainbow Astro RST Alpaca Telescope device.
type Telescope struct {
	alpacadev.BaseTelescope

	serial string // USB-serial port; empty = auto-detect the first RST

	mu   sync.Mutex
	m    *rst.Mount // nil ⇔ not connected
	snap snapshot

	siteLat, siteLon, siteEl float64
	trackingRate             alpacadev.DriveRate
	slewSettleSec            int

	// Optics — instrument profile (the mount can't report it). Backed by an
	// OpticsStore so the host can inject a holder shared with the INDI front-end.
	optics alpacadev.OpticsStore
}

// NewTelescope builds the driver. An empty serial auto-detects the first RST.
func NewTelescope(serial string) *Telescope {
	t := &Telescope{serial: serial, trackingRate: alpacadev.DriveSidereal, optics: &localOptics{}}
	t.IfaceVer = alpacadev.InterfaceVersionTelescope
	return t
}

func (t *Telescope) dial() (*rst.Mount, error) {
	if t.serial != "" {
		return rst.Open(t.serial)
	}
	return rst.Find()
}

// --- Hardware lifecycle + connection model ----------------------------------

// Open starts the supervised background loop that dials the mount and keeps it
// connected. It touches no hardware itself.
func (t *Telescope) Open(ctx context.Context) error {
	go alpacadev.Supervise(ctx, t.ID, func() { t.manage(ctx) })
	return nil
}

// Close disconnects from the mount and releases the serial port.
func (t *Telescope) Close(ctx context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.m != nil {
		t.m.Close()
		t.m = nil
	}
	return nil
}

// Connect reports success once the mount is connected; the connection itself is made
// and maintained by the background manage loop, so this only checks the state.
func (t *Telescope) Connect(ctx context.Context) error {
	if !t.Connected() {
		return alpacadev.ErrNotConnected
	}
	return nil
}

// Connected reports whether the mount link is up.
func (t *Telescope) Connected() bool { t.mu.Lock(); defer t.mu.Unlock(); return t.m != nil }

// Disconnect is a no-op: the background loop owns the connection lifecycle.
func (t *Telescope) Disconnect(ctx context.Context) error { return nil }

// Busy reports whether a motion is in progress (the server gates mutating writes on it).
func (t *Telescope) Busy() bool { return t.Slewing() }

func (t *Telescope) manage(ctx context.Context) {
	var lastErr string
	for ctx.Err() == nil {
		t.mu.Lock()
		present := t.m != nil
		t.mu.Unlock()
		if !present {
			m, err := t.dial()
			if err == nil {
				t.mu.Lock()
				t.m = m
				t.mu.Unlock()
				log.Printf("rst: mount %s connected", t.ID)
				lastErr = ""
			} else {
				if es := err.Error(); es != lastErr { // log each new failure once, not every retry
					log.Printf("rst: mount %s connect failed: %v (retrying)", t.ID, err)
					lastErr = es
				}
				sleepCtx(ctx, acquirePoll)
			}
			continue
		}
		t.mu.Lock()
		m := t.m
		t.mu.Unlock()
		if _, err := m.RA(); err != nil && !alive(ctx, m) {
			log.Printf("rst: mount %s lost (%v); reconnecting", t.ID, err)
			t.mu.Lock()
			m.Close()
			t.m = nil
			t.mu.Unlock()
			lastErr = ""
			continue
		}
		sleepCtx(ctx, monitorPoll)
	}
}

// alive re-probes the mount after a failed health-check read, tolerating a transient
// miss (a delayed reply or a collision with an async completion token on the shared
// serial line) so it doesn't trigger a disruptive teardown+reconnect. It reports true
// as soon as any retry read succeeds, false once all retries fail (a genuine loss) or
// ctx is cancelled.
func alive(ctx context.Context, m *rst.Mount) bool {
	for i := 0; i < healthRetries; i++ {
		sleepCtx(ctx, healthRetryGap)
		if ctx.Err() != nil {
			return true // shutting down; don't churn the connection
		}
		if _, err := m.RA(); err == nil {
			return true
		}
	}
	return false
}

func (t *Telescope) mount() *rst.Mount { t.mu.Lock(); defer t.mu.Unlock(); return t.m }

// LiveMount returns the connected mount as a lx200.Mount (or ErrNotConnected), the
// seam the LX200 bridge and INDI server drive the same mount object through.
func (t *Telescope) LiveMount() (lx200.Mount, error) {
	if m := t.mount(); m != nil {
		return m, nil
	}
	return nil, alpacadev.ErrNotConnected
}

// --- ASCOM Command* passthrough -------------------------------------------------
// CommandBlind/String/Bool send a raw LX200 command the typed API doesn't wrap,
// mapping to the Blind/Get/Ack reply shapes. lx200.Frame adds ':'…'#' framing unless
// raw. The server gates these by Connected()/Busy(); the nil-guard covers the
// reconnect race.

// CommandBlind sends a raw LX200 command that produces no reply.
func (t *Telescope) CommandBlind(cmd string, raw bool) error {
	m := t.mount()
	if m == nil {
		return alpacadev.ErrNotConnected
	}
	return m.Blind(lx200.Frame(cmd, raw))
}

// CommandString sends a raw LX200 query and returns its #-terminated reply.
func (t *Telescope) CommandString(cmd string, raw bool) (string, error) {
	m := t.mount()
	if m == nil {
		return "", alpacadev.ErrNotConnected
	}
	return m.Get(lx200.Frame(cmd, raw))
}

// CommandBool sends a raw LX200 "set" command and reports the mount's 1/0 ack.
func (t *Telescope) CommandBool(cmd string, raw bool) (bool, error) {
	m := t.mount()
	if m == nil {
		return false, alpacadev.ErrNotConnected
	}
	return m.Ack(lx200.Frame(cmd, raw))
}

// --- Capabilities (RST: harmonic; park, find-home, pulse-guide, move-axis) ---

// CanSlew reports that the mount can slew to equatorial coordinates.
func (t *Telescope) CanSlew() bool { return true }

// CanSlewAsync reports that the mount supports asynchronous slews.
func (t *Telescope) CanSlewAsync() bool { return true }

// CanSync reports that the mount can sync to coordinates.
func (t *Telescope) CanSync() bool { return true }

// CanSetTracking reports that tracking can be turned on and off.
func (t *Telescope) CanSetTracking() bool { return true }

// CanPark reports that the mount can park.
func (t *Telescope) CanPark() bool { return true }

// CanUnpark reports that the mount can unpark.
func (t *Telescope) CanUnpark() bool { return true }

// CanFindHome reports that the mount can seek its mechanical home.
func (t *Telescope) CanFindHome() bool { return true }

// CanPulseGuide reports that the mount supports pulse guiding.
func (t *Telescope) CanPulseGuide() bool { return true }

// CanMoveAxis reports that the given axis supports MoveAxis (both primary and secondary do).
func (t *Telescope) CanMoveAxis(axis alpacadev.TelescopeAxis) bool {
	return axis == alpacadev.AxisPrimary || axis == alpacadev.AxisSecondary
}

// --- Position / status getters ----------------------------------------------
// Each returns the live value from the mount and caches it, falling back to the last
// good cached value (snapshot) when the mount is unreachable or a read fails.

// RightAscension returns the current right ascension in hours.
func (t *Telescope) RightAscension() float64 {
	if m := t.mount(); m != nil {
		if v, err := m.RA(); err == nil {
			return t.setF(&t.snap.ra, v)
		}
	}
	return t.getF(&t.snap.ra)
}

// Declination returns the current declination in degrees.
func (t *Telescope) Declination() float64 {
	if m := t.mount(); m != nil {
		if v, err := m.Dec(); err == nil {
			return t.setF(&t.snap.dec, v)
		}
	}
	return t.getF(&t.snap.dec)
}

// Altitude returns the current altitude above the horizon in degrees.
func (t *Telescope) Altitude() float64 {
	if m := t.mount(); m != nil {
		if v, err := m.Altitude(); err == nil {
			return t.setF(&t.snap.alt, v)
		}
	}
	return t.getF(&t.snap.alt)
}

// Azimuth returns the current azimuth in degrees, East of North.
func (t *Telescope) Azimuth() float64 {
	if m := t.mount(); m != nil {
		if v, err := m.Azimuth(); err == nil {
			return t.setF(&t.snap.az, v)
		}
	}
	return t.getF(&t.snap.az)
}

// SiderealTime returns the local apparent sidereal time in hours.
func (t *Telescope) SiderealTime() float64 {
	if m := t.mount(); m != nil {
		if v, err := m.SiderealTime(); err == nil {
			return t.setF(&t.snap.lst, v)
		}
	}
	return t.getF(&t.snap.lst)
}

// Slewing reports whether a goto, home, or park is in progress.
func (t *Telescope) Slewing() bool {
	if m := t.mount(); m != nil {
		if v, err := m.Slewing(); err == nil {
			return t.setB(&t.snap.slewing, v)
		}
	}
	return t.getB(&t.snap.slewing)
}

// Tracking reports whether the mount is tracking.
func (t *Telescope) Tracking() bool {
	if m := t.mount(); m != nil {
		if v, err := m.Tracking(); err == nil {
			return t.setB(&t.snap.tracking, v)
		}
	}
	return t.getB(&t.snap.tracking)
}

// AtPark reports whether the mount is parked.
func (t *Telescope) AtPark() bool {
	if m := t.mount(); m != nil {
		if v, err := m.AtPark(); err == nil {
			return t.setB(&t.snap.atPark, v)
		}
	}
	return t.getB(&t.snap.atPark)
}

// AtHome reports whether the mount is at its home position.
func (t *Telescope) AtHome() bool {
	if m := t.mount(); m != nil {
		if v, err := m.AtHome(); err == nil {
			return t.setB(&t.snap.atHome, v)
		}
	}
	return t.getB(&t.snap.atHome)
}

// IsPulseGuiding reports whether a pulse guide is in progress.
func (t *Telescope) IsPulseGuiding() bool {
	if m := t.mount(); m != nil {
		return m.IsPulseGuiding()
	}
	return false
}

// SideOfPier returns the side of pier the tube is on, derived from the axis angles.
func (t *Telescope) SideOfPier() alpacadev.PierSide {
	if m := t.mount(); m != nil {
		if ps, err := m.PierSide(); err == nil {
			t.mu.Lock()
			t.snap.pier = alpacadev.PierSide(ps)
			t.mu.Unlock()
			return alpacadev.PierSide(ps)
		}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.snap.pier
}

// Site elevation, slew-settle time, and the tracking rate are driver-remembered (the
// mount doesn't read them back); target RA/Dec live in the embedded BaseTelescope
// (promoted TargetRightAscension/TargetDeclination), which enforces read-before-set.

// SiteLatitude returns the observing latitude in degrees — the mount's own site
// (GPS-fed on the RST, :Gt#), falling back to the last value set through this driver
// if the mount can't be read (so a client that never wrote it still gets the real
// location, not 0).
func (t *Telescope) SiteLatitude() float64 {
	if m := t.mount(); m != nil {
		if v, err := m.SiteLatitude(); err == nil {
			return v
		}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.siteLat
}

// SiteLongitude returns the observing longitude in degrees East-positive, from the
// mount (:Gg#) with the driver-set value as fallback (see SiteLatitude).
func (t *Telescope) SiteLongitude() float64 {
	if m := t.mount(); m != nil {
		if v, err := m.SiteLongitude(); err == nil {
			return v
		}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.siteLon
}

// SiteElevation returns the driver-remembered site elevation in metres.
func (t *Telescope) SiteElevation() float64 { t.mu.Lock(); defer t.mu.Unlock(); return t.siteEl }

// SlewSettleTime returns the configured post-slew settle time in seconds.
func (t *Telescope) SlewSettleTime() int { t.mu.Lock(); defer t.mu.Unlock(); return t.slewSettleSec }

// TrackingRate returns the current tracking rate (sidereal/lunar/solar).
func (t *Telescope) TrackingRate() alpacadev.DriveRate {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.trackingRate
}

// TrackingRates returns the tracking rates the RST supports.
func (t *Telescope) TrackingRates() []alpacadev.DriveRate {
	return []alpacadev.DriveRate{alpacadev.DriveSidereal, alpacadev.DriveLunar, alpacadev.DriveSolar}
}

// UTCDate returns the current UTC time as an ISO-8601 string (the host clock; the RST
// has no clock-read command).
func (t *Telescope) UTCDate() string { return time.Now().UTC().Format("2006-01-02T15:04:05.000Z") }

// --- Setters ----------------------------------------------------------------
// RST exposes no clock command, so SetUTCDate stays BaseTelescope (not implemented).

// SetTracking turns tracking on or off.
func (t *Telescope) SetTracking(on bool) error {
	m := t.mount()
	if m == nil {
		return alpacadev.ErrNotConnected
	}
	return m.SetTracking(on)
}

// SetTrackingRate sets the tracking rate (sidereal, lunar, or solar).
func (t *Telescope) SetTrackingRate(r alpacadev.DriveRate) error {
	m := t.mount()
	if m == nil {
		return alpacadev.ErrNotConnected
	}
	var err error
	switch r {
	case alpacadev.DriveSidereal:
		err = m.TrackSidereal()
	case alpacadev.DriveLunar:
		err = m.TrackLunar()
	case alpacadev.DriveSolar:
		err = m.TrackSolar()
	default:
		return alpacadev.ErrInvalidValue
	}
	if err != nil {
		return err
	}
	t.mu.Lock()
	t.trackingRate = r
	t.mu.Unlock()
	return nil
}

// SetTargetRightAscension sets the goto/sync target right ascension in hours.
func (t *Telescope) SetTargetRightAscension(ra float64) error {
	if ra < 0 || ra >= 24 {
		return alpacadev.ErrInvalidValue
	}
	m := t.mount()
	if m == nil {
		return alpacadev.ErrNotConnected
	}
	ok, err := m.SetTargetRA(ra)
	if err != nil {
		return err
	}
	if !ok {
		return alpacadev.ErrInvalidValue
	}
	return t.BaseTelescope.SetTargetRightAscension(ra)
}

// SetTargetDeclination sets the goto/sync target declination in degrees.
func (t *Telescope) SetTargetDeclination(dec float64) error {
	if dec < -90 || dec > 90 {
		return alpacadev.ErrInvalidValue
	}
	m := t.mount()
	if m == nil {
		return alpacadev.ErrNotConnected
	}
	ok, err := m.SetTargetDec(dec)
	if err != nil {
		return err
	}
	if !ok {
		return alpacadev.ErrInvalidValue
	}
	return t.BaseTelescope.SetTargetDeclination(dec)
}

// SetSiteLatitude sets the observing latitude in degrees, on the mount and locally.
func (t *Telescope) SetSiteLatitude(deg float64) error {
	if deg < -90 || deg > 90 {
		return alpacadev.ErrInvalidValue
	}
	m := t.mount()
	if m == nil {
		return alpacadev.ErrNotConnected
	}
	if err := m.SetSiteLatitude(deg); err != nil {
		return err
	}
	t.mu.Lock()
	t.siteLat = deg
	t.mu.Unlock()
	return nil
}

// SetSiteLongitude sets the observing longitude in degrees East-positive, on the
// mount and locally.
func (t *Telescope) SetSiteLongitude(deg float64) error {
	if deg < -180 || deg > 180 {
		return alpacadev.ErrInvalidValue
	}
	m := t.mount()
	if m == nil {
		return alpacadev.ErrNotConnected
	}
	if err := m.SetSiteLongitude(deg); err != nil {
		return err
	}
	t.mu.Lock()
	t.siteLon = deg
	t.mu.Unlock()
	return nil
}

// SetSiteElevation sets the observing site elevation in metres (driver-remembered;
// the RST has no elevation command).
func (t *Telescope) SetSiteElevation(meters float64) error {
	if meters < -300 || meters > 10000 {
		return alpacadev.ErrInvalidValue
	}
	m := t.mount()
	if m == nil {
		return alpacadev.ErrNotConnected
	}
	if err := m.SetSiteElevation(meters); err != nil {
		return err
	}
	t.mu.Lock()
	t.siteEl = meters
	t.mu.Unlock()
	return nil
}

// SetSlewSettleTime sets the post-slew settle time in seconds.
func (t *Telescope) SetSlewSettleTime(seconds int) error {
	if seconds < 0 {
		return alpacadev.ErrInvalidValue
	}
	t.mu.Lock()
	t.slewSettleSec = seconds
	t.mu.Unlock()
	return nil
}

// --- Motion -----------------------------------------------------------------

// AbortSlew immediately stops any slew or continuous move.
func (t *Telescope) AbortSlew() error {
	m := t.mount()
	if m == nil {
		return alpacadev.ErrNotConnected
	}
	return m.Halt()
}

// SlewToCoordinatesAsync starts an asynchronous goto to the given RA/Dec, returning
// immediately.
func (t *Telescope) SlewToCoordinatesAsync(ra, dec float64) error {
	if err := t.SetTargetRightAscension(ra); err != nil {
		return err
	}
	if err := t.SetTargetDeclination(dec); err != nil {
		return err
	}
	return t.startSlew()
}

// SlewToCoordinates gotos the given RA/Dec and blocks until the slew completes.
func (t *Telescope) SlewToCoordinates(ra, dec float64) error {
	if err := t.SlewToCoordinatesAsync(ra, dec); err != nil {
		return err
	}
	return t.waitSlew()
}

// SlewToTargetAsync starts an asynchronous goto to the current target coordinates.
func (t *Telescope) SlewToTargetAsync() error { return t.startSlew() }

// SlewToTarget gotos the current target coordinates and blocks until complete.
func (t *Telescope) SlewToTarget() error {
	if err := t.startSlew(); err != nil {
		return err
	}
	return t.waitSlew()
}

func (t *Telescope) startSlew() error {
	m := t.mount()
	if m == nil {
		return alpacadev.ErrNotConnected
	}
	return m.SlewToTarget()
}

// SyncToCoordinates syncs the pointing model to the given RA/Dec.
func (t *Telescope) SyncToCoordinates(ra, dec float64) error {
	if err := t.SetTargetRightAscension(ra); err != nil {
		return err
	}
	if err := t.SetTargetDeclination(dec); err != nil {
		return err
	}
	return t.SyncToTarget()
}

// SyncToTarget syncs the pointing model to the current target coordinates.
func (t *Telescope) SyncToTarget() error {
	m := t.mount()
	if m == nil {
		return alpacadev.ErrNotConnected
	}
	_, err := m.SyncToTarget()
	return err
}

// Park sends the mount to its park position — the polar axis — with tracking off.
func (t *Telescope) Park() error {
	m := t.mount()
	if m == nil {
		return alpacadev.ErrNotConnected
	}
	return m.Park()
}

// Unpark releases the park latch and re-enables tracking.
func (t *Telescope) Unpark() error {
	m := t.mount()
	if m == nil {
		return alpacadev.ErrNotConnected
	}
	return m.Unpark()
}

// FindHome seeks the mount's mechanical home — on the RST, the West horizon.
func (t *Telescope) FindHome() error {
	m := t.mount()
	if m == nil {
		return alpacadev.ErrNotConnected
	}
	return m.FindHome()
}

// PulseGuide issues a timed guide pulse in the given direction for ms milliseconds.
func (t *Telescope) PulseGuide(dir alpacadev.GuideDirection, ms int) error {
	if ms < 0 {
		return alpacadev.ErrInvalidValue
	}
	m := t.mount()
	if m == nil {
		return alpacadev.ErrNotConnected
	}
	d, ok := guideDir(dir)
	if !ok {
		return alpacadev.ErrInvalidValue
	}
	return m.PulseGuide(d, ms)
}

// Guide rate — ASCOM exposes it per-axis in deg/s; the RST has a single ×sidereal
// rate shared by both axes (:CU0#/:Cu0=), so both properties read/write the one mount
// value. The `guiderate` Action is the same setting in ×sidereal units (its native
// form). siderealDegPerSec matches the vendor driver's guide-rate math (15"/s).
const siderealDegPerSec = 15.0 / 3600.0

// CanSetGuideRates reports that the guide rate can be set.
func (t *Telescope) CanSetGuideRates() bool { return true }

// GuideRateRightAscension returns the guide rate in degrees/second.
func (t *Telescope) GuideRateRightAscension() float64 { return t.guideRateDegPerSec() }

// GuideRateDeclination returns the guide rate in degrees/second (the RST shares one
// rate across both axes, so this equals GuideRateRightAscension).
func (t *Telescope) GuideRateDeclination() float64 { return t.guideRateDegPerSec() }

// SetGuideRateRightAscension sets the guide rate in degrees/second (one rate shared
// by both axes).
func (t *Telescope) SetGuideRateRightAscension(degPerSec float64) error {
	return t.setGuideRateDegPerSec(degPerSec)
}

// SetGuideRateDeclination sets the guide rate in degrees/second (one rate shared by
// both axes).
func (t *Telescope) SetGuideRateDeclination(degPerSec float64) error {
	return t.setGuideRateDegPerSec(degPerSec)
}

func (t *Telescope) guideRateDegPerSec() float64 {
	m := t.mount()
	if m == nil {
		return 0
	}
	x, err := m.GuideRate() // ×sidereal
	if err != nil {
		return 0
	}
	return x * siderealDegPerSec
}

func (t *Telescope) setGuideRateDegPerSec(degPerSec float64) error {
	if degPerSec < 0 {
		return alpacadev.ErrInvalidValue
	}
	m := t.mount()
	if m == nil {
		return alpacadev.ErrNotConnected
	}
	return m.SetGuideRate(degPerSec / siderealDegPerSec)
}

// AxisRates returns the supported MoveAxis rate range (deg/s) for the given axis.
func (t *Telescope) AxisRates(axis alpacadev.TelescopeAxis) []alpacadev.AxisRate {
	if axis != alpacadev.AxisPrimary && axis != alpacadev.AxisSecondary {
		return []alpacadev.AxisRate{}
	}
	return []alpacadev.AxisRate{{Minimum: 0, Maximum: maxAxisRate}}
}

// MoveAxis starts a continuous slew on the given axis at rate deg/s (rate 0 stops it).
func (t *Telescope) MoveAxis(axis alpacadev.TelescopeAxis, rate float64) error {
	m := t.mount()
	if m == nil {
		return alpacadev.ErrNotConnected
	}
	a, ok := axisOf(axis)
	if !ok {
		return alpacadev.ErrInvalidValue
	}
	if rate == 0 {
		return m.StopAxis(a)
	}
	if math.Abs(rate) > maxAxisRate {
		return alpacadev.ErrInvalidValue
	}
	return m.MoveAxis(a, rate > 0, presetForRate(math.Abs(rate)))
}

// --- helpers ----------------------------------------------------------------

func (t *Telescope) setF(p *float64, v float64) float64 { t.mu.Lock(); *p = v; t.mu.Unlock(); return v }
func (t *Telescope) getF(p *float64) float64            { t.mu.Lock(); defer t.mu.Unlock(); return *p }
func (t *Telescope) setB(p *bool, v bool) bool          { t.mu.Lock(); *p = v; t.mu.Unlock(); return v }
func (t *Telescope) getB(p *bool) bool                  { t.mu.Lock(); defer t.mu.Unlock(); return *p }

func (t *Telescope) waitSlew() error {
	deadline := time.Now().Add(slewTimeout)
	for {
		m := t.mount()
		if m == nil {
			return alpacadev.ErrNotConnected
		}
		if sl, err := m.Slewing(); err == nil && !sl {
			// A movement-limit / error abort clears Slewing but leaves a fault; surface
			// it so a blocked slew fails loudly instead of looking like it arrived.
			if f := m.Fault(); f != "" {
				return alpacadev.NewError(alpacadev.ErrNumUnspecified, "slew aborted at "+f)
			}
			return nil
		}
		if time.Now().After(deadline) {
			return alpacadev.NewError(alpacadev.ErrNumUnspecified, "slew did not complete within timeout")
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func guideDir(d alpacadev.GuideDirection) (lx200.Direction, bool) {
	switch d {
	case alpacadev.GuideNorth:
		return lx200.North, true
	case alpacadev.GuideSouth:
		return lx200.South, true
	case alpacadev.GuideEast:
		return lx200.East, true
	case alpacadev.GuideWest:
		return lx200.West, true
	}
	return 0, false
}

func axisOf(a alpacadev.TelescopeAxis) (lx200.Axis, bool) {
	switch a {
	case alpacadev.AxisPrimary:
		return lx200.AxisPrimary, true
	case alpacadev.AxisSecondary:
		return lx200.AxisSecondary, true
	}
	return 0, false
}

func presetForRate(absRate float64) lx200.Rate {
	switch {
	case absRate <= 0.05:
		return lx200.RateGuide
	case absRate <= 0.5:
		return lx200.RateCenter
	case absRate <= 2.0:
		return lx200.RateFind
	default:
		return lx200.RateMax
	}
}

func sleepCtx(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

var (
	_ alpacadev.Telescope = (*Telescope)(nil)
	_ alpacadev.Hardware  = (*Telescope)(nil)
	_ alpacadev.Busyable  = (*Telescope)(nil)
)
