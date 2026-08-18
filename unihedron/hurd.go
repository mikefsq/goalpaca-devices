package driver

import (
	"github.com/mikefsq/goalpaca/registry"
	alpacadev "github.com/mikefsq/goalpaca/server"
)

// init registers this driver in the goalpaca driver registry, so a composed
// host (alpacahurd) can construct it from a config entry by importing this
// package.
// Config is the entry's driver-owned keys. Every field selects the hardware
// to bind and applies at the next start; the setup page shows them read-only.
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
