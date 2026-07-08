package driver

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	alpacadev "github.com/mikefsq/goalpaca/server"
	lx200 "github.com/mikefsq/lx200"
	"github.com/mikefsq/lx200/tenmicron"
)

const txQ = "ClientID=1&ClientTransactionID=1"

// statusIdle is a :Ginfo# reply: RA 12h, Dec +45°, pier West, az 180, alt 30,
// Gstat 0 (tracking), slew 0.
const statusIdle = "12.000000,45.000000,W,180.00000,30.00000,2459580.5,0,0#"

// fakeTransport is a scripted in-memory lx200.Transport (the cross-module
// internal/lx200test fake isn't importable here). Each written command queues its
// mapped reply for the next reads.
type fakeTransport struct {
	mu      sync.Mutex
	replies map[string]string
	writes  []string
	rbuf    []byte
}

func (f *fakeTransport) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cmd := string(p)
	f.writes = append(f.writes, cmd)
	if r, ok := f.replies[cmd]; ok {
		f.rbuf = append(f.rbuf, r...)
	}
	return len(p), nil
}

func (f *fakeTransport) Read(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.rbuf) == 0 {
		return 0, nil
	}
	n := copy(p, f.rbuf)
	f.rbuf = f.rbuf[n:]
	return n, nil
}

func (f *fakeTransport) Close() error { return nil }

func (f *fakeTransport) wrote(cmd string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, w := range f.writes {
		if w == cmd {
			return true
		}
	}
	return false
}

type resp struct {
	Value        any
	ErrorNumber  int
	ErrorMessage string
}

// newStack wires a Telescope (over the fake transport) into a real Alpaca server
// behind httptest, returning the device API base URL and the fake. The mount is
// injected directly (in-package test), so manage()/Open are never started.
func newStack(t *testing.T, replies map[string]string) (string, *fakeTransport) {
	t.Helper()
	// Defaults so the snapshot-priming poll (pollOnce) resolves every command it
	// issues; the caller's map overrides any of these. Getters are snapshot-served, so
	// the snapshot must be primed the way manage() does on connect.
	merged := map[string]string{
		":Ginfo#": "0.000000,0.000000,E,0.00000,0.00000,2451545.0,1,0#", // neutral status
		":GaXa#":  "0.0#",                                               // primary axis angle (deg) — not at home
		":GaXb#":  "0.0#",                                               // secondary axis angle (deg)
		":Ggui#":  "15.0#",                                              // guide rate (arcsec/s)
		":GREF#":  "0",                                                  // refraction off — single status byte, no '#'
		":GUDT#":  "2026-07-07,12:00:00#",                               // mount UTC clock (ISO, ultra precision)
	}
	for k, v := range replies {
		merged[k] = v
	}
	f := &fakeTransport{replies: merged}
	tel := NewTelescope("test")
	tel.m = &tenmicron.Mount{Conn: lx200.New(f, time.Second)}
	_ = tel.pollOnce(tel.m, true) // prime the snapshot (as manage() does on connect)

	srv := alpacadev.New(alpacadev.Config{
		Discovery:    alpacadev.DiscoveryConfig{Mode: alpacadev.DiscoveryOff},
		ServerName:   "tenmicron-test",
		Manufacturer: "test",
	})
	if err := srv.Register(alpacadev.TelescopeType, 0, tel); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(http.HandlerFunc(srv.ServeHTTP))
	t.Cleanup(ts.Close)
	return ts.URL + "/api/v1/telescope/0/", f
}

func decode(t *testing.T, r *http.Response) resp {
	t.Helper()
	defer r.Body.Close()
	var out resp
	if err := json.NewDecoder(r.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

func get(t *testing.T, base, member string) resp {
	t.Helper()
	r, err := http.Get(base + member + "?" + txQ)
	if err != nil {
		t.Fatalf("GET %s: %v", member, err)
	}
	return decode(t, r)
}

func getQ(t *testing.T, base, member, extra string) resp {
	t.Helper()
	r, err := http.Get(base + member + "?" + extra + "&" + txQ)
	if err != nil {
		t.Fatalf("GET %s: %v", member, err)
	}
	return decode(t, r)
}

func put(t *testing.T, base, member, form string) resp {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPut, base+member, strings.NewReader(form+"&"+txQ))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT %s: %v", member, err)
	}
	return decode(t, r)
}

