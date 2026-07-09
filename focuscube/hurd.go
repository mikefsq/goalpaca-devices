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
		Name:          "focuscube",
		Type:          alpacadev.FocuserType,
		Description:   "Pegasus Astro FocusCube focuser",
		ConfigExample: `{ "driver": "focuscube", "index": 0, "maxstep": 100000 }`,
		New: func(spec registry.Spec) (alpacadev.Device, error) {
			var cfg struct {
				Index   int    `json:"index,omitempty"`
				Serial  string `json:"serial,omitempty"`
				MaxStep int    `json:"maxstep,omitempty"` // travel (the device doesn't report it)
			}
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
