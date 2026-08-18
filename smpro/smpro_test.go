package driver

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"testing"
	"time"

	alpacadev "github.com/mikefsq/goalpaca/server"
	"github.com/mikefsq/stellarmate"
	"github.com/mikefsq/stellarmate/bus"
	"github.com/mikefsq/stellarmate/eeprom"
	"github.com/mikefsq/stellarmate/mcp23008"
	"github.com/mikefsq/stellarmate/mcp3208"
	"github.com/mikefsq/stellarmate/mcp4725"
	"github.com/mikefsq/stellarmate/tmc2209"
)

// newTestHub wires a Hub over a Board built from the stellarmate bus fakes —
// no hardware, mirroring the library's own test harness. The fake ADC always
// reads mid-scale (≈13.5 V input), so the variable-output paths work.
func newTestHub(t *testing.T) (*Hub, *bus.FakePWM) {
	t.Helper()
	gpio := &bus.FakeI2C{OnWrite: func(f *bus.FakeI2C, p []byte) {
		if len(p) == 2 && p[0] == mcp23008.RegOLAT {
			f.Regs[mcp23008.RegGPIO] = p[1]
		}
	}}
	adc := &bus.FakeSPI{Xfer: func(tx []byte) []byte {
		return []byte{0x00, 0x09, 0xF0} // code 2544: vin ≈ 13.5 V
	}}
	// The MCP4725 answers a plain read with [status, dacHi, dacLo] regardless of what
	// was last written, so the fake has to mirror a written code into that shape —
	// otherwise a read-back of the DAC returns code 0, i.e. the ceiling, and the
	// setpoint round-trip silently "passes" while measuring nothing.
	dacI2C := &bus.FakeI2C{OnWrite: func(f *bus.FakeI2C, p []byte) {
		if len(p) == 3 && p[0] == 0x40 { // write-DAC-register command
			f.Regs[0x40] = 0xC0 // status byte
			f.Regs[0x41] = p[1] // DAC high 8 bits
			f.Regs[0x42] = p[2] // DAC low 4 bits, left-justified
		}
	}}
	eepI2C := &bus.FakeI2C{}
	eepI2C.Regs[eeprom.SerialOffset] = 0x0E
	serial := &bus.FakeSerial{}
	st := tmc2209.New(serial)
	st.Settle = 0
	dew0 := &bus.FakePWM{}
	dev := stellarmate.Devices{
		GPIO:    mcp23008.New(gpio),
		ADC:     mcp3208.New(adc),
		DAC:     mcp4725.New(dacI2C),
		EEPROM:  eeprom.New(eepI2C),
		Dew:     [2]bus.PWM{dew0, &bus.FakePWM{}},
		Stepper: st,
		UserLED: &bus.FakeGPIO{},
	}
	hub := &Hub{openBoard: func() (*stellarmate.Board, []error, error) {
		return stellarmate.NewBoard(stellarmate.DefaultConfig(), dev), nil, nil
	}}
	if err := hub.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = hub.Close(context.Background()) })
	return hub, dew0
}

// connectedSwitch builds the Switch and opens an ASCOM session on it. Connect is
// now a real step: the board being open is the HARDWARE being available, while
// Connected is this client's session, and operational members fault with
// NotConnected until the session is live. See session.go.
func connectedSwitch(t *testing.T, hub *Hub) *SMProSwitch {
	t.Helper()
	s := NewSwitch(hub)
	if err := s.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	return s
}

func connectedFocuser(t *testing.T, hub *Hub) *SMProFocuser {
	t.Helper()
	f := NewFocuser(hub)
	if err := f.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	return f
}

// Every slot's metadata must satisfy the ISwitchV3 constraints ConformU checks:
// a positive step, max > min, and the range an exact multiple of the step.
func TestSlotTableCompliance(t *testing.T) {
	hub, _ := newTestHub(t)
	s := connectedSwitch(t, hub)
	for id := 0; id < s.MaxSwitch(); id++ {
		name, _ := s.GetSwitchName(id)
		min, _ := s.MinSwitchValue(id)
		max, _ := s.MaxSwitchValue(id)
		step, _ := s.SwitchStep(id)
		if step <= 0 || max <= min {
			t.Errorf("slot %d (%s): min=%v max=%v step=%v", id, name, min, max, step)
		}
		ratio := (max - min) / step
		if math.Abs(ratio-math.Round(ratio)) > 1e-9 {
			t.Errorf("slot %d (%s): range %v not a multiple of step %v", id, name, max-min, step)
		}
		if desc, _ := s.GetSwitchDescription(id); desc == "" || name == "" {
			t.Errorf("slot %d: empty name or description", id)
		}
	}
}