func TestCapabilitiesWired(t *testing.T) {
	base, _ := newStack(t, nil)
	for _, m := range []string{
		"canslewaltaz", "canslewaltazasync", "cansyncaltaz",
		"cansetguiderates", "cansetpark", "canfindhome",
	} {
		if r := get(t, base, m); r.ErrorNumber != 0 || r.Value != true {
			t.Errorf("%s = %v (err %d), want true", m, r.Value, r.ErrorNumber)
		}
	}
}

func TestDoesRefraction(t *testing.T) {
	base, f := newStack(t, map[string]string{":GREF#": "1", ":SREF0#": "1"}) // :GREF# = bare byte
	if r := get(t, base, "doesrefraction"); r.ErrorNumber != 0 || r.Value != true {
		t.Errorf("doesrefraction = %v (err %d), want true", r.Value, r.ErrorNumber)
	}
	if r := put(t, base, "doesrefraction", "DoesRefraction=false"); r.ErrorNumber != 0 {
		t.Errorf("set doesrefraction: err %d (%s)", r.ErrorNumber, r.ErrorMessage)
	}
	if !f.wrote(":SREF0#") {
		t.Errorf("SetDoesRefraction did not send :SREF0#; writes=%v", f.writes)
	}
}

func TestGuideRate(t *testing.T) {
	base, f := newStack(t, map[string]string{":Ggui#": "7.50#"})
	r := get(t, base, "guideraterightascension")
	if r.ErrorNumber != 0 {
		t.Fatalf("guiderate err %d", r.ErrorNumber)
	}
	if v, _ := r.Value.(float64); math.Abs(v-7.5/3600) > 1e-9 {
		t.Errorf("guideRate = %v deg/s, want %v", v, 7.5/3600)
	}
	if r := put(t, base, "guideraterightascension", "GuideRateRightAscension=0.0020833333"); r.ErrorNumber != 0 {
		t.Errorf("set guiderate: err %d (%s)", r.ErrorNumber, r.ErrorMessage)
	}
	if !f.wrote(":Rg07.500#") {
		t.Errorf("SetGuideRate did not send :Rg07.500#; writes=%v", f.writes)
	}

	// The library now sends the rate verbatim (its own clamp was removed), so the
	// Alpaca layer is the sole enforcer of the mount's [0.1×, 1.0×] sidereal band and
	// must report the clamped value it actually sent. Above 1.0× sidereal clamps down
	// to 15.041"/s (:Rg15.041#).
	base2, f2 := newStack(t, map[string]string{":Ggui#": "7.50#"})
	if r := put(t, base2, "guideraterightascension", "GuideRateRightAscension=0.006"); r.ErrorNumber != 0 { // 21.6"/s
		t.Errorf("set high guiderate: err %d (%s)", r.ErrorNumber, r.ErrorMessage)
	}
	if !f2.wrote(":Rg15.041#") {
		t.Errorf("above-band guide rate did not clamp to :Rg15.041#; writes=%v", f2.writes)
	}
	if v, _ := get(t, base2, "guideraterightascension").Value.(float64); math.Abs(v-15.041/3600) > 1e-9 {
		t.Errorf("clamped-high readback = %v deg/s, want %v", v, 15.041/3600)
	}

	// Below 0.1× sidereal clamps up to 1.504"/s (:Rg01.504#).
	base3, f3 := newStack(t, map[string]string{":Ggui#": "7.50#"})
	if r := put(t, base3, "guideraterightascension", "GuideRateRightAscension=0.0001"); r.ErrorNumber != 0 { // 0.36"/s
		t.Errorf("set low guiderate: err %d (%s)", r.ErrorNumber, r.ErrorMessage)
	}
	if !f3.wrote(":Rg01.504#") {
		t.Errorf("below-band guide rate did not clamp to :Rg01.504#; writes=%v", f3.writes)
	}
	if v, _ := get(t, base3, "guideraterightascension").Value.(float64); math.Abs(v-1.5041/3600) > 1e-9 {
		t.Errorf("clamped-low readback = %v deg/s, want %v", v, 1.5041/3600)
	}
}

func TestSlewToAltAzAsync(t *testing.T) {
	base, f := newStack(t, map[string]string{
		":Sa+45*30:00.0#": "1", ":Sz123*30:00.0#": "1", ":MA#": "0",
	})
	if r := put(t, base, "slewtoaltazasync", "Azimuth=123.5&Altitude=45.5"); r.ErrorNumber != 0 {
		t.Fatalf("slewtoaltazasync: err %d (%s)", r.ErrorNumber, r.ErrorMessage)
	}
	if !f.wrote(":MA#") {
		t.Errorf("slew did not issue :MA#; writes=%v", f.writes)
	}
}

