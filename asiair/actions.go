package driver

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/mikefsq/goasi/asiair"
	alpacadev "github.com/mikefsq/goalpaca/server"
)

// The Action seam carries what ISwitchV3 has no home for: the ambient-weather
// feed that drives auto-dew, the auto-dew ramp parameters, and the timed DSLR
// frame sequence.
//
// SetEnvironment deliberately shares the smpro and tenmicron schema field for
// field, because they are fed by the same producer: an MGPBox (or any weather
// source) pushes one environment snapshot to every device that wants it. The
// mount consumes pressure and temperature for refraction and ignores the rest;
// a power board consumes temperature, humidity and dew point for auto-dew and
// ignores the rest. A feeder therefore sends one payload shape to all of them,
// and unknown fields are tolerated rather than rejected.
var asiairActions = []string{
	"SetEnvironment", // PUT/GET weather: {"temperature_c":12.3,"humidity_pct":78.5[,"dewpoint_c":8.1]}
	"SetAutoDew",     // PUT ramp: {"port":1,"on":2,"off":10,"max":100[,"enabled":true]}
	"AutoDew",        // GET ramp + current duty for {"port":1}
	"StartSequence",  // PUT DSLR run: {"frames":20,"exposure_s":120[,"delay_s":3]}
	"AbortSequence",  // PUT: stop the run and open the shutter
	"SequenceStatus", // GET progress
}

// SupportedActions lists the device-specific Action names.
func (s *AsiairSwitch) SupportedActions() []string { return asiairActions }

// environment is the SetEnvironment payload, matching the smpro and tenmicron
// schema field for field. Every field is a pointer so a feeder sends only what it
// has, and only the present fields are applied. The site/time fields mean nothing
// to a power board and are accepted-and-ignored so one broadcast payload can serve
// every device.
//
// DewpointC is advisory, not authoritative: see actionSetEnvironment.
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
//
// The port is named "port" here rather than smpro's "channel", because on this
// board the dimmable outputs ARE ports — and under the ports-2+4 pairing a
// "channel 2" would be ambiguous between the second dew channel and Port 2.
// "channel" is still accepted as an alias, so a client written against smpro's
// schema works unchanged on the default pairing (where channel 1/2 == port 1/2).
type autoDewJSON struct {
	Port    *int     `json:"port,omitempty"`
	Channel *int     `json:"channel,omitempty"` // alias for port, for smpro-shaped clients
	Enabled *bool    `json:"enabled,omitempty"`
	On      *float64 `json:"on,omitempty"`      // margin °C at/below which duty = Max
	Off     *float64 `json:"off,omitempty"`     // margin °C at/above which duty = 0
	Max     *int     `json:"max,omitempty"`     // duty ceiling %
	DutyPct *int     `json:"dutyPct,omitempty"` // reply only: current duty
}

// sequenceJSON is the StartSequence payload and the SequenceStatus reply.
type sequenceJSON struct {
	Frames      int      `json:"frames"`
	ExposureS   float64  `json:"exposure_s"`
	DelayS      float64  `json:"delay_s,omitempty"`
	Running     *bool    `json:"running,omitempty"`        // reply only
	Frame       *int     `json:"frame,omitempty"`          // reply only: 1-based
	RemainingS  *float64 `json:"remaining_s,omitempty"`    // reply only: of the current exposure
	ShutterOpen *bool    `json:"shutter_closed,omitempty"` // reply only: contact closed == exposing
}

// Action dispatches a device-specific Action by name (matched case-insensitively,
// as in the sibling smpro, mgpbox and tenmicron drivers).
func (s *AsiairSwitch) Action(name, params string) (string, error) {
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
	case "startsequence":
		return actionStartSequence(b, params)
	case "abortsequence":
		return actionAbortSequence(b)
	case "sequencestatus":
		return actionSequenceStatus(b)
	}
	return "", alpacadev.ErrActionNotImplemented
}

// --- Weather ---

