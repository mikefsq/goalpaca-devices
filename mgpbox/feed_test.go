package driver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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
