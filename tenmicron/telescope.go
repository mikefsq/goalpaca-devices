// Package driver is the ASCOM Alpaca Telescope device for 10Micron GM-series
// mounts, over the lx200/tenmicron protocol library (TCP). It is served
// standalone by cmd/tenmicron and hosted by the astrofleet aggregator.
package driver

import (
	"context"
	"log"
	"math"
	"strings"
	"sync"
	"time"

	alpacadev "github.com/mikefsq/goalpaca/server"
	lx200 "github.com/mikefsq/lx200"
	"github.com/mikefsq/lx200/tenmicron"
)

const (
	maxAxisRate = 6.0 // advertised MoveAxis ceiling (deg/s); snapped to a preset
	slewTimeout = 3 * time.Minute
	acquirePoll = 3 * time.Second
	monitorPoll = 1 * time.Second        // snapshot refresh cadence when idle/tracking
	slewPoll    = 250 * time.Millisecond // refresh while slewing
	fullPoll    = 5 * time.Second        // cadence for the slow-changing set (home/guide/refraction)

	// mountCacheTTL is the mount's :Ginfo# cache lifetime. Longer than monitorPoll so
	// the LX200 bridge and INDI server ride the poller's cache between cycles.
	mountCacheTTL = 2 * time.Second
)

// snapshot is the cached mount state every getter is served from. The background
// poller (pollOnce) refreshes it; a property GET is a locked read with no mount I/O.
type snapshot struct {
	ra, dec, alt, az                  float64
	guideRate                         float64 // deg/s (one rate, both axes)
	pier                              alpacadev.PierSide
	slewing, tracking, atPark, atHome bool
	doesRefraction                    bool
}

// Telescope is the 10Micron Alpaca Telescope device. It owns the mount for the
// process lifetime; Connected ≡ mount reachable; Busy() gates writes while slewing.
type Telescope struct {
	alpacadev.BaseTelescope

	addr string

	mu   sync.Mutex
	m    *tenmicron.Mount // nil ⇔ not connected
	snap snapshot

	siteLat, siteLon, siteEl float64
	raRate, decRate          float64 // custom tracking-rate offsets (no mount read-back)
	trackingRate             alpacadev.DriveRate
	slewSettleSec            int
	pulseUntil               time.Time // PulseGuide completion deadline (IsPulseGuiding)
	canHome                  bool      // homing supported (model-derived at connect)

	// Optics — instrument profile (the mount can't report it). Backed by an
	// OpticsStore so the fleet can inject a holder shared with the INDI front-end.
	optics alpacadev.OpticsStore
}

