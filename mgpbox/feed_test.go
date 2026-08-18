package driver

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mikefsq/astromi.ch/mgpbox"
)

// TestBuildEnv checks the payload assembled from a snapshot with meteo + a GPS fix.
func TestBuildEnv(t *testing.T) {
	box := fakeBoxGPS()
	defer box.Close()
	// Wait for the reader to fold in both lines.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := box.Meteo(); ok {
			if fx, ok := box.Fix(); ok && fx.Valid {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	p := buildEnv(box)
	if p == nil {
		t.Fatal("buildEnv = nil")
	}
	if p.PressureHPa == nil || *p.PressureHPa < 1015 || *p.PressureHPa > 1016 {
		t.Errorf("pressure = %v", p.PressureHPa)
	}
	if p.TemperatureC == nil || p.DewpointC == nil || p.HumidityPct == nil {
		t.Error("missing a meteo field")
	}
	if p.Latitude == nil || *p.Latitude < 51.5 || *p.Latitude > 51.6 {
		t.Errorf("latitude = %v", p.Latitude)
	}
	if p.Time == nil {
		t.Error("missing time")
	}
}

// A box with no dew-point transducer must still feed a usable dew point: derived from
// the temperature and humidity it does report, and identical to what its
// ObservingConditions property reports for the same sample. Shipping the unreported 0
// instead would give a consumer a margin of the whole air temperature ("bone dry") and
// switch its dew heaters off on exactly the night they are needed.
func TestBuildEnvDerivesDewpoint(t *testing.T) {
	// The standard meteo line (23.5 °C, 54 %RH) with the dew-point transducer zeroed.
	const s = "$PXDR,P,101531.0,P,0,C,23.5,C,1,H,54.0,P,2,C,0.0,C,3,1.1*36\n"
	box := mgpbox.New(newFakeT(s), mgpbox.DeviceInfo{Port: "fake"})
	defer box.Close()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := box.Meteo(); ok {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	p := buildEnv(box)
	if p == nil {
		t.Fatal("buildEnv = nil")
	}
	if p.DewpointC == nil {
		t.Fatal("DewpointC omitted; want one derived from temperature + humidity")
	}
	// 23.5 °C at 54 %RH derives to ~13.65 °C — and the transducer, when this box has
	// one, reads 13.6, so the estimate is checked against the hardware's own answer.
	if math.Abs(*p.DewpointC-13.65) > 0.1 {
		t.Errorf("DewpointC = %v, want ~13.65 (derived)", *p.DewpointC)
	}
	// It must not be the unreported zero.
	if *p.DewpointC == 0 {
		t.Error("DewpointC = 0 — the unreported value shipped")
	}
}

// TestPushEnvironment posts to a mock mount server and checks the request shape.
func TestPushEnvironment(t *testing.T) {
	var gotAction, gotParams string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		gotAction = r.PostForm.Get("Action")
		gotParams = r.PostForm.Get("Parameters")
		if !strings.HasSuffix(r.URL.Path, "/telescope/0/action") {
			t.Errorf("path = %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"Value":"{\"applied\":[\"pressure\"]}","ErrorNumber":0,"ErrorMessage":""}`))
	}))
	defer srv.Close()

	m := NewMGPBox(0)
	m.openDev = func() (*mgpbox.MGPBox, error) { return fakeBoxGPS(), nil }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Open(ctx)
	for !m.Connected() && ctx.Err() == nil {
		time.Sleep(10 * time.Millisecond)
	}
	m.SetMountFeed(strings.TrimPrefix(srv.URL, "http://"), 0)

	out, err := m.pushEnvironment(ctx)
	if err != nil {
		t.Fatalf("pushEnvironment: %v", err)
	}
	if gotAction != "setenvironment" {
		t.Errorf("action = %q, want setenvironment", gotAction)
	}
	var env envPayload
	if err := json.Unmarshal([]byte(gotParams), &env); err != nil {
		t.Fatalf("params not JSON: %v (%q)", err, gotParams)
	}
	if env.PressureHPa == nil || env.Latitude == nil {
		t.Errorf("payload missing fields: %s", gotParams)
	}
	if !strings.Contains(out, "applied") {
		t.Errorf("reply = %q", out)
	}
}

func TestPushEnvironmentOffIsNoop(t *testing.T) {
	m := NewMGPBox(0)
	m.openDev = func() (*mgpbox.MGPBox, error) { return fakeBoxGPS(), nil }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Open(ctx)
	for !m.Connected() && ctx.Err() == nil {
		time.Sleep(10 * time.Millisecond)
	}
	// No mount configured → no-op, no error.
	if out, err := m.pushEnvironment(ctx); err != nil || out != "" {
		t.Errorf("pushEnvironment(off) = %q, %v; want \"\", nil", out, err)
	}
}

func TestActionMountFeed(t *testing.T) {
	m := NewMGPBox(0)
	if v, _ := m.Action("mountfeed", ""); v != "off" {
		t.Errorf("mountfeed (initial) = %q, want off", v)
	}
	if v, err := m.Action("mountfeed", "10.0.0.9:11110/2"); err != nil || v != "ok" {
		t.Fatalf("mountfeed set = %q, %v", v, err)
	}
	if v, _ := m.Action("mountfeed", ""); v != "10.0.0.9:11110 (telescope 2)" {
		t.Errorf("mountfeed (read) = %q", v)
	}
	if v, _ := m.Action("mountfeed", "off"); v != "ok" {
		t.Errorf("mountfeed off = %q", v)
	}
	if v, _ := m.Action("mountfeed", ""); v != "off" {
		t.Errorf("mountfeed after off = %q", v)
	}
}

func TestParseFeedTarget(t *testing.T) {
	tests := []struct {
		in   string
		want string
		bad  bool
	}{
		// Historical spellings must keep meaning what they always did.
		{in: "10.0.1.5:11111", want: "10.0.1.5:11111/telescope/0"},
		{in: "10.0.1.5:11111/2", want: "10.0.1.5:11111/telescope/2"},
		// The general form.
		{in: "localhost:11130/switch/0", want: "localhost:11130/switch/0"},
		{in: "localhost:11130/switch", want: "localhost:11130/switch/0"},
		{in: "host:1/telescope/1", want: "host:1/telescope/1"},
		// A typo'd device type must be rejected, not silently POSTed into the void.
		{in: "host:1/dome/0", bad: true},
		{in: "host:1/switch/x", bad: true},
		{in: "", bad: true},
	}
	for _, tt := range tests {
		got, err := ParseFeedTarget(tt.in)
		if tt.bad {
			if err == nil {
				t.Errorf("ParseFeedTarget(%q) = %v, want an error", tt.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseFeedTarget(%q): %v", tt.in, err)
			continue
		}
		if got.String() != tt.want {
			t.Errorf("ParseFeedTarget(%q) = %s, want %s", tt.in, got, tt.want)
		}
	}
}

// The snapshot must reach every configured target — a mount and a dew switch — each at
// its own device-type path, and one unreachable target must not cost the others their
// weather.
func TestFeedPushesToAllTargets(t *testing.T) {
	var mu sync.Mutex
	got := map[string]string{} // request path -> Parameters

	handler := func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		mu.Lock()
		got[r.URL.Path] = r.PostForm.Get("Parameters")
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"Value":"ok","ErrorNumber":0,"ErrorMessage":""}`))
	}
	mount := httptest.NewServer(http.HandlerFunc(handler))
	defer mount.Close()
	sw := httptest.NewServer(http.HandlerFunc(handler))
	defer sw.Close()

	m := NewMGPBox(0)
	m.openDev = func() (*mgpbox.MGPBox, error) { return fakeBoxGPS(), nil }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Open(ctx)
	for !m.Connected() && ctx.Err() == nil {
		time.Sleep(10 * time.Millisecond)
	}

	targets := []FeedTarget{
		{Addr: strings.TrimPrefix(mount.URL, "http://"), Type: "telescope", Device: 0},
		{Addr: strings.TrimPrefix(sw.URL, "http://"), Type: "switch", Device: 0},
		{Addr: "127.0.0.1:1", Type: "switch", Device: 0}, // refused: must not stop the others
	}
	if err := m.SetFeedTargets(targets); err != nil {
		t.Fatal(err)
	}
	// The unreachable target surfaces an error, but the reachable ones still got fed.
	if _, err := m.pushEnvironment(ctx); err == nil {
		t.Error("pushEnvironment: want an error from the unreachable target")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("pushed to %d targets, want 2: %v", len(got), got)
	}
	for _, path := range []string{"/api/v1/telescope/0/action", "/api/v1/switch/0/action"} {
		body, ok := got[path]
		if !ok {
			t.Errorf("no push to %s", path)
			continue
		}
		var env envPayload
		if err := json.Unmarshal([]byte(body), &env); err != nil {
			t.Errorf("%s: params not JSON: %v", path, err)
			continue
		}
		// Both consumers get the whole snapshot and each takes what it understands.
		if env.TemperatureC == nil || env.HumidityPct == nil || env.DewpointC == nil {
			t.Errorf("%s: missing weather in %s", path, body)
		}
		if env.PressureHPa == nil || env.Latitude == nil {
			t.Errorf("%s: missing refraction/site datums in %s", path, body)
		}
	}
}

// The historical single-mount API and the new list must stay coherent.
func TestFeedActionAndBackCompat(t *testing.T) {
	m := NewMGPBox(0)
	if v, _ := m.Action("feed", ""); v != "off" {
		t.Errorf("feed (initial) = %q, want off", v)
	}
	// Set a two-target list through the Action.
	if v, err := m.Action("Feed", "10.0.1.5:11111/telescope/0,localhost:11130/switch/0"); err != nil || v != "ok" {
		t.Fatalf("feed set = %q, %v", v, err)
	}
	if v, _ := m.Action("feed", ""); v != "10.0.1.5:11111/telescope/0,localhost:11130/switch/0" {
		t.Errorf("feed (read) = %q", v)
	}
	// MountFeed reads the telescope entry out of the list.
	if v, _ := m.Action("mountfeed", ""); v != "10.0.1.5:11111 (telescope 0)" {
		t.Errorf("mountfeed (read) = %q", v)
	}
	// An invalid entry rejects the whole set rather than dropping one consumer.
	if _, err := m.Action("feed", "10.0.1.5:11111,host:2/dome/0"); err == nil {
		t.Error("feed with a bad target: want an error")
	}
	if v, _ := m.Action("feed", ""); v != "10.0.1.5:11111/telescope/0,localhost:11130/switch/0" {
		t.Errorf("a rejected set changed the list: %q", v)
	}
	if v, _ := m.Action("feed", "off"); v != "ok" {
		t.Errorf("feed off = %q", v)
	}
	if v, _ := m.Action("feed", ""); v != "off" {
		t.Errorf("feed after off = %q", v)
	}
}

func TestBackoffSchedule(t *testing.T) {
	// One cycle, then doubling, capped.
	want := []time.Duration{
		30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute,
		8 * time.Minute, 15 * time.Minute, 15 * time.Minute, 15 * time.Minute,
	}
	for i, w := range want {
		if got := backoffFor(i + 1); got != w {
			t.Errorf("backoffFor(%d) = %v, want %v", i+1, got, w)
		}
	}
	if got := backoffFor(100); got != feedBackoffMax {
		t.Errorf("backoffFor(100) = %v, want the %v cap", got, feedBackoffMax)
	}
}

// A failing target must be retried on a backoff rather than on every cycle — otherwise a
// mount that is switched off costs a 10 s timeout every 30 s all night — while a healthy
// target alongside it keeps being fed on schedule.
func TestFeedBacksOffFailingTargetOnly(t *testing.T) {
	var mu sync.Mutex
	var okHits, deadHits int

	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		okHits++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"Value":"ok","ErrorNumber":0,"ErrorMessage":""}`))
	}))
	defer good.Close()
	// A target that always returns an Alpaca error.
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		deadHits++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"Value":"","ErrorNumber":1025,"ErrorMessage":"nope"}`))
	}))
	defer dead.Close()

	m := NewMGPBox(0)
	m.openDev = func() (*mgpbox.MGPBox, error) { return fakeBoxGPS(), nil }
	clock := time.Date(2026, 7, 14, 22, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return clock }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Open(ctx)
	for !m.Connected() && ctx.Err() == nil {
		time.Sleep(10 * time.Millisecond)
	}
	if err := m.SetFeedTargets([]FeedTarget{
		{Addr: strings.TrimPrefix(good.URL, "http://"), Type: "switch"},
		{Addr: strings.TrimPrefix(dead.URL, "http://"), Type: "telescope"},
	}); err != nil {
		t.Fatal(err)
	}

	// Cycle 1: both tried; the dead one fails and earns a 30 s backoff.
	if _, err := m.pushEnv(ctx, false); err == nil {
		t.Error("want an error from the dead target")
	}
	// Cycles 2 and 3, 10 s apart: inside the dead target's backoff, so it is skipped —
	// but the healthy target is still fed every time.
	clock = clock.Add(10 * time.Second)
	out, _ := m.pushEnv(ctx, false)
	if !strings.Contains(out, "backing off") {
		t.Errorf("second cycle should report the skip: %s", out)
	}
	clock = clock.Add(10 * time.Second)
	m.pushEnv(ctx, false)

	mu.Lock()
	if deadHits != 1 {
		t.Errorf("dead target hit %d times, want 1 (the rest backed off)", deadHits)
	}
	if okHits != 3 {
		t.Errorf("healthy target hit %d times, want 3 (never penalised)", okHits)
	}
	mu.Unlock()

	// Past the backoff, it is retried — and fails again, doubling to 60 s.
	clock = clock.Add(30 * time.Second)
	m.pushEnv(ctx, false)
	mu.Lock()
	if deadHits != 2 {
		t.Errorf("dead target hit %d times after the backoff expired, want 2", deadHits)
	}
	mu.Unlock()
	clock = clock.Add(31 * time.Second) // inside the new 60 s window
	m.pushEnv(ctx, false)
	mu.Lock()
	if deadHits != 2 {
		t.Errorf("dead target hit %d times, want 2 (backoff should have doubled to 60s)", deadHits)
	}
	mu.Unlock()

	// An operator-triggered push ignores the backoff entirely.
	if _, err := m.pushEnvironmentNow(ctx); err == nil {
		t.Error("manual push: want the dead target's error")
	}
	mu.Lock()
	if deadHits != 3 {
		t.Errorf("manual push did not bypass the backoff (hits = %d, want 3)", deadHits)
	}
	mu.Unlock()
}

