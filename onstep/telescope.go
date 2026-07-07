// Command onstep is a standalone ASCOM Alpaca Telescope driver for OnStep /
// OnStepX controllers, built directly on the lx200/onstep library.
package driver

import (
	"context"
	"log"
	"math"
	"sync"
	"time"

	alpacadev "github.com/mikefsq/goalpaca/server"
	lx200 "github.com/mikefsq/lx200"
	"github.com/mikefsq/lx200/onstep"
)

const (
	maxAxisRate = 6.0 // MoveAxis ceiling (deg/s); snapped to a preset
	slewTimeout = 3 * time.Minute
	acquirePoll = 3 * time.Second
	monitorPoll = 2 * time.Second
)

// snapshot caches the last good value of each getter, returned when the mount is
// unreachable or a live read fails.
type snapshot struct {
	ra, dec, alt, az, lst             float64
	pier                              alpacadev.PierSide
	slewing, tracking, atPark, atHome bool
}

// Telescope is the OnStep Alpaca Telescope device.
type Telescope struct {
	alpacadev.BaseTelescope

	serial, addr string // USB-serial port or WiFi/TCP host:port (addr wins)

	mu   sync.Mutex
	m    *onstep.Mount // nil ⇔ not connected
	snap snapshot

	siteLat, siteLon, siteEl float64
	trackingRate             alpacadev.DriveRate
	slewSettleSec            int
}

// NewTelescope builds the driver. addr (WiFi/TCP) takes precedence over serial.
func NewTelescope(serial, addr string) *Telescope {
	t := &Telescope{serial: serial, addr: addr, trackingRate: alpacadev.DriveSidereal}
	t.IfaceVer = alpacadev.InterfaceVersionTelescope
	return t
}

func (t *Telescope) dial() (*onstep.Mount, error) {
	if t.addr != "" {
		return onstep.Dial(t.addr)
	}
	return onstep.Open(t.serial)
}

// --- Hardware lifecycle + connection model ----------------------------------

func (t *Telescope) Open(ctx context.Context) error {
	go alpacadev.Supervise(ctx, t.ID, func() { t.manage(ctx) })
	return nil
}

func (t *Telescope) Close(ctx context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.m != nil {
		t.m.Close()
		t.m = nil
	}
	return nil
}

func (t *Telescope) Connect(ctx context.Context) error {
	if !t.Connected() {
		return alpacadev.ErrNotConnected
	}
	return nil
}

