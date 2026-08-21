package driver

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/mikefsq/goasi/asiair"
	"github.com/mikefsq/goasi/asiair/ads1015"
	"github.com/mikefsq/goasi/asiair/bus"
	alpacadev "github.com/mikefsq/goalpaca/server"
)

// newTestHub wires a Hub over a Board built from the asiair bus fakes — no
// hardware, mirroring the library's own harness.
//
// pwmPorts says which ports get a real PWM channel. Passing the default
// {0,1,-1,-1} gives the shipping board; passing all -1 models the case where the
// pwm-2chan overlay is missing and Open fell back to on/off, which is the awkward
// path the Switch has to handle honestly.
func newTestHub(t *testing.T, cfg asiair.Config, pwmPorts [4]int) (*Hub, [4]*bus.FakePWM, [4]*bus.FakeGPIO, *bus.FakeGPIO) {
	t.Helper()

	var pwm [4]*bus.FakePWM
	var gpio [4]*bus.FakeGPIO
	shutter := &bus.FakeGPIO{}

	var dev asiair.Devices
	for p := 0; p < asiair.NumPorts; p++ {
		if pwmPorts[p] >= 0 {
			pwm[p] = &bus.FakePWM{}
			dev.PortPWM[p] = pwm[p]
		} else {
			gpio[p] = &bus.FakeGPIO{}
			dev.PortGPIO[p] = gpio[p]
		}
	}
	dev.Shutter = shutter

	// Telemetry samples: chip 0x48 (P1/P2 volts, P3/P4 amps), 0x49 (P3/P4 volts,
	// P1/P2 amps), 0x4B (main). AIN1 on 0x49 is −1: an idle shunt sitting a hair
	// below zero, which is what the clamp at the ASCOM boundary has to absorb.
	samples := []map[ads1015.Mux]int16{
		{ads1015.AIN0: 1200, ads1015.AIN2: 1150, ads1015.AIN1: 40, ads1015.AIN3: 80},
		{ads1015.AIN0: 1100, ads1015.AIN2: 1000, ads1015.AIN1: -1, ads1015.AIN3: 20},
		{ads1015.AIN2: 1300, ads1015.AIN3: 800},
	}
	for i := 0; i < 3; i++ {
		d := ads1015.New(ads1015.NewFake(samples[i]))
		d.Settle = 0
		dev.ADC[i] = d
	}

	cfg.PWMChannel = pwmPorts
	hub := &Hub{
		cfg: cfg,
		openBoard: func() (*asiair.Board, []error, error) {
			return asiair.NewBoard(cfg, dev), nil, nil
		},
	}
	return hub, pwm, gpio, shutter
}

// newDefault builds the shipping board: ports 1-2 dimmable.
func newDefault(t *testing.T) (*AsiairSwitch, [4]*bus.FakePWM, [4]*bus.FakeGPIO, *bus.FakeGPIO) {
	t.Helper()
	cfg := asiair.DefaultConfig()
	hub, pwm, gpio, shutter := newTestHub(t, cfg, cfg.PWMChannel)
	s := NewSwitch(hub)
	if err := s.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close(context.Background()) })
	if err := s.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	return s, pwm, gpio, shutter
}

// findSlot returns the id of the slot with the given name.
func findSlot(t *testing.T, s *AsiairSwitch, name string) int {
	t.Helper()
	for i := 0; i < s.MaxSwitch(); i++ {
		n, _ := s.GetSwitchName(i)
		if n == name {
			return i
		}
	}
	t.Fatalf("no slot named %q", name)
	return -1
}

// ===== Slot array =====

// The array must be the same length under both PWM pairings, so a client profile
// keyed on switch id survives a change of pairing.
func TestSlotCountStableAcrossPairings(t *testing.T) {
	a := len(newSlots(asiair.DefaultConfig()))
	b := len(newSlots(asiair.DefaultConfig().WithPWMPorts24()))
	if a != b {
		t.Errorf("slot count moved between pairings: %d vs %d", a, b)
	}
	if a != 17 {
		t.Errorf("slot count = %d, want 17 (4 ports + shutter + 2 auto-dew + 10 telemetry)", a)
	}
}

