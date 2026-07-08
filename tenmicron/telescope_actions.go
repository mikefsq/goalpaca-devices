package driver

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"

	alpacadev "github.com/mikefsq/goalpaca/server"
	"github.com/mikefsq/lx200/tenmicron"
)

// ASCOM Actions expose the 10Micron features that no standard Telescope member covers.
// They share the RST driver's get/put convention: for a read/write action an EMPTY
// params reads the current value, a non-empty params sets it (and echoes it back);
// read-only actions reject a value; operations take no value (or a fixed token). Names
// are lower-case; matching is case-insensitive. Model, alignment and satellite actions
// live in telescope_actions_model.go / telescope_actions_satellite.go and are merged in.

type actionFn func(params string) (string, error)

// SupportedActions lists the custom action names (sorted, for a stable response).
func (t *Telescope) SupportedActions() []string {
	reg := t.actions()
	names := make([]string, 0, len(reg))
	for n := range reg {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Action dispatches a custom action by name (case-insensitive).
func (t *Telescope) Action(name, params string) (string, error) {
	fn, ok := t.actions()[strings.ToLower(strings.TrimSpace(name))]
	if !ok {
		return "", alpacadev.ErrActionNotImplemented
	}
	return fn(strings.TrimSpace(params))
}

// live returns the connected mount or ErrNotConnected.
func (t *Telescope) live() (*tenmicron.Mount, error) {
	if m := t.mount(); m != nil {
		return m, nil
	}
	return nil, alpacadev.ErrNotConnected
}

// --- action shapes ----------------------------------------------------------

// read wraps a mount read as a read-only action (rejects a params value).
func (t *Telescope) read(fn func(*tenmicron.Mount) (string, error)) actionFn {
	return func(params string) (string, error) {
		if params != "" {
			return "", badValue("action is read-only (pass no value)")
		}
		m, err := t.live()
		if err != nil {
			return "", err
		}
		return fn(m)
	}
}

// readWrite wraps a read + write: empty params reads; a value sets it and is echoed.
func (t *Telescope) readWrite(get func(*tenmicron.Mount) (string, error), set func(*tenmicron.Mount, string) error) actionFn {
	return func(params string) (string, error) {
		m, err := t.live()
		if err != nil {
			return "", err
		}
		if params == "" {
			return get(m)
		}
		if err := set(m, params); err != nil {
			return "", err
		}
		return params, nil
	}
}

// writeOnly wraps a setter the mount cannot read back: empty params is rejected, a value
// sets it and is echoed.
func (t *Telescope) writeOnly(set func(*tenmicron.Mount, string) error) actionFn {
	return func(params string) (string, error) {
		if params == "" {
			return "", badValue("action requires a value (the mount does not report it)")
		}
		m, err := t.live()
		if err != nil {
			return "", err
		}
		if err := set(m, params); err != nil {
			return "", err
		}
		return params, nil
	}
}

// op wraps a value-less operation (trigger); returns the fn's status string.
func (t *Telescope) op(fn func(*tenmicron.Mount) (string, error)) actionFn {
	return func(params string) (string, error) {
		if params != "" {
			return "", badValue("action takes no value")
		}
		m, err := t.live()
		if err != nil {
			return "", err
		}
		return fn(m)
	}
}

// indexed wraps a read that takes a 1-based index (model/alignment slots): the params
// value is the index (default 1).
func (t *Telescope) indexed(fn func(*tenmicron.Mount, int) (string, error)) actionFn {
	return func(params string) (string, error) {
		m, err := t.live()
		if err != nil {
			return "", err
		}
		n := 1
		if params != "" {
			v, err := strconv.Atoi(params)
			if err != nil {
				return "", alpacadev.ErrInvalidValue
			}
			n = v
		}
		return fn(m, n)
	}
}

// --- shared helpers ---------------------------------------------------------

func ftoa(v float64, prec int) string { return strconv.FormatFloat(v, 'f', prec, 64) }

// ptr returns a pointer to v (for the omitempty-pointer JSON payloads).
func ptr[T any](v T) *T { return &v }

func badValue(msg string) error { return alpacadev.NewError(alpacadev.ErrNumInvalidValue, msg) }

func parseFloatArg(p string) (float64, error) {
	v, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
	if err != nil {
		return 0, alpacadev.ErrInvalidValue
	}
	return v, nil
}

func parseIntArg(p string) (int, error) {
	v, err := strconv.Atoi(strings.TrimSpace(p))
	if err != nil {
		return 0, alpacadev.ErrInvalidValue
	}
	return v, nil
}

func parseBool(s string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true", "1", "on", "yes":
		return true, true
	case "false", "0", "off", "no":
		return false, true
	}
	return false, false
}

// okErr collapses a (bool ok, error) mount reply into an error: a false ok (mount
// rejected the value) becomes an InvalidValue with rejectMsg.
func okErr(ok bool, err error, rejectMsg string) error {
	if err != nil {
		return err
	}
	if !ok {
		return badValue(rejectMsg)
	}
	return nil
}

func meridianStr(s tenmicron.MeridianSide) string {
	switch s {
	case tenmicron.MeridianBothSides:
		return "both"
	case tenmicron.MeridianWestOnly:
		return "westonly"
	case tenmicron.MeridianEastOnly:
		return "eastonly"
	}
	return strconv.Itoa(int(s))
}

func parseMeridian(p string) (tenmicron.MeridianSide, bool) {
	switch strings.ToLower(strings.TrimSpace(p)) {
	case "both", "1":
		return tenmicron.MeridianBothSides, true
	case "westonly", "west", "2":
		return tenmicron.MeridianWestOnly, true
	case "eastonly", "east", "3":
		return tenmicron.MeridianEastOnly, true
	}
	return 0, false
}

func weatherModeStr(w tenmicron.WeatherAutoUpdate) string {
	switch w {
	case tenmicron.WeatherAutoOff:
		return "off"
	case tenmicron.WeatherAutoWhenIdle:
		return "whenidle"
	case tenmicron.WeatherAutoContinuous:
		return "continuous"
	}
	return strconv.Itoa(int(w))
}

func parseWeatherMode(p string) (tenmicron.WeatherAutoUpdate, bool) {
	switch strings.ToLower(strings.TrimSpace(p)) {
	case "off", "0":
		return tenmicron.WeatherAutoOff, true
	case "whenidle", "idle", "1":
		return tenmicron.WeatherAutoWhenIdle, true
	case "continuous", "2":
		return tenmicron.WeatherAutoContinuous, true
	}
	return 0, false
}

func gpsStr(s tenmicron.GPSSync) string {
	switch s {
	case tenmicron.GPSSyncOff:
		return "off"
	case tenmicron.GPSSyncGPS:
		return "gps"
	case tenmicron.GPSSyncPPS:
		return "gps+pps"
	}
	return strconv.Itoa(int(s))
}

func featureStr(s tenmicron.FeatureState) string {
	switch s {
	case tenmicron.FeatureEnabled:
		return "enabled"
	case tenmicron.FeatureDisabled:
		return "disabled"
	case tenmicron.FeatureUnavailable:
		return "unavailable"
	}
	return strconv.Itoa(int(s))
}

// --- registry ---------------------------------------------------------------

func (t *Telescope) actions() map[string]actionFn {
	reg := map[string]actionFn{
		// environment / optics / refraction datums
		"setenvironment":   t.actionSetEnvironment,
		"setoptics":        t.actionSetOptics,
		"dualaxistracking": t.actionDualAxisTracking,

		"refractionpressure": t.readWrite(
			func(m *tenmicron.Mount) (string, error) { p, _, err := m.Refraction(); return ftoa(p, 1), err },
			func(m *tenmicron.Mount, p string) error {
				v, err := parseFloatArg(p)
				if err != nil {
					return err
				}
				return m.SetRefractionPressure(v)
			}),
		"refractiontemperature": t.readWrite(
			func(m *tenmicron.Mount) (string, error) { _, tc, err := m.Refraction(); return ftoa(tc, 1), err },
			func(m *tenmicron.Mount, p string) error {
				v, err := parseFloatArg(p)
				if err != nil {
					return err
				}
				return m.SetRefractionTemperature(v)
			}),

		// pointing & meridian limits
		"highaltitudelimit": t.readWrite(
			func(m *tenmicron.Mount) (string, error) { v, err := m.HighAltitudeLimit(); return ftoa(v, 0), err },
			func(m *tenmicron.Mount, p string) error {
				v, err := parseIntArg(p)
				if err != nil {
					return err
				}
				ok, err := m.SetHighAltitudeLimit(v)
				return okErr(ok, err, "high-altitude limit rejected")
			}),
		"lowaltitudelimit": t.readWrite(
			func(m *tenmicron.Mount) (string, error) { v, err := m.LowAltitudeLimit(); return ftoa(v, 0), err },
			func(m *tenmicron.Mount, p string) error {
				v, err := parseIntArg(p)
				if err != nil {
					return err
				}
				ok, err := m.SetLowAltitudeLimit(v)
				return okErr(ok, err, "low-altitude limit rejected")
			}),
		"meridianslewlimit": t.readWrite(
			func(m *tenmicron.Mount) (string, error) { v, err := m.MeridianSlewLimit(); return strconv.Itoa(v), err },
			func(m *tenmicron.Mount, p string) error {
				v, err := parseIntArg(p)
				if err != nil {
					return err
				}
				ok, err := m.SetMeridianSlewLimit(v)
				return okErr(ok, err, "meridian slew limit rejected")
			}),
		"meridiantracklimit": t.readWrite(
			func(m *tenmicron.Mount) (string, error) { v, err := m.MeridianTrackLimit(); return strconv.Itoa(v), err },
			func(m *tenmicron.Mount, p string) error {
				v, err := parseIntArg(p)
				if err != nil {
					return err
				}
				ok, err := m.SetMeridianTrackLimit(v)
				return okErr(ok, err, "meridian track limit rejected")
			}),
		"meridiansidebehaviour": t.readWrite(
			func(m *tenmicron.Mount) (string, error) { s, err := m.MeridianSideBehaviour(); return meridianStr(s), err },
			func(m *tenmicron.Mount, p string) error {
				s, ok := parseMeridian(p)
				if !ok {
					return badValue("want both/westonly/eastonly")
				}
				ok2, err := m.SetMeridianSideBehaviour(s)
				return okErr(ok2, err, "meridian side rejected")
			}),
		"unattendedflip": t.readWrite(
			func(m *tenmicron.Mount) (string, error) { v, err := m.UnattendedFlip(); return strconv.FormatBool(v), err },
			func(m *tenmicron.Mount, p string) error {
				b, ok := parseBool(p)
				if !ok {
					return badValue("want true/false")
				}
				return m.SetUnattendedFlip(b)
			}),

		// slew rates & backlash
		"maxslewrate": t.readWrite(
			func(m *tenmicron.Mount) (string, error) { v, err := m.MaxSlewRate(); return ftoa(v, 1), err },
			func(m *tenmicron.Mount, p string) error {
				v, err := parseIntArg(p)
				if err != nil {
					return err
				}
				ok, err := m.SetMaxSlewRate(v)
				if err := okErr(ok, err, "max slew rate rejected"); err != nil {
					return err
				}
				t.mu.Lock() // keep AxisRates in sync with the new ceiling
				t.maxSlewRate = float64(v)
				t.mu.Unlock()
				return nil
			}),
		"minslewrate": t.read(func(m *tenmicron.Mount) (string, error) { v, err := m.MinSlewRate(); return ftoa(v, 1), err }),
		"rabacklash": t.writeOnly(func(m *tenmicron.Mount, p string) error {
			v, err := parseFloatArg(p)
			if err != nil {
				return err
			}
			return m.SetRABacklash(v)
		}),
		"decbacklash": t.writeOnly(func(m *tenmicron.Mount, p string) error {
			v, err := parseFloatArg(p)
			if err != nil {
				return err
			}
			return m.SetDecBacklash(v)
		}),

		// weather station / refraction auto-update
		"weather": t.read(t.readWeather),
		"weatherautoupdate": t.readWrite(
			func(m *tenmicron.Mount) (string, error) { md, err := m.WeatherAutoUpdateMode(); return weatherModeStr(md), err },
			func(m *tenmicron.Mount, p string) error {
				md, ok := parseWeatherMode(p)
				if !ok {
					return badValue("want off/whenidle/continuous")
				}
				ok2, err := m.SetWeatherAutoUpdateMode(md)
				return okErr(ok2, err, "weather auto-update rejected")
			}),

		// dithering / PEC
		"dither": t.actionDither,
		"pec":    t.actionPEC,

		// time / GPS
		"utcoffset": t.readWrite(
			func(m *tenmicron.Mount) (string, error) { d, err := m.UTCOffset(); return ftoa(d.Hours(), 2), err },
			func(m *tenmicron.Mount, p string) error {
				v, err := parseFloatArg(p)
				if err != nil {
					return err
				}
				return m.SetUTCOffset(time.Duration(v * float64(time.Hour)))
			}),
		"syncgps":   t.op(func(m *tenmicron.Mount) (string, error) { ok, err := m.UpdateFromGPS(); return strconv.FormatBool(ok), err }),
		"gpsstatus": t.read(func(m *tenmicron.Mount) (string, error) { s, err := m.GPSSyncState(); return gpsStr(s), err }),

		// power / system / network
		"autopoweron": t.readWrite(
			func(m *tenmicron.Mount) (string, error) { s, err := m.AutoPowerOnState(); return featureStr(s), err },
			func(m *tenmicron.Mount, p string) error {
				b, ok := parseBool(p)
				if !ok {
					return badValue("want true/false")
				}
				return m.SetAutoPowerOn(b)
			}),
		"wakeonlan": t.readWrite(
			func(m *tenmicron.Mount) (string, error) { s, err := m.WakeOnLANState(); return featureStr(s), err },
			func(m *tenmicron.Mount, p string) error {
				b, ok := parseBool(p)
				if !ok {
					return badValue("want true/false")
				}
				return m.SetWakeOnLAN(b)
			}),
		"webinterface": t.readWrite(
			func(m *tenmicron.Mount) (string, error) { v, err := m.WebInterfaceActive(); return strconv.FormatBool(v), err },
			func(m *tenmicron.Mount, p string) error {
				b, ok := parseBool(p)
				if !ok {
					return badValue("want true/false")
				}
				return m.SetWebInterface(b)
			}),
		"shutdown": func(p string) (string, error) {
			if strings.ToLower(strings.TrimSpace(p)) != "confirm" {
				return "", badValue("shutdown requires the value 'confirm'")
			}
			m, err := t.live()
			if err != nil {
				return "", err
			}
			if err := m.Shutdown(); err != nil {
				return "", err
			}
			return "shutting down", nil
		},
		"network": t.read(t.readNetwork),

		// telemetry
		"firmware":          t.read(t.readFirmware),
		"motortemperature":  t.read(t.readMotorTemps),
		"parallacticangle":  t.read(func(m *tenmicron.Mount) (string, error) { v, err := m.ParallacticAngle(); return ftoa(v, 4), err }),
		"axisangles":        t.read(t.readAxisAngles),
		"timetotrackingend": t.read(func(m *tenmicron.Mount) (string, error) { d, err := m.TimeToTrackingEnd(); return ftoa(d.Seconds(), 0), err }),

		// parking variants
		"parkinplace": t.op(func(m *tenmicron.Mount) (string, error) { return "parking", m.ParkInPlace() }),
		"parktosaved": t.op(func(m *tenmicron.Mount) (string, error) { return "parking", m.ParkToSaved() }),
	}

	// Back-compat: the original set-only names now resolve to the RW refraction actions.
	reg["setrefractionpressure"] = reg["refractionpressure"]
	reg["setrefractiontemperature"] = reg["refractiontemperature"]

	for k, v := range t.modelActions() {
		reg[k] = v
	}
	for k, v := range t.satelliteActions() {
		reg[k] = v
	}
	return reg
}

// --- JSON-valued reads ------------------------------------------------------

func (t *Telescope) readWeather(m *tenmicron.Mount) (string, error) {
	type field struct {
		Value  float64 `json:"value"`
		AgeSec float64 `json:"age_sec"`
	}
	out := map[string]field{}
	add := func(name string, v float64, age time.Duration, err error) {
		if err == nil {
			out[name] = field{v, age.Seconds()}
		}
	}
	p, pa, pe := m.WeatherPressure()
	add("pressure_hpa", p, pa, pe)
	tc, ta, te := m.WeatherTemperature()
	add("temperature_c", tc, ta, te)
	h, ha, he := m.WeatherHumidity()
	add("humidity_pct", h, ha, he)
	d, da, de := m.WeatherDewPoint()
	add("dewpoint_c", d, da, de)
	if len(out) == 0 {
		return "", badValue("no weather-station data available")
	}
	b, _ := json.Marshal(out)
	return string(b), nil
}

func (t *Telescope) readNetwork(m *tenmicron.Mount) (string, error) {
	net := func(c tenmicron.NetworkConfig) map[string]string {
		return map[string]string{"ip": c.IP, "netmask": c.Netmask, "gateway": c.Gateway, "flag": c.Flag}
	}
	out := map[string]any{}
	wired, werr := m.WiredNetwork()
	if werr == nil {
		out["wired"] = net(wired)
	}
	if ws, err := m.WirelessNetwork(); err == nil {
		out["wireless"] = net(ws)
	}
	if st, err := m.WirelessStatus(); err == nil {
		out["wireless_status"] = st
	}
	if len(out) == 0 {
		if werr != nil {
			return "", werr
		}
		return "", badValue("no network info available")
	}
	b, _ := json.Marshal(out)
	return string(b), nil
}

func (t *Telescope) readFirmware(m *tenmicron.Mount) (string, error) {
	mc := m.MountClass()
	out := map[string]any{
		"firmware": m.FirmwareVersion().String(),
		"product":  mc.Product,
		"altaz":    mc.AltAz,
	}
	if hw, err := m.HardwareID(); err == nil {
		out["hardware_id"] = hw
	}
	if cb, err := m.ControlBoxVersion(); err == nil {
		out["control_box"] = cb
	}
	b, _ := json.Marshal(out)
	return string(b), nil
}

func (t *Telescope) readMotorTemps(m *tenmicron.Mount) (string, error) {
	out := map[string]float64{}
	add := func(name string, el tenmicron.TemperatureElement) {
		if v, err := m.Temperature(el); err == nil {
			out[name] = v
		}
	}
	add("ra_motor", tenmicron.TempRAAzMotor)
	add("dec_motor", tenmicron.TempDecAltMotor)
	add("electronics_box", tenmicron.TempElectronicsBox)
	if len(out) == 0 {
		return "", badValue("no temperature sensors available")
	}
	b, _ := json.Marshal(out)
	return string(b), nil
}

func (t *Telescope) readAxisAngles(m *tenmicron.Mount) (string, error) {
	p, err := m.AxisAnglePrimary()
	if err != nil {
		return "", err
	}
	s, err := m.AxisAngleSecondary()
	if err != nil {
		return "", err
	}
	b, _ := json.Marshal(map[string]float64{"primary": p, "secondary": s})
	return string(b), nil
}

// --- multi-mode operations --------------------------------------------------

// actionDither reads dither status (empty params → JSON: active + amplitude/timing) or
// triggers start/stop/now.
func (t *Telescope) actionDither(params string) (string, error) {
	m, err := t.live()
	if err != nil {
		return "", err
	}
	switch strings.ToLower(strings.TrimSpace(params)) {
	case "":
		active, err := m.DitheringActive()
		if err != nil {
			return "", err
		}
		dp, err := m.DitherParameters()
		if err != nil {
			return strconv.FormatBool(active), nil // params optional (older firmware)
		}
		b, _ := json.Marshal(map[string]any{
			"active":       active,
			"ra_arcsec":    dp.RAArcsec,
			"dec_arcsec":   dp.DecArcsec,
			"delay_sec":    dp.DelaySec,
			"exposure_sec": dp.ExposureSec,
			"interval_sec": dp.IntervalSec,
		})
		return string(b), nil
	case "start", "on":
		ok, err := m.StartDithering()
		return strconv.FormatBool(ok), err
	case "stop", "off":
		ok, err := m.StopDithering()
		return strconv.FormatBool(ok), err
	case "now":
		ok, err := m.DitherNow()
		return strconv.FormatBool(ok), err
	default:
		return "", badValue("dither: want empty (status), start, stop, or now")
	}
}

// actionPEC controls periodic error correction: start/stop/train[:short|medium|long].
func (t *Telescope) actionPEC(params string) (string, error) {
	m, err := t.live()
	if err != nil {
		return "", err
	}
	p := strings.ToLower(strings.TrimSpace(params))
	switch p {
	case "start":
		return "pec started", m.StartPEC()
	case "stop":
		return "pec stopped", m.StopPEC()
	case "train":
		return "pec training", m.TrainPEC()
	case "train:short":
		return "pec training", m.TrainPECLength(tenmicron.PECShort)
	case "train:medium":
		return "pec training", m.TrainPECLength(tenmicron.PECMedium)
	case "train:long":
		return "pec training", m.TrainPECLength(tenmicron.PECLong)
	}
	return "", badValue("pec: want start, stop, train, or train:short|medium|long")
}

// --- setenvironment / dualaxistracking (site + refraction datums) -----------

// actionDualAxisTracking reads or sets dual-axis tracking (:Gdat#/:SdatN#) — the mount
// driving both axes to follow the refraction/pointing model. params: empty or "?" reads
// and returns "true"/"false"; "true"/"false" (also 1/0, on/off) sets it. Disabling is
// equatorial-only — an AltAz mount rejects it.
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

// environment is the setenvironment payload. Every field is optional (a pointer); only
// present fields are pushed to the mount, so a feeder sends just what changed.
type environment struct {
	PressureHPa  *float64 `json:"pressure_hpa,omitempty"`
	TemperatureC *float64 `json:"temperature_c,omitempty"`
	Latitude     *float64 `json:"latitude,omitempty"`  // degrees, East-positive (ASCOM)
	Longitude    *float64 `json:"longitude,omitempty"` // degrees, East-positive
	ElevationM   *float64 `json:"elevation_m,omitempty"`
	Time         *string  `json:"time,omitempty"` // RFC3339 (e.g. 2026-06-02T15:04:05Z)
}

// actionSetEnvironment applies the present payload fields to the mount: refraction
// pressure/temperature and (via the standard setters) site latitude/longitude/elevation
// and UTC date. Returns a JSON object listing applied fields. Updating the refraction
// datums does not enable refraction — set DoesRefraction for that.
func (t *Telescope) actionSetEnvironment(params string) (string, error) {
	if params == "" { // read-only: return the current environment in the payload's shape
		return t.readEnvironment()
	}
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

// readEnvironment returns the current environment as a COMPLETE setenvironment payload
// template — every accepted field is present (so a client can read it, edit fields, and
// send it back). Site latitude/longitude/elevation and UTC date come from the driver's
// cache; refraction pressure/temperature are read from the mount best-effort and default
// to 0 when the mount is unreachable rather than being omitted. Never errors.
func (t *Telescope) readEnvironment() (string, error) {
	env := environment{
		PressureHPa:  ptr(0.0),
		TemperatureC: ptr(0.0),
		Latitude:     ptr(t.SiteLatitude()),
		Longitude:    ptr(t.SiteLongitude()),
		ElevationM:   ptr(t.SiteElevation()),
		Time:         ptr(t.UTCDate()),
	}
	if m := t.mount(); m != nil {
		if p, tc, err := m.Refraction(); err == nil {
			env.PressureHPa, env.TemperatureC = ptr(p), ptr(tc)
		}
	}
	out, _ := json.Marshal(env)
	return string(out), nil
}
