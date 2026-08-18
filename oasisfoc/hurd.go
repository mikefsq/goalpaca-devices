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
	Index int `json:"index,omitempty" alpaca:"label=Enumeration index,min=0,when=start,help=Bind the Nth attached unit; prefer Serial where the device has one"`
}

func init() {
	registry.Register(registry.Driver{
		Name:          "oasisfoc",
		Type:          alpacadev.FocuserType,
		Description:   "Astroasis Oasis focuser",
		ConfigExample: `{ "driver": "oasisfoc", "index": 0 }`,
		Config:        func() any { return &Config{} },
		New: func(spec registry.Spec) (alpacadev.Device, error) {
			var cfg Config
			if err := spec.Decode(&cfg); err != nil {
				return nil, err
			}
			d := NewOasisFocuser(cfg.Index)
			if spec.Name != "" {
				d.DevName = spec.Name
			}
			return d, nil
		},
	})
}
