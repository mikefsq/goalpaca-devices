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
		Name:          "unihedron",
		Type:          alpacadev.ObservingConditionsType,
		Description:   "Unihedron SQM sky-quality meter",
		ConfigExample: `{ "driver": "unihedron", "index": 0 }`,
		New: func(spec registry.Spec) (alpacadev.Device, error) {
			var cfg struct {
				Index  int    `json:"index,omitempty"`
				Serial string `json:"serial,omitempty"`
			}
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
