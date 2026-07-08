package driver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	alpacadev "github.com/mikefsq/goalpaca/server"
	"github.com/mikefsq/oasis-astro/oasisfoc"
)

const txQ = "ClientID=1&ClientTransactionID=1"

// fakeHID is a scripted oasisfoc.Transport: it echoes a valid-opcode reply for any
// command, with a scripted override per opcode (e.g. the status reply).
type fakeHID struct {
	mu       sync.Mutex
	lastOp   byte
	replies  map[byte][]byte
	statusFn func() []byte // if set, overrides the 0x32 status reply call-by-call
	stopped  bool          // set true once a StopMove (0x37) is written
}

func (f *fakeHID) Write(buf []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(buf) > 1 {
		f.lastOp = buf[1]
		if buf[1] == 0x37 { // StopMove
			f.stopped = true
		}
	}
	return len(buf), nil
}

func (f *fakeHID) Read(buf []byte, timeoutMS int) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if timeoutMS <= 0 {
		return 0, nil // drain
	}
	if f.lastOp == 0x32 && f.statusFn != nil {
		return copy(buf, f.statusFn()), nil
	}
	if r, ok := f.replies[f.lastOp]; ok {
		return copy(buf, r), nil
	}
	return copy(buf, []byte{f.lastOp, 0x00}), nil // default opcode-echo ack
}

func (f *fakeHID) Close() error { return nil }

// statusReply is the :0x32 status frame: moving at +11, position BE int32 at +12.
func statusReply(pos uint32, moving byte) []byte {
	r := make([]byte, 16)
	r[0], r[1] = 0x32, 0x0e
	r[11] = moving
	r[12], r[13], r[14], r[15] = byte(pos>>24), byte(pos>>16), byte(pos>>8), byte(pos)
	return r
}

type resp struct {
	Value        any
	ErrorNumber  int
	ErrorMessage string
}

func serve(t *testing.T, foc *OasisFocuser) string {
	t.Helper()
	srv := alpacadev.New(alpacadev.Config{
		Discovery: alpacadev.DiscoveryConfig{Mode: alpacadev.DiscoveryOff}, ServerName: "t", Manufacturer: "t",
	})
	if err := srv.Register(alpacadev.FocuserType, 0, foc); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(http.HandlerFunc(srv.ServeHTTP))
	t.Cleanup(ts.Close)
	return ts.URL + "/api/v1/focuser/0/"
}

func get(t *testing.T, base, member string) resp {
	t.Helper()
	r, err := http.Get(base + member + "?" + txQ)
	if err != nil {
		t.Fatalf("GET %s: %v", member, err)
	}
	defer r.Body.Close()
	var out resp
	json.NewDecoder(r.Body).Decode(&out)
	return out
}

func put(t *testing.T, base, member, form string) resp {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPut, base+member, strings.NewReader(form+"&"+txQ))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT %s: %v", member, err)
	}
	defer r.Body.Close()
	var out resp
	json.NewDecoder(r.Body).Decode(&out)
	return out
}

func TestOasisFocuserAlpaca(t *testing.T) {
	f := &fakeHID{replies: map[byte][]byte{0x32: statusReply(5000, 0)}}
	foc := NewOasisFocuser(0)
	foc.dev = oasisfoc.New(f, oasisfoc.DeviceInfo{}) // inject (in-package); no manageHardware
	foc.maxStep = 100000

	base := serve(t, foc)

	if get(t, base, "connected").Value != true {
		t.Errorf("connected = false, want true")
	}
	if get(t, base, "absolute").Value != true {
		t.Errorf("absolute = false, want true")
	}
	if v, _ := get(t, base, "maxstep").Value.(float64); int(v) != 100000 {
		t.Errorf("maxstep = %v, want 100000", v)
	}
	if v, _ := get(t, base, "position").Value.(float64); int(v) != 5000 {
		t.Errorf("position = %v, want 5000", v)
	}
	if get(t, base, "ismoving").Value != false {
		t.Errorf("ismoving = true, want false")
	}
	if r := put(t, base, "move", "Position=6000"); r.ErrorNumber != 0 {
		t.Errorf("move: err %d (%s)", r.ErrorNumber, r.ErrorMessage)
	}
	if r := put(t, base, "move", "Position=-1"); r.ErrorNumber != 0x401 { // ErrInvalidValue
		t.Errorf("move(-1): err %d, want 0x401", r.ErrorNumber)
	}
	if r := put(t, base, "halt", ""); r.ErrorNumber != 0 {
		t.Errorf("halt: err %d (%s)", r.ErrorNumber, r.ErrorMessage)
	}
}