// actionSetEnvironment is dual-mode, like the mount's and smpro's: empty params
// reads the stored conditions, a JSON body applies the fields it carries.
func actionSetEnvironment(b *asiair.Board, params string) (string, error) {
	if strings.TrimSpace(params) == "" {
		return readEnvironment(b)
	}
	var env environment
	if err := json.Unmarshal([]byte(params), &env); err != nil {
		return "", alpacadev.NewError(alpacadev.ErrNumInvalidValue, "setenvironment: invalid JSON: "+err.Error())
	}

	// Merge onto the stored conditions rather than replacing them: a feeder may
	// send a pressure-only or site-only snapshot, and a blind overwrite would zero
	// the temperature and humidity — leaving a margin of the whole air temperature,
	// which reads as "bone dry" and switches the heaters off.
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
	// zero and the asiair library derives it from temperature and humidity
	// (Magnus). Not every feeder computes one — the MGPBox does not, yet its feed
	// sends the field regardless, so it arrives as a literal 0. Taken at face value
	// that is a margin of the whole air temperature: "bone dry", heaters off, on
	// exactly the night they are needed. A dew point above the air temperature is
	// likewise unphysical (a broken or mis-scaled sensor); ignore it and derive,
	// which errs toward heating rather than toward a dewed optic.
	w.Dewpoint = 0
	if dp := env.DewpointC; dp != nil && *dp != 0 && *dp <= w.Temperature {
		w.Dewpoint = *dp
		applied = append(applied, "dewpoint")
	}

	if len(applied) == 0 {
		// Nothing we consume (a pressure/site/time-only push). Leave the conditions
		// — and therefore the heaters — exactly as they are.
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

func readEnvironment(b *asiair.Board) (string, error) {
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

// --- Auto-dew ---

// portOf resolves the payload's 1-based port (or its "channel" alias) to a
// library Port, and rejects one that cannot dim. Auto-dew on a port that can only
// switch fully on or off is not auto-dew, and quietly accepting it would leave a
// client believing a ramp is running when the heater is simply off.
func portOf(b *asiair.Board, a autoDewJSON) (asiair.Port, error) {
	n := 0
	switch {
	case a.Port != nil:
		n = *a.Port
	case a.Channel != nil:
		n = *a.Channel
	default:
		return 0, alpacadev.NewError(alpacadev.ErrNumInvalidValue, "missing \"port\" (1..4)")
	}
	if n < 1 || n > asiair.NumPorts {
		return 0, alpacadev.NewError(alpacadev.ErrNumInvalidValue, "port must be 1..4")
	}
	p := asiair.Port(n - 1)
	if !b.Dimmable(p) {
		return 0, alpacadev.NewError(alpacadev.ErrNumInvalidValue, fmt.Sprintf(
			"port %d cannot auto-dew: it has no hardware PWM. Put the dew heater on a dimmable port", n))
	}
	return p, nil
}

func actionSetAutoDew(b *asiair.Board, params string) (string, error) {
	var a autoDewJSON
	if err := json.Unmarshal([]byte(params), &a); err != nil {
		return "", alpacadev.NewError(alpacadev.ErrNumInvalidValue, "setautodew: invalid JSON: "+err.Error())
	}
	p, err := portOf(b, a)
	if err != nil {
		return "", err
	}
	cfg, err := b.AutoDew(p)
	if err != nil {
		return "", err
	}
	// Absent fields keep their current values, so a client can flip Enabled without
	// re-stating the ramp.
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
	if err := b.SetAutoDew(p, cfg); err != nil {
		return "", err
	}
	return "ok", nil
}

func actionAutoDew(b *asiair.Board, params string) (string, error) {
	var a autoDewJSON
	if err := json.Unmarshal([]byte(params), &a); err != nil {
		return "", alpacadev.NewError(alpacadev.ErrNumInvalidValue, "autodew: invalid JSON: "+err.Error())
	}
	p, err := portOf(b, a)
	if err != nil {
		return "", err
	}
	cfg, err := b.AutoDew(p)
	if err != nil {
		return "", err
	}
	duty, _ := b.PortDuty(p)
	n := int(p) + 1
	out, err := json.Marshal(autoDewJSON{
		Port: &n, Enabled: &cfg.Enabled, On: &cfg.On, Off: &cfg.Off,
		Max: &cfg.Max, DutyPct: &duty,
	})
	return string(out), err
}

// --- DSLR sequence ---

// actionStartSequence begins a timed DSLR run. It returns immediately; poll
// SequenceStatus to follow it, or AbortSequence to stop it.
//
// There is no cap on the exposure. The vendor driver chops its waits into 50 s
// segments, but that is an artefact of pigpio's millisecond timer, not a property
// of the shutter — a 20-minute bulb frame is an ordinary thing to ask a DSLR for.
func actionStartSequence(b *asiair.Board, params string) (string, error) {
	var s sequenceJSON
	if err := json.Unmarshal([]byte(params), &s); err != nil {
		return "", alpacadev.NewError(alpacadev.ErrNumInvalidValue, "startsequence: invalid JSON: "+err.Error())
	}
	seq := asiair.Sequence{
		Frames:   s.Frames,
		Exposure: secs(s.ExposureS),
		Delay:    secs(s.DelayS),
	}
	if err := b.StartSequence(seq); err != nil {
		return "", alpacadev.NewError(alpacadev.ErrNumInvalidValue, "startsequence: "+err.Error())
	}
	return "ok", nil
}

// actionAbortSequence stops a run and opens the shutter contact. It is safe to
// call when nothing is running — a client that has lost track of the board's state
// should be able to say "stop, whatever you are doing" without first having to ask.
func actionAbortSequence(b *asiair.Board) (string, error) {
	if err := b.AbortSequence(); err != nil {
		return "", err
	}
	return "ok", nil
}

func actionSequenceStatus(b *asiair.Board) (string, error) {
	st := b.SequenceStatus()
	rem := st.Remaining.Seconds()
	out, err := json.Marshal(sequenceJSON{
		Frames:      st.Frames,
		Running:     &st.Running,
		Frame:       &st.Frame,
		RemainingS:  &rem,
		ShutterOpen: &st.ShutterOpen,
	})
	return string(out), err
}

func secs(f float64) time.Duration { return time.Duration(f * float64(time.Second)) }
