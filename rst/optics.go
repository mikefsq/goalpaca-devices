package driver

import (
	"encoding/json"
	"math"
	"sync"

	alpacadev "github.com/mikefsq/goalpaca/server"
)

// localOptics is the default in-process alpacadev.OpticsStore. The fleet injects a
// shared holder via UseOptics so the INDI front-end's TELESCOPE_INFO reports what an
// Alpaca setoptics Action sets; a standalone driver uses this default. Metres / m².
type localOptics struct {
	mu                     sync.Mutex
	ap, area, fl, gap, gfl float64
}

func (o *localOptics) Optics() (float64, float64, float64, float64, float64) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.ap, o.area, o.fl, o.gap, o.gfl
}

func (o *localOptics) SetOptics(ap, area, fl, gap, gfl float64) {
	o.mu.Lock()
	o.ap, o.area, o.fl, o.gap, o.gfl = ap, area, fl, gap, gfl
	o.mu.Unlock()
}

func (t *Telescope) opticsStore() alpacadev.OpticsStore {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.optics
}

// UseOptics replaces the optics holder with a shared one (the fleet injects a holder
// the INDI front-end reads too). Call before serving.
func (t *Telescope) UseOptics(s alpacadev.OpticsStore) {
	t.mu.Lock()
	t.optics = s
	t.mu.Unlock()
}

// ApertureDiameter / ApertureArea / FocalLength override the BaseTelescope zero
// defaults, reading the optics holder (metres / m²).
// ApertureDiameter returns the objective aperture diameter in metres.
func (t *Telescope) ApertureDiameter() float64 { ap, _, _, _, _ := t.opticsStore().Optics(); return ap }

// ApertureArea returns the effective light-collecting area in square metres.
func (t *Telescope) ApertureArea() float64 { _, a, _, _, _ := t.opticsStore().Optics(); return a }

// FocalLength returns the focal length in metres.
func (t *Telescope) FocalLength() float64 { _, _, fl, _, _ := t.opticsStore().Optics(); return fl }

// SetOptics configures the instrument-profile optics (metres / m²); a zero area
// defaults to the circular aperture area. The guide scope defaults to the main scope
// (OAG); a separate guide scope is set via the setoptics Action's guider_* fields.
func (t *Telescope) SetOptics(diameterMeters, areaSqMeters, focalLengthMeters float64) {
	if areaSqMeters == 0 && diameterMeters > 0 {
		r := diameterMeters / 2
		areaSqMeters = math.Pi * r * r
	}
	t.opticsStore().SetOptics(diameterMeters, areaSqMeters, focalLengthMeters, diameterMeters, focalLengthMeters)
}

// setoptics is registered as an Action in actions.go (with the rest of the RST
// custom actions); actionSetOptics is its handler.

// opticsParams is the setoptics payload. Lengths are millimetres (focal_length,
// aperture, guider_*); aperture_area is m². Present fields patch the current optics,
// guider_* default to the main scope.
type opticsParams struct {
	Aperture          *float64 `json:"aperture,omitempty"`            // mm
	ApertureArea      *float64 `json:"aperture_area,omitempty"`       // m²
	FocalLength       *float64 `json:"focal_length,omitempty"`        // mm
	GuiderAperture    *float64 `json:"guider_aperture,omitempty"`     // mm
	GuiderFocalLength *float64 `json:"guider_focal_length,omitempty"` // mm
}

func (t *Telescope) actionSetOptics(params string) (string, error) {
	var p opticsParams
	if err := json.Unmarshal([]byte(params), &p); err != nil {
		return "", alpacadev.NewError(alpacadev.ErrNumInvalidValue, "setoptics: invalid JSON: "+err.Error())
	}
	s := t.opticsStore()
	ap, area, fl, gap, gfl := s.Optics()

	var applied []string
	if p.Aperture != nil { // mm → metres (the holder/ASCOM unit)
		ap, applied = *p.Aperture/1000, append(applied, "aperture")
	}
	if p.ApertureArea != nil {
		area, applied = *p.ApertureArea, append(applied, "aperture_area")
	}
	if p.FocalLength != nil {
		fl, applied = *p.FocalLength/1000, append(applied, "focal_length")
	}
	if p.GuiderAperture != nil {
		gap, applied = *p.GuiderAperture/1000, append(applied, "guider_aperture")
	}
	if p.GuiderFocalLength != nil {
		gfl, applied = *p.GuiderFocalLength/1000, append(applied, "guider_focal_length")
	}
	if area == 0 && ap > 0 {
		r := ap / 2
		area = math.Pi * r * r
	}
	if gap == 0 {
		gap = ap
	}
	if gfl == 0 {
		gfl = fl
	}
	s.SetOptics(ap, area, fl, gap, gfl)

	out, _ := json.Marshal(struct {
		Applied []string `json:"applied"`
	}{Applied: applied})
	return string(out), nil
}