func TestOasisFocuserMovingAndDisconnected(t *testing.T) {
	f := &fakeHID{replies: map[byte][]byte{0x32: statusReply(5000, 1)}} // moving
	foc := NewOasisFocuser(0)
	foc.dev = oasisfoc.New(f, oasisfoc.DeviceInfo{})
	base := serve(t, foc)
	if get(t, base, "ismoving").Value != true {
		t.Errorf("ismoving = false, want true")
	}

	foc2 := NewOasisFocuser(0) // no dev → not connected
	base2 := serve2(t, foc2)
	if get(t, base2, "connected").Value != false {
		t.Errorf("connected = true, want false (no device)")
	}
}

// serve2 registers a second device on a fresh server (avoids index-0 clash).
func serve2(t *testing.T, foc *OasisFocuser) string { return serve(t, foc) }

// --- scripted replies for the Action tests ---

func cfgReply() []byte { // 0x30 part-1: maxStep 80000, beeps on
	r := make([]byte, 20)
	r[0], r[1] = 0x30, 0x12
	r[2], r[3], r[4], r[5] = 0xff, 0xff, 0xff, 0xff // mask
	r[6], r[7], r[8], r[9] = 0x00, 0x01, 0x38, 0x80 // maxStep 80000
	r[17], r[18], r[19] = 1, 1, 1                   // beepOnMove, beepOnStartup, bluetoothOn
	return r
}

func extReply() []byte { // 0x3a part-2: heatingTemp 2500 (25°C), rest 0
	r := make([]byte, 42)
	r[0], r[1] = 0x3a, 0x28
	r[6], r[7], r[8], r[9] = 0x00, 0x01, 0x38, 0x80     // maxStep
	r[17], r[18], r[19] = 1, 1, 1                       // beeps/bt (data[15:18])
	r[21], r[22], r[23], r[24] = 0x00, 0x00, 0x09, 0xc4 // heatingTemp @data[19:23]
	return r
}

func verReply() []byte { // 0x02: hardware 1.2.0.0, firmware 2.1.1.0
	r := make([]byte, 36)
	r[0], r[1] = 0x02, 0x24
	r[6], r[7] = 0x01, 0x02                // hardware 1.2.0.0 (data[4:8])
	r[10], r[11], r[12] = 0x02, 0x01, 0x01 // firmware 2.1.1.0 (data[8:12])
	return r
}

func modelReply() []byte { // 0x01: "OasisFocuserRose"
	r := append([]byte{0x01, 0x20}, []byte("OasisFocuserRose")...)
	for len(r) < 34 {
		r = append(r, 0)
	}
	return r
}

// action PUTs the Alpaca action endpoint and returns the response.
func action(t *testing.T, base, name, params string) resp {
	t.Helper()
	return put(t, base, "action", "Action="+name+"&Parameters="+params)
}

