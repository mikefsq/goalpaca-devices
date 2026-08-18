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