func TestSyncToAltAz(t *testing.T) {
	base, _ := newStack(t, map[string]string{
		":Sa+45*30:00.0#": "1", ":Sz123*30:00.0#": "1", ":CM#": "AltAz#",
	})
	if r := put(t, base, "synctoaltaz", "Azimuth=123.5&Altitude=45.5"); r.ErrorNumber != 0 {
		t.Errorf("synctoaltaz: err %d (%s)", r.ErrorNumber, r.ErrorMessage)
	}
}

func TestDestinationSideOfPier(t *testing.T) {
	base, _ := newStack(t, map[string]string{
		":Sr12:00:00.00#": "1", ":Sd+45*00:00.0#": "1", ":GTsid#": "2", // 2 => West (bare digit, no '#')
	})
	r := getQ(t, base, "destinationsideofpier", "RightAscension=12.0&Declination=45.0")
	if r.ErrorNumber != 0 {
		t.Fatalf("destinationsideofpier: err %d (%s)", r.ErrorNumber, r.ErrorMessage)
	}
	if v, _ := r.Value.(float64); int(v) != int(alpacadev.PierWest) {
		t.Errorf("destinationsideofpier = %v, want West(%d)", r.Value, alpacadev.PierWest)
	}
}

func TestSetPark(t *testing.T) {
	base, f := newStack(t, map[string]string{":PyX#": "1"}) // bare status byte, no '#'; '1' = save accepted
	if r := put(t, base, "setpark", ""); r.ErrorNumber != 0 {
		t.Errorf("setpark: err %d (%s)", r.ErrorNumber, r.ErrorMessage)
	}
	if !f.wrote(":PyX#") {
		t.Errorf("setpark did not send :PyX#; writes=%v", f.writes)
	}
}

func TestIsPulseGuiding(t *testing.T) {
	base, _ := newStack(t, nil) // :Mg…# is blind (no reply needed)
	if r := put(t, base, "pulseguide", "Direction=0&Duration=600"); r.ErrorNumber != 0 {
		t.Fatalf("pulseguide: err %d (%s)", r.ErrorNumber, r.ErrorMessage)
	}
	if r := get(t, base, "ispulseguiding"); r.ErrorNumber != 0 || r.Value != true {
		t.Errorf("ispulseguiding = %v (err %d), want true within the pulse window", r.Value, r.ErrorNumber)
	}
}

func TestSetSideOfPier(t *testing.T) {
	// Currently East; requesting West must flip.
	base, f := newStack(t, map[string]string{":pS#": "East#", ":FLIP#": "1"})
	if get(t, base, "cansetpierside").Value != true {
		t.Errorf("cansetpierside = false, want true")
	}
	if r := put(t, base, "sideofpier", "SideOfPier=1"); r.ErrorNumber != 0 { // 1 = West
		t.Fatalf("setsideofpier(West): err %d (%s)", r.ErrorNumber, r.ErrorMessage)
	}
	if !f.wrote(":FLIP#") {
		t.Errorf("changing side did not issue :FLIP#; writes=%v", f.writes)
	}

	// Already East; requesting East must be a no-op (no flip).
	base2, f2 := newStack(t, map[string]string{":pS#": "East#"})
	if r := put(t, base2, "sideofpier", "SideOfPier=0"); r.ErrorNumber != 0 { // 0 = East
		t.Fatalf("setsideofpier(East): err %d (%s)", r.ErrorNumber, r.ErrorMessage)
	}
	if f2.wrote(":FLIP#") {
		t.Errorf("same-side request should not flip; writes=%v", f2.writes)
	}
}

