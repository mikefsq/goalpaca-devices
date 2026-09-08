package driver

import (
	"context"
	"fmt"

	"github.com/mikefsq/goalpaca/registry"
	alpacadev "github.com/mikefsq/goalpaca/server"
	"github.com/mikefsq/goasi/eaf"
)

// Config contains device selection and settings.
type Config struct {
	Index  int    `json:"index,omitempty" alpaca:"label=Enumeration index,min=0,when=start,help=Bind the Nth attached unit; prefer Serial where the device has one"`
	Serial string `json:"serial,omitempty" alpaca:"label=Serial,when=start,help=Bind by serial (stable across replug and start-before-plug)"`
}

func init() {
	registry.Register(registry.Driver{
		Name:          "asieaf",
		Type:          alpacadev.FocuserType,
		Description:   "ZWO EAF focuser",
		ConfigExample: `{ "driver": "asieaf", "index": 0 }`,
		Config:        func() any { return &Config{} },
		// Serial before index: the EAF reports a HID serial that survives a replug, where the
		// index is only the order this enumeration happened to see.
		Identity: []string{"serial", "index"},
		Scan:     scanFocusers,
		New: func(spec registry.Spec) (alpacadev.Device, error) {
			var cfg Config
			if err := spec.Decode(&cfg); err != nil {
				return nil, err
			}
			d := NewASIFocuser(cfg.Index, cfg.Serial)
			if spec.Name != "" {
				d.DevName = spec.Name
			}
			return d, nil
		},
	})
}

// scanFocusers lists the attached EAF units.
//
// The serial comes from the USB descriptor, so nothing is opened and a focuser another process is
// driving is still listed with its identity intact.
func scanFocusers(ctx context.Context) ([]registry.Found, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	devs, err := eaf.Enumerate()
	if err != nil {
		return nil, err
	}
	out := make([]registry.Found, 0, len(devs))
	for i, d := range devs {
		vals := map[string]any{"index": i}
		label := d.Product
		if label == "" {
			label = "ZWO EAF"
		}
		if d.Serial != "" {
			vals["serial"] = d.Serial
			label = fmt.Sprintf("%s (serial %s)", label, d.Serial)
		}
		out = append(out, registry.Found{Label: label, Values: vals})
	}
	return out, nil
}
