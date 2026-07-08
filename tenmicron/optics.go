package driver

import (
	"encoding/json"
	"math"
	"sync"

	alpacadev "github.com/mikefsq/goalpaca/server"
)

// Optical-train parameters: the mount can't report them, so they come from config and
// can be updated at runtime via the setoptics Action. The fleet injects one holder
// (alpacadev.OpticsStore) that the INDI front-end (TELESCOPE_INFO) also reads; a
// standalone driver uses localOptics. Values are metres / m².

// localOptics is the default in-process OpticsStore.
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

// opticsParams is the setoptics Action payload. Lengths are millimetres (e.g.
// focal_length 1000, aperture 130); aperture_area is m². Every field is optional;
// present fields patch the current optics, guider_* default to the main scope (OAG).
type opticsParams struct {
	Aperture          *float64 `json:"aperture,omitempty"`            // mm
	ApertureArea      *float64 `json:"aperture_area,omitempty"`       // m²
	FocalLength       *float64 `json:"focal_length,omitempty"`        // mm
	GuiderAperture    *float64 `json:"guider_aperture,omitempty"`     // mm
	GuiderFocalLength *float64 `json:"guider_focal_length,omitempty"` // mm
}

// actionSetOptics applies the present payload fields to the optics holder, so
// ApertureDiameter/FocalLength (Alpaca) and INDI TELESCOPE_INFO report the new optical
// train. Does not touch the mount, so it works whether or not the mount is connected.
func (t *Telescope) actionSetOptics(params string) (string, error) {
	s := t.opticsStore()
	if params == "" { // read-only: return the current optics in the payload's shape
		ap, area, fl, gap, gfl := s.Optics()
		out, _ := json.Marshal(opticsParams{
			Aperture:          ptr(ap * 1000), // metres → mm
			ApertureArea:      ptr(area),
			FocalLength:       ptr(fl * 1000),
			GuiderAperture:    ptr(gap * 1000),
			GuiderFocalLength: ptr(gfl * 1000),
		})
		return string(out), nil
	}
	var p opticsParams
	if err := json.Unmarshal([]byte(params), &p); err != nil {
		return "", alpacadev.NewError(alpacadev.ErrNumInvalidValue, "setoptics: invalid JSON: "+err.Error())
	}
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
	if area == 0 && ap > 0 { // default circular aperture area
		r := ap / 2
		area = math.Pi * r * r
	}
	if gap == 0 { // guide scope defaults to the main scope
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
