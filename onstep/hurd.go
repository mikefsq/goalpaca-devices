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
		Name:          "onstep",
		Type:          alpacadev.TelescopeType,
		Description:   "OnStep telescope controller (USB serial or TCP)",
		ConfigExample: `{ "driver": "onstep", "addr": "192.168.0.1:9999" }`,
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
				return nil, fmt.Errorf("onstep requires \"serial\" or \"addr\"")
			}
			d := NewTelescope(cfg.Serial, cfg.Addr)
			d.ID = "onstep-" + conn
			d.DevName = "OnStep"
			if spec.Name != "" {
				d.DevName = spec.Name
			}
			d.Desc = "OnStep controller (" + conn + ")"
			return d, nil
		},
	})
}
