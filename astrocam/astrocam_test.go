package driver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mikefsq/astrocam"
	_ "github.com/mikefsq/astrocam/sensors" // registers the PID -> sensor profile table
	alpacadev "github.com/mikefsq/goalpaca/server"
)

// These e2e tests run the WHOLE stack — Alpaca HTTP → PureASICamera → astrocam.Camera →
// StubTransport — with no hardware, the same discipline as asiefw_test.go's fakeWheel.
// The driver's openDev/aliveFn seams are pointed at a stub-backed camera.

const (
	pid6200 = 0x620A // ASI6200MC Pro — IMX455, cooled, color
	pid290  = 0x290F // ASI290MM Mini — IMX290, mono, ST4
	pid174  = 0x1749 // ASI174MM Mini — IMX174, mono (generic snap path)

	errNotConnected = 0x407 // 1031
)

// stubDev is a swappable fake "device": each open() hands the driver a fresh astrocam.Camera
// over a StubTransport. present models plug/unplug for the liveness + re-acquire tests.
type stubDev struct {
	pid     uint16
	serial  astrocam.Serial
	mu      sync.Mutex
	present bool
}

func (s *stubDev) setPresent(v bool) { s.mu.Lock(); s.present = v; s.mu.Unlock() }
func (s *stubDev) isPresent() bool   { s.mu.Lock(); defer s.mu.Unlock(); return s.present }

func (s *stubDev) open() (*astrocam.Camera, astrocam.DeviceInfo, error) {
	if !s.isPresent() {
		return nil, astrocam.DeviceInfo{}, errors.New("no device")
	}
	tr := astrocam.NewStubTransport()
	tr.Serial = s.serial
	cam, err := astrocam.Open(tr, astrocam.ZWO.VID, s.pid)
	if err != nil {
		return nil, astrocam.DeviceInfo{}, err
	}
	return cam, astrocam.DeviceInfo{VID: astrocam.ZWO.VID, PID: s.pid, Location: 0x1000, Name: cam.Name()}, nil
}

// newStubStack wires a stub-backed camera into a real Alpaca server behind httptest.
func newStubStack(t *testing.T, sd *stubDev) (base, mgmt string) {
	t.Helper()
	dev := NewPureASICamera(0, "")
	dev.openDev = sd.open
	dev.aliveFn = sd.isPresent

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := dev.Open(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dev.Close(context.Background()) }) // stops the TEC goroutine + stub

	srv := alpacadev.New(alpacadev.Config{
		Discovery:    alpacadev.DiscoveryConfig{Mode: alpacadev.DiscoveryOff},
		ServerName:   "asicam-test",
		Manufacturer: "test",
	})
	if err := srv.Register(alpacadev.CameraType, 0, dev); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(http.HandlerFunc(srv.ServeHTTP))
	t.Cleanup(ts.Close)
	return ts.URL + "/api/v1/camera/0/", ts.URL + "/management/v1/"
}

// --- HTTP helpers (same shape as asiefw_test.go) ---

const txQ = "ClientID=1&ClientTransactionID=1"

type resp struct {
	Value        any
	ErrorNumber  int
	ErrorMessage string
}

func decode(t *testing.T, r *http.Response) resp {
	t.Helper()
	defer r.Body.Close()
	var out resp
	if err := json.NewDecoder(r.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

func get(t *testing.T, base, member string) resp {
	t.Helper()
	r, err := http.Get(base + member + "?" + txQ)
	if err != nil {
		t.Fatalf("GET %s: %v", member, err)
	}
	return decode(t, r)
}

func put(t *testing.T, base, member, form string) resp {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPut, base+member, strings.NewReader(form+"&"+txQ))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT %s: %v", member, err)
	}
	return decode(t, r)
}

