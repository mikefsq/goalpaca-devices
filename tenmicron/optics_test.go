package driver

import "testing"

func TestSetOpticsAction(t *testing.T) {
	tel := NewTelescope("x")
	// Action takes mm; ASCOM getters report metres.
	if _, err := tel.Action("setoptics", `{"aperture":200,"focal_length":1600}`); err != nil {
		t.Fatalf("setoptics: %v", err)
	}
	if tel.ApertureDiameter() != 0.2 || tel.FocalLength() != 1.6 {
		t.Errorf("ApertureDiameter=%v FocalLength=%v want 0.2/1.6 (metres)", tel.ApertureDiameter(), tel.FocalLength())
	}
	if tel.ApertureArea() == 0 {
		t.Error("aperture area should default from diameter")
	}
}

// UseOptics injects a shared holder; setoptics must write through to it (this is
// what lets the INDI front-end report what was set over Alpaca). guider_* defaults
// to the main scope when omitted.
func TestUseOpticsSharedHolder(t *testing.T) {
	tel := NewTelescope("x")
	h := &localOptics{}
	tel.UseOptics(h)
	if _, err := tel.Action("setoptics", `{"aperture":150,"focal_length":1000,"guider_focal_length":240}`); err != nil {
		t.Fatalf("setoptics: %v", err)
	}
	ap, _, fl, gap, gfl := h.Optics() // holder is metres
	if ap != 0.15 || fl != 1.0 || gap != 0.15 || gfl != 0.24 {
		t.Errorf("holder = ap%v fl%v gap%v gfl%v want 0.15/1.0/0.15/0.24 (metres)", ap, fl, gap, gfl)
	}
}

func TestSetOpticsInvalidJSON(t *testing.T) {
	tel := NewTelescope("x")
	if _, err := tel.Action("setoptics", `{nope}`); err == nil {
		t.Error("invalid JSON should error")
	}
}
