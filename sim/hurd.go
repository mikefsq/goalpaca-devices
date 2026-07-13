package driver

import (
	"encoding/json"

	"github.com/mikefsq/goalpaca/registry"
	alpacadev "github.com/mikefsq/goalpaca/server"
	"github.com/mikefsq/goalpaca/sim"
)

// init registers the simulated devices — one per ASCOM type — in the goalpaca driver
// registry, so a composed host (alpacahurd) constructs them from config by importing
// this package. Keep it always compiled in: a binary that can serve a full
// no-hardware herd (see the sim config) is how installs stay verifiable.
//
// sim-telescope and sim-camera share one simulated sky (guideModel): the mount owns
// the pointing error and the camera renders it, so PHD2 can calibrate and guide a
// closed loop. Both also drive the INDI/LX200 front-ends via the seams in mount.go /
// camera.go. The rest are standalone Alpaca sims from goalpaca/sim.
func init() {
	// Every sim decodes an empty driver-config so a stray driver-owned key in a sim
	// entry is still reported as a typo (common keys are stripped by Decode first).
	simNew := func(mk func(spec registry.Spec) alpacadev.Device) func(registry.Spec) (alpacadev.Device, error) {
		return func(spec registry.Spec) (alpacadev.Device, error) {
			if err := spec.Decode(&struct{}{}); err != nil {
				return nil, err
			}
			return mk(spec), nil
		}
	}
	setName := func(spec registry.Spec, name *string) {
		if spec.Name != "" {
			*name = spec.Name
		}
	}

	registry.Register(registry.Driver{
		Name: "sim-telescope", Type: alpacadev.TelescopeType,
		Description:   "simulated mount (Alpaca + INDI + LX200)",
		ConfigExample: `{ "driver": "sim-telescope", "name": "Sim Mount", "aperture": 130, "focalLength": 1000, "indi": true }`,
		New: simNew(func(spec registry.Spec) alpacadev.Device {
			return newSimMount(spec.Name)
		}),
	})
	registry.Register(registry.Driver{
		Name: "sim-camera", Type: alpacadev.CameraType,
		Description:   "simulated guide camera (Alpaca + INDI); focalLength (mm) + pixelSizeX/Y (µm) set the scale, pixelCountX/Y the sensor size",
		ConfigExample: `{ "driver": "sim-camera", "name": "Sim Guide Camera", "focalLength": 200, "pixelSizeX": 2.9, "pixelSizeY": 2.9, "pixelCountX": 1936, "pixelCountY": 1096, "indi": true }`,
		New: func(spec registry.Spec) (alpacadev.Device, error) {
			// pixel* are driver-owned keys (decoded strictly); focalLength is a
			// host-common key (stripped by Decode), read straight from the raw entry.
			var cfg struct {
				PixelSizeX  float64 `json:"pixelSizeX,omitempty"`
				PixelSizeY  float64 `json:"pixelSizeY,omitempty"`
				PixelCountX int     `json:"pixelCountX,omitempty"`
				PixelCountY int     `json:"pixelCountY,omitempty"`
			}
			if err := spec.Decode(&cfg); err != nil {
				return nil, err
			}
			var common struct {
				FocalLength float64 `json:"focalLength"`
			}
			_ = json.Unmarshal(spec.Raw, &common)
			return newSimCamera(spec.Name, common.FocalLength, cfg.PixelSizeX, cfg.PixelSizeY, cfg.PixelCountX, cfg.PixelCountY), nil
		},
	})
	registry.Register(registry.Driver{
		Name: "sim-focuser", Type: alpacadev.FocuserType,
		Description:   "simulated focuser",
		ConfigExample: `{ "driver": "sim-focuser", "name": "Sim Focuser" }`,
		New: simNew(func(spec registry.Spec) alpacadev.Device {
			d := sim.NewFocuser()
			setName(spec, &d.DevName)
			return d
		}),
	})
	registry.Register(registry.Driver{
		Name: "sim-filterwheel", Type: alpacadev.FilterWheelType,
		Description:   "simulated filter wheel",
		ConfigExample: `{ "driver": "sim-filterwheel", "name": "Sim Filter Wheel" }`,
		New: simNew(func(spec registry.Spec) alpacadev.Device {
			d := sim.NewFilterWheel()
			setName(spec, &d.DevName)
			return d
		}),
	})
	registry.Register(registry.Driver{
		Name: "sim-rotator", Type: alpacadev.RotatorType,
		Description:   "simulated rotator",
		ConfigExample: `{ "driver": "sim-rotator", "name": "Sim Rotator" }`,
		New: simNew(func(spec registry.Spec) alpacadev.Device {
			d := sim.NewRotator()
			setName(spec, &d.DevName)
			return d
		}),
	})
	registry.Register(registry.Driver{
		Name: "sim-switch", Type: alpacadev.SwitchType,
		Description:   "simulated switch bank",
		ConfigExample: `{ "driver": "sim-switch", "name": "Sim Switch" }`,
		New: simNew(func(spec registry.Spec) alpacadev.Device {
			d := sim.NewSwitch()
			setName(spec, &d.DevName)
			return d
		}),
	})
	registry.Register(registry.Driver{
		Name: "sim-dome", Type: alpacadev.DomeType,
		Description:   "simulated dome",
		ConfigExample: `{ "driver": "sim-dome", "name": "Sim Dome" }`,
		New: simNew(func(spec registry.Spec) alpacadev.Device {
			d := sim.NewDome()
			setName(spec, &d.DevName)
			return d
		}),
	})
	registry.Register(registry.Driver{
		Name: "sim-covercalibrator", Type: alpacadev.CoverCalibratorType,
		Description:   "simulated cover / flat panel",
		ConfigExample: `{ "driver": "sim-covercalibrator", "name": "Sim Flat Panel" }`,
		New: simNew(func(spec registry.Spec) alpacadev.Device {
			d := sim.NewCoverCalibrator()
			setName(spec, &d.DevName)
			return d
		}),
	})
	registry.Register(registry.Driver{
		Name: "sim-observingconditions", Type: alpacadev.ObservingConditionsType,
		Description:   "simulated weather station",
		ConfigExample: `{ "driver": "sim-observingconditions", "name": "Sim Weather" }`,
		New: simNew(func(spec registry.Spec) alpacadev.Device {
			d := sim.NewObservingConditions()
			setName(spec, &d.DevName)
			return d
		}),
	})
	registry.Register(registry.Driver{
		Name: "sim-safetymonitor", Type: alpacadev.SafetyMonitorType,
		Description:   "simulated safety monitor",
		ConfigExample: `{ "driver": "sim-safetymonitor", "name": "Sim Safety" }`,
		New: simNew(func(spec registry.Spec) alpacadev.Device {
			d := sim.NewSafetyMonitor()
			setName(spec, &d.DevName)
			return d
		}),
	})
}