// The boolean and analog views must agree per ISwitchV3: SetSwitch(true) drives
// MaxSwitchValue, false drives MinSwitchValue, and GetSwitch is false only at min.
func TestBooleanAnalogViews(t *testing.T) {
	hub, _ := newTestHub(t)
	s := connectedSwitch(t, hub)

	// Slot 0: Power 1 (binary).
	if err := s.SetSwitch(0, true); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.GetSwitchValue(0); v != 1 {
		t.Errorf("power on: value = %v, want 1", v)
	}
	if on, _ := s.GetSwitch(0); !on {
		t.Error("power on: GetSwitch = false")
	}

	// Slot 4: Dew Heater 1 (0..100). SetSwitch(true) must drive it to Max.
	if err := s.SetSwitch(4, true); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.GetSwitchValue(4); v != 100 {
		t.Errorf("dew SetSwitch(true): value = %v, want 100", v)
	}
	if err := s.SetSwitchValue(4, 60); err != nil {
		t.Fatal(err)
	}
	if on, _ := s.GetSwitch(4); !on {
		t.Error("dew at 60%: GetSwitch = false, want true (only min reads false)")
	}
	if err := s.SetSwitch(4, false); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.GetSwitchValue(4); v != 0 {
		t.Errorf("dew SetSwitch(false): value = %v, want 0", v)
	}
	if on, _ := s.GetSwitch(4); on {
		t.Error("dew at 0%: GetSwitch = true, want false")
	}
}

func TestReadOnlySensors(t *testing.T) {
	hub, _ := newTestHub(t)
	s := connectedSwitch(t, hub)
	for id := 13; id <= 16; id++ {
		if w, _ := s.CanWrite(id); w {
			t.Errorf("slot %d: CanWrite = true, want false", id)
		}
		if err := s.SetSwitchValue(id, 1); !errors.Is(err, alpacadev.ErrNotImplemented) {
			t.Errorf("slot %d: write err = %v, want NotImplemented", id, err)
		}
	}
	// The input-voltage sensor must read the fake supply (≈13.5 V).
	v, err := s.GetSwitchValue(13)
	if err != nil {
		t.Fatal(err)
	}
	if v < 12 || v > 15 {
		t.Errorf("input voltage = %v, want ≈13.5", v)
	}
}

func TestSetSwitchValueRangeAndRounding(t *testing.T) {
	hub, _ := newTestHub(t)
	s := connectedSwitch(t, hub)
	// Out of range is an InvalidValue error, not a clamp.
	if err := s.SetSwitchValue(4, 101); err == nil {
		t.Error("dew 101%: want InvalidValue error")
	}
	if err := s.SetSwitchValue(4, -1); err == nil {
		t.Error("dew -1%: want InvalidValue error")
	}
	// Off-step values round to the nearest step rather than erroring.
	if err := s.SetSwitchValue(4, 49.6); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.GetSwitchValue(4); v != 50 {
		t.Errorf("dew 49.6%%: value = %v, want 50 (rounded to step)", v)
	}
}

// Toggling an Auto Dew slot with fresh conditions applied must drive the heater.
func TestAutoDewSlotAndEnvironmentAction(t *testing.T) {
	hub, dew0 := newTestHub(t)
	s := connectedSwitch(t, hub)

	// Push dewing conditions (margin ≈ 0.8 °C), then enable auto-dew 1.
	if _, err := s.Action("SetEnvironment", `{"temperature_c":12,"humidity_pct":95}`); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSwitch(11, true); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.GetSwitchValue(4); v != 100 {
		t.Errorf("auto-dew at 0.8 °C margin: duty = %v, want 100", v)
	}
	if !dew0.Enabled {
		t.Error("dew PWM channel not enabled")
	}

	// Empty params reads back the stored conditions, fresh.
	reply, err := s.Action("setenvironment", "")
	if err != nil {
		t.Fatal(err)
	}
	var r envReply
	if err := json.Unmarshal([]byte(reply), &r); err != nil {
		t.Fatalf("read reply %q: %v", reply, err)
	}
	if r.TemperatureC != 12 || r.Stale {
		t.Errorf("environment = %+v, want temperature_c 12 and stale false", r)
	}
	if math.Abs(r.DewpointC-11.2) > 0.3 {
		t.Errorf("dewpoint_c = %v, want ≈11.2 (derived)", r.DewpointC)
	}

	// SetAutoDew partial update: flip Enabled off without restating the ramp.
	if _, err := s.Action("SetAutoDew", `{"channel":1,"enabled":false}`); err != nil {
		t.Fatal(err)
	}
	if on, _ := s.GetSwitch(11); on {
		t.Error("auto-dew 1 still enabled after SetAutoDew disable")
	}
}