func value(t *testing.T, base, member string) any {
	t.Helper()
	r := get(t, base, member)
	if r.ErrorNumber != 0 {
		t.Fatalf("GET %s: ErrorNumber=%d (%s)", member, r.ErrorNumber, r.ErrorMessage)
	}
	return r.Value
}

func waitConnected(t *testing.T, base string, want bool) {
	t.Helper()
	// Generous: an unplug is debounced over ~3 liveness ticks (≈6 s) before teardown.
	deadline := time.Now().Add(12 * time.Second)
	for {
		r := get(t, base, "connected")
		if r.ErrorNumber == 0 && r.Value == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("connected never became %v (last=%v err=%d)", want, r.Value, r.ErrorNumber)
		}
		time.Sleep(30 * time.Millisecond)
	}
}

// --- Tests ---

// TestAlpacaCamera6200 covers metadata, connection, geometry, ranges, and cooling end-to-end
// against a stub-backed cooled color camera.
func TestAlpacaCamera6200(t *testing.T) {
	sd := &stubDev{pid: pid6200, present: true, serial: astrocam.Serial{0x06, 0x11, 0x8f, 0x06, 0x1f, 0x09, 0x09, 0x00}}
	base, mgmt := newStubStack(t, sd)
	waitConnected(t, base, true)

	t.Run("metadata", func(t *testing.T) {
		if v := value(t, base, "name"); v != "ASI6200MC Pro" {
			t.Errorf("name = %v", v)
		}
		if v := value(t, base, "interfaceversion"); v != float64(alpacadev.InterfaceVersionCamera) {
			t.Errorf("interfaceversion = %v, want %d", v, alpacadev.InterfaceVersionCamera)
		}
		if v := value(t, base, "sensortype"); v != float64(alpacadev.SensorRGGB) {
			t.Errorf("sensortype = %v, want %d (RGGB)", v, alpacadev.SensorRGGB)
		}
	})

	t.Run("geometry", func(t *testing.T) {
		if v := value(t, base, "cameraxsize"); v != float64(9576) {
			t.Errorf("cameraxsize = %v, want 9576", v)
		}
		if v := value(t, base, "cameraysize"); v != float64(6388) {
			t.Errorf("cameraysize = %v, want 6388", v)
		}
		if v := value(t, base, "maxadu"); v != float64(65535) {
			t.Errorf("maxadu = %v, want 65535", v)
		}
		if v := value(t, base, "maxbinx"); v != float64(4) {
			t.Errorf("maxbinx = %v, want 4 (IMX455 supports 1/2/3/4×)", v)
		}
	})

	t.Run("ranges", func(t *testing.T) {
		if v := value(t, base, "gainmax"); v != float64(700) {
			t.Errorf("gainmax = %v, want 700", v)
		}
		if v := value(t, base, "exposuremax"); v.(float64) < 1000 {
			t.Errorf("exposuremax = %v, want a large value", v)
		}
		// set gain and read it back
		if r := put(t, base, "gain", "Gain=123"); r.ErrorNumber != 0 {
			t.Fatalf("PUT gain: err %d", r.ErrorNumber)
		}
		if v := value(t, base, "gain"); v != float64(123) {
			t.Errorf("gain after set = %v, want 123", v)
		}
		// out-of-range gain is rejected
		if r := put(t, base, "gain", "Gain=99999"); r.ErrorNumber == 0 {
			t.Errorf("gain=99999 should be rejected")
		}
	})

	t.Run("cooling", func(t *testing.T) {
		if v := value(t, base, "cangetcoolerpower"); v != true {
			t.Errorf("cangetcoolerpower = %v, want true", v)
		}
		if v := value(t, base, "cansetccdtemperature"); v != true {
			t.Errorf("cansetccdtemperature = %v, want true", v)
		}
		// CCDTemperature reads through the stub (returns 0).
		if r := get(t, base, "ccdtemperature"); r.ErrorNumber != 0 {
			t.Errorf("ccdtemperature: err %d (%s)", r.ErrorNumber, r.ErrorMessage)
		}
		// Setpoint round-trip.
		if r := put(t, base, "setccdtemperature", "SetCCDTemperature=-10"); r.ErrorNumber != 0 {
			t.Fatalf("PUT setccdtemperature: err %d (%s)", r.ErrorNumber, r.ErrorMessage)
		}
		if v := value(t, base, "setccdtemperature"); v != float64(-10) {
			t.Errorf("setccdtemperature = %v, want -10", v)
		}
		// Cooler on → power readable → off (off also stops the TEC goroutine).
		if r := put(t, base, "cooleron", "CoolerOn=true"); r.ErrorNumber != 0 {
			t.Fatalf("PUT cooleron=true: err %d", r.ErrorNumber)
		}
		if v := value(t, base, "cooleron"); v != true {
			t.Errorf("cooleron = %v, want true", v)
		}
		if r := get(t, base, "coolerpower"); r.ErrorNumber != 0 {
			t.Errorf("coolerpower: err %d", r.ErrorNumber)
		}
		if r := put(t, base, "cooleron", "CoolerOn=false"); r.ErrorNumber != 0 {
			t.Fatalf("PUT cooleron=false: err %d", r.ErrorNumber)
		}
		if v := value(t, base, "cooleron"); v != false {
			t.Errorf("cooleron after off = %v, want false", v)
		}
	})

	t.Run("uniqueid", func(t *testing.T) {
		r, err := http.Get(mgmt + "configureddevices?" + txQ)
		if err != nil {
			t.Fatal(err)
		}
		arr, ok := decode(t, r).Value.([]any)
		if !ok || len(arr) == 0 {
			t.Fatalf("configureddevices = %v", arr)
		}
		if id := arr[0].(map[string]any)["UniqueID"]; id != "ASI-06118f061f090900" {
			t.Errorf("UniqueID = %v, want ASI-06118f061f090900 (factory serial)", id)
		}
	})

	t.Run("guiding-not-supported", func(t *testing.T) {
		if v := value(t, base, "canpulseguide"); v != false {
			t.Errorf("canpulseguide = %v, want false (6200 has no ST4)", v)
		}
	})
}

