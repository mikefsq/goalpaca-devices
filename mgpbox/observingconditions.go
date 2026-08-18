// Package driver is the ASCOM Alpaca ObservingConditions device for the Astromi.ch
// MGPBox / MGPBox v2 (GPS + weather + dew-heater box), over the Go mikefsq/astromi.ch
// mgpbox library (FTDI USB-serial). Sibling of the other goalpaca-devices drivers; served
// standalone by cmd/mgpbox or hosted by alpacahurd.
//
// The MGPBox exposes four ambient sensors, mapped directly to ASCOM: Temperature,
// Humidity, Pressure (hPa), DewPoint. The remaining ObservingConditions properties stay at
// the BaseObservingConditions NotImplemented default (the box has no cloud/wind/rain/sky
// sensors; its GPS has no ASCOM ObservingConditions home).
//
// Unlike a request/response instrument, the MGPBox streams continuously — the library's
// background reader keeps the latest snapshot — so Refresh is a no-op and reads are served
// from the freshest streamed sample.
package driver

import (
	"context"
	"fmt"
	"log"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/mikefsq/astromi.ch/mgpbox"
	alpacadev "github.com/mikefsq/goalpaca/server"
)

var _ alpacadev.ObservingConditions = (*MGPBox)(nil)

// staleAfter marks the stream dead if no meteo sample arrives within this window; the
// supervisor then re-acquires. The box emits meteo roughly once per second.
const staleAfter = 30 * time.Second

// firstSampleWait bounds how long acquisition waits for the first streamed meteo sample,
// which both confirms the port is really an MGPBox and primes the snapshot.
const firstSampleWait = 6 * time.Second

// MGPBox adapts a mikefsq/astromi.ch MGPBox to the alpacadev.ObservingConditions +
// Hardware interfaces.
type MGPBox struct {
	alpacadev.BaseObservingConditions

	index  int
	serial string // FTDI USB-bridge serial to bind to; "" = discover

	mu  sync.Mutex
	dev *mgpbox.MGPBox // open handle; nil when no box is attached
	// feedTargets are the Alpaca devices the environment snapshot is pushed to —
	// typically a mount (refraction datums + site + time) and a dew controller
	// (temperature, humidity, dew point). Empty = feed off.
	feedTargets []FeedTarget
	// feedState is each failing target's retry bookkeeping, keyed by FeedTarget.String().
	// A target with no entry is healthy; entries are dropped on success and pruned when
	// the target list changes.
	feedState  map[string]*feedState
	feedTxn    uint32 // ClientTransactionID counter for feed requests
	gpsEnabled bool   // last-commanded GPS power state (the box can't report it); the GpsEnable read returns this

	openDev func() (*mgpbox.MGPBox, error) // injectable for tests
	now     func() time.Time               // clock, so tests can run backoff without sleeping
}

// SetFeedTargets replaces the environment-feed target list. Each target is an Alpaca
// device that accepts a setenvironment Action; the driver pushes the box's GPS + weather
// snapshot to every one of them on each cycle. An empty list disables the feed. Invalid
// targets are rejected as a group, so a typo cannot silently drop one consumer.
func (m *MGPBox) SetFeedTargets(targets []FeedTarget) error {
	norm := make([]FeedTarget, 0, len(targets))
	for _, t := range targets {
		n, err := t.normalize()
		if err != nil {
			return err
		}
		norm = append(norm, n)
	}
	m.mu.Lock()
	m.feedTargets = norm
	// Reconfiguring the feed clears every target's backoff: an operator changing the
	// list is saying "use this now", and should not have to wait out a retry delay
	// accumulated against the old one.
	m.feedState = make(map[string]*feedState, len(norm))
	m.mu.Unlock()
	return nil
}

// FeedTargets returns the current environment-feed target list.
func (m *MGPBox) FeedTargets() []FeedTarget {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]FeedTarget(nil), m.feedTargets...)
}

// SetMountFeed configures a single telescope feed target, the historical one-mount
// spelling. Empty addr disables the feed entirely. Prefer SetFeedTargets, which can
// also feed a dew controller.
func (m *MGPBox) SetMountFeed(addr string, device int) {
	if strings.TrimSpace(addr) == "" {
		_ = m.SetFeedTargets(nil)
		return
	}
	_ = m.SetFeedTargets([]FeedTarget{{Addr: addr, Type: "telescope", Device: device}})
}

// NewMGPBox creates the driver for the MGPBox at the given discovery index. Prefer
// NewMGPBoxBySerial for a stable identity.
func NewMGPBox(index int) *MGPBox {
	m := &MGPBox{index: index}
	m.init()
	m.ID = fmt.Sprintf("Astromi-MGPBox-oc%d", index)
	m.DevName = fmt.Sprintf("Astromi MGPBox %d", index)
	m.openDev = m.openByIndex
	return m
}

