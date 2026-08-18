package driver

import (
	"encoding/json"
	"strings"

	alpacadev "github.com/mikefsq/goalpaca/server"
	"github.com/mikefsq/stellarmate"
)

// The Action seam carries what ISwitchV3 has no home for: the ambient-weather
// feed that drives auto-dew, and the auto-dew ramp parameters.
//
// SetEnvironment deliberately shares the tenmicron mount's action name and
// payload schema, because they are fed by the same producer: an MGPBox (or any
// weather source) pushes one environment snapshot to every device that wants it.
// The mount consumes pressure and temperature for refraction and ignores the
// rest; the SM Pro consumes temperature, humidity and dew point for auto-dew and
// ignores the rest. A feeder therefore sends one payload shape to both, and
// unknown fields are silently tolerated rather than rejected.
var smproActions = []string{
	"SetEnvironment", // PUT/GET weather: {"temperature_c":12.3,"humidity_pct":78.5[,"dewpoint_c":8.1]}
	"SetAutoDew",     // PUT ramp: {"channel":1,"on":2,"off":10,"max":100[,"enabled":true]}
	"AutoDew",        // GET ramp + current duty for {"channel":1}
}

// SupportedActions lists the device-specific Action names.
func (s *SMProSwitch) SupportedActions() []string { return smproActions }

// environment is the SetEnvironment payload, matching the tenmicron mount's
// schema field-for-field. Every field is a pointer so a feeder sends only what it
// has, and only the present fields are applied. The site/time fields mean nothing
// to a power board and are accepted-and-ignored so one broadcast payload can
// serve both devices.
//
// DewpointC is advisory, not authoritative: see actionSetEnvironment. A feeder
// that does not compute a dew point still sends the field (as 0), so it is used
// only when plausible and otherwise derived from temperature and humidity.
type environment struct {
	TemperatureC *float64 `json:"temperature_c,omitempty"`
	HumidityPct  *float64 `json:"humidity_pct,omitempty"`
	DewpointC    *float64 `json:"dewpoint_c,omitempty"`
	PressureHPa  *float64 `json:"pressure_hpa,omitempty"` // ignored (refraction datum)
	Latitude     *float64 `json:"latitude,omitempty"`     // ignored (site datum)
	Longitude    *float64 `json:"longitude,omitempty"`    // ignored
	ElevationM   *float64 `json:"elevation_m,omitempty"`  // ignored
	Time         *string  `json:"time,omitempty"`         // ignored
}

// envReply is the SetEnvironment read: the stored conditions in the payload's own
// shape, plus how old they are and whether auto-dew has stopped steering on them.
type envReply struct {
	TemperatureC float64  `json:"temperature_c"`
	HumidityPct  float64  `json:"humidity_pct"`
	DewpointC    float64  `json:"dewpoint_c"`
	MarginC      float64  `json:"margin_c"` // temperature − dew point, the auto-dew driver
	AgeSeconds   *float64 `json:"age_seconds,omitempty"`
	Stale        bool     `json:"stale"`
}

// autoDewJSON is the SetAutoDew payload and the AutoDew reply body.
type autoDewJSON struct {
	Channel int      `json:"channel"` // 1 or 2
	Enabled *bool    `json:"enabled,omitempty"`
	On      *float64 `json:"on,omitempty"`      // margin °C at/below which duty = Max
	Off     *float64 `json:"off,omitempty"`     // margin °C at/above which duty = 0
	Max     *int     `json:"max,omitempty"`     // duty ceiling %
	DutyPct *int     `json:"dutyPct,omitempty"` // reply only: current heater duty
}

// Action dispatches a device-specific Action by name (matched case-insensitively,
// as in the sibling mgpbox and tenmicron drivers).
func (s *SMProSwitch) Action(name, params string) (string, error) {
	b, err := s.board()
	if err != nil {
		return "", err
	}
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "setenvironment":
		return actionSetEnvironment(b, params)
	case "setautodew":
		return actionSetAutoDew(b, params)
	case "autodew":
		return actionAutoDew(b, params)
	}
	return "", alpacadev.ErrActionNotImplemented
}

