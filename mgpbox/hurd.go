package driver

import (
	"context"
	"fmt"

	"github.com/mikefsq/astromi.ch/mgpbox"
	"github.com/mikefsq/goalpaca/registry"
	alpacadev "github.com/mikefsq/goalpaca/server"
)

// Config contains device selection and settings.
type Config struct {
	Index       int          `json:"index,omitempty"       alpaca:"label=Enumeration index,min=0,when=start,help=Bind the Nth attached unit; prefer Serial"`
	Serial      string       `json:"serial,omitempty"      alpaca:"label=Serial,when=start,help=FTDI serial (stable across replug)"`
	Feed        []FeedTarget `json:"feed,omitempty"        alpaca:"hidden"`
	MountAddr   string       `json:"mountAddr,omitempty"   alpaca:"label=Mount address,when=start,help=host:port of the telescope to feed (legacy; use feed)"`
	MountDevice int          `json:"mountDevice,omitempty" alpaca:"label=Mount device number,min=0,when=start"`
}

func init() {
	registry.Register(registry.Driver{
		Name:        "mgpbox",
		Type:        alpacadev.ObservingConditionsType,
		Description: "Astromi.ch MGPBox weather + GPS box",
		Config:      func() any { return &Config{} },
		// The FTDI serial survives a replug; the enumeration index does not.
		Identity: []string{"serial", "index"},
		Scan:     scanDevices,
		ConfigExample: `{ "driver": "mgpbox", "index": 0, ` +
			`"feed": [ { "addr": "10.0.1.5:11111", "type": "telescope", "device": 0 }, ` +
			`{ "addr": "localhost:11130", "type": "switch", "device": 0 } ] }`,
		New: func(spec registry.Spec) (alpacadev.Device, error) {
			var cfg Config
			if err := spec.Decode(&cfg); err != nil {
				return nil, err
			}
			// Prefer the stable USB-bridge serial when given; otherwise discover.
			var d *MGPBox
			if cfg.Serial != "" {
				d = NewMGPBoxBySerial(cfg.Index, cfg.Serial)
			} else {
				d = NewMGPBox(cfg.Index)
			}
			if spec.Name != "" {
				d.DevName = spec.Name
			}
			targets := cfg.Feed
			if cfg.MountAddr != "" {
				targets = append(targets, FeedTarget{Addr: cfg.MountAddr, Type: "telescope", Device: cfg.MountDevice})
			}
			if err := d.SetFeedTargets(targets); err != nil {
				return nil, err
			}
			return d, nil
		},
	})
}

// scanDevices lists the attached MGPBox units.
//
// mgpbox.Discover pokes each candidate port and keeps the ones that stream MGPBox content,
// closing the port again. Enumerating ports alone would not do: the box shares its FTDI bridge
// with a Unihedron SQM and most other serial instruments, so a listing by descriptor reports each
// of them as an MGPBox.
func scanDevices(ctx context.Context) ([]registry.Found, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	devs, err := mgpbox.Discover()
	if err != nil {
		return nil, err
	}
	out := make([]registry.Found, 0, len(devs))
	for i, d := range devs {
		vals := map[string]any{"index": i}
		label := d.Product
		if label == "" {
			label = "MGPBox"
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
