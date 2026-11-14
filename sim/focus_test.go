package driver

import (
	"testing"

	"github.com/mikefsq/goalpaca/registry"
	"github.com/mikefsq/goalpaca/sim"
)

// The herd focuser starts at best focus; moving off it widens stars symmetrically.
func TestHerdFocuserStartsInFocusAndDefocuses(t *testing.T) {
	drv, ok := registry.Lookup("sim-focuser")
	if !ok {
		t.Fatal("sim-focuser not registered")
	}
	dev, err := drv.New(registry.Spec{Raw: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	f := dev.(*sim.Focuser)
	if p, _ := f.Position(); p != focusBest {
		t.Fatalf("start position %d, want %d", p, focusBest)
	}
	if s := focus.sigma(); s != starSigma {
		t.Fatalf("in-focus sigma %.2f, want %.2f", s, starSigma)
	}
	at := func(pos int) float64 {
		focus.bind(func() (int, error) { return pos, nil })
		return focus.sigma()
	}
	defer focus.bind(f.Position)
	lo, hi := at(focusBest-1000), at(focusBest+1000)
	if lo != hi || lo < 4.5*starSigma || lo > 5.5*starSigma {
		t.Fatalf("±1000 sigma %.2f / %.2f, want equal and ≈5× %.1f", lo, hi, starSigma)
	}
	if at(focusBest-100) <= starSigma {
		t.Fatal("100 steps off focus does not widen stars")
	}
}
