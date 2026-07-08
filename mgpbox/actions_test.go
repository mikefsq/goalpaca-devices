package driver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/mikefsq/astromi.ch/mgpbox"
	alpacadev "github.com/mikefsq/goalpaca/server"
)

// fakeBoxGPS streams a meteo line (so acquisition completes) plus a GPS RMC fix.
func fakeBoxGPS() *mgpbox.MGPBox {
	const s = "$PXDR,P,101531.0,P,0,C,23.5,C,1,H,54.0,P,2,C,13.6,C,3,1.1*02\n" +
		"$GPRMC,220516,A,5133.82,N,00042.24,W,173.8,231.8,130694,004.2,W*70\n"
	return mgpbox.New(newFakeT(s), mgpbox.DeviceInfo{Port: "fake"})
}

func acquire(t *testing.T, box func() (*mgpbox.MGPBox, error)) (*MGPBox, context.CancelFunc) {
	t.Helper()
	m := NewMGPBox(0)
	m.openDev = box
	ctx, cancel := context.WithCancel(context.Background())
	if err := m.Open(ctx); err != nil {
		cancel()
		t.Fatalf("Open: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for !m.Connected() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !m.Connected() {
		cancel()
		t.Fatal("device not acquired")
	}
	return m, cancel
}

func TestSupportedActionsHasGPS(t *testing.T) {
	m := NewMGPBox(0)
	found := false
	for _, a := range m.SupportedActions() {
		if strings.EqualFold(a, "gps") { // advertised CamelCase ("Gps"), matched case-insensitively
			found = true
		}
	}
	if !found {
		t.Errorf("SupportedActions = %v, missing a GPS action", m.SupportedActions())
	}
}

func TestActionDisconnected(t *testing.T) {
	m := NewMGPBox(0)
	if _, err := m.Action("gps", ""); err != alpacadev.ErrNotConnected {
		t.Errorf("Action(gps) err = %v, want ErrNotConnected", err)
	}
}

func TestActionGPS(t *testing.T) {
	m, cancel := acquire(t, func() (*mgpbox.MGPBox, error) { return fakeBoxGPS(), nil })
	defer cancel()

	// Wait for the RMC line to be folded in.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if s, _ := m.Action("gps", ""); strings.Contains(s, `"valid":true`) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	out, err := m.Action("gps", "")
	if err != nil {
		t.Fatalf("Action(gps): %v", err)
	}
	var g gpsJSON
	if err := json.Unmarshal([]byte(out), &g); err != nil {
		t.Fatalf("gps JSON: %v (%q)", err, out)
	}
	if !g.Valid || g.Latitude < 51.5 || g.Latitude > 51.6 || g.Longitude > -0.6 {
		t.Errorf("gps = %+v", g)
	}
	if lat, _ := m.Action("latitude", ""); !strings.HasPrefix(lat, "51.56") {
		t.Errorf("latitude = %q", lat)
	}
}

func TestScalarActions(t *testing.T) {
	m, cancel := acquire(t, func() (*mgpbox.MGPBox, error) { return fakeBoxGPS(), nil })
	defer cancel()
	// Wait for both meteo + fix to be folded in.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if v, _ := m.Action("temperature", ""); v == "23.5" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Every measurement should have a scalar action and a non-empty value.
	want := map[string]string{
		"temperature": "23.5", "humidity": "54", "pressure": "1015.31", "dewpoint": "13.6",
		"pcal": "0", "tcal": "0", "hcal": "0",
	}
	for act, exp := range want {
		if got, err := m.Action(act, ""); err != nil || got != exp {
			t.Errorf("Action(%q) = %q, %v; want %q", act, got, err, exp)
		}
	}
	// GPS scalars present (fix is valid in fakeBoxGPS).
	for _, act := range []string{"latitude", "longitude", "satellites", "gpstime"} {
		if got, err := m.Action(act, ""); err != nil || got == "" {
			t.Errorf("Action(%q) = %q, %v; want non-empty", act, got, err)
		}
	}
	// Case-insensitive dispatch (not an Alpaca requirement, but supported here).
	if got, err := m.Action("TEMPERATURE", ""); err != nil || got != "23.5" {
		t.Errorf("Action(TEMPERATURE) = %q, %v; want 23.5", got, err)
	}
	// Every advertised action name resolves (no typo'd entry in SupportedActions).
	for _, a := range m.SupportedActions() {
		if _, err := m.Action(a, ""); err == alpacadev.ErrActionNotImplemented {
			t.Errorf("advertised action %q dispatches to ErrActionNotImplemented", a)
		}
	}
}

func TestActionGpsEnable(t *testing.T) {
	m, cancel := acquire(t, func() (*mgpbox.MGPBox, error) { return fakeBoxGPS(), nil })
	defer cancel()
	// Empty reads the last-commanded state; boot default is on.
	if v, err := m.Action("GpsEnable", ""); err != nil || v != "true" {
		t.Errorf("GpsEnable read (default) = %q, %v; want true", v, err)
	}
	// false powers off and reads back off (case-insensitive dispatch).
	if v, err := m.Action("gpsenable", "false"); err != nil || v != "false" {
		t.Errorf("GpsEnable set false = %q, %v", v, err)
	}
	if v, _ := m.Action("GPSENABLE", ""); v != "false" {
		t.Errorf("GpsEnable read after set = %q, want false", v)
	}
}

func TestActionUnknown(t *testing.T) {
	m, cancel := acquire(t, func() (*mgpbox.MGPBox, error) { return fakeBoxGPS(), nil })
	defer cancel()
	if _, err := m.Action("nonsense", ""); err != alpacadev.ErrActionNotImplemented {
		t.Errorf("Action(nonsense) err = %v, want ErrActionNotImplemented", err)
	}
}
