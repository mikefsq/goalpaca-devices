package driver

import (
	"fmt"

	"github.com/mikefsq/goalpaca/registry"
	alpacadev "github.com/mikefsq/goalpaca/server"
)

// Config contains device selection and settings.
type Config struct {
	Serial string `json:"serial,omitempty" alpaca:"label=Serial,when=start,help=Bind by serial (stable across replug and start-before-plug)"`
	Addr   string `json:"addr,omitempty" alpaca:"label=Address,when=start,help=host:port"`
}

func init() {
	registry.Register(registry.Driver{
		Name:          "onstep",
		Type:          alpacadev.TelescopeType,
		Description:   "OnStep telescope controller (USB serial or TCP)",
		ConfigExample: `{ "driver": "onstep", "addr": "192.168.0.1:9999" }`,
		Config:        func() any { return &Config{} },
		// 'G': OnStep controllers most commonly drive German equatorials.
		FrontEnd: lx200FrontEnd('G', "OnStep"),
		New: func(spec registry.Spec) (alpacadev.Device, error) {
			var cfg Config
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
