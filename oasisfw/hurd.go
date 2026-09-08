package driver

import (
	"context"
	"fmt"

	"github.com/mikefsq/goalpaca/registry"
	alpacadev "github.com/mikefsq/goalpaca/server"
	"github.com/mikefsq/oasis-astro/oasisfw"
)

// Config contains device selection and settings.
type Config struct {
	Index int `json:"index,omitempty" alpaca:"label=Enumeration index,min=0,when=start,help=Bind the Nth attached unit; prefer Serial where the device has one"`
}

func init() {
	registry.Register(registry.Driver{
		Name:          "oasisfw",
		Type:          alpacadev.FilterWheelType,
		Description:   "Astroasis Oasis filter wheel",
		ConfigExample: `{ "driver": "oasisfw", "index": 0 }`,
		Config:        func() any { return &Config{} },
		// See oasisfoc: the index is the whole identity this driver binds by.
		Identity: []string{"index"},
		Scan:     scanWheels,
		New: func(spec registry.Spec) (alpacadev.Device, error) {
			var cfg Config
			if err := spec.Decode(&cfg); err != nil {
				return nil, err
			}
			d := NewOasisWheel(cfg.Index)
			if spec.Name != "" {
				d.DevName = spec.Name
			}
			return d, nil
		},
	})
}

// scanWheels lists the attached Oasis units.
//
// The serial is in the USB descriptor, so nothing is opened. It goes in the LABEL rather than into
// the config: this driver binds by enumeration index, so a serial in the entry would be a key it
// never reads. Shown anyway, because it is what tells two identical units apart while choosing.
func scanWheels(ctx context.Context) ([]registry.Found, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	devs, err := oasisfw.Enumerate()
	if err != nil {
		return nil, err
	}
	out := make([]registry.Found, 0, len(devs))
	for i, d := range devs {
		label := d.Product
		if label == "" {
			label = "Oasis filter wheel"
		}
		if d.Serial != "" {
			label = fmt.Sprintf("%s (serial %s)", label, d.Serial)
		}
		out = append(out, registry.Found{Label: label, Values: map[string]any{"index": i}})
	}
	return out, nil
}
