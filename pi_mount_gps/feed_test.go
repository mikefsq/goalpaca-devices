package driver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	alpacadev "github.com/mikefsq/goalpaca/server"
	pimountgps "github.com/mikefsq/pi_mount_gps"
)

var testNow = time.Date(2026, 10, 1, 20, 0, 10, 0, time.UTC)

func fixedBoard() *fakeBoard {
	b := goodBoard()
	b.fix = pimountgps.Fix{
		Time: time.Date(2026, 10, 1, 20, 0, 0, 0, time.UTC), Valid: true, Quality: 2,
		Lat: 37.95, Lon: -122.55, AltM: 30.1, Satellites: 12,
		FixType: "3", PDOP: 1.43, HDOP: 0.7, VDOP: 1.19,
	}
	b.fixAge = 800 * time.Millisecond
	b.hasFix = true
	return b
}

func TestBuildEnvChronyTime(t *testing.T) {
	p, src := buildEnv(fixedBoard(), i2cFix(fixedBoard()), testNow, true)
	if src != timeChrony || p == nil || *p.Time != "2026-10-01T20:00:10Z" {
		t.Fatalf("src=%s payload=%+v", src, p)
	}
	if *p.PressureHPa != 1008.4 || *p.TemperatureC != 12.5 || *p.HumidityPct != 80 || *p.DewpointC != 9.1 {
		t.Fatalf("weather = %+v", p)
	}
	if *p.Latitude != 37.95 || *p.Longitude != -122.55 || *p.ElevationM != 30.1 {
		t.Fatalf("site = %+v", p)
	}
}

func TestBuildEnvFallsBackToGPSTime(t *testing.T) {
	p, src := buildEnv(fixedBoard(), i2cFix(fixedBoard()), testNow, false)
	// fix time 20:00:00 + 0.8 s age, formatted to whole seconds.
	if src != timeGPS || *p.Time != "2026-10-01T20:00:00Z" {
		t.Fatalf("src=%s time=%v", src, *p.Time)
	}
}

func TestBuildEnvNoFixNoSite(t *testing.T) {
	b := fixedBoard()
	b.fixStale = true
	p, src := buildEnv(b, i2cFix(b), testNow, false)
	if src != timeNone || p.Time != nil || p.Latitude != nil || p.ElevationM != nil {
		t.Fatalf("stale fix leaked site/time: src=%s %+v", src, p)
	}
	if p.PressureHPa == nil {
		t.Fatal("weather missing")
	}
	// chrony time still goes out without a fix; the site does not.
	p, src = buildEnv(b, i2cFix(b), testNow, true)
	if src != timeChrony || p.Time == nil || p.Latitude != nil {
		t.Fatalf("src=%s %+v", src, p)
	}
}

func TestBuildEnvOmitsMissingAndStale(t *testing.T) {
	b := fixedBoard()
	b.noSHT = true
	p, _ := buildEnv(b, i2cFix(b), testNow, false)
	if p.HumidityPct != nil || p.DewpointC != nil || p.PressureHPa == nil {
		t.Fatalf("no SHT45: %+v", p)
	}
	b = fixedBoard()
	b.stale = true
	p, _ = buildEnv(b, i2cFix(b), testNow, false)
	if p.TemperatureC != nil || p.PressureHPa != nil || p.Latitude == nil {
		t.Fatalf("stale weather: %+v", p)
	}
	b.fixStale = true
	if p, _ := buildEnv(b, i2cFix(b), testNow, false); p != nil {
		t.Fatalf("nothing fresh should send nothing, got %+v", p)
	}
}

// alpacaServer records setenvironment pushes and answers like an Alpaca device.
type alpacaServer struct {
	mu     sync.Mutex
	paths  []string
	params []string
}

func (s *alpacaServer) handler(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	s.mu.Lock()
	s.paths = append(s.paths, r.URL.Path)
	s.params = append(s.params, r.Form.Get("Parameters"))
	s.mu.Unlock()
	json.NewEncoder(w).Encode(map[string]any{"Value": `{"applied":["pressure"]}`, "ErrorNumber": 0})
}

