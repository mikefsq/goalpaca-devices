package driver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	alpacadev "github.com/mikefsq/goalpaca/server"
	"github.com/mikefsq/goasi/efw"
)

// Alpaca error numbers (ASCOM standard).
const (
	errNotConnected     = 0x407 // 1031
	errInvalidValue     = 0x401 // 1025
	errInvalidOperation = 0x40B // 1035
)

// fakeWheel is an in-memory efw.Transport modeling a filter wheel, so the whole
// stack — Alpaca HTTP → asiefw → goasi/efw → transport — runs end-to-end with no
// hardware. It can also simulate motion (moving) and unplug (removed).
type fakeWheel struct {
	mu        sync.Mutex
	sent      [][]byte
	lastQuery byte // byte[4] subcode of the last query (opcode 0x02)
	pos       int  // 0-based current slot, updated when a move arrives
	slots     int
	moving    bool // status reports state=moving (Position → -1)
	removed   bool // transport calls error (simulated unplug)
}

// Fixtures captured from a real EFW (serial → 1f2120703dcef2b1, model EFW-S-0).
var (
	fxSerial    = []byte{0x01, 0x7e, 0x5a, 0x0c, 0x01, 0x0f, 0x02, 0x01, 0x02, 0x00, 0x07, 0x03, 0xdc, 0xef, 0x2b, 0x01}
	fxHandshake = []byte{0x01, 0x7e, 0x5a, 0x04, 0x03, 0x00, 0x09, 0x00, 0x45, 0x46, 0x57, 0x2d, 0x53, 0x2d, 0x30, 0x00}
)

func (f *fakeWheel) set(field *bool, v bool) { f.mu.Lock(); *field = v; f.mu.Unlock() }
func (f *fakeWheel) isRemoved() bool         { f.mu.Lock(); defer f.mu.Unlock(); return f.removed }

func (f *fakeWheel) SetFeature(b []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.removed {
		return errors.New("device removed")
	}
	f.sent = append(f.sent, append([]byte(nil), b...))
	switch {
	case len(b) >= 6 && b[3] == 0x01 && (b[4] == 0x02 || b[4] == 0x03): // move
		f.pos = int(b[5]) - 1
	case len(b) >= 5 && b[3] == 0x02: // query
		f.lastQuery = b[4]
	}
	return nil
}

func (f *fakeWheel) GetFeature(b []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.removed {
		return errors.New("device removed")
	}
	for i := range b {
		b[i] = 0
	}
	switch f.lastQuery {
	case 0x01: // status
		state := byte(0x01) // idle
		if f.moving {
			state = 0x04
		}
		copy(b, []byte{0x01, 0x7e, 0x5a, 0x01, state, 0x00, byte(f.pos + 1), byte(f.pos + 1), byte(f.pos + 1), byte(f.slots)})
	case 0x0c: // factory serial
		copy(b, fxSerial)
	case 0x04: // open handshake (firmware/model)
		copy(b, fxHandshake)
	case 0x0d: // user alias (unset → zeros)
	}
	return nil
}

func (f *fakeWheel) Close() error { return nil }

// lastMove returns the slot the most recent move command targeted, or -1.
func (f *fakeWheel) lastMove() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.sent) - 1; i >= 0; i-- {
		if b := f.sent[i]; len(b) >= 6 && b[3] == 0x01 && (b[4] == 0x02 || b[4] == 0x03) {
			return int(b[5]) - 1
		}
	}
	return -1
}

// lastMoveDir returns byte[4] of the most recent move command (0x02 bidirectional,
// 0x03 unidirectional), or 0 if none.
func (f *fakeWheel) lastMoveDir() byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.sent) - 1; i >= 0; i-- {
		if b := f.sent[i]; len(b) >= 6 && b[3] == 0x01 && (b[4] == 0x02 || b[4] == 0x03) {
			return b[4]
		}
	}
	return 0
}

