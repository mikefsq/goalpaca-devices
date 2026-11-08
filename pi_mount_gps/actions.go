package driver

import (
	"context"
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"time"

	alpacadev "github.com/mikefsq/goalpaca/server"
	pimountgps "github.com/mikefsq/x1500_gps_12v/driver"
)

// Actions expose the weather, the GPS and the environment feed. Names, formats
// and the Gps JSON match the mgpbox driver's, so a client written for one works
// with the other. Names are case-insensitive.
var piActions = []string{
	// Weather scalars (also the standard ObservingConditions properties)
	"Temperature", "Humidity", "Pressure", "DewPoint", "TemperatureOffset",
	// GPS scalars
	"Latitude", "Longitude", "Altitude", "Satellites", "FixQuality", "FixType",
	"PDOP", "HDOP", "VDOP", "GpsTime",
	// GPS fix as one JSON object
	"Gps",
	// where the time and the fix come from: chrony/gps/none, gpsd/i2c/none
	"TimeSource", "GpsSource",
	// environment feed: read or replace the target list, read each target's
	// health, and push now
	"Feed", "FeedStatus", "PushFeed",
}

// SupportedActions lists the device-specific Action names.
func (w *Driver) SupportedActions() []string { return piActions }

// gpsJSON is the "Gps" action payload, as mgpbox sends it. Valid is false while
// the fix is stale or the receiver has no position; the other fields then hold
// the last values. FixQuality and FixType are the raw NMEA digits (GGA quality
// "0" none, "1" GPS, "2" DGPS…; GSA type "1" none, "2" 2D, "3" 3D).
type gpsJSON struct {
	Valid      bool    `json:"valid"`
	Time       string  `json:"time,omitempty"` // RFC3339 UTC of the fix
	Latitude   float64 `json:"latitude"`
	Longitude  float64 `json:"longitude"`
	Altitude   float64 `json:"altitude"` // m above mean sea level
	Satellites int     `json:"satellites"`
	FixQuality string  `json:"fixQuality"`
	FixType    string  `json:"fixType"`
	PDOP       float64 `json:"pdop"`
	HDOP       float64 `json:"hdop"`
	VDOP       float64 `json:"vdop"`
}

// DeviceState reports the feed's health in the Platform 7 DeviceState batch, so
// a client learns that a consumer has stopped being fed without knowing about
// this driver's Actions.
func (w *Driver) DeviceState() []alpacadev.StateValue {
	targets := w.FeedTargets()
	if len(targets) == 0 {
		return nil
	}
	return []alpacadev.StateValue{
		{Name: "FeedTargets", Value: len(targets)},
		{Name: "FeedFailures", Value: w.feedFailures()},
	}
}

// Action dispatches a device-specific Action by name. Feed and TemperatureOffset accept values;
// the others are read-only getters or a no-argument trigger. The feed actions
// work without the board; the rest need it open.
func (w *Driver) Action(name, params string) (string, error) {
	lname := strings.ToLower(strings.TrimSpace(name))
	if strings.TrimSpace(params) != "" && lname != "feed" && lname != "temperatureoffset" {
		return "", alpacadev.NewError(alpacadev.ErrNumInvalidValue, "action takes no value")
	}
	switch lname {
	case "temperatureoffset":
		return w.actionTemperatureOffset(params)
	case "feed":
		return w.actionFeed(params)
	case "feedstatus":
		b, err := json.Marshal(w.FeedHealth())
		return string(b), err
	case "pushfeed":
		// Operator-triggered: ignore backoff and try every target now.
		return w.pushEnvironmentNow(context.Background())
	case "timesource":
		w.mu.Lock()
		src := w.timeSource
		w.mu.Unlock()
		if src == "" {
			// No push yet: report what a push would use now.
			src = timeNone
			if w.clockSynced() {
				src = timeChrony
			} else if w.currentFix().valid {
				src = timeGPS
			}
		}
		return src, nil
	// Weather scalars go through the properties, so they agree with them,
	// including returning an error while the readings are stale.
	case "temperature":
		return fnum(w.Temperature())
	case "humidity":
		return fnum(w.Humidity())
	case "pressure":
		return fnum(w.Pressure())
	case "dewpoint":
		return fnum(w.DewPoint())
	}

	_, err := w.openedBoard()
	if err != nil {
		if lname == "gps" || lname == "gpssource" || isGPSScalar(lname) {
			return "", err
		}
		return "", alpacadev.ErrActionNotImplemented
	}
	g := w.currentFix()
	fx := g.fix
	switch lname {
	case "gpssource":
		return g.src, nil
	case "latitude":
		return strconv.FormatFloat(fx.Lat, 'f', 6, 64), nil
	case "longitude":
		return strconv.FormatFloat(fx.Lon, 'f', 6, 64), nil
	case "altitude":
		return strconv.FormatFloat(fx.AltM, 'f', 1, 64), nil
	case "satellites":
		return strconv.Itoa(fx.Satellites), nil
	case "fixquality":
		return strconv.Itoa(fx.Quality), nil
	case "fixtype":
		return fx.FixType, nil
	case "pdop":
		return fnum(fx.PDOP, nil)
	case "hdop":
		return fnum(fx.HDOP, nil)
	case "vdop":
		return fnum(fx.VDOP, nil)
	case "gpstime":
		if fx.Time.IsZero() {
			return "", nil
		}
		return fx.Time.UTC().Format(time.RFC3339), nil
	case "gps":
		return marshalFix(fx, g.valid)
	}
	return "", alpacadev.ErrActionNotImplemented
}

