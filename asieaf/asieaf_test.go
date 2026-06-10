package driver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	alpacadev "github.com/mikefsq/goalpaca/server"
	"github.com/mikefsq/goasi/eaf"
)

// Alpaca/ASCOM error numbers.
const (
	errNotImplemented   = 0x400
	errInvalidValue     = 0x401
	errNotConnected     = 0x407
	errInvalidOperation = 0x40B
)

// fakeEAF is an in-memory eaf.Transport modeling an EAF, so the whole stack —
// Alpaca HTTP → asieaf → goasi/eaf → transport — runs end-to-end with no hardware.
// It uses the 16-bit status path (no firmware handshake on the New() open path).
type fakeEAF struct {
	mu      sync.Mutex
	pos     int
	maxStep int
	moving  bool
	removed bool
	sent    [][]byte
}

func (f *fakeEAF) set(field *bool, v bool) { f.mu.Lock(); *field = v; f.mu.Unlock() }
func (f *fakeEAF) isRemoved() bool         { f.mu.Lock(); defer f.mu.Unlock(); return f.removed }

func (f *fakeEAF) SetFeature(b []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.removed {
		return errors.New("device removed")
	}
	f.sent = append(f.sent, append([]byte(nil), b...))
	// Control report (opcode 0x03): a move (b[4]==1) settles instantly to the
	// 16-bit target at [8][9].
	if len(b) > 9 && b[3] == 0x03 && b[4] == 1 {
		f.pos = int(b[8])<<8 | int(b[9])
	}
	return nil
}

func (f *fakeEAF) GetFeature(b []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.removed {
		return errors.New("device removed")
	}
	if len(f.sent) == 0 {
		return nil
	}
	last := f.sent[len(f.sent)-1]
	// Reply to the status query [7E 5A 02 03].
	if len(last) > 4 && last[3] == 0x02 && last[4] == 0x03 {
		b[0], b[1], b[2], b[3] = 0x01, 0x7E, 0x5A, 0x03
		if f.moving {
			b[4] = 1
		}
		b[8], b[9] = byte(f.pos>>8), byte(f.pos)
		b[14], b[15] = byte(f.maxStep>>8), byte(f.maxStep)
	}
	return nil
}

func (f *fakeEAF) Close() error { return nil }

// moveCount counts control-report moves that reached the transport.
func (f *fakeEAF) moveCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, b := range f.sent {
		if len(b) > 4 && b[3] == 0x03 && b[4] == 1 {
			n++
		}
	}
	return n
}

// --- HTTP helpers ---

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

