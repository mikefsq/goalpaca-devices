// Package driver is the ASCOM Alpaca ObservingConditions device for Unihedron Sky
// Quality Meters (SQM-LU / SQM-LU-DL / SQM-LE), over the Go mikefsq/unihedron library
// (FTDI USB-serial). It is served standalone by cmd/unihedron and can be hosted by
// astrofleet.
//
// Sensor mapping is deliberately narrow and honest. An SQM measures exactly two things:
//   - sky brightness in mag/arcsec²  → SkyQuality()  (the ASCOM field defined in those
//     very units — an exact, not converted, mapping)
//   - temperature at the light sensor → Temperature() (ambient-ish; the SQM has no IR
//     sky sensor, so SkyTemperature is intentionally left NotImplemented, as is
//     SkyBrightness, whose ASCOM unit is Lux — a band/model-dependent conversion we
//     decline to fabricate)
//
// Everything else stays at the BaseObservingConditions NotImplemented default.
package driver

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	alpacadev "github.com/mikefsq/goalpaca/server"
	"github.com/mikefsq/unihedron"
)

var _ alpacadev.ObservingConditions = (*SQM)(nil)

// cacheTTL bounds how long one device reading is reused. A client's Refresh() +
// several property GETs (SkyQuality, Temperature, TimeSinceLastUpdate) then share a
// single serial round-trip instead of hammering the meter once per property.
const cacheTTL = 2 * time.Second

// SQM adapts a mikefsq/unihedron Sky Quality Meter to the alpacadev.ObservingConditions
// + Hardware interfaces.
type SQM struct {
	alpacadev.BaseObservingConditions

	index  int
	serial string // USB-bridge serial to bind to; "" = bind by enumeration index

	mu     sync.Mutex
	dev    *unihedron.SQM // open handle; nil when no meter is attached
	last   unihedron.Reading
	lastAt time.Time // zero = never read

	openDev func() (*unihedron.SQM, error) // injectable for tests
}

// NewSQM creates the driver for the SQM at the given enumeration index. Binding follows
// plug order; prefer NewSQMBySerial for a stable identity.
func NewSQM(index int) *SQM {
	s := &SQM{index: index}
	s.init()
	s.ID = fmt.Sprintf("Unihedron-SQM-oc%d", index)
	s.DevName = fmt.Sprintf("Unihedron SQM %d", index)
	s.openDev = s.openByIndex
	return s
}

// NewSQMBySerial binds the Alpaca device (devNum used only for the stable ID) to the
// SQM whose FTDI USB-bridge serial number is serial. The serial is read from the USB
// descriptor before the port opens, so the binding is plug-order- and platform-
// independent and disambiguates several FTDI devices sharing VID 0x0403.
func NewSQMBySerial(devNum int, serial string) *SQM {
	s := &SQM{index: devNum, serial: serial}
	s.init()
	s.ID = "Unihedron-SQM-" + serial
	s.DevName = "Unihedron SQM " + serial
	s.openDev = s.openBySerial
	return s
}

func (s *SQM) init() {
	s.Version = "0.1.0"
	s.Info = "unihedron — Unihedron SQM Alpaca ObservingConditions driver over Go mikefsq/unihedron"
	s.IfaceVer = alpacadev.InterfaceVersionObservingConditions
}

// --- Hardware lifecycle (mirrors the sibling focuscube device) ---

func (s *SQM) Open(ctx context.Context) error {
	if s.openDev == nil {
		s.openDev = s.openByIndex
	}
	go alpacadev.Supervise(ctx, s.ID, func() { s.manageHardware(ctx) })
	return nil
}

func (s *SQM) Close(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dev != nil {
		s.dev.Close()
		s.dev = nil
	}
	return nil
}

func (s *SQM) Connect(ctx context.Context) error {
	if !s.Connected() {
		return alpacadev.ErrNotConnected
	}
	return nil
}

func (s *SQM) Connected() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dev != nil
}

func (s *SQM) Disconnect(ctx context.Context) error { return nil }

