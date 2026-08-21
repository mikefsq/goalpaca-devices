package driver

import (
	"context"
	"errors"
	"math"
	"net/http/httptest"
	"testing"

	"github.com/mikefsq/goasi/asiair"
	"github.com/mikefsq/goalpaca/client"
	"github.com/mikefsq/goalpaca/conformance"
	alpacadev "github.com/mikefsq/goalpaca/server"
)

// The ISwitchV3 invariants, checked over a real HTTP server and client — fakes
// for the board, everything else genuine.
//
// A note on why this is not simply conformance.CheckSwitch. That harness is
// written against goalpaca's sim switch and assumes every slot is writable, that
// SetSwitchName round-trips, and that slot 0 is an analog 0..100. None of those
// hold for a device with read-only sensors, and ISwitchV3 does not require them:
// CanWrite exists precisely so a switch can be a sensor, and a driver whose names
// describe fixed hardware functions has nothing to rename. (smpro would fail the
// same three checks for the same reasons.) So the checks below are the subset that
// genuinely applies, plus CheckCommon, which does apply in full.
//
// Both PWM pairings are exercised, because the slot metadata is built per device
// from the pairing rather than being a fixed table — so a pairing change could
// quietly produce an illegal switch array, and this is what would catch it.
func TestSwitchConformance(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  asiair.Config
	}{
		{"default pairing (ports 1+2 dimmable)", asiair.DefaultConfig()},
		{"ports 2+4 pairing", asiair.DefaultConfig().WithPWMPorts24()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := serve(t, tc.cfg)

			// The common device members: Connect/Disconnect/Connected, Description,
			// DriverInfo, SupportedActions, and the NotConnected gating. This one
			// applies in full.
			conformance.CheckCommon(t, c)

			if err := c.SetConnected(true); err != nil {
				t.Fatal(err)
			}
			n, err := c.MaxSwitch()
			if err != nil {
				t.Fatal(err)
			}
			if n != 17 {
				t.Fatalf("MaxSwitch = %d, want 17", n)
			}

			for id := 0; id < n; id++ {
				checkSlotMetadata(t, c, id)
			}
			checkInvalidID(t, c, n)
			checkRangeValidation(t, c)
		})
	}
}

// checkSlotMetadata is switchdevCheckSwitchProperties minus the CanWrite
// assumption, plus the step-consistency rule.
func checkSlotMetadata(t *testing.T, c *client.Switch, id int) {
	t.Helper()

	name, err := c.GetSwitchName(id)
	if err != nil || name == "" {
		t.Errorf("GetSwitchName(%d) = %q, %v; want non-empty", id, name, err)
	}
	if desc, err := c.GetSwitchDescription(id); err != nil || desc == "" {
		t.Errorf("GetSwitchDescription(%d) = %q, %v; want non-empty", id, desc, err)
	}

	min, err := c.MinSwitchValue(id)
	if err != nil {
		t.Fatalf("MinSwitchValue(%d): %v", id, err)
	}
	max, err := c.MaxSwitchValue(id)
	if err != nil {
		t.Fatalf("MaxSwitchValue(%d): %v", id, err)
	}
	step, err := c.SwitchStep(id)
	if err != nil {
		t.Fatalf("SwitchStep(%d): %v", id, err)
	}

	if max <= min {
		t.Errorf("%s: max %g <= min %g", name, max, min)
	}
	if step <= 0 {
		t.Errorf("%s: step = %g, want > 0", name, step)
	}
	if step > max-min {
		t.Errorf("%s: step %g exceeds the range %g..%g", name, step, min, max)
	}
	// ISwitchV3: the range must be a whole number of steps.
	if steps := (max - min) / step; math.Abs(steps-math.Round(steps)) > 1e-6 {
		t.Errorf("%s: range %g..%g is not an integer multiple of step %g (%g steps)",
			name, min, max, step, steps)
	}

	// Every slot must read, and the value must lie inside its own advertised range.
	// This is where a signed ADC reading below zero would show up as a violation.
	v, err := c.GetSwitchValue(id)
	if err != nil {
		t.Errorf("%s: GetSwitchValue: %v", name, err)
		return
	}
	if v < min || v > max {
		t.Errorf("%s: value %g outside its advertised %g..%g", name, v, min, max)
	}

	// A read-only sensor must refuse a write; a writable slot must accept one.
	w, err := c.CanWrite(id)
	if err != nil {
		t.Fatalf("%s: CanWrite: %v", name, err)
	}
	if !w {
		if err := c.SetSwitchValue(id, min); err == nil {
			t.Errorf("%s: CanWrite is false but SetSwitchValue succeeded", name)
		}
	}
}

