package driver

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	alpacadev "github.com/mikefsq/goalpaca/server"
	pimountgps "github.com/mikefsq/x1500_gps_12v/driver"
)

type fakeBoard struct {
	mu        sync.Mutex
	w         pimountgps.Weather
	age       time.Duration
	read      bool
	stale     bool
	noSHT     bool
	refreshes int
	closed    bool

	fix      pimountgps.Fix
	fixAge   time.Duration
	hasFix   bool // a fix has been published
	fixStale bool
}

func (f *fakeBoard) HasHumidity() bool { return !f.noSHT }
func (f *fakeBoard) HasPressure() bool { return true }
func (f *fakeBoard) Fix() (pimountgps.Fix, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.fix, nil
}
func (f *fakeBoard) FixAge() (time.Duration, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.fixAge, f.hasFix
}
func (f *fakeBoard) FixStale() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.fixStale || !f.hasFix
}

func (f *fakeBoard) Weather() pimountgps.Weather {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.w
}
func (f *fakeBoard) WeatherAge() (time.Duration, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.age, f.read
}
func (f *fakeBoard) WeatherStale() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stale || !f.read
}
func (f *fakeBoard) WeatherErr() error { return nil }
func (f *fakeBoard) Refresh() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refreshes++
	return nil
}
func (f *fakeBoard) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

func goodBoard() *fakeBoard {
	return &fakeBoard{
		w:    pimountgps.Weather{Temperature: 12.5, Humidity: 80, Dewpoint: 9.1, Pressure: 1008.4},
		age:  1500 * time.Millisecond,
		read: true,
	}
}

func newConnected(t *testing.T, b *fakeBoard) *Driver {
	t.Helper()
	w := NewDriver(pimountgps.DefaultConfig(), "")
	w.openBoard = func() (board, error) { return b, nil }
	w.openGPS = func() (board, error) { return b, nil }
	w.gpsBoard = b
	w.clockSynced = func() bool { return false }
	if err := w.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close(context.Background()) })
	deadline := time.Now().Add(time.Second)
	for !w.Connected() {
		if time.Now().After(deadline) {
			t.Fatal("never acquired the board")
		}
		time.Sleep(time.Millisecond)
	}
	return w
}

func TestReadings(t *testing.T) {
	w := newConnected(t, goodBoard())
	for name, get := range map[string]func() (float64, error){
		"temperature": w.Temperature, "humidity": w.Humidity,
		"dewpoint": w.DewPoint, "pressure": w.Pressure,
	} {
		v, err := get()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		want := map[string]float64{"temperature": 12.5, "humidity": 80, "dewpoint": 9.1, "pressure": 1008.4}[name]
		if v != want {
			t.Fatalf("%s = %v, want %v", name, v, want)
		}
	}
	if _, err := w.SkyQuality(); !errors.Is(err, alpacadev.ErrNotImplemented) {
		t.Fatalf("SkyQuality err = %v", err)
	}
}

func TestStaleIsValueNotSet(t *testing.T) {
	b := goodBoard()
	b.stale = true
	w := newConnected(t, b)
	if _, err := w.Temperature(); !errors.Is(err, alpacadev.ErrValueNotSet) {
		t.Fatalf("err = %v, want ErrValueNotSet", err)
	}
}

func TestNotConnected(t *testing.T) {
	w := NewDriver(pimountgps.DefaultConfig(), "")
	if _, err := w.Temperature(); !errors.Is(err, alpacadev.ErrNotConnected) {
		t.Fatalf("Temperature err = %v", err)
	}
	if err := w.Refresh(); !errors.Is(err, alpacadev.ErrNotConnected) {
		t.Fatalf("Refresh err = %v", err)
	}
}

func TestTimeSinceLastUpdate(t *testing.T) {
	b := goodBoard()
	w := newConnected(t, b)
	if s, err := w.TimeSinceLastUpdate("Pressure"); err != nil || s != 1.5 {
		t.Fatalf("got %v, %v", s, err)
	}
	if _, err := w.TimeSinceLastUpdate("SkyQuality"); !errors.Is(err, alpacadev.ErrNotImplemented) {
		t.Fatalf("unsupported sensor err = %v", err)
	}
	b.mu.Lock()
	b.read = false
	b.mu.Unlock()
	if s, err := w.TimeSinceLastUpdate(""); err != nil || s != -1 {
		t.Fatalf("before first read: %v, %v", s, err)
	}
}

func TestRefreshAndAveragePeriod(t *testing.T) {
	b := goodBoard()
	w := newConnected(t, b)
	if err := w.Refresh(); err != nil || b.refreshes != 1 {
		t.Fatalf("Refresh = %v, refreshes %d", err, b.refreshes)
	}
	if err := w.SetAveragePeriod(0); err != nil {
		t.Fatal(err)
	}
	if err := w.SetAveragePeriod(1); !errors.Is(err, alpacadev.ErrInvalidValue) {
		t.Fatalf("SetAveragePeriod(1) err = %v", err)
	}
}

func TestSensorDescription(t *testing.T) {
	w := NewDriver(pimountgps.DefaultConfig(), "")
	for _, s := range []string{"Temperature", "humidity", "DewPoint", "pressure"} {
		if d, err := w.SensorDescription(s); err != nil || d == "" {
			t.Fatalf("%s: %q, %v", s, d, err)
		}
	}
	if _, err := w.SensorDescription("windspeed"); !errors.Is(err, alpacadev.ErrNotImplemented) {
		t.Fatal(err)
	}
}

func TestCloseReleasesBoard(t *testing.T) {
	b := goodBoard()
	w := newConnected(t, b)
	w.Close(context.Background())
	if !b.closed || w.Connected() {
		t.Fatalf("closed=%v connected=%v", b.closed, w.Connected())
	}
}