// NewMGPBoxBySerial binds the Alpaca device (devNum used only for the stable ID) to the
// MGPBox whose FTDI USB-bridge serial number is serial (read from the USB descriptor, so
// the binding survives replug / port renumbering and disambiguates it from other FTDI
// devices — e.g. a Unihedron SQM — sharing VID 0x0403).
func NewMGPBoxBySerial(devNum int, serial string) *MGPBox {
	m := &MGPBox{index: devNum, serial: serial}
	m.init()
	m.ID = "Astromi-MGPBox-" + serial
	m.DevName = "Astromi MGPBox " + serial
	m.openDev = m.openBySerial
	return m
}

func (m *MGPBox) init() {
	m.Version = "0.1.0"
	m.Info = "mgpbox — Astromi.ch MGPBox Alpaca ObservingConditions driver over Go mikefsq/astromi.ch"
	m.IfaceVer = alpacadev.InterfaceVersionObservingConditions
	m.gpsEnabled = true // the box powers its GPS at boot
	m.feedState = make(map[string]*feedState)
	m.now = time.Now
}

// --- Hardware lifecycle (mirrors the sibling unihedron device) ---

func (m *MGPBox) Open(ctx context.Context) error {
	if m.openDev == nil {
		m.openDev = m.openByIndex
	}
	go alpacadev.Supervise(ctx, m.ID, func() { m.manageHardware(ctx) })
	// The environment feed runs unconditionally and no-ops while no mount is configured,
	// so SetMountFeed can enable it at runtime (e.g. via the mountfeed Action).
	go alpacadev.Supervise(ctx, m.ID+"-feed", func() { m.feedLoop(ctx) })
	return nil
}

func (m *MGPBox) Close(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.dev != nil {
		m.dev.Close()
		m.dev = nil
	}
	return nil
}

func (m *MGPBox) Connect(ctx context.Context) error {
	if !m.Connected() {
		return alpacadev.ErrNotConnected
	}
	return nil
}

func (m *MGPBox) Connected() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.dev != nil
}

func (m *MGPBox) Disconnect(ctx context.Context) error { return nil }

// manageHardware owns the box for the process lifetime: it acquires the device, then
// watches the stream freshness and re-acquires if it goes stale.
func (m *MGPBox) manageHardware(ctx context.Context) {
	for ctx.Err() == nil {
		m.mu.Lock()
		present := m.dev != nil
		m.mu.Unlock()
		if !present {
			if m.tryAcquire() {
				log.Printf("mgpbox: MGPBox %s acquired", m.ID)
			} else {
				sleepCtx(ctx, 3*time.Second)
			}
			continue
		}
		if m.stale() {
			log.Printf("mgpbox: MGPBox %s stream stale; re-acquiring", m.ID)
			m.mu.Lock()
			if m.dev != nil {
				m.dev.Close()
				m.dev = nil
			}
			m.mu.Unlock()
		}
		sleepCtx(ctx, 5*time.Second)
	}
}

// stale reports whether the latest meteo sample is older than staleAfter (or absent).
func (m *MGPBox) stale() bool {
	m.mu.Lock()
	dev := m.dev
	m.mu.Unlock()
	if dev == nil {
		return true
	}
	me, ok := dev.Meteo()
	if !ok {
		return true
	}
	return time.Since(me.Time) > staleAfter
}

