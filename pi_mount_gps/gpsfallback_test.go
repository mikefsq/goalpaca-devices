package driver

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestGPSFallbackLifecycle(t *testing.T) {
	now := time.Now()
	g := newGpsdClient("")
	g.now = func() time.Time { return now }
	g.apply([]byte(tpv))
	opens := 0
	var last *fakeBoard
	w := &Driver{gpsd: g, openGPS: func() (board, error) { opens++; last = fixedBoard(); return last, nil }}
	if err := w.reconcileGPSFallback(); err != nil {
		t.Fatal(err)
	}
	if opens != 0 {
		t.Fatal("healthy gpsd opened I2C GPS")
	}
	now = now.Add(gpsdFresh + time.Second)
	if err := w.reconcileGPSFallback(); err != nil {
		t.Fatal(err)
	}
	if opens != 1 || w.currentFix().src != gpsSrcI2C {
		t.Fatal("no fallback after stale gpsd")
	}
	w.reconcileGPSFallback()
	if opens != 1 {
		t.Fatal("opened GPS twice")
	}
	g.apply([]byte(tpv))
	w.reconcileGPSFallback()
	if !last.closed || w.gpsBoard != nil || w.currentFix().src != gpsSrcGpsd {
		t.Fatal("recovery did not stop I2C GPS")
	}
	now = now.Add(gpsdFresh + time.Second)
	w.reconcileGPSFallback()
	if opens != 2 {
		t.Fatal("second outage did not reopen GPS")
	}
	w.closeGPSFallback()
	if !last.closed || w.gpsBoard != nil {
		t.Fatal("shutdown left GPS open")
	}
}

func TestGPSFallbackStartupAndRetry(t *testing.T) {
	calls := 0
	w := &Driver{gpsd: newGpsdClient(""), openGPS: func() (board, error) { calls++; return nil, errors.New("absent") }}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w.manageGPSFallback(ctx)
	if calls != 0 {
		t.Fatal("canceled startup touched I2C")
	}
	for i := 0; i < 2; i++ {
		if err := w.reconcileGPSFallback(); err == nil || w.gpsBoard != nil {
			t.Fatal("failed open was retained")
		}
	}
	if calls != 2 {
		t.Fatal("failed open was not retried")
	}
	w.gpsd = nil
	w.openGPS = func() (board, error) { calls++; return fixedBoard(), nil }
	if err := w.reconcileGPSFallback(); err != nil || w.gpsBoard == nil {
		t.Fatal("gpsd off must enable fallback")
	}
	w.closeGPSFallback()
}
