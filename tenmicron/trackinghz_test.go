package driver

import (
	"math"
	"testing"

	alpacadev "github.com/mikefsq/goalpaca/server"
)

// TestApplyTrackingHz walks the :GT# readings this mount has produced and the transitions between
// them, from a driver that has set nothing (sidereal, no offset).
func TestApplyTrackingHz(t *testing.T) {
	tel := &Telescope{}
	steps := []struct {
		name   string
		hz     float64
		rate   alpacadev.DriveRate
		raRate float64
	}{
		{"keypad custom 10\"/s", 40.0, alpacadev.DriveSidereal, 10/siderealArcsecPerSec - 1},
		{":RT1# solar", 60.3, alpacadev.DriveSolar, 0},
		{"keypad sidereal, rounded", 60.2, alpacadev.DriveSidereal, 0},
		{"sidereal, truncated", 60.1, alpacadev.DriveSidereal, 0},
		{":RT0# lunar", 62.4, alpacadev.DriveLunar, 0},
		{"tracking off leaves lunar", 0, alpacadev.DriveLunar, 0},
		{":RT2# sidereal after lunar", 60.2, alpacadev.DriveSidereal, 0},
		{":RT1# solar after sidereal", 60.3, alpacadev.DriveSolar, 0},
		{":RT2# sidereal after solar", 60.2, alpacadev.DriveSidereal, 0},
	}
	for _, s := range steps {
		tel.applyTrackingHzLocked(s.hz)
		if tel.trackingRate != s.rate || math.Abs(tel.raRate-s.raRate) > 1e-9 {
			t.Errorf("%s (%.1f Hz): rate %v raRate %.6f, want %v %.6f",
				s.name, s.hz, tel.trackingRate, tel.raRate, s.rate, s.raRate)
		}
	}
}

// TestApplyTrackingHzKeepsSmallClientOffset: an offset a client wrote reads back exactly, although
// :GT# can only resolve it to 0.025"/s.
func TestApplyTrackingHzKeepsSmallClientOffset(t *testing.T) {
	tel := &Telescope{raRate: 0.0005} // what SetRightAscensionRate stores
	tel.applyTrackingHzLocked(60.2)   // 15.0486"/s rounds to the same one-decimal reading as sidereal
	if tel.raRate != 0.0005 {
		t.Fatalf("raRate %.6f, want the client's 0.000500", tel.raRate)
	}
}

// TestSnapToKeypadPreset: sidereal-based presets land on the keypad's 15.0"/s values; other rates
// pass through.
func TestSnapToKeypadPreset(t *testing.T) {
	for _, c := range []struct{ in, want float64 }{
		{0.5 * siderealArcsecPerSec, 7.5},
		{0.33 * siderealArcsecPerSec, 4.95},
		{1.0 * siderealArcsecPerSec, 15.0},
		{7.5, 7.5},
		{6.0, 6.0},
	} {
		if got := snapToKeypadPreset(c.in); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("snapToKeypadPreset(%.4f) = %.4f, want %.4f", c.in, got, c.want)
		}
	}
}
