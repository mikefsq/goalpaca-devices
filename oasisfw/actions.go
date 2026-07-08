package driver

import (
	"fmt"
	"strconv"
	"strings"

	alpacadev "github.com/mikefsq/goalpaca/server"
	"github.com/mikefsq/oasis-astro/oasisfw"
)

// Aliases to the library types, so the action helpers read cleanly.
type (
	oasisDev    = *oasisfw.Oasis
	oasisConfig = oasisfw.Config
)

// The Oasis filter wheel exposes more than ASCOM IFilterWheel (Names/FocusOffsets/
// Position): identity, temperature, the config block (speed/autorun/bluetooth/turbo),
// calibrate, per-slot names/offsets/colors, and friendly/bluetooth names, surfaced via
// the Action seam.
//
// Conventions (goalpaca standard): names are advertised in CamelCase and matched
// case-insensitively. Config fields are single read/write actions — EMPTY params reads, a
// value writes (put/empty = read). Read-only telemetry rejects a params value. The per-slot
// actions are the exception: their read takes the slot index as params and their write takes
// "slot:value", so the index is required both ways and they stay a read + SetX pair rather
// than one dual-mode action.
var wheelActions = []string{
	// identity / telemetry (read-only)
	"Serial", "Model", "HardwareVersion", "FirmwareVersion", "FirmwareBuildDate",
	"ProtocolVersion", "Temperature", "TemperatureRaw", "Slots", "State", "Config",
	// config (read/write: empty reads, a value writes)
	"Speed", "Autorun", "BluetoothOn", "Turbo", "FriendlyName", "BluetoothName",
	// per-slot (read takes slot index; SetX takes "slot:value")
	"SlotName", "FocusOffset", "Color",
	"SetSlotName", "SetFocusOffset", "SetColor",
	// maintenance
	"Calibrate",
	// destructive (guarded)
	"FactoryReset",
}

// SupportedActions lists the device-specific Action names (the wheelActions set above).
func (w *OasisWheel) SupportedActions() []string { return wheelActions }

// Action dispatches a device-specific command to the oasisfw library, serialized on the
// driver mutex. Config fields are read/write (empty reads, a value writes); telemetry is
// read-only; per-slot actions carry the slot index in params.
func (w *OasisWheel) Action(name, params string) (string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	d := w.dev
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
	case "temperature":
		return ro(params, func() (string, error) { return floatStr(d.Temperature()) }) // °C
	case "temperatureraw":
		return ro(params, func() (string, error) { return intStr32(d.TemperatureRaw()) })
	case "slots":
		return ro(params, func() (string, error) { return intStr(d.Slots()) })
	case "state":
		return ro(params, func() (string, error) { return intStr(d.State()) })
	case "config":
		return ro(params, func() (string, error) {
			c, err := d.Config()
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("speed=%d autorun=%d bluetoothOn=%d turbo=%d",
				c.Speed, c.Autorun, c.BluetoothOn, c.Turbo), nil
		})

	// --- config (read/write: empty reads, a value writes) ---
	case "speed":
		return rwI(params, cfgField(d, func(c oasisConfig) int { return c.Speed }),
			func(n int32) error { return d.SetSpeed(int(n)) })
	case "autorun":
		return rwB(params, cfgBoolField(d, func(c oasisConfig) int { return c.Autorun }), d.SetAutorun)
	case "bluetoothon":
		return rwB(params, cfgBoolField(d, func(c oasisConfig) int { return c.BluetoothOn }), d.SetBluetoothOn)
	case "turbo":
		return rwB(params, cfgBoolField(d, func(c oasisConfig) int { return c.Turbo }), d.SetTurbo)
	case "friendlyname":
		return rwS(params, d.FriendlyName, d.SetFriendlyName)
	case "bluetoothname":
		return rwS(params, d.BluetoothName, d.SetBluetoothName)

	// --- per-slot reads (params = slot index) ---
	case "slotname":
		s, err := parseInt(params)
		if err != nil {
			return "", err
		}
		return d.SlotName(s)
	case "focusoffset":
		s, err := parseInt(params)
		if err != nil {
			return "", err
		}
		return intStr32(d.FocusOffset(s))
	case "color":
		s, err := parseInt(params)
		if err != nil {
			return "", err
		}
		c, err := d.Color(s)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%#08x", c), nil

	// --- per-slot writes (params = "slot:value") ---
	case "setslotname":
		slot, val, err := splitSlot(params)
		if err != nil {
			return "", err
		}
		return ok(d.SetSlotName(slot, val))
	case "setfocusoffset":
		slot, val, err := splitSlot(params)
		if err != nil {
			return "", err
		}
		n, e := strconv.ParseInt(strings.TrimSpace(val), 10, 32)
		if e != nil {
			return "", fmt.Errorf("%w: focus offset not an integer", alpacadev.ErrInvalidValue)
		}
		return ok(d.SetFocusOffset(slot, int32(n)))
	case "setcolor":
		slot, val, err := splitSlot(params)
		if err != nil {
			return "", err
		}
		c, e := strconv.ParseUint(strings.TrimPrefix(strings.TrimSpace(val), "0x"), 16, 32)
		if e != nil {
			return "", fmt.Errorf("%w: color not hex", alpacadev.ErrInvalidValue)
		}
		return ok(d.SetColor(slot, uint32(c)))

	// --- maintenance ---
	case "calibrate":
		return trigger(params, d.Calibrate)
	case "factoryreset":
		if strings.ToLower(strings.TrimSpace(params)) != "confirm" {
			return "", fmt.Errorf("%w: factoryreset requires params=confirm", alpacadev.ErrInvalidValue)
		}
		return ok(d.FactoryReset())
	}

	return "", alpacadev.ErrActionNotImplemented
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