// A dimmable port is advertised as a 0..100 duty; a port that cannot dim is a
// plain boolean. That difference is the Pi's silicon, and the metadata must say so.
func TestPortSlotMetadata(t *testing.T) {
	s, _, _, _ := newDefault(t)

	for _, tc := range []struct {
		name string
		max  float64
	}{
		{"Port 1", 100}, // dimmable
		{"Port 2", 100}, // dimmable
		{"Port 3", 1},   // GPIO 26 — no PWM alt-function at all
		{"Port 4", 1},   // GPIO 18 — collides with Port 1 on PWM0
	} {
		id := findSlot(t, s, tc.name)
		max, _ := s.MaxSwitchValue(id)
		if max != tc.max {
			t.Errorf("%s MaxSwitchValue = %g, want %g", tc.name, max, tc.max)
		}
		if w, _ := s.CanWrite(id); !w {
			t.Errorf("%s CanWrite = false", tc.name)
		}
	}
}

// Under the ports-2+4 pairing the auto-dew slots must name the ports they really
// control — "Auto Dew 1" would be a lie there.
func TestAutoDewSlotsNameTheirPorts(t *testing.T) {
	names := func(cfg asiair.Config) []string {
		var out []string
		for _, sl := range newSlots(cfg) {
			if strings.HasPrefix(sl.name, "Auto Dew") {
				out = append(out, sl.name)
			}
		}
		return out
	}
	if got := names(asiair.DefaultConfig()); got[0] != "Auto Dew (Port 1)" || got[1] != "Auto Dew (Port 2)" {
		t.Errorf("default pairing auto-dew slots = %v", got)
	}
	if got := names(asiair.DefaultConfig().WithPWMPorts24()); got[0] != "Auto Dew (Port 2)" || got[1] != "Auto Dew (Port 4)" {
		t.Errorf("ports-2+4 pairing auto-dew slots = %v", got)
	}
}

// Telemetry slots are read-only.
func TestTelemetrySlotsAreReadOnly(t *testing.T) {
	s, _, _, _ := newDefault(t)
	for _, name := range []string{"Input Voltage", "Input Current", "Port 1 Voltage", "Port 4 Current"} {
		id := findSlot(t, s, name)
		if w, _ := s.CanWrite(id); w {
			t.Errorf("%s CanWrite = true, want read-only", name)
		}
		if err := s.SetSwitchValue(id, 1); err != alpacadev.ErrNotImplemented {
			t.Errorf("%s SetSwitchValue = %v, want ErrNotImplemented", name, err)
		}
	}
}

// ===== Ports =====

func TestSetPortDuty(t *testing.T) {
	s, _, _, _ := newDefault(t)
	id := findSlot(t, s, "Port 1")
	if err := s.SetSwitchValue(id, 60); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.GetSwitchValue(id); v != 60 {
		t.Errorf("Port 1 = %g, want 60", v)
	}
	// The boolean view: anything above Min is "on".
	if on, _ := s.GetSwitch(id); !on {
		t.Error("GetSwitch = false at 60% duty")
	}
	// SetSwitch(true) drives MaxSwitchValue, i.e. full duty.
	if err := s.SetSwitch(id, true); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.GetSwitchValue(id); v != 100 {
		t.Errorf("after SetSwitch(true), Port 1 = %g, want 100", v)
	}
}

func TestSetPortBoolean(t *testing.T) {
	s, _, gpio, _ := newDefault(t)
	id := findSlot(t, s, "Port 3")
	if err := s.SetSwitch(id, true); err != nil {
		t.Fatal(err)
	}
	if !gpio[2].Value() {
		t.Error("port 3 line not driven high")
	}
	if on, _ := s.GetSwitch(id); !on {
		t.Error("GetSwitch(port 3) = false after SetSwitch(true)")
	}
}

func TestSetSwitchValueOutOfRange(t *testing.T) {
	s, _, _, _ := newDefault(t)
	id := findSlot(t, s, "Port 1")
	if err := s.SetSwitchValue(id, 101); err == nil {
		t.Error("SetSwitchValue(101) on a 0..100 slot returned nil")
	}
	if err := s.SetSwitchValue(id, -1); err == nil {
		t.Error("SetSwitchValue(-1) returned nil")
	}
}