// actionSetEnvironment is dual-mode, like the mount's: empty params reads the
// stored conditions, a JSON body applies the fields it carries.
func actionSetEnvironment(b *stellarmate.Board, params string) (string, error) {
	if strings.TrimSpace(params) == "" {
		return readEnvironment(b)
	}
	var env environment
	if err := json.Unmarshal([]byte(params), &env); err != nil {
		return "", alpacadev.NewError(alpacadev.ErrNumInvalidValue, "setenvironment: invalid JSON: "+err.Error())
	}

	// Merge onto the stored conditions rather than replacing them: a feeder may
	// send a pressure-only or site-only snapshot, and a blind overwrite would
	// zero the temperature and humidity — leaving a margin of the whole air
	// temperature, which reads as "bone dry" and switches the heaters off.
	w := b.Weather()
	var applied []string
	if env.TemperatureC != nil {
		w.Temperature = *env.TemperatureC
		applied = append(applied, "temperature")
	}
	if env.HumidityPct != nil {
		w.Humidity = *env.HumidityPct
		applied = append(applied, "humidity")
	}

	// Dew point: only a *usable* supplied value is trusted; otherwise it is left
	// zero and the stellarmate library derives it from temperature and humidity
	// (Magnus). Not every feeder computes one — the MGPBox does not, yet its feed
	// sends the field regardless, so it arrives as a literal 0. Taken at face
	// value that is a margin of the whole air temperature: "bone dry", heaters
	// off, on exactly the night they are needed. A dew point above the air
	// temperature is likewise unphysical (a broken or mis-scaled sensor); ignore
	// it and derive, which errs toward heating rather than toward a dewed optic.
	w.Dewpoint = 0
	if dp := env.DewpointC; dp != nil && *dp != 0 && *dp <= w.Temperature {
		w.Dewpoint = *dp
		applied = append(applied, "dewpoint")
	}

	if len(applied) == 0 {
		// Nothing we consume (a pressure/site/time-only push, e.g. from a feeder
		// whose GPS has a fix but whose meteo sensor has no sample yet). Leave
		// the conditions — and therefore the heaters — exactly as they are.
		return marshalApplied(nil)
	}
	// A margin needs a dew point, and a dew point needs either a usable supplied
	// value or a humidity to derive one from. Temperature alone is not actionable:
	// storing it with no dew point would again read as a huge margin. Hold instead.
	if w.Humidity <= 0 && w.Dewpoint == 0 {
		return marshalApplied(nil)
	}
	if err := b.SetWeather(w); err != nil {
		return "", alpacadev.NewError(alpacadev.ErrNumInvalidValue, "setenvironment: "+err.Error())
	}
	return marshalApplied(applied)
}

func marshalApplied(applied []string) (string, error) {
	if applied == nil {
		applied = []string{}
	}
	out, err := json.Marshal(struct {
		Applied []string `json:"applied"`
	}{Applied: applied})
	return string(out), err
}

func readEnvironment(b *stellarmate.Board) (string, error) {
	w := b.Weather()
	r := envReply{
		TemperatureC: w.Temperature,
		HumidityPct:  w.Humidity,
		DewpointC:    w.Dewpoint,
		MarginC:      b.DewMargin(),
		Stale:        b.WeatherStale(),
	}
	if age, ok := b.WeatherAge(); ok {
		secs := age.Seconds()
		r.AgeSeconds = &secs
	}
	out, err := json.Marshal(r)
	return string(out), err
}

// chanIndex maps the payload's 1-based channel to the library's 0-based one.
func chanIndex(ch int) (int, error) {
	if ch < 1 || ch > 2 {
		return 0, alpacadev.NewError(alpacadev.ErrNumInvalidValue, "channel must be 1 or 2")
	}
	return ch - 1, nil
}

func actionSetAutoDew(b *stellarmate.Board, params string) (string, error) {
	var a autoDewJSON
	if err := json.Unmarshal([]byte(params), &a); err != nil {
		return "", alpacadev.NewError(alpacadev.ErrNumInvalidValue, "setautodew: invalid JSON: "+err.Error())
	}
	ch, err := chanIndex(a.Channel)
	if err != nil {
		return "", err
	}
	cfg, err := b.AutoDew(ch)
	if err != nil {
		return "", err
	}
	// Absent fields keep their current values, so a client can flip Enabled
	// without re-stating the ramp.
	if a.Enabled != nil {
		cfg.Enabled = *a.Enabled
	}
	if a.On != nil {
		cfg.On = *a.On
	}
	if a.Off != nil {
		cfg.Off = *a.Off
	}
	if a.Max != nil {
		cfg.Max = *a.Max
	}
	if err := b.SetAutoDew(ch, cfg); err != nil {
		return "", err
	}
	return "ok", nil
}

func actionAutoDew(b *stellarmate.Board, params string) (string, error) {
	var a autoDewJSON
	if err := json.Unmarshal([]byte(params), &a); err != nil {
		return "", alpacadev.NewError(alpacadev.ErrNumInvalidValue, "autodew: invalid JSON: "+err.Error())
	}
	ch, err := chanIndex(a.Channel)
	if err != nil {
		return "", err
	}
	cfg, err := b.AutoDew(ch)
	if err != nil {
		return "", err
	}
	duty, _ := b.DewDuty(ch)
	out, err := json.Marshal(autoDewJSON{
		Channel: a.Channel, Enabled: &cfg.Enabled, On: &cfg.On, Off: &cfg.Off,
		Max: &cfg.Max, DutyPct: &duty,
	})
	return string(out), err
}