// A target that comes back must be fed immediately again, not left on its last backoff.
func TestFeedRecoveryResetsBackoff(t *testing.T) {
	var mu sync.Mutex
	var hits int
	healthy := false

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		up := healthy
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if !up {
			w.Write([]byte(`{"Value":"","ErrorNumber":1025,"ErrorMessage":"down"}`))
			return
		}
		w.Write([]byte(`{"Value":"ok","ErrorNumber":0,"ErrorMessage":""}`))
	}))
	defer srv.Close()

	m := NewMGPBox(0)
	m.openDev = func() (*mgpbox.MGPBox, error) { return fakeBoxGPS(), nil }
	clock := time.Date(2026, 7, 14, 22, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return clock }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Open(ctx)
	for !m.Connected() && ctx.Err() == nil {
		time.Sleep(10 * time.Millisecond)
	}
	m.SetFeedTargets([]FeedTarget{{Addr: strings.TrimPrefix(srv.URL, "http://"), Type: "switch"}})

	// Fail twice: backoff grows to 60 s.
	m.pushEnv(ctx, false)
	clock = clock.Add(30 * time.Second)
	m.pushEnv(ctx, false)

	// It comes back; wait out the 60 s and push.
	mu.Lock()
	healthy = true
	mu.Unlock()
	clock = clock.Add(61 * time.Second)
	if _, err := m.pushEnv(ctx, false); err != nil {
		t.Fatalf("recovered target: %v", err)
	}
	mu.Lock()
	before := hits
	mu.Unlock()

	// The very next cycle must feed it again — no residual backoff.
	clock = clock.Add(30 * time.Second)
	if _, err := m.pushEnv(ctx, false); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if hits != before+1 {
		t.Errorf("recovered target not fed on the next cycle (hits %d -> %d)", before, hits)
	}
	mu.Unlock()
}