func TestCustomRates(t *testing.T) {
	base, f := newStack(t, map[string]string{
		":RR+000.5000#": "1", // RA 1:1
		":RD+000.0665#": "1", // 1.0 arcsec/s ÷ 15.0410681 ≈ 0.0665
		":Sdat1#":       "1", // a non-zero Dec rate auto-enables dual-axis tracking
	})
	for _, m := range []string{"cansetrightascensionrate", "cansetdeclinationrate"} {
		if get(t, base, m).Value != true {
			t.Errorf("%s = false, want true", m)
		}
	}
	if r := put(t, base, "rightascensionrate", "RightAscensionRate=0.5"); r.ErrorNumber != 0 {
		t.Fatalf("set RA rate: err %d (%s)", r.ErrorNumber, r.ErrorMessage)
	}
	if !f.wrote(":RR+000.5000#") {
		t.Errorf("RA rate did not send :RR+000.5000#; writes=%v", f.writes)
	}
	if v, _ := get(t, base, "rightascensionrate").Value.(float64); v != 0.5 {
		t.Errorf("rightascensionrate readback = %v, want 0.5", v)
	}
	if r := put(t, base, "declinationrate", "DeclinationRate=1.0"); r.ErrorNumber != 0 {
		t.Fatalf("set Dec rate: err %d (%s)", r.ErrorNumber, r.ErrorMessage)
	}
	if !f.wrote(":RD+000.0665#") {
		t.Errorf("Dec rate did not send :RD+000.0665#; writes=%v", f.writes)
	}
	if !f.wrote(":Sdat1#") { // the coupling: a non-zero Dec rate enables dual-axis tracking
		t.Errorf("Dec rate did not enable dual-axis tracking (:Sdat1#); writes=%v", f.writes)
	}
	if v, _ := get(t, base, "declinationrate").Value.(float64); v != 1.0 {
		t.Errorf("declinationrate readback = %v, want 1.0", v)
	}
}

func TestOptics(t *testing.T) {
	f := &fakeTransport{}
	tel := NewTelescope("test")
	tel.m = &tenmicron.Mount{Conn: lx200.New(f, time.Second)}
	tel.SetOptics(0.2, 0, 1.0) // 200mm diameter, area auto, 1m focal length

	srv := alpacadev.New(alpacadev.Config{Discovery: alpacadev.DiscoveryConfig{Mode: alpacadev.DiscoveryOff}, ServerName: "t", Manufacturer: "t"})
	if err := srv.Register(alpacadev.TelescopeType, 0, tel); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(http.HandlerFunc(srv.ServeHTTP))
	t.Cleanup(ts.Close)
	base := ts.URL + "/api/v1/telescope/0/"

	if v, _ := get(t, base, "aperturediameter").Value.(float64); math.Abs(v-0.2) > 1e-9 {
		t.Errorf("aperturediameter = %v, want 0.2", v)
	}
	if v, _ := get(t, base, "focallength").Value.(float64); math.Abs(v-1.0) > 1e-9 {
		t.Errorf("focallength = %v, want 1.0", v)
	}
	wantArea := math.Pi * 0.1 * 0.1
	if v, _ := get(t, base, "aperturearea").Value.(float64); math.Abs(v-wantArea) > 1e-9 {
		t.Errorf("aperturearea = %v, want %v (computed from diameter)", v, wantArea)
	}
}

func TestSetEnvironmentAction(t *testing.T) {
	base, f := newStack(t, map[string]string{
		":SRPRS1013.2#":             "1",
		":SRTMP+020.5#":             "1",
		":St+45*30:00#":             "1",
		":Sg+122*30:00#":            "1", // East-positive -122.5 → 10Micron +122.5
		":Sev+0100.0#":              "1",
		":SUDT2026-06-02,15:04:05#": "1",
	})
	if sa, ok := get(t, base, "supportedactions").Value.([]any); !ok || len(sa) == 0 {
		t.Errorf("supportedactions = %v, want non-empty", get(t, base, "supportedactions").Value)
	}
	body := `{"pressure_hpa":1013.2,"temperature_c":20.5,"latitude":45.5,"longitude":-122.5,"elevation_m":100,"time":"2026-06-02T15:04:05Z"}`
	r := put(t, base, "action", "Action=setenvironment&Parameters="+url.QueryEscape(body))
	if r.ErrorNumber != 0 {
		t.Fatalf("setenvironment: err %d (%s)", r.ErrorNumber, r.ErrorMessage)
	}
	for _, want := range []string{":SRPRS1013.2#", ":SRTMP+020.5#", ":St+45*30:00#", ":Sg+122*30:00#", ":Sev+0100.0#", ":SUDT2026-06-02,15:04:05#"} {
		if !f.wrote(want) {
			t.Errorf("setenvironment did not send %q; writes=%v", want, f.writes)
		}
	}
}

