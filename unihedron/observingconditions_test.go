package driver

import (
	"context"
	"math"
	"testing"
	"time"

	alpacadev "github.com/mikefsq/goalpaca/server"
	"github.com/mikefsq/unihedron"
)

// fakeT is a scripted unihedron.Transport: it answers each written command with canned
// bytes matched by the command string.
type fakeT struct {
	replies map[string][]byte
	out     []byte
}

func (f *fakeT) Write(p []byte) (int, error) {
	if r, ok := f.replies[string(p)]; ok {
		f.out = append(f.out, r...)
	}
	return len(p), nil
}

func (f *fakeT) Read(p []byte) (int, error) {
	if len(f.out) == 0 {
		return 0, nil
	}
	n := copy(p, f.out)
	f.out = f.out[n:]
	return n, nil
}

func (f *fakeT) Close() error { return nil }

// fakeSQM wraps the unihedron library over the scripted transport, with the live-
// captured responses from the real SQM-LU (serial 5533).
func fakeSQM() *unihedron.SQM {
	return unihedron.New(&fakeT{replies: map[string][]byte{
		"ix": []byte("i,00000004,00000003,00000076,00005533\r\n"),
		"cx": []byte("c,00000019.96m,0000199.361s, 019.0C,00000008.71m, 020.3C\r\n"),
		"rx": []byte("r, 11.19m,0000003248Hz,0000000000c,0000000.000s, 025.1C\r\n"),
	}}, unihedron.DeviceInfo{Port: "fake"})
}

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

func TestDisconnectedBehavior(t *testing.T) {
	s := NewSQM(0)
	if s.Connected() {
		t.Error("Connected() = true before acquire")
	}
	if _, err := s.SkyQuality(); err != alpacadev.ErrNotConnected {
		t.Errorf("SkyQuality() err = %v, want ErrNotConnected", err)
	}
	if _, err := s.Temperature(); err != alpacadev.ErrNotConnected {
		t.Errorf("Temperature() err = %v, want ErrNotConnected", err)
	}
	if err := s.Refresh(); err != alpacadev.ErrNotConnected {
		t.Errorf("Refresh() err = %v, want ErrNotConnected", err)
	}
	if err := s.Connect(context.Background()); err != alpacadev.ErrNotConnected {
		t.Errorf("Connect() err = %v, want ErrNotConnected", err)
	}
	// Not-yet-read cache reports the ASCOM negative sentinel.
	if v, err := s.TimeSinceLastUpdate(""); err != nil || v != -1 {
		t.Errorf("TimeSinceLastUpdate() = %v, %v; want -1, nil", v, err)
	}
}

func TestIdentityDefaults(t *testing.T) {
	s := NewSQMBySerial(0, "AG0JWD3W")
	if s.UniqueID() != "Unihedron-SQM-AG0JWD3W" {
		t.Errorf("UniqueID = %q", s.UniqueID())
	}
	if s.InterfaceVersion() != alpacadev.InterfaceVersionObservingConditions {
		t.Errorf("InterfaceVersion = %d", s.InterfaceVersion())
	}
}

// TestAcquireAndRead drives the full path: inject a fake opener, run manageHardware
// long enough to acquire, then read the mapped sensors.
func TestAcquireAndRead(t *testing.T) {
	s := NewSQM(0)
	s.openDev = func() (*unihedron.SQM, error) { return fakeSQM(), nil }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Open(ctx); err != nil {
		t.Fatalf("Open: %v", err)
	}

	// Wait for the supervisor to acquire the device.
	deadline := time.Now().Add(2 * time.Second)
	for !s.Connected() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !s.Connected() {
		t.Fatal("device not acquired")
	}

	q, err := s.SkyQuality()
	if err != nil || !approx(q, 11.19) {
		t.Errorf("SkyQuality() = %v, %v; want 11.19", q, err)
	}
	temp, err := s.Temperature()
	if err != nil || !approx(temp, 25.1) {
		t.Errorf("Temperature() = %v, %v; want 25.1", temp, err)
	}
	if err := s.Refresh(); err != nil {
		t.Errorf("Refresh() err = %v", err)
	}
	if v, err := s.TimeSinceLastUpdate("skyquality"); err != nil || v < 0 {
		t.Errorf("TimeSinceLastUpdate(skyquality) = %v, %v", v, err)
	}
	// The Desc string picked up the unit's identity during acquire.
	if s.Description() == "" {
		t.Error("Description() empty after acquire")
	}
}

func TestUnsupportedSensors(t *testing.T) {
	s := NewSQM(0)
	// Humidity et al. stay at the BaseObservingConditions NotImplemented default.
	if _, err := s.Humidity(); err != alpacadev.ErrNotImplemented {
		t.Errorf("Humidity() err = %v, want ErrNotImplemented", err)
	}
	if _, err := s.SkyTemperature(); err != alpacadev.ErrNotImplemented {
		t.Errorf("SkyTemperature() err = %v, want ErrNotImplemented", err)
	}
	// TimeSinceLastUpdate / SensorDescription reject sensors this meter lacks.
	if _, err := s.TimeSinceLastUpdate("humidity"); err != alpacadev.ErrNotImplemented {
		t.Errorf("TimeSinceLastUpdate(humidity) err = %v, want ErrNotImplemented", err)
	}
	if _, err := s.SensorDescription("windspeed"); err != alpacadev.ErrNotImplemented {
		t.Errorf("SensorDescription(windspeed) err = %v, want ErrNotImplemented", err)
	}
	d, err := s.SensorDescription("skyquality")
	if err != nil || d == "" {
		t.Errorf("SensorDescription(skyquality) = %q, %v", d, err)
	}
}