// checkInvalidID: every id-indexed member must reject an out-of-range id.
func checkInvalidID(t *testing.T, c *client.Switch, n int) {
	t.Helper()
	for _, id := range []int{-1, n, n + 100} {
		if _, err := c.GetSwitch(id); !errors.Is(err, alpacadev.ErrInvalidValue) {
			t.Errorf("GetSwitch(%d) = %v, want InvalidValue", id, err)
		}
		if _, err := c.GetSwitchValue(id); !errors.Is(err, alpacadev.ErrInvalidValue) {
			t.Errorf("GetSwitchValue(%d) = %v, want InvalidValue", id, err)
		}
		if err := c.SetSwitch(id, true); !errors.Is(err, alpacadev.ErrInvalidValue) {
			t.Errorf("SetSwitch(%d) = %v, want InvalidValue", id, err)
		}
		if _, err := c.CanWrite(id); !errors.Is(err, alpacadev.ErrInvalidValue) {
			t.Errorf("CanWrite(%d) = %v, want InvalidValue", id, err)
		}
	}
}

// checkRangeValidation: a value outside Min..Max must be rejected, on whichever
// port slot is writable in this pairing.
func checkRangeValidation(t *testing.T, c *client.Switch) {
	t.Helper()
	if err := c.SetSwitchValue(0, 9999); !errors.Is(err, alpacadev.ErrInvalidValue) {
		t.Errorf("SetSwitchValue(0, 9999) = %v, want InvalidValue", err)
	}
	if err := c.SetSwitchValue(0, -1); !errors.Is(err, alpacadev.ErrInvalidValue) {
		t.Errorf("SetSwitchValue(0, -1) = %v, want InvalidValue", err)
	}
}

// serve hosts the ASIAIR switch (over board fakes) on a real HTTP server and
// returns a client pointed at it.
func serve(t *testing.T, cfg asiair.Config) *client.Switch {
	t.Helper()
	hub, _, _, _ := newTestHub(t, cfg, cfg.PWMChannel)
	dev := NewSwitch(hub)

	srv := alpacadev.New(alpacadev.Config{
		Discovery: alpacadev.DiscoveryConfig{Mode: alpacadev.DiscoveryOff},
	})
	if err := srv.Register(alpacadev.SwitchType, 0, dev); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := dev.Open(context.Background()); err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { dev.Close(context.Background()) })

	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return client.NewSwitch(ts.URL, 0)
}

// Disconnect must end the ASCOM session (Connected false, members fault) WITHOUT
// dropping the board — the ports keep their power. Both halves, in one test,
// because getting one right by breaking the other is the failure mode.
func TestDisconnectEndsSessionButKeepsPower(t *testing.T) {
	cfg := asiair.DefaultConfig()
	hub, _, gpio, _ := newTestHub(t, cfg, cfg.PWMChannel)
	s := NewSwitch(hub)
	ctx := context.Background()
	if err := s.Open(ctx); err != nil {
		t.Fatal(err)
	}
	defer s.Close(ctx)
	if err := s.Connect(ctx); err != nil {
		t.Fatal(err)
	}

	id := findSlot(t, s, "Port 3") // an on/off port: the mount or the camera
	if err := s.SetSwitch(id, true); err != nil {
		t.Fatal(err)
	}
	if !gpio[2].Value() {
		t.Fatal("setup: port 3 not on")
	}

	if err := s.Disconnect(ctx); err != nil {
		t.Fatal(err)
	}

	// ASCOM half: the session is over.
	if s.Connected() {
		t.Error("Connected() = true after Disconnect")
	}
	if _, err := s.GetSwitchValue(id); !errors.Is(err, alpacadev.ErrNotConnected) {
		t.Errorf("GetSwitchValue after Disconnect = %v, want NotConnected", err)
	}
	if _, err := s.Action("SequenceStatus", ""); !errors.Is(err, alpacadev.ErrNotConnected) {
		t.Errorf("Action after Disconnect = %v, want NotConnected", err)
	}

	// Hardware half: the mount is still powered.
	if gpio[2].Closed() {
		t.Fatal("Disconnect released the GPIO line — the mount just lost power")
	}
	if !gpio[2].Value() {
		t.Fatal("port 3 switched off across a Disconnect")
	}

	// And a reconnecting client finds its ports exactly as it left them.
	if err := s.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	if on, _ := s.GetSwitch(id); !on {
		t.Error("port 3 reads off after reconnecting")
	}
}
