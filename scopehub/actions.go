package driver

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	alpacadev "github.com/mikefsq/goalpaca/server"
	scopehub "github.com/mikefsq/scope_hub/driver"
)

// Device Actions. Names match any case.
var actions = []string{
	"SaveSettings",    // write live settings to flash, applied at power-on. Returns "ok"
	"RestoreDefaults", // restore defaults in RAM; SaveSettings makes them permanent. Returns "ok"
	"Status",          // every reading as JSON: {"status":...,"dew":...,"aux":...}
	"SwitchLayout",    // switch page layout as JSON; see layout.go
}

func (s *Switch) SupportedActions() []string { return actions }

func (s *Switch) Action(name, params string) (string, error) {
	if !s.Connected() {
		return "", alpacadev.ErrNotConnected
	}
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "savesettings":
		if err := s.b.Do(func(d device) error { return d.Save() }); err != nil {
			return "", err
		}
		return "ok", nil
	case "restoredefaults":
		if err := s.b.Do(func(d device) error { return d.Defaults() }); err != nil {
			return "", err
		}
		return "ok", nil
	case "switchlayout":
		return switchLayoutJSON, nil
	case "status":
		n, err := s.b.Snapshot()
		if err != nil {
			return "", err
		}
		out, err := json.Marshal(struct {
			Status *scopehub.Status `json:"status"`
			Dew    *scopehub.Dew    `json:"dew"`
			Aux    *scopehub.Aux    `json:"aux"`
		}{n.st, n.dew, n.aux})
		return string(out), err
	}
	return "", alpacadev.ErrActionNotImplemented
}

// ObservingConditions Actions. Names and formats match pi_mount_gps, so its
// clients read this board too.
var ocActions = []string{
	"Temperature", "Humidity", "Pressure", "DewPoint", // weather scalars, same as properties
	"Latitude", "Longitude", "Altitude", "Satellites", "FixQuality", "GpsTime", // GPS scalars
	"Gps", // whole GPS fix as JSON
}

func (o *ObservingConditions) SupportedActions() []string { return ocActions }

// gpsJSON is "Gps" action payload, pi_mount_gps format. NEO-M9N report has no
// GSA mode or dilutions, so FixType stays "" and DOPs 0.
type gpsJSON struct {
	Valid      bool    `json:"valid"`
	Time       string  `json:"time,omitempty"` // fix time, RFC3339 UTC
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

// gpsTime joins RMC date (ddmmyy) and UTC (hhmmss.ss) as RFC3339. "" when
// either missing. Fractional seconds dropped.
func gpsTime(g scopehub.GPS) string {
	if len(g.Date) != 6 || len(g.UTC) < 6 {
		return ""
	}
	t, err := time.Parse("020106150405", g.Date+g.UTC[:6])
	if err != nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func (o *ObservingConditions) Action(name, params string) (string, error) {
	if strings.TrimSpace(params) != "" {
		return "", alpacadev.NewError(alpacadev.ErrNumInvalidValue, "action takes no value")
	}
	lname := strings.ToLower(strings.TrimSpace(name))
	switch lname {
	// Weather scalars go through properties, so values agree.
	case "temperature":
		return fnum(o.Temperature())
	case "humidity":
		return fnum(o.Humidity())
	case "pressure":
		return fnum(o.Pressure())
	case "dewpoint":
		return fnum(o.DewPoint())
	case "latitude", "longitude", "altitude", "satellites", "fixquality", "gpstime", "gps":
	default:
		return "", alpacadev.ErrActionNotImplemented
	}
	n, err := o.snapshot()
	if err != nil {
		return "", err
	}
	g := n.st.GPS
	switch lname {
	case "latitude":
		return strconv.FormatFloat(g.LatDeg, 'f', 6, 64), nil
	case "longitude":
		return strconv.FormatFloat(g.LonDeg, 'f', 6, 64), nil
	case "altitude":
		return strconv.FormatFloat(g.AltM, 'f', 1, 64), nil
	case "satellites":
		return strconv.Itoa(g.Satellites), nil
	case "fixquality":
		return strconv.Itoa(g.Fix), nil
	case "gpstime":
		return gpsTime(g), nil
	}
	// Valid needs live NMEA and GGA fix. Else fields hold last values.
	out, err := json.Marshal(gpsJSON{
		Valid: g.Seen && g.Fix > 0, Time: gpsTime(g),
		Latitude: g.LatDeg, Longitude: g.LonDeg, Altitude: g.AltM,
		Satellites: g.Satellites, FixQuality: strconv.Itoa(g.Fix),
	})
	return string(out), err
}

// fnum formats a measurement as shortest exact decimal (23.5, 1015.32).
func fnum(v float64, err error) (string, error) {
	if err != nil {
		return "", err
	}
	return strconv.FormatFloat(v, 'f', -1, 64), nil
}
