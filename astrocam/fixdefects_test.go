package driver

import (
	"encoding/json"
	"testing"

	"github.com/mikefsq/goalpaca/registry"
)

// Hot-pixel correction defaults ON, and a config overrides it only by naming the key.
//
// The distinction that needs pinning is "absent" versus "explicitly false": FixDefects is a plain
// bool, so both decode to false, and only key presence tells them apart. Get that wrong and either
// the default never applies or it can never be turned off — neither of which is visible without
// constructing a camera and looking.
func TestFixDefectsDefault(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want bool
	}{
		{"absent means on", `{"driver":"astrocam","serial":"2e19c40425000900","name":"ASI6200MM"}`, true},
		{"explicit false means off", `{"driver":"astrocam","serial":"aaaa","fixdefects":false}`, false},
		{"explicit true means on", `{"driver":"astrocam","serial":"aaaa","fixdefects":true}`, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dev, err := newFromRaw(t, tc.raw)
			if err != nil {
				t.Fatalf("construct: %v", err)
			}
			if got := dev.fixDefects; got != tc.want {
				t.Errorf("fixDefects = %v, want %v (config: %s)", got, tc.want, tc.raw)
			}
		})
	}
}

// newFromRaw builds a camera through the registered driver's New, the same path alpacahurd uses.
func newFromRaw(t *testing.T, raw string) (*PureASICamera, error) {
	t.Helper()
	d, ok := registry.Lookup("astrocam")
	if !ok {
		t.Fatal("astrocam driver not registered")
	}
	dev, err := d.New(registry.Spec{Raw: json.RawMessage(raw)})
	if err != nil {
		return nil, err
	}
	ic, ok := dev.(*indiCamera)
	if !ok {
		t.Fatalf("New returned %T, want *indiCamera", dev)
	}
	return ic.PureASICamera, nil
}