func TestOasisFocuserActions(t *testing.T) {
	f := &fakeHID{replies: map[byte][]byte{
		0x32: statusReply(5000, 0),
		0x30: cfgReply(),
		0x3a: extReply(),
		0x02: verReply(),
		0x01: modelReply(),
	}}
	foc := NewOasisFocuser(0)
	foc.dev = oasisfoc.New(f, oasisfoc.DeviceInfo{})
	foc.maxStep = 80000
	foc.dev.Handshake() // populate cached version/model (OpenAt does this on real hardware)
	base := serve(t, foc)

	// SupportedActions advertises the surface.
	sa := get(t, base, "supportedactions")
	names, _ := sa.Value.([]any)
	have := map[string]bool{}
	for _, n := range names {
		have[strings.ToLower(n.(string))] = true // advertised CamelCase; match case-insensitively
	}
	for _, want := range []string{"backlash", "reverse", "heatingon", "config", "firmwareversion", "movein"} {
		if !have[want] {
			t.Errorf("supportedactions missing %q", want)
		}
	}
	// Collapsed convention: no separate SetX actions.
	if have["setbacklash"] || have["setreverse"] {
		t.Error("SetX actions should be collapsed into their read/write field action")
	}

	// Getters.
	if r := action(t, base, "firmwareversion", ""); r.Value != "2.1.1.0" {
		t.Errorf("firmwareversion = %v (err %d), want 2.1.1.0", r.Value, r.ErrorNumber)
	}
	if r := action(t, base, "hardwareversion", ""); r.Value != "1.2.0.0" {
		t.Errorf("hardwareversion = %v, want 1.2.0.0", r.Value)
	}
	if r := action(t, base, "model", ""); r.Value != "OasisFocuserRose" {
		t.Errorf("model = %v, want OasisFocuserRose", r.Value)
	}
	if r := action(t, base, "heatingtemperature", ""); r.Value != "2500" {
		t.Errorf("heatingtemperature = %v, want 2500", r.Value)
	}
	if r := action(t, base, "config", ""); !strings.Contains(r.Value.(string), "maxStep=80000") {
		t.Errorf("config = %v, want it to contain maxStep=80000", r.Value)
	}

	// Dual-mode field: a value writes → "ok"; empty reads back a value.
	if r := action(t, base, "beeponmove", "off"); r.Value != "ok" {
		t.Errorf("beeponmove set: %v (err %d %s)", r.Value, r.ErrorNumber, r.ErrorMessage)
	}
	if r := action(t, base, "backlash", "500"); r.Value != "ok" {
		t.Errorf("backlash set: %v (err %d)", r.Value, r.ErrorNumber)
	}
	if r := action(t, base, "backlash", ""); r.ErrorNumber != 0 || r.Value == "" {
		t.Errorf("backlash read: %v (err %d), want a value", r.Value, r.ErrorNumber)
	}
	if r := action(t, base, "heatingon", "true"); r.Value != "ok" {
		t.Errorf("heatingon set: %v (err %d)", r.Value, r.ErrorNumber)
	}
	if r := action(t, base, "movein", "100"); r.Value != "ok" {
		t.Errorf("movein: %v (err %d)", r.Value, r.ErrorNumber)
	}

	// Read-only action rejects a params value.
	if r := action(t, base, "serial", "xyz"); r.ErrorNumber != 0x401 {
		t.Errorf("serial(with params): err %d, want 0x401 (read-only)", r.ErrorNumber)
	}
	// Bad integer param → InvalidValue.
	if r := action(t, base, "backlash", "notanumber"); r.ErrorNumber != 0x401 {
		t.Errorf("backlash(bad): err %d, want 0x401", r.ErrorNumber)
	}
	// factoryreset is guarded.
	if r := action(t, base, "factoryreset", ""); r.ErrorNumber != 0x401 {
		t.Errorf("factoryreset(no confirm): err %d, want 0x401 (guarded)", r.ErrorNumber)
	}
	if r := action(t, base, "factoryreset", "confirm"); r.Value != "ok" {
		t.Errorf("factoryreset(confirm): %v (err %d)", r.Value, r.ErrorNumber)
	}
	// Unknown action → ActionNotImplemented (0x40C).
	if r := action(t, base, "bogus", ""); r.ErrorNumber != 0x40c {
		t.Errorf("bogus action: err %d, want 0x40c", r.ErrorNumber)
	}
}

// --- Tier-2 hardware end-to-end test (gated; opens the REAL focuser) ---
//
// Mirrors the fleet pattern (asieaf/asiefw TestAlpacaHardware): open the real device via
// the driver's normal Open() lifecycle, serve it on a real Alpaca server behind httptest,
// and drive the full HTTP stack with a small non-destructive relative move-and-restore.

func value(t *testing.T, base, member string) any {
	t.Helper()
	r := get(t, base, member)
	if r.ErrorNumber != 0 {
		t.Fatalf("%s: ErrorNumber=%d (%s)", member, r.ErrorNumber, r.ErrorMessage)
	}
	return r.Value
}