// The awkward path: the config says a port is dimmable, but the overlay is
// missing and the library fell back to on/off. 0 and 100 must still work — the
// port really can be switched — and only a fractional duty may fail, with an error
// that says what to do about it.
func TestFallbackPortWhenOverlayMissing(t *testing.T) {
	cfg := asiair.DefaultConfig()
	// The board has no PWM at all: every port fell back to a GPIO line.
	hub, _, gpio, _ := newTestHub(t, cfg, [4]int{-1, -1, -1, -1})
	// ...but the Switch's metadata still comes from the config, which says ports
	// 1 and 2 are dimmable. This is exactly the disagreement to handle.
	hub.cfg = cfg
	s := NewSwitch(hub)
	if err := s.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	if err := s.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}

	id := findSlot(t, s, "Port 1")
	if max, _ := s.MaxSwitchValue(id); max != 100 {
		t.Fatalf("Port 1 max = %g, want 100 (metadata comes from config)", max)
	}

	// Full on works.
	if err := s.SetSwitchValue(id, 100); err != nil {
		t.Errorf("SetSwitchValue(100) on a fallback port: %v — it must still switch", err)
	}
	if !gpio[0].Value() {
		t.Error("port 1 line not driven high")
	}
	if v, _ := s.GetSwitchValue(id); v != 100 {
		t.Errorf("GetSwitchValue = %g, want 100", v)
	}

	// Off works.
	if err := s.SetSwitchValue(id, 0); err != nil {
		t.Errorf("SetSwitchValue(0) on a fallback port: %v", err)
	}
	if gpio[0].Value() {
		t.Error("port 1 line still high after 0")
	}

	// A fractional duty cannot work, and must say why rather than silently
	// rounding to full power — a user who never learns their dew heater has no PWM
	// spends a season wondering why it only runs flat out.
	err := s.SetSwitchValue(id, 60)
	if err == nil {
		t.Fatal("SetSwitchValue(60) on a non-dimmable port returned nil")
	}
	if !strings.Contains(err.Error(), "pwm-2chan") {
		t.Errorf("error = %q, want it to name the missing overlay", err)
	}
}

// ===== Telemetry =====

func TestTelemetryValues(t *testing.T) {
	s, _, _, _ := newDefault(t)
	for _, tc := range []struct {
		name string
		want float64
	}{
		{"Input Voltage", 13.65},  // 1300 * 21/2000
		{"Input Current", 4.0},    // 800 / 200
		{"Port 1 Voltage", 12.60}, // 1200 * 21/2000
		{"Port 3 Voltage", 11.55}, // 1100 * 21/2000 (on the OTHER chip)
		{"Port 1 Current", 0.25},  // 20 / 80 (on the OTHER chip)
		{"Port 3 Current", 1.0},   // 80 / 80
	} {
		id := findSlot(t, s, tc.name)
		v, err := s.GetSwitchValue(id)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if math.Abs(v-tc.want) > 1e-6 {
			t.Errorf("%s = %g, want %g", tc.name, v, tc.want)
		}
	}
}

// An idle current shunt reads a hair below zero (the ADC result is signed). ASCOM
// requires GetSwitchValue to fall within Min..Max, so the noise is clamped at the
// boundary — but it must be clamped to 0, not wrapped into 51 amps.
func TestIdleShuntClampsToZeroNotFiftyAmps(t *testing.T) {
	s, _, _, _ := newDefault(t)
	id := findSlot(t, s, "Port 2 Current") // seeded at −1 LSB
	v, err := s.GetSwitchValue(id)
	if err != nil {
		t.Fatal(err)
	}
	if v != 0 {
		t.Errorf("idle port 2 current = %g A, want 0 (clamped)", v)
	}
	min, _ := s.MinSwitchValue(id)
	max, _ := s.MaxSwitchValue(id)
	if v < min || v > max {
		t.Errorf("value %g outside the advertised %g..%g — ISwitchV3 violation", v, min, max)
	}
}

// ===== Shutter =====

// The shutter is a LEVEL, not a momentary: a bulb exposure needs the contact held.
func TestShutterIsALevel(t *testing.T) {
	s, _, _, shutter := newDefault(t)
	id := findSlot(t, s, "DSLR Shutter")

	if err := s.SetSwitch(id, true); err != nil {
		t.Fatal(err)
	}
	if !shutter.Value() {
		t.Error("shutter contact not closed")
	}
	// It must STAY closed — that is the whole point of a level.
	time.Sleep(30 * time.Millisecond)
	if !shutter.Value() {
		t.Error("shutter contact opened on its own — a bulb exposure would be cut short")
	}
	if on, _ := s.GetSwitch(id); !on {
		t.Error("GetSwitch = false while the contact is closed")
	}

	if err := s.SetSwitch(id, false); err != nil {
		t.Fatal(err)
	}
	if shutter.Value() {
		t.Error("shutter contact still closed")
	}
}

// ===== Actions =====

