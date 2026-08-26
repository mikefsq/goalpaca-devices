// Package driver is the ASCOM Alpaca Telescope device for Rainbow Astro RST
// harmonic mounts (RST-135/300), over the lx200/rst protocol library
// (USB-serial). It is served standalone by cmd/rst and hosted by alpacahurd.
package driver

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"strings"
	"sync"
	"time"

	alpacadev "github.com/mikefsq/goalpaca/server"
	lx200 "github.com/mikefsq/lx200"
	"github.com/mikefsq/lx200/rst"
)

const (
	maxAxisRate = rst.AxisRateMax  // advertised MoveAxis ceiling (deg/s) — the vendor's fastest
	minAxisRate = rst.AxisRateSlow // advertised floor — sidereal; zero is "stop", not a rate
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
	// stopLoop ends the loop Open started and waits for it. Close calls it
	// before releasing the handle, so a reload's replacement opens the hardware
	// with no old loop left to re-acquire it (server.RunLoop).
	stopLoop func(time.Duration)
	alpacadev.BaseTelescope

	serial string // USB bridge serial to bind; empty = ask every candidate and take the one that answers

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

// NewTelescope builds the driver. Since FTDI 0403:6001 is common we have to validate the device is an RST.
func NewTelescope(serial string) *Telescope {
	t := &Telescope{serial: serial, trackingRate: alpacadev.DriveSidereal, optics: &localOptics{}}
	t.IfaceVer = alpacadev.InterfaceVersionTelescope
	t.Version = "0.1.0"
	t.Info = "rst — Rainbow Astro RST Alpaca telescope driver over mikefsq/lx200"
	return t
}

// dial finds and opens the mount.
func (t *Telescope) dial() (*rst.Mount, error) {
	m, rep, err := rst.FindMatching(rst.Filter{Serial: t.serial})
	if err != nil {
		return nil, err
	}
	if rep.Serial != "" {
		log.Printf("rst: mount found on USB bridge %s", rep.Serial)
	}
	return m, nil
}

// --- Hardware lifecycle + connection model ----------------------------------

// Open starts the supervised background loop that dials the mount and keeps it
// connected. It touches no hardware itself.
func (t *Telescope) Open(ctx context.Context) error {
	t.stopLoop = alpacadev.RunLoop(ctx, t.ID, t.manage)
	return nil
}

// Close disconnects from the mount and releases the serial port.
func (t *Telescope) Close(ctx context.Context) error {
	if t.stopLoop != nil {
		t.stopLoop(10 * time.Second) // end the loop Open started before the handle goes
	}
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
func (t *Telescope) CanPark() bool { return (&rst.Mount{}).AlpacaCanPark() }

// CanUnpark reports that the mount can unpark.
func (t *Telescope) CanUnpark() bool { return (&rst.Mount{}).AlpacaCanUnpark() }

// CanFindHome reports that the mount can seek its mechanical home.
func (t *Telescope) CanFindHome() bool { return (&rst.Mount{}).AlpacaCanFindHome() }

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

// AtPark reports whether the mount is parked at the polar axis, and stopped.
//
// The driver reads the mount's MECHANICAL axis angles (:CY#) rather than its position on the
// sky: the polar-axis park and a plain goto to the celestial pole point at the same place, so
// only the RA-axis rotation distinguishes the intended stow from a mount lying on its side.
// Reading the mount rather than a latch also means reconnecting to a mount someone else parked
// still reports parked. Unpark does not move the telescope, so this goes false on the state
// change while the tube stays where it is.
func (t *Telescope) AtPark() bool {
	if m := t.mount(); m != nil {
		if v, err := m.AlpacaAtPark(); err == nil {
			return t.setB(&t.snap.atPark, v)
		}
	}
	return t.getB(&t.snap.atPark)
}

// AtHome reports whether the mount is physically at its home position, and stopped, by reading
// the mechanical axis angles (:CY#) and requiring both at zero.
//
// Not the horizon coordinates: those are derived from the mount's pointing model, which reports
// a plausible position even when its mechanical reference is stale. Observed on hardware as
// Az 270.0 / Alt 0.0 — exactly home — with the RA axis nowhere near it.
//
// Deliberately not the mount's :AH#, which reports whether a home seek is RUNNING and reads
// false immediately after one succeeds. AtHome is also distinct from HomeFound, the latch that
// gates gotos.
func (t *Telescope) AtHome() bool {
	if m := t.mount(); m != nil {
		if v, err := m.AlpacaAtHome(); err == nil {
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

// TrackingRate returns the current tracking rate (sidereal/lunar/solar), read from the mount.
//
// It used to return only what this driver had last been told to set, which meant a rate changed
// from the handset, or any reconnect, reported sidereal regardless of what the mount was doing.
// The mount answers :Ct?#, so there is no reason to guess.
//
// The mount has a fourth mode, custom (:CT3), with no ASCOM DriveRate to map it to. In that
// case the last known value is returned rather than a wrong one: DriveRates is an enum with no
// "other" member, so there is nothing truthful to say.
func (t *Telescope) TrackingRate() alpacadev.DriveRate {
	if m := t.mount(); m != nil {
		if tm, err := m.TrackMode(); err == nil {
			if r, ok := driveRateOf(tm); ok {
				t.mu.Lock()
				t.trackingRate = r
				t.mu.Unlock()
				return r
			}
		}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.trackingRate
}

// driveRateOf maps the mount's tracking mode onto an ASCOM DriveRate. ok is false for the
// mount's custom rate, which ASCOM cannot express.
func driveRateOf(tm rst.TrackMode) (alpacadev.DriveRate, bool) {
	switch tm {
	case rst.TrackModeSidereal:
		return alpacadev.DriveSidereal, true
	case rst.TrackModeSolar:
		return alpacadev.DriveSolar, true
	case rst.TrackModeLunar:
		return alpacadev.DriveLunar, true
	}
	return 0, false // TrackModeCustom
}

// TrackingRates returns the tracking rates the RST supports.
func (t *Telescope) TrackingRates() []alpacadev.DriveRate {
	return []alpacadev.DriveRate{alpacadev.DriveSidereal, alpacadev.DriveLunar, alpacadev.DriveSolar}
}

// UTCDate returns the MOUNT's UTC time as an ISO-8601 string, reconstructed from its local
// clock (:GL#), its date (:GC#) and its UTC offset (:GG#).
//
// This used to return the host clock, on the belief that the RST had no clock-read command.
// It has three, and the difference is not academic: a mount whose clock is wrong computes a
// wrong hour angle and points somewhere else entirely. The development mount was found three
// hours fast — a 45 degree pointing error — while this member cheerfully reported the host's
// correct time, so nothing downstream could notice.
//
// If the mount cannot be read this returns an EMPTY STRING rather than falling back to the host
// clock. The ASCOM property has no error to return — a client that cannot be told "this read
// failed" is better served by a value it cannot parse than by a plausible one that is not the
// mount's. Host time is the more dangerous answer precisely because it looks right: it is the
// same substitution that hid the three-hour error, differing only in which code path produces
// it. An empty string cannot be mistaken for a timestamp.
func (t *Telescope) UTCDate() string {
	m := t.mount()
	if m == nil {
		return ""
	}
	s, err := mountUTC(m)
	if err != nil {
		return ""
	}
	return s
}

const utcLayout = "2006-01-02T15:04:05.000Z"

// mountUTC assembles the mount's own UTC time. :GC# gives MM/DD/YY, :GL# the local time as
// hours since midnight, and :GG# the offset to ADD to local to reach UTC (LX200 convention,
// so a Pacific mount reports +7).
//
// :GC# is the LOCAL date, not the UTC one, and the two disagree for part of every day. Observed
// on hardware: at 00:02 UTC the mount reported 08/25/26 with a local time of 17:03, because it
// was still the 25th in PDT. Adding the offset to local midnight carries the day correctly;
// treating :GC# as a UTC date would put the result 24 hours out for those hours.
//
// The century is assumed to be 2000+yy — the mount sends two digits and never says.
func mountUTC(m *rst.Mount) (string, error) {
	date, err := m.Date() // "MM/DD/YY"
	if err != nil {
		return "", err
	}
	local, err := m.LocalTime() // hours since midnight
	if err != nil {
		return "", err
	}
	off, err := m.UTCOffset() // hours to add to local for UTC
	if err != nil {
		return "", err
	}
	var mm, dd, yy int
	if _, err := fmt.Sscanf(date, "%d/%d/%d", &mm, &dd, &yy); err != nil {
		return "", fmt.Errorf("rst: cannot parse the mount date %q: %w", date, err)
	}
	midnight := time.Date(2000+yy, time.Month(mm), dd, 0, 0, 0, 0, time.UTC)
	utc := midnight.
		Add(time.Duration(local * float64(time.Hour))).
		Add(time.Duration(off * float64(time.Hour)))
	return utc.Format(utcLayout), nil
}

// --- Setters ----------------------------------------------------------------

// SetUTCDate sets the mount's clock from an ISO-8601 UTC timestamp.
//
// The mount's UTC offset is left alone — the handset owns the site's civil time, and rewriting
// it from a client's timezone would silently move it. So the timestamp is converted through the
// offset the mount already holds and written as local time (:SL#).
//
// The DATE is written only if the mount's differs from the one implied by the timestamp. :SC#
// triggers a planetary-data recompute, so a routine clock sync — where the date is already
// right — costs one extra :GC# read and sends nothing. But a mount on the wrong date has no
// other way to be corrected over the wire, and the date feeds sidereal time: an hour angle
// computed from the wrong day is a pointing error with no symptom.
func (t *Telescope) SetUTCDate(iso string) error {
	m := t.mount()
	if m == nil {
		return alpacadev.ErrNotConnected
	}
	utc, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		if utc, err = time.Parse(utcLayout, iso); err != nil {
			return alpacadev.NewError(alpacadev.ErrNumInvalidValue, "UTCDate must be an ISO-8601 timestamp")
		}
	}
	return m.SetUTC(utc.UTC())
}

// SetTracking turns tracking on or off.
func (t *Telescope) SetTracking(on bool) error {
	// Only turning tracking ON is motion; a parked mount must still be able to stop tracking.
	if on {
		if err := t.refuseIfParked("Tracking = true"); err != nil {
			return err
		}
	}
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

// refuseIfParked returns a Parked error when the mount is parked, naming the member.
//
// The Alpaca HTTP layer already gates every motion member this way, so over the network this
// changes nothing. It exists for the callers that do NOT go through HTTP — alpacahurd holds
// this Telescope directly and calls these methods as Go functions, which bypasses the gate
// entirely. A guarantee that only holds on one of two call paths is not a guarantee.
func (t *Telescope) refuseIfParked(member string) error {
	if t.AtPark() {
		return alpacadev.NewError(alpacadev.ErrNumParked, member+" is not valid while the mount is parked")
	}
	return nil
}

// AbortSlew immediately stops any slew or continuous move.
func (t *Telescope) AbortSlew() error {
	m := t.mount()
	if m == nil {
		return alpacadev.ErrNotConnected
	}
	// A home seek is the one thing this cannot stop: :Q# interrupts the firmware routine that
	// clears the mount's homing guard, and on the development mount that left the guard set
	// until a power cycle, with :Ch# a silent no-op in the meantime. The driver refuses rather
	// than sending it; the seek ends on its own.
	return ascomErr(m.Halt())
}

// SlewToCoordinatesAsync starts an asynchronous goto to the given RA/Dec, returning
// immediately.
func (t *Telescope) SlewToCoordinatesAsync(ra, dec float64) error {
	if err := t.refuseIfParked("SlewToCoordinatesAsync"); err != nil {
		return err
	}
	if err := t.SetTargetRightAscension(ra); err != nil {
		return err
	}
	if err := t.SetTargetDeclination(dec); err != nil {
		return err
	}
	return t.startSlew()
}

// CanSlewAltAz and CanSlewAltAzAsync report that the mount can goto horizontal coordinates.
// It can, and does: Park is an alt/az goto to the celestial pole. These previously reported
// false, declining a capability the driver was already using internally.
func (t *Telescope) CanSlewAltAz() bool      { return true }
func (t *Telescope) CanSlewAltAzAsync() bool { return true }

// SlewToAltAzAsync starts a goto to horizontal coordinates (:Sz#/:Sa# then :MA#).
//
// Like the equatorial goto this is gated on the mount having homed — the West-horizon
// reference is what its pointing model is built from, and an un-homed alt/az goto is refused.
func (t *Telescope) SlewToAltAzAsync(az, alt float64) error {
	if err := t.refuseIfParked("SlewToAltAzAsync"); err != nil {
		return err
	}
	m := t.mount()
	if m == nil {
		return alpacadev.ErrNotConnected
	}
	if az < 0 || az >= 360 {
		return alpacadev.NewError(alpacadev.ErrNumInvalidValue, "azimuth must be in [0,360)")
	}
	if alt < -90 || alt > 90 {
		return alpacadev.NewError(alpacadev.ErrNumInvalidValue, "altitude must be in [-90,90]")
	}
	if err := m.SlewToAltAz(az, alt); err != nil {
		return err
	}
	t.setB(&t.snap.slewing, true)
	return nil
}

// SlewToAltAz gotos horizontal coordinates and blocks until the slew completes.
func (t *Telescope) SlewToAltAz(az, alt float64) error {
	if err := t.SlewToAltAzAsync(az, alt); err != nil {
		return err
	}
	return t.waitSlew()
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
	if err := t.refuseIfParked("SlewToTarget"); err != nil {
		return err
	}
	m := t.mount()
	if m == nil {
		return alpacadev.ErrNotConnected
	}
	return ascomErr(m.SlewToTarget())
}

// ascomErr gives the mount's own refusals a specific ASCOM error number. Left alone they fall
// through to Unspecified (0x4FF), which tells a client nothing it can act on.
//
// A goto the mount refuses — answered with an "MSZZ#" frame — is an InvalidOperation: the
// request was well formed, the mount declined it in its current state. A goto refused because
// the mount has not homed is the same class. Parked is its own ASCOM number.
func ascomErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, rst.ErrHoming):
		// The request was well formed and the mount declined it in its current state, which is
		// what InvalidOperation means. Not Parked, and not a fault.
		return alpacadev.NewError(alpacadev.ErrNumInvalidOperation, err.Error())
	case errors.Is(err, rst.ErrParked):
		return alpacadev.NewError(alpacadev.ErrNumParked, err.Error())
	case errors.Is(err, rst.ErrNotImplemented):
		return alpacadev.NewError(alpacadev.ErrNumNotImplemented, err.Error())
	case strings.Contains(err.Error(), "refused"), strings.Contains(err.Error(), "homed"):
		return alpacadev.NewError(alpacadev.ErrNumInvalidOperation, err.Error())
	}
	return err
}

// SyncToCoordinates syncs the pointing model to the given RA/Dec.
func (t *Telescope) SyncToCoordinates(ra, dec float64) error {
	if err := t.refuseIfParked("SyncToCoordinates"); err != nil {
		return err
	}
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
	if err := t.refuseIfParked("SyncToTarget"); err != nil {
		return err
	}
	m := t.mount()
	if m == nil {
		return alpacadev.ErrNotConnected
	}
	_, err := m.SyncToTarget()
	return err
}

// CanSetPark reports that the park position cannot be redefined.
//
// This mount has two mechanical positions and both are fixed: home is the West horizon, park is
// the polar axis with the OTA along the RA axis. There is no arbitrary park to set.
func (t *Telescope) CanSetPark() bool { return (&rst.Mount{}).AlpacaCanSetPark() }

// SetPark is not supported — see CanSetPark.
func (t *Telescope) SetPark() error {
	m := t.mount()
	if m == nil {
		return alpacadev.ErrNotConnected
	}
	return ascomErr(m.AlpacaSetPark())
}

// Park sends the mount to its park position — the polar axis, OTA along the RA axis and the
// tube on top — with tracking off.
//
// This is an EQUATORIAL goto, to an hour angle of +6h and a declination just short of 90. The
// hour angle is what pins the RA axis: the pole is an RA singularity, so an alt/az goto to the
// same sky position leaves the rotation wherever the path ended, tube out to one side. It runs
// at the mount's slot-3 speed and reports intermediate positions, so Slewing and the
// coordinates both track progress. Park() completes when AtPark becomes true.
func (t *Telescope) Park() error {
	m := t.mount()
	if m == nil {
		return alpacadev.ErrNotConnected
	}
	return ascomErr(m.AlpacaPark())
}

// Unpark clears the parked state and does not move the telescope or enable tracking.
func (t *Telescope) Unpark() error {
	m := t.mount()
	if m == nil {
		return alpacadev.ErrNotConnected
	}
	return ascomErr(m.AlpacaUnpark())
}

// FindHome zeros the encoders, for rst-135e at the west horizon.
func (t *Telescope) FindHome() error {
	m := t.mount()
	if m == nil {
		return alpacadev.ErrNotConnected
	}
	if err := ascomErr(m.AlpacaFindHome()); err != nil {
		return err
	}
	// The mount keeps answering :GR#/:GD#/:GZ#/:GA# with its STARTING position for the whole
	// seek, so the cache would otherwise hold a plausible, wrong, unchanging fix.
	t.zeroPosition()
	return nil
}

// zeroPosition clears the cached pointing. See FindHome for why.
func (t *Telescope) zeroPosition() {
	t.mu.Lock()
	t.snap.ra, t.snap.dec, t.snap.alt, t.snap.az = 0, 0, 0, 0
	t.mu.Unlock()
}

// PulseGuide issues a timed guide pulse in the given direction for ms milliseconds.
func (t *Telescope) PulseGuide(dir alpacadev.GuideDirection, ms int) error {
	if err := t.refuseIfParked("PulseGuide"); err != nil {
		return err
	}
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
//
// The minimum is the sidereal rate, not zero. Zero is not a rate the mount can be asked to move
// at — it is the stop command, always accepted and never advertised — and publishing a range
// that starts at zero is a conformance error rather than a generosity.
func (t *Telescope) AxisRates(axis alpacadev.TelescopeAxis) []alpacadev.AxisRate {
	if axis != alpacadev.AxisPrimary && axis != alpacadev.AxisSecondary {
		return []alpacadev.AxisRate{}
	}
	return []alpacadev.AxisRate{{Minimum: minAxisRate, Maximum: maxAxisRate}}
}

// MoveAxis starts a continuous slew on the given axis at rate deg/s (rate 0 stops it).
func (t *Telescope) MoveAxis(axis alpacadev.TelescopeAxis, rate float64) error {
	// Rate zero is a stop, and stopping a parked mount is not motion — it is always allowed.
	if rate != 0 {
		if err := t.refuseIfParked("MoveAxis"); err != nil {
			return err
		}
	}
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
	// MoveAxisRate programs the mount's speed slot before selecting it. Snapping to a preset
	// letter — which this used to do — only selects a slot, so a request for 2 deg/s was
	// delivered at whatever that slot held (0.42 deg/s on the development mount).
	return m.MoveAxisRate(a, rate > 0, math.Abs(rate))
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
				return alpacadev.NewError(alpacadev.ErrNumInvalidOperation, "slew aborted at "+f)
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
