package driver

import (
	"fmt"
	"strconv"
	"strings"

	alpacadev "github.com/mikefsq/goalpaca/server"
	"github.com/mikefsq/oasis-astro/oasisfoc"
)

// Aliases to the library types, so the action helpers read cleanly.
type (
	oasisDev    = *oasisfoc.Oasis
	oasisConfig = oasisfoc.Config
	oasisExt    = oasisfoc.ExtConfig
)

// The Oasis focuser exposes more than ASCOM IFocuserV3 has slots for (backlash, reverse,
// beeps, speed, heating, stall, USB power, names, identity, relative moves, sync/zero),
// surfaced via the Action seam: SupportedActions lists names, Action(name, params)
// dispatches to the oasisfoc library.
//
// Convention: action names are lowercase. Getters ignore params and return a string
// ("true"/"false" for booleans). Setters take the value in params (booleans accept
// 1/0/true/false/on/off, ints a decimal number, names the raw string) and return "ok".
// Unknown names return ActionNotImplemented.
var oasisActions = []string{
	// identity / telemetry (read)
	"serial", "model", "hardwareversion", "firmwareversion", "firmwarebuilddate",
	"protocolversion", "temperatureinternal", "temperatureexternal", "config",
	// config (read)
	"backlash", "backlashdirection", "reverse", "speed",
	"beeponmove", "beeponstartup", "bluetoothon",
	"heatingon", "heatingtemperature", "stalldetection", "usbpowercapacity",
	"friendlyname", "bluetoothname",
	// config (write)
	"setbacklash", "setbacklashdirection", "setreverse", "setspeed",
	"setbeeponmove", "setbeeponstartup", "setmaxstep",
	"setheatingon", "setheatingtemperature", "setstalldetection", "setusbpowercapacity",
	"setfriendlyname", "setbluetoothname",
	// motion / position (not in IFocuserV3)
	"movein", "moveout", "sync", "setzero", "clearstall",
	// destructive (guarded)
	"factoryreset",
}

func (f *OasisFocuser) SupportedActions() []string { return oasisActions }

// Action dispatches a device-specific command to the oasisfoc library, serialized on the
// driver mutex.
func (f *OasisFocuser) Action(name, params string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d := f.dev
	if d == nil {
		return "", alpacadev.ErrNotConnected
	}

	switch strings.ToLower(strings.TrimSpace(name)) {

	// --- identity / telemetry (read) ---
	case "serial":
		return d.Serial()
	case "model":
		return d.Model(), nil
	case "hardwareversion":
		return d.HardwareVersion(), nil
	case "firmwareversion":
		return d.FirmwareVersion(), nil
	case "firmwarebuilddate":
		return d.FirmwareBuildDate(), nil
	case "protocolversion":
		return d.ProtocolVersion(), nil
	case "temperatureinternal":
		return floatStr(d.TemperatureInternal())
	case "temperatureexternal":
		return floatStr(d.TemperatureExternal())
	case "config":
		return f.dumpConfig(d)

	// --- config (read) ---
	case "backlash":
		return cfgInt(d, func(c oasisConfig) int { return int(c.Backlash) })
	case "backlashdirection":
		return cfgInt(d, func(c oasisConfig) int { return c.BacklashDirection })
	case "reverse":
		return cfgBool(d, func(c oasisConfig) int { return c.ReverseDirection })
	case "speed":
		return cfgInt(d, func(c oasisConfig) int { return c.Speed })
	case "beeponmove":
		return cfgBool(d, func(c oasisConfig) int { return c.BeepOnMove })
	case "beeponstartup":
		return cfgBool(d, func(c oasisConfig) int { return c.BeepOnStartup })
	case "bluetoothon":
		return cfgBool(d, func(c oasisConfig) int { return c.BluetoothOn })
	case "heatingon":
		return extBool(d, func(e oasisExt) int { return e.HeatingOn })
	case "heatingtemperature":
		return extInt(d, func(e oasisExt) int { return int(e.HeatingTemperature) })
	case "stalldetection":
		return extBool(d, func(e oasisExt) int { return e.StallDetection })
	case "usbpowercapacity":
		return extInt(d, func(e oasisExt) int { return int(e.UsbPowerCapacity) })
	case "friendlyname":
		return d.FriendlyName()
	case "bluetoothname":
		return d.BluetoothName()

	// --- config (write) ---
	case "setbacklash":
		return setI(params, func(n int32) error { return d.SetBacklash(n) })
	case "setbacklashdirection":
		return setI(params, func(n int32) error { return d.SetBacklashDirection(int(n)) })
	case "setreverse":
		return ok(d.SetReverseDirection(parseBool(params)))
	case "setspeed":
		return setI(params, func(n int32) error { return d.SetSpeed(int(n)) })
	case "setbeeponmove":
		return ok(d.SetBeepOnMove(parseBool(params)))
	case "setbeeponstartup":
		return ok(d.SetBeepOnStartup(parseBool(params)))
	case "setmaxstep":
		return setI(params, func(n int32) error { return d.SetMaxStep(n) })
	case "setheatingon":
		return ok(d.SetHeatingOn(parseBool(params)))
	case "setheatingtemperature":
		return setI(params, func(n int32) error { return d.SetHeatingTemperature(n) })
	case "setstalldetection":
		return ok(d.SetStallDetection(parseBool(params)))
	case "setusbpowercapacity":
		return setI(params, func(n int32) error { return d.SetUsbPowerCapacity(n) })
	case "setfriendlyname":
		return ok(d.SetFriendlyName(params))
	case "setbluetoothname":
		return ok(d.SetBluetoothName(params))

	// --- motion / position ---
	case "movein":
		return setI(params, func(n int32) error { return d.MoveIn(n) })
	case "moveout":
		return setI(params, func(n int32) error { return d.MoveOut(n) })
	case "sync":
		return setI(params, func(n int32) error { return d.SyncPosition(n) })
	case "setzero":
		return ok(d.SetZeroPosition())
	case "clearstall":
		return ok(d.ClearStall())

	// --- destructive (guarded) ---
	case "factoryreset":
		if strings.ToLower(strings.TrimSpace(params)) != "confirm" {
			return "", fmt.Errorf("%w: factoryreset requires params=confirm", alpacadev.ErrInvalidValue)
		}
		return ok(d.FactoryReset())
	}

	return "", alpacadev.ErrActionNotImplemented
}

