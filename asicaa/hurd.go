package driver

import (
	"github.com/mikefsq/goalpaca/registry"
	alpacadev "github.com/mikefsq/goalpaca/server"
)

// init registers this driver in the goalpaca driver registry, so a composed host
// (alpacahurd) can construct it from a config entry by importing this package.
//
// Unlike the pure-Go drivers, this one is cgo: it reaches the rotator through
// goasi/caa, which binds the proprietary ZWO CAA SDK. A host that compiles it in
// needs CGO_ENABLED=1 and the SDK shared library present at both build and run
// time, and can no longer be cross-compiled as a static binary. Hosts that ship
// one static binary per platform must leave this driver out of their build list.
// Config is the entry's driver-owned keys. Every field selects the hardware
// to bind and applies at the next start; the setup page shows them read-only.
type Config struct {
	Index  int    `json:"index,omitempty" alpaca:"label=Enumeration index,min=0,when=start,help=Bind the Nth attached unit; prefer Serial where the device has one"`
	Serial string `json:"serial,omitempty" alpaca:"label=Serial,when=start,help=Bind by serial (stable across replug and start-before-plug)"`
}

func init() {
	registry.Register(registry.Driver{
		Name:          "asicaa",
		Type:          alpacadev.RotatorType,
		Description:   "ZWO CAA camera-angle adjuster / rotator (ZWO SDK, cgo)",
		ConfigExample: `{ "driver": "asicaa", "index": 0 }`,
		Config:        func() any { return &Config{} },
		New: func(spec registry.Spec) (alpacadev.Device, error) {
			var cfg Config
			if err := spec.Decode(&cfg); err != nil {
				return nil, err
			}
			// Serial binding gives the device a stable identity before the
			// rotator is attached; index selects by enumeration order instead.
			d := NewASIRotator(cfg.Index, cfg.Serial)
			if spec.Name != "" {
				d.DevName = spec.Name
			}
			return d, nil
		},
	})
}
