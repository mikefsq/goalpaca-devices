package driver

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/mikefsq/goalpaca-devices/internal/hostclock"
	mountdriver "github.com/mikefsq/lx200/tenmicron"
)

// WeatherSource identifies an Alpaca ObservingConditions device, not a native
// mount endpoint. MaxAgeSeconds defaults to 30; negative sample ages are rejected.
type WeatherSource struct {
	Addr          string  `json:"addr"`
	Device        int     `json:"device"`
	MaxAgeSeconds float64 `json:"maxAgeSeconds,omitempty"`
}

func endpoint(addr string) (string, error) {
	if !strings.Contains(addr, "://") {
		addr = "http://" + addr
	}
	u, err := url.Parse(addr)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", fmt.Errorf("invalid ObservingConditions address")
	}
	return strings.TrimRight(u.String(), "/"), nil
}

// Explicit flat schema settings take precedence over the legacy nested object.
func (c Config) weatherSource() *WeatherSource {
	if c.ObservingConditionsAddr != "" {
		return &WeatherSource{Addr: c.ObservingConditionsAddr, Device: c.ObservingConditionsDevice, MaxAgeSeconds: c.ObservingConditionsMaxAgeSeconds}
	}
	return c.ObservingConditions
}

func (c Config) validateSources() error {
	if w := c.weatherSource(); w != nil {
		if _, err := endpoint(w.Addr); err != nil {
			return err
		}
		if w.Device < 0 || !finite(w.MaxAgeSeconds) || w.MaxAgeSeconds < 0 {
			return fmt.Errorf("invalid ObservingConditions device or maximum age")
		}
	}
	if c.Gpsd != "" {
		host, port, err := net.SplitHostPort(c.Gpsd)
		p, e := strconv.Atoi(port)
		if err != nil || host == "" || e != nil || p < 1 || p > 65535 {
			return fmt.Errorf("gpsd must be host:port")
		}
	}
	return nil
}
func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

var sourceHTTP = &http.Client{Timeout: 5 * time.Second}

func (s WeatherSource) get(ctx context.Context, property string, query url.Values, value any) error {
	base, err := endpoint(s.Addr)
	if err != nil {
		return err
	}
	if query == nil {
		query = url.Values{}
	}
	query.Set("ClientID", "1")
	query.Set("ClientTransactionID", "1")
	path := fmt.Sprintf("%s/api/v1/observingconditions/%d/%s?%s", base, s.Device, property, query.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	resp, err := sourceHTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: HTTP %d", property, resp.StatusCode)
	}
	var envelope struct {
		Value        json.RawMessage
		ErrorNumber  *int
		ErrorMessage string
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&envelope); err != nil {
		return err
	}
	if envelope.ErrorNumber == nil || *envelope.ErrorNumber != 0 {
		return fmt.Errorf("%s: Alpaca error: %s", property, envelope.ErrorMessage)
	}
	if len(envelope.Value) == 0 || string(envelope.Value) == "null" {
		return fmt.Errorf("%s: missing value", property)
	}
	return json.Unmarshal(envelope.Value, value)
}

func (s WeatherSource) read(ctx context.Context) (environment, error) {
	var env environment
	var connected bool
	if err := s.get(ctx, "connected", nil, &connected); err != nil {
		return env, err
	}
	if !connected {
		return env, fmt.Errorf("ObservingConditions is disconnected")
	}
	maxAge := s.MaxAgeSeconds
	if maxAge == 0 {
		maxAge = 30
	}
	for _, name := range []string{"pressure", "temperature"} {
		var v, age float64
		if err := s.get(ctx, name, nil, &v); err != nil {
			return environment{}, err
		}
		if err := s.get(ctx, "timesincelastupdate", url.Values{"SensorName": {name}}, &age); err != nil {
			return environment{}, err
		}
		if !finite(age) || age < 0 || age > maxAge {
			return environment{}, fmt.Errorf("%s reading is stale or unavailable (age %g s)", name, age)
		}
		if !finite(v) {
			return environment{}, fmt.Errorf("%s is not finite", name)
		}
		if name == "pressure" {
			if v <= 0 || v > 1200 {
				return environment{}, fmt.Errorf("pressure outside 0–1200 hPa")
			}
			env.PressureHPa = ptr(v)
		} else {
			if v < -100 || v > 100 {
				return environment{}, fmt.Errorf("temperature outside -100–100 °C")
			}
			env.TemperatureC = ptr(v)
		}
	}
	return env, nil
}

