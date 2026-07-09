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
		Name:          "oasisfoc",
		Type:          alpacadev.FocuserType,
		Description:   "Astroasis Oasis focuser",
		ConfigExample: `{ "driver": "oasisfoc", "index": 0 }`,
		New: func(spec registry.Spec) (alpacadev.Device, error) {
			var cfg struct {
				Index int `json:"index,omitempty"`
			}
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