// --- value helpers ---

// ok maps a library error to the standard action result ("ok" on success, else the error).
func ok(err error) (string, error) {
	if err != nil {
		return "", err
	}
	return "ok", nil
}

// intStr formats an int getter result, propagating its error.
func intStr(v int, err error) (string, error) {
	if err != nil {
		return "", err
	}
	return strconv.Itoa(v), nil
}

// intStr32 formats an int32 getter result, propagating its error.
func intStr32(v int32, err error) (string, error) {
	if err != nil {
		return "", err
	}
	return strconv.Itoa(int(v)), nil
}

// floatStr formats a float getter result to 2 decimals, propagating its error.
func floatStr(v float64, err error) (string, error) {
	if err != nil {
		return "", err
	}
	return strconv.FormatFloat(v, 'f', 2, 64), nil
}

// parseInt parses an integer params value (InvalidValue on a non-integer).
func parseInt(s string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0, fmt.Errorf("%w: expected an integer, got %q", alpacadev.ErrInvalidValue, s)
	}
	return n, nil
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

// splitSlot parses "slot:value" into the slot index and the value string.
func splitSlot(s string) (int, string, error) {
	i := strings.IndexByte(s, ':')
	if i < 0 {
		return 0, "", fmt.Errorf("%w: expected 'slot:value', got %q", alpacadev.ErrInvalidValue, s)
	}
	slot, err := strconv.Atoi(strings.TrimSpace(s[:i]))
	if err != nil {
		return 0, "", fmt.Errorf("%w: bad slot in %q", alpacadev.ErrInvalidValue, s)
	}
	return slot, s[i+1:], nil
}

// cfgField returns a getter thunk that reads one int field from the Config block — so rw*/ro
// can defer the read until dispatch decides the call is a read.
func cfgField(d oasisDev, pick func(oasisConfig) int) func() (string, error) {
	return func() (string, error) {
		c, err := d.Config()
		if err != nil {
			return "", err
		}
		return strconv.Itoa(pick(c)), nil
	}
}

// cfgBoolField returns a getter thunk that reads one bool field from the Config block.
func cfgBoolField(d oasisDev, pick func(oasisConfig) int) func() (string, error) {
	return func() (string, error) {
		c, err := d.Config()
		if err != nil {
			return "", err
		}
		return boolStr(pick(c)), nil
	}
}
