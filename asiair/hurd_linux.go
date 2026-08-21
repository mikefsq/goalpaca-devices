// The _linux suffix is this driver's platform declaration: the GPIO character
// devices and I2C ADCs it drives exist only on the ASIAIR's Linux SBC, so
// registration compiles only there. On every other platform the package still
// builds (a fat alpacahurd blank-imports it anywhere) but registers nothing,
// so the driver is absent from that host's registry and a device entry naming
// it degrades to the skipped-entry path. See alpacahurd's DRIVERS.md,
// "Platform-specific drivers".
package driver

import (
	"sync"

	"github.com/mikefsq/goasi/asiair"
	"github.com/mikefsq/goalpaca/registry"
	alpacadev "github.com/mikefsq/goalpaca/server"
)

// The ASIAIR presents one device today, but the hub cache is here for the same
// reason smpro's is: two Boards on the same hardware would double-drive the GPIO
// lines and the I2C ADCs, and — worse on this board — the second Open would fail
// outright, because a GPIO character-device line request is exclusive. Whoever
// gets there second finds ports 3 and 4 already claimed.
//
// So a composed host (alpacahurd), which constructs each device independently
// from its own config entry, shares hubs through this cache, keyed by GPIO chip.
var (
	hubMu     sync.Mutex
	hubByChip = map[string]*Hub{}
)

func sharedHub(cfg asiair.Config) *Hub {
	hubMu.Lock()
	defer hubMu.Unlock()
	if h, ok := hubByChip[cfg.GPIOChip]; ok {
		return h
	}
	h := NewHub(cfg)
	hubByChip[cfg.GPIOChip] = h
	return h
}

// init registers the ASIAIR device in the goalpaca driver registry, so a composed
// host can construct it from a config entry by importing this package.
func init() {
	registry.Register(registry.Driver{
		Name:          "asiair-switch",
		Type:          alpacadev.SwitchType,
		Description:   "ZWO ASIAIR power board: four ports (two dimmable), DSLR shutter, per-port telemetry",
		ConfigExample: `{ "driver": "asiair-switch" }`,
		New: func(spec registry.Spec) (alpacadev.Device, error) {
			d := NewSwitch(sharedHub(asiair.DefaultConfig()))
			if spec.Name != "" {
				d.DevName = spec.Name
			}
			return d, nil
		},
	})
}
