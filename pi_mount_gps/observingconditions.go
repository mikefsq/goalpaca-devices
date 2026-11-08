// Package driver exposes the pi_mount_gps board through ASCOM Alpaca
// ObservingConditions: SHT45 temperature and humidity, the dew point derived
// from them, and BMP580 station pressure. The board library polls the sensors
// and the GPS in the background, so property GETs are served from its cache and
// never block on I2C. The driver also feeds weather, site and time to other
// Alpaca devices such as a 10Micron mount (see feed.go).
package driver

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"

	alpacadev "github.com/mikefsq/goalpaca/server"
	pimountgps "github.com/mikefsq/x1500_gps_12v/driver"
)

var _ alpacadev.ObservingConditions = (*Driver)(nil)

// checkInterval is how often manageHardware checks an open board's health.
const checkInterval = 5 * time.Second

// board is the part of *pimountgps.Board the driver uses; tests substitute a fake.
type board interface {
	Weather() pimountgps.Weather
	WeatherAge() (time.Duration, bool)
	WeatherStale() bool
	WeatherErr() error
	HasHumidity() bool
	HasPressure() bool
	Refresh() error
	Fix() (pimountgps.Fix, error)
	FixAge() (time.Duration, bool)
	FixStale() bool
	Close() error
}

// Driver adapts the pi_mount_gps board to alpacadev.ObservingConditions, and
// pushes its weather, position and time to other Alpaca devices (see feed.go).
type Driver struct {
	stopLoop        func(time.Duration)
	stopFeed        func(time.Duration)
	stopGpsd        func(time.Duration)
	stopGPSFallback func(time.Duration)
	gpsMu           sync.Mutex
	gpsBoard        board // opened only while gpsd has no usable fix
	openGPS         func() (board, error)
	alpacadev.BaseObservingConditions

	mu    sync.Mutex
	board board // nil when the board is not open
	// feedTargets are the Alpaca devices the environment snapshot is pushed to.
	// Empty = feed off.
	feedTargets []FeedTarget
	// feedState is each target's retry bookkeeping, keyed by FeedTarget.String().
	feedState map[string]*feedState
	feedTxn   uint32 // ClientTransactionID counter for feed requests
	// timeSource is where the last push took its time from (timeChrony,
	// timeGPS or timeNone); "" before the first push.
	timeSource string

	temperatureOffset float64     // guarded by mu; survives hardware reopen
	gpsd              *gpsdClient // nil when gpsd is disabled

	openBoard   func() (board, error) // injectable for tests
	clockSynced func() bool           // whether chrony has the system clock synchronised
	now         func() time.Time
}

// NewDriver creates the driver for the board wired as cfg. gpsdAddr is the
// gpsd to prefer for the GPS fix; "" disables it.
func NewDriver(cfg pimountgps.Config, gpsdAddr string) *Driver {
	w := &Driver{temperatureOffset: cfg.EnclosureHeatingC}
	w.ID = "pi_mount_gps-oc0"
	w.DevName = "pi_mount_gps"
	w.Desc = "pi_mount_gps board: SHT45 temperature/humidity, BMP580 pressure, u-blox GPS"
	w.Version = "0.1.0"
	w.Info = "pi_mount_gps — ObservingConditions driver over Go mikefsq/pi_mount_gps"
	w.IfaceVer = alpacadev.InterfaceVersionObservingConditions
	w.feedState = make(map[string]*feedState)
	w.clockSynced = chronySynced
	w.now = time.Now
	if gpsdAddr != "" {
		w.gpsd = newGpsdClient(gpsdAddr)
	}
	w.openBoard = func() (board, error) {
		b, warnings, err := pimountgps.OpenSubsystems(cfg, pimountgps.SubsystemWeather)
		for _, warn := range warnings {
			log.Printf("pi_mount_gps: %v", warn)
		}
		if err != nil {
			return nil, err
		}
		return b, nil
	}
	w.openGPS = func() (board, error) {
		b, warnings, err := pimountgps.OpenSubsystems(cfg, pimountgps.SubsystemGPS)
		for _, warn := range warnings {
			log.Printf("pi_mount_gps: GPS fallback: %v", warn)
		}
		if err != nil {
			return nil, err
		}
		return b, nil
	}
	return w
}

// SetFeedTargets replaces the environment-feed target list. Each target is an
// Alpaca device that accepts a setenvironment Action. An empty list disables the
// feed. Invalid targets are rejected as a group, so a typo cannot silently drop
// one consumer.
func (w *Driver) SetFeedTargets(targets []FeedTarget) error {
	norm := make([]FeedTarget, 0, len(targets))
	for _, t := range targets {
		n, err := t.normalize()
		if err != nil {
			return err
		}
		norm = append(norm, n)
	}
	w.mu.Lock()
	w.feedTargets = norm
	// A new list clears every target's backoff: the operator wants it used now.
	w.feedState = make(map[string]*feedState, len(norm))
	w.mu.Unlock()
	return nil
}

// FeedTargets returns the current environment-feed target list.
func (w *Driver) FeedTargets() []FeedTarget {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]FeedTarget(nil), w.feedTargets...)
}

func (w *Driver) Open(ctx context.Context) error {
	w.stopGPSFallback = alpacadev.RunLoop(ctx, w.ID+"-gps-fallback", w.manageGPSFallback)
	w.stopLoop = alpacadev.RunLoop(ctx, w.ID, w.manageHardware)
	// The feed runs unconditionally and no-ops while no target is configured, so
	// the Feed action can enable it at runtime.
	w.stopFeed = alpacadev.RunLoop(ctx, w.ID+"-feed", w.feedLoop)
	if w.gpsd != nil {
		w.stopGpsd = alpacadev.RunLoop(ctx, w.ID+"-gpsd", w.gpsd.run)
	}
	return nil
}

