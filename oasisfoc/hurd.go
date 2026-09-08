package driver

import (
	"context"
	"fmt"

	"github.com/mikefsq/goalpaca/registry"
	alpacadev "github.com/mikefsq/goalpaca/server"
	"github.com/mikefsq/oasis-astro/oasisfoc"
)

// Config contains device selection and settings.
type Config struct {
	Index int `json:"index,omitempty" alpaca:"label=Enumeration index,min=0,when=start,help=Bind the Nth attached unit; prefer Serial where the device has one"`
}

func init() {
	registry.Register(registry.Driver{
		Name:          "oasisfoc",
		Type:          alpacadev.FocuserType,
		Description:   "Astroasis Oasis focuser",
		ConfigExample: `{ "driver": "oasisfoc", "index": 0 }`,
		Config:        func() any { return &Config{} },
		// The index is all this driver binds by, so it is the whole identity — which is why a scan
		// is worth having: an operator picks the unit from a list rather than guessing a number.
		Identity: []string{"index"},
		Scan:     scanFocusers,
		New: func(spec registry.Spec) (alpacadev.Device, error) {
			var cfg Config
			if err := spec.Decode(&cfg); err != nil {
				return nil, err
			}
			d := NewOasisFocuser(cfg.Index)
			if spec.Name != "" {
				d.DevName = spec.Name
			}
			return d, nil
		},
	})
}

// scanFocusers lists the attached Oasis units.
//
// The serial is in the USB descriptor, so nothing is opened. It goes in the LABEL rather than into
// the config: this driver binds by enumeration index, so a serial in the entry would be a key it
// never reads. Shown anyway, because it is what tells two identical units apart while choosing.
func scanFocusers(ctx context.Context) ([]registry.Found, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	devs, err := oasisfoc.Enumerate()
	if err != nil {
		return nil, err
	}
	out := make([]registry.Found, 0, len(devs))
	for i, d := range devs {
		label := d.Product
		if label == "" {
			label = "Oasis focuser"
		}
		if d.Serial != "" {
			label = fmt.Sprintf("%s (serial %s)", label, d.Serial)
		}
		out = append(out, registry.Found{Label: label, Values: map[string]any{"index": i}})
	}
	return out, nil
}