// The SM Pro must accept the MGPBox's environment payload verbatim — the same
// snapshot it pushes to the tenmicron mount. The refraction/site/time fields mean
// nothing to a power board and must be tolerated, not rejected.
func TestAcceptsMGPBoxPayload(t *testing.T) {
	hub, _ := newTestHub(t)
	s := connectedSwitch(t, hub)

	// Byte-for-byte the shape mgpbox/feed.go builds (envPayload).
	const mgpboxSnapshot = `{"pressure_hpa":1013.2,"temperature_c":12,"humidity_pct":95,` +
		`"dewpoint_c":11.1,"latitude":37.77,"longitude":-122.42,"elevation_m":15.5,` +
		`"time":"2026-07-14T04:05:06Z"}`

	reply, err := s.Action("setenvironment", mgpboxSnapshot)
	if err != nil {
		t.Fatalf("MGPBox snapshot rejected: %v", err)
	}
	var applied struct {
		Applied []string `json:"applied"`
	}
	if err := json.Unmarshal([]byte(reply), &applied); err != nil {
		t.Fatal(err)
	}
	if len(applied.Applied) != 3 {
		t.Errorf("applied = %v, want temperature + humidity + dewpoint", applied.Applied)
	}
	// A plausible sensor dew point wins over our estimate.
	env, _ := s.Action("setenvironment", "")
	var r envReply
	_ = json.Unmarshal([]byte(env), &r)
	if r.DewpointC != 11.1 {
		t.Errorf("dewpoint_c = %v, want the sensor's 11.1", r.DewpointC)
	}
}

// The dew point must be derived whenever the feeder's value is unusable. A feeder
// that does not compute one (the MGPBox) still sends the field as 0; trusting that
// zero gives a margin of the whole air temperature — heaters off on a dewing night.
func TestDewpointDerivedWhenUnusable(t *testing.T) {
	tests := []struct {
		name    string
		payload string
	}{
		// The MGPBox case: the field is present, but the box computes no dew point.
		{"zero dew point", `{"temperature_c":12,"humidity_pct":95,"dewpoint_c":0}`},
		// A feeder that omits the field entirely.
		{"absent dew point", `{"temperature_c":12,"humidity_pct":95}`},
		// Unphysical: a dew point above the air temperature (broken/mis-scaled sensor).
		{"dew point above air temp", `{"temperature_c":12,"humidity_pct":95,"dewpoint_c":30}`},
	}
	for _, tt := range tests {
		hub, _ := newTestHub(t)
		s := connectedSwitch(t, hub)
		if _, err := s.Action("setenvironment", tt.payload); err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}
		env, _ := s.Action("setenvironment", "")
		var r envReply
		if err := json.Unmarshal([]byte(env), &r); err != nil {
			t.Fatal(err)
		}
		// 12 °C at 95 %RH derives to ≈11.2 °C: a 0.8 °C margin, not a 12 °C one.
		if math.Abs(r.DewpointC-11.2) > 0.3 {
			t.Errorf("%s: dewpoint_c = %v, want ≈11.2 (derived)", tt.name, r.DewpointC)
		}
		if r.MarginC > 1.5 {
			t.Errorf("%s: margin = %v °C — heaters would stay off on a dewing night", tt.name, r.MarginC)
		}
		// And the heater must actually run.
		if err := s.SetSwitch(11, true); err != nil {
			t.Fatal(err)
		}
		if v, _ := s.GetSwitchValue(4); v != 100 {
			t.Errorf("%s: dew duty = %v, want 100", tt.name, v)
		}
	}
}

