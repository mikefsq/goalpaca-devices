package driver

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	alpacadev "github.com/mikefsq/goalpaca/server"
)

// A fake HID panel. Tests never enumerate or open real hardware.
type fakeHID struct {
	status, info  []byte
	last          []byte
	sent          [][]byte
	code          uint16
	failSet       bool
	neverFinishes bool
	closed        bool
}

func newFakeHID() *fakeHID {
	f := &fakeHID{status: make([]byte, hidStatusBytes), info: make([]byte, hidInfoBytes)}
	copy(f.info, "1.14.2-dsigma")
	f.info[hidVersionBytes] = 3
	f.info[hidVersionBytes+1] = hidProtocol
	return f
}

func (f *fakeHID) setFeature(id byte, data []byte) error {
	if f.failSet {
		return errors.New("device gone")
	}
	f.last = append([]byte{}, data...)
	f.sent = append(f.sent, f.last)
	return nil
}

func (f *fakeHID) getFeature(id byte, n int) ([]byte, error) {
	switch id {
	case hidReportStatus:
		return f.status, nil
	case hidReportInfo:
		return f.info, nil
	}
	reply := append([]byte{}, f.last...)
	if !f.neverFinishes {
		reply[2] = hidStateDone
		binary.LittleEndian.PutUint16(reply[8:], f.code)
	}
	return reply, nil
}

func (f *fakeHID) close() error { f.closed = true; return nil }

func TestHIDLinkIdentifies(t *testing.T) {
	firmware, product, err := identify(&hidLink{t: newFakeHID()})
	if err != nil {
		t.Fatal(err)
	}
	if firmware != "Board=DeepSkyDad.FP2, Version=1.14.2-dsigma" || product != 3 {
		t.Fatalf("got %q, product %d", firmware, product)
	}
}

func TestHIDLinkStatus(t *testing.T) {
	f := newFakeHID()
	l := &hidLink{t: f}
	f.status[4] = hidCoverMoving
	f.status[8] = 1
	binary.LittleEndian.PutUint16(f.status[10:], 1234)
	f.status[14] = 2
	binary.LittleEndian.PutUint16(f.status[18:], uint16(2452))
	for command, want := range map[string]string{"GMOV": "1", "GOPS": "2", "GLON": "1", "GLBR": "1234", "GHTM": "2", "GHTT": "24.52"} {
		if got, err := exchange(l, command); err != nil || got != want {
			t.Fatalf("%s: got %q, %v", command, got, err)
		}
	}
	missing := int16(hidNoTemperature)
	binary.LittleEndian.PutUint16(f.status[18:], uint16(missing))
	if got, _ := exchange(l, "GHTT"); got != hidUnavailableTemperature {
		t.Fatalf("missing temperature: %q", got)
	}
}

func TestHIDLinkCommands(t *testing.T) {
	f := newFakeHID()
	l := &hidLink{t: f}
	for _, command := range []string{"SLBR0500", "SLON1", "SHTM2", "STRG270", "SMOV", "STOP"} {
		if err := acknowledge(l, command); err != nil {
			t.Fatalf("%s: %v", command, err)
		}
	}
	want := []struct {
		opcode byte
		value  uint32
	}{{hidOpSetBrightness, 500}, {hidOpSetLight, 1}, {hidOpSetHeaterMode, 2}, {hidOpMove, 270}, {hidOpStop, 0}}
	if len(f.sent) != len(want) {
		t.Fatalf("sent %d reports, want %d", len(f.sent), len(want))
	}
	for i, w := range want {
		if f.sent[i][0] != w.opcode || binary.LittleEndian.Uint32(f.sent[i][4:]) != w.value {
			t.Fatalf("report %d: %v", i, f.sent[i])
		}
	}
}

func TestHIDLinkErrors(t *testing.T) {
	f := newFakeHID()
	f.code = 303
	err := acknowledge(&hidLink{t: f}, "SMOV")
	if !isRejected(err) || err.Error() != "SMOV rejected: !303" {
		t.Fatalf("firmware error: %v", err)
	}
	if got, _ := exchange(&hidLink{t: newFakeHID()}, "GOBT"); got != hidUnknownCommand {
		t.Fatalf("unknown command: %q", got)
	}
	f = newFakeHID()
	f.failSet = true
	if err := acknowledge(&hidLink{t: f}, "SLON1"); err == nil || isRejected(err) {
		t.Fatalf("transport failure must not look like a rejection: %v", err)
	}
	f = newFakeHID()
	f.neverFinishes = true
	if err := acknowledge(&hidLink{t: f}, "SLON1"); err == nil || isRejected(err) {
		t.Fatalf("timeout must not look like a rejection: %v", err)
	}
}

