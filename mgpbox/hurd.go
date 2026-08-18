package driver

import (
	"github.com/mikefsq/goalpaca/registry"
	alpacadev "github.com/mikefsq/goalpaca/server"
)

// init registers this driver in the goalpaca driver registry, so a composed
// host (alpacahurd) can construct it from a config entry by importing this
// package.
// Config is the mgpbox entry's driver-owned keys. Index and Serial bind the
// box and apply at the next start. Feed lists the Alpaca devices the box pushes
// its GPS + weather snapshot to, via each one's setenvironment Action; a
// tenmicron telescope takes the pressure/temperature refraction datums plus the
// site and time, and an SM Pro switch takes temperature, humidity and dew point
// for its dew heaters. Every consumer ignores what it does not understand, so
// one snapshot serves them all. Feed is a list and stays a config-file matter,
// so the setup page omits it. MountAddr and MountDevice are the historical
// single-telescope spelling, kept so existing configs keep working; together
// they equal one Feed entry of type "telescope".
type Config struct {
	Index       int          `json:"index,omitempty"       alpaca:"label=Enumeration index,min=0,when=start,help=Bind the Nth attached unit; prefer Serial"`
	Serial      string       `json:"serial,omitempty"      alpaca:"label=Serial,when=start,help=FTDI serial (stable across replug)"`
	Feed        []FeedTarget `json:"feed,omitempty"        alpaca:"hidden"`
	MountAddr   string       `json:"mountAddr,omitempty"   alpaca:"label=Mount address,when=start,help=host:port of the telescope to feed (legacy; use feed)"`
	MountDevice int          `json:"mountDevice,omitempty" alpaca:"label=Mount device number,min=0,when=start"`
}

func init() {
	registry.Register(registry.Driver{
		Name:        "mgpbox",
		Type:        alpacadev.ObservingConditionsType,
		Description: "Astromi.ch MGPBox weather + GPS box",
		Config:      func() any { return &Config{} },
		ConfigExample: `{ "driver": "mgpbox", "index": 0, ` +
			`"feed": [ { "addr": "10.0.1.5:11111", "type": "telescope", "device": 0 }, ` +
			`{ "addr": "localhost:11130", "type": "switch", "device": 0 } ] }`,
		New: func(spec registry.Spec) (alpacadev.Device, error) {
			var cfg Config
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
			targets := cfg.Feed
			if cfg.MountAddr != "" {
				targets = append(targets, FeedTarget{Addr: cfg.MountAddr, Type: "telescope", Device: cfg.MountDevice})
			}
			if err := d.SetFeedTargets(targets); err != nil {
				return nil, err
			}
			return d, nil
		},
	})
}
