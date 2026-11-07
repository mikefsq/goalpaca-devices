package driver

import (
	"time"

	pimountgps "github.com/mikefsq/pi_mount_gps"
)

// GPS sources, as reported by the GpsSource action.
const (
	gpsSrcGpsd = "gpsd" // the system gpsd, reading the receiver's UART
	gpsSrcI2C  = "i2c"  // the board's own read of the receiver over I2C
	gpsSrcNone = "none" // no current fix from either
)

// gpsFix is the fix the driver reports and feeds, with where it came from.
type gpsFix struct {
	fix   pimountgps.Fix
	age   time.Duration // since the fix was received
	valid bool          // a current position fix
	src   string
}

// currentFix picks the GPS source: gpsd when it has a recent fix, else the
// board's I2C receiver. With neither current it returns the I2C receiver's last
// fix, marked invalid, so the values stay readable. The weather board never opens the GPS receiver.
func (w *Driver) currentFix() gpsFix {
	if w.gpsd != nil {
		if fx, age, ok := w.gpsd.current(); ok {
			return gpsFix{fix: fx, age: age, valid: true, src: gpsSrcGpsd}
		}
	}
	w.gpsMu.Lock()
	defer w.gpsMu.Unlock()
	b := w.gpsBoard
	if b == nil {
		return gpsFix{src: gpsSrcNone}
	}
	fx, err := b.Fix()
	if err != nil { // the board's receiver was not opened
		return gpsFix{src: gpsSrcNone}
	}
	age, _ := b.FixAge()
	if b.FixStale() {
		return gpsFix{fix: fx, age: age, src: gpsSrcNone}
	}
	return gpsFix{fix: fx, age: age, valid: true, src: gpsSrcI2C}
}