// TestAlpacaGuiding290 checks the mono ST4 camera exposes guiding.
func TestAlpacaGuiding290(t *testing.T) {
	sd := &stubDev{pid: pid290, present: true, serial: astrocam.Serial{0x1d, 0x21, 0x04, 0x06, 0x22, 0x09, 0x09, 0x00}}
	base, _ := newStubStack(t, sd)
	waitConnected(t, base, true)

	if v := value(t, base, "sensortype"); v != float64(alpacadev.SensorMonochrome) {
		t.Errorf("sensortype = %v, want monochrome", v)
	}
	if v := value(t, base, "canpulseguide"); v != true {
		t.Errorf("canpulseguide = %v, want true (290 has ST4)", v)
	}
	if v := value(t, base, "cangetcoolerpower"); v != false {
		t.Errorf("cangetcoolerpower = %v, want false (290 not cooled)", v)
	}
	// CCDTemperature on a non-cooled body is NotImplemented.
	if r := get(t, base, "ccdtemperature"); r.ErrorNumber == 0 {
		t.Errorf("ccdtemperature on non-cooled should be an error, got value")
	}
	// A short pulse must be accepted and clear IsPulseGuiding.
	if r := put(t, base, "pulseguide", "Direction=0&Duration=50"); r.ErrorNumber != 0 {
		t.Fatalf("PUT pulseguide: err %d (%s)", r.ErrorNumber, r.ErrorMessage)
	}
	deadline := time.Now().Add(2 * time.Second)
	for value(t, base, "ispulseguiding") == true {
		if time.Now().After(deadline) {
			t.Fatal("ispulseguiding never cleared")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestAlpacaFPSPercent exercises the "fpspercent" Action: advertised, query defaults to 100, a
// valid value round-trips, and out-of-range / non-numeric inputs are rejected.
func TestAlpacaFPSPercent(t *testing.T) {
	sd := &stubDev{pid: pid290, present: true, serial: astrocam.Serial{0x1d, 0x21, 0x04, 0x06, 0x22, 0x09, 0x09, 0x01}}
	base, _ := newStubStack(t, sd)
	waitConnected(t, base, true)

	acts, ok := value(t, base, "supportedactions").([]any)
	if !ok {
		t.Fatalf("supportedactions not a list: %T", value(t, base, "supportedactions"))
	}
	has := false
	for _, a := range acts {
		if s, _ := a.(string); strings.EqualFold(s, "fpspercent") {
			has = true
		}
	}
	if !has {
		t.Fatalf("supportedactions %v missing fpspercent", acts)
	}

	// Query before any set: driver default 100.
	if r := put(t, base, "action", "Action=fpspercent&Parameters="); r.ErrorNumber != 0 || r.Value != "100" {
		t.Fatalf("query fpspercent = %v (err %d), want \"100\"", r.Value, r.ErrorNumber)
	}
	// Set a valid value; it echoes back and the query reflects it.
	if r := put(t, base, "action", "Action=fpspercent&Parameters=50"); r.ErrorNumber != 0 || r.Value != "50" {
		t.Fatalf("set fpspercent=50 = %v (err %d), want \"50\"", r.Value, r.ErrorNumber)
	}
	if r := put(t, base, "action", "Action=fpspercent&Parameters="); r.Value != "50" {
		t.Fatalf("query after set = %v, want \"50\"", r.Value)
	}
	// Out of range and non-numeric are rejected (value unchanged).
	if r := put(t, base, "action", "Action=fpspercent&Parameters=200"); r.ErrorNumber == 0 {
		t.Errorf("fpspercent=200 should error, got %v", r.Value)
	}
	if r := put(t, base, "action", "Action=fpspercent&Parameters=abc"); r.ErrorNumber == 0 {
		t.Errorf("fpspercent=abc should error, got %v", r.Value)
	}
	if r := put(t, base, "action", "Action=fpspercent&Parameters="); r.Value != "50" {
		t.Errorf("query after rejected sets = %v, want \"50\" (unchanged)", r.Value)
	}
}

// TestAlpacaCapture exercises the async exposure data plane end-to-end: StartExposure →
// ImageReady → ImageBytes, against the stub (which serves a synthetic frame).
func TestAlpacaCapture(t *testing.T) {
	sd := &stubDev{pid: pid174, present: true, serial: astrocam.Serial{0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09}}
	base, _ := newStubStack(t, sd)
	waitConnected(t, base, true)

	w := int(value(t, base, "numx").(float64))
	h := int(value(t, base, "numy").(float64))
	if w <= 0 || h <= 0 {
		t.Fatalf("bad frame geometry %dx%d", w, h)
	}

	if r := put(t, base, "startexposure", "Duration=0.05&Light=true"); r.ErrorNumber != 0 {
		t.Fatalf("startexposure: err %d (%s)", r.ErrorNumber, r.ErrorMessage)
	}
	deadline := time.Now().Add(15 * time.Second)
	for value(t, base, "imageready") != true {
		if time.Now().After(deadline) {
			t.Fatal("imageready never became true")
		}
		time.Sleep(50 * time.Millisecond)
	}

	req, _ := http.NewRequest(http.MethodGet, base+"imagearray?"+txQ, nil)
	req.Header.Set("Accept", "application/imagebytes")
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	body, _ := io.ReadAll(r.Body)
	const imageBytesHeader = 44 // ASCOM ImageBytes metadata header
	want := w*h*2 + imageBytesHeader
	if len(body) != want {
		t.Errorf("ImageBytes = %d bytes, want %d (%dx%d RAW16 + %d header)", len(body), want, w, h, imageBytesHeader)
	}
}

// TestAlpacaCaptureSubFrame drives a sub-frame ROI end-to-end: set StartX/StartY/NumX/NumY
// to a crop, expose, and assert ImageBytes is exactly the cropped size (numX·numY·2 + header)
// — proving StartX/NumX → astrocam.SetROI → FrameBytes → ImageBytes all agree. It also checks
// that an out-of-range window is rejected at StartExposure with ASCOM InvalidValue (0x401).
func TestAlpacaCaptureSubFrame(t *testing.T) {
	sd := &stubDev{pid: pid174, present: true, serial: astrocam.Serial{0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18, 0x19}}
	base, _ := newStubStack(t, sd)
	waitConnected(t, base, true)

	const errInvalidValue = 0x401

	// Out-of-range window must be rejected at StartExposure (NumX past the sensor edge).
	full := int(value(t, base, "cameraxsize").(float64))
	if r := put(t, base, "numx", fmt.Sprintf("NumX=%d", full+64)); r.ErrorNumber != 0 {
		t.Fatalf("set numx: err %d", r.ErrorNumber)
	}
	if r := put(t, base, "startexposure", "Duration=0.05&Light=true"); r.ErrorNumber != errInvalidValue {
		t.Errorf("oversized ROI: startexposure ErrorNumber=%d, want %d (InvalidValue)", r.ErrorNumber, errInvalidValue)
	}

	// A valid sub-frame crop.
	const cx, cy, cw, ch = 64, 32, 640, 480
	for member, v := range map[string]string{
		"startx": fmt.Sprintf("StartX=%d", cx), "starty": fmt.Sprintf("StartY=%d", cy),
		"numx": fmt.Sprintf("NumX=%d", cw), "numy": fmt.Sprintf("NumY=%d", ch),
	} {
		if r := put(t, base, member, v); r.ErrorNumber != 0 {
			t.Fatalf("set %s: err %d (%s)", member, r.ErrorNumber, r.ErrorMessage)
		}
	}
	if r := put(t, base, "startexposure", "Duration=0.05&Light=true"); r.ErrorNumber != 0 {
		t.Fatalf("startexposure: err %d (%s)", r.ErrorNumber, r.ErrorMessage)
	}
	deadline := time.Now().Add(15 * time.Second)
	for value(t, base, "imageready") != true {
		if time.Now().After(deadline) {
			t.Fatal("imageready never became true")
		}
		time.Sleep(50 * time.Millisecond)
	}

	req, _ := http.NewRequest(http.MethodGet, base+"imagearray?"+txQ, nil)
	req.Header.Set("Accept", "application/imagebytes")
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	body, _ := io.ReadAll(r.Body)
	const imageBytesHeader = 44
	want := cw*ch*2 + imageBytesHeader
	if len(body) != want {
		t.Errorf("sub-frame ImageBytes = %d bytes, want %d (%dx%d RAW16 + %d header)", len(body), want, cw, ch, imageBytesHeader)
	}
}

// TestAlpacaBinning drives binning end-to-end on the 6200 (IMX455, the fully-decoded binning
// path): MaxBinX reflects the sensor caps, SetBinX rescopes the subframe to binned pixels, an
// unsupported factor is rejected, and a small binned crop captures at the binned byte size.
func TestAlpacaBinning(t *testing.T) {
	sd := &stubDev{pid: pid6200, present: true, serial: astrocam.Serial{0x06, 0x11, 0x8f, 0x06, 0x1f, 0x09, 0x09, 0x00}}
	base, _ := newStubStack(t, sd)
	waitConnected(t, base, true)

	const errInvalidValue = 0x401
	if v := value(t, base, "maxbinx").(float64); v != 4 {
		t.Errorf("maxbinx = %v, want 4", v)
	}

	// bin 5 is past the sensor's {1,2,3,4} — rejected.
	if r := put(t, base, "binx", "BinX=5"); r.ErrorNumber != errInvalidValue {
		t.Errorf("binx=5: ErrorNumber=%d, want %d (InvalidValue)", r.ErrorNumber, errInvalidValue)
	}

	// bin 2: accepted, subframe resets to the binned full frame (9576/2 × 6388/2).
	if r := put(t, base, "binx", "BinX=2"); r.ErrorNumber != 0 {
		t.Fatalf("binx=2: err %d (%s)", r.ErrorNumber, r.ErrorMessage)
	}
	if v := value(t, base, "binx").(float64); v != 2 {
		t.Errorf("binx = %v, want 2", v)
	}
	if v := value(t, base, "numx").(float64); v != 4788 {
		t.Errorf("numx after bin 2 = %v, want 4788 (9576/2)", v)
	}
	if v := value(t, base, "numy").(float64); v != 3194 {
		t.Errorf("numy after bin 2 = %v, want 3194 (6388/2)", v)
	}

	// A small binned crop captures at the binned byte size (numX·numY·2 + header).
	const cw, ch = 256, 256
	for member, v := range map[string]string{
		"numx": fmt.Sprintf("NumX=%d", cw), "numy": fmt.Sprintf("NumY=%d", ch),
	} {
		if r := put(t, base, member, v); r.ErrorNumber != 0 {
			t.Fatalf("set %s: err %d", member, r.ErrorNumber)
		}
	}
	if r := put(t, base, "startexposure", "Duration=0.05&Light=true"); r.ErrorNumber != 0 {
		t.Fatalf("startexposure: err %d (%s)", r.ErrorNumber, r.ErrorMessage)
	}
	deadline := time.Now().Add(20 * time.Second)
	for value(t, base, "imageready") != true {
		if time.Now().After(deadline) {
			t.Fatal("imageready never became true")
		}
		time.Sleep(50 * time.Millisecond)
	}
	req, _ := http.NewRequest(http.MethodGet, base+"imagearray?"+txQ, nil)
	req.Header.Set("Accept", "application/imagebytes")
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	body, _ := io.ReadAll(r.Body)
	if want := cw*ch*2 + 44; len(body) != want {
		t.Errorf("binned ImageBytes = %d, want %d (%dx%d RAW16 + 44)", len(body), want, cw, ch)
	}
}

// TestAlpacaReadoutRAW8 drives the RAW8 readout mode end-to-end: select readoutmode 1 (RAW8),
// expose, and assert ImageBytes is the 1-byte/pixel size (numX·numY·1 + header) and maxadu = 255.
func TestAlpacaReadoutRAW8(t *testing.T) {
	sd := &stubDev{pid: pid174, present: true, serial: astrocam.Serial{0x22, 0x23, 0x24, 0x25, 0x26, 0x27, 0x28, 0x29}}
	base, _ := newStubStack(t, sd)
	waitConnected(t, base, true)

	if r := put(t, base, "readoutmode", "ReadoutMode=1"); r.ErrorNumber != 0 { // RAW8
		t.Fatalf("set readoutmode 1: err %d (%s)", r.ErrorNumber, r.ErrorMessage)
	}
	if v := value(t, base, "maxadu").(float64); v != 255 {
		t.Errorf("RAW8 maxadu = %v, want 255", v)
	}
	const cw, ch = 256, 256
	for member, v := range map[string]string{"numx": "NumX=256", "numy": "NumY=256"} {
		if r := put(t, base, member, v); r.ErrorNumber != 0 {
			t.Fatalf("set %s: err %d", member, r.ErrorNumber)
		}
	}
	if r := put(t, base, "startexposure", "Duration=0.05&Light=true"); r.ErrorNumber != 0 {
		t.Fatalf("startexposure: err %d (%s)", r.ErrorNumber, r.ErrorMessage)
	}
	deadline := time.Now().Add(15 * time.Second)
	for value(t, base, "imageready") != true {
		if time.Now().After(deadline) {
			t.Fatal("imageready never became true")
		}
		time.Sleep(50 * time.Millisecond)
	}
	req, _ := http.NewRequest(http.MethodGet, base+"imagearray?"+txQ, nil)
	req.Header.Set("Accept", "application/imagebytes")
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	body, _ := io.ReadAll(r.Body)
	if want := cw*ch*1 + 44; len(body) != want {
		t.Errorf("RAW8 ImageBytes = %d, want %d (%dx%d RAW8 + 44)", len(body), want, cw, ch)
	}
}

// TestAlpacaNotConnected: with no device, operational members return NotConnected while
// connection-exempt members still answer.
func TestAlpacaNotConnected(t *testing.T) {
	sd := &stubDev{pid: pid6200, present: false} // never present
	base, _ := newStubStack(t, sd)
	time.Sleep(200 * time.Millisecond) // let manageHardware (fail to) acquire

	if v := value(t, base, "connected"); v != false {
		t.Errorf("connected = %v, want false", v)
	}
	if v := value(t, base, "name"); v == nil || v == "" {
		t.Errorf("name should answer when disconnected, got %v", v)
	}
	if r := get(t, base, "cameraxsize"); r.ErrorNumber != errNotConnected {
		t.Errorf("GET cameraxsize: ErrorNumber=%d, want %d (NotConnected)", r.ErrorNumber, errNotConnected)
	}
	if r := put(t, base, "startexposure", "Duration=1&Light=true"); r.ErrorNumber != errNotConnected {
		t.Errorf("PUT startexposure: ErrorNumber=%d, want %d (NotConnected)", r.ErrorNumber, errNotConnected)
	}
}

// TestAlpacaReEnumeration: an unplug flips connected to false; a replug re-acquires.
func TestAlpacaReEnumeration(t *testing.T) {
	sd := &stubDev{pid: pid6200, present: true, serial: astrocam.Serial{0x06, 0x11, 0x8f, 0x06, 0x1f, 0x09, 0x09, 0x00}}
	base, _ := newStubStack(t, sd)
	waitConnected(t, base, true)

	sd.setPresent(false) // unplug
	waitConnected(t, base, false)

	sd.setPresent(true) // replug
	waitConnected(t, base, true)

	if v := value(t, base, "name"); v != "ASI6200MC Pro" {
		t.Errorf("name after reconnect = %v", v)
	}
}

// TestAlpacaHardware runs the same stack against a REAL attached camera. Gated by
// ASICAM_HARDWARE + ASICAM_SERIAL so CI / no-hardware runs skip it.
//
//	ASICAM_HARDWARE=1 ASICAM_SERIAL=06118f061f090900 go test -run TestAlpacaHardware -v
func TestAlpacaHardware(t *testing.T) {
	if os.Getenv("ASICAM_HARDWARE") == "" {
		t.Skip("set ASICAM_HARDWARE=1 ASICAM_SERIAL=<hex> with a real camera attached")
	}
	dev := NewPureASICamera(0, os.Getenv("ASICAM_SERIAL")) // real openDev/aliveFn
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := dev.Open(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dev.Close(context.Background()) })
	srv := alpacadev.New(alpacadev.Config{Discovery: alpacadev.DiscoveryConfig{Mode: alpacadev.DiscoveryOff}, ServerName: "asicam-hw", Manufacturer: "test"})
	if err := srv.Register(alpacadev.CameraType, 0, dev); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(http.HandlerFunc(srv.ServeHTTP))
	t.Cleanup(ts.Close)
	base := ts.URL + "/api/v1/camera/0/"
	waitConnected(t, base, true)
	t.Logf("name=%v size=%vx%v temp=%v", value(t, base, "name"),
		value(t, base, "cameraxsize"), value(t, base, "cameraysize"), get(t, base, "ccdtemperature").Value)
}
