package driver

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	alpacadev "github.com/mikefsq/goalpaca/server"
	scopehub "github.com/mikefsq/scope_hub/driver"
)

type fakeDev struct {
	st     scopehub.Status
	dew    scopehub.Dew
	aux    scopehub.Aux
	labels scopehub.Labels
	saved  int
	broken bool
	closed bool
}

func newFake() *fakeDev {
	f := &fakeDev{}
	f.st.Input = scopehub.Reading{Valid: true, Volts: 13.2, Amps: 0.5}
	f.st.DCPWMHz = 1
	f.st.LEDBrightness = 10
	f.dew = scopehub.Dew{Valid: true, AmbientC: 10, DewpointC: 6, MarginC: 4,
		DewSettings: scopehub.DewSettings{StartC: 6, FullC: 1, MinPct: 10, MaxPct: 100, FailPct: 50}}
	for i := 0; i < ports; i++ {
		f.st.DC = append(f.st.DC, scopehub.DCPort{Port: i + 1, AmpsValid: true})
		f.st.USB = append(f.st.USB, scopehub.USBPort{Port: i + 1, Enabled: true})
	}
	return f
}

func (f *fakeDev) Info() scopehub.Info { return scopehub.Info{Serial: "TEST"} }
func (f *fakeDev) Status() (*scopehub.Status, error) {
	if f.broken {
		return nil, errors.New("link down")
	}
	s := f.st
	return &s, nil
}
func (f *fakeDev) SetDC(port, duty int) error {
	if duty < 0 || duty > 100 {
		return scopehub.ErrOutOfRange
	}
	f.st.DC[port-1].Duty = duty
	f.st.DC[port-1].Amps = 0.01 * float64(duty)
	return nil
}
func (f *fakeDev) SetUSB(port int, on bool) error { f.st.USB[port-1].Enabled = on; return nil }
func (f *fakeDev) SetDCEnabled(port int, on bool) error {
	f.st.DC[port-1].Enabled = on
	return nil
}
func (f *fakeDev) SetDCPWM(port int, on bool) error { f.st.DC[port-1].PWM = on; return nil }
func (f *fakeDev) SetLEDBrightness(p int) error     { f.st.LEDBrightness = p; return nil }
func (f *fakeDev) SetDCPWMHz(hz float64) (float64, error) {
	f.st.DCPWMHz = hz
	return hz, nil
}
func (f *fakeDev) Close() error { f.closed = true; return nil }
func (f *fakeDev) Labels() (scopehub.Labels, error) {
	if f.broken {
		return scopehub.Labels{}, errors.New("link down")
	}
	return f.labels, nil
}
func (f *fakeDev) SetDCLabel(port int, name string) error {
	if len(name) > scopehub.MaxLabelLen {
		return scopehub.ErrOutOfRange
	}
	f.labels.DC[port-1] = name
	return nil
}
func (f *fakeDev) SetUSBLabel(port int, name string) error { f.labels.USB[port-1] = name; return nil }
func (f *fakeDev) Dew() (*scopehub.Dew, error) {
	if f.broken {
		return nil, errors.New("link down")
	}
	w := f.dew
	return &w, nil
}
func (f *fakeDev) SetDCAuto(port int, on bool) error { f.dew.Auto[port-1] = on; return nil }
func (f *fakeDev) Aux() (*scopehub.Aux, error) {
	if f.broken {
		return nil, errors.New("link down")
	}
	a := f.aux
	return &a, nil
}
func (f *fakeDev) SetLEDAuto(on bool) error { f.aux.LEDAuto = on; return nil }
func (f *fakeDev) Save() error              { f.saved++; return nil }
func (f *fakeDev) Defaults() error          { return nil }
func (f *fakeDev) SetDewSettings(s scopehub.DewSettings) error {
	f.dew.DewSettings = s
	return nil
}

func newTestSwitch(t *testing.T, f *fakeDev) *Switch {
	t.Helper()
	s := NewSwitch(Config{})
	s.b.open = func(string) (device, error) { return f, nil }
	if err := s.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Board is shared per serial process-wide. Release it so next test opens own fake.
	t.Cleanup(func() { s.Close(context.Background()) })
	if err := s.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	return s
}

