package driver

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/mikefsq/astromi.ch/mgpbox"
	alpacadev "github.com/mikefsq/goalpaca/server"
)

// Every measurement the MGPBox makes is reachable as a scalar Action: the weather values
// (also the standard ObservingConditions properties), the dew-heater state, the GPS fix
// fields, and the calibration values. GPS and calibration additionally have a whole-object
// JSON action ("gps", "calibration"). The GPS receiver and calibration data have no home in
// ASCOM ObservingConditions, so the Action seam is the only route for them.
//
// Naming: SupportedActions advertises CamelCase names, but the dispatch matches
// case-insensitively (it lowercases the incoming name before the switch), so a client may
// send "Temperature", "temperature", or "TEMPERATURE". This is a driver choice, not an
// Alpaca requirement — ASCOM action names are free-form strings. Getters reject a params
// value (only MountFeed and GpsEnable take one) and return a plain string; "Gps"/
// "Calibration" return JSON; RebootGps returns "ok". Unknown names return ActionNotImplemented.
// mgpActions is advertised in CamelCase; the dispatch matches case-insensitively (it
// lowercases the incoming name), so a client may send any casing.
var mgpActions = []string{
	// Weather scalars (also exposed as the standard ObservingConditions properties;
	// duplicated here so every measurement is reachable through a uniform scalar Action).
	"Temperature", "Humidity", "Pressure", "DewPoint",
	// dew heater
	"DewOffset", "DewPWM",
	// GPS scalars
	"Latitude", "Longitude", "Altitude", "Satellites", "FixQuality", "FixType",
	"PDOP", "HDOP", "VDOP", "GpsTime",
	// GPS fix as one JSON object
	"Gps",
	// calibration: scalars + one JSON object
	"Pcal", "Tcal", "Hcal", "Calibration",
	// GPS control
	"GpsEnable", "RebootGps",
	// mount environment feed (configure / trigger)
	"MountFeed", "PushMount",
}

// SupportedActions lists the device-specific Action names (the mgpActions set above).
func (m *MGPBox) SupportedActions() []string { return mgpActions }

// gpsJSON is the "gps" action payload: the whole GPS fix, with Valid telling the client
// whether the box has a fix yet (latitude/longitude are 0 until it does).
type gpsJSON struct {
	Valid      bool    `json:"valid"`
	Time       string  `json:"time,omitempty"` // RFC3339 UTC, when the fix carries a time
	Latitude   float64 `json:"latitude"`
	Longitude  float64 `json:"longitude"`
	Altitude   float64 `json:"altitude"`
	Satellites int     `json:"satellites"`
	FixQuality string  `json:"fixQuality"`
	FixType    string  `json:"fixType"`
	PDOP       float64 `json:"pdop"`
	HDOP       float64 `json:"hdop"`
	VDOP       float64 `json:"vdop"`
}

