package driver

import (
	"context"
	"fmt"

	"github.com/mikefsq/goalpaca/registry"
	alpacadev "github.com/mikefsq/goalpaca/server"
	scopehub "github.com/mikefsq/scope_hub/driver"
)

// Config selects the board. Port names live on board; clients set them with
// SetSwitchName.
type Config struct {
	Serial string `json:"serial,omitempty"   alpaca:"label=Serial,when=start,help=RP2350 unique ID of the board; empty selects the first board"`
}

func init() {
	registry.Register(registry.Driver{
		Name:          "scopehub",
		Type:          alpacadev.SwitchType,
		Description:   "scope_hub USB hub controller: 12 V outputs, USB-C port power, current monitors",
		Config:        func() any { return &Config{} },
		Identity:      []string{"serial"},
		Scan:          scanBoards,
		ConfigExample: `{ "driver": "scopehub", "serial": "8E70418DA7737E15" }`,
		New: func(spec registry.Spec) (alpacadev.Device, error) {
			var cfg Config
			if err := spec.Decode(&cfg); err != nil {
				return nil, err
			}
			d := NewSwitch(cfg)
			d.Instance = spec.Instance
			if spec.Name != "" {
				d.DevName = spec.Name
			}
			return d, nil
		},
		// Board sensors and GPS, from same entry and same open board.
		// "observingconditions": [{"enable": false}] turns it off.
		Extra: []registry.Kind{{
			Type:     alpacadev.ObservingConditionsType,
			MultiKey: "observingconditions",
			Default:  true,
			Config:   func() any { return &Config{} },
			New: func(spec registry.Spec) (alpacadev.Device, error) {
				var cfg Config
				if err := spec.Decode(&cfg); err != nil {
					return nil, err
				}
				o := NewObservingConditions(cfg)
				o.Instance = spec.Instance
				if spec.Name != "" {
					o.DevName = spec.Name
				}
				return o, nil
			},
		}},
	})
}

// scanBoards lists attached boards. Reads USB descriptors only; opens nothing.
func scanBoards(ctx context.Context) ([]registry.Found, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	devs, err := scopehub.List()
	if err != nil {
		return nil, err
	}
	out := make([]registry.Found, 0, len(devs))
	for _, d := range devs {
		out = append(out, registry.Found{
			Label:  fmt.Sprintf("%s (serial %s)", d.Product, d.Serial),
			Values: map[string]any{"serial": d.Serial},
		})
	}
	return out, nil
}
