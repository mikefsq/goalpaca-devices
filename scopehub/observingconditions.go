package driver

import (
	"context"
	"strings"
	"sync/atomic"
	"time"

	alpacadev "github.com/mikefsq/goalpaca/server"
)

var _ alpacadev.ObservingConditions = (*ObservingConditions)(nil)

// ObservingConditions is ASCOM ObservingConditions device for one board's
// sensors. Shares board with Switch of same serial.
type ObservingConditions struct {
	alpacadev.BaseObservingConditions
	b    *board
	held atomic.Bool // b is shared board; Open sets, Close clears
}

// NewObservingConditions creates device. Touches no hardware.
func NewObservingConditions(cfg Config) *ObservingConditions {
	o := &ObservingConditions{b: newBoard(cfg.Serial)}
	o.Version = "0.2.0"
	o.Info = "scopehub: scope_hub USB hub controller over USB HID"
	o.IfaceVer = alpacadev.InterfaceVersionObservingConditions
	o.ID = "ScopeHub-oc"
	if cfg.Serial != "" {
		o.ID += "-" + cfg.Serial
	}
	o.DevName = "Scope Hub Weather"
	o.Desc = "scope_hub: SHT45 temperature/humidity, BMP581 pressure, NEO-M9N GPS"
	return o
}

// Open takes reference on shared board. Failed Open keeps it, as Switch.Open.
func (o *ObservingConditions) Open(ctx context.Context) error {
	if !o.held.Load() {
		o.b = o.b.share()
		o.held.Store(true)
	}
	return o.b.Open(ctx)
}

// Close drops reference. Board closes when no device holds it.
func (o *ObservingConditions) Close(ctx context.Context) error {
	if o.held.Swap(false) {
		o.b.release()
	}
	return nil
}

// Connected means hardware present: board held and answering a status read. Dropped handle
// reopens here.
func (o *ObservingConditions) Connected() bool { return o.held.Load() && o.b.present() == nil }

// Connect succeeds only with hardware present. Else NotConnected, same as Switch.
func (o *ObservingConditions) Connect(ctx context.Context) error {
	if !o.Connected() {
		return alpacadev.ErrNotConnected
	}
	return nil
}

// Disconnect leaves board to Switch and later requests.
func (o *ObservingConditions) Disconnect(ctx context.Context) error { return nil }

func (o *ObservingConditions) snapshot() (*snapshot, error) {
	if !o.held.Load() {
		return nil, alpacadev.ErrNotConnected
	}
	return o.b.Snapshot()
}

// reading returns one sensor value. ValueNotSet when firmware marks it invalid.
func (o *ObservingConditions) reading(what string, v func(n *snapshot) (float64, bool)) (float64, error) {
	n, err := o.snapshot()
	if err != nil {
		return 0, err
	}
	x, ok := v(n)
	if !ok {
		return 0, notValid(what)
	}
	return x, nil
}

// Temperature is dew controller air temperature, board sensor plus offset.
// Same value as switch "Air Temperature".
func (o *ObservingConditions) Temperature() (float64, error) {
	return o.reading("Temperature", func(n *snapshot) (float64, bool) { return n.dew.AmbientC, n.dew.Valid })
}

func (o *ObservingConditions) Humidity() (float64, error) {
	return o.reading("Humidity", func(n *snapshot) (float64, bool) { return n.st.Env.RHPct, n.st.Env.THValid })
}

func (o *ObservingConditions) DewPoint() (float64, error) {
	return o.reading("DewPoint", func(n *snapshot) (float64, bool) { return n.dew.DewpointC, n.dew.Valid })
}

// Pressure is station pressure, hPa, per ASCOM.
func (o *ObservingConditions) Pressure() (float64, error) {
	return o.reading("Pressure", func(n *snapshot) (float64, bool) { return n.st.Env.PressureHPa, n.st.Env.BaroValid })
}

// SetAveragePeriod accepts only 0. Readings instantaneous.
func (o *ObservingConditions) SetAveragePeriod(hours float64) error {
	if hours != 0 {
		return alpacadev.ErrInvalidValue
	}
	return nil
}

// sensorDesc describes each supported sensor, keyed by lowercase ASCOM name.
// VEML7700 light sensor drives LED dimming, not sky brightness, so left out.
var sensorDesc = map[string]string{
	"temperature": "SHT45 air temperature plus the board's temperature offset (°C)",
	"humidity":    "Sensirion SHT45 relative humidity (%)",
	"dewpoint":    "Dew point from the dew controller (°C)",
	"pressure":    "Bosch BMP581 station pressure (hPa)",
}

// TimeSinceLastUpdate returns seconds since last board read.
func (o *ObservingConditions) TimeSinceLastUpdate(name string) (float64, error) {
	if _, ok := sensorDesc[strings.ToLower(name)]; !ok && name != "" {
		return 0, alpacadev.ErrNotImplemented
	}
	n, err := o.snapshot()
	if err != nil {
		return 0, err
	}
	return time.Since(n.at).Seconds(), nil
}

func (o *ObservingConditions) SensorDescription(name string) (string, error) {
	if d, ok := sensorDesc[strings.ToLower(name)]; ok {
		return d, nil
	}
	return "", alpacadev.ErrNotImplemented
}