// dumpConfig returns the whole config block as a key=value string (one read of each part).
func (f *OasisFocuser) dumpConfig(d oasisDev) (string, error) {
	c, err := d.Config()
	if err != nil {
		return "", err
	}
	e, err := d.ExtConfig()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("maxStep=%d backlash=%d backlashDir=%d reverse=%d speed=%d "+
		"beepOnMove=%d beepOnStartup=%d bluetoothOn=%d stall=%d heatingOn=%d "+
		"heatingTemp=%d usbPower=%d",
		c.MaxStep, c.Backlash, c.BacklashDirection, c.ReverseDirection, c.Speed,
		c.BeepOnMove, c.BeepOnStartup, c.BluetoothOn, e.StallDetection, e.HeatingOn,
		e.HeatingTemperature, e.UsbPowerCapacity), nil
}

// --- small helpers ---

func ok(err error) (string, error) {
	if err != nil {
		return "", err
	}
	return "ok", nil
}

func floatStr(v float64, err error) (string, error) {
	if err != nil {
		return "", err
	}
	return strconv.FormatFloat(v, 'f', 2, 64), nil
}

func setI(params string, fn func(int32) error) (string, error) {
	n, err := strconv.ParseInt(strings.TrimSpace(params), 10, 32)
	if err != nil {
		return "", fmt.Errorf("%w: expected an integer, got %q", alpacadev.ErrInvalidValue, params)
	}
	return ok(fn(int32(n)))
}

func parseBool(params string) bool {
	switch strings.ToLower(strings.TrimSpace(params)) {
	case "1", "true", "on", "yes", "enable", "enabled":
		return true
	}
	return false
}

func boolStr(i int) string {
	if i != 0 {
		return "true"
	}
	return "false"
}

func cfgInt(d oasisDev, pick func(oasisConfig) int) (string, error) {
	c, err := d.Config()
	if err != nil {
		return "", err
	}
	return strconv.Itoa(pick(c)), nil
}

func cfgBool(d oasisDev, pick func(oasisConfig) int) (string, error) {
	c, err := d.Config()
	if err != nil {
		return "", err
	}
	return boolStr(pick(c)), nil
}

func extInt(d oasisDev, pick func(oasisExt) int) (string, error) {
	e, err := d.ExtConfig()
	if err != nil {
		return "", err
	}
	return strconv.Itoa(pick(e)), nil
}

func extBool(d oasisDev, pick func(oasisExt) int) (string, error) {
	e, err := d.ExtConfig()
	if err != nil {
		return "", err
	}
	return boolStr(pick(e)), nil
}
