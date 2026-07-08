package driver

import (
	"context"
	"io"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/mikefsq/astromi.ch/mgpbox"
	alpacadev "github.com/mikefsq/goalpaca/server"
)

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

// fakeT streams canned MGPBox bytes then blocks reads until Close, like a real port.
type fakeT struct {
	mu     sync.Mutex
	data   []byte
	closed chan struct{}
	once   sync.Once
}

func newFakeT(s string) *fakeT { return &fakeT{data: []byte(s), closed: make(chan struct{})} }

func (f *fakeT) Read(p []byte) (int, error) {
	f.mu.Lock()
	if len(f.data) > 0 {
		n := copy(p, f.data)
		f.data = f.data[n:]
		f.mu.Unlock()
		return n, nil
	}
	f.mu.Unlock()
	<-f.closed
	return 0, io.EOF
}

func (f *fakeT) Write(p []byte) (int, error) { return len(p), nil }
func (f *fakeT) Close() error                { f.once.Do(func() { close(f.closed) }); return nil }

// fakeBox builds a streaming MGPBox over a fake transport with a live-captured PXDR line.
func fakeBox() *mgpbox.MGPBox {
	const line = "MGPBox by Astromi.ch\n$PCAL,P,0,T,0,H,0,MM,1,MG,1*68\n" +
		"$PXDR,P,101531.0,P,0,C,23.5,C,1,H,54.0,P,2,C,13.6,C,3,1.1*02\n"
	return mgpbox.New(newFakeT(line), mgpbox.DeviceInfo{Port: "fake"})
}

func TestDisconnectedBehavior(t *testing.T) {
	m := NewMGPBox(0)
	if m.Connected() {
		t.Error("Connected() = true before acquire")
	}
	for _, tc := range []struct {
		name string
		fn   func() (float64, error)
	}{
		{"Temperature", m.Temperature}, {"Humidity", m.Humidity},
		{"Pressure", m.Pressure}, {"DewPoint", m.DewPoint},
	} {
		if _, err := tc.fn(); err != alpacadev.ErrNotConnected {
			t.Errorf("%s() err = %v, want ErrNotConnected", tc.name, err)
		}
	}
	if err := m.Connect(context.Background()); err != alpacadev.ErrNotConnected {
		t.Errorf("Connect() err = %v, want ErrNotConnected", err)
	}
	if v, err := m.TimeSinceLastUpdate(""); err != nil || v != -1 {
		t.Errorf("TimeSinceLastUpdate() = %v, %v; want -1, nil", v, err)
	}
}

func TestIdentity(t *testing.T) {
	m := NewMGPBoxBySerial(0, "D30B0DP6")
	if m.UniqueID() != "Astromi-MGPBox-D30B0DP6" {
		t.Errorf("UniqueID = %q", m.UniqueID())
	}
	if m.InterfaceVersion() != alpacadev.InterfaceVersionObservingConditions {
		t.Errorf("InterfaceVersion = %d", m.InterfaceVersion())
	}
}

func TestAcquireAndRead(t *testing.T) {
	m := NewMGPBox(0)
	m.openDev = func() (*mgpbox.MGPBox, error) { return fakeBox(), nil }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := m.Open(ctx); err != nil {
		t.Fatalf("Open: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for !m.Connected() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !m.Connected() {
		t.Fatal("device not acquired")
	}

	if v, err := m.Temperature(); err != nil || !approx(v, 23.5) {
		t.Errorf("Temperature() = %v, %v; want 23.5", v, err)
	}
	if v, err := m.Humidity(); err != nil || !approx(v, 54.0) {
		t.Errorf("Humidity() = %v, %v; want 54.0", v, err)
	}
	if v, err := m.Pressure(); err != nil || !approx(v, 1015.31) {
		t.Errorf("Pressure() = %v, %v; want 1015.31", v, err)
	}
	if v, err := m.DewPoint(); err != nil || !approx(v, 13.6) {
		t.Errorf("DewPoint() = %v, %v; want 13.6", v, err)
	}
	if err := m.Refresh(); err != nil {
		t.Errorf("Refresh() err = %v", err)
	}
	if v, err := m.TimeSinceLastUpdate("temperature"); err != nil || v < 0 {
		t.Errorf("TimeSinceLastUpdate(temperature) = %v, %v", v, err)
	}
}

func TestUnsupportedSensors(t *testing.T) {
	m := NewMGPBox(0)
	if _, err := m.SkyTemperature(); err != alpacadev.ErrNotImplemented {
		t.Errorf("SkyTemperature() err = %v, want ErrNotImplemented", err)
	}
	if _, err := m.WindSpeed(); err != alpacadev.ErrNotImplemented {
		t.Errorf("WindSpeed() err = %v, want ErrNotImplemented", err)
	}
	if _, err := m.TimeSinceLastUpdate("windspeed"); err != alpacadev.ErrNotImplemented {
		t.Errorf("TimeSinceLastUpdate(windspeed) err = %v, want ErrNotImplemented", err)
	}
	if d, err := m.SensorDescription("pressure"); err != nil || d == "" {
		t.Errorf("SensorDescription(pressure) = %q, %v", d, err)
	}
}