func isGPSScalar(name string) bool {
	switch name {
	case "latitude", "longitude", "altitude", "satellites", "fixquality", "fixtype",
		"pdop", "hdop", "vdop", "gpstime":
		return true
	}
	return false
}

// fnum formats a measurement as its shortest exact decimal (e.g. 23.5, 1015.32).
func fnum(v float64, err error) (string, error) {
	if err != nil {
		return "", err
	}
	return strconv.FormatFloat(v, 'f', -1, 64), nil
}

// openedBoard returns the open board or ErrNotConnected.
func (w *Driver) openedBoard() (board, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.board == nil {
		return nil, alpacadev.ErrNotConnected
	}
	return w.board, nil
}

// marshalFix serialises a fix as the "Gps" action's JSON payload.
func marshalFix(fx pimountgps.Fix, valid bool) (string, error) {
	g := gpsJSON{
		Valid: valid, Latitude: fx.Lat, Longitude: fx.Lon, Altitude: fx.AltM,
		Satellites: fx.Satellites, FixQuality: strconv.Itoa(fx.Quality), FixType: fx.FixType,
		PDOP: fx.PDOP, HDOP: fx.HDOP, VDOP: fx.VDOP,
	}
	if !fx.Time.IsZero() {
		g.Time = fx.Time.UTC().Format(time.RFC3339)
	}
	out, err := json.Marshal(g)
	return string(out), err
}

// actionFeed reads or replaces the feed target list. params: empty reads it (a
// comma-separated "host:port/type/device" list, or "off"); "off"/"none"/
// "disable" turns the feed off; otherwise a comma-separated target list, each
// "host:port[/type[/device]]". An invalid entry rejects the whole list.
func (w *Driver) actionFeed(params string) (string, error) {
	p := strings.TrimSpace(params)
	switch strings.ToLower(p) {
	case "":
		targets := w.FeedTargets()
		if len(targets) == 0 {
			return "off", nil
		}
		out := make([]string, len(targets))
		for i, t := range targets {
			out[i] = t.String()
		}
		return strings.Join(out, ","), nil
	case "off", "none", "disable":
		_ = w.SetFeedTargets(nil)
		return "ok", nil
	default:
		targets, err := ParseFeedTargets(p)
		if err != nil {
			return "", alpacadev.NewError(alpacadev.ErrNumInvalidValue, err.Error())
		}
		if err := w.SetFeedTargets(targets); err != nil {
			return "", alpacadev.NewError(alpacadev.ErrNumInvalidValue, err.Error())
		}
		return "ok", nil
	}
}

// Empty parameters read the value; a number sets enclosure heating in Celsius.
// This changes runtime state only; the configuration file supplies startup state.
func (w *Driver) actionTemperatureOffset(params string) (string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if value := strings.TrimSpace(params); value != "" {
		heat, err := strconv.ParseFloat(value, 64)
		if err != nil || math.IsNaN(heat) || math.IsInf(heat, 0) || heat < 0 {
			return "", alpacadev.NewError(alpacadev.ErrNumInvalidValue, "temperature offset must be finite and non-negative (°C of enclosure heating)")
		}
		if setter, ok := w.board.(interface{ SetEnclosureHeating(float64) error }); ok {
			if err := setter.SetEnclosureHeating(heat); err != nil {
				return "", alpacadev.NewError(alpacadev.ErrNumInvalidValue, err.Error())
			}
		}
		w.temperatureOffset = heat
	}
	return strconv.FormatFloat(w.temperatureOffset, 'f', -1, 64), nil
}
