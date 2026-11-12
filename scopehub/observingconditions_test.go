package driver

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	alpacadev "github.com/mikefsq/goalpaca/server"
	scopehub "github.com/mikefsq/scope_hub/driver"
)

func newTestOC(t *testing.T) *ObservingConditions {
	t.Helper()
	o := NewObservingConditions(Config{})
	if err := o.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { o.Close(context.Background()) })
	return o
}

func TestSharedBoardRefcount(t *testing.T) {
	ctx := context.Background()
	f := newFake()
	s := newTestSwitch(t, f)
	o := newTestOC(t)
	if o.b != s.b {
		t.Fatal("switch and observingconditions hold different boards")
	}
	if !o.Connected() {
		t.Fatal("observingconditions not connected on the shared board")
	}
	// Reload. Old switch closes, replacement opens, board stays open.
	s.Close(ctx)
	if f.closed {
		t.Fatal("board closed while observingconditions still holds it")
	}
	s2 := NewSwitch(Config{})
	s2.b.open = func(string) (device, error) { t.Fatal("reopened a held board"); return nil, nil }
	if err := s2.Open(ctx); err != nil {
		t.Fatal(err)
	}
	if s2.b != o.b {
		t.Fatal("replacement switch did not share the board")
	}
	s2.Close(ctx)
	o.Close(ctx)
	if !f.closed {
		t.Fatal("last close left the board open")
	}
	if o.Connected() {
		t.Fatal("connected after close")
	}
	boardsMu.Lock()
	n := len(boards)
	boardsMu.Unlock()
	if n != 0 {
		t.Fatalf("%d boards still registered", n)
	}
}

func TestObservingConditionsReadings(t *testing.T) {
	f := newFake()
	f.st.Env = scopehub.Env{THValid: true, TempC: 9, RHPct: 55, BaroValid: true, PressureHPa: 1012.5}
	newTestSwitch(t, f)
	o := newTestOC(t)
	for _, c := range []struct {
		name string
		get  func() (float64, error)
		want float64
	}{
		{"Temperature", o.Temperature, 10}, // dew controller air temperature, not raw SHT45
		{"Humidity", o.Humidity, 55},
		{"DewPoint", o.DewPoint, 6},
		{"Pressure", o.Pressure, 1012.5},
	} {
		if v, err := c.get(); err != nil || v != c.want {
			t.Errorf("%s = %v, %v; want %v", c.name, v, err, c.want)
		}
	}
	for name, get := range map[string]func() (float64, error){"WindSpeed": o.WindSpeed, "SkyBrightness": o.SkyBrightness} {
		if _, err := get(); !errors.Is(err, alpacadev.ErrNotImplemented) {
			t.Errorf("%s err = %v, want not implemented", name, err)
		}
	}
	if v, err := o.Action("Pressure", ""); err != nil || v != "1012.5" {
		t.Errorf("Pressure action = %q, %v", v, err)
	}

	f.st.Env.THValid = false
	f.dew.Valid = false
	o.b.snap = nil
	var ae *alpacadev.AlpacaError
	if _, err := o.Humidity(); !errors.As(err, &ae) || ae.Number != alpacadev.ErrNumValueNotSet {
		t.Errorf("Humidity with invalid sensor: err = %v, want ValueNotSet", err)
	}
	if _, err := o.Temperature(); !errors.As(err, &ae) || ae.Number != alpacadev.ErrNumValueNotSet {
		t.Errorf("Temperature with invalid dew reading: err = %v, want ValueNotSet", err)
	}
}

func TestObservingConditionsGpsAction(t *testing.T) {
	f := newFake()
	f.st.GPS = scopehub.GPS{Seen: true, RMCValid: true, Fix: 1, Satellites: 9,
		LatDeg: 40.5, LonDeg: -111.9, AltM: 1400.4, UTC: "032000.00", Date: "310826"}
	newTestSwitch(t, f)
	o := newTestOC(t)
	raw, err := o.Action("Gps", "")
	if err != nil {
		t.Fatal(err)
	}
	var g gpsJSON
	if err := json.Unmarshal([]byte(raw), &g); err != nil {
		t.Fatal(err)
	}
	want := gpsJSON{Valid: true, Time: "2026-08-31T03:20:00Z", Latitude: 40.5, Longitude: -111.9,
		Altitude: 1400.4, Satellites: 9, FixQuality: "1"}
	if g != want {
		t.Errorf("Gps = %+v, want %+v", g, want)
	}
	for name, want := range map[string]string{
		"Latitude": "40.500000", "Longitude": "-111.900000", "Altitude": "1400.4",
		"Satellites": "9", "FixQuality": "1", "GpsTime": "2026-08-31T03:20:00Z",
	} {
		if v, err := o.Action(name, ""); err != nil || v != want {
			t.Errorf("%s = %q, %v; want %q", name, v, err, want)
		}
	}

	f.st.GPS.Fix = 0
	o.b.snap = nil
	if raw, _ := o.Action("Gps", ""); json.Unmarshal([]byte(raw), &g) != nil || g.Valid {
		t.Errorf("Gps without a fix = %s, want valid:false", raw)
	}
}

// Connected means hardware present: an unplugged board (status read fails) reads not connected
// on both devices, even with the handle still cached, and Connect fails.
func TestConnectedNeedsLiveHardware(t *testing.T) {
	f := newFake()
	s := newTestSwitch(t, f)
	o := newTestOC(t)
	if !s.Connected() || !o.Connected() {
		t.Fatal("not connected with board present")
	}
	f.broken = true
	s.b.snap = nil
	s.b.open = func(string) (device, error) { return nil, errors.New("no board") }
	if s.Connected() || o.Connected() {
		t.Fatal("connected with board gone")
	}
	if err := s.Connect(context.Background()); err != alpacadev.ErrNotConnected {
		t.Fatal("switch Connect succeeded with board gone")
	}
	if err := o.Connect(context.Background()); err != alpacadev.ErrNotConnected {
		t.Fatal("observingconditions Connect succeeded with board gone")
	}
}
