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
	"github.com/mikefsq/oasis-astro/oasisfw"
)

const txQ = "ClientID=1&ClientTransactionID=1"

// fakeHID is a scripted oasisfw.Transport: echoes a valid-opcode ack for any
// command, with a scripted override per opcode (the status reply).
type fakeHID struct {
	mu      sync.Mutex
	lastOp  byte
	replies map[byte][]byte
}

func (f *fakeHID) Write(buf []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(buf) > 1 {
		f.lastOp = buf[1]
	}
	return len(buf), nil
}

func (f *fakeHID) Read(buf []byte, timeoutMS int) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if timeoutMS <= 0 {
		return 0, nil
	}
	if r, ok := f.replies[f.lastOp]; ok {
		return copy(buf, r), nil
	}
	return copy(buf, []byte{f.lastOp, 0x00}), nil
}

func (f *fakeHID) Close() error { return nil }

// statusReply: opStatus 0x32, filterStatus(state)@6, filterPosition(1-based)@7.
func statusReply(state, pos1 byte) []byte {
	r := make([]byte, 10)
	r[0], r[1] = 0x32, 0x08
	r[6] = state
	r[7] = pos1
	return r
}

type resp struct {
	Value        any
	ErrorNumber  int
	ErrorMessage string
}

func serve(t *testing.T, w *OasisWheel) string {
	t.Helper()
	srv := alpacadev.New(alpacadev.Config{
		Discovery: alpacadev.DiscoveryConfig{Mode: alpacadev.DiscoveryOff}, ServerName: "t", Manufacturer: "t",
	})
	if err := srv.Register(alpacadev.FilterWheelType, 0, w); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(http.HandlerFunc(srv.ServeHTTP))
	t.Cleanup(ts.Close)
	return ts.URL + "/api/v1/filterwheel/0/"
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

func newWheel(replies map[byte][]byte, slots int) *OasisWheel {
	w := NewOasisWheel(0)
	w.dev = oasisfw.New(&fakeHID{replies: replies}, oasisfw.DeviceInfo{})
	w.slots = slots
	w.names = make([]string, slots)
	w.offsets = make([]int, slots)
	for i := range w.names {
		w.names[i] = "Filter"
	}
	return w
}

func TestOasisWheelAlpaca(t *testing.T) {
	w := newWheel(map[byte][]byte{0x32: statusReply(0, 3)}, 5) // idle, wire pos 3 → ASCOM 2
	base := serve(t, w)

	if get(t, base, "connected").Value != true {
		t.Errorf("connected = false, want true")
	}
	if v, _ := get(t, base, "position").Value.(float64); int(v) != 2 {
		t.Errorf("position = %v, want 2 (0-based)", v)
	}
	if n, ok := get(t, base, "names").Value.([]any); !ok || len(n) != 5 {
		t.Errorf("names = %v, want 5 entries", get(t, base, "names").Value)
	}
	if o, ok := get(t, base, "focusoffsets").Value.([]any); !ok || len(o) != 5 {
		t.Errorf("focusoffsets = %v, want 5 entries", get(t, base, "focusoffsets").Value)
	}
	if r := put(t, base, "position", "Position=3"); r.ErrorNumber != 0 {
		t.Errorf("setposition(3): err %d (%s)", r.ErrorNumber, r.ErrorMessage)
	}
	if r := put(t, base, "position", "Position=9"); r.ErrorNumber != 0x401 {
		t.Errorf("setposition(9, over slots): err %d, want 0x401", r.ErrorNumber)
	}
}

func TestOasisWheelMoving(t *testing.T) {
	w := newWheel(map[byte][]byte{0x32: statusReply(1, 0)}, 5) // state 1 = moving
	base := serve(t, w)
	if v, _ := get(t, base, "position").Value.(float64); int(v) != -1 {
		t.Errorf("position = %v, want -1 while moving", v)
	}
}

// --- Action tests ---

func wcfgReply() []byte { // 0x30: speed 0, autorun 1, bt 0, turbo 0
	r := make([]byte, 10)
	r[0], r[1] = 0x30, 0x08
	r[2], r[3], r[4], r[5] = 0xff, 0xff, 0xff, 0xff
	r[7] = 1 // r[6]=speed, r[7]=autorun, r[8]=bluetoothOn, r[9]=turbo
	return r
}

func wverReply() []byte { // 0x02: hardware 2.4.0.0, firmware 1.7.1.0
	r := make([]byte, 36)
	r[0], r[1] = 0x02, 0x24
	r[6], r[7] = 0x02, 0x04                // hardware (data[4:8])
	r[10], r[11], r[12] = 0x01, 0x07, 0x01 // firmware (data[8:12])
	return r
}

func wmodelReply() []byte { // 0x01: "OasisFilterWheel"
	r := append([]byte{0x01, 0x20}, []byte("OasisFilterWheel")...)
	for len(r) < 34 {
		r = append(r, 0)
	}
	return r
}

func wtableReply(op byte) []byte { // 0x53/0x55: 8-entry int32 page table (data@+3)
	r := make([]byte, 36)
	r[0], r[1] = op, 0x22
	return r
}

func waction(t *testing.T, base, name, params string) resp {
	return put(t, base, "action", "Action="+name+"&Parameters="+params)
}

func TestOasisWheelActions(t *testing.T) {
	w := newWheel(map[byte][]byte{
		0x32: statusReply(0, 1),
		0x30: wcfgReply(),
		0x02: wverReply(),
		0x01: wmodelReply(),
		0x53: wtableReply(0x53), // focus-offset page (for setfocusoffset read-modify-write)
		0x55: wtableReply(0x55), // color page (for setcolor read-modify-write)
	}, 7)
	w.dev.Handshake()
	base := serve(t, w)

	sa := get(t, base, "supportedactions")
	names, _ := sa.Value.([]any)
	have := map[string]bool{}
	for _, n := range names {
		have[strings.ToLower(n.(string))] = true // advertised CamelCase; match case-insensitively
	}
	for _, x := range []string{"turbo", "setslotname", "firmwareversion", "config", "calibrate", "setcolor"} {
		if !have[x] {
			t.Errorf("supportedactions missing %q", x)
		}
	}
	// Config fields collapsed to single read/write actions; per-slot SetX kept (indexed).
	if have["setturbo"] || have["setspeed"] || have["setautorun"] {
		t.Error("config SetX actions should be collapsed into their read/write field action")
	}

	if r := waction(t, base, "firmwareversion", ""); r.Value != "1.7.1.0" {
		t.Errorf("firmwareversion = %v, want 1.7.1.0", r.Value)
	}
	if r := waction(t, base, "hardwareversion", ""); r.Value != "2.4.0.0" {
		t.Errorf("hardwareversion = %v, want 2.4.0.0", r.Value)
	}
	if r := waction(t, base, "model", ""); r.Value != "OasisFilterWheel" {
		t.Errorf("model = %v, want OasisFilterWheel", r.Value)
	}
	if r := waction(t, base, "autorun", ""); r.Value != "true" {
		t.Errorf("autorun = %v, want true", r.Value)
	}
	if r := waction(t, base, "config", ""); !strings.Contains(r.Value.(string), "autorun=1") {
		t.Errorf("config = %v, want autorun=1", r.Value)
	}
	for _, tc := range []struct{ name, params string }{
		{"turbo", "on"}, {"speed", "2"}, {"autorun", "off"}, // dual-mode fields: value writes
		{"setslotname", "1:Ha"}, {"setfocusoffset", "2:-150"}, {"setcolor", "0:00ff00"}, {"calibrate", ""},
	} {
		if r := waction(t, base, tc.name, tc.params); r.Value != "ok" {
			t.Errorf("%s(%q) = %v (err %d %s)", tc.name, tc.params, r.Value, r.ErrorNumber, r.ErrorMessage)
		}
	}
	// Dual-mode field reads back on empty params; read-only rejects a params value.
	if r := waction(t, base, "speed", ""); r.ErrorNumber != 0 || r.Value == "" {
		t.Errorf("speed read = %v (err %d), want a value", r.Value, r.ErrorNumber)
	}
	if r := waction(t, base, "serial", "xyz"); r.ErrorNumber != 0x401 {
		t.Errorf("serial(with params): err %d, want 0x401 (read-only)", r.ErrorNumber)
	}
	if r := waction(t, base, "speed", "x"); r.ErrorNumber != 0x401 {
		t.Errorf("speed(bad): err %d, want 0x401", r.ErrorNumber)
	}
	if r := waction(t, base, "setslotname", "noslot"); r.ErrorNumber != 0x401 {
		t.Errorf("setslotname(bad): err %d, want 0x401", r.ErrorNumber)
	}
	if r := waction(t, base, "factoryreset", ""); r.ErrorNumber != 0x401 {
		t.Errorf("factoryreset(guard): err %d, want 0x401", r.ErrorNumber)
	}
	if r := waction(t, base, "bogus", ""); r.ErrorNumber != 0x40c {
		t.Errorf("bogus: err %d, want 0x40c", r.ErrorNumber)
	}
}

// --- Tier-2 hardware end-to-end test (gated; opens the REAL wheel) ---
//
// Mirrors the host pattern (asiefw TestAlpacaHardware): open the real device through
// the driver's normal Open() lifecycle, serve it on a real Alpaca server behind httptest,
// and drive it through the full HTTP stack with a non-destructive move-and-restore.

func value(t *testing.T, base, member string) any {
	t.Helper()
	r := get(t, base, member)
	if r.ErrorNumber != 0 {
		t.Fatalf("%s: ErrorNumber=%d (%s)", member, r.ErrorNumber, r.ErrorMessage)
	}
	return r.Value
}

func decodeResp(t *testing.T, r *http.Response) resp {
	t.Helper()
	defer r.Body.Close()
	var out resp
	json.NewDecoder(r.Body).Decode(&out)
	return out
}

func waitConnected(t *testing.T, base string, want bool) {
	t.Helper()
	deadline := time.Now().Add(12 * time.Second) // allow a manageHardware retry cycle (3s) if the device is briefly busy
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

// newRealStack opens the REAL wheel via the driver's normal lifecycle and serves it.
func newRealStack(t *testing.T) (base, mgmt string) {
	t.Helper()
	w := NewOasisWheel(0) // default openDev = real openByIndex (platform transport)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()                      // stop manageHardware (its defer closes the device)
		w.Close(context.Background()) // and close synchronously so the handle is released now
	})
	if err := w.Open(ctx); err != nil {
		t.Fatal(err)
	}
	srv := alpacadev.New(alpacadev.Config{
		Discovery: alpacadev.DiscoveryConfig{Mode: alpacadev.DiscoveryOff}, ServerName: "oasisfw-test", Manufacturer: "test",
	})
	if err := srv.Register(alpacadev.FilterWheelType, 0, w); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(http.HandlerFunc(srv.ServeHTTP))
	t.Cleanup(ts.Close)
	return ts.URL + "/api/v1/filterwheel/0/", ts.URL + "/management/v1/"
}

