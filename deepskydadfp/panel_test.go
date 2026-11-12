package driver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mikefsq/goalpaca/registry"
	alpacadev "github.com/mikefsq/goalpaca/server"
)

// A framed, stateful FP2 emulator. Tests never enumerate or open real hardware.
type fakePanel struct {
	reply                          bytes.Buffer
	commands                       []string
	override                       map[string]string
	product, position, brightness  int
	on, moving, closed, shortWrite bool
}

func (f *fakePanel) Write(b []byte) (int, error) {
	command := strings.TrimSuffix(strings.TrimPrefix(string(b), "["), "]")
	f.commands = append(f.commands, command)
	if f.shortWrite {
		return len(b) - 1, nil
	}
	reply, ok := f.override[command]
	if !ok {
		switch {
		case command == "GFRM":
			reply = "DeepSkyDad.FP2 1.0.14"
		case command == "GPRD":
			reply = strconv.Itoa(f.product)
		case command == "GMOV":
			reply = "0"
			if f.moving {
				reply = "1"
			}
		case command == "GOPS":
			reply = strconv.Itoa(f.position)
		case command == "GLON":
			reply = "0"
			if f.on {
				reply = "1"
			}
		case command == "GLBR":
			reply = strconv.Itoa(f.brightness)
		case strings.HasPrefix(command, "SLBR"):
			f.brightness, _ = strconv.Atoi(strings.TrimPrefix(command, "SLBR"))
			reply = "OK"
		case command == "SLON0":
			f.on = false
			reply = "OK"
		case command == "SLON1":
			f.on = true
			reply = "OK"
		case strings.HasPrefix(command, "STRG"):
			reply = "OK"
		case command == "SMOV":
			f.moving = true
			reply = "OK"
		case command == "STOP":
			f.moving = false
			f.position = 3
			reply = "OK"
		default:
			return 0, errors.New("unexpected command " + command)
		}
	}
	f.reply.WriteString("(" + reply + ")")
	return len(b), nil
}
func (f *fakePanel) Read(b []byte) (int, error) { return f.reply.Read(b) }
func (f *fakePanel) Close() error               { f.closed = true; return nil }
func setup(t *testing.T, product int) (*Panel, *fakePanel) {
	t.Helper()
	f := &fakePanel{product: product, override: map[string]string{}}
	p := NewPanel(Config{Serial: "test"})
	p.open = func() (link, error) { return f, nil }
	if err := p.acquireLocked(); err != nil {
		t.Fatal(err)
	}
	if err := p.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	return p, f
}
func TestConnectOnlyReads(t *testing.T) {
	p, f := setup(t, 1)
	for _, cmd := range f.commands {
		if !strings.HasPrefix(cmd, "G") {
			t.Fatalf("connection changed hardware: %v", f.commands)
		}
	}
	if !p.Connected() || p.CoverState() != alpacadev.CoverClosed || p.CalibratorState() != alpacadev.CalibratorOff {
		t.Fatal("bad initial snapshot")
	}
	if err := p.CalibratorOn(1234); err != nil {
		t.Fatal(err)
	}
	if !p.CalibratorChanging() || p.CalibratorState() != alpacadev.CalibratorNotReady {
		t.Fatal("unconfirmed light reported ready")
	}
	if err := p.refreshLocked(); err != nil {
		t.Fatal(err)
	}
	if p.Brightness() != 1234 || p.CalibratorState() != alpacadev.CalibratorReady {
		t.Fatal("calibrator state")
	}
	if err := p.Close(context.Background()); err != nil || !f.closed {
		t.Fatal("close left handle open")
	}
}
func TestMovementAndInterrupts(t *testing.T) {
	p, f := setup(t, 1)
	if err := p.CloseCover(); err != nil {
		t.Fatal(err)
	}
	if p.CoverState() != alpacadev.CoverMoving || !p.Busy() {
		t.Fatal("motion not asynchronous")
	}
	before := len(f.commands)
	if err := p.OpenCover(); err == nil || len(f.commands) != before {
		t.Fatal("replaced active motion")
	}
	if err := p.CalibratorOff(); err != nil {
		t.Fatal(err)
	}
	if err := p.HaltCover(); err != nil {
		t.Fatal(err)
	}
	if p.CoverState() != alpacadev.CoverUnknown || p.CoverMoving() {
		t.Fatal("halt invented an endpoint")
	}
	if err := p.OpenCover(); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(f.commands[len(f.commands)-2:], ","); got != "STRG0,SMOV" {
		t.Fatal(got)
	}
	f.moving, f.position = false, 1
	if err := p.refreshLocked(); err != nil || p.CoverState() != alpacadev.CoverOpen {
		t.Fatal("completion not read from hardware", err)
	}
}
func TestCapabilitiesAndValidation(t *testing.T) {
	for _, product := range []int{1, 2, 3, 4, 5} {
		t.Run(strconv.Itoa(product), func(t *testing.T) {
			p, f := setup(t, product)
			for _, n := range []int{-1, 4097} {
				if err := p.CalibratorOn(n); err == nil {
					t.Fatal("invalid brightness")
				}
			}
			before := len(f.commands)
			if product == 2 {
				if p.CoverState() != alpacadev.CoverNotPresent || p.OpenCover() != alpacadev.ErrNotImplemented || p.HaltCover() != alpacadev.ErrNotImplemented {
					t.Fatal("nonmotorized panel exposed cover")
				}
			}
			if product == 5 {
				if p.CalibratorState() != alpacadev.CalibratorNotPresent || p.MaxBrightness() != 0 || p.CalibratorOn(1) != alpacadev.ErrNotImplemented || p.CalibratorOff() != alpacadev.ErrNotImplemented {
					t.Fatal("cover-only device exposed light")
				}
			}
			if len(f.commands) != before {
				t.Fatal("invalid/unsupported operation wrote to panel")
			}
		})
	}
}
func TestFailuresInvalidateWithoutReplaying(t *testing.T) {
	for _, short := range []bool{false, true} {
		t.Run(strconv.FormatBool(short), func(t *testing.T) {
			p, f := setup(t, 1)
			f.shortWrite = short
			f.override["SMOV"] = "ERR"
			if err := p.OpenCover(); err == nil {
				t.Fatal("command failure hidden")
			}
			count := len(f.commands)
			if p.Connected() {
				t.Fatal("failed sequence left apparently healthy state")
			}
			if err := p.OpenCover(); err == nil || len(f.commands) != count {
				t.Fatal("failed motion was replayed")
			}
			if short && !f.closed {
				t.Fatal("ambiguous transport kept open")
			}
		})
	}
}
func TestMalformedPollingAndStaleData(t *testing.T) {
	for _, cmd := range []string{"GMOV", "GOPS", "GLON", "GLBR"} {
		p, f := setup(t, 1)
		f.override[cmd] = "invalid"
		if err := p.refreshLocked(); err == nil {
			t.Fatal("malformed poll accepted", cmd)
		}
	}
	p, _ := setup(t, 1)
	p.now = func() time.Time { return p.state.at.Add(staleAfter + time.Second) }
	if p.Connected() || p.CoverState() != alpacadev.CoverUnknown || p.CalibratorState() != alpacadev.CalibratorUnknown {
		t.Fatal("stale readings looked current")
	}
}
func TestIdentityRejectsOtherDevices(t *testing.T) {
	f := &fakePanel{product: 1, override: map[string]string{"GFRM": "unrelated instrument"}}
	if _, _, err := identify(f); err == nil {
		t.Fatal("accepted wrong serial device")
	}
	f = &fakePanel{product: 99, override: map[string]string{}}
	if _, _, err := identify(f); err == nil {
		t.Fatal("unknown product silently supported")
	}
	for _, product := range []string{"Deep Sky Dad AF3", "Unrelated FP2"} {
		if candidate(&portInfo{IsUSB: true, Product: product}) {
			t.Fatal("discovery included unrelated device")
		}
	}
	if !candidate(&portInfo{IsUSB: true, Product: "Deep Sky Dad FP2"}) {
		t.Fatal("panel descriptor not matched")
	}
	drv, _ := registry.Lookup("deepskydadfp")
	if err := registry.CheckIdentity(drv); err != nil {
		t.Fatal(err)
	}
}
func TestConcurrentBrightnessTransactionsStayTogether(t *testing.T) {
	p, f := setup(t, 1)
	f.commands = nil
	var wg sync.WaitGroup
	for n := 1; n <= 8; n++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			if err := p.CalibratorOn(n); err != nil {
				t.Error(err)
			}
		}(n)
	}
	wg.Wait()
	for i := 0; i < len(f.commands); i += 2 {
		if !strings.HasPrefix(f.commands[i], "SLBR") || f.commands[i+1] != "SLON1" {
			t.Fatal("interleaved multi-command write", f.commands)
		}
	}
}
func TestAlpacaHTTP(t *testing.T) {
	p, _ := setup(t, 1)
	s := alpacadev.New(alpacadev.Config{Discovery: alpacadev.DiscoveryConfig{Mode: alpacadev.DiscoveryOff}})
	if err := s.Register(alpacadev.CoverCalibratorType, 0, p); err != nil {
		t.Fatal(err)
	}
	request := func(method, member string, form url.Values) (any, int) {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequest(method, "/api/v1/covercalibrator/0/"+member, strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		s.ServeHTTP(w, r)
		var result struct {
			Value       any
			ErrorNumber int
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(w.Code, w.Body.String())
		}
		return result.Value, result.ErrorNumber
	}
	if _, err := request(http.MethodPut, "calibratoron", url.Values{"Brightness": {"256"}}); err != 0 {
		t.Fatal(err)
	}
	if err := p.refreshLocked(); err != nil {
		t.Fatal(err)
	}
	if value, err := request(http.MethodGet, "brightness", nil); err != 0 || value != float64(256) {
		t.Fatal(value, err)
	}
	if _, err := request(http.MethodPut, "calibratoron", url.Values{"Brightness": {"4097"}}); err != alpacadev.ErrNumInvalidValue {
		t.Fatal(err)
	}
	if _, err := request(http.MethodPut, "opencover", nil); err != 0 {
		t.Fatal(err)
	}
	if _, err := request(http.MethodPut, "haltcover", nil); err != 0 {
		t.Fatal("halt was blocked during motion", err)
	}
}

type brokenLink struct {
	read   string
	writes int
	short  bool
}

func (b *brokenLink) Read(p []byte) (int, error) {
	if b.read == "" {
		return 0, io.EOF
	}
	p[0] = b.read[0]
	b.read = b.read[1:]
	return 1, nil
}
func (b *brokenLink) Write(p []byte) (int, error) {
	b.writes++
	if b.short {
		return len(p) - 1, nil
	}
	return len(p), nil
}
func (b *brokenLink) Close() error { return nil }
func TestWireFraming(t *testing.T) {
	for _, reply := range []string{"noise\r\n(OK)", "(123)"} {
		b := &brokenLink{read: reply}
		r, err := exchange(b, "GTEST")
		if err != nil || r == "" {
			t.Fatal(r, err)
		}
	}
	for _, reply := range []string{"(partial", "(" + strings.Repeat("x", 256) + ")", "(bad\x00)"} {
		b := &brokenLink{read: reply}
		if _, err := exchange(b, "GTEST"); err == nil || b.writes != 1 {
			t.Fatal("malformed response/replayed query", err)
		}
	}
	b := &brokenLink{short: true}
	if _, err := exchange(b, "SMOV"); err != io.ErrShortWrite || b.writes != 1 {
		t.Fatal(err)
	}
}

type silentLink struct{}

func (silentLink) Write(b []byte) (int, error) { return len(b), nil }
func (silentLink) Read([]byte) (int, error)    { time.Sleep(50 * time.Millisecond); return 0, nil }
func (silentLink) Close() error                { return nil }
func TestSilentPanelTimesOut(t *testing.T) {
	start := time.Now()
	if _, err := exchange(silentLink{}, "GFRM"); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatal("silent panel did not time out", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("serial timeout exceeded its bound")
	}
}

func TestDirectStatusAndClose(t *testing.T) {
	p, f := setup(t, 2)
	if err := p.CalibratorOn(1234); err != nil {
		t.Fatal(err)
	}
	if err := p.CalibratorOff(); err != nil {
		t.Fatal(err)
	}
	state, err := p.ReadStatus()
	if err != nil || state.Model != "FT2" || state.Cover != "not_present" || state.LightOn || state.Brightness != 1234 || state.Firmware == "" {
		t.Fatal(state, err)
	}
	f.override["GLBR"] = "invalid"
	if _, err := p.ReadStatus(); err == nil || !f.closed {
		t.Fatal("failed direct status kept link open", err)
	}
	if _, err := p.ReadStatus(); err == nil {
		t.Fatal("read from a closed direct connection")
	}
}

// Connected follows held hardware. Disconnect leaves panel held and connected; only losing the
// hardware disconnects it.
func TestConnectedFollowsHardware(t *testing.T) {
	p, _ := setup(t, 1)
	for range 2 {
		if err := p.Disconnect(context.Background()); err != nil || !p.Connected() || p.Connecting() {
			t.Fatal("disconnect", err)
		}
	}
	for range 2 {
		if err := p.Connect(context.Background()); err != nil || !p.Connected() || p.Connecting() {
			t.Fatal("connect", err)
		}
	}
	p.dropLocked()
	if p.Connected() {
		t.Fatal("connected without hardware")
	}
	if err := p.Connect(context.Background()); err != alpacadev.ErrNotConnected {
		t.Fatal("connect without hardware must be NotConnected", err)
	}
}
func TestLightReadbackAndTimeout(t *testing.T) {
	p, f := setup(t, 1)
	f.override["SLON1"] = "OK"
	if err := p.CalibratorOn(100); err != nil {
		t.Fatal(err)
	}
	if err := p.refreshLocked(); err != nil {
		t.Fatal(err)
	}
	if !p.CalibratorChanging() || p.CalibratorState() != alpacadev.CalibratorNotReady || p.Brightness() != 0 {
		t.Fatal("ACK mistaken for readiness")
	}
	now := p.now().Add(lightTimeout + time.Second)
	p.now = func() time.Time { return now }
	if err := p.refreshLocked(); err != nil {
		t.Fatal(err)
	}
	if p.CalibratorChanging() || p.CalibratorState() != alpacadev.CalibratorError {
		t.Fatal("light timeout not reported")
	}
	if err := p.refreshLocked(); err != nil || p.CalibratorState() != alpacadev.CalibratorError {
		t.Fatal("poll erased error", err)
	}
	delete(f.override, "SLON1")
	if err := p.CalibratorOn(0); err != nil {
		t.Fatal(err)
	}
	if err := p.refreshLocked(); err != nil || p.CalibratorState() != alpacadev.CalibratorReady || p.Brightness() != 0 {
		t.Fatal("zero brightness", err)
	}
	if err := p.CalibratorOff(); err != nil || !p.CalibratorChanging() {
		t.Fatal("off initiator", err)
	}
	if err := p.refreshLocked(); err != nil || p.CalibratorChanging() || p.CalibratorState() != alpacadev.CalibratorOff || p.Brightness() != 0 {
		t.Fatal("off readback", err)
	}
}
func TestLightClosesCoverBeforeEnabling(t *testing.T) {
	p, f := setup(t, 1)
	f.position = 1
	if err := p.refreshLocked(); err != nil {
		t.Fatal(err)
	}
	f.commands = nil
	if err := p.CalibratorOn(100); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(f.commands, ","); got != "STRG270,SMOV" {
		t.Fatal(got)
	}
	if !p.CalibratorChanging() || !p.CoverMoving() {
		t.Fatal("missing pending state")
	}
	if err := p.refreshLocked(); err != nil || f.on {
		t.Fatal("illuminated during motion", err)
	}
	f.position, f.moving = 0, false
	if err := p.refreshLocked(); err != nil || !f.on || !p.CalibratorChanging() {
		t.Fatal("did not enable after closure", err)
	}
	if err := p.refreshLocked(); err != nil || p.CalibratorChanging() || p.CalibratorState() != alpacadev.CalibratorReady {
		t.Fatal("readback", err)
	}
}
func TestDeferredLightCancellation(t *testing.T) {
	for _, cancel := range []string{"off", "halt", "disconnect"} {
		t.Run(cancel, func(t *testing.T) {
			p, f := setup(t, 1)
			f.position = 1
			if err := p.refreshLocked(); err != nil {
				t.Fatal(err)
			}
			if err := p.CalibratorOn(100); err != nil {
				t.Fatal(err)
			}
			var err error
			switch cancel {
			case "off":
				err = p.CalibratorOff()
			case "halt":
				err = p.HaltCover()
			case "disconnect":
				err = p.Disconnect(context.Background())
			}
			if err != nil {
				t.Fatal(err)
			}
			f.position, f.moving = 0, false
			if err := p.refreshLocked(); err != nil || f.on {
				t.Fatal("canceled light turned on", err)
			}
		})
	}
}
func TestMotionFailureAndTimeout(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		t.Run(strconv.FormatBool(timeout), func(t *testing.T) {
			p, f := setup(t, 1)
			if err := p.OpenCover(); err != nil {
				t.Fatal(err)
			}
			if timeout {
				now := p.now().Add(motionTimeout + time.Second)
				p.now = func() time.Time { return now }
			} else {
				f.moving = false
				f.position = 3
			}
			if err := p.refreshLocked(); err != nil || p.CoverState() != alpacadev.CoverError || p.CoverMoving() {
				t.Fatal("motion failure", err)
			}
			if err := p.HaltCover(); err != nil || p.CoverState() != alpacadev.CoverUnknown {
				t.Fatal("halt recovery", err)
			}
		})
	}
}