func TestPanelRunsOverHID(t *testing.T) {
	f := newFakeHID()
	p := NewPanel(Config{})
	p.open = func() (link, error) { return &hidLink{t: f}, nil }
	if err := p.acquireLocked(); err != nil {
		t.Fatal(err)
	}
	p.MarkConnected()
	status, err := p.ReadStatus()
	if err != nil {
		t.Fatal(err)
	}
	if status.Model != "OFP2" || status.Cover != "closed" || status.Firmware == "" {
		t.Fatalf("status %+v", status)
	}
	if err := p.OpenCover(); err != nil {
		t.Fatal(err)
	}
	if f.last[0] != hidOpMove || binary.LittleEndian.Uint32(f.last[4:]) != 0 {
		t.Fatalf("open sent %v", f.last)
	}
}

func hidPanel(t *testing.T, product byte) (*Panel, *fakeHID) {
	t.Helper()
	f := newFakeHID()
	f.info[hidVersionBytes] = product
	f.status[4] = 0
	p := NewPanel(Config{})
	p.open = func() (link, error) { return &hidLink{t: f}, nil }
	if err := p.acquireLocked(); err != nil {
		t.Fatal(err)
	}
	p.MarkConnected()
	return p, f
}

func TestHIDActionsListedOnlyOverHID(t *testing.T) {
	p, _ := hidPanel(t, 3)
	want := []string{"HeaterTemperature", "HeaterMode", "Status", "ClearFault", "OutputMode", "PWMFrequency",
		"DeltaSigmaDivider", "HeaterPower", "CoverPosition"}
	if got := p.SupportedActions(); !reflect.DeepEqual(got, want) {
		t.Fatal(got)
	}
	p, _ = hidPanel(t, 2) // FT2 has no cover or heater.
	want = []string{"Status", "ClearFault", "OutputMode", "PWMFrequency", "DeltaSigmaDivider"}
	if got := p.SupportedActions(); !reflect.DeepEqual(got, want) {
		t.Fatal(got)
	}
	p, _ = hidPanel(t, 5) // SFP2 has a cover, but no light or heater.
	want = []string{"Status", "CoverPosition"}
	if got := p.SupportedActions(); !reflect.DeepEqual(got, want) {
		t.Fatal(got)
	}
	for _, name := range []string{"ClearFault", "OutputMode", "PWMFrequency", "DeltaSigmaDivider", "HeaterPower"} {
		if _, err := p.Action(name, ""); err != alpacadev.ErrNotImplemented {
			t.Fatal(name, err)
		}
	}
	serial, _ := setup(t, 3)
	for _, name := range []string{"Status", "ClearFault", "OutputMode", "PWMFrequency", "DeltaSigmaDivider", "HeaterPower", "CoverPosition"} {
		for _, listed := range serial.SupportedActions() {
			if listed == name {
				t.Fatal("listed over serial:", name)
			}
		}
		for _, args := range []string{"", "1"} {
			if _, err := serial.Action(name, args); err != alpacadev.ErrActionNotImplemented {
				t.Fatal(name, err)
			}
		}
	}
}

func TestHIDActionsEmptyParametersRead(t *testing.T) {
	p, f := hidPanel(t, 3)
	binary.LittleEndian.PutUint16(f.status[0:], 90)
	binary.LittleEndian.PutUint16(f.status[16:], 200)
	f.status[20] = 6
	binary.LittleEndian.PutUint16(f.status[24:], 7)
	binary.LittleEndian.PutUint32(f.status[26:], 16000)
	before := len(f.sent)
	for _, test := range []struct{ name, args, want string }{
		{"OutputMode", "", "delta-sigma"},
		{"PWMFrequency", "", "16000"},
		{"DeltaSigmaDivider", "  ", "7"},
		{"heaterpower", "", "200"},
		{"CoverPosition", "", "90"},
	} {
		if got, err := p.Action(test.name, test.args); err != nil || got != test.want {
			t.Fatal(test.name, got, err)
		}
	}
	f.status[20] = 0
	if got, err := p.Action("OutputMode", ""); err != nil || got != "pwm" {
		t.Fatal(got, err)
	}
	if len(f.sent) != before {
		t.Fatal("a read sent a command")
	}
}

func TestHIDActionsValuesWrite(t *testing.T) {
	p, f := hidPanel(t, 3)
	for _, test := range []struct {
		name, args, want string
		opcode           byte
		value            uint32
	}{
		{"CLEARFAULT", "", "OK", hidOpClearFault, 0},
		{"OutputMode", "pwm", "pwm", hidOpSetMode, 0},
		{"OutputMode", "delta-sigma", "delta-sigma", hidOpSetMode, 6},
		{"PWMFrequency", "20000", "20000", hidOpSetPWMHz, 20000},
		{"DeltaSigmaDivider", " 8 ", "8", hidOpSetDivider, 8},
		{"HeaterPower", "200", "200", hidOpSetHeaterPwr, 200},
	} {
		got, err := p.Action(test.name, test.args)
		if err != nil || got != test.want {
			t.Fatal(test.name, got, err)
		}
		if f.last[0] != test.opcode || binary.LittleEndian.Uint32(f.last[4:]) != test.value {
			t.Fatal(test.name, f.last)
		}
	}
	if got, err := p.Action("CoverPosition", "90"); err != nil || got != "90" {
		t.Fatal(got, err)
	}
	if f.last[0] != hidOpMove || binary.LittleEndian.Uint32(f.last[4:]) != 90 {
		t.Fatal(f.last)
	}
	if p.CoverState() != alpacadev.CoverMoving {
		t.Fatal("move not tracked")
	}
}

