package driver

import (
	"context"
	"fmt"

	"github.com/mikefsq/goalpaca/registry"
	alpacadev "github.com/mikefsq/goalpaca/server"
	"github.com/mikefsq/unihedron"
)

// Config contains device selection and settings.
type Config struct {
	Index  int    `json:"index,omitempty" alpaca:"label=Enumeration index,min=0,when=start,help=Bind the Nth attached unit; prefer Serial where the device has one"`
	Serial string `json:"serial,omitempty" alpaca:"label=Serial,when=start,help=Bind by serial (stable across replug and start-before-plug)"`
}

func init() {
	registry.Register(registry.Driver{
		Name:          "unihedron",
		Type:          alpacadev.ObservingConditionsType,
		Description:   "Unihedron SQM sky-quality meter",
		ConfigExample: `{ "driver": "unihedron", "index": 0 }`,
		Config:        func() any { return &Config{} },
		// The FTDI serial survives a replug; the enumeration index does not, and the port path
		// renumbers when devices are plugged in a different order.
		Identity: []string{"serial", "index"},
		Scan:     scanDevices,
		New: func(spec registry.Spec) (alpacadev.Device, error) {
			var cfg Config
			if err := spec.Decode(&cfg); err != nil {
				return nil, err
			}
			// Prefer the stable USB-bridge serial when given; otherwise bind by
			// enumeration index.
			var d *SQM
			if cfg.Serial != "" {
				d = NewSQMBySerial(cfg.Index, cfg.Serial)
			} else {
				d = NewSQM(cfg.Index)
			}
			if spec.Name != "" {
				d.DevName = spec.Name
			}
			return d, nil
		},
	})
}

// scanDevices lists the attached SQM meters.
//
// unihedron.Discover probes each candidate port with `ix` and keeps the ones that answer as a
// meter, closing the port again. Enumerating ports alone would not do: 0403:6001 is the same FTDI
// bridge an MGPBox and half the other instruments on a rig use, so a listing by descriptor reports
// every one of them as an SQM.
//
// The value stored is the USB-BRIDGE serial, which is what OpenBySerial matches. The meter's own
// serial number goes in the label, because that is the number printed on the unit and the one an
// operator recognises.
func scanDevices(ctx context.Context) ([]registry.Found, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	devs, err := unihedron.Discover()
	if err != nil {
		return nil, err
	}
	out := make([]registry.Found, 0, len(devs))
	for i, d := range devs {
		vals := map[string]any{"index": i}
		label := fmt.Sprintf("Unihedron SQM on %s", d.Port)
		if d.Unit.Serial != 0 {
			label = fmt.Sprintf("Unihedron SQM %d on %s", d.Unit.Serial, d.Port)
		}
		if d.Serial != "" {
			vals["serial"] = d.Serial
			label = fmt.Sprintf("%s (bridge serial %s)", label, d.Serial)
		}
		out = append(out, registry.Found{Label: label, Values: vals})
	}
	return out, nil
}
