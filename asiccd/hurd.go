package driver

import (
	"github.com/mikefsq/goalpaca/registry"
	alpacadev "github.com/mikefsq/goalpaca/server"
)

// Config contains device selection and settings.
type Config struct {
	Index  int    `json:"index,omitempty" alpaca:"label=Enumeration index,min=0,when=start,help=Bind the Nth attached unit; prefer Serial where the device has one"`
	Serial string `json:"serial,omitempty" alpaca:"label=Serial,when=start,help=Bind by serial (stable across replug and start-before-plug)"`
}

func init() {
	registry.Register(registry.Driver{
		Name:          "asiccd",
		Type:          alpacadev.CameraType,
		Description:   "ZWO ASI camera (ZWO SDK, cgo; see astrocam for the pure-Go driver)",
		ConfigExample: `{ "driver": "asiccd", "index": 0, "name": "Main camera" }`,
		Config:        func() any { return &Config{} },
		New: func(spec registry.Spec) (alpacadev.Device, error) {
			var cfg Config
			if err := spec.Decode(&cfg); err != nil {
				return nil, err
			}
			// Serial binding gives the device a stable identity before the
			// camera is attached; index selects by enumeration order instead.
			d := NewASICamera(cfg.Index, cfg.Serial)
			if spec.Name != "" {
				d.DevName = spec.Name
			}
			return d, nil
		},
	})
}
