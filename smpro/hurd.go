package driver

import (
	"sync"

	"github.com/mikefsq/goalpaca/registry"
	alpacadev "github.com/mikefsq/goalpaca/server"
	"github.com/mikefsq/stellarmate"
)

// The two SM Pro devices must share one Hub (one Board): two Boards on the same
// hardware would double-drive the I2C expander and the stepper UART. A composed
// host constructs each device independently from its own config entry, so the
// registry constructors share hubs through this cache, keyed by I2C bus.
var (
	hubMu    sync.Mutex
	hubByBus = map[string]*Hub{}
)

func sharedHub(cfg stellarmate.Config) *Hub {
	hubMu.Lock()
	defer hubMu.Unlock()
	if h, ok := hubByBus[cfg.I2CBus]; ok {
		return h
	}
	h := NewHub(cfg)
	hubByBus[cfg.I2CBus] = h
	return h
}

// init registers both SM Pro devices in the goalpaca driver registry, so a
// composed host (alpacahurd) can construct them from config entries by
// importing this package.
func init() {
	registry.Register(registry.Driver{
		Name:          "smpro-switch",
		Type:          alpacadev.SwitchType,
		Description:   "StellarMate SM Pro power/dew/variable-output switch",
		ConfigExample: `{ "driver": "smpro-switch" }`,
		New: func(spec registry.Spec) (alpacadev.Device, error) {
			d := NewSwitch(sharedHub(stellarmate.DefaultConfig()))
			if spec.Name != "" {
				d.DevName = spec.Name
			}
			return d, nil
		},
	})
	registry.Register(registry.Driver{
		Name:          "smpro-focuser",
		Type:          alpacadev.FocuserType,
		Description:   "StellarMate SM Pro TMC2209 focuser",
		ConfigExample: `{ "driver": "smpro-focuser" }`,
		New: func(spec registry.Spec) (alpacadev.Device, error) {
			d := NewFocuser(sharedHub(stellarmate.DefaultConfig()))
			if spec.Name != "" {
				d.DevName = spec.Name
			}
			return d, nil
		},
	})
}