// NewTelescope builds the driver for the 10Micron mount at addr (host:port).
func NewTelescope(addr string) *Telescope {
	// canHome defaults true; primeStatics narrows it from the model on connect.
	t := &Telescope{addr: addr, trackingRate: alpacadev.DriveSidereal, optics: &localOptics{}, canHome: true}
	t.IfaceVer = alpacadev.InterfaceVersionTelescope
	return t
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

// manage acquires, monitors and re-acquires the mount for the process lifetime. Each
// tick refreshes the snapshot (pollOnce), which doubles as the liveness check; the
// cadence tightens while slewing.
func (t *Telescope) manage(ctx context.Context) {
	var lastErr string
	var lastFull time.Time // when the slow-changing set was last refreshed
	for ctx.Err() == nil {
		t.mu.Lock()
		present := t.m != nil
		t.mu.Unlock()
		if !present {
			m, err := tenmicron.Connect(t.addr)
			if err == nil {
				m.SetStatusTTL(mountCacheTTL)
				if perr := t.pollOnce(m, true); perr != nil { // prime snapshot before clients see Connected
					m.Close()
					err = perr
				}
			}
			if err == nil {
				t.primeStatics(m) // site coords + model (CanFindHome); best-effort
				t.mu.Lock()
				t.m = m
				t.mu.Unlock()
				lastFull = time.Now()
				log.Printf("tenmicron: mount %s connected", t.ID)
				lastErr = ""
			} else {
				if es := err.Error(); es != lastErr { // log each new failure once, not every retry
					log.Printf("tenmicron: mount %s connect failed: %v (retrying)", t.ID, err)
					lastErr = es
				}
				sleepCtx(ctx, acquirePoll)
			}
			continue
		}
		t.mu.Lock()
		m := t.m
		t.mu.Unlock()
		full := time.Since(lastFull) >= fullPoll
		if err := t.pollOnce(m, full); err != nil { // poll doubles as liveness check
			log.Printf("tenmicron: mount %s lost (%v); reconnecting", t.ID, err)
			t.mu.Lock()
			m.Close()
			t.m = nil
			t.mu.Unlock()
			lastErr = ""
			continue
		}
		if full {
			lastFull = time.Now()
		}
		interval := monitorPoll
		if t.getB(&t.snap.slewing) {
			interval = slewPoll
		}
		sleepCtx(ctx, interval)
	}
}

// pollOnce refreshes the snapshot from the mount. One forced :Ginfo# (m.Refresh) reads
// the whole volatile set and re-arms the shared status cache; a failure means the link
// is down and is returned so manage reconnects. When full, it also refreshes the
// slow-changing set (home/guide-rate/refraction) via separate round-trips; transient
// errors there keep the last good value.
func (t *Telescope) pollOnce(m *tenmicron.Mount, full bool) error {
	st, err := m.Refresh() // forced :Ginfo#; re-arms the shared cache
	if err != nil {
		return err
	}
	t.mu.Lock()
	t.snap.ra, t.snap.dec, t.snap.alt, t.snap.az = st.RA, st.Dec, st.Alt, st.Az
	t.snap.pier = alpacadev.PierSide(st.Pier)
	t.snap.slewing, t.snap.tracking, t.snap.atPark = st.IsSlewing(), st.IsTracking(), st.IsParked()
	t.mu.Unlock()

	if !full {
		return nil
	}
	atHome, ahErr := m.AtHome()
	guide, gErr := m.GuideRate() // arcsec/s
	refr, rErr := m.RefractionCorrection()
	t.mu.Lock()
	if ahErr == nil {
		t.snap.atHome = atHome
	}
	if gErr == nil {
		t.snap.guideRate = guide / arcsecPerDeg
	}
	if rErr == nil {
		t.snap.doesRefraction = refr
	}
	t.mu.Unlock()
	return nil
}

// primeStatics reads fixed/slow mount facts once on connect: the stored site
// coordinates (SiteLatitude/Longitude/Elevation and local SiderealTime) and the model
// (CanFindHome). Best-effort: a failed read leaves the existing value.
func (t *Telescope) primeStatics(m *tenmicron.Mount) {
	if lat, err := m.SiteLatitude(); err == nil {
		t.mu.Lock()
		t.siteLat = lat
		t.mu.Unlock()
	}
	if lon, err := m.SiteLongitude(); err == nil {
		t.mu.Lock()
		t.siteLon = lon
		t.mu.Unlock()
	}
	if el, err := m.SiteElevation(); err == nil {
		t.mu.Lock()
		t.siteEl = el
		t.mu.Unlock()
	}
	if p, err := m.Product(); err == nil {
		t.mu.Lock()
		t.canHome = isHomingModel(p)
		t.mu.Unlock()
	}
}

// isHomingModel reports whether a 10Micron model supports a homing search (:hF#). The
// GM1000 family has no home sensor (FindHome is a no-op); larger mounts
// (GM3000/GM4000/AZ…) home.
func isHomingModel(product string) bool { return !strings.Contains(product, "GM1000") }

func (t *Telescope) mount() *tenmicron.Mount { t.mu.Lock(); defer t.mu.Unlock(); return t.m }

// LiveMount returns the currently-connected mount as a lx200.Mount (or
// ErrNotConnected), the seam the LX200 bridge (Stellarium/SkySafari) consumes to drive
// the same mount object. Called per operation, so a reconnect is picked up transparently.
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
	s, err := m.Get(lx200.Frame(cmd, raw))
	if err != nil {
		return "", err
	}
	if raw {
		// raw=true returns the device's exact reply; lx200 Get strips the '#'
		// terminator, but 10Micron replies are '#'-terminated and clients require it.
		s += "#"
	}
	return s, nil
}

func (t *Telescope) CommandBool(cmd string, raw bool) (bool, error) {
	m := t.mount()
	if m == nil {
		return false, alpacadev.ErrNotConnected
	}
	return m.Ack(lx200.Frame(cmd, raw))
}

