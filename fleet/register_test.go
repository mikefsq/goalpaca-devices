package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"testing"

	alpacadev "github.com/mikefsq/goalpaca/server"
)

type cfgDev struct {
	DeviceName   string
	DeviceType   string
	DeviceNumber int
	UniqueID     string
}

// serveFleet registers every spec on a fresh server and returns the live base URL.
// Registration does not open hardware (that happens in srv.Run, which we don't call),
// so this exercises the dispatch + per-type numbering without any devices attached.
func serveFleet(t *testing.T, specs ...DeviceSpec) string {
	t.Helper()
	srv := alpacadev.New(alpacadev.Config{
		Discovery: alpacadev.DiscoveryConfig{Mode: alpacadev.DiscoveryOff}, ServerName: "t", Manufacturer: "t",
	})
	c := counters{}
	for _, s := range specs {
		if !s.enabled() {
			continue
		}
		if _, err := registerDevice(srv, s, s.Port, c); err != nil {
			t.Fatalf("registerDevice(%q): %v", s.Driver, err)
		}
	}
	ts := httptest.NewServer(http.HandlerFunc(srv.ServeHTTP))
	t.Cleanup(ts.Close)
	return ts.URL
}

func configured(t *testing.T, base string) []cfgDev {
	t.Helper()
	r, err := http.Get(base + "/management/v1/configureddevices")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	var out struct{ Value []cfgDev }
	if err := json.NewDecoder(r.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	sort.Slice(out.Value, func(i, j int) bool {
		if out.Value[i].DeviceType != out.Value[j].DeviceType {
			return out.Value[i].DeviceType < out.Value[j].DeviceType
		}
		return out.Value[i].DeviceNumber < out.Value[j].DeviceNumber
	})
	return out.Value
}

// TestFleetRegistersEnabledDevices: a mixed config registers each enabled device
// under the right ASCOM type, with device numbers assigned sequentially per type.
func TestFleetRegistersEnabledDevices(t *testing.T) {
	base := serveFleet(t,
		DeviceSpec{Driver: "tenmicron", Addr: "127.0.0.1:1"},
		DeviceSpec{Driver: "oasisfoc", Index: 0},
		DeviceSpec{Driver: "focuslynx", Index: 0, Channel: 1},
		DeviceSpec{Driver: "oasisfw", Index: 0},
		DeviceSpec{Driver: "asicam", Serial: "deadbeef"},
	)

	devs := configured(t, base)
	want := []cfgDev{
		{DeviceType: "camera", DeviceNumber: 0},
		{DeviceType: "filterwheel", DeviceNumber: 0},
		{DeviceType: "focuser", DeviceNumber: 0}, // oasisfoc
		{DeviceType: "focuser", DeviceNumber: 1}, // focuslynx
		{DeviceType: "telescope", DeviceNumber: 0},
	}
	if len(devs) != len(want) {
		t.Fatalf("got %d configured devices, want %d: %+v", len(devs), len(want), devs)
	}
	for i, w := range want {
		if devs[i].DeviceType != w.DeviceType || devs[i].DeviceNumber != w.DeviceNumber {
			t.Errorf("device %d = %s/%d, want %s/%d", i, devs[i].DeviceType, devs[i].DeviceNumber, w.DeviceType, w.DeviceNumber)
		}
		if devs[i].UniqueID == "" {
			t.Errorf("device %d (%s) has empty UniqueID", i, devs[i].DeviceType)
		}
	}
}

// TestFleetIdentityBinding: identity-bound focusers register from config — focuslynx
// by protocol nickname, focuscube by USB serial — numbered by config order, with no
// hardware touched at registration.
func TestFleetIdentityBinding(t *testing.T) {
	base := serveFleet(t,
		DeviceSpec{Driver: "focuslynx", Nickname: "OAG focuser"},
		DeviceSpec{Driver: "focuscube", Serial: "FT1ABCDE", MaxStep: 120000},
	)
	devs := configured(t, base)
	if len(devs) != 2 {
		t.Fatalf("got %d devices, want 2: %+v", len(devs), devs)
	}
	for i, d := range devs {
		if d.DeviceType != "focuser" || d.DeviceNumber != i {
			t.Errorf("device %d = %s/%d, want focuser/%d", i, d.DeviceType, d.DeviceNumber, i)
		}
	}
}

// TestFleetPerTypeNumbering: same-kind devices get 0,1,2… within their type.
func TestFleetPerTypeNumbering(t *testing.T) {
	base := serveFleet(t,
		DeviceSpec{Driver: "oasisfoc", Index: 0},
		DeviceSpec{Driver: "focuscube", Index: 0},
		DeviceSpec{Driver: "asieaf", Index: 0},
	)
	devs := configured(t, base)
	if len(devs) != 3 {
		t.Fatalf("got %d, want 3 focusers: %+v", len(devs), devs)
	}
	for i, d := range devs {
		if d.DeviceType != "focuser" || d.DeviceNumber != i {
			t.Errorf("device %d = %s/%d, want focuser/%d", i, d.DeviceType, d.DeviceNumber, i)
		}
	}
}

// TestFleetEnableFlag: a device with "enable": false is skipped; omitted/true runs.
func TestFleetEnableFlag(t *testing.T) {
	off, on := false, true
	base := serveFleet(t,
		DeviceSpec{Driver: "oasisfoc", Index: 0},                // omitted -> enabled
		DeviceSpec{Driver: "focuscube", Index: 0, Enable: &off}, // disabled, skipped
		DeviceSpec{Driver: "focuslynx", Index: 0, Enable: &on},  // explicitly enabled
	)
	devs := configured(t, base)
	if len(devs) != 2 {
		t.Fatalf("got %d devices, want 2 (focuscube disabled): %+v", len(devs), devs)
	}
	for i, d := range devs {
		if d.DeviceType != "focuser" || d.DeviceNumber != i {
			t.Errorf("device %d = %s/%d, want focuser/%d", i, d.DeviceType, d.DeviceNumber, i)
		}
	}
}

func TestRegisterErrors(t *testing.T) {
	srv := alpacadev.New(alpacadev.Config{Discovery: alpacadev.DiscoveryConfig{Mode: alpacadev.DiscoveryOff}, ServerName: "t", Manufacturer: "t"})
	c := counters{}
	if _, err := registerDevice(srv, DeviceSpec{Driver: "tenmicron"}, 0, c); err == nil {
		t.Error("tenmicron without addr should error")
	}
	if _, err := registerDevice(srv, DeviceSpec{Driver: "nope"}, 0, c); err == nil {
		t.Error("unknown driver should error")
	}
	if _, err := registerDevice(srv, DeviceSpec{Driver: "asiccd"}, 0, c); err == nil {
		t.Error("asiccd (ZWO SDK) should error in the vendor-free fleet")
	}
}

func TestLoadConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fleet.json")
	const body = `{"port":12000,"discovery":"off","devices":[{"driver":"oasisfoc"},{"driver":"tenmicron","addr":"x:1"}]}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != 12000 || cfg.Discovery != "off" || len(cfg.Devices) != 2 {
		t.Fatalf("parsed config wrong: %+v", cfg)
	}

	// Unknown field is rejected (catches config typos).
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte(`{"prt":1,"devices":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(bad); err == nil {
		t.Error("unknown config field should be rejected")
	}
}
