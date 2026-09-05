// Registration is Linux-only; other platforms import the package without registering devices.
package driver

import (
	"sync"

	"github.com/mikefsq/goalpaca/registry"
	alpacadev "github.com/mikefsq/goalpaca/server"
	"github.com/mikefsq/goasi/asiair"
)

// Share a board per GPIO chip to avoid competing exclusive line requests.
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
