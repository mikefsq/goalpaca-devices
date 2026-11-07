package driver

import (
	"fmt"

	"github.com/mikefsq/goalpaca/registry"
	alpacadev "github.com/mikefsq/goalpaca/server"
)

// Config contains device selection and settings.
type Config struct {
	ObservingConditions              *WeatherSource `json:"observingConditions,omitempty" alpaca:"hidden"`
	ObservingConditionsAddr          string         `json:"observingConditionsAddr,omitempty" alpaca:"label=ObservingConditions address,when=start,help=Optional host:port; blank disables weather pull unless observingConditions is configured"`
	ObservingConditionsDevice        int            `json:"observingConditionsDevice,omitempty" alpaca:"label=ObservingConditions device,when=start,min=0,help=Alpaca device number on the weather server"`
	ObservingConditionsMaxAgeSeconds float64        `json:"observingConditionsMaxAgeSeconds,omitempty" alpaca:"label=Weather maximum age (seconds),when=start,min=0,help=Maximum sample age; zero uses 30 seconds"`

	Gpsd          string `json:"gpsd,omitempty" alpaca:"label=Location gpsd address,when=start,help=Optional host:port for Resync or gpsdSync position updates; blank disables"`
	GpsdSync      bool   `json:"gpsdSync,omitempty" alpaca:"label=Sync gpsd position on connect,when=start,help=Apply GPS position once on connect; default false"`
	HostClockSync bool   `json:"hostClockSync,omitempty" alpaca:"label=Sync host clock,when=start,help=Set and nudge clock once on connect and during Resync; false disables automatic clock sync"`

	Addr string `json:"addr,omitempty" alpaca:"label=Address,when=start,help='host:port (3490, 3492)'"`
}

func init() {
	registry.Register(registry.Driver{
		Name:          "tenmicron",
		Type:          alpacadev.TelescopeType,
		Description:   "10Micron GM-series mount (TCP)",
		ConfigExample: `{ "driver": "tenmicron", "addr": "10.0.1.51:3492", "aperture": 200, "focalLength": 1600 }`,
		Config:        func() any { return &Config{} },
		// The address IS the identity, and there is no Scan: a mount on the network answers
		// nothing until it is dialled, so a host asks for the address rather than offering a
		// list. Declaring the key anyway is what lets a host pin this device the same way it
		// pins a scanned one.
		Identity: []string{"addr"},
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
			cfg.ObservingConditions = cfg.weatherSource()
			if err := cfg.validateSources(); err != nil {
				return nil, err
			}
			d := NewTelescope(cfg.Addr)
			d.sources = cfg
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