func TestSetEnvironmentPartial(t *testing.T) {
	// Only pressure present → only :SRPRS sent; no site/time commands.
	base, f := newStack(t, map[string]string{":SRPRS1000.0#": "1"})
	r := put(t, base, "action", "Action=setenvironment&Parameters="+url.QueryEscape(`{"pressure_hpa":1000.0}`))
	if r.ErrorNumber != 0 {
		t.Fatalf("setenvironment(partial): err %d (%s)", r.ErrorNumber, r.ErrorMessage)
	}
	if !f.wrote(":SRPRS1000.0#") {
		t.Errorf("partial: did not send :SRPRS1000.0#; writes=%v", f.writes)
	}
	for _, unwanted := range f.writes {
		if strings.HasPrefix(unwanted, ":St") || strings.HasPrefix(unwanted, ":Sg") || strings.HasPrefix(unwanted, ":SUDT") {
			t.Errorf("partial update sent an unrequested command %q", unwanted)
		}
	}
}

func TestDualAxisTrackingAction(t *testing.T) {
	base, f := newStack(t, map[string]string{
		":Gdat#":  "1", // read-back: single bare status byte, no '#' (AckByte)
		":Sdat1#": "1",
		":Sdat0#": "1",
	})
	sa, _ := get(t, base, "supportedactions").Value.([]any)
	found := false
	for _, a := range sa {
		if s, _ := a.(string); s == "dualaxistracking" {
			found = true
		}
	}
	if !found {
		t.Errorf("dualaxistracking not in supportedactions: %v", sa)
	}
	// read (empty params)
	if r := put(t, base, "action", "Action=dualaxistracking&Parameters="); r.ErrorNumber != 0 || r.Value != "true" {
		t.Errorf("read dualaxistracking = %v (err %d), want \"true\"", r.Value, r.ErrorNumber)
	}
	// set false / true
	if r := put(t, base, "action", "Action=dualaxistracking&Parameters=false"); r.ErrorNumber != 0 {
		t.Errorf("set dualaxistracking false: err %d (%s)", r.ErrorNumber, r.ErrorMessage)
	}
	if !f.wrote(":Sdat0#") {
		t.Errorf("did not send :Sdat0#; writes=%v", f.writes)
	}
	if r := put(t, base, "action", "Action=dualaxistracking&Parameters=true"); r.ErrorNumber != 0 {
		t.Errorf("set dualaxistracking true: err %d (%s)", r.ErrorNumber, r.ErrorMessage)
	}
	if !f.wrote(":Sdat1#") {
		t.Errorf("did not send :Sdat1#; writes=%v", f.writes)
	}
	// bad param → InvalidValue
	if r := put(t, base, "action", "Action=dualaxistracking&Parameters=maybe"); r.ErrorNumber != 0x401 {
		t.Errorf("bad param: err %d, want 0x401", r.ErrorNumber)
	}
}

func TestGranularRefractionActions(t *testing.T) {
	base, f := newStack(t, map[string]string{":SRPRS0980.0#": "1", ":SRTMP-005.0#": "1"})
	if r := put(t, base, "action", "Action=setrefractionpressure&Parameters=980.0"); r.ErrorNumber != 0 {
		t.Errorf("setrefractionpressure: err %d (%s)", r.ErrorNumber, r.ErrorMessage)
	}
	if !f.wrote(":SRPRS0980.0#") {
		t.Errorf("did not send :SRPRS0980.0#; writes=%v", f.writes)
	}
	if r := put(t, base, "action", "Action=setrefractiontemperature&Parameters=-5.0"); r.ErrorNumber != 0 {
		t.Errorf("setrefractiontemperature: err %d (%s)", r.ErrorNumber, r.ErrorMessage)
	}
	if !f.wrote(":SRTMP-005.0#") {
		t.Errorf("did not send :SRTMP-005.0#; writes=%v", f.writes)
	}
	// bad params → InvalidValue
	if r := put(t, base, "action", "Action=setrefractionpressure&Parameters=abc"); r.ErrorNumber != 0x401 {
		t.Errorf("bad pressure: err %d, want 0x401", r.ErrorNumber)
	}
}

func TestCoreReadsThroughHTTP(t *testing.T) {
	base, _ := newStack(t, map[string]string{":Ginfo#": statusIdle})
	if v, _ := get(t, base, "rightascension").Value.(float64); math.Abs(v-12) > 1e-6 {
		t.Errorf("rightascension = %v, want 12", v)
	}
	if v, _ := get(t, base, "declination").Value.(float64); math.Abs(v-45) > 1e-6 {
		t.Errorf("declination = %v, want 45", v)
	}
	if get(t, base, "tracking").Value != true {
		t.Errorf("tracking = false, want true")
	}
	if get(t, base, "slewing").Value != false {
		t.Errorf("slewing = true, want false")
	}
	if v, _ := get(t, base, "sideofpier").Value.(float64); int(v) != int(alpacadev.PierWest) {
		t.Errorf("sideofpier = %v, want West", v)
	}
}

