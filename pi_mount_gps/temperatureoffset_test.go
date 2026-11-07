package driver

import (
	"encoding/json"
	"testing"
)

type heatingBoard struct {
	fakeBoard
	heat float64
}

func (b *heatingBoard) SetEnclosureHeating(v float64) error { b.heat = v; return nil }

func TestTemperatureOffsetConfigAndAction(t *testing.T) {
	var cfg Config
	if err := json.Unmarshal([]byte(`{"temperatureOffset":3.5}`), &cfg); err != nil {
		t.Fatal(err)
	}
	bc := boardCfg(cfg)
	if bc.EnclosureHeatingC != 3.5 || boardCfg(Config{}).EnclosureHeatingC != 0 {
		t.Fatal("config offset mapping")
	}
	w := NewDriver(bc, "")
	if got, err := w.Action("TemperatureOffset", ""); err != nil || got != "3.5" {
		t.Fatalf("read %q %v", got, err)
	}
	b := &heatingBoard{}
	w.board = b
	if got, err := w.Action("temperatureoffset", "5"); err != nil || got != "5" || b.heat != 5 {
		t.Fatalf("set %q %v heat %g", got, err, b.heat)
	}
	for _, bad := range []string{"-1", "NaN", "+Inf", "bad"} {
		if _, err := w.Action("TemperatureOffset", bad); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
	if got, _ := w.Action("TemperatureOffset", ""); got != "5" || b.heat != 5 {
		t.Fatal("invalid input changed value")
	}
	if got, err := w.Action("TemperatureOffset", "0"); err != nil || got != "0" || b.heat != 0 {
		t.Fatal("reset failed")
	}
}
