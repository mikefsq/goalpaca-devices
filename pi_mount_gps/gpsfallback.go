package driver

import (
	"context"
	"log"
	"time"
)

// Give gpsd time to deliver its first report before touching the I2C receiver.
// Weather acquisition runs independently throughout startup and GPS failover.
func (w *Driver) manageGPSFallback(ctx context.Context) {
	defer w.closeGPSFallback()
	if w.gpsd != nil {
		sleepCtx(ctx, gpsdFresh)
	}
	for ctx.Err() == nil {
		if err := w.reconcileGPSFallback(); err != nil {
			log.Printf("pi_mount_gps: opening I2C GPS fallback: %v", err)
			sleepCtx(ctx, 10*time.Second)
		} else {
			sleepCtx(ctx, time.Second)
		}
	}
}

func (w *Driver) closeGPSFallback() {
	w.gpsMu.Lock()
	defer w.gpsMu.Unlock()
	if w.gpsBoard != nil {
		w.gpsBoard.Close() // stops the receiver polling loop before closing its handle
		w.gpsBoard = nil
	}
}

func (w *Driver) reconcileGPSFallback() error {
	if w.gpsd != nil {
		if _, _, ok := w.gpsd.current(); ok {
			w.closeGPSFallback()
			return nil
		}
	}
	w.gpsMu.Lock()
	defer w.gpsMu.Unlock()
	if w.gpsBoard != nil {
		return nil
	}
	b, err := w.openGPS()
	if err != nil {
		return err
	}
	w.gpsBoard = b
	return nil
}
