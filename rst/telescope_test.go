package driver

import (
	"errors"
	"testing"
	"time"

	alpacadev "github.com/mikefsq/goalpaca/server"
	"github.com/mikefsq/lx200/rst"
)

// parkedNumber returns the ASCOM error number in err, or 0 if it carries none.
func parkedNumber(err error) int {
	var ae *alpacadev.AlpacaError
	if errors.As(err, &ae) {
		return ae.Number
	}
	return 0
}

// parked builds a telescope whose cached state says parked, with no mount attached. The gate
// must fire before anything reaches the hardware, so a nil mount is the right fixture: if the
// guard is missing the member returns NotConnected instead, which the test can tell apart.
func parked() *Telescope {
	tel := NewTelescope("", "")
	tel.snap.atPark = true
	return tel
}

// ASCOM requires motion to be refused while parked. The Alpaca HTTP layer enforces this, but
// alpacahurd holds the driver directly and calls these as Go methods, which never touches that
// layer — so the guarantee has to exist here too.
func TestMotionIsRefusedWhileParked(t *testing.T) {
	for _, c := range []struct {
		name string
		call func(*Telescope) error
	}{
		{"SlewToCoordinatesAsync", func(t *Telescope) error { return t.SlewToCoordinatesAsync(3, 20) }},
		{"SlewToTargetAsync", func(t *Telescope) error { return t.SlewToTargetAsync() }},
		{"SlewToAltAzAsync", func(t *Telescope) error { return t.SlewToAltAzAsync(90, 45) }},
		{"SyncToCoordinates", func(t *Telescope) error { return t.SyncToCoordinates(3, 20) }},
		{"SyncToTarget", func(t *Telescope) error { return t.SyncToTarget() }},
		{"PulseGuide", func(t *Telescope) error { return t.PulseGuide(alpacadev.GuideNorth, 100) }},
		{"MoveAxis", func(t *Telescope) error { return t.MoveAxis(alpacadev.AxisPrimary, 1.0) }},
		{"SetTracking(true)", func(t *Telescope) error { return t.SetTracking(true) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := c.call(parked())
			if err == nil {
				t.Fatal("succeeded while parked")
			}
			if n := parkedNumber(err); n != alpacadev.ErrNumParked {
				t.Errorf("error = %v (ASCOM 0x%X); want Parked 0x%X", err, n, alpacadev.ErrNumParked)
			}
		})
	}
}

// Two motions are not motion. Stopping an axis and stopping tracking must work on a parked
// mount, or a client that parks with tracking left on has no way to settle it.
func TestStoppingIsAllowedWhileParked(t *testing.T) {
	for _, c := range []struct {
		name string
		call func(*Telescope) error
	}{
		{"MoveAxis rate 0", func(t *Telescope) error { return t.MoveAxis(alpacadev.AxisPrimary, 0) }},
		{"SetTracking(false)", func(t *Telescope) error { return t.SetTracking(false) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			// No mount attached, so NotConnected is the expected outcome — what matters is
			// that it is NOT refused as Parked.
			if n := parkedNumber(c.call(parked())); n == alpacadev.ErrNumParked {
				t.Error("refused as Parked; stopping must be allowed on a parked mount")
			}
		})
	}
}

// Zero is the stop command, not a rate the mount can be asked to move at. Advertising a range
// that starts at zero is a conformance error.
func TestAxisRatesMinimumIsNotZero(t *testing.T) {
	tel := NewTelescope("", "")
	for _, axis := range []alpacadev.TelescopeAxis{alpacadev.AxisPrimary, alpacadev.AxisSecondary} {
		rates := tel.AxisRates(axis)
		if len(rates) == 0 {
			t.Fatalf("axis %v: no rates advertised", axis)
		}
		for _, r := range rates {
			if r.Minimum <= 0 {
				t.Errorf("axis %v: Minimum = %v; must be greater than zero", axis, r.Minimum)
			}
			if r.Maximum < r.Minimum {
				t.Errorf("axis %v: Maximum %v below Minimum %v", axis, r.Maximum, r.Minimum)
			}
		}
	}
	if tel.AxisRates(alpacadev.TelescopeAxis(99)) == nil {
		t.Error("an unknown axis must return an empty slice, not nil")
	}
}

// The mount's tracking mode maps onto ASCOM's DriveRate, except for its custom rate, which
// ASCOM cannot express — that must report "no mapping" rather than silently claiming sidereal.
func TestDriveRateMapping(t *testing.T) {
	for _, c := range []struct {
		mode rst.TrackMode
		want alpacadev.DriveRate
		ok   bool
	}{
		{rst.TrackModeSidereal, alpacadev.DriveSidereal, true},
		{rst.TrackModeSolar, alpacadev.DriveSolar, true},
		{rst.TrackModeLunar, alpacadev.DriveLunar, true},
		{rst.TrackModeCustom, 0, false},
	} {
		got, ok := driveRateOf(c.mode)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("driveRateOf(%v) = %v, %v; want %v, %v", c.mode, got, ok, c.want, c.ok)
		}
	}
}

// UTCDate must report the MOUNT's clock or nothing
func TestUTCDateIsEmptyRatherThanHostTimeWhenTheMountCannotBeRead(t *testing.T) {
	tel := NewTelescope("", "") // no mount attached
	got := tel.UTCDate()
	if got == "" {
		return
	}
	t.Errorf("UTCDate = %q with no mount; want an empty string", got)
	if _, err := time.Parse(utcLayout, got); err == nil {
		t.Error("...and it parses as a timestamp, so a client would treat it as the mount's clock")
	}
}

// ASCOM requires DriverInfo and DriverVersion to be non-empty
func TestDriverInfoAndVersionAreSet(t *testing.T) {
	tel := NewTelescope("", "")
	if tel.DriverVersion() == "" {
		t.Error("DriverVersion is empty")
	}
	if tel.DriverInfo() == "" {
		t.Error("DriverInfo is empty")
	}
}