func waitConnected(t *testing.T, base string, want bool) {
	t.Helper()
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

func waitNotMoving(t *testing.T, base string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if value(t, base, "ismoving") == false {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("focuser never stopped moving")
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func newRealStack(t *testing.T) (base, mgmt string) {
	t.Helper()
	f := NewOasisFocuser(0) // default openDev = real openByIndex (platform transport)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()                      // stop manageHardware (its defer closes the device)
		f.Close(context.Background()) // and close synchronously so the handle is released now
	})
	if err := f.Open(ctx); err != nil {
		t.Fatal(err)
	}
	srv := alpacadev.New(alpacadev.Config{
		Discovery: alpacadev.DiscoveryConfig{Mode: alpacadev.DiscoveryOff}, ServerName: "oasisfoc-test", Manufacturer: "test",
	})
	if err := srv.Register(alpacadev.FocuserType, 0, f); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(http.HandlerFunc(srv.ServeHTTP))
	t.Cleanup(ts.Close)
	return ts.URL + "/api/v1/focuser/0/", ts.URL + "/management/v1/"
}

func TestOasisFocuserHardware(t *testing.T) {
	if os.Getenv("OASISFOC_HARDWARE") == "" {
		t.Skip("set OASISFOC_HARDWARE=1 (with a real Oasis focuser attached and free) to run the hardware e2e test")
	}
	base, _ := newRealStack(t)
	waitConnected(t, base, true)

	for _, a := range []string{"model", "firmwareversion", "serial"} {
		r := action(t, base, a, "")
		t.Logf("%s = %v (err %d %s)", a, r.Value, r.ErrorNumber, r.ErrorMessage)
	}
	waitNotMoving(t, base, 30*time.Second)
	start := int(value(t, base, "position").(float64))
	maxstep := int(value(t, base, "maxstep").(float64))
	t.Logf("position=%d maxstep=%d", start, maxstep)

	// Small, bounded relative move-and-restore.
	const delta = 200
	target := start + delta
	if target > maxstep {
		target = start - delta
	}
	if target < 0 {
		t.Skip("no safe move range near start position")
	}
	t.Logf("moving %d -> %d", start, target)
	if r := put(t, base, "move", "Position="+strconv.Itoa(target)); r.ErrorNumber != 0 {
		t.Fatalf("move: ErrorNumber=%d (%s)", r.ErrorNumber, r.ErrorMessage)
	}
	waitNotMoving(t, base, 30*time.Second)
	t.Logf("arrived at %d; restoring to %d", int(value(t, base, "position").(float64)), start)
	if r := put(t, base, "move", "Position="+strconv.Itoa(start)); r.ErrorNumber != 0 {
		t.Fatalf("restore move: ErrorNumber=%d", r.ErrorNumber)
	}
	waitNotMoving(t, base, 30*time.Second)
}

// TestSyncShortMove: a short move (≤ syncMoveMaxSteps) blocks inside Move until the
// device reports idle AND at target, so IsMoving is already false on return — no client
// poll wait. The scripted status reports "moving at start" for the first reads, then
// "idle at target".
func TestSyncShortMove(t *testing.T) {
	const start, target = 5000, 5100 // 100-step move, under the threshold
	reads := 0
	f := &fakeHID{replies: map[byte][]byte{}}
	f.statusFn = func() []byte {
		reads++
		if reads <= 4 { // first couple of polls: still moving, not yet at target
			return statusReply(start, 1)
		}
		return statusReply(target, 0) // settled at target, idle
	}
	foc := NewOasisFocuser(0)
	foc.dev = oasisfoc.New(f, oasisfoc.DeviceInfo{})
	foc.maxStep = 80000

	t0 := time.Now()
	if err := foc.Move(target); err != nil {
		t.Fatalf("Move: %v", err)
	}
	el := time.Since(t0)
	if el < syncMovePoll {
		t.Errorf("short Move returned in %v; should block until the device settles", el)
	}
	if el > syncMoveCap {
		t.Errorf("short Move blocked %v; exceeds the %v cap", el, syncMoveCap)
	}
	if foc.IsMoving() {
		t.Error("IsMoving should be false immediately after a synchronous short Move")
	}
}

// TestNoOpMove: a move whose target already equals the live position is a no-op — it must
// return immediately (no settle block) and must NOT issue a MoveTo (0x36). NINA re-sends the
// same target on a separate connection; that duplicate should resolve instantly.
func TestNoOpMove(t *testing.T) {
	const pos = 41700
	f := &fakeHID{replies: map[byte][]byte{}}
	f.statusFn = func() []byte { return statusReply(pos, 0) } // always at target, idle
	foc := NewOasisFocuser(0)
	foc.dev = oasisfoc.New(f, oasisfoc.DeviceInfo{})
	foc.maxStep = 80000

	t0 := time.Now()
	if err := foc.Move(pos); err != nil {
		t.Fatalf("Move: %v", err)
	}
	if el := time.Since(t0); el >= syncMovePoll {
		t.Errorf("no-op Move took %v; should return immediately without the settle block", el)
	}
	f.mu.Lock()
	lastOp := f.lastOp
	f.mu.Unlock()
	if lastOp == 0x36 { // MoveTo
		t.Error("no-op Move issued a MoveTo (0x36); should skip re-commanding when already at target")
	}
}

// TestConcurrentMoveHalt: a Halt issued while a synchronous Move is blocking must run
// concurrently (separate goroutine, as Go's HTTP server does), stop the device, and let
// the Move return promptly — with no data race on device access (run under -race).
func TestConcurrentMoveHalt(t *testing.T) {
	f := &fakeHID{replies: map[byte][]byte{}}
	f.statusFn = func() []byte {
		if f.stopped {
			return statusReply(5040, 0) // idle once StopMove was issued
		}
		return statusReply(5010, 1) // moving
	}
	foc := NewOasisFocuser(0)
	foc.dev = oasisfoc.New(f, oasisfoc.DeviceInfo{})
	foc.maxStep = 80000

	done := make(chan error, 1)
	go func() { done <- foc.Move(5100) }() // short move → blocks; device reports moving
	time.Sleep(40 * time.Millisecond)      // let the block start polling
	if err := foc.Halt(); err != nil {
		t.Fatalf("Halt: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Move: %v", err)
		}
	case <-time.After(syncMoveCap + 500*time.Millisecond):
		t.Fatal("Move did not return after a concurrent Halt")
	}
}