func (m *MGPBox) tryAcquire() bool {
	dev, err := m.openDev()
	if err != nil {
		return false
	}
	dev.EnableMeteo() // ensure meteo streaming is on
	dev.CalGet()
	// Wait for the first sample to confirm this really is a streaming MGPBox.
	deadline := time.Now().Add(firstSampleWait)
	for time.Now().Before(deadline) {
		if _, ok := dev.Meteo(); ok {
			m.mu.Lock()
			m.dev = dev
			m.Desc = "Astromi.ch MGPBox (FTDI serial; temperature/humidity/pressure/dewpoint)"
			m.mu.Unlock()
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	dev.Close()
	return false
}

func (m *MGPBox) openByIndex() (*mgpbox.MGPBox, error) {
	found, err := mgpbox.Discover()
	if err != nil {
		return nil, err
	}
	if m.index < 0 || m.index >= len(found) {
		return nil, fmt.Errorf("no MGPBox at index %d (found %d)", m.index, len(found))
	}
	return mgpbox.OpenPort(found[m.index].Port)
}

func (m *MGPBox) openBySerial() (*mgpbox.MGPBox, error) { return mgpbox.OpenBySerial(m.serial) }

// --- ObservingConditions members ---

// meteo returns the latest streamed weather snapshot, or ErrNotConnected when no device
// is attached or no sample has arrived yet.
func (m *MGPBox) meteo() (mgpbox.Meteo, error) {
	m.mu.Lock()
	dev := m.dev
	m.mu.Unlock()
	if dev == nil {
		return mgpbox.Meteo{}, alpacadev.ErrNotConnected
	}
	me, ok := dev.Meteo()
	if !ok {
		return mgpbox.Meteo{}, alpacadev.ErrNotConnected
	}
	return me, nil
}

// Temperature returns the ambient temperature, °C.
func (m *MGPBox) Temperature() (float64, error) {
	me, err := m.meteo()
	return me.Temperature, err
}

// Humidity returns the relative humidity, %RH.
func (m *MGPBox) Humidity() (float64, error) {
	me, err := m.meteo()
	return me.Humidity, err
}

// Pressure returns the barometric pressure, hPa (the ASCOM unit).
func (m *MGPBox) Pressure() (float64, error) {
	me, err := m.meteo()
	return me.Pressure, err
}

// magnusA/magnusB are the Magnus-formula coefficients (Sonntag, over water).
const magnusA, magnusB = 17.62, 243.12

// dewPointFrom computes the dew point (°C) from an air temperature (°C) and a
// relative humidity (%).
func dewPointFrom(tempC, humidity float64) float64 {
	if humidity <= 0 {
		humidity = 0.001 // ln(0) is -Inf
	}
	if humidity > 100 {
		humidity = 100
	}
	gamma := math.Log(humidity/100) + magnusA*tempC/(magnusB+tempC)
	return magnusB * gamma / (magnusA - gamma)
}

// resolveDewpoint returns this box's dew point: its own transducer reading when it
// has one, otherwise a value derived from the temperature and humidity it does
// report (Magnus). ok is false only when neither is possible — no transducer and
// no humidity to derive from — in which case there is genuinely no dew point to
// publish, and callers must omit it rather than publish a zero.
//
// It is the single source of truth for every dew-point surface this driver has
// (the ObservingConditions property, the scalar Action, and the environment feed),
// so they cannot disagree about the same sample.
func resolveDewpoint(me mgpbox.Meteo) (float64, bool) {
	if me.Dewpoint != 0 {
		return me.Dewpoint, true
	}
	if me.Humidity <= 0 {
		return 0, false
	}
	return dewPointFrom(me.Temperature, me.Humidity), true
}

// DewPoint returns the dew-point temperature, °C. Not every MGPBox carries the
// dew-point transducer; when the box reports none the value is derived from the
// temperature and humidity it does report.
//
// Deriving is the right answer rather than ErrNotImplemented: ASCOM links
// DewPoint, Humidity and Temperature — a driver implements all three or none —
// and this box implements the other two. Returning the unreported 0 would be worse
// than either, since a client cannot tell it from a real freezing dew point on a
// mild night, and anything steering dew heaters off it would read the margin as
// the whole air temperature and stay idle.
func (m *MGPBox) DewPoint() (float64, error) {
	me, err := m.meteo()
	if err != nil {
		return 0, err
	}
	dp, _ := resolveDewpoint(me) // no humidity and no transducer: 0 is all there is
	return dp, nil
}

// Refresh is a no-op: the MGPBox streams continuously, so reads are always served from the
// freshest sample. It errors only when disconnected.
func (m *MGPBox) Refresh() error {
	_, err := m.meteo()
	return err
}

// supported reports whether name is a sensor this box implements ("" = any).
func supported(name string) bool {
	switch strings.ToLower(name) {
	case "", "temperature", "humidity", "pressure", "dewpoint":
		return true
	}
	return false
}

// TimeSinceLastUpdate returns seconds since the latest streamed sample, for the sensors
// this box implements (or "" = any). The gate has already validated name against the
// canonical ASCOM sensor set.
func (m *MGPBox) TimeSinceLastUpdate(name string) (float64, error) {
	if !supported(name) {
		return 0, alpacadev.ErrNotImplemented
	}
	me, err := m.meteo()
	if err != nil {
		return -1, nil // ASCOM convention: negative when no value has been read yet
	}
	return time.Since(me.Time).Seconds(), nil
}

// SensorDescription describes the backing hardware for the sensors this box provides.
func (m *MGPBox) SensorDescription(name string) (string, error) {
	switch strings.ToLower(name) {
	case "temperature":
		return "Astromi.ch MGPBox ambient temperature (°C)", nil
	case "humidity":
		return "Astromi.ch MGPBox relative humidity (%RH)", nil
	case "pressure":
		return "Astromi.ch MGPBox barometric pressure (hPa)", nil
	case "dewpoint":
		return "Astromi.ch MGPBox dew point (°C; derived from temperature and humidity when the box reports none)", nil
	}
	return "", alpacadev.ErrNotImplemented
}

func sleepCtx(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
