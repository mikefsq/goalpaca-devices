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
		Name:          "rst",
		Type:          alpacadev.TelescopeType,
		Description:   "Rainbow Astro RST mount (USB serial, auto-detected)",
		ConfigExample: `{ "driver": "rst" }`,
		New: func(spec registry.Spec) (alpacadev.Device, error) {
			var cfg struct {
				Serial string `json:"serial,omitempty"`
			}
			if err := spec.Decode(&cfg); err != nil {
				return nil, err
			}
			id := cfg.Serial
			if id == "" {
				id = "auto"
			}
			d := NewTelescope(cfg.Serial)
			d.ID = "rst-" + id
			d.DevName = "Rainbow Astro RST"
			if spec.Name != "" {
				d.DevName = spec.Name
			}
			d.Desc = "Rainbow Astro RST mount (" + id + ")"
			return d, nil
		},
	})
}