func TestSupportedActions(t *testing.T) {
	s, _, _, _ := newDefault(t)
	got := s.SupportedActions()
	want := []string{"SetEnvironment", "SetAutoDew", "AutoDew", "StartSequence", "AbortSequence", "SequenceStatus"}
	if len(got) != len(want) {
		t.Fatalf("SupportedActions = %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("action %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// The environment push drives the heater, and takes the same payload the mount and
// the SM Pro take — one feeder, one schema, three consumers.
func TestSetEnvironmentDrivesAutoDew(t *testing.T) {
	s, _, _, _ := newDefault(t)

	if _, err := s.Action("SetAutoDew", `{"port":1,"on":2,"off":10,"max":100,"enabled":true}`); err != nil {
		t.Fatal(err)
	}
	// A full MGPBox-shaped payload: the site and pressure fields must be tolerated
	// and ignored, not rejected.
	_, err := s.Action("SetEnvironment",
		`{"temperature_c":6.0,"humidity_pct":100,"dewpoint_c":6.0,"pressure_hpa":1013,"latitude":51.5,"time":"2026-07-14T22:00:00Z"}`)
	if err != nil {
		t.Fatal(err)
	}

	// Margin 0 °C -> dewing hard -> full power.
	id := findSlot(t, s, "Port 1")
	if v, _ := s.GetSwitchValue(id); v != 100 {
		t.Errorf("duty = %g at a 0 °C margin, want 100", v)
	}

	// Dry night: margin 15 -> off.
	if _, err := s.Action("SetEnvironment", `{"temperature_c":20,"humidity_pct":38,"dewpoint_c":5}`); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.GetSwitchValue(id); v != 0 {
		t.Errorf("duty = %g at a 15 °C margin, want 0", v)
	}
}

// A feeder that sends dewpoint_c as a literal 0 (the MGPBox does) must not be
// taken at face value: that reads as a margin of the whole air temperature — "bone
// dry" — and would switch the heaters off on exactly the night they are needed.
func TestZeroDewpointIsDerivedNotTrusted(t *testing.T) {
	s, _, _, _ := newDefault(t)
	if _, err := s.Action("SetEnvironment", `{"temperature_c":10,"humidity_pct":95,"dewpoint_c":0}`); err != nil {
		t.Fatal(err)
	}
	out, err := s.Action("SetEnvironment", "") // dual-mode read
	if err != nil {
		t.Fatal(err)
	}
	var r envReply
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatal(err)
	}
	if r.DewpointC == 0 {
		t.Fatal("dewpoint left at 0 — a 95% RH night would read as bone dry")
	}
	if r.MarginC > 2 {
		t.Errorf("margin = %.2f at 95%% RH, want small", r.MarginC)
	}
}

// A pressure-only push (a feeder whose GPS has a fix but whose meteo sensor has no
// sample yet) must leave the heaters exactly as they are.
func TestPressureOnlyPushIsIgnored(t *testing.T) {
	s, _, _, _ := newDefault(t)
	out, err := s.Action("SetEnvironment", `{"pressure_hpa":1013,"latitude":51.5}`)
	if err != nil {
		t.Fatal(err)
	}
	var r struct {
		Applied []string `json:"applied"`
	}
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatal(err)
	}
	if len(r.Applied) != 0 {
		t.Errorf("applied = %v, want nothing", r.Applied)
	}
}

// smpro-shaped clients say "channel"; this board says "port". Both must work.
func TestAutoDewChannelAlias(t *testing.T) {
	s, _, _, _ := newDefault(t)
	if _, err := s.Action("SetAutoDew", `{"channel":2,"on":3,"off":9,"max":80,"enabled":true}`); err != nil {
		t.Fatal(err)
	}
	out, err := s.Action("AutoDew", `{"port":2}`)
	if err != nil {
		t.Fatal(err)
	}
	var a autoDewJSON
	if err := json.Unmarshal([]byte(out), &a); err != nil {
		t.Fatal(err)
	}
	if a.Max == nil || *a.Max != 80 || a.On == nil || *a.On != 3 || a.Enabled == nil || !*a.Enabled {
		t.Errorf("AutoDew reply = %s, want the ramp set via the channel alias", out)
	}
}

// Auto-dew on a port that cannot dim must be refused, not silently accepted — a
// client would otherwise believe a ramp is running while the heater sits off.
func TestAutoDewRefusedOnNonDimmablePort(t *testing.T) {
	s, _, _, _ := newDefault(t)
	_, err := s.Action("SetAutoDew", `{"port":3,"enabled":true}`)
	if err == nil {
		t.Fatal("SetAutoDew on port 3 returned nil")
	}
	if !strings.Contains(err.Error(), "hardware PWM") {
		t.Errorf("error = %q, want it to explain that port 3 cannot dim", err)
	}
}

// The DSLR sequence: start, follow, abort — and the shutter must end up open.
func TestSequenceActions(t *testing.T) {
	s, _, _, shutter := newDefault(t)

	if _, err := s.Action("StartSequence", `{"frames":5,"exposure_s":1,"delay_s":0.1}`); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)

	out, err := s.Action("SequenceStatus", "")
	if err != nil {
		t.Fatal(err)
	}
	var st sequenceJSON
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		t.Fatal(err)
	}
	if st.Running == nil || !*st.Running || st.Frames != 5 {
		t.Errorf("status = %s, want a running 5-frame sequence", out)
	}
	if !shutter.Value() {
		t.Error("shutter not closed during the first exposure")
	}

	if _, err := s.Action("AbortSequence", ""); err != nil {
		t.Fatal(err)
	}
	if shutter.Value() {
		t.Fatal("shutter still CLOSED after AbortSequence — the DSLR is stuck in a bulb exposure")
	}
	// And it must stay open: no stale write may re-close it.
	time.Sleep(150 * time.Millisecond)
	if shutter.Value() {
		t.Fatal("a stale write re-closed the shutter after AbortSequence")
	}

	out, _ = s.Action("SequenceStatus", "")
	json.Unmarshal([]byte(out), &st)
	if st.Running == nil || *st.Running {
		t.Errorf("status after abort = %s, want not running", out)
	}
}

