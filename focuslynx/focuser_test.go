package driver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	alpacadev "github.com/mikefsq/goalpaca/server"
	"github.com/mikefsq/optec-astro/focuslynx"
)

const txQ = "ClientID=1&ClientTransactionID=1"

// fakeHub is a scripted focuslynx.Transport: it answers GETSTATUS/GETCONFIG with
// key=value…END blocks and ACKs action commands with "!", mirroring the library's
// own test hub.
type fakeHub struct {
	mu      sync.Mutex
	pos     int
	maxStep int
	moving  bool
	tcomp   bool
	out     []byte
}

func (f *fakeHub) Write(b []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cmd := string(b)
	switch {
	case strings.Contains(cmd, "GETSTATUS"):
		mv := "0"
		if f.moving {
			mv = "1"
		}
		f.out = append(f.out, []byte(fmt.Sprintf("!\r\nSTATUS1\r\nTemp(C) = +21.7\r\nCurr Pos = %07d\r\nIsMoving = %s\r\nTmpProbe = 1\r\nEND\r\n", f.pos, mv))...)
	case strings.Contains(cmd, "GETCONFIG"):
		tc := 0
		if f.tcomp {
			tc = 1
		}
		f.out = append(f.out, []byte(fmt.Sprintf("!\r\nCONFIG1\r\nMax Pos  = %07d\r\nTComp ON = %d\r\nEND\r\n", f.maxStep, tc))...)
	case strings.Contains(cmd, "SCTE"):
		f.tcomp = strings.Contains(cmd, "SCTE1")
		f.out = append(f.out, []byte("!\r\nSET\r\n")...)
	default: // MA / HALT / etc → "!" ack + one-word status line
		f.out = append(f.out, []byte("!\r\nM\r\n")...)
	}
	return len(b), nil
}

func (f *fakeHub) Read(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.out) == 0 {
		return 0, nil
	}
	n := copy(p, f.out)
	f.out = f.out[n:]
	return n, nil
}

func (f *fakeHub) Close() error { return nil }

type resp struct {
	Value        any
	ErrorNumber  int
	ErrorMessage string
}

func serve(t *testing.T, foc *OptecFocuser) string {
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

func TestTempCompAlpaca(t *testing.T) {
	f := &fakeHub{pos: 1000, maxStep: 100000}
	foc := NewOptecFocuser(0, 1)
	foc.hub = focuslynx.New(f, focuslynx.DeviceInfo{Port: "fake"})
	base := serve(t, foc)

	if get(t, base, "tempcompavailable").Value != true {
		t.Errorf("tempcompavailable = false, want true (probe present)")
	}
	if get(t, base, "tempcomp").Value != false {
		t.Errorf("tempcomp = true, want false initially")
	}
	if r := put(t, base, "tempcomp", "TempComp=true"); r.ErrorNumber != 0 {
		t.Fatalf("set tempcomp: err %d (%s)", r.ErrorNumber, r.ErrorMessage)
	}
	if get(t, base, "tempcomp").Value != true {
		t.Errorf("tempcomp = false after enable, want true")
	}
	if r := put(t, base, "tempcomp", "TempComp=false"); r.ErrorNumber != 0 {
		t.Fatalf("clear tempcomp: err %d (%s)", r.ErrorNumber, r.ErrorMessage)
	}
	if get(t, base, "tempcomp").Value != false {
		t.Errorf("tempcomp = true after disable, want false")
	}
}

func TestNicknameBindingDiscoversChannel(t *testing.T) {
	foc := NewOptecFocuserByNickname(0, "OAG focuser")
	if foc.ID != "FocusLynx-OAG focuser" {
		t.Errorf("ID = %q, want FocusLynx-OAG focuser", foc.ID)
	}
	// Stand in for OpenByNickname resolving the nickname to hub + channel 2.
	f := &fakeHub{pos: 42, maxStep: 90000}
	foc.openDev = func() (*focuslynx.Hub, int, error) {
		return focuslynx.New(f, focuslynx.DeviceInfo{Port: "fake"}), 2, nil
	}
	if !foc.tryAcquire() {
		t.Fatal("tryAcquire failed")
	}
	if foc.ch != 2 {
		t.Errorf("ch = %d, want 2 (channel discovered at open)", foc.ch)
	}
	if foc.maxStep != 90000 {
		t.Errorf("maxStep = %d, want 90000", foc.maxStep)
	}
}

func TestOptecFocuserAlpaca(t *testing.T) {
	f := &fakeHub{pos: 1234, maxStep: 112000, moving: false}
	foc := NewOptecFocuser(0, 1)
	foc.hub = focuslynx.New(f, focuslynx.DeviceInfo{Port: "fake", Baud: 115200})
	foc.maxStep = 112000 // cached at acquire in production

	base := serve(t, foc)

	if get(t, base, "connected").Value != true {
		t.Errorf("connected = false, want true")
	}
	if get(t, base, "absolute").Value != true {
		t.Errorf("absolute = false, want true")
	}
	if v, _ := get(t, base, "maxstep").Value.(float64); int(v) != 112000 {
		t.Errorf("maxstep = %v, want 112000", v)
	}
	if v, _ := get(t, base, "position").Value.(float64); int(v) != 1234 {
		t.Errorf("position = %v, want 1234", v)
	}
	if get(t, base, "ismoving").Value != false {
		t.Errorf("ismoving = true, want false")
	}
	if v, _ := get(t, base, "temperature").Value.(float64); v != 21.7 {
		t.Errorf("temperature = %v, want 21.7", v)
	}
	if r := put(t, base, "move", "Position=5000"); r.ErrorNumber != 0 {
		t.Errorf("move: err %d (%s)", r.ErrorNumber, r.ErrorMessage)
	}
	if r := put(t, base, "move", "Position=999999"); r.ErrorNumber != 0x401 {
		t.Errorf("move(over max): err %d, want 0x401", r.ErrorNumber)
	}
	if r := put(t, base, "halt", ""); r.ErrorNumber != 0 {
		t.Errorf("halt: err %d (%s)", r.ErrorNumber, r.ErrorMessage)
	}
}
