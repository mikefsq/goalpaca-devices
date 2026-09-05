package driver

import (
	"fmt"

	"github.com/mikefsq/goalpaca/registry"
	alpacadev "github.com/mikefsq/goalpaca/server"
)

// Config contains device selection and settings.
type Config struct {
	Addr string `json:"addr,omitempty" alpaca:"label=Address,when=start,help=host:port"`
}

func init() {
	registry.Register(registry.Driver{
		Name:          "tenmicron",
		Type:          alpacadev.TelescopeType,
		Description:   "10Micron GM-series mount (TCP)",
		ConfigExample: `{ "driver": "tenmicron", "addr": "10.0.1.51:3492", "aperture": 200, "focalLength": 1600 }`,
		Config:        func() any { return &Config{} },
		// 'G': the GM series are German equatorials.
		FrontEnd: lx200FrontEnd('G', "10micron"),
		New: func(spec registry.Spec) (alpacadev.Device, error) {
			var cfg Config
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
