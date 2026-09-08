package driver

import (
	"context"
	"fmt"

	"github.com/mikefsq/goalpaca/registry"
	alpacadev "github.com/mikefsq/goalpaca/server"
	"github.com/mikefsq/goasi/efw"
)

// Config contains device selection and settings.
type Config struct {
	Index          int    `json:"index,omitempty" alpaca:"label=Enumeration index,min=0,when=start,help=Bind the Nth attached unit; prefer Serial where the device has one"`
	Serial         string `json:"serial,omitempty" alpaca:"label=Serial,when=start,help=Bind by serial (stable across replug and start-before-plug)"`
	Unidirectional bool   `json:"unidirectional,omitempty" alpaca:"label=Unidirectional,help=Always rotate one way"`
}

func init() {
	registry.Register(registry.Driver{
		Name:          "asiefw",
		Type:          alpacadev.FilterWheelType,
		Description:   "ZWO EFW filter wheel",
		ConfigExample: `{ "driver": "asiefw", "index": 0 }`,
		Config:        func() any { return &Config{} },
		// The ZWO serial is not in the USB layer for an EFW, so it costs a brief open — and it is
		// still the value worth binding to, because the index moves when a wheel is replugged.
		Identity: []string{"serial", "index"},
		Scan:     scanWheels,
		New: func(spec registry.Spec) (alpacadev.Device, error) {
			var cfg Config
			if err := spec.Decode(&cfg); err != nil {
				return nil, err
			}
			d := NewASIFilterWheel(cfg.Index, cfg.Serial, cfg.Unidirectional)
			if spec.Name != "" {
				d.DevName = spec.Name
			}
			return d, nil
		},
	})
}

// scanWheels lists the attached EFW units with their slot counts.
//
// efw.List opens each wheel briefly because the ZWO serial is not in the USB descriptor, and
// releases it again. A wheel another process holds comes back with no serial rather than missing:
// an operator looking for the wheel they just connected needs to see it listed.
func scanWheels(ctx context.Context) ([]registry.Found, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	list, err := efw.List()
	if err != nil {
		return nil, err
	}
	out := make([]registry.Found, 0, len(list))
	for i, l := range list {
		vals := map[string]any{"index": i}
		label := fmt.Sprintf("ZWO EFW (%d slots)", l.Slots)
		if l.Serial != "" {
			vals["serial"] = l.Serial
			label = fmt.Sprintf("%s (serial %s)", label, l.Serial)
		} else {
			label += " (in use — serial unreadable)"
		}
		out = append(out, registry.Found{Label: label, Values: vals})
	}
	return out, nil
}
