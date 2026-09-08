package driver

import (
	"context"
	"fmt"

	"github.com/mikefsq/goalpaca/registry"
	alpacadev "github.com/mikefsq/goalpaca/server"
	"github.com/mikefsq/pegasus-astro/focuscube"
)

// Config contains device selection and settings.
type Config struct {
	Index   int    `json:"index,omitempty" alpaca:"label=Enumeration index,min=0,when=start,help=Bind the Nth attached unit; prefer Serial where the device has one"`
	Serial  string `json:"serial,omitempty" alpaca:"label=Serial,when=start,help=Bind by serial (stable across replug and start-before-plug)"`
	MaxStep int    `json:"maxstep,omitempty" alpaca:"label=Max step,min=0,when=start,help=Travel in steps; the device does not report it"`
}

func init() {
	registry.Register(registry.Driver{
		Name:          "focuscube",
		Type:          alpacadev.FocuserType,
		Description:   "Pegasus Astro FocusCube focuser",
		ConfigExample: `{ "driver": "focuscube", "index": 0, "maxstep": 100000 }`,
		Config:        func() any { return &Config{} },
		// The FTDI serial survives a replug; the enumeration index does not.
		Identity: []string{"serial", "index"},
		Scan:     scanFocusers,
		New: func(spec registry.Spec) (alpacadev.Device, error) {
			var cfg Config
			if err := spec.Decode(&cfg); err != nil {
				return nil, err
			}
			maxStep := cfg.MaxStep
			if maxStep == 0 {
				maxStep = 100000
			}
			// Prefer the stable USB-serial binding when given; fall back to enumeration index.
			var d *PegasusFocuser
			if cfg.Serial != "" {
				d = NewPegasusFocuserBySerial(cfg.Index, cfg.Serial, maxStep)
			} else {
				d = NewPegasusFocuser(cfg.Index, maxStep)
			}
			if spec.Name != "" {
				d.DevName = spec.Name
			}
			return d, nil
		},
	})
}

// scanFocusers lists the attached FocusCube units.
//
// Ports come from the OS enumerator, so nothing is opened and a focuser another process is driving
// is still listed with its serial.
func scanFocusers(ctx context.Context) ([]registry.Found, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	devs, err := focuscube.Enumerate()
	if err != nil {
		return nil, err
	}
	out := make([]registry.Found, 0, len(devs))
	for i, d := range devs {
		vals := map[string]any{"index": i}
		label := d.Product
		if label == "" {
			label = "Pegasus FocusCube"
		}
		label = fmt.Sprintf("%s on %s", label, d.Port)
		if d.Serial != "" {
			vals["serial"] = d.Serial
			label = fmt.Sprintf("%s (serial %s)", label, d.Serial)
		}
		out = append(out, registry.Found{Label: label, Values: vals})
	}
	return out, nil
}