func TestHTTPConnectionCapabilitiesAndDeviceState(t *testing.T) {
	for _, product := range []int{1, 2, 3, 4, 5} {
		t.Run(strconv.Itoa(product), func(t *testing.T) {
			p, _ := setup(t, product)
			s := alpacadev.New(alpacadev.Config{Discovery: alpacadev.DiscoveryConfig{Mode: alpacadev.DiscoveryOff}})
			if err := s.Register(alpacadev.CoverCalibratorType, 0, p); err != nil {
				t.Fatal(err)
			}
			request := func(method, member string, form url.Values) (any, int) {
				t.Helper()
				w := httptest.NewRecorder()
				r := httptest.NewRequest(method, "/api/v1/covercalibrator/0/"+member, strings.NewReader(form.Encode()))
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				s.ServeHTTP(w, r)
				var result struct {
					Value       any
					ErrorNumber int
				}
				if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				return result.Value, result.ErrorNumber
			}
			for _, member := range []string{"brightness", "maxbrightness", "calibratorstate", "coverstate", "calibratorchanging", "covermoving"} {
				_, code := request(http.MethodGet, member, nil)
				expected := 0
				if product == 5 && (member == "brightness" || member == "maxbrightness") {
					expected = alpacadev.ErrNumNotImplemented
				}
				if code != expected {
					t.Fatalf("%s error %d, want %d", member, code, expected)
				}
			}
			value, code := request(http.MethodGet, "devicestate", nil)
			if code != 0 {
				t.Fatal(code)
			}
			entries := value.([]any)
			names := map[string]bool{}
			for _, entry := range entries {
				names[entry.(map[string]any)["Name"].(string)] = true
			}
			for _, name := range []string{"CalibratorState", "CoverState", "CalibratorChanging", "CoverMoving", "TimeStamp"} {
				if !names[name] {
					t.Fatal("missing DeviceState member", name, value)
				}
			}
			if names["Brightness"] != (product != 5) {
				t.Fatal("DeviceState included unavailable brightness", value)
			}
			for _, disconnect := range []string{"disconnect", "connected"} {
				if _, code := request(http.MethodPut, disconnect, url.Values{"Connected": {"false"}}); code != 0 {
					t.Fatal(code)
				}
				if value, code := request(http.MethodGet, "connected", nil); code != 0 || value != false {
					t.Fatal(value, code)
				}
				for _, member := range []string{"brightness", "maxbrightness", "calibratorstate", "coverstate", "calibratorchanging", "covermoving", "devicestate"} {
					if _, code := request(http.MethodGet, member, nil); code != alpacadev.ErrNumNotConnected {
						t.Fatal(member, code)
					}
				}
				if _, code := request(http.MethodPut, "connect", nil); code != 0 {
					t.Fatal(code)
				}
				if value, code := request(http.MethodGet, "connecting", nil); code != 0 || value != false {
					t.Fatal(value, code)
				}
				if value, code := request(http.MethodGet, "connected", nil); code != 0 || value != true {
					t.Fatal(value, code)
				}
			}
		})
	}
}