// FeedStatus must let an operator see that a consumer stopped being fed — the feed runs
// in the background, so without a readable status its failures are visible only in the log.
func TestFeedStatusAction(t *testing.T) {
	var mu sync.Mutex
	up := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		ok := up
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if ok {
			w.Write([]byte(`{"Value":"ok","ErrorNumber":0,"ErrorMessage":""}`))
			return
		}
		w.Write([]byte(`{"Value":"","ErrorNumber":1025,"ErrorMessage":"mount is off"}`))
	}))
	defer srv.Close()

	m := NewMGPBox(0)
	m.openDev = func() (*mgpbox.MGPBox, error) { return fakeBoxGPS(), nil }
	clock := time.Date(2026, 7, 14, 22, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return clock }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Open(ctx)
	for !m.Connected() && ctx.Err() == nil {
		time.Sleep(10 * time.Millisecond)
	}
	addr := strings.TrimPrefix(srv.URL, "http://")
	m.SetFeedTargets([]FeedTarget{{Addr: addr, Type: "switch"}})

	status := func() FeedHealth {
		t.Helper()
		raw, err := m.Action("FeedStatus", "")
		if err != nil {
			t.Fatal(err)
		}
		var hs []FeedHealth
		if err := json.Unmarshal([]byte(raw), &hs); err != nil {
			t.Fatalf("FeedStatus not JSON: %v (%s)", err, raw)
		}
		if len(hs) != 1 {
			t.Fatalf("FeedStatus = %d targets, want 1", len(hs))
		}
		return hs[0]
	}

	// A configured but never-pushed target reads healthy with zero counts.
	if h := status(); !h.Healthy || h.TotalPushes != 0 {
		t.Errorf("before any push: %+v", h)
	}

	// One good push.
	m.pushEnv(ctx, false)
	h := status()
	if !h.Healthy || h.TotalPushes != 1 || h.TotalFailures != 0 {
		t.Errorf("after a good push: %+v", h)
	}
	if h.LastSuccessSeconds == nil || *h.LastSuccessSeconds != 0 {
		t.Errorf("lastSuccessSeconds = %v, want 0", h.LastSuccessSeconds)
	}
	if n := m.feedFailures(); n != 0 {
		t.Errorf("feedFailures = %d, want 0", n)
	}

	// Now it goes down: two failures, backoff to 60 s, and the error is readable.
	mu.Lock()
	up = false
	mu.Unlock()
	clock = clock.Add(30 * time.Second)
	m.pushEnv(ctx, false)
	clock = clock.Add(30 * time.Second)
	m.pushEnv(ctx, false)

	h = status()
	if h.Healthy {
		t.Error("target should be unhealthy")
	}
	if h.ConsecutiveFailures != 2 || h.TotalFailures != 2 || h.TotalPushes != 3 {
		t.Errorf("counts wrong: %+v", h)
	}
	if !strings.Contains(h.LastError, "mount is off") {
		t.Errorf("lastError = %q, want the Alpaca message", h.LastError)
	}
	if h.RetryInSeconds != 60 {
		t.Errorf("retryInSeconds = %v, want 60", h.RetryInSeconds)
	}
	// The success it had 60 s ago is still reported — the record survives failures.
	if h.LastSuccessSeconds == nil || *h.LastSuccessSeconds != 60 {
		t.Errorf("lastSuccessSeconds = %v, want 60", h.LastSuccessSeconds)
	}
	if n := m.feedFailures(); n != 2 {
		t.Errorf("feedFailures = %d, want 2", n)
	}

	// DeviceState surfaces the same thing to a Platform 7 client.
	var failures any
	for _, sv := range m.DeviceState() {
		if sv.Name == "FeedFailures" {
			failures = sv.Value
		}
	}
	if failures != 2 {
		t.Errorf("DeviceState FeedFailures = %v, want 2", failures)
	}

	// Recovery: the streak resets but the running totals persist, so "up now, but it
	// dropped 2 pushes overnight" is still visible.
	mu.Lock()
	up = true
	mu.Unlock()
	clock = clock.Add(61 * time.Second)
	m.pushEnv(ctx, false)
	h = status()
	if !h.Healthy || h.ConsecutiveFailures != 0 {
		t.Errorf("after recovery: %+v", h)
	}
	if h.TotalFailures != 2 || h.TotalPushes != 4 {
		t.Errorf("running totals lost on recovery: %+v", h)
	}
	if h.LastError != "" || h.RetryInSeconds != 0 {
		t.Errorf("stale failure fields after recovery: %+v", h)
	}
}

// PushFeed is the current name for the manual push; PushMount is kept for old clients.
func TestPushFeedActionNames(t *testing.T) {
	m := NewMGPBox(0)
	for _, name := range []string{"PushFeed", "pushfeed", "PushMount"} {
		// No box attached and no targets: a no-op that must not error.
		if _, err := m.Action(name, ""); err != nil {
			t.Errorf("Action(%q) = %v", name, err)
		}
	}
	var advertised bool
	for _, a := range m.SupportedActions() {
		if a == "PushFeed" {
			advertised = true
		}
	}
	if !advertised {
		t.Error("PushFeed not advertised in SupportedActions")
	}
}