func TestActionReadWrite(t *testing.T) {
	// maxslewrate: empty params reads (:GMsb#), a value writes (:Sw8#) and echoes.
	base, f := newStack(t, map[string]string{":GMsb#": "8.0#", ":Sw8#": "1"})
	if r := put(t, base, "action", "Action=maxslewrate&Parameters="); r.ErrorNumber != 0 || r.Value != "8.0" {
		t.Errorf("read maxslewrate = %v (err %d), want \"8.0\"", r.Value, r.ErrorNumber)
	}
	if r := put(t, base, "action", "Action=maxslewrate&Parameters=8"); r.ErrorNumber != 0 || r.Value != "8" {
		t.Fatalf("set maxslewrate = %v (err %d %s)", r.Value, r.ErrorNumber, r.ErrorMessage)
	}
	if !f.wrote(":Sw8#") {
		t.Errorf("maxslewrate did not send :Sw8#; writes=%v", f.writes)
	}
}

func TestActionReadOnlyRejectsValue(t *testing.T) {
	// minslewrate is read-only: empty reads (:GMsa#), a value is InvalidValue (0x401).
	base, _ := newStack(t, map[string]string{":GMsa#": "0.5#"})
	if r := put(t, base, "action", "Action=minslewrate&Parameters="); r.ErrorNumber != 0 || r.Value != "0.5" {
		t.Errorf("read minslewrate = %v (err %d), want \"0.5\"", r.Value, r.ErrorNumber)
	}
	if r := put(t, base, "action", "Action=minslewrate&Parameters=3"); r.ErrorNumber != 0x401 {
		t.Errorf("minslewrate with value: err %d, want 0x401 (read-only)", r.ErrorNumber)
	}
}

func TestActionWriteOnlyNeedsValue(t *testing.T) {
	// rabacklash has no mount getter: empty params is InvalidValue (0x401).
	base, _ := newStack(t, nil)
	if r := put(t, base, "action", "Action=rabacklash&Parameters="); r.ErrorNumber != 0x401 {
		t.Errorf("rabacklash empty: err %d, want 0x401 (needs value)", r.ErrorNumber)
	}
}

func TestActionMeridianSlewLimit(t *testing.T) {
	base, f := newStack(t, map[string]string{":Glms#": "10#", ":Slms15#": "1"})
	if r := put(t, base, "action", "Action=meridianslewlimit&Parameters="); r.ErrorNumber != 0 || r.Value != "10" {
		t.Errorf("read meridianslewlimit = %v (err %d), want \"10\"", r.Value, r.ErrorNumber)
	}
	if r := put(t, base, "action", "Action=meridianslewlimit&Parameters=15"); r.ErrorNumber != 0 {
		t.Fatalf("set meridianslewlimit: err %d (%s)", r.ErrorNumber, r.ErrorMessage)
	}
	if !f.wrote(":Slms15#") {
		t.Errorf("did not send :Slms15#; writes=%v", f.writes)
	}
}

func TestSupportedActionsExpanded(t *testing.T) {
	base, _ := newStack(t, nil)
	sa, _ := get(t, base, "supportedactions").Value.([]any)
	got := map[string]bool{}
	for _, a := range sa {
		if s, ok := a.(string); ok {
			got[s] = true
		}
	}
	for _, want := range []string{
		"maxslewrate", "meridianslewlimit", "rabacklash", "weather", "firmware",
		"savemodel", "loadmodel", "alignmentinfo", "loadtle", "trajectoryoffset",
		"parkinplace", "utcoffset", "gpsstatus",
		"setrefractionpressure", // legacy alias preserved
	} {
		if !got[want] {
			t.Errorf("supportedactions missing %q", want)
		}
	}
}

func TestSetOpticsReadOnEmpty(t *testing.T) {
	base, _ := newStack(t, nil)
	if r := put(t, base, "action", "Action=setoptics&Parameters="+url.QueryEscape(`{"aperture":130,"focal_length":1000}`)); r.ErrorNumber != 0 {
		t.Fatalf("setoptics write: err %d (%s)", r.ErrorNumber, r.ErrorMessage)
	}
	r := put(t, base, "action", "Action=setoptics&Parameters=")
	if r.ErrorNumber != 0 {
		t.Fatalf("setoptics read: err %d (%s)", r.ErrorNumber, r.ErrorMessage)
	}
	var got map[string]float64
	if err := json.Unmarshal([]byte(r.Value.(string)), &got); err != nil {
		t.Fatalf("parse optics %q: %v", r.Value, err)
	}
	if got["aperture"] != 130 || got["focal_length"] != 1000 {
		t.Errorf("read optics = %v, want aperture 130 focal_length 1000", got)
	}
}