func TestPushFeedAction(t *testing.T) {
	srv := &alpacaServer{}
	ts := httptest.NewServer(http.HandlerFunc(srv.handler))
	defer ts.Close()

	w := newConnected(t, fixedBoard())
	w.now = func() time.Time { return testNow }
	addr := strings.TrimPrefix(ts.URL, "http://")
	if _, err := w.Action("Feed", addr+"/telescope/0"); err != nil {
		t.Fatal(err)
	}
	if got, _ := w.Action("feed", ""); got != addr+"/telescope/0" {
		t.Fatalf("feed = %q", got)
	}
	if _, err := w.Action("PushFeed", ""); err != nil {
		t.Fatal(err)
	}
	if len(srv.paths) != 1 || srv.paths[0] != "/api/v1/telescope/0/action" {
		t.Fatalf("paths = %v", srv.paths)
	}
	var env envPayload
	if err := json.Unmarshal([]byte(srv.params[0]), &env); err != nil {
		t.Fatal(err)
	}
	if env.PressureHPa == nil || env.Latitude == nil || env.Time == nil {
		t.Fatalf("pushed %s", srv.params[0])
	}
	if src, _ := w.Action("TimeSource", ""); src != timeGPS {
		t.Fatalf("TimeSource = %q", src)
	}
	var health []FeedHealth
	st, _ := w.Action("FeedStatus", "")
	if err := json.Unmarshal([]byte(st), &health); err != nil || len(health) != 1 || !health[0].Healthy {
		t.Fatalf("FeedStatus = %s", st)
	}
}

func TestFeedBacksOffDeadTarget(t *testing.T) {
	w := newConnected(t, fixedBoard())
	clock := testNow
	w.now = func() time.Time { return clock }
	if err := w.SetFeedTargets([]FeedTarget{{Addr: "127.0.0.1:1"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.pushEnvironment(context.Background()); err == nil {
		t.Fatal("push to a closed port succeeded")
	}
	out, _ := w.pushEnvironment(context.Background())
	if !strings.Contains(out, "backing off") {
		t.Fatalf("second push not skipped: %s", out)
	}
	if st := w.DeviceState(); len(st) != 2 || st[1].Value != 1 {
		t.Fatalf("DeviceState = %+v", st)
	}
}

func TestGpsAction(t *testing.T) {
	w := newConnected(t, fixedBoard())
	out, err := w.Action("gps", "")
	if err != nil {
		t.Fatal(err)
	}
	want := `{"valid":true,"time":"2026-10-01T20:00:00Z","latitude":37.95,"longitude":-122.55,` +
		`"altitude":30.1,"satellites":12,"fixQuality":"2","fixType":"3","pdop":1.43,"hdop":0.7,"vdop":1.19}`
	if out != want {
		t.Fatalf("gps =\n%s\nwant\n%s", out, want)
	}
	if _, err := w.Action("gps", "x"); err == nil {
		t.Fatal("gps accepted a value")
	}
}

func TestParseChronyTracking(t *testing.T) {
	synced := "ACEB3C08,172.235.60.8,3,1790885506.910560937,0.000034068,-0.000046071,0.000086028,0.999,-0.005,0.154,0.026754815,0.000543375,260.0,Normal\n"
	if !parseChronyTracking(synced) {
		t.Fatal("Normal not synced")
	}
	unsynced := "00000000,,0,0.000000000,0.000000000,0.000000000,0.000000000,0.000,0.000,0.000,1.000000000,1.000000000,0.0,Not synchronised\n"
	if parseChronyTracking(unsynced) || parseChronyTracking("") {
		t.Fatal("unsynced read as synced")
	}
}

// TestScalarActions pins the scalar formats to mgpbox's.
func TestScalarActions(t *testing.T) {
	w := newConnected(t, fixedBoard())
	for name, want := range map[string]string{
		"Temperature": "12.5", "Humidity": "80", "Pressure": "1008.4", "DewPoint": "9.1",
		"Latitude": "37.950000", "Longitude": "-122.550000", "Altitude": "30.1",
		"Satellites": "12", "FixQuality": "2", "FixType": "3",
		"PDOP": "1.43", "HDOP": "0.7", "VDOP": "1.19", "GpsTime": "2026-10-01T20:00:00Z",
		"GpsSource": "i2c",
	} {
		got, err := w.Action(name, "")
		if err != nil || got != want {
			t.Errorf("%s = %q, %v; want %q", name, got, err, want)
		}
	}
	if _, err := w.Action("Bogus", ""); !errors.Is(err, alpacadev.ErrActionNotImplemented) {
		t.Fatalf("Bogus err = %v", err)
	}
}

func TestActionsNeedBoard(t *testing.T) {
	w := NewDriver(pimountgps.DefaultConfig(), "")
	for _, a := range []string{"Gps", "Latitude", "Temperature"} {
		if _, err := w.Action(a, ""); !errors.Is(err, alpacadev.ErrNotConnected) {
			t.Errorf("%s err = %v, want NotConnected", a, err)
		}
	}
	if got, err := w.Action("Feed", ""); err != nil || got != "off" {
		t.Fatalf("Feed without board = %q, %v", got, err)
	}
}

// i2cFix is the fix a driver without gpsd would take from b.
func i2cFix(b *fakeBoard) gpsFix { return (&Driver{gpsBoard: b}).currentFix() }
