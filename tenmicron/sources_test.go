package driver

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	lx200 "github.com/mikefsq/lx200"
	mountdriver "github.com/mikefsq/lx200/tenmicron"
)

func weatherServer(t *testing.T, age float64, connected bool, failPressure bool) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/v1/observingconditions/2/") {
			t.Errorf("wrong device: %s", r.URL.Path)
		}
		var v any
		errCode := 0
		switch r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:] {
		case "connected":
			v = connected
		case "pressure":
			v = 1000.2
			if failPressure {
				errCode = 1024
			}
		case "temperature":
			v = 12.5
		case "timesincelastupdate":
			v = age
			if n := r.URL.Query().Get("SensorName"); n != "pressure" && n != "temperature" {
				t.Error("missing sensor freshness check")
			}
		default:
			t.Errorf("unexpected URL %s", r.URL.Path)
		}
		json.NewEncoder(w).Encode(map[string]any{"Value": v, "ErrorNumber": errCode, "ErrorMessage": "test"})
	}))
	t.Cleanup(s.Close)
	return s
}
func TestWeatherSourceRejectsUnavailableReadings(t *testing.T) {
	for _, tc := range []struct {
		age                 float64
		connected, fail, ok bool
	}{{1, true, false, true}, {31, true, false, false}, {-1, true, false, false}, {1, false, false, false}, {1, true, true, false}} {
		s := weatherServer(t, tc.age, tc.connected, tc.fail)
		env, err := (WeatherSource{Addr: s.URL, Device: 2}).read(context.Background())
		if (err == nil) != tc.ok {
			t.Fatalf("case %+v err %v", tc, err)
		}
		if tc.ok && (*env.PressureHPa != 1000.2 || *env.TemperatureC != 12.5 || env.Time != nil || env.Latitude != nil) {
			t.Fatalf("weather payload %+v", env)
		}
	}
}
func TestPullWeatherAppliesAndReports(t *testing.T) {
	s := weatherServer(t, 1, true, false)
	f := &fakeTransport{replies: map[string]string{":SRPRS1000.2#": "1", ":SRTMP+012.5#": "1"}}
	tel := NewTelescope("test")
	tel.m = &mountdriver.Mount{Conn: lx200.New(f, time.Second)}
	tel.sources.ObservingConditions = &WeatherSource{Addr: s.URL, Device: 2}
	tel.pullEnvironment(context.Background())
	if !f.wrote(":SRPRS1000.2#") || !f.wrote(":SRTMP+012.5#") {
		t.Fatal("missing refraction writes")
	}
	sstat := tel.sourceStatus["weather"]
	if sstat.Error != "" || sstat.LastSuccess.IsZero() {
		t.Fatalf("status %+v", sstat)
	}
	result, err := tel.actionSourceStatus("")
	if err != nil || !strings.Contains(result, `"enabled":true`) {
		t.Fatalf("status %s %v", result, err)
	}
}
func TestGPSLocationValidation(t *testing.T) {
	now := time.Now().UTC()
	base := map[string]any{"class": "TPV", "mode": 3, "time": now.Format(time.RFC3339Nano), "lat": 38.0, "lon": -122.0, "altMSL": 30.0}
	for _, tc := range []struct {
		key   string
		value any
		valid bool
	}{{"mode", 3, true}, {"mode", 2, false}, {"lat", nil, false}, {"lon", 181, false}, {"altMSL", nil, false}, {"altMSL", 11000, false}, {"time", now.Add(-10 * time.Second).Format(time.RFC3339Nano), false}, {"time", now.Add(10 * time.Second).Format(time.RFC3339Nano), false}} {
		r := map[string]any{}
		for k, v := range base {
			r[k] = v
		}
		r[tc.key] = tc.value
		data, _ := json.Marshal(r)
		env, ok := gpsLocation(data, now)
		if ok != tc.valid {
			t.Fatalf("%s=%v accepted=%v", tc.key, tc.value, ok)
		}
		if ok && (env.Time != nil || *env.Latitude != 38 || *env.ElevationM != 30) {
			t.Fatalf("location payload %+v", env)
		}
	}
}
func TestGPSLocationStream(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		c, e := l.Accept()
		if e != nil {
			return
		}
		defer c.Close()
		c.SetDeadline(time.Now().Add(time.Second))
		buf := make([]byte, 512)
		c.Read(buf)
		fmt.Fprintf(c, "{\"class\":\"VERSION\"}\n{\"class\":\"TPV\",\"mode\":3,\"time\":%q,\"lat\":38,\"lon\":-122,\"altMSL\":30}\n", time.Now().UTC().Format(time.RFC3339Nano))
	}()
	env, err := readGPSLocation(context.Background(), l.Addr().String())
	if err != nil || env.Latitude == nil {
		t.Fatalf("gpsd read %+v %v", env, err)
	}
	<-done
}
func TestSourceConfiguration(t *testing.T) {
	for _, c := range []Config{{ObservingConditions: &WeatherSource{Addr: "", Device: 0}}, {Gpsd: "localhost"}, {ObservingConditions: &WeatherSource{Addr: "localhost:1234", Device: -1}}} {
		if c.validateSources() == nil {
			t.Fatalf("accepted %+v", c)
		}
	}
	if err := (Config{Gpsd: "localhost:2947", ObservingConditions: &WeatherSource{Addr: "localhost:11304", Device: 0}, HostClockSync: true}).validateSources(); err != nil {
		t.Fatal(err)
	}
	if err := (Config{}).validateSources(); err != nil {
		t.Fatal(err)
	}
}