// Slot ids, matching README channel list.
const (
	idInputVolts = 0
	idDCPower    = 2
	idUSBPower   = 8
	idDCDuty     = 14
	idDCDewAuto  = 20
	idLED        = 26
	idDCCurrent  = 27
	idTempOffset = 45
	idPWMHz      = 50
	idLEDAuto    = 51
	idDewMargin  = 54
)

func TestSlotCount(t *testing.T) {
	if n := newTestSwitch(t, newFake()).MaxSwitch(); n != 64 {
		t.Errorf("MaxSwitch %d, want 64", n)
	}
}

func TestNames(t *testing.T) {
	f := newFake()
	s := newTestSwitch(t, f)
	if name, _ := s.GetSwitchName(idDCPower); name != "DC 1" {
		t.Errorf("unnamed DC 1 is %q", name)
	}
	if err := s.SetSwitchName(idDCPower, "Camera"); err != nil || f.labels.DC[0] != "Camera" {
		t.Fatalf("rename: %v %q", err, f.labels.DC[0])
	}
	s.b.snap = nil // next read fetches new labels
	for id, want := range map[int]string{idDCPower: "DC 1 (Camera)", idDCDuty: "DC 1 Duty (Camera)",
		idDCCurrent: "DC 1 Current (Camera)", idDCDewAuto: "DC 1 Dew Auto (Camera)"} {
		if name, _ := s.GetSwitchName(id); name != want {
			t.Errorf("switch %d is %q, want %q", id, name, want)
		}
	}
	if d, _ := s.GetSwitchDescription(idDCPower); !strings.HasPrefix(d, "DC 1:") {
		t.Errorf("description %q lost the port", d)
	}
	// Client writing back full name it read keeps label.
	if err := s.SetSwitchName(idDCPower, "DC 1 (Camera)"); err != nil || f.labels.DC[0] != "Camera" {
		t.Errorf("round trip: %v %q", err, f.labels.DC[0])
	}
	if err := s.SetSwitchName(idDCPower, "DC 1"); err != nil || f.labels.DC[0] != "" {
		t.Errorf("fixed name should clear the label: %v %q", err, f.labels.DC[0])
	}
	if err := s.SetSwitchName(idLED, "x"); !errors.Is(err, alpacadev.ErrNotImplemented) {
		t.Errorf("rename LED Brightness: %v", err)
	}
	var ae *alpacadev.AlpacaError
	if err := s.SetSwitchName(idDCPower, "a name that is too long"); !errors.As(err, &ae) || ae.Number != alpacadev.ErrNumInvalidValue {
		t.Errorf("long name: %v", err)
	}
}

