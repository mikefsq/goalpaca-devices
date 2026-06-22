package driver

import (
	"encoding/json"
	"strconv"
	"strings"

	alpacadev "github.com/mikefsq/goalpaca/server"
)

// This file exposes the device-specific ASCOM Actions another driver (e.g. an
// environment/GPS feeder) calls over the standard stateless Alpaca port to push
// observing-site data into the mount. Site latitude/longitude/elevation and UTC
// date are already standard ASCOM Telescope members (sitelatitude, sitelongitude,
// siteelevation, utcdate) — a caller can PUT those directly. Refraction pressure
// and temperature are NOT standard Telescope members (they belong to the mount's
// refraction model), so they are exposed here as Actions. The setenvironment
// Action is a one-call convenience that applies any subset of all six fields.

func (t *Telescope) SupportedActions() []string {
	return []string{"setenvironment", "setrefractionpressure", "setrefractiontemperature", "setoptics", "dualaxistracking"}
}

func (t *Telescope) Action(name, params string) (string, error) {
	switch strings.ToLower(name) {
	case "setenvironment":
		return t.actionSetEnvironment(params)
	case "setoptics":
		return t.actionSetOptics(params)
	case "dualaxistracking":
		return t.actionDualAxisTracking(params)
	case "setrefractionpressure":
		v, err := strconv.ParseFloat(strings.TrimSpace(params), 64)
		if err != nil {
			return "", alpacadev.NewError(alpacadev.ErrNumInvalidValue, "setrefractionpressure: want a number (hPa)")
		}
		m := t.mount()
		if m == nil {
			return "", alpacadev.ErrNotConnected
		}
		return "", m.SetRefractionPressure(v)
	case "setrefractiontemperature":
		v, err := strconv.ParseFloat(strings.TrimSpace(params), 64)
		if err != nil {
			return "", alpacadev.NewError(alpacadev.ErrNumInvalidValue, "setrefractiontemperature: want a number (°C)")
		}
		m := t.mount()
		if m == nil {
			return "", alpacadev.ErrNotConnected
		}
		return "", m.SetRefractionTemperature(v)
	default:
		return "", alpacadev.NewError(alpacadev.ErrNumNotImplemented, "unknown action "+name)
	}
}

// actionDualAxisTracking reads or sets dual-axis tracking (:Gdat#/:SdatN#) — the mount
// driving BOTH axes to follow the refraction/pointing model. There is no standard ASCOM
// Telescope member for it, so it is exposed as an Action. params: empty or "?" reads and
// returns "true"/"false"; "true"/"false" (also 1/0, on/off) sets it. Disabling is
// equatorial-only — an AltAz mount rejects it (error surfaced to the caller).
func (t *Telescope) actionDualAxisTracking(params string) (string, error) {
	m := t.mount()
	if m == nil {
		return "", alpacadev.ErrNotConnected
	}
	switch strings.TrimSpace(strings.ToLower(params)) {
	case "", "?", "get":
		on, err := m.DualAxisTracking()
		if err != nil {
			return "", err
		}
		return strconv.FormatBool(on), nil
	case "true", "1", "on":
		return "", m.SetDualAxisTracking(true)
	case "false", "0", "off":
		return "", m.SetDualAxisTracking(false)
	default:
		return "", alpacadev.NewError(alpacadev.ErrNumInvalidValue, "dualaxistracking: want true/false (or empty to read)")
	}
}

// environment is the setenvironment payload. Every field is optional (a pointer);
// only present fields are pushed to the mount, so a feeder sends just what changed.
type environment struct {
	PressureHPa  *float64 `json:"pressure_hpa,omitempty"`
	TemperatureC *float64 `json:"temperature_c,omitempty"`
	Latitude     *float64 `json:"latitude,omitempty"`  // degrees, East-positive (ASCOM)
	Longitude    *float64 `json:"longitude,omitempty"` // degrees, East-positive
	ElevationM   *float64 `json:"elevation_m,omitempty"`
	Time         *string  `json:"time,omitempty"` // RFC3339 (e.g. 2026-06-02T15:04:05Z)
}

// actionSetEnvironment applies the present fields of the JSON payload to the mount:
// refraction pressure/temperature (mount refraction model) and, reusing the
// standard ASCOM setters, site latitude/longitude/elevation and UTC date. Returns
// a JSON object listing which fields were applied. Updating the refraction datums
// does not by itself enable refraction — set DoesRefraction (the standard member)
// for that.
func (t *Telescope) actionSetEnvironment(params string) (string, error) {
	var env environment
	if err := json.Unmarshal([]byte(params), &env); err != nil {
		return "", alpacadev.NewError(alpacadev.ErrNumInvalidValue, "setenvironment: invalid JSON: "+err.Error())
	}

	var applied []string
	if env.PressureHPa != nil || env.TemperatureC != nil {
		m := t.mount()
		if m == nil {
			return "", alpacadev.ErrNotConnected
		}
		if env.PressureHPa != nil {
			if err := m.SetRefractionPressure(*env.PressureHPa); err != nil {
				return "", err
			}
			applied = append(applied, "pressure")
		}
		if env.TemperatureC != nil {
			if err := m.SetRefractionTemperature(*env.TemperatureC); err != nil {
				return "", err
			}
			applied = append(applied, "temperature")
		}
	}
	if env.Latitude != nil {
		if err := t.SetSiteLatitude(*env.Latitude); err != nil {
			return "", err
		}
		applied = append(applied, "latitude")
	}
	if env.Longitude != nil {
		if err := t.SetSiteLongitude(*env.Longitude); err != nil {
			return "", err
		}
		applied = append(applied, "longitude")
	}
	if env.ElevationM != nil {
		if err := t.SetSiteElevation(*env.ElevationM); err != nil {
			return "", err
		}
		applied = append(applied, "elevation")
	}
	if env.Time != nil {
		if err := t.SetUTCDate(*env.Time); err != nil {
			return "", err
		}
		applied = append(applied, "time")
	}

	out, _ := json.Marshal(struct {
		Applied []string `json:"applied"`
	}{Applied: applied})
	return string(out), nil
}