func TestInitializationSingleAttempt(t *testing.T) {
	var order []string
	step := func(name string) func() error {
		return func() error {
			order = append(order, name)
			if name == "gps" {
				return fmt.Errorf("no fix")
			}
			return nil
		}
	}
	state := sourceInitialization{requested: true}
	state.advance(true, true, step("utc"), step("gps"), step("nudge"))
	state.advance(true, true, step("utc"), step("gps"), step("nudge"))
	if state.requested || strings.Join(order, ",") != "utc,gps,nudge" {
		t.Fatalf("unexpected repeat: %v", order)
	}
	state = sourceInitialization{requested: true}
	state.advance(true, true, step("utc"), step("gps"), step("nudge"))
	if len(order) != 6 {
		t.Fatal("reconnect did not initialize")
	}
	state = sourceInitialization{}
	state.advance(true, true, step("utc"), step("gps"), step("nudge"))
	if len(order) != 6 {
		t.Fatal("boot triggered initialization")
	}
}

func TestWeatherOnlyWritesChanges(t *testing.T) {
	f := &fakeTransport{replies: map[string]string{":SRPRS1000.2#": "1", ":SRPRS1001.0#": "1", ":SRTMP+012.5#": "1"}}
	tel := NewTelescope("test")
	tel.m = &mountdriver.Mount{Conn: lx200.New(f, time.Second)}
	env := environment{PressureHPa: ptr(1000.2), TemperatureC: ptr(12.5)}
	for i := 0; i < 2; i++ {
		if err := tel.applySourceWeather(context.Background(), env); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.writes) != 2 {
		t.Fatalf("unchanged data resent: %v", f.writes)
	}
	env.PressureHPa = ptr(1000.21)
	tel.applySourceWeather(context.Background(), env)
	if len(f.writes) != 2 {
		t.Fatal("sub-resolution change resent")
	}
	env.PressureHPa = ptr(1001.0)
	tel.applySourceWeather(context.Background(), env)
	if len(f.writes) != 3 || f.writes[2] != ":SRPRS1001.0#" {
		t.Fatalf("changed pressure: %v", f.writes)
	}
}

func TestIdleInitializationRejectsTracking(t *testing.T) {
	f := &fakeTransport{replies: map[string]string{":Ginfo#": statusIdle}}
	tel := NewTelescope("test")
	tel.m = &mountdriver.Mount{Conn: lx200.New(f, time.Second)}
	called := false
	err := tel.withIdleMount(false, func(*mountdriver.Mount) error { called = true; return nil })
	if err == nil || called {
		t.Fatalf("tracking mount allowed initialization: %v", err)
	}
}