// Temperature with no humidity and no usable dew point is not actionable: there is
// nothing to compute a margin from, so it must not be stored (storing it alone
// would read as a huge margin and switch the heaters off).
func TestTemperatureOnlyIsNotActionable(t *testing.T) {
	hub, _ := newTestHub(t)
	s := connectedSwitch(t, hub)
	reply, err := s.Action("setenvironment", `{"temperature_c":12,"dewpoint_c":0}`)
	if err != nil {
		t.Fatal(err)
	}
	if reply != `{"applied":[]}` {
		t.Errorf("temperature-only push applied = %s, want [] (not actionable)", reply)
	}
	env, _ := s.Action("setenvironment", "")
	var r envReply
	_ = json.Unmarshal([]byte(env), &r)
	if r.Stale != true {
		t.Error("weather should still be absent/stale after a non-actionable push")
	}
}

// A pressure/site-only push (a feeder whose GPS has a fix but whose meteo sensor
// has no sample yet) must leave the conditions — and the heaters — untouched. A
// blind overwrite would zero temperature and humidity, producing a margin of the
// whole air temperature: "bone dry", heaters off, on a dewing night.
func TestSiteOnlyPushDoesNotClobberWeather(t *testing.T) {
	hub, _ := newTestHub(t)
	s := connectedSwitch(t, hub)

	if _, err := s.Action("setenvironment", `{"temperature_c":12,"humidity_pct":95}`); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSwitch(11, true); err != nil { // auto-dew 1 on -> 100%
		t.Fatal(err)
	}

	// Now a snapshot carrying no weather at all.
	reply, err := s.Action("setenvironment", `{"pressure_hpa":1013.2,"latitude":37.77}`)
	if err != nil {
		t.Fatal(err)
	}
	if reply != `{"applied":[]}` {
		t.Errorf("site-only push applied = %s, want []", reply)
	}
	env, _ := s.Action("setenvironment", "")
	var r envReply
	_ = json.Unmarshal([]byte(env), &r)
	if r.TemperatureC != 12 || r.HumidityPct != 95 {
		t.Errorf("site-only push clobbered the weather: %+v", r)
	}
	if v, _ := s.GetSwitchValue(4); v != 100 {
		t.Errorf("dew duty = %v after site-only push, want 100 held", v)
	}
}

func TestFocuserMoveAndHalt(t *testing.T) {
	hub, _ := newTestHub(t)
	f := connectedFocuser(t, hub)
	if !f.Absolute() {
		t.Error("Absolute = false")
	}
	start, err := f.Position()
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Move(start + 100); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for f.IsMoving() {
		if time.Now().After(deadline) {
			t.Fatal("move did not complete")
		}
		time.Sleep(2 * time.Millisecond)
	}
	if pos, _ := f.Position(); pos != start+100 {
		t.Errorf("position = %d, want %d", pos, start+100)
	}

	// A long move can be halted, and Halt is not an error when idle either.
	if err := f.Move(start + 8000); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	if err := f.Halt(); err != nil {
		t.Fatal(err)
	}
	if f.IsMoving() {
		t.Error("still moving after Halt")
	}
	if err := f.Halt(); err != nil {
		t.Errorf("Halt while idle: %v", err)
	}
}

// The hub refcounts Open/Close: the board must survive one device's Close and
// be released by the second.
func TestHubRefcount(t *testing.T) {
	hub, _ := newTestHub(t) // refs = 1 (cleanup closes it)
	ctx := context.Background()
	if err := hub.Open(ctx); err != nil { // refs = 2
		t.Fatal(err)
	}
	if err := hub.Close(ctx); err != nil { // refs = 1
		t.Fatal(err)
	}
	if !hub.Ready() {
		t.Fatal("board released while a device still holds the hub")
	}
	if _, err := hub.Board(); err != nil {
		t.Fatal(err)
	}
}