func (w *Driver) Close(ctx context.Context) error {
	if w.stopGPSFallback != nil {
		w.stopGPSFallback(10 * time.Second)
	}
	if w.stopFeed != nil {
		w.stopFeed(15 * time.Second)
	}
	if w.stopGpsd != nil {
		w.stopGpsd(5 * time.Second)
	}
	if w.stopLoop != nil {
		w.stopLoop(10 * time.Second)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.board != nil {
		w.board.Close()
		w.board = nil
	}
	return nil
}

func (w *Driver) Connect(ctx context.Context) error {
	if !w.Connected() {
		return alpacadev.ErrNotConnected
	}
	return nil
}

func (w *Driver) Connected() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.board != nil
}

func (w *Driver) Disconnect(ctx context.Context) error { return nil }

// manageHardware owns the board for the process lifetime: it opens it, and
// reopens it if the sensors stop producing readings. The board itself does the
// polling.
func (w *Driver) manageHardware(ctx context.Context) {
	for ctx.Err() == nil {
		w.mu.Lock()
		b := w.board
		w.mu.Unlock()
		if b == nil {
			nb, err := w.openBoard()
			if err != nil {
				sleepCtx(ctx, 3*time.Second)
				continue
			}
			w.mu.Lock()
			if setter, ok := nb.(interface{ SetEnclosureHeating(float64) error }); ok {
				if err := setter.SetEnclosureHeating(w.temperatureOffset); err != nil {
					w.mu.Unlock()
					nb.Close()
					log.Printf("pi_mount_gps: temperature offset: %v", err)
					sleepCtx(ctx, 3*time.Second)
					continue
				}
			}
			w.board = nb
			w.mu.Unlock()
			log.Printf("pi_mount_gps: %s acquired", w.ID)
			continue
		}
		sleepCtx(ctx, checkInterval)
		if _, ever := b.WeatherAge(); ever && b.WeatherStale() {
			w.mu.Lock()
			if w.board == b { // still ours — a concurrent Close may have cleared it
				log.Printf("pi_mount_gps: %s readings stale (%v); reopening", w.ID, b.WeatherErr())
				b.Close()
				w.board = nil
			}
			w.mu.Unlock()
		}
	}
}

// weather returns the cached conditions, or an error when the board is closed
// or its readings are missing or stale.
func (w *Driver) weather() (pimountgps.Weather, error) {
	w.mu.Lock()
	b := w.board
	w.mu.Unlock()
	if b == nil {
		return pimountgps.Weather{}, alpacadev.ErrNotConnected
	}
	if b.WeatherStale() {
		return pimountgps.Weather{}, alpacadev.ErrValueNotSet
	}
	return b.Weather(), nil
}

// Temperature returns the SHT45 air temperature, °C.
func (w *Driver) Temperature() (float64, error) {
	r, err := w.weather()
	return r.Temperature, err
}

// Humidity returns the SHT45 relative humidity, %.
func (w *Driver) Humidity() (float64, error) {
	r, err := w.weather()
	return r.Humidity, err
}

// DewPoint returns the dew point derived from temperature and humidity, °C.
func (w *Driver) DewPoint() (float64, error) {
	r, err := w.weather()
	return r.Dewpoint, err
}

// Pressure returns the BMP580 station pressure, hPa, which is what ASCOM asks
// for (the pressure at the observatory, not reduced to sea level).
func (w *Driver) Pressure() (float64, error) {
	r, err := w.weather()
	return r.Pressure, err
}

// SetAveragePeriod accepts only 0: readings are instantaneous.
func (w *Driver) SetAveragePeriod(hours float64) error {
	if hours != 0 {
		return alpacadev.ErrInvalidValue
	}
	return nil
}

// Refresh reads the sensors now.
func (w *Driver) Refresh() error {
	w.mu.Lock()
	b := w.board
	w.mu.Unlock()
	if b == nil {
		return alpacadev.ErrNotConnected
	}
	return b.Refresh()
}

// supported reports whether name is a sensor this board implements ("" = any).
func supported(name string) bool {
	switch strings.ToLower(name) {
	case "", "temperature", "humidity", "dewpoint", "pressure":
		return true
	}
	return false
}

// TimeSinceLastUpdate returns seconds since the last good reading, or -1 before
// the first one.
func (w *Driver) TimeSinceLastUpdate(name string) (float64, error) {
	if !supported(name) {
		return 0, alpacadev.ErrNotImplemented
	}
	w.mu.Lock()
	b := w.board
	w.mu.Unlock()
	if b == nil {
		return 0, alpacadev.ErrNotConnected
	}
	age, ok := b.WeatherAge()
	if !ok {
		return -1, nil
	}
	return age.Seconds(), nil
}

// SensorDescription describes the hardware behind each sensor.
func (w *Driver) SensorDescription(name string) (string, error) {
	switch strings.ToLower(name) {
	case "temperature":
		return "Sensirion SHT45 air temperature (°C)", nil
	case "humidity":
		return "Sensirion SHT45 relative humidity (%)", nil
	case "dewpoint":
		return "Dew point from SHT45 temperature and humidity, Magnus formula (°C)", nil
	case "pressure":
		return "Bosch BMP580 station pressure (hPa)", nil
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
