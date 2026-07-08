package main

import (
	"fmt"
	"strings"

	alpacadev "github.com/mikefsq/goalpaca/server"
	"github.com/mikefsq/goalpaca/sim"

	am5drv "github.com/mikefsq/goalpaca-devices/asiam5"
	asicamdrv "github.com/mikefsq/goalpaca-devices/astrocam"
	asieafdrv "github.com/mikefsq/goalpaca-devices/asieaf"
	asiefwdrv "github.com/mikefsq/goalpaca-devices/asiefw"
	focuscubedrv "github.com/mikefsq/goalpaca-devices/focuscube"
	focuslynxdrv "github.com/mikefsq/goalpaca-devices/focuslynx"
	mgpboxdrv "github.com/mikefsq/goalpaca-devices/mgpbox"
	oasisfocdrv "github.com/mikefsq/goalpaca-devices/oasisfoc"
	oasisfwdrv "github.com/mikefsq/goalpaca-devices/oasisfw"
	onstepdrv "github.com/mikefsq/goalpaca-devices/onstep"
	rstdrv "github.com/mikefsq/goalpaca-devices/rst"
	tenmicrondrv "github.com/mikefsq/goalpaca-devices/tenmicron"
	unihedrondrv "github.com/mikefsq/goalpaca-devices/unihedron"
)

// counters assigns sequential, 0-based ASCOM device numbers within each device type.
type counters map[alpacadev.DeviceType]int

func (c counters) next(t alpacadev.DeviceType) int {
	n := c[t]
	c[t] = n + 1
	return n
}

