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
// surfaced via the Action seam.
//
// Conventions (goalpaca standard): names are advertised in CamelCase and matched
// case-insensitively. Config fields are single read/write actions — EMPTY params reads the
// current value, a value writes it (put/empty = read). Read-only telemetry rejects a params
// value; no-arg operations reject a params value; operations that take an argument (MoveIn/
// MoveOut/Sync) require it. Booleans read back as "true"/"false" and accept
// 1/0/true/false/on/off on write.
var oasisActions = []string{
	// identity / telemetry (read-only)
	"Serial", "Model", "HardwareVersion", "FirmwareVersion", "FirmwareBuildDate",
	"ProtocolVersion", "TemperatureInternal", "TemperatureExternal", "Config", "BluetoothOn",
	// config (read/write: empty reads, a value writes)
	"Backlash", "BacklashDirection", "Reverse", "Speed", "MaxStep",
	"BeepOnMove", "BeepOnStartup", "HeatingOn", "HeatingTemperature",
	"StallDetection", "UsbPowerCapacity", "FriendlyName", "BluetoothName",
	// operations
	"MoveIn", "MoveOut", "Sync", "SetZero", "ClearStall",
	// destructive (guarded)
	"FactoryReset",
}

// SupportedActions lists the device-specific Action names (the oasisActions set above).
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

	// --- identity / telemetry (read-only) ---
	case "serial":
		return ro(params, d.Serial)
	case "model":
		return ro(params, wrap(d.Model))
	case "hardwareversion":
		return ro(params, wrap(d.HardwareVersion))
	case "firmwareversion":
		return ro(params, wrap(d.FirmwareVersion))
	case "firmwarebuilddate":
		return ro(params, wrap(d.FirmwareBuildDate))
	case "protocolversion":
		return ro(params, wrap(d.ProtocolVersion))
	case "temperatureinternal":
		return ro(params, func() (string, error) { return floatStr(d.TemperatureInternal()) })
	case "temperatureexternal":
		return ro(params, func() (string, error) { return floatStr(d.TemperatureExternal()) })
	case "config":
		return ro(params, func() (string, error) { return f.dumpConfig(d) })
	case "bluetoothon": // readable, but no library setter — read-only
		return ro(params, cfgBool(d, func(c oasisConfig) int { return c.BluetoothOn }))

	// --- config (read/write: empty reads, a value writes) ---
	case "backlash":
		return rwI(params, cfgInt(d, func(c oasisConfig) int { return int(c.Backlash) }), d.SetBacklash)
	case "backlashdirection":
		return rwI(params, cfgInt(d, func(c oasisConfig) int { return c.BacklashDirection }),
			func(n int32) error { return d.SetBacklashDirection(int(n)) })
	case "reverse":
		return rwB(params, cfgBool(d, func(c oasisConfig) int { return c.ReverseDirection }), d.SetReverseDirection)
	case "speed":
		return rwI(params, cfgInt(d, func(c oasisConfig) int { return c.Speed }),
			func(n int32) error { return d.SetSpeed(int(n)) })
	case "maxstep":
		return rwI(params, cfgInt(d, func(c oasisConfig) int { return int(c.MaxStep) }), d.SetMaxStep)
	case "beeponmove":
		return rwB(params, cfgBool(d, func(c oasisConfig) int { return c.BeepOnMove }), d.SetBeepOnMove)
	case "beeponstartup":
		return rwB(params, cfgBool(d, func(c oasisConfig) int { return c.BeepOnStartup }), d.SetBeepOnStartup)
	case "heatingon":
		return rwB(params, extBool(d, func(e oasisExt) int { return e.HeatingOn }), d.SetHeatingOn)
	case "heatingtemperature":
		return rwI(params, extInt(d, func(e oasisExt) int { return int(e.HeatingTemperature) }), d.SetHeatingTemperature)
	case "stalldetection":
		return rwB(params, extBool(d, func(e oasisExt) int { return e.StallDetection }), d.SetStallDetection)
	case "usbpowercapacity":
		return rwI(params, extInt(d, func(e oasisExt) int { return int(e.UsbPowerCapacity) }), d.SetUsbPowerCapacity)
	case "friendlyname":
		return rwS(params, d.FriendlyName, d.SetFriendlyName)
	case "bluetoothname":
		return rwS(params, d.BluetoothName, d.SetBluetoothName)

	// --- operations ---
	case "movein":
		return setI(params, d.MoveIn)
	case "moveout":
		return setI(params, d.MoveOut)
	case "sync":
		return setI(params, func(n int32) error { return d.SyncPosition(n) })
	case "setzero":
		return trigger(params, d.SetZeroPosition)
	case "clearstall":
		return trigger(params, d.ClearStall)

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