func (t *Telescope) Connected() bool                      { t.mu.Lock(); defer t.mu.Unlock(); return t.m != nil }
func (t *Telescope) Disconnect(ctx context.Context) error { return nil }
func (t *Telescope) Busy() bool                           { return t.Slewing() }

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
				log.Printf("onstep: mount %s connected", t.ID)
				lastErr = ""
			} else {
				if es := err.Error(); es != lastErr { // log each new failure once
					log.Printf("onstep: mount %s connect failed: %v (retrying)", t.ID, err)
					lastErr = es
				}
				sleepCtx(ctx, acquirePoll)
			}
			continue
		}
		t.mu.Lock()
		m := t.m
		t.mu.Unlock()
		if _, err := m.RA(); err != nil {
			log.Printf("onstep: mount %s lost (%v); reconnecting", t.ID, err)
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

func (t *Telescope) mount() *onstep.Mount { t.mu.Lock(); defer t.mu.Unlock(); return t.m }

// LiveMount returns the connected mount as a lx200.Mount, or ErrNotConnected.
func (t *Telescope) LiveMount() (lx200.Mount, error) {
	if m := t.mount(); m != nil {
		return m, nil
	}
	return nil, alpacadev.ErrNotConnected
}

// --- ASCOM Command* passthrough -------------------------------------------------
// CommandBlind/String/Bool send a raw LX200 command not wrapped by the typed API,
// mapping to the Blind/Get/Ack reply shapes. lx200.Frame adds ':'…'#' framing
// unless raw.

func (t *Telescope) CommandBlind(cmd string, raw bool) error {
	m := t.mount()
	if m == nil {
		return alpacadev.ErrNotConnected
	}
	return m.Blind(lx200.Frame(cmd, raw))
}

func (t *Telescope) CommandString(cmd string, raw bool) (string, error) {
	m := t.mount()
	if m == nil {
		return "", alpacadev.ErrNotConnected
	}
	return m.Get(lx200.Frame(cmd, raw))
}

func (t *Telescope) CommandBool(cmd string, raw bool) (bool, error) {
	m := t.mount()
	if m == nil {
		return false, alpacadev.ErrNotConnected
	}
	return m.Ack(lx200.Frame(cmd, raw))
}

// --- Capabilities (OnStep: park, find-home, pulse-guide, move-axis) ---

func (t *Telescope) CanSlew() bool        { return true }
func (t *Telescope) CanSlewAsync() bool   { return true }
func (t *Telescope) CanSync() bool        { return true }
func (t *Telescope) CanSetTracking() bool { return true }
func (t *Telescope) CanPark() bool        { return true }
func (t *Telescope) CanUnpark() bool      { return true }
func (t *Telescope) CanFindHome() bool    { return true }
func (t *Telescope) CanPulseGuide() bool  { return true }
func (t *Telescope) CanMoveAxis(axis alpacadev.TelescopeAxis) bool {
	return axis == alpacadev.AxisPrimary || axis == alpacadev.AxisSecondary
}

// --- Position / status getters ----------------------------------------------

func (t *Telescope) RightAscension() float64 {
	if m := t.mount(); m != nil {
		if v, err := m.RA(); err == nil {
			return t.setF(&t.snap.ra, v)
		}
	}
	return t.getF(&t.snap.ra)
}

func (t *Telescope) Declination() float64 {
	if m := t.mount(); m != nil {
		if v, err := m.Dec(); err == nil {
			return t.setF(&t.snap.dec, v)
		}
	}
	return t.getF(&t.snap.dec)
}

func (t *Telescope) Altitude() float64 {
	if m := t.mount(); m != nil {
		if v, err := m.Altitude(); err == nil {
			return t.setF(&t.snap.alt, v)
		}
	}
	return t.getF(&t.snap.alt)
}

func (t *Telescope) Azimuth() float64 {
	if m := t.mount(); m != nil {
		if v, err := m.Azimuth(); err == nil {
			return t.setF(&t.snap.az, v)
		}
	}
	return t.getF(&t.snap.az)
}

func (t *Telescope) SiderealTime() float64 {
	if m := t.mount(); m != nil {
		if v, err := m.SiderealTime(); err == nil {
			return t.setF(&t.snap.lst, v)
		}
	}
	return t.getF(&t.snap.lst)
}

func (t *Telescope) Slewing() bool {
	if m := t.mount(); m != nil {
		if v, err := m.Slewing(); err == nil {
			return t.setB(&t.snap.slewing, v)
		}
	}
	return t.getB(&t.snap.slewing)
}

func (t *Telescope) Tracking() bool {
	if m := t.mount(); m != nil {
		if v, err := m.Tracking(); err == nil {
			return t.setB(&t.snap.tracking, v)
		}
	}
	return t.getB(&t.snap.tracking)
}

func (t *Telescope) AtPark() bool {
	if m := t.mount(); m != nil {
		if v, err := m.AtPark(); err == nil {
			return t.setB(&t.snap.atPark, v)
		}
	}
	return t.getB(&t.snap.atPark)
}

func (t *Telescope) AtHome() bool {
	if m := t.mount(); m != nil {
		if v, err := m.AtHome(); err == nil {
			return t.setB(&t.snap.atHome, v)
		}
	}
	return t.getB(&t.snap.atHome)
}

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

// Driver-remembered properties (the mount does not read these back). Target
// RA/Dec are stored by the embedded BaseTelescope (promoted TargetRightAscension/
// TargetDeclination), which also enforces the ASCOM read-before-set rule.
func (t *Telescope) SiteLatitude() float64 { t.mu.Lock(); defer t.mu.Unlock(); return t.siteLat }
func (t *Telescope) SiteLongitude() float64     { t.mu.Lock(); defer t.mu.Unlock(); return t.siteLon }
func (t *Telescope) SiteElevation() float64     { t.mu.Lock(); defer t.mu.Unlock(); return t.siteEl }
func (t *Telescope) SlewSettleTime() int        { t.mu.Lock(); defer t.mu.Unlock(); return t.slewSettleSec }

func (t *Telescope) TrackingRate() alpacadev.DriveRate {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.trackingRate
}

func (t *Telescope) TrackingRates() []alpacadev.DriveRate {
	return []alpacadev.DriveRate{alpacadev.DriveSidereal, alpacadev.DriveLunar, alpacadev.DriveSolar}
}

func (t *Telescope) UTCDate() string { return time.Now().UTC().Format("2006-01-02T15:04:05.000Z") }

// --- Setters ----------------------------------------------------------------

func (t *Telescope) SetTracking(on bool) error {
	m := t.mount()
	if m == nil {
		return alpacadev.ErrNotConnected
	}
	return m.SetTracking(on)
}

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

func (t *Telescope) SetSlewSettleTime(seconds int) error {
	if seconds < 0 {
		return alpacadev.ErrInvalidValue
	}
	t.mu.Lock()
	t.slewSettleSec = seconds
	t.mu.Unlock()
	return nil
}

func (t *Telescope) SetUTCDate(s string) error {
	tm, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return alpacadev.ErrInvalidValue
	}
	m := t.mount()
	if m == nil {
		return alpacadev.ErrNotConnected
	}
	return m.SetUTC(tm)
}

