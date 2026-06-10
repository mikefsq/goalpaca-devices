package driver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"
	"time"

	alpacadev "github.com/mikefsq/goalpaca/server"
)

// TestAlpacaHardware drives the full stack — HTTP client -> Alpaca server ->
// PegasusFocuser -> focuscube lib -> FTDI serial -> device — against a REAL Pegasus
// FocusCube. Gated by FOCUSCUBE_HARDWARE so CI/no-hardware runs skip it. It is
// non-destructive: the small test move returns to the start position. Foreground only —
// the httptest server and acquire goroutine are torn down on cleanup, so nothing is
// left running.
//
//	FOCUSCUBE_HARDWARE=1 go test -run TestAlpacaHardware -v ./...                          # index 0
//	FOCUSCUBE_HARDWARE=1 FOCUSCUBE_INDEX=1 FOCUSCUBE_MAXSTEP=200000 go test -run TestAlpacaHardware -v ./...
//	FOCUSCUBE_HARDWARE=1 FOCUSCUBE_SERIAL=PA4W71AP go test -run TestAlpacaHardware -v ./... # bind by USB serial
func TestAlpacaHardware(t *testing.T) {
	if os.Getenv("FOCUSCUBE_HARDWARE") == "" {
		t.Skip("set FOCUSCUBE_HARDWARE=1 (+ optional FOCUSCUBE_SERIAL / FOCUSCUBE_INDEX / FOCUSCUBE_MAXSTEP) with a real device attached")
	}

	index := envInt("FOCUSCUBE_INDEX", 0)
	maxStep := envInt("FOCUSCUBE_MAXSTEP", 100000)

	var dev *PegasusFocuser
	if s := os.Getenv("FOCUSCUBE_SERIAL"); s != "" {
		dev = NewPegasusFocuserBySerial(0, s, maxStep) // stable USB-serial binding
	} else {
		dev = NewPegasusFocuser(index, maxStep)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := dev.Open(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dev.Close(context.Background()) })

	srv := alpacadev.New(alpacadev.Config{
		Discovery: alpacadev.DiscoveryConfig{Mode: alpacadev.DiscoveryOff}, ServerName: "focuscube-hw", Manufacturer: "test",
	})
	if err := srv.Register(alpacadev.FocuserType, 0, dev); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(http.HandlerFunc(srv.ServeHTTP))
	t.Cleanup(ts.Close)
	base := ts.URL + "/api/v1/focuser/0/"

	waitConnected(t, base)

	// Read surface over HTTP.
	t.Logf("name=%v", value(t, base, "name"))
	t.Logf("absolute=%v maxstep=%v position=%v temperature=%v ismoving=%v",
		value(t, base, "absolute"), value(t, base, "maxstep"), value(t, base, "position"),
		value(t, base, "temperature"), value(t, base, "ismoving"))
	t.Logf("tempcompavailable=%v tempcomp=%v", value(t, base, "tempcompavailable"), value(t, base, "tempcomp"))

	// The FocusCube serial protocol has no on-device temperature compensation, so the
	// driver must report it unavailable and reject enabling it (the ASCOM contract).
	if get(t, base, "tempcompavailable").Value != false {
		t.Errorf("tempcompavailable = true, want false (FocusCube has no on-device temp comp)")
	}
	if r := put(t, base, "tempcomp", "TempComp=true"); r.ErrorNumber == 0 {
		t.Errorf("PUT tempcomp=true unexpectedly succeeded; want a NotImplemented error")
	}

	// Small absolute move over HTTP, then return to the start position.
	p0 := toInt(value(t, base, "position"))
	tgt := p0 + 150
	if r := put(t, base, "move", "Position="+strconv.Itoa(tgt)); r.ErrorNumber != 0 {
		t.Fatalf("PUT move=%d: err %d (%s)", tgt, r.ErrorNumber, r.ErrorMessage)
	}
	waitStill(t, base)
	if got := toInt(value(t, base, "position")); iabs(got-tgt) > 5 {
		t.Errorf("position after move = %d, want ~%d", got, tgt)
	}
	if r := put(t, base, "move", "Position="+strconv.Itoa(p0)); r.ErrorNumber != 0 { // restore
		t.Errorf("PUT move=%d (restore): err %d (%s)", p0, r.ErrorNumber, r.ErrorMessage)
	}
	waitStill(t, base)
	t.Logf("move e2e over HTTP: %d -> %d -> %d", p0, tgt, toInt(value(t, base, "position")))
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func waitConnected(t *testing.T, base string) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if get(t, base, "connected").Value == true {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("device never connected (no FocusCube acquired?)")
}

func waitStill(t *testing.T, base string) {
	t.Helper()
	// Wait for motion to begin (the device can report ismoving=false briefly after a
	// move is accepted), then for it to finish.
	for i := 0; i < 20; i++ {
		if get(t, base, "ismoving").Value == true {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	for i := 0; i < 600; i++ {
		if get(t, base, "ismoving").Value == false {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("focuser never stopped moving")
}

func value(t *testing.T, base, member string) any {
	t.Helper()
	return get(t, base, member).Value
}

func toInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	}
	return 0
}

func iabs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