// TestSyncMoveStoppedShort: if the device stops short of target (a concurrent /halt or a
// soft-limit stop) the synchronous block must release as soon as motion ends — not spin
// to the cap waiting for an at-target that will never come.
func TestSyncMoveStoppedShort(t *testing.T) {
	const start, target = 5000, 5100 // short move (synchronous path)
	reads := 0
	f := &fakeHID{replies: map[byte][]byte{}}
	f.statusFn = func() []byte {
		reads++
		if reads <= 4 {
			return statusReply(start+10, 1) // moving, mid-travel
		}
		return statusReply(start+40, 0) // halted: idle, NOT at target
	}
	foc := NewOasisFocuser(0)
	foc.dev = oasisfoc.New(f, oasisfoc.DeviceInfo{})
	foc.maxStep = 80000

	t0 := time.Now()
	if err := foc.Move(target); err != nil {
		t.Fatalf("Move: %v", err)
	}
	if el := time.Since(t0); el >= syncMoveCap {
		t.Errorf("stopped-short Move blocked %v; should release when motion ends, not spin to the cap", el)
	}
}

// TestAsyncLongMove: a move beyond syncMoveMaxSteps returns immediately (async) even
// while the device still reports moving — it must not block the handler.
func TestAsyncLongMove(t *testing.T) {
	f := &fakeHID{replies: map[byte][]byte{0x32: statusReply(5000, 1)}} // always moving
	foc := NewOasisFocuser(0)
	foc.dev = oasisfoc.New(f, oasisfoc.DeviceInfo{})
	foc.maxStep = 80000

	t0 := time.Now()
	if err := foc.Move(60000); err != nil { // ~55000 steps, far over the threshold
		t.Fatalf("Move: %v", err)
	}
	if el := time.Since(t0); el >= syncMoveCap {
		t.Errorf("long Move blocked %v; should return immediately (async)", el)
	}
}
