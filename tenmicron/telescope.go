// Command tenmicron is a standalone ASCOM Alpaca Telescope driver for 10Micron
// GM-series mounts, built directly on the lx200/tenmicron protocol library.
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
	slewPoll    = 250 * time.Millisecond // faster refresh while slewing (responsive progress)
	fullPoll    = 5 * time.Second        // cadence for the slow-changing set (home/guide/refraction)

	// mountCacheTTL is the mount's :Ginfo# cache lifetime while this driver polls it.
	// Longer than monitorPoll so the LX200 bridge and INDI server (which read the same
	// mount live) ride the poller's cache between cycles instead of refetching; the
	// poller force-refreshes each cycle (pollOnce → Refresh), so its own reads stay live.
	mountCacheTTL = 2 * time.Second
)

// snapshot is the cached mount state every getter is served from. The background
// poller (pollOnce, driven by manage) refreshes it; a property GET is then a cheap
// locked read with no mount I/O — so a client never pays a round-trip per property or
// queues on the single mount link. This mirrors how a native ASCOM mount driver works.
type snapshot struct {
	ra, dec, alt, az                  float64
	guideRate                         float64 // deg/s (one rate, both axes)
	pier                              alpacadev.PierSide
	slewing, tracking, atPark, atHome bool
	doesRefraction                    bool
}

// Telescope is the 10Micron Alpaca Telescope device. It owns the mount for the
// life of the process; Connected ≡ mount reachable; Busy() gates writes while
// slewing.
type Telescope struct {
	alpacadev.BaseTelescope

	addr string

	mu   sync.Mutex
	m    *tenmicron.Mount // nil ⇔ not connected
	snap snapshot

	targetRA, targetDec      float64
	siteLat, siteLon, siteEl float64
	raRate, decRate          float64 // custom tracking-rate offsets (no mount read-back)
	trackingRate             alpacadev.DriveRate
	slewSettleSec            int
	pulseUntil               time.Time // PulseGuide completion deadline (IsPulseGuiding)
	canHome                  bool      // homing supported (model-derived at connect; see primeStatics)

	// Optics — instrument profile (the mount can't report it). Backed by an
	// OpticsStore so the fleet can inject a holder shared with the INDI front-end.
	optics alpacadev.OpticsStore
}

