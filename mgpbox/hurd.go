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
		Name:          "mgpbox",
		Type:          alpacadev.ObservingConditionsType,
		Description:   "Astromi.ch MGPBox weather + GPS box",
		ConfigExample: `{ "driver": "mgpbox", "index": 0 }`,
		New: func(spec registry.Spec) (alpacadev.Device, error) {
			var cfg struct {
				Index  int    `json:"index,omitempty"`
				Serial string `json:"serial,omitempty"`

				// MountAddr feeds this box's GPS + weather readings into a tenmicron
				// mount's Alpaca server: host:port of that server (its telescope is
				// MountDevice, default 0).
				MountAddr   string `json:"mountAddr,omitempty"`
				MountDevice int    `json:"mountDevice,omitempty"`
			}
			if err := spec.Decode(&cfg); err != nil {
				return nil, err
			}
			// Prefer the stable USB-bridge serial when given; otherwise discover.
			var d *MGPBox
			if cfg.Serial != "" {
				d = NewMGPBoxBySerial(cfg.Index, cfg.Serial)
			} else {
				d = NewMGPBox(cfg.Index)
			}
			if spec.Name != "" {
				d.DevName = spec.Name
			}
			if cfg.MountAddr != "" {
				d.SetMountFeed(cfg.MountAddr, cfg.MountDevice)
			}
			return d, nil
		},
	})
}