func TestSetEnvironmentReadOnEmpty(t *testing.T) {
	base, _ := newStack(t, map[string]string{":GRPRS#": "1013.0#", ":GRTMP#": "15.0#"})
	r := put(t, base, "action", "Action=setenvironment&Parameters=")
	if r.ErrorNumber != 0 {
		t.Fatalf("setenvironment read: err %d (%s)", r.ErrorNumber, r.ErrorMessage)
	}
	var env map[string]any
	if err := json.Unmarshal([]byte(r.Value.(string)), &env); err != nil {
		t.Fatalf("parse env %q: %v", r.Value, err)
	}
	if env["pressure_hpa"] != 1013.0 || env["temperature_c"] != 15.0 {
		t.Errorf("env read = %v, want pressure 1013 temp 15", env)
	}
	// The template must be COMPLETE: every accepted field present, so a client can edit
	// and re-send it.
	for _, k := range []string{"pressure_hpa", "temperature_c", "latitude", "longitude", "elevation_m", "time"} {
		if _, ok := env[k]; !ok {
			t.Errorf("env template missing field %q: %v", k, env)
		}
	}
}

func TestRefractionAliasReadsOnEmpty(t *testing.T) {
	// The legacy setrefraction* names now read on empty (aliased to the RW actions).
	base, f := newStack(t, map[string]string{":GRPRS#": "980.5#", ":GRTMP#": "-3.0#"})
	if r := put(t, base, "action", "Action=setrefractionpressure&Parameters="); r.ErrorNumber != 0 || r.Value != "980.5" {
		t.Errorf("setrefractionpressure read = %v (err %d), want \"980.5\"", r.Value, r.ErrorNumber)
	}
	if r := put(t, base, "action", "Action=setrefractiontemperature&Parameters="); r.ErrorNumber != 0 || r.Value != "-3.0" {
		t.Errorf("setrefractiontemperature read = %v (err %d), want \"-3.0\"", r.Value, r.ErrorNumber)
	}
	if !f.wrote(":GRPRS#") {
		t.Errorf("read did not query :GRPRS#; writes=%v", f.writes)
	}
}

func TestParkInPlace(t *testing.T) {
	// The Park button parks where the mount is (:PiP#), not at a saved position.
	base, f := newStack(t, map[string]string{":PiP#": "1#"})
	if r := put(t, base, "park", ""); r.ErrorNumber != 0 {
		t.Fatalf("park: err %d (%s)", r.ErrorNumber, r.ErrorMessage)
	}
	if !f.wrote(":PiP#") {
		t.Errorf("park did not send :PiP# (park-in-place); writes=%v", f.writes)
	}
}

func TestFindHomeSlewsToRAAxis(t *testing.T) {
	// The Home button slews to the RA-axis reference (primary 90°, secondary 0°) via a
	// direct axis-angle slew and stops — no park.
	base, f := newStack(t, map[string]string{
		":SaXa+090.0000#": "1", ":SaXb+000.0000#": "1", ":MaX#": "0",
	})
	if r := put(t, base, "findhome", ""); r.ErrorNumber != 0 {
		t.Fatalf("findhome: err %d (%s)", r.ErrorNumber, r.ErrorMessage)
	}
	for _, w := range []string{":SaXa+090.0000#", ":SaXb+000.0000#", ":MaX#"} {
		if !f.wrote(w) {
			t.Errorf("findhome did not send %q; writes=%v", w, f.writes)
		}
	}
}

func TestAtHomeFromAxisAngles(t *testing.T) {
	// AtHome is the axis-angle test: primary (RA) ≈ 90°, secondary (Dec) ≈ 0°.
	base, _ := newStack(t, map[string]string{":GaXa#": "90.0#", ":GaXb#": "0.0#"})
	if v := get(t, base, "athome").Value; v != true {
		t.Errorf("athome = %v, want true at the RA-axis home position", v)
	}
	// Away from the reference → false.
	base2, _ := newStack(t, map[string]string{":GaXa#": "45.0#", ":GaXb#": "10.0#"})
	if v := get(t, base2, "athome").Value; v != false {
		t.Errorf("athome = %v, want false away from home", v)
	}
}

