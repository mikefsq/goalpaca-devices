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

// The Oasis filter wheel exposes more than the ASCOM IFilterWheel interface (Names /
// FocusOffsets / Position) has slots for: identity, temperature, the config block
// (speed/autorun/bluetooth/turbo), calibrate, per-slot names/offsets/colors, and the
// friendly/bluetooth names. Those are surfaced via the ASCOM Action seam.
//
// Convention: action names are lowercase. Getters ignore params and return a string
// ("true"/"false" for booleans). Setters take the value in params — booleans accept
// 1/0/true/false/on/off, ints a decimal number, names the raw string; per-slot setters
// take "slot:value" (e.g. setslotname=1:Ha, setcolor=0:00ff00). Setters return "ok".
var wheelActions = []string{
	// identity / telemetry (read)
	"serial", "model", "hardwareversion", "firmwareversion", "firmwarebuilddate",
	"protocolversion", "temperature", "temperatureraw", "slots", "state", "config",
	// config (read)
	"speed", "autorun", "bluetoothon", "turbo", "friendlyname", "bluetoothname",
	// per-slot (read) — params = slot index
	"slotname", "focusoffset", "color",
	// config (write)
	"setspeed", "setautorun", "setbluetoothon", "setturbo",
	"setfriendlyname", "setbluetoothname",
	// per-slot (write) — params = "slot:value"
	"setslotname", "setfocusoffset", "setcolor",
	// motion / maintenance
	"calibrate", "factoryreset",
}

func (w *OasisWheel) SupportedActions() []string { return wheelActions }

func (w *OasisWheel) Action(name, params string) (string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	d := w.dev
	if d == nil {
		return "", alpacadev.ErrNotConnected
	}

	switch strings.ToLower(strings.TrimSpace(name)) {

	// --- identity / telemetry ---
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
	case "temperature":
		return floatStr(d.Temperature()) // °C
	case "temperatureraw":
		return intStr32(d.TemperatureRaw())
	case "slots":
		return intStr(d.Slots())
	case "state":
		return intStr(d.State())
	case "config":
		c, err := d.Config()
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("speed=%d autorun=%d bluetoothOn=%d turbo=%d",
			c.Speed, c.Autorun, c.BluetoothOn, c.Turbo), nil
	case "friendlyname":
		return d.FriendlyName()
	case "bluetoothname":
		return d.BluetoothName()

	// --- config fields (read) ---
	case "speed":
		return cfgField(d, func(c oasisConfig) int { return c.Speed })
	case "autorun":
		return cfgBoolField(d, func(c oasisConfig) int { return c.Autorun })
	case "bluetoothon":
		return cfgBoolField(d, func(c oasisConfig) int { return c.BluetoothOn })
	case "turbo":
		return cfgBoolField(d, func(c oasisConfig) int { return c.Turbo })

	// --- per-slot reads (params = slot) ---
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

	// --- config writes ---
	case "setspeed":
		return setI(params, func(n int32) error { return d.SetSpeed(int(n)) })
	case "setautorun":
		return ok(d.SetAutorun(parseBool(params)))
	case "setbluetoothon":
		return ok(d.SetBluetoothOn(parseBool(params)))
	case "setturbo":
		return ok(d.SetTurbo(parseBool(params)))
	case "setfriendlyname":
		return ok(d.SetFriendlyName(params))
	case "setbluetoothname":
		return ok(d.SetBluetoothName(params))

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
		return ok(d.Calibrate())
	case "factoryreset":
		if strings.ToLower(strings.TrimSpace(params)) != "confirm" {
			return "", fmt.Errorf("%w: factoryreset requires params=confirm", alpacadev.ErrInvalidValue)
		}
		return ok(d.FactoryReset())
	}

	return "", alpacadev.ErrActionNotImplemented
}

// --- helpers ---

func ok(err error) (string, error) {
	if err != nil {
		return "", err
	}
	return "ok", nil
}

func intStr(v int, err error) (string, error) {
	if err != nil {
		return "", err
	}
	return strconv.Itoa(v), nil
}

func intStr32(v int32, err error) (string, error) {
	if err != nil {
		return "", err
	}
	return strconv.Itoa(int(v)), nil
}

func floatStr(v float64, err error) (string, error) {
	if err != nil {
		return "", err
	}
	return strconv.FormatFloat(v, 'f', 2, 64), nil
}

func parseInt(s string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0, fmt.Errorf("%w: expected an integer, got %q", alpacadev.ErrInvalidValue, s)
	}
	return n, nil
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

func cfgField(d oasisDev, pick func(oasisConfig) int) (string, error) {
	c, err := d.Config()
	if err != nil {
		return "", err
	}
	return strconv.Itoa(pick(c)), nil
}

func cfgBoolField(d oasisDev, pick func(oasisConfig) int) (string, error) {
	c, err := d.Config()
	if err != nil {
		return "", err
	}
	return boolStr(pick(c)), nil
}