// --- Capabilities (10Micron: German-equatorial; park + pulse-guide + move-axis +
// find-home) ---------------------------------------------------------------------
// :hF#/:h?# home commands work only on mounts with homing sensors (GM4000QCI /
// AZ2000QCI); on a GM1000HPS FindHome is a no-op.

func (t *Telescope) CanSlew() bool        { return true }
func (t *Telescope) CanSlewAsync() bool   { return true }
func (t *Telescope) CanSync() bool        { return true }
func (t *Telescope) CanSetTracking() bool { return true }
func (t *Telescope) CanPark() bool        { return true }
func (t *Telescope) CanUnpark() bool      { return true }
func (t *Telescope) CanFindHome() bool    { t.mu.Lock(); defer t.mu.Unlock(); return t.canHome }
func (t *Telescope) CanPulseGuide() bool  { return true }
func (t *Telescope) CanMoveAxis(axis alpacadev.TelescopeAxis) bool {
	return axis == alpacadev.AxisPrimary || axis == alpacadev.AxisSecondary
}

// --- Position / status getters ----------------------------------------------

// These return the last poller value from the snapshot — no synchronous mount I/O.
// Freshness is bounded by the poll cadence (slewPoll while slewing, else monitorPoll).
func (t *Telescope) RightAscension() float64 { return t.getF(&t.snap.ra) }
func (t *Telescope) Declination() float64    { return t.getF(&t.snap.dec) }
func (t *Telescope) Altitude() float64       { return t.getF(&t.snap.alt) }
func (t *Telescope) Azimuth() float64        { return t.getF(&t.snap.az) }
func (t *Telescope) Slewing() bool           { return t.getB(&t.snap.slewing) }
func (t *Telescope) Tracking() bool          { return t.getB(&t.snap.tracking) }
func (t *Telescope) AtPark() bool            { return t.getB(&t.snap.atPark) }
func (t *Telescope) AtHome() bool            { return t.getB(&t.snap.atHome) }

// SiderealTime is computed locally (mean LST from the system UTC clock + site
// longitude) rather than the mount's :GS# round-trip, keeping a frequently-polled call
// off the mount link. Tracks the mount's apparent LST to ~1 arcsec.
func (t *Telescope) SiderealTime() float64 {
	t.mu.Lock()
	lon := t.siteLon
	t.mu.Unlock()
	return localSiderealTime(time.Now().UTC(), lon)
}

// localSiderealTime returns mean local sidereal time in hours (0..24) for a UTC instant
// and site longitude in degrees east: GMST from the J2000 linear term, plus longitude.
func localSiderealTime(utc time.Time, lonDegEast float64) float64 {
	jd := 2440587.5 + float64(utc.Unix())/86400.0 + float64(utc.Nanosecond())/86400e9
	d := jd - 2451545.0 // days since J2000.0
	gmst := math.Mod(18.697374558+24.06570982441908*d, 24)
	lst := math.Mod(gmst+lonDegEast/15, 24)
	if lst < 0 {
		lst += 24
	}
	return lst
}

func (t *Telescope) FindHome() error {
	m := t.mount()
	if m == nil {
		return alpacadev.ErrNotConnected
	}
	return m.FindHome()
}

func (t *Telescope) SideOfPier() alpacadev.PierSide {
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
	m := t.mount()
	if m == nil {
		return alpacadev.ErrNotConnected
	}
	// Hold OpLock across set-target-then-slew so it can't interleave with the LX200
	// bridge and leave the single target register holding one client's RA, another's Dec.
	defer m.OpLock()()
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
	m := t.mount()
	if m == nil {
		return alpacadev.ErrNotConnected
	}
	defer m.OpLock()() // atomic vs the LX200 bridge — see SlewToCoordinatesAsync
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
	if err := m.PulseGuide(d, ms); err != nil {
		return err
	}
	// :Mg…# returns immediately; the mount guides autonomously for ms. Record the
	// window for IsPulseGuiding.
	t.mu.Lock()
	t.pulseUntil = time.Now().Add(time.Duration(ms) * time.Millisecond)
	t.mu.Unlock()
	return nil
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