// --- dispatch helpers (shared shape across the fleet's Action drivers) ---

// ro runs a read-only getter, rejecting a params value.
func ro(params string, get func() (string, error)) (string, error) {
	if strings.TrimSpace(params) != "" {
		return "", roErr()
	}
	return get()
}

// rwI is a dual-mode int field action: EMPTY params reads via get, a value writes it via setI.
func rwI(params string, get func() (string, error), set func(int32) error) (string, error) {
	if strings.TrimSpace(params) == "" {
		return get()
	}
	return setI(params, set)
}

// rwB is the bool counterpart of rwI (empty reads via get, a value writes via set).
func rwB(params string, get func() (string, error), set func(bool) error) (string, error) {
	if strings.TrimSpace(params) == "" {
		return get()
	}
	return ok(set(parseBool(params)))
}

// rwS is the string counterpart of rwI (empty reads via get, a value writes via set).
func rwS(params string, get func() (string, error), set func(string) error) (string, error) {
	if strings.TrimSpace(params) == "" {
		return get()
	}
	return ok(set(params))
}

// trigger runs a no-arg operation, rejecting a params value.
func trigger(params string, fn func() error) (string, error) {
	if strings.TrimSpace(params) != "" {
		return "", noValErr()
	}
	return ok(fn())
}

// wrap adapts a no-error getter into the func() (string, error) shape.
func wrap(get func() string) func() (string, error) {
	return func() (string, error) { return get(), nil }
}

// roErr is the error returned when a value is passed to a read-only action.
func roErr() error {
	return alpacadev.NewError(alpacadev.ErrNumInvalidValue, "action is read-only (pass no value)")
}

// noValErr is the error returned when a value is passed to an action that takes none.
func noValErr() error {
	return alpacadev.NewError(alpacadev.ErrNumInvalidValue, "action takes no value")
}

// --- small value helpers ---

// ok maps a library error to the standard action result ("ok" on success, else the error).
func ok(err error) (string, error) {
	if err != nil {
		return "", err
	}
	return "ok", nil
}

// floatStr formats a float getter result to 2 decimals, propagating its error.
func floatStr(v float64, err error) (string, error) {
	if err != nil {
		return "", err
	}
	return strconv.FormatFloat(v, 'f', 2, 64), nil
}

// setI parses an integer params value and applies it via fn (InvalidValue on a non-integer).
func setI(params string, fn func(int32) error) (string, error) {
	n, err := strconv.ParseInt(strings.TrimSpace(params), 10, 32)
	if err != nil {
		return "", fmt.Errorf("%w: expected an integer, got %q", alpacadev.ErrInvalidValue, params)
	}
	return ok(fn(int32(n)))
}

// parseBool parses a boolean action value (1/true/on/yes/enable = true, else false).
func parseBool(params string) bool {
	switch strings.ToLower(strings.TrimSpace(params)) {
	case "1", "true", "on", "yes", "enable", "enabled":
		return true
	}
	return false
}

// boolStr renders a nonzero int (the library's bool encoding) as "true", else "false".
func boolStr(i int) string {
	if i != 0 {
		return "true"
	}
	return "false"
}

// cfgInt returns a getter thunk that reads one int field from the Config block — so rw*/ro
// can defer the mount read until dispatch decides the call is a read.
func cfgInt(d oasisDev, pick func(oasisConfig) int) func() (string, error) {
	return func() (string, error) {
		c, err := d.Config()
		if err != nil {
			return "", err
		}
		return strconv.Itoa(pick(c)), nil
	}
}

// cfgBool returns a getter thunk that reads one bool field from the Config block.
func cfgBool(d oasisDev, pick func(oasisConfig) int) func() (string, error) {
	return func() (string, error) {
		c, err := d.Config()
		if err != nil {
			return "", err
		}
		return boolStr(pick(c)), nil
	}
}

// extInt returns a getter thunk that reads one int field from the ExtConfig block.
func extInt(d oasisDev, pick func(oasisExt) int) func() (string, error) {
	return func() (string, error) {
		e, err := d.ExtConfig()
		if err != nil {
			return "", err
		}
		return strconv.Itoa(pick(e)), nil
	}
}

// extBool returns a getter thunk that reads one bool field from the ExtConfig block.
func extBool(d oasisDev, pick func(oasisExt) int) func() (string, error) {
	return func() (string, error) {
		e, err := d.ExtConfig()
		if err != nil {
			return "", err
		}
		return boolStr(pick(e)), nil
	}
}
