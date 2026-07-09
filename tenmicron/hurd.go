package driver

import (
	"fmt"

	"github.com/mikefsq/goalpaca/registry"
	alpacadev "github.com/mikefsq/goalpaca/server"
)

// init registers this driver in the goalpaca driver registry, so a composed
// host (alpacahurd) can construct it from a config entry by importing this
// package. Construction touches no hardware; the device connects in its own
// acquire/monitor/re-acquire loop once served.
func init() {
	registry.Register(registry.Driver{
		Name:          "tenmicron",
		Type:          alpacadev.TelescopeType,
		Description:   "10Micron GM-series mount (TCP)",
		ConfigExample: `{ "driver": "tenmicron", "addr": "10.0.1.51:3492", "aperture": 200, "focalLength": 1600 }`,
		New: func(spec registry.Spec) (alpacadev.Device, error) {
			var cfg struct {
				Addr string `json:"addr"`
			}
			if err := spec.Decode(&cfg); err != nil {
				return nil, err
			}
			if cfg.Addr == "" {
				return nil, fmt.Errorf("tenmicron requires \"addr\" (mount host:port)")
			}
			d := NewTelescope(cfg.Addr)
			// Optics are seeded (unit-converted from config mm) by the shared holder the
			// host injects via UseOptics.
			d.ID = "10micron-" + cfg.Addr
			d.DevName = "10Micron GM"
			if spec.Name != "" {
				d.DevName = spec.Name
			}
			d.Desc = "10Micron GM-series mount (" + cfg.Addr + ")"
			return d, nil
		},
	})
}
