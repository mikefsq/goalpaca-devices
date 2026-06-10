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
// OptecFocuser -> focuslynx lib -> serial -> device — against a REAL FocusLynx /
// ThirdLynx. Gated by FOCUSLYNX_HARDWARE so CI/no-hardware runs skip it. It is
// non-destructive: temp compensation is restored and the small test move returns to
// the start position. Foreground only — the httptest server and acquire goroutine are
// torn down on cleanup, so nothing is left running.
//
//	FOCUSLYNX_HARDWARE=1 FOCUSLYNX_NICKNAME="QuickSync FTX40" go test -run TestAlpacaHardware -v ./...
//	FOCUSLYNX_HARDWARE=1 go test -run TestAlpacaHardware -v ./...   # binds hub index 0, channel 1
func TestAlpacaHardware(t *testing.T) {
	if os.Getenv("FOCUSLYNX_HARDWARE") == "" {
		t.Skip("set FOCUSLYNX_HARDWARE=1 (+ optional FOCUSLYNX_NICKNAME) with a real device attached")
	}

	var dev *OptecFocuser
	if nk := os.Getenv("FOCUSLYNX_NICKNAME"); nk != "" {
		dev = NewOptecFocuserByNickname(0, nk)
	} else {
		dev = NewOptecFocuser(0, 1)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := dev.Open(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dev.Close(context.Background()) })

	srv := alpacadev.New(alpacadev.Config{
		Discovery: alpacadev.DiscoveryConfig{Mode: alpacadev.DiscoveryOff}, ServerName: "focuslynx-hw", Manufacturer: "test",
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

	// Temperature-compensation round-trip over HTTP.
	if get(t, base, "tempcompavailable").Value != true {
		t.Errorf("tempcompavailable = false, want true (probe present)")
	}
	if r := put(t, base, "tempcomp", "TempComp=true"); r.ErrorNumber != 0 {
		t.Fatalf("PUT tempcomp=true: err %d (%s)", r.ErrorNumber, r.ErrorMessage)
	}
	if get(t, base, "tempcomp").Value != true {
		t.Errorf("tempcomp != true after enabling over HTTP")
	}
	if r := put(t, base, "tempcomp", "TempComp=false"); r.ErrorNumber != 0 { // restore
		t.Errorf("PUT tempcomp=false (restore): err %d (%s)", r.ErrorNumber, r.ErrorMessage)
	}
	if get(t, base, "tempcomp").Value != false {
		t.Errorf("tempcomp != false after disabling over HTTP")
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

func waitConnected(t *testing.T, base string) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if get(t, base, "connected").Value == true {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("device never connected (no FocusLynx acquired?)")
}

func waitStill(t *testing.T, base string) {
	t.Helper()
	// Wait for motion to begin (the hub reports ismoving=false briefly after a move is
	// accepted), then for it to finish.
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