// NewTelescope builds the driver for the 10Micron mount at addr (host:port).
func NewTelescope(addr string) *Telescope {
	// canHome defaults true (most 10Micron mounts home); primeStatics narrows it from
	// the model on connect. A pre-connect read still reports the optimistic default.
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

// manage acquires, monitors and re-acquires the mount for the process lifetime. The
// monitor cycle IS the status poll: each tick refreshes the snapshot (pollOnce) that
// every getter is served from, so there is no separate health check and no
// per-property mount I/O. The cadence tightens while slewing for responsive progress.
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
				m.SetStatusTTL(mountCacheTTL) // poller is the sole :Ginfo# refresher; other front-ends ride its cache
				if perr := t.pollOnce(m, true); perr != nil { // prime the snapshot before clients see Connected
					m.Close()
					err = perr
				}
			}
			if err == nil {
				t.primeStatics(m) // stored site coords + model (CanFindHome); best-effort
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
		if err := t.pollOnce(m, full); err != nil { // the poll is also the liveness check
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

// pollOnce refreshes the snapshot from the mount — the background read every getter is
// served from. It does ONE forced :Ginfo# (m.Refresh) for the whole volatile set; that
// read also re-arms the mount's status cache, so the LX200 bridge and INDI server —
// which read the same mount live — ride this poller's value (with a cache TTL longer
// than the poll interval, set on connect) instead of each issuing their own round-trip
// on the single mount link. A :Ginfo# failure means the link is down and is returned so
// manage reconnects. When full, it also refreshes the slow-changing set
// (home/guide-rate/refraction) — separate round-trips done only every fullPoll;
// transient errors on those keep the last good value.
func (t *Telescope) pollOnce(m *tenmicron.Mount, full bool) error {
	st, err := m.Refresh() // forced :Ginfo#; re-arms the shared cache for the other front-ends
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

// primeStatics reads the fixed/slow mount facts once on connect: the stored site
// coordinates (so SiteLatitude/Longitude/Elevation — and the local SiderealTime —
// report the mount's real values instead of 0 until a client pushes them) and the
// model (so CanFindHome reflects whether this mount actually homes). Best-effort: a
// failed read leaves the existing value.
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
// GM1000 family has no home sensor — FindHome is a no-op and the native driver reports
// CanFindHome=false — so report false for it; the larger homing-capable mounts
// (GM3000/GM4000/AZ…) keep the default true. Narrow (only the confirmed no-home family)
// so the capability is never stripped from a mount that has it.
func isHomingModel(product string) bool { return !strings.Contains(product, "GM1000") }

func (t *Telescope) mount() *tenmicron.Mount { t.mu.Lock(); defer t.mu.Unlock(); return t.m }

// LiveMount returns the mount that is connected right now as a lx200.Mount, or
// ErrNotConnected. It is the seam an independent front-end (the LX200 bridge for
// Stellarium/SkySafari) consumes so it drives the same mount object this driver
// does — the single source of truth — rather than talking to this wrapper. The
// bridge calls it per operation, so a reconnect here is picked up transparently.
func (t *Telescope) LiveMount() (lx200.Mount, error) {
	if m := t.mount(); m != nil {
		return m, nil
	}
	return nil, alpacadev.ErrNotConnected
}

// --- ASCOM Command* passthrough -------------------------------------------------
// CommandBlind/String/Bool send a raw LX200 command the typed API doesn't wrap
// (e.g. a 10Micron extended command), mapping to the core Blind/Get/Ack reply
// shapes. lx200.Frame adds ':'…'#' framing unless raw. The server already gates
// these by Connected()/Busy() (so they're rejected mid-slew); the nil-guard covers
// the reconnect race.

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
		// ASCOM raw=true means "return the device's exact reply". lx200 Get strips the
		// '#' terminator (handy for typed reads), but a raw passthrough must be verbatim:
		// 10Micron replies are '#'-terminated and the NINA TenMicron plugin's ANTLR
		// parsers require the trailing '#' (and the unmodified comma/colon/'*' payload).
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
// Note: :hF#/:h?# home commands work only on mounts with homing sensors
// (GM4000QCI / AZ2000QCI); on a GM1000HPS FindHome is a no-op.

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

// These return the last value the background poller read into the snapshot — no
// synchronous mount I/O per call (see snapshot / pollOnce). Freshness is bounded by
// the poll cadence (slewPoll while slewing, else monitorPoll).
func (t *Telescope) RightAscension() float64 { return t.getF(&t.snap.ra) }
func (t *Telescope) Declination() float64    { return t.getF(&t.snap.dec) }
func (t *Telescope) Altitude() float64       { return t.getF(&t.snap.alt) }
func (t *Telescope) Azimuth() float64        { return t.getF(&t.snap.az) }
func (t *Telescope) Slewing() bool           { return t.getB(&t.snap.slewing) }
func (t *Telescope) Tracking() bool          { return t.getB(&t.snap.tracking) }
func (t *Telescope) AtPark() bool            { return t.getB(&t.snap.atPark) }
func (t *Telescope) AtHome() bool            { return t.getB(&t.snap.atHome) }

// SiderealTime is computed locally (mean LST from the system UTC clock + site
// longitude) rather than the mount's :GS# round-trip — it takes a frequent un-batched
// call (NINA polls it constantly) off the mount link entirely. The value tracks the
// mount's apparent LST to ~1 arcsec (~0.07 s), negligible for display and flip timing,
// and relies only on the box clock, which the driver already trusts for UTCDate.
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

// Driver-remembered properties (the mount does not read these back).
func (t *Telescope) TargetRightAscension() float64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.targetRA
}
func (t *Telescope) TargetDeclination() float64 { t.mu.Lock(); defer t.mu.Unlock(); return t.targetDec }
func (t *Telescope) SiteLatitude() float64      { t.mu.Lock(); defer t.mu.Unlock(); return t.siteLat }
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
	t.mu.Lock()
	t.targetRA = ra
	t.mu.Unlock()
	return nil
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
	t.mu.Lock()
	t.targetDec = dec
	t.mu.Unlock()
	return nil
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
	// Hold the mount's OpLock across the whole set-target-then-slew sequence so it
	// cannot interleave with the LX200 bridge's, which would leave the device's
	// single target register holding one client's RA with another's Dec.
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
	// :Mg…# returns immediately; the mount guides autonomously for ms, so record
	// the window for IsPulseGuiding.
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
