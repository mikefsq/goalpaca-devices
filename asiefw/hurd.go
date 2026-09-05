package driver

import (
	"github.com/mikefsq/goalpaca/registry"
	alpacadev "github.com/mikefsq/goalpaca/server"
)

// Config contains device selection and settings.
type Config struct {
	Index          int    `json:"index,omitempty" alpaca:"label=Enumeration index,min=0,when=start,help=Bind the Nth attached unit; prefer Serial where the device has one"`
	Serial         string `json:"serial,omitempty" alpaca:"label=Serial,when=start,help=Bind by serial (stable across replug and start-before-plug)"`
	Unidirectional bool   `json:"unidirectional,omitempty" alpaca:"label=Unidirectional,help=Always rotate one way"`
}

func init() {
	registry.Register(registry.Driver{
		Name:          "asiefw",
		Type:          alpacadev.FilterWheelType,
		Description:   "ZWO EFW filter wheel",
		ConfigExample: `{ "driver": "asiefw", "index": 0 }`,
		Config:        func() any { return &Config{} },
		New: func(spec registry.Spec) (alpacadev.Device, error) {
			var cfg Config
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
