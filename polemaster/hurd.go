package driver

import (
	"github.com/mikefsq/goalpaca/registry"
	alpacadev "github.com/mikefsq/goalpaca/server"
)

// Config selects an optional firmware image. The library opens the first
// matching camera; there is no per-unit selector.
type Config struct {
	Firmware string `json:"firmware,omitempty" alpaca:"label=Firmware image,when=start,help=Intel HEX image to download instead of the built-in one; leave empty for the firmware the driver carries"`
}

func init() {
	registry.Register(registry.Driver{
		Name:          "polemaster",
		Type:          alpacadev.CameraType,
		Description:   "QHY PoleMaster polar-alignment camera (pure-Go USB driver)",
		ConfigExample: `{ "driver": "polemaster", "name": "PoleMaster" }`,
		Config:        func() any { return &Config{} },
		New: func(spec registry.Spec) (alpacadev.Device, error) {
			var cfg Config
			if err := spec.Decode(&cfg); err != nil {
				return nil, err
			}
			d := NewPoleMaster(cfg.Firmware)
			d.Instance = spec.Instance
			if spec.Name != "" {
				d.DevName = spec.Name
			}
			return d, nil
		},
	})
}