// The writable setpoint (slot 7) must be reachable: ASCOM requires GetSwitchValue to
// return what SetSwitchValue set, and the DAC's ceiling is ~0.8565*Vin — a 12 V max
// would clamp to ~11.5 and read back wrong. It must also stay the *programmed* value,
// not the measured rail: the output decays over tens of seconds after the enable drops,
// so a measurement-backed setpoint would drift below MinSwitchValue and never settle.
func TestVariableVoltageSetpointIsReachable(t *testing.T) {
	hub, _ := newTestHub(t)
	s := connectedSwitch(t, hub)

	if mx, _ := s.MaxSwitchValue(7); mx != 11 {
		t.Errorf("Variable Voltage max = %v, want 11 (12 V is unreachable: ceiling is ~0.8565*Vin)", mx)
	}
	// SetSwitch(true) drives MaxSwitchValue; it must read back as what was set.
	if err := s.SetSwitch(7, true); err != nil {
		t.Fatal(err)
	}
	v, err := s.GetSwitchValue(7)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(v-11) > 0.15 {
		t.Errorf("setpoint after SetSwitch(true) = %.2f, want ~11 (the advertised max)", v)
	}
	// Writable, and the measured slot is not.
	if w, _ := s.CanWrite(7); !w {
		t.Error("slot 7 should be writable")
	}
	if w, _ := s.CanWrite(16); w {
		t.Error("slot 16 (measured) must be read-only")
	}
}

// Disconnect must end the ASCOM session — Connected goes false and operational
// members fault — WITHOUT releasing the board.
//
// This was a bug: Connected() reported hub.Ready(), i.e. "is the hardware open?",
// and Disconnect was a total no-op. So Connected stayed true forever after a
// client disconnected, and every member kept working. ASCOM requires the opposite.
//
// But the fix must not swing the other way. Releasing the board on Disconnect
// would cut the power outputs, the dew heaters and the variable rail the moment a
// client closed its session — and on the SM Pro that means the mount and the
// camera. The board belongs to the process; the session belongs to the client.
// Both halves are asserted here, because getting one right by breaking the other
// is the real failure mode.
func TestDisconnectEndsSessionButKeepsHardware(t *testing.T) {
	hub, _ := newTestHub(t)
	ctx := context.Background()
	s := connectedSwitch(t, hub)

	if err := s.SetSwitch(0, true); err != nil { // Power 1 on
		t.Fatal(err)
	}
	if on, _ := s.GetSwitch(0); !on {
		t.Fatal("setup: power 1 not on")
	}

	if err := s.Disconnect(ctx); err != nil {
		t.Fatal(err)
	}

	// ASCOM half: the session is over.
	if s.Connected() {
		t.Error("Connected() = true after Disconnect")
	}
	if _, err := s.GetSwitchValue(0); !errors.Is(err, alpacadev.ErrNotConnected) {
		t.Errorf("GetSwitchValue after Disconnect = %v, want NotConnected", err)
	}
	if err := s.SetSwitch(0, false); !errors.Is(err, alpacadev.ErrNotConnected) {
		t.Errorf("SetSwitch after Disconnect = %v, want NotConnected", err)
	}
	if _, err := s.Action("SetEnvironment", ""); !errors.Is(err, alpacadev.ErrNotConnected) {
		t.Errorf("Action after Disconnect = %v, want NotConnected", err)
	}

	// Hardware half: the board is still open and Power 1 is still ON.
	if !hub.Ready() {
		t.Fatal("Disconnect released the board — the mount and camera just lost power")
	}
	b, err := hub.Board()
	if err != nil {
		t.Fatal(err)
	}
	if on, err := b.Power(0); err != nil || !on {
		t.Error("power 1 switched off across a Disconnect")
	}

	// A reconnecting client finds its outputs exactly as it left them.
	if err := s.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	if on, _ := s.GetSwitch(0); !on {
		t.Error("power 1 reads off after reconnecting")
	}
}

// The Switch and the Focuser hold SEPARATE sessions: a client may connect to one
// and leave the other alone. (They still share one board — that is the Hub's job.)
func TestSessionsArePerDevice(t *testing.T) {
	hub, _ := newTestHub(t)
	s := connectedSwitch(t, hub)
	f := NewFocuser(hub) // deliberately not connected

	if !s.Connected() {
		t.Error("switch not connected")
	}
	if f.Connected() {
		t.Error("focuser reports connected, but no client ever connected to it")
	}
	if _, err := f.Position(); !errors.Is(err, alpacadev.ErrNotConnected) {
		t.Errorf("focuser Position without a session = %v, want NotConnected", err)
	}
	// Disconnecting the switch must not disturb a focuser session.
	if err := f.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.Disconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !f.Connected() {
		t.Error("disconnecting the switch dropped the focuser's session")
	}
	if _, err := f.Position(); err != nil {
		t.Errorf("focuser Position after the switch disconnected: %v", err)
	}
}
