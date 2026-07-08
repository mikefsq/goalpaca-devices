package driver

import (
	"strings"
	"testing"
)

func TestSetOpticsAction(t *testing.T) {
	tel := NewTelescope("")
	// Action takes mm; ASCOM getters report metres.
	if _, err := tel.Action("setoptics", `{"aperture":130,"focal_length":715}`); err != nil {
		t.Fatalf("setoptics: %v", err)
	}
	if tel.ApertureDiameter() != 0.13 || tel.FocalLength() != 0.715 {
		t.Errorf("ApertureDiameter=%v FocalLength=%v want 0.13/0.715 (metres)", tel.ApertureDiameter(), tel.FocalLength())
	}
	if tel.ApertureArea() == 0 {
		t.Error("aperture area should default from diameter")
	}
}

func TestUseOpticsSharedHolder(t *testing.T) {
	tel := NewTelescope("")
	h := &localOptics{}
	tel.UseOptics(h)
	if _, err := tel.Action("setoptics", `{"aperture":130,"focal_length":715,"guider_focal_length":200}`); err != nil {
		t.Fatalf("setoptics: %v", err)
	}
	ap, _, fl, gap, gfl := h.Optics() // holder is metres
	if ap != 0.13 || fl != 0.715 || gap != 0.13 || gfl != 0.2 {
		t.Errorf("holder = ap%v fl%v gap%v gfl%v want 0.13/0.715/0.13/0.2 (metres)", ap, fl, gap, gfl)
	}
}

func TestSupportedActionsHasSetOptics(t *testing.T) {
	tel := NewTelescope("")
	var found bool
	for _, a := range tel.SupportedActions() {
		if strings.EqualFold(a, "setoptics") { // advertised CamelCase ("SetOptics"), matched case-insensitively
			found = true
		}
	}
	if !found {
		t.Error("SupportedActions should include a SetOptics action")
	}
}