// newStack wires a focuser (with the given device-opener) into a real Alpaca
// server behind httptest. Returns the device API base URL and the management base.
func newStack(t *testing.T, open func() (*eaf.EAF, error)) (base, mgmt string) {
	t.Helper()
	foc := NewASIFocuser(0, "")
	if open != nil {
		foc.openDev = open // inject a fake; nil = use the real platform transport
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := foc.Open(ctx); err != nil {
		t.Fatal(err)
	}

	srv := alpacadev.New(alpacadev.Config{
		Discovery:    alpacadev.DiscoveryConfig{Mode: alpacadev.DiscoveryOff},
		ServerName:   "asieaf-test",
		Manufacturer: "test",
	})
	if err := srv.Register(alpacadev.FocuserType, 0, foc); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(http.HandlerFunc(srv.ServeHTTP))
	t.Cleanup(ts.Close)
	return ts.URL + "/api/v1/focuser/0/", ts.URL + "/management/v1/"
}

func waitConnected(t *testing.T, base string, want bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
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

func fakeOpener(f *fakeEAF) func() (*eaf.EAF, error) {
	return func() (*eaf.EAF, error) {
		if f.isRemoved() {
			return nil, errors.New("no device")
		}
		return eaf.New(f, eaf.DeviceInfo{FeatureLen: 64}), nil
	}
}

// TestAlpacaFocuser covers the Focuser surface end-to-end against a connected
// (fake) EAF.
func TestAlpacaFocuser(t *testing.T) {
	fake := &fakeEAF{pos: 1000, maxStep: 60000}
	base, _ := newStack(t, fakeOpener(fake))
	waitConnected(t, base, true)

	if v := value(t, base, "absolute"); v != true {
		t.Errorf("absolute = %v, want true", v)
	}
	if v := value(t, base, "maxstep"); v != float64(60000) {
		t.Errorf("maxstep = %v, want 60000", v)
	}
	if v := value(t, base, "position"); v != float64(1000) {
		t.Errorf("position = %v, want 1000", v)
	}
	if v := value(t, base, "ismoving"); v != false {
		t.Errorf("ismoving = %v, want false", v)
	}
	// Temperature: EAF thermistor LUT undecoded -> NotImplemented.
	if r := get(t, base, "temperature"); r.ErrorNumber != errNotImplemented {
		t.Errorf("GET temperature: ErrorNumber=%d, want %d (NotImplemented)", r.ErrorNumber, errNotImplemented)
	}

	// Absolute move.
	if r := put(t, base, "move", "Position=5000"); r.ErrorNumber != 0 {
		t.Fatalf("PUT move: ErrorNumber=%d (%s)", r.ErrorNumber, r.ErrorMessage)
	}
	if v := value(t, base, "position"); v != float64(5000) {
		t.Errorf("position after move = %v, want 5000", v)
	}

	// Out-of-range move is rejected.
	if r := put(t, base, "move", "Position=70000"); r.ErrorNumber != errInvalidValue {
		t.Errorf("PUT move past maxstep: ErrorNumber=%d, want %d (InvalidValue)", r.ErrorNumber, errInvalidValue)
	}

	// Halt answers.
	if r := put(t, base, "halt", ""); r.ErrorNumber != 0 {
		t.Errorf("PUT halt: ErrorNumber=%d (%s)", r.ErrorNumber, r.ErrorMessage)
	}
}

// TestAlpacaBusyGating: a move while the focuser is moving is rejected and never
// reaches the transport.
func TestAlpacaBusyGating(t *testing.T) {
	fake := &fakeEAF{pos: 1000, maxStep: 60000}
	base, _ := newStack(t, fakeOpener(fake))
	waitConnected(t, base, true)

	fake.set(&fake.moving, true)
	if v := value(t, base, "ismoving"); v != true {
		t.Fatalf("ismoving = %v, want true", v)
	}

	before := fake.moveCount()
	r := put(t, base, "move", "Position=2000")
	if r.ErrorNumber != errInvalidOperation {
		t.Errorf("PUT move while moving: ErrorNumber=%d, want %d (InvalidOperation)", r.ErrorNumber, errInvalidOperation)
	}
	if fake.moveCount() != before {
		t.Errorf("a move while busy should not reach the transport")
	}
}

// TestAlpacaNotConnected: with no focuser present, operational members return
// NotConnected while connection-exempt members still answer.
func TestAlpacaNotConnected(t *testing.T) {
	neverOpens := func() (*eaf.EAF, error) { return nil, errors.New("no device") }
	base, _ := newStack(t, neverOpens)
	time.Sleep(200 * time.Millisecond) // let manageHardware (fail to) acquire

	if v := value(t, base, "connected"); v != false {
		t.Errorf("connected = %v, want false", v)
	}
	if v := value(t, base, "name"); v == nil || v == "" {
		t.Errorf("name should answer when disconnected, got %v", v)
	}
	if r := get(t, base, "position"); r.ErrorNumber != errNotConnected {
		t.Errorf("GET position: ErrorNumber=%d, want %d (NotConnected)", r.ErrorNumber, errNotConnected)
	}
	if r := put(t, base, "move", "Position=1"); r.ErrorNumber != errNotConnected {
		t.Errorf("PUT move: ErrorNumber=%d, want %d (NotConnected)", r.ErrorNumber, errNotConnected)
	}
}

// TestAlpacaHardware runs the full stack against a REAL attached EAF (the platform
// transport, not a fake). Gated by EAF_HARDWARE so no-hardware runs skip it.
//
//	EAF_HARDWARE=1 go test -race -run TestAlpacaHardware -v ./...
func TestAlpacaHardware(t *testing.T) {
	if os.Getenv("EAF_HARDWARE") == "" {
		t.Skip("set EAF_HARDWARE=1 to run against a real EAF")
	}
	base, _ := newStack(t, nil) // nil opener -> real platform transport
	waitConnected(t, base, true)
	t.Logf("position=%v maxstep=%v", value(t, base, "position"), value(t, base, "maxstep"))
}