func TestUTCDateFromMount(t *testing.T) {
	// The mount clock (:GUDT#) — set far from the host clock — must drive UTCDate,
	// proving it reflects the mount (via the poller's skew) and not time.Now().
	base, _ := newStack(t, map[string]string{":GUDT#": "2020-01-02,03:04:05#"})
	r := get(t, base, "utcdate")
	if r.ErrorNumber != 0 {
		t.Fatalf("utcdate err %d (%s)", r.ErrorNumber, r.ErrorMessage)
	}
	got, err := time.Parse(time.RFC3339, r.Value.(string))
	if err != nil {
		t.Fatalf("parse utcdate %q: %v", r.Value, err)
	}
	want := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	if d := got.Sub(want); d < -5*time.Second || d > 5*time.Second {
		t.Errorf("utcdate = %v, want ≈ mount clock %v (skew-driven, not host clock)", got, want)
	}
}

func TestAxisRatesFromMount(t *testing.T) {
	tel := NewTelescope("test")
	// Before the mount reports, AxisRates advertises the fallback ceiling.
	if r := tel.AxisRates(alpacadev.AxisPrimary); len(r) != 1 || r[0].Maximum != defaultMaxAxisRate {
		t.Fatalf("default AxisRates = %v, want max %v", r, defaultMaxAxisRate)
	}
	// primeStatics stores the mount's MaxSlewRate (:GMsb#); AxisRates then advertises it,
	// so a client can't request a rate the mount would clamp.
	tel.maxSlewRate = 3.0
	for _, ax := range []alpacadev.TelescopeAxis{alpacadev.AxisPrimary, alpacadev.AxisSecondary} {
		if r := tel.AxisRates(ax); len(r) != 1 || r[0].Minimum != 0 || r[0].Maximum != 3.0 {
			t.Errorf("AxisRates(%v) = %v, want {0, 3.0} (mount MaxSlewRate)", ax, r)
		}
	}
	if r := tel.AxisRates(alpacadev.TelescopeAxis(9)); len(r) != 0 {
		t.Errorf("AxisRates(invalid axis) = %v, want empty", r)
	}
}

func TestSlewSettleTime(t *testing.T) {
	// The value must reach the mount (:Sstm, deciseconds field), not just be cached, so
	// the mount holds "slewing" through the settle window.
	base, f := newStack(t, map[string]string{":Sstm00005.000#": "1"})
	if r := put(t, base, "slewsettletime", "SlewSettleTime=5"); r.ErrorNumber != 0 {
		t.Fatalf("set slewsettletime: err %d (%s)", r.ErrorNumber, r.ErrorMessage)
	}
	if !f.wrote(":Sstm00005.000#") {
		t.Errorf("SetSlewSettleTime did not send :Sstm00005.000#; writes=%v", f.writes)
	}
	if v, _ := get(t, base, "slewsettletime").Value.(float64); int(v) != 5 {
		t.Errorf("slewsettletime readback = %v, want 5", v)
	}
}

func TestMoveAxisExactRate(t *testing.T) {
	// MoveAxis must command the exact rate AxisRates advertises, not a snapped preset.
	// Uses the secondary (Dec/Alt) axis, whose move skips the primary's speed-correction
	// handshake — both writes are blind (:RE rate then :Mn move).
	base, f := newStack(t, nil)
	if r := put(t, base, "moveaxis", "Axis=1&Rate=1.5"); r.ErrorNumber != 0 {
		t.Fatalf("moveaxis: err %d (%s)", r.ErrorNumber, r.ErrorMessage)
	}
	if !f.wrote(":RE01.500000#") {
		t.Errorf("MoveAxis did not send exact rate :RE01.500000#; writes=%v", f.writes)
	}
	if !f.wrote(":Mn#") {
		t.Errorf("MoveAxis did not issue the north move :Mn#; writes=%v", f.writes)
	}
	// Rate 0 stops the axis rather than issuing a move.
	base2, f2 := newStack(t, nil)
	if r := put(t, base2, "moveaxis", "Axis=1&Rate=0"); r.ErrorNumber != 0 {
		t.Fatalf("moveaxis stop: err %d (%s)", r.ErrorNumber, r.ErrorMessage)
	}
	for _, w := range f2.writes {
		if strings.HasPrefix(w, ":RE") || w == ":Mn#" || w == ":Ms#" {
			t.Errorf("rate 0 should stop, not move; sent %q", w)
		}
	}
}
