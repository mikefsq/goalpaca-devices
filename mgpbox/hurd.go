package driver

import (
	"github.com/mikefsq/goalpaca/registry"
	alpacadev "github.com/mikefsq/goalpaca/server"
)

// init registers this driver in the goalpaca driver registry, so a composed
// host (alpacahurd) can construct it from a config entry by importing this
// package.
func init() {
	registry.Register(registry.Driver{
		Name:        "mgpbox",
		Type:        alpacadev.ObservingConditionsType,
		Description: "Astromi.ch MGPBox weather + GPS box",
		ConfigExample: `{ "driver": "mgpbox", "index": 0, ` +
			`"feed": [ { "addr": "10.0.1.5:11111", "type": "telescope", "device": 0 }, ` +
			`{ "addr": "localhost:11130", "type": "switch", "device": 0 } ] }`,
		New: func(spec registry.Spec) (alpacadev.Device, error) {
			var cfg struct {
				Index  int    `json:"index,omitempty"`
				Serial string `json:"serial,omitempty"`

				// Feed lists the Alpaca devices this box pushes its GPS + weather
				// snapshot to, via each one's setenvironment Action. A tenmicron
				// telescope takes the pressure/temperature refraction datums plus the
				// site and time; an SM Pro switch takes temperature, humidity and dew
				// point for its dew heaters. Every consumer ignores what it does not
				// understand, so one snapshot serves them all.
				Feed []FeedTarget `json:"feed,omitempty"`

				// MountAddr/MountDevice are the historical single-telescope spelling,
				// kept so existing configs keep working. Equivalent to one Feed entry
				// of type "telescope".
				MountAddr   string `json:"mountAddr,omitempty"`
				MountDevice int    `json:"mountDevice,omitempty"`
			}
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