func TestHIDActionsRejectBadValues(t *testing.T) {
	p, f := hidPanel(t, 3)
	before := len(f.sent)
	for _, test := range []struct{ name, args string }{
		{"Status", "1"}, {"ClearFault", "1"}, {"OutputMode", "6"},
		{"PWMFrequency", "999"}, {"PWMFrequency", "100001"}, {"PWMFrequency", "fast"},
		{"DeltaSigmaDivider", "3"}, {"DeltaSigmaDivider", "1281"},
		{"HeaterPower", "-1"}, {"HeaterPower", "257"},
		{"CoverPosition", "-1"}, {"CoverPosition", "271"}, {"CoverPosition", "1][SMOV"},
	} {
		if _, err := p.Action(test.name, test.args); err != alpacadev.ErrInvalidValue {
			t.Fatal(test.name, test.args, err)
		}
	}
	if len(f.sent) != before {
		t.Fatal("bad value reached the panel")
	}
	f.code = 3
	if _, err := p.Action("ClearFault", ""); err == nil || !p.Connected() {
		t.Fatal("firmware rejection must report an error and keep the link:", err)
	}
	f.failSet = true
	if _, err := p.Action("ClearFault", ""); err == nil || p.Connected() {
		t.Fatal("transport failure must drop the link:", err)
	}
}

func TestHIDStatusAction(t *testing.T) {
	p, f := hidPanel(t, 3)
	binary.LittleEndian.PutUint16(f.status[0:], 270)
	f.status[5], f.status[20], f.status[21] = 1, 6, 1
	binary.LittleEndian.PutUint16(f.status[12:], 179)
	binary.LittleEndian.PutUint16(f.status[18:], 2452)
	binary.LittleEndian.PutUint16(f.status[24:], 7)
	binary.LittleEndian.PutUint32(f.status[26:], 16000)
	binary.LittleEndian.PutUint32(f.status[30:], 16000000)
	binary.LittleEndian.PutUint32(f.status[34:], 3)
	binary.LittleEndian.PutUint32(f.status[38:], 2)
	text, err := p.Action("Status", "")
	if err != nil {
		t.Fatal(err)
	}
	var s hidStatus
	if err := json.Unmarshal([]byte(text), &s); err != nil {
		t.Fatal(err)
	}
	if s.Position != 270 || !s.Calibrated || s.OutputMode != "delta-sigma" || !s.OutputOK || s.Divider != 7 ||
		s.PWMRequestedHz != 16000 || s.PWMActualHz != 16000 || s.Underruns != 3 || s.TXStalls != 2 ||
		s.MinutesToAutoOff == nil || *s.MinutesToAutoOff != 179 || s.TemperatureC == nil || *s.TemperatureC != 24.52 {
		t.Fatalf("status %+v", s)
	}
	binary.LittleEndian.PutUint16(f.status[12:], hidNoTimeout)
	missing := int16(hidNoTemperature)
	binary.LittleEndian.PutUint16(f.status[18:], uint16(missing))
	text, _ = p.Action("Status", "")
	s = hidStatus{}
	json.Unmarshal([]byte(text), &s)
	if s.MinutesToAutoOff != nil || s.TemperatureC != nil {
		t.Fatalf("missing values must be null: %s", text)
	}
}

func TestCandidateOnEveryPlatform(t *testing.T) {
	for _, test := range []struct {
		port portInfo
		want bool
	}{
		{portInfo{IsUSB: true, VID: "2E8A", PID: "000A", Product: "Deep Sky Dad FP2"}, true},
		{portInfo{IsUSB: true, VID: "2e8a", PID: "000a", Product: "USB Serial Device (COM5)"}, true},
		{portInfo{IsUSB: true, VID: "2E8A", PID: "000A"}, true},
		{portInfo{IsUSB: true, VID: "2E8A", PID: "000A", Product: "Pico"}, false},
		{portInfo{IsUSB: true, VID: "0403", PID: "6001", Product: "USB Serial Port (COM6)"}, false},
		{portInfo{VID: "2E8A", PID: "000A"}, false},
	} {
		if got := candidate(&test.port); got != test.want {
			t.Errorf("%+v: got %v", test.port, got)
		}
	}
}
