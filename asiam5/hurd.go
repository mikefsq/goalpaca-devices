package driver

import (
	"fmt"

	"github.com/mikefsq/goalpaca/registry"
	alpacadev "github.com/mikefsq/goalpaca/server"
)

// init registers this driver in the goalpaca driver registry, so a composed
// host (alpacahurd) can construct it from a config entry by importing this
// package.
func init() {
	registry.Register(registry.Driver{
		Name:          "asiam5",
		Type:          alpacadev.TelescopeType,
		Description:   "ZWO AM-series harmonic mount (USB serial or TCP)",
		ConfigExample: `{ "driver": "asiam5", "serial": "0123456789ABCDEF" }`,
		New: func(spec registry.Spec) (alpacadev.Device, error) {
			var cfg struct {
				Serial string `json:"serial,omitempty"`
				Addr   string `json:"addr,omitempty"`
			}
			if err := spec.Decode(&cfg); err != nil {
				return nil, err
			}
			conn := cfg.Addr
			if conn == "" {
				conn = cfg.Serial
			}
			if conn == "" {
				return nil, fmt.Errorf("asiam5 requires \"serial\" or \"addr\"")
			}
			d := NewTelescope(cfg.Serial, cfg.Addr)
			d.ID = "zwoam5-" + conn
			d.DevName = "ZWO AM5"
			if spec.Name != "" {
				d.DevName = spec.Name
			}
			d.Desc = "ZWO AM-series mount (" + conn + ")"
			return d, nil
		},
	})
}