// Abort must be safe when nothing is running: a client that has lost track of the
// board should be able to say "stop, whatever you are doing".
func TestAbortWhenIdleIsSafe(t *testing.T) {
	s, _, _, _ := newDefault(t)
	if _, err := s.Action("AbortSequence", ""); err != nil {
		t.Errorf("AbortSequence while idle = %v, want nil", err)
	}
}

func TestUnknownAction(t *testing.T) {
	s, _, _, _ := newDefault(t)
	if _, err := s.Action("Nonsense", ""); err != alpacadev.ErrActionNotImplemented {
		t.Errorf("unknown action = %v, want ErrActionNotImplemented", err)
	}
}

// ===== Lifecycle =====

// Close, on the other hand, IS a real teardown — and it does drop the lines. That
// is correct at process exit (a driver that has stopped running should not leave
// rails it can no longer control), and it is why Disconnect must not call it.
func TestCloseReleasesTheBoard(t *testing.T) {
	cfg := asiair.DefaultConfig()
	hub, _, gpio, _ := newTestHub(t, cfg, cfg.PWMChannel)
	s := NewSwitch(hub)
	if err := s.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSwitch(findSlot(t, s, "Port 3"), true); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !gpio[2].Closed() {
		t.Error("Close did not release the GPIO line")
	}
	if s.Connected() {
		t.Error("Connected() = true after Close")
	}
	if _, err := s.GetSwitchValue(0); err != alpacadev.ErrNotConnected {
		t.Errorf("GetSwitchValue after Close = %v, want ErrNotConnected", err)
	}
}

// Open/Close are refcounted, so a composed host running the device's Hardware
// lifecycle more than once does not tear the board down under itself.
func TestHubRefcount(t *testing.T) {
	cfg := asiair.DefaultConfig()
	hub, _, _, _ := newTestHub(t, cfg, cfg.PWMChannel)
	ctx := context.Background()
	if err := hub.Open(ctx); err != nil {
		t.Fatal(err)
	}
	if err := hub.Open(ctx); err != nil {
		t.Fatal(err)
	}
	if err := hub.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if !hub.Ready() {
		t.Fatal("board released while a reference was still held")
	}
	if err := hub.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if hub.Ready() {
		t.Error("board still open after the last Close")
	}
}

func TestNotConnected(t *testing.T) {
	cfg := asiair.DefaultConfig()
	hub, _, _, _ := newTestHub(t, cfg, cfg.PWMChannel)
	s := NewSwitch(hub)
	if s.Connected() {
		t.Error("Connected() = true before Open")
	}
	if err := s.Connect(context.Background()); err != alpacadev.ErrNotConnected {
		t.Errorf("Connect before Open = %v, want ErrNotConnected", err)
	}
	if _, err := s.Action("SequenceStatus", ""); err != alpacadev.ErrNotConnected {
		t.Errorf("Action before Open = %v, want ErrNotConnected", err)
	}
}