func (f *fakeWheel) moveCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, b := range f.sent {
		if len(b) >= 6 && b[3] == 0x01 && (b[4] == 0x02 || b[4] == 0x03) {
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

// newStack wires a wheel (with the given device-opener) into a real Alpaca server
// behind httptest. Returns the device API base URL and the management base URL.
func newStack(t *testing.T, uni bool, open func() (*efw.EFW, error)) (base, mgmt string) {
	t.Helper()
	wheel := NewASIFilterWheel(0, "", uni)
	if open != nil {
		wheel.openDev = open // inject a fake; nil = use the real platform transport
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := wheel.Open(ctx); err != nil {
		t.Fatal(err)
	}

	srv := alpacadev.New(alpacadev.Config{
		Discovery:    alpacadev.DiscoveryConfig{Mode: alpacadev.DiscoveryOff},
		ServerName:   "asiefw-test",
		Manufacturer: "test",
	})
	if err := srv.Register(alpacadev.FilterWheelType, 0, wheel); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(http.HandlerFunc(srv.ServeHTTP))
	t.Cleanup(ts.Close)
	return ts.URL + "/api/v1/filterwheel/0/", ts.URL + "/management/v1/"
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

func waitPosition(t *testing.T, base string, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		v := value(t, base, "position")
		if int(v.(float64)) == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("position never reached %d (last=%v)", want, v)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func fakeOpener(f *fakeWheel) func() (*efw.EFW, error) {
	return func() (*efw.EFW, error) {
		if f.isRemoved() {
			return nil, errors.New("no device")
		}
		return efw.New(f, efw.DeviceInfo{FeatureLen: 64}), nil
	}
}

// TestAlpacaFilterWheel covers the full FilterWheel surface end-to-end against a
// connected (fake) wheel.
func TestAlpacaFilterWheel(t *testing.T) {
	fake := &fakeWheel{slots: 7}
	base, mgmt := newStack(t, false, fakeOpener(fake))
	waitConnected(t, base, true)

	t.Run("metadata", func(t *testing.T) {
		if v := value(t, base, "name"); v != "EFW-S-0" {
			t.Errorf("name = %v, want EFW-S-0", v)
		}
		if v := value(t, base, "description"); !strings.Contains(v.(string), "7 slots") {
			t.Errorf("description = %v, want it to mention 7 slots", v)
		}
		if v := value(t, base, "driverversion"); v != "0.1.0" {
			t.Errorf("driverversion = %v, want 0.1.0", v)
		}
		if v := value(t, base, "interfaceversion"); v != float64(alpacadev.InterfaceVersionFilterWheel) {
			t.Errorf("interfaceversion = %v, want %d", v, alpacadev.InterfaceVersionFilterWheel)
		}
		if v := value(t, base, "driverinfo"); !strings.Contains(v.(string), "asiefw") {
			t.Errorf("driverinfo = %v", v)
		}
		if _, ok := value(t, base, "supportedactions").([]any); !ok {
			t.Errorf("supportedactions not an array")
		}
	})

	t.Run("connection", func(t *testing.T) {
		if v := value(t, base, "connected"); v != true {
			t.Errorf("connected = %v, want true", v)
		}
		if v := value(t, base, "connecting"); v != false {
			t.Errorf("connecting = %v, want false", v)
		}
		if r := put(t, base, "connected", "Connected=true"); r.ErrorNumber != 0 {
			t.Errorf("PUT connected=true: err %d", r.ErrorNumber)
		}
		// Disconnect is a logical no-op for a persistent-owner driver: the wheel
		// stays connected (connection follows hardware, not client sessions).
		if r := put(t, base, "connected", "Connected=false"); r.ErrorNumber != 0 {
			t.Errorf("PUT connected=false: err %d", r.ErrorNumber)
		}
		if v := value(t, base, "connected"); v != true {
			t.Errorf("connected after disconnect = %v, want still true", v)
		}
	})

	t.Run("reads", func(t *testing.T) {
		if v := value(t, base, "position"); v != float64(0) {
			t.Errorf("position = %v, want 0", v)
		}
		names, ok := value(t, base, "names").([]any)
		if !ok || len(names) != 7 || names[0] != "Filter 1" {
			t.Errorf("names = %v, want 7 entries starting Filter 1", names)
		}
		offsets, ok := value(t, base, "focusoffsets").([]any)
		if !ok || len(offsets) != 7 || offsets[0] != float64(0) {
			t.Errorf("focusoffsets = %v, want 7 zeros", offsets)
		}
	})

	t.Run("devicestate", func(t *testing.T) {
		arr, ok := value(t, base, "devicestate").([]any)
		if !ok {
			t.Fatalf("devicestate not an array: %v", arr)
		}
		found := false
		for _, e := range arr {
			if m, ok := e.(map[string]any); ok && m["Name"] == "Position" {
				found = true
			}
		}
		if !found {
			t.Errorf("devicestate missing Position: %v", arr)
		}
	})

	t.Run("uniqueid via management", func(t *testing.T) {
		r, err := http.Get(mgmt + "configureddevices?" + txQ)
		if err != nil {
			t.Fatal(err)
		}
		got := decode(t, r)
		arr, ok := got.Value.([]any)
		if !ok || len(arr) == 0 {
			t.Fatalf("configureddevices = %v", got.Value)
		}
		dev := arr[0].(map[string]any)
		if dev["UniqueID"] != "EFW-1f2120703dcef2b1" {
			t.Errorf("UniqueID = %v, want EFW-1f2120703dcef2b1 (factory serial)", dev["UniqueID"])
		}
	})

	t.Run("move valid", func(t *testing.T) {
		if r := put(t, base, "position", "Position=3"); r.ErrorNumber != 0 {
			t.Fatalf("PUT position=3: err %d (%s)", r.ErrorNumber, r.ErrorMessage)
		}
		if got := fake.lastMove(); got != 3 {
			t.Fatalf("transport saw move to %d, want 3", got)
		}
		if dir := fake.lastMoveDir(); dir != 0x02 {
			t.Errorf("move direction = 0x%02x, want 0x02 (bidirectional default)", dir)
		}
		if v := value(t, base, "position"); v != float64(3) {
			t.Errorf("position after move = %v, want 3", v)
		}
	})

	t.Run("move out of range", func(t *testing.T) {
		before := fake.moveCount()
		r := put(t, base, "position", "Position=99")
		if r.ErrorNumber != errInvalidValue {
			t.Errorf("PUT position=99: ErrorNumber=%d, want %d (InvalidValue)", r.ErrorNumber, errInvalidValue)
		}
		if fake.moveCount() != before {
			t.Errorf("out-of-range move should not reach the transport")
		}
	})
}

// TestAlpacaUnidirectional: a wheel configured unidirectional must stamp the uni
// direction byte (0x03) into the move command that an Alpaca move produces.
func TestAlpacaUnidirectional(t *testing.T) {
	fake := &fakeWheel{slots: 7}
	base, _ := newStack(t, true, fakeOpener(fake)) // unidirectional
	waitConnected(t, base, true)

	if r := put(t, base, "position", "Position=3"); r.ErrorNumber != 0 {
		t.Fatalf("PUT position=3: err %d (%s)", r.ErrorNumber, r.ErrorMessage)
	}
	if dir := fake.lastMoveDir(); dir != 0x03 {
		t.Errorf("move direction = 0x%02x, want 0x03 (unidirectional)", dir)
	}
}

// TestAlpacaBusyGating: while the wheel is moving, mutating writes are rejected
// with InvalidOperation, but reads still work.
func TestAlpacaBusyGating(t *testing.T) {
	fake := &fakeWheel{slots: 7}
	base, _ := newStack(t, false, fakeOpener(fake))
	waitConnected(t, base, true)

	fake.set(&fake.moving, true)

	if v := value(t, base, "position"); v != float64(-1) {
		t.Errorf("position while moving = %v, want -1", v)
	}
	before := fake.moveCount()
	r := put(t, base, "position", "Position=2")
	if r.ErrorNumber != errInvalidOperation {
		t.Errorf("PUT position while moving: ErrorNumber=%d, want %d (InvalidOperation)", r.ErrorNumber, errInvalidOperation)
	}
	if fake.moveCount() != before {
		t.Errorf("a move while busy should not reach the transport")
	}
}

// TestAlpacaNotConnected: with no wheel present, operational members return
// NotConnected, while connection-exempt members still answer.
func TestAlpacaNotConnected(t *testing.T) {
	neverOpens := func() (*efw.EFW, error) { return nil, errors.New("no device") }
	base, _ := newStack(t, false, neverOpens)
	// Give manageHardware a moment to (fail to) acquire.
	time.Sleep(200 * time.Millisecond)

	if v := value(t, base, "connected"); v != false {
		t.Errorf("connected = %v, want false", v)
	}
	if v := value(t, base, "name"); v == nil || v == "" { // exempt: answers even when disconnected
		t.Errorf("name should answer when disconnected, got %v", v)
	}
	if r := get(t, base, "position"); r.ErrorNumber != errNotConnected {
		t.Errorf("GET position: ErrorNumber=%d, want %d (NotConnected)", r.ErrorNumber, errNotConnected)
	}
	if r := put(t, base, "position", "Position=1"); r.ErrorNumber != errNotConnected {
		t.Errorf("PUT position: ErrorNumber=%d, want %d (NotConnected)", r.ErrorNumber, errNotConnected)
	}
}

// TestAlpacaHardware runs the same full Alpaca stack against a REAL attached EFW
// (the platform transport, not a fake). Gated by EFW_HARDWARE so CI / no-hardware
// runs skip it. It physically rotates the wheel one slot and back.
//
//	EFW_HARDWARE=1 go test -race -run TestAlpacaHardware -v ./...
func TestAlpacaHardware(t *testing.T) {
	if os.Getenv("EFW_HARDWARE") == "" {
		t.Skip("set EFW_HARDWARE=1 (with a real EFW attached and free) to run the hardware e2e test")
	}
	base, mgmt := newStack(t, false, nil) // nil opener → real platform transport
	waitConnected(t, base, true)

	t.Logf("name=%v firmware-derived", value(t, base, "name"))
	names, ok := value(t, base, "names").([]any)
	if !ok || len(names) == 0 {
		t.Fatalf("no filter names: %v", names)
	}
	slots := len(names)

	if r, err := http.Get(mgmt + "configureddevices?" + txQ); err == nil {
		if arr, ok := decode(t, r).Value.([]any); ok && len(arr) > 0 {
			t.Logf("uniqueid=%v slots=%d", arr[0].(map[string]any)["UniqueID"], slots)
		}
	}

	start := int(value(t, base, "position").(float64))
	if start < 0 {
		t.Skip("wheel was moving at start")
	}
	target := (start + 1) % slots
	t.Logf("moving %d -> %d", start, target)
	if r := put(t, base, "position", "Position="+strconv.Itoa(target)); r.ErrorNumber != 0 {
		t.Fatalf("move: ErrorNumber=%d (%s)", r.ErrorNumber, r.ErrorMessage)
	}
	waitPosition(t, base, target, 30*time.Second)
	t.Logf("arrived at %d; restoring to %d", target, start)

	if r := put(t, base, "position", "Position="+strconv.Itoa(start)); r.ErrorNumber != 0 {
		t.Fatalf("restore move: ErrorNumber=%d", r.ErrorNumber)
	}
	waitPosition(t, base, start, 30*time.Second)
}

// TestAlpacaReEnumeration: a drop (transport errors) flips connected to false;
// when the device returns, the driver re-acquires and connected goes true again.
func TestAlpacaReEnumeration(t *testing.T) {
	fake := &fakeWheel{slots: 7}
	base, _ := newStack(t, false, fakeOpener(fake))
	waitConnected(t, base, true)

	fake.set(&fake.removed, true) // unplug
	waitConnected(t, base, false) // the monitor probe detects it and tears down

	fake.set(&fake.removed, false) // replug
	waitConnected(t, base, true)   // re-acquired

	// Still fully operational after the reconnect.
	if v := value(t, base, "position"); v != float64(0) && v != float64(3) {
		t.Errorf("position after reconnect = %v, want a valid slot", v)
	}
}
