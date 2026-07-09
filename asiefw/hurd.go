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
		Name:          "asiefw",
		Type:          alpacadev.FilterWheelType,
		Description:   "ZWO EFW filter wheel",
		ConfigExample: `{ "driver": "asiefw", "index": 0 }`,
		New: func(spec registry.Spec) (alpacadev.Device, error) {
			var cfg struct {
				Index          int    `json:"index,omitempty"`
				Serial         string `json:"serial,omitempty"`
				Unidirectional bool   `json:"unidirectional,omitempty"` // always rotate one way
			}
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