// registerDevice constructs the device named by spec.Driver and registers it on srv
// under the next free number for its ASCOM type, returning it for the other
// front-ends (INDI hub, LX200 bridge). Registration touches no hardware; the
// device's hardware loop is started later by srv.Run.
func registerDevice(srv *alpacadev.Server, spec DeviceSpec, port int, c counters) (alpacadev.Device, error) {
	reg := func(t alpacadev.DeviceType, d alpacadev.Device) (alpacadev.Device, error) {
		return d, srv.Register(t, c.next(t), d)
	}

	switch strings.ToLower(spec.Driver) {

	// ---- Telescopes (mounts) ----
	case "tenmicron":
		if spec.Addr == "" {
			return nil, fmt.Errorf("tenmicron requires \"addr\" (mount host:port)")
		}
		d := tenmicrondrv.NewTelescope(spec.Addr)
		// Optics are seeded (unit-converted from config mm) by the shared holder the
		// caller injects via UseOptics.
		d.ID = "10micron-" + spec.Addr
		d.DevName = pick(spec.Name, "10Micron GM")
		d.Desc = "10Micron GM-series mount (" + spec.Addr + ")"
		return reg(alpacadev.TelescopeType, d)

	case "asiam5":
		conn := pick(spec.Addr, spec.Serial)
		if conn == "" {
			return nil, fmt.Errorf("asiam5 requires \"serial\" or \"addr\"")
		}
		d := am5drv.NewTelescope(spec.Serial, spec.Addr)
		d.ID = "zwoam5-" + conn
		d.DevName = pick(spec.Name, "ZWO AM5")
		d.Desc = "ZWO AM-series mount (" + conn + ")"
		return reg(alpacadev.TelescopeType, d)

	case "onstep":
		conn := pick(spec.Addr, spec.Serial)
		if conn == "" {
			return nil, fmt.Errorf("onstep requires \"serial\" or \"addr\"")
		}
		d := onstepdrv.NewTelescope(spec.Serial, spec.Addr)
		d.ID = "onstep-" + conn
		d.DevName = pick(spec.Name, "OnStep")
		d.Desc = "OnStep controller (" + conn + ")"
		return reg(alpacadev.TelescopeType, d)

	case "rst":
		id := pick(spec.Serial, "auto")
		d := rstdrv.NewTelescope(spec.Serial)
		d.ID = "rst-" + id
		d.DevName = pick(spec.Name, "Rainbow Astro RST")
		d.Desc = "Rainbow Astro RST mount (" + id + ")"
		return reg(alpacadev.TelescopeType, d)

	// ---- Cameras ----
	case "asicam":
		d := asicamdrv.NewPureASICamera(spec.Index, spec.Serial)
		d.SetFixDefects(spec.FixDefects) // "fixdefects": true → factory hot-pixel correction
		if spec.Name != "" {
			d.DevName = spec.Name
		}
		// Wrap so the camera can also serve the INDI CCD front-end (guide camera) when
		// "indi": true.
		return reg(alpacadev.CameraType, newAstrocamINDI(d))

	// ---- Focusers ----
	case "asieaf":
		d := asieafdrv.NewASIFocuser(spec.Index, spec.Serial)
		if spec.Name != "" {
			d.DevName = spec.Name
		}
		return reg(alpacadev.FocuserType, d)

	case "oasisfoc":
		d := oasisfocdrv.NewOasisFocuser(spec.Index)
		if spec.Name != "" {
			d.DevName = spec.Name
		}
		return reg(alpacadev.FocuserType, d)

	case "focuscube":
		maxStep := spec.MaxStep
		if maxStep == 0 {
			maxStep = 100000
		}
		// Prefer the stable USB-serial binding when given; fall back to enumeration index.
		var d *focuscubedrv.PegasusFocuser
		if spec.Serial != "" {
			d = focuscubedrv.NewPegasusFocuserBySerial(spec.Index, spec.Serial, maxStep)
		} else {
			d = focuscubedrv.NewPegasusFocuser(spec.Index, maxStep)
		}
		if spec.Name != "" {
			d.DevName = spec.Name
		}
		return reg(alpacadev.FocuserType, d)

	case "focuslynx":
		// Prefer the stable protocol-nickname binding when given (channel is then
		// discovered over the protocol); otherwise bind by enumeration index + channel.
		var d *focuslynxdrv.OptecFocuser
		if spec.Nickname != "" {
			d = focuslynxdrv.NewOptecFocuserByNickname(spec.Index, spec.Nickname)
		} else {
			ch := spec.Channel
			if ch == 0 {
				ch = 1
			}
			d = focuslynxdrv.NewOptecFocuser(spec.Index, ch)
		}
		if spec.Name != "" {
			d.DevName = spec.Name
		}
		return reg(alpacadev.FocuserType, d)

	// ---- Filter wheels ----
	case "asiefw":
		d := asiefwdrv.NewASIFilterWheel(spec.Index, spec.Serial, spec.Unidirectional)
		if spec.Name != "" {
			d.DevName = spec.Name
		}
		return reg(alpacadev.FilterWheelType, d)

	case "oasisfw":
		d := oasisfwdrv.NewOasisWheel(spec.Index)
		if spec.Name != "" {
			d.DevName = spec.Name
		}
		return reg(alpacadev.FilterWheelType, d)

	// ---- Observing conditions (sky quality / weather) ----
	case "mgpbox":
		// Astromi.ch MGPBox weather box (temperature/humidity/pressure/dewpoint) over
		// FTDI serial. Prefer the stable USB-bridge serial when given; otherwise discover.
		var d *mgpboxdrv.MGPBox
		if spec.Serial != "" {
			d = mgpboxdrv.NewMGPBoxBySerial(spec.Index, spec.Serial)
		} else {
			d = mgpboxdrv.NewMGPBox(spec.Index)
		}
		if spec.Name != "" {
			d.DevName = spec.Name
		}
		if spec.MountAddr != "" {
			// Feed GPS + weather into a tenmicron mount's setenvironment Action.
			d.SetMountFeed(spec.MountAddr, spec.MountDevice)
		}
		return reg(alpacadev.ObservingConditionsType, d)

	case "unihedron":
		// Unihedron SQM sky-quality meter over FTDI serial. Prefer the stable
		// USB-bridge serial when given; otherwise bind by enumeration index.
		var d *unihedrondrv.SQM
		if spec.Serial != "" {
			d = unihedrondrv.NewSQMBySerial(spec.Index, spec.Serial)
		} else {
			d = unihedrondrv.NewSQM(spec.Index)
		}
		if spec.Name != "" {
			d.DevName = spec.Name
		}
		return reg(alpacadev.ObservingConditionsType, d)

	// ---- Simulators (no hardware; for client development) ----
	case "sim-telescope":
		// Backed by a lx200.Mount adapter, so the sim also drives the INDI/LX200
		// front-ends — not just Alpaca (unlike the other sim-* devices).
		return reg(alpacadev.TelescopeType, newSimMount(spec.Name))
	case "sim-camera":
		// Backed by a ccd.Camera adapter, so the sim camera also drives the INDI CCD
		// device — PHD2 can use it as a guide camera, not just Alpaca.
		return reg(alpacadev.CameraType, newSimCamera(spec.Name))
	case "sim-focuser":
		d := sim.NewFocuser()
		simName(spec, &d.DevName)
		return reg(alpacadev.FocuserType, d)
	case "sim-filterwheel":
		d := sim.NewFilterWheel()
		simName(spec, &d.DevName)
		return reg(alpacadev.FilterWheelType, d)
	case "sim-rotator":
		d := sim.NewRotator()
		simName(spec, &d.DevName)
		return reg(alpacadev.RotatorType, d)
	case "sim-switch":
		d := sim.NewSwitch()
		simName(spec, &d.DevName)
		return reg(alpacadev.SwitchType, d)
	case "sim-dome":
		d := sim.NewDome()
		simName(spec, &d.DevName)
		return reg(alpacadev.DomeType, d)
	case "sim-covercalibrator":
		d := sim.NewCoverCalibrator()
		simName(spec, &d.DevName)
		return reg(alpacadev.CoverCalibratorType, d)
	case "sim-observingconditions":
		d := sim.NewObservingConditions()
		simName(spec, &d.DevName)
		return reg(alpacadev.ObservingConditionsType, d)
	case "sim-safetymonitor":
		d := sim.NewSafetyMonitor()
		simName(spec, &d.DevName)
		return reg(alpacadev.SafetyMonitorType, d)

	// ---- ZWO SDK devices (cgo + vendor library) ----
	case "asiccd", "asicaa":
		return nil, fmt.Errorf("%q needs the ZWO SDK (cgo) and is not built into the vendor-free fleet; "+
			"run its standalone cmd, or use the Go asicam driver for ZWO cameras", spec.Driver)

	default:
		return nil, fmt.Errorf("unknown driver %q", spec.Driver)
	}
}

// simName overrides a simulated device's display name when the config sets one.
func simName(spec DeviceSpec, name *string) {
	if spec.Name != "" {
		*name = spec.Name
	}
}

// pick returns the first non-empty string.
func pick(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