func TestSlots(t *testing.T) {
	f := newFake()
	s := newTestSwitch(t, f)

	if err := s.SetSwitch(idDCPower, true); err != nil || !f.st.DC[0].Enabled {
		t.Errorf("DC 1 power: %v %v", err, f.st.DC[0].Enabled)
	}
	if err := s.SetSwitchValue(idDCDuty, 40.4); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.GetSwitchValue(idDCDuty); v != 40 {
		t.Errorf("DC 1 duty %v, want 40", v)
	}
	if a, _ := s.GetSwitchValue(idDCCurrent); a != 0.4 {
		t.Errorf("DC 1 current %v, want 0.4", a)
	}
	if err := s.SetSwitch(idDCDewAuto+2, true); err != nil || !f.dew.Auto[2] {
		t.Errorf("DC 3 dew auto: %v %v", err, f.dew.Auto[2])
	}
	if err := s.SetSwitchValue(idTempOffset, -2.34); err != nil || f.dew.TempOffsetC != -2.3 {
		t.Errorf("temperature offset: %v %v", err, f.dew.TempOffsetC)
	}
	if v, _ := s.GetSwitchValue(idDewMargin); v != 4 {
		t.Errorf("dew margin %v, want 4", v)
	}
	if err := s.SetSwitch(idUSBPower+1, false); err != nil || f.st.USB[1].Enabled {
		t.Errorf("USB 2 off: %v", err)
	}
	if err := s.SetSwitch(idLEDAuto, true); err != nil || !f.aux.LEDAuto {
		t.Errorf("LED auto: %v", err)
	}
	if ok, _ := s.CanWrite(idInputVolts); ok {
		t.Error("Input Voltage writable")
	}
	if err := s.SetSwitchValue(idInputVolts, 1); !errors.Is(err, alpacadev.ErrNotImplemented) {
		t.Errorf("write to a reading: %v", err)
	}
	var ae *alpacadev.AlpacaError
	if err := s.SetSwitchValue(idPWMHz, 50); !errors.As(err, &ae) || ae.Number != alpacadev.ErrNumInvalidValue {
		t.Errorf("PWM 50 Hz: %v, want InvalidValue", err)
	}
	if err := s.SetSwitch(56, true); err != nil || !f.st.DC[0].PWM {
		t.Errorf("DC 1 PWM: %v", err)
	}
	if name, _ := s.GetSwitchName(56); name != "DC 1 PWM" {
		t.Errorf("PWM switch name %q", name)
	}
	r, err := s.Action("SwitchLayout", "")
	var lay switchLayout
	if err != nil || json.Unmarshal([]byte(r), &lay) != nil || lay.Kind != "scopehub" || len(lay.Tables) != 2 {
		t.Fatalf("SwitchLayout: %v %.80s", err, r)
	}
	for _, tb := range lay.Tables {
		for _, c := range tb.Columns {
			if last := c.First + len(tb.RowNames) - 1; last >= s.MaxSwitch() {
				t.Errorf("%s %s runs past the switch list to %d", tb.Title, c.Title, last)
			}
		}
	}
	if r, err := s.Action("SaveSettings", ""); err != nil || r != "ok" || f.saved != 1 {
		t.Errorf("SaveSettings: %q %v", r, err)
	}
}

func TestReopenAfterFailure(t *testing.T) {
	f := newFake()
	s := newTestSwitch(t, f)
	f.broken = true
	if _, err := s.GetSwitchValue(idInputVolts); err == nil {
		t.Fatal("expected an error from a broken link")
	}
	if !f.closed {
		t.Error("failed handle not closed")
	}
	f.broken = false
	if v, err := s.GetSwitchValue(idInputVolts); err != nil || v != 13.2 {
		t.Errorf("after reopen: %v %v", v, err)
	}
}

func TestDisconnectKeepsOutputs(t *testing.T) {
	f := newFake()
	s := newTestSwitch(t, f)
	s.SetSwitch(idDCPower, true)
	s.Disconnect(context.Background())
	if !f.st.DC[0].Enabled {
		t.Error("disconnect switched DC 1 off")
	}
	if _, err := s.GetSwitchValue(idDCPower); !errors.Is(err, alpacadev.ErrNotConnected) {
		t.Errorf("read after disconnect: %v", err)
	}
}

func TestBarometerReadings(t *testing.T) {
	f := newFake()
	// Barometer readings do not depend on SHT45 or dew controller.
	f.dew.Valid = false
	f.st.Env = scopehub.Env{BaroValid: true, PressureHPa: 1013.25, BaroTempC: 23.75}
	s := newTestSwitch(t, f)
	for _, reading := range []struct {
		id    int
		name  string
		value float64
	}{
		{62, "Pressure", 1013.25}, {63, "Barometer Temperature", 23.75},
	} {
		if name, err := s.GetSwitchName(reading.id); err != nil || name != reading.name {
			t.Fatal(reading.id, name, err)
		}
		if value, err := s.GetSwitchValue(reading.id); err != nil || value != reading.value {
			t.Fatal(reading.name, value, err)
		}
		if canWrite, err := s.CanWrite(reading.id); err != nil || canWrite {
			t.Fatal("sensor writable", reading.name, err)
		}
		if err := s.SetSwitchValue(reading.id, reading.value); !errors.Is(err, alpacadev.ErrNotImplemented) {
			t.Fatal("sensor accepted write", err)
		}
	}
	f.st.Env.BaroValid = false
	s.b.snap = nil
	for _, id := range []int{62, 63} {
		var ae *alpacadev.AlpacaError
		if _, err := s.GetSwitchValue(id); !errors.As(err, &ae) || ae.Number != alpacadev.ErrNumValueNotSet {
			t.Fatal("invalid barometer returned a reading", id, err)
		}
	}
}