func TestOasisWheelHardware(t *testing.T) {
	if os.Getenv("OASISFW_HARDWARE") == "" {
		t.Skip("set OASISFW_HARDWARE=1 (with a real Oasis filter wheel attached and free) to run the hardware e2e test")
	}
	base, mgmt := newRealStack(t)
	waitConnected(t, base, true)

	names, ok := value(t, base, "names").([]any)
	if !ok || len(names) == 0 {
		t.Fatalf("no filter names: %v", names)
	}
	slots := len(names)
	for _, a := range []string{"model", "firmwareversion", "temperature"} {
		r := waction(t, base, a, "")
		t.Logf("%s = %v (err %d %s)", a, r.Value, r.ErrorNumber, r.ErrorMessage)
	}

	if r, err := http.Get(mgmt + "configureddevices?" + txQ); err == nil {
		if arr, ok := decodeResp(t, r).Value.([]any); ok && len(arr) > 0 {
			t.Logf("uniqueid=%v", arr[0].(map[string]any)["UniqueID"])
		}
	}

	// Wait for the wheel to settle (a prior op may still be finishing) before sampling start.
	start := -1
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		if p := int(value(t, base, "position").(float64)); p >= 0 {
			start = p
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if start < 0 {
		t.Skip("wheel never became idle")
	}
	target := (start + 1) % slots
	t.Logf("moving slot %d -> %d", start, target)
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