// --- Motion -----------------------------------------------------------------

func (t *Telescope) AbortSlew() error {
	m := t.mount()
	if m == nil {
		return alpacadev.ErrNotConnected
	}
	return m.Halt()
}

func (t *Telescope) SlewToCoordinatesAsync(ra, dec float64) error {
	if err := t.SetTargetRightAscension(ra); err != nil {
		return err
	}
	if err := t.SetTargetDeclination(dec); err != nil {
		return err
	}
	return t.startSlew()
}

func (t *Telescope) SlewToCoordinates(ra, dec float64) error {
	if err := t.SlewToCoordinatesAsync(ra, dec); err != nil {
		return err
	}
	return t.waitSlew()
}

func (t *Telescope) SlewToTargetAsync() error { return t.startSlew() }

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

func (t *Telescope) SyncToCoordinates(ra, dec float64) error {
	if err := t.SetTargetRightAscension(ra); err != nil {
		return err
	}
	if err := t.SetTargetDeclination(dec); err != nil {
		return err
	}
	return t.SyncToTarget()
}

func (t *Telescope) SyncToTarget() error {
	m := t.mount()
	if m == nil {
		return alpacadev.ErrNotConnected
	}
	_, err := m.SyncToTarget()
	return err
}

func (t *Telescope) Park() error {
	m := t.mount()
	if m == nil {
		return alpacadev.ErrNotConnected
	}
	return m.Park()
}

func (t *Telescope) Unpark() error {
	m := t.mount()
	if m == nil {
		return alpacadev.ErrNotConnected
	}
	return m.Unpark()
}

func (t *Telescope) FindHome() error {
	m := t.mount()
	if m == nil {
		return alpacadev.ErrNotConnected
	}
	return m.FindHome()
}

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

func (t *Telescope) AxisRates(axis alpacadev.TelescopeAxis) []alpacadev.AxisRate {
	if axis != alpacadev.AxisPrimary && axis != alpacadev.AxisSecondary {
		return []alpacadev.AxisRate{}
	}
	return []alpacadev.AxisRate{{Minimum: 0, Maximum: maxAxisRate}}
}

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