func TestFailedClockNeverRetriesOrNudges(t *testing.T) {
	clockCalls, gpsCalls, nudgeCalls := 0, 0, 0
	state := sourceInitialization{requested: true}
	for i := 0; i < 4; i++ {
		state.advance(true, true, func() error { clockCalls++; return fmt.Errorf("clock unavailable or unstable") }, func() error { gpsCalls++; return nil }, func() error { nudgeCalls++; return nil })
	}
	if state.requested || clockCalls != 1 || gpsCalls != 1 || nudgeCalls != 0 {
		t.Fatalf("calls: %d %d %d", clockCalls, gpsCalls, nudgeCalls)
	}
}

func TestAbsentGPSDoesNotPreventClockNudge(t *testing.T) {
	var order []string
	state := sourceInitialization{requested: true}
	state.advance(true, true, func() error { order = append(order, "utc"); return nil }, func() error { order = append(order, "gps absent"); return fmt.Errorf("connection refused") }, func() error { order = append(order, "nudge"); return nil })
	if state.requested || strings.Join(order, ",") != "utc,gps absent,nudge" {
		t.Fatalf("%v %+v", order, state)
	}
}

func TestGPSReportsMissingFixAndUnavailableService(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		c, err := l.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		c.SetDeadline(time.Now().Add(time.Second))
		buf := make([]byte, 512)
		c.Read(buf)
		fmt.Fprintln(c, `{"class":"TPV","mode":1}`)
		c.Read(buf)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err = readGPSLocation(ctx, l.Addr().String())
	if err == nil || !strings.Contains(err.Error(), "no fresh 3D fix") {
		t.Fatalf("live gpsd should report missing fix: %v", err)
	}
	<-done
	addr := l.Addr().String()
	l.Close()
	_, err = readGPSLocation(context.Background(), addr)
	if err == nil {
		t.Fatalf("absent gpsd must report error: %v", err)
	}
}

func TestRepeatedConnectDoesNotInitialize(t *testing.T) {
	tel := NewTelescope("test")
	tel.m = &mountdriver.Mount{}
	// An attempted initialization, even a failed one, must not repeat.
	tel.initialization = sourceInitialization{mount: tel.m}
	tel.sources.HostClockSync = true
	if err := tel.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if tel.initialization.requested {
		t.Fatal("repeated connect scheduled initialization")
	}
	if err := tel.Disconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if tel.initialization.mount != nil {
		t.Fatal("disconnect did not clear initialization")
	}
}

func TestConnectWithoutClockSyncDoesNotInitialize(t *testing.T) {
	tel := NewTelescope("test")
	tel.m = &mountdriver.Mount{}
	tel.sources.Gpsd = "localhost:2947"
	if err := tel.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if tel.initialization.mount != nil {
		t.Fatal("disabled auto sync initialized sources")
	}
}

func TestGPSSyncIndependentOnConnect(t *testing.T) {
	var cfg Config
	if err := json.Unmarshal([]byte(`{"gpsd":"127.0.0.1:2947"}`), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.GpsdSync || cfg.HostClockSync {
		t.Fatal("sync must default off")
	}
	tel := NewTelescope("test")
	tel.m = &mountdriver.Mount{}
	tel.sources = Config{GpsdSync: true, Gpsd: "invalid-address"}
	if err := tel.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	status := tel.sourceStatus["location"]
	if status.LastAttempt.IsZero() || status.Error == "" {
		t.Fatal("GPS-only connect did not attempt location")
	}
	if !tel.sourceStatus["clock"].LastAttempt.IsZero() {
		t.Fatal("GPS-only connect attempted clock")
	}
	if err := tel.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if tel.sourceStatus["location"].LastAttempt != status.LastAttempt {
		t.Fatal("GPS retried on repeated connect")
	}
}