// manageHardware owns the meter for the process lifetime: it acquires the device, then
// health-checks it with a lightweight reading and re-acquires on loss.
func (s *SQM) manageHardware(ctx context.Context) {
	for ctx.Err() == nil {
		s.mu.Lock()
		present := s.dev != nil
		s.mu.Unlock()
		if !present {
			if s.tryAcquire() {
				log.Printf("unihedron: SQM %s acquired", s.ID)
			} else {
				sleepCtx(ctx, 3*time.Second)
			}
			continue
		}
		s.mu.Lock()
		var err error
		if s.dev != nil {
			_, err = s.dev.Reading()
		}
		if err != nil {
			log.Printf("unihedron: SQM %s lost (%v); re-acquiring", s.ID, err)
			s.dev.Close()
			s.dev = nil
		}
		s.mu.Unlock()
		sleepCtx(ctx, 5*time.Second)
	}
}

func (s *SQM) tryAcquire() bool {
	dev, err := s.openDev()
	if err != nil {
		return false
	}
	info, err := dev.UnitInfo()
	if err != nil {
		dev.Close()
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dev = dev
	s.Desc = fmt.Sprintf("Unihedron SQM (FTDI serial, unit serial %d, protocol %d, feature %d)",
		info.Serial, info.Protocol, info.Feature)
	return true
}

// openByIndex probes the FTDI ports for actual SQMs (skipping other FTDI devices such as
// a Pegasus focuser) and opens the index-th one. With the default index 0 this reliably
// finds the meter without a configured serial; index only matters in the rare multi-SQM
// setup.
func (s *SQM) openByIndex() (*unihedron.SQM, error) {
	found, err := unihedron.Discover()
	if err != nil {
		return nil, err
	}
	if s.index < 0 || s.index >= len(found) {
		return nil, fmt.Errorf("no SQM at index %d (found %d)", s.index, len(found))
	}
	return unihedron.OpenPort(found[s.index].Port)
}

func (s *SQM) openBySerial() (*unihedron.SQM, error) { return unihedron.OpenBySerial(s.serial) }

// --- ObservingConditions members ---

// reading returns a cached meter reading, refreshing from the device when the cache is
// older than cacheTTL (or force is set). Caller must not hold s.mu.
func (s *SQM) reading(force bool) (unihedron.Reading, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dev == nil {
		return unihedron.Reading{}, alpacadev.ErrNotConnected
	}
	if !force && !s.lastAt.IsZero() && time.Since(s.lastAt) < cacheTTL {
		return s.last, nil
	}
	r, err := s.dev.Reading()
	if err != nil {
		return unihedron.Reading{}, err
	}
	s.last = r
	s.lastAt = time.Now()
	return r, nil
}

// SkyQuality returns the sky brightness in mag/arcsec² — the SQM's native measurement,
// and the exact quantity the ASCOM SkyQuality property is defined in.
func (s *SQM) SkyQuality() (float64, error) {
	r, err := s.reading(false)
	return r.MagPerArcsec2, err
}

// Temperature returns the temperature measured at the light sensor, °C.
func (s *SQM) Temperature() (float64, error) {
	r, err := s.reading(false)
	return r.TempC, err
}

// Refresh forces an immediate device reading, updating the cache for the property GETs
// that follow.
func (s *SQM) Refresh() error {
	_, err := s.reading(true)
	return err
}

// supported reports whether name is a sensor this SQM implements ("" = any).
func supported(name string) bool {
	switch strings.ToLower(name) {
	case "", "skyquality", "temperature":
		return true
	}
	return false
}

// TimeSinceLastUpdate returns seconds since the last successful reading, for the sensors
// this meter implements (or "" = any). The gate has already validated name against the
// canonical ASCOM sensor set; here we additionally reject sensors this device lacks.
func (s *SQM) TimeSinceLastUpdate(name string) (float64, error) {
	if !supported(name) {
		return 0, alpacadev.ErrNotImplemented
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lastAt.IsZero() {
		return -1, nil // ASCOM convention: negative when no value has been read yet
	}
	return time.Since(s.lastAt).Seconds(), nil
}

// SensorDescription describes the backing hardware for the sensors this meter provides.
func (s *SQM) SensorDescription(name string) (string, error) {
	switch strings.ToLower(name) {
	case "skyquality":
		return "Unihedron SQM TSL237 light-to-frequency sensor (mag/arcsec²)", nil
	case "temperature":
		return "Unihedron SQM light-sensor temperature (°C)", nil
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