// readGPSLocation takes a new TPV from gpsd. It never opens the receiver hardware.
// Require a fresh 3D report containing all coordinates; do not merge old fields.
func readGPSLocation(ctx context.Context, addr string) (environment, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return environment{}, err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	if _, err = conn.Write([]byte("?WATCH={\"enable\":true,\"json\":true};\n")); err != nil {
		return environment{}, err
	}
	scan := bufio.NewScanner(conn)
	scan.Buffer(make([]byte, 4096), 1<<20)
	responding := false
	for scan.Scan() {
		var report struct{ Class string }
		if json.Unmarshal(scan.Bytes(), &report) == nil && (report.Class == "VERSION" || report.Class == "WATCH" || report.Class == "TPV" || report.Class == "SKY") {
			responding = true
		}
		env, ok := gpsLocation(scan.Bytes(), time.Now())
		if ok {
			return env, nil
		}
	}
	if responding && ctx.Err() == context.DeadlineExceeded {
		return environment{}, fmt.Errorf("gpsd is responding but has no fresh 3D fix")
	}
	if ctx.Err() != nil {
		return environment{}, ctx.Err()
	}
	if err := scan.Err(); err != nil {
		return environment{}, err
	}
	return environment{}, fmt.Errorf("gpsd provided no fresh 3D location")
}
func gpsLocation(data []byte, now time.Time) (environment, bool) {
	var r struct {
		Class    string
		Mode     int
		Time     string
		Lat, Lon *float64
		AltMSL   *float64 `json:"altMSL"`
	}
	if json.Unmarshal(data, &r) != nil || r.Class != "TPV" || r.Mode != 3 || r.Lat == nil || r.Lon == nil || r.AltMSL == nil {
		return environment{}, false
	}
	stamp, err := time.Parse(time.RFC3339Nano, r.Time)
	if err != nil || now.Sub(stamp) > 5*time.Second || stamp.Sub(now) > 2*time.Second {
		return environment{}, false
	}
	if !finite(*r.Lat) || !finite(*r.Lon) || !finite(*r.AltMSL) || math.Abs(*r.Lat) > 90 || math.Abs(*r.Lon) > 180 || *r.AltMSL < -300 || *r.AltMSL > 10000 {
		return environment{}, false
	}
	return environment{Latitude: r.Lat, Longitude: r.Lon, ElevationM: r.AltMSL}, true
}

type sourceStatus struct {
	Enabled     bool      `json:"enabled"`
	LastAttempt time.Time `json:"last_attempt,omitempty"`
	LastSuccess time.Time `json:"last_success,omitempty"`
	Error       string    `json:"error,omitempty"`
	Result      any       `json:"result,omitempty"`
}

func (t *Telescope) recordSource(name string, result any, err error) {
	t.sourceMu.Lock()
	defer t.sourceMu.Unlock()
	if t.sourceStatus == nil {
		t.sourceStatus = map[string]sourceStatus{}
	}
	s := t.sourceStatus[name]
	previous := s.Error
	s.Enabled = true
	s.LastAttempt = time.Now()
	s.Result = result
	s.Error = ""
	if err != nil {
		s.Error = err.Error()
	} else {
		s.LastSuccess = s.LastAttempt
	}
	t.sourceStatus[name] = s
	if s.Error != "" && s.Error != previous {
		log.Printf("tenmicron: %s source: %s", name, s.Error)
	}
}
func (t *Telescope) actionSourceStatus(params string) (string, error) {
	if strings.TrimSpace(params) != "" {
		return "", badValue("SourceStatus takes no parameters")
	}
	t.sourceMu.Lock()
	defer t.sourceMu.Unlock()
	result := map[string]sourceStatus{}
	for name, enabled := range map[string]bool{"weather": t.sources.ObservingConditions != nil, "location": t.sources.Gpsd != "", "clock": t.sources.HostClockSync} {
		s := t.sourceStatus[name]
		s.Enabled = enabled
		result[name] = s
	}
	b, err := json.Marshal(result)
	return string(b), err
}

// sourceInitialization records the single explicitly requested synchronization attempt.
type sourceInitialization struct {
	requested, coarse, location, nudge bool
	mount                              *mountdriver.Mount
}

func (t *Telescope) sourceLoop(ctx context.Context) {
	var nextWeather time.Time
	var previous *mountdriver.Mount
	for ctx.Err() == nil {
		m := t.mount()
		if m != previous {
			previous = m
			nextWeather = time.Time{}
		}
		if m != nil {
			if !time.Now().Before(nextWeather) && ctx.Err() == nil {
				t.pullEnvironment(ctx)
				nextWeather = time.Now().Add(30 * time.Second)
			}
		}
		sleepCtx(ctx, 5*time.Second)
	}
}

// withIdleMount guards an individual initialization stage against tracking,
// slewing, disconnection, and competing GPS/PPS clock ownership.
func (t *Telescope) withIdleMount(clock bool, fn func(*mountdriver.Mount) error) error {
	t.clockMu.Lock()
	defer t.clockMu.Unlock()
	m := t.mount()
	if m == nil {
		return fmt.Errorf("mount disconnected")
	}
	if _, err := m.Refresh(); err != nil {
		return err
	}
	moving, err := m.Slewing()
	if err != nil {
		return err
	}
	tracking, err := m.Tracking()
	if err != nil {
		return err
	}
	if moving || tracking {
		return fmt.Errorf("initialization skipped while mount is tracking or slewing")
	}
	if clock {
		gps, err := m.GPSSyncState()
		if err != nil {
			return err
		}
		if gps != mountdriver.GPSSyncOff {
			return fmt.Errorf("mount GPS/PPS synchronization is active")
		}
	}
	return fn(m)
}