// Action dispatches a device-specific Action by name (matched case-insensitively). Only
// MountFeed and GpsEnable take a params value; every other action is a read-only getter or
// a no-arg trigger and rejects params.
func (m *MGPBox) Action(name, params string) (string, error) {
	lname := strings.ToLower(strings.TrimSpace(name))

	// MountFeed is the only action that takes a value (host:port to set, empty to read).
	// Everything else is a read-only getter or a no-arg trigger, so a params value is a
	// client error (put/empty = read, per the goalpaca convention).
	if strings.TrimSpace(params) != "" && lname != "mountfeed" && lname != "gpsenable" {
		return "", alpacadev.NewError(alpacadev.ErrNumInvalidValue, "action takes no value")
	}

	// Feed configuration/trigger first — these don't require an attached device.
	switch lname {
	case "mountfeed":
		return m.actionMountFeed(params)
	case "pushmount":
		return m.pushEnvironment(context.Background())
	}

	dev, err := m.device()
	if err != nil {
		return "", err
	}
	me, _ := dev.Meteo()
	fx, _ := dev.Fix()
	c, _ := dev.Calibration()

	switch lname {

	// --- weather scalars (mirror the ObservingConditions properties) ---
	case "temperature":
		return fnum(me.Temperature), nil
	case "humidity":
		return fnum(me.Humidity), nil
	case "pressure":
		return fnum(me.Pressure), nil
	case "dewpoint":
		return fnum(me.Dewpoint), nil

	// --- dew heater ---
	case "dewoffset":
		return strconv.Itoa(me.DewOffset), nil
	case "dewpwm":
		return strconv.Itoa(me.DewPWM), nil

	// --- GPS scalars ---
	case "latitude":
		return strconv.FormatFloat(fx.Latitude, 'f', 6, 64), nil
	case "longitude":
		return strconv.FormatFloat(fx.Longitude, 'f', 6, 64), nil
	case "altitude":
		return strconv.FormatFloat(fx.Altitude, 'f', 1, 64), nil
	case "satellites":
		return strconv.Itoa(fx.Satellites), nil
	case "fixquality":
		return fx.Quality, nil
	case "fixtype":
		return fx.FixType, nil
	case "pdop":
		return fnum(fx.PDOP), nil
	case "hdop":
		return fnum(fx.HDOP), nil
	case "vdop":
		return fnum(fx.VDOP), nil
	case "gpstime":
		if fx.Time.IsZero() {
			return "", nil
		}
		return fx.Time.UTC().Format(time.RFC3339), nil
	case "gps":
		return marshalFix(fx)

	// --- calibration scalars + JSON ---
	case "pcal":
		return strconv.Itoa(c.Pcal), nil
	case "tcal":
		return strconv.Itoa(c.Tcal), nil
	case "hcal":
		return strconv.Itoa(c.Hcal), nil
	case "calibration":
		b, _ := json.Marshal(c)
		return string(b), nil

	// --- GPS control ---
	case "gpsenable":
		// Dual-mode bool: empty reads the last-commanded power state (the box can't report
		// it), true/false powers the GPS on/off.
		if strings.TrimSpace(params) == "" {
			m.mu.Lock()
			on := m.gpsEnabled
			m.mu.Unlock()
			return strconv.FormatBool(on), nil
		}
		on := parseOnOff(params)
		var e error
		if on {
			e = dev.GpsOn()
		} else {
			e = dev.GpsOff()
		}
		if e != nil {
			return "", e
		}
		m.mu.Lock()
		m.gpsEnabled = on
		m.mu.Unlock()
		return strconv.FormatBool(on), nil
	case "rebootgps":
		return ok(dev.RebootGps())
	}
	return "", alpacadev.ErrActionNotImplemented
}

// fnum formats a measurement as its shortest exact decimal (e.g. 23.5, 1015.32, 2.1).
func fnum(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

// parseOnOff parses a boolean action value (true/1/on/yes = true, anything else = false).
func parseOnOff(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "on", "yes", "enable", "enabled":
		return true
	}
	return false
}

// actionMountFeed reads or sets the environment-feed target. params: empty reads it
// (returns "host:port (telescope N)" or "off"); "off"/"none"/"disable" turns it off;
// otherwise "host:port" (optionally "host:port/N" for the telescope device number) enables
// it. The feed loop picks up the change on its next tick.
func (m *MGPBox) actionMountFeed(params string) (string, error) {
	p := strings.TrimSpace(params)
	switch strings.ToLower(p) {
	case "":
		m.mu.Lock()
		addr, device := m.mountAddr, m.mountDevice
		m.mu.Unlock()
		if addr == "" {
			return "off", nil
		}
		return fmt.Sprintf("%s (telescope %d)", addr, device), nil
	case "off", "none", "disable":
		m.SetMountFeed("", 0)
		return "ok", nil
	default:
		addr, device := p, 0
		if i := strings.LastIndex(p, "/"); i >= 0 {
			if n, err := strconv.Atoi(p[i+1:]); err == nil {
				addr, device = p[:i], n
			}
		}
		m.SetMountFeed(addr, device)
		return "ok", nil
	}
}

// device returns the open library handle or ErrNotConnected.
func (m *MGPBox) device() (*mgpbox.MGPBox, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.dev == nil {
		return nil, alpacadev.ErrNotConnected
	}
	return m.dev, nil
}

// marshalFix serialises a GPS fix as the "Gps" action's JSON payload.
func marshalFix(fx mgpbox.Fix) (string, error) {
	g := gpsJSON{
		Valid: fx.HasFix, Latitude: fx.Latitude, Longitude: fx.Longitude, Altitude: fx.Altitude,
		Satellites: fx.Satellites, FixQuality: fx.Quality, FixType: fx.FixType,
		PDOP: fx.PDOP, HDOP: fx.HDOP, VDOP: fx.VDOP,
	}
	if !fx.Time.IsZero() {
		g.Time = fx.Time.UTC().Format(time.RFC3339)
	}
	b, err := json.Marshal(g)
	return string(b), err
}

// ok maps a library error to the "ok"/error Action result.
func ok(err error) (string, error) {
	if err != nil {
		return "", err
	}
	return "ok", nil
}
