package driver

import (
	"time"

	alpacadev "github.com/mikefsq/goalpaca/server"
	"github.com/mikefsq/lx200/am5"
)

// The AM5 features that ASCOM already has a home for.
//
// These are here rather than in the Action surface on purpose. An Action is for what no standard
// member covers; putting a reading ASCOM defines behind a custom name means every client that
// knows the standard one gets the wrong answer from the driver. Three were being answered by
// BaseTelescope's defaults, which for an AM5 are wrong or empty.

// siderealDegPerSec matches the RST driver's guide-rate math (15"/s), so the two mounts report
// the same units for the same setting.
const siderealDegPerSec = 15.0 / 3600.0

// AlignmentMode reports how the mount is mounted, read from the mount rather than assumed.
//
// BaseTelescope answers AlignGermanPolar unconditionally, which is right for an AM5 on a wedge
// and wrong for the same mount in AltAz — a mode the operator can change at any time with one
// command (:AA#/:AP#, see the MountMode action). A client that trusts it decides how to flip and
// which axes to drive, so the wrong answer is not cosmetic.
//
// A disconnected mount reports German polar: the property has no error channel, and the AM series
// ships equatorial.
func (t *Telescope) AlignmentMode() alpacadev.AlignmentMode {
	m := t.mount()
	if m == nil {
		return alpacadev.AlignGermanPolar
	}
	mode, err := m.MountMode()
	if err != nil {
		return alpacadev.AlignGermanPolar
	}
	if mode == am5.ModeAltAz {
		return alpacadev.AlignAltAz
	}
	return alpacadev.AlignGermanPolar
}

// Guide rate — ASCOM exposes it per axis in deg/s; the AM5 has a single ×sidereal rate shared by
// both (:Ggr# / :Rg), so both properties read and write the one mount value.

// CanSetGuideRates reports that the guide rate can be set.
func (t *Telescope) CanSetGuideRates() bool { return true }

// GuideRateRightAscension returns the guide rate in degrees/second.
func (t *Telescope) GuideRateRightAscension() float64 { return t.guideRateDegPerSec() }

// GuideRateDeclination returns the guide rate in degrees/second (one rate, shared).
func (t *Telescope) GuideRateDeclination() float64 { return t.guideRateDegPerSec() }

// SetGuideRateRightAscension sets the guide rate in degrees/second (one rate, shared).
func (t *Telescope) SetGuideRateRightAscension(degPerSec float64) error {
	return t.setGuideRateDegPerSec(degPerSec)
}

// SetGuideRateDeclination sets the guide rate in degrees/second (one rate, shared).
func (t *Telescope) SetGuideRateDeclination(degPerSec float64) error {
	return t.setGuideRateDegPerSec(degPerSec)
}

func (t *Telescope) guideRateDegPerSec() float64 {
	m := t.mount()
	if m == nil {
		return 0
	}
	x, err := m.GuideRate() // ×sidereal
	if err != nil {
		return 0
	}
	return x * siderealDegPerSec
}

func (t *Telescope) setGuideRateDegPerSec(degPerSec float64) error {
	if degPerSec < 0 {
		return alpacadev.ErrInvalidValue
	}
	m := t.mount()
	if m == nil {
		return alpacadev.ErrNotConnected
	}
	return m.SetGuideRate(degPerSec / siderealDegPerSec)
}

// UTCDate reports the MOUNT's clock, not this machine's.
//
// It used to return time.Now(), which answers a different question: ASCOM defines UTCDate as the
// device's own time, and the whole reason a client reads it is to find out whether the mount
// agrees with the host. An AltAz mount derives its pointing from that clock, so a driver that
// reports the host's time hides exactly the fault the property exists to expose.
//
// The host's time is still the fallback, because the property cannot report an error and a
// plausible wrong answer beats an empty one — but only when the mount cannot be asked.
func (t *Telescope) UTCDate() string {
	if m := t.mount(); m != nil {
		if clk, err := m.Clock(); err == nil {
			return clk.UTC().Format("2006-01-02T15:04:05.000Z")
		}
	}
	return time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
}