// actionResync explicitly applies configured time and location sources once.
func (t *Telescope) actionResync(params string) (string, error) {
	if strings.TrimSpace(params) != "" {
		return "", badValue("Resync takes no parameters")
	}
	if !t.Connected() {
		return "", fmt.Errorf("mount disconnected")
	}
	t.initializeSources(context.Background(), false)
	return t.actionSourceStatus("")
}

func (t *Telescope) initializeSources(parent context.Context, onConnect bool) {
	t.initMu.Lock()
	defer t.initMu.Unlock()
	if onConnect && ((!t.sources.HostClockSync && !t.sources.GpsdSync) || t.initialization.mount == t.mount()) {
		return
	}
	t.initialization = sourceInitialization{requested: true, mount: t.mount()}
	state := &t.initialization
	if !state.requested || parent.Err() != nil {
		return
	}
	if m := t.mount(); m != state.mount {
		// A new hardware session needs a fresh client connection trigger.
		*state = sourceInitialization{}
		return
	}
	ctx, cancel := context.WithTimeout(parent, 25*time.Second)
	defer cancel()
	coarse := func() error {
		_, err := usableHostClock(ctx, hostclock.ReadClock)
		if err != nil {
			t.recordSource("clock", map[string]string{"stage": "validate_host_clock"}, err)
			log.Printf("tenmicron: resync clock validation: %v", err)
			return err
		}
		err = t.withIdleMount(true, func(m *mountdriver.Mount) error {
			// SUDT has whole-second precision. Round rather than truncate to
			// leave room for transport delay inside the fine-nudge range.
			return m.SetUTC(time.Now().UTC().Round(time.Second))
		})
		t.recordSource("clock", map[string]string{"stage": "set_utc"}, err)
		log.Printf("tenmicron: resync UTC set: %v", stepOutcome(err))
		return err
	}
	location := func() error {
		env, err := readGPSLocation(ctx, t.sources.Gpsd)
		if err == nil {
			err = t.withIdleMount(false, func(m *mountdriver.Mount) error {
				data, _ := json.Marshal(env)
				_, err := t.actionSetEnvironment(string(data))
				return err
			})
		}
		t.recordSource("location", env, err)
		log.Printf("tenmicron: resync GPS location: %v", stepOutcome(err))
		return err
	}
	nudge := func() error {
		result, err := t.syncHostClockContext(ctx)
		if err == nil && result.Status != "within_measurement_uncertainty" {
			err = fmt.Errorf("clock synchronization did not converge")
		}
		t.recordSource("clock", result, err)
		log.Printf("tenmicron: resync clock nudge: %v (offset %.3f ms, uncertainty %.3f ms)", stepOutcome(err), result.OffsetMs, result.UncertaintyMs)
		return err
	}
	state.advance(t.sources.HostClockSync, (!onConnect || t.sources.GpsdSync) && t.sources.Gpsd != "", coarse, location, nudge)
	log.Printf("tenmicron: initialization finished; see SourceStatus for applied/skipped steps")
}

func stepOutcome(err error) any {
	if err != nil {
		return err
	}
	return "ok"
}

func (s *sourceInitialization) advance(clock, gps bool, coarse, location, nudge func() error) {
	if !s.requested {
		return
	}
	for i, stage := range []struct {
		enabled bool
		done    *bool
		run     func() error
	}{
		{clock, &s.coarse, coarse}, {gps, &s.location, location}, {clock, &s.nudge, nudge},
	} {
		if stage.enabled && !*stage.done {
			err := stage.run()
			// Skip failed stages and allow independent sources to proceed.
			// Never nudge after a failed initial clock set.
			*stage.done = true
			if err != nil && i == 0 {
				s.nudge = true
			}
		}
	}
	s.requested = false
}

// Weather remains live throughout tracking, but only changed wire-resolution
// values are sent. Cache each successful acknowledgement independently so a
// partial failure retries only the field that failed. Reconnection clears it.
func (t *Telescope) applySourceWeather(ctx context.Context, env environment) error {
	t.weatherApplyMu.Lock()
	defer t.weatherApplyMu.Unlock()
	t.clockMu.Lock()
	defer t.clockMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	m := t.mount()
	if m == nil {
		return fmt.Errorf("mount disconnected")
	}
	if m != t.weatherMount {
		t.weatherMount = m
		t.weatherApplied = envApplied{}
	}
	last := &t.weatherApplied
	p, c := math.Round(*env.PressureHPa*10)/10, math.Round(*env.TemperatureC*10)/10
	if !last.havePressure || last.pressure != p {
		if err := m.SetRefractionPressure(p); err != nil {
			return err
		}
		last.pressure, last.havePressure = p, true
	}
	if !last.haveTemp || last.temperature != c {
		if err := m.SetRefractionTemperature(c); err != nil {
			return err
		}
		last.temperature, last.haveTemp = c, true
	}
	return nil
}
func (t *Telescope) pullEnvironment(ctx context.Context) {
	if s := t.sources.ObservingConditions; s != nil {
		readCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		env, err := s.read(readCtx)
		cancel()
		if err == nil {
			err = t.applySourceWeather(ctx, env)
		}
		t.recordSource("weather", env, err)
	}
}
