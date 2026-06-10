package driver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	alpacadev "github.com/mikefsq/goalpaca/server"
	"github.com/mikefsq/pegasus-astro/focuscube"
)

const txQ = "ClientID=1&ClientTransactionID=1"

// fakeSerial is a scripted focuscube.Transport: ASCII "cmd\n" → "reply\n", with the
// same prefix match on ':' the library's own fake uses (so "M:5000" → "M:" reply).
type fakeSerial struct {
	mu      sync.Mutex
	replies map[string]string
	rbuf    []byte
}

func (f *fakeSerial) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cmd := strings.TrimRight(string(p), "\n")
	reply, ok := f.replies[cmd]
	if !ok {
		if i := strings.IndexByte(cmd, ':'); i >= 0 {
			reply, ok = f.replies[cmd[:i+1]]
		}
	}
	if ok {
		f.rbuf = append(f.rbuf, []byte(reply+"\n")...)
	}
	return len(p), nil
}

func (f *fakeSerial) Read(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.rbuf) == 0 {
		return 0, nil
	}
	n := copy(p, f.rbuf)
	f.rbuf = f.rbuf[n:]
	return n, nil
}

func (f *fakeSerial) Close() error { return nil }

type resp struct {
	Value        any
	ErrorNumber  int
	ErrorMessage string
}

func serve(t *testing.T, foc *PegasusFocuser) string {
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

func TestPegasusFocuserAlpaca(t *testing.T) {
	f := &fakeSerial{replies: map[string]string{
		"P": "1234", "I": "0", "T": "20.5", "M:": "M:1", "H": "H:1",
	}}
	foc := NewPegasusFocuser(0, 100000)
	foc.dev = focuscube.New(f, focuscube.DeviceInfo{Port: "fake"}) // inject (in-package)

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
	if v, _ := get(t, base, "position").Value.(float64); int(v) != 1234 {
		t.Errorf("position = %v, want 1234", v)
	}
	if get(t, base, "ismoving").Value != false {
		t.Errorf("ismoving = true, want false")
	}
	if v, _ := get(t, base, "temperature").Value.(float64); v != 20.5 {
		t.Errorf("temperature = %v, want 20.5", v)
	}
	if r := put(t, base, "move", "Position=5000"); r.ErrorNumber != 0 {
		t.Errorf("move: err %d (%s)", r.ErrorNumber, r.ErrorMessage)
	}
	if r := put(t, base, "move", "Position=200000"); r.ErrorNumber != 0x401 {
		t.Errorf("move(over max): err %d, want 0x401", r.ErrorNumber)
	}
	if r := put(t, base, "halt", ""); r.ErrorNumber != 0 {
		t.Errorf("halt: err %d (%s)", r.ErrorNumber, r.ErrorMessage)
	}
}
