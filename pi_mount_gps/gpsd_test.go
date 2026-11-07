package driver

import (
	"bufio"
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

const (
	tpv = `{"class":"TPV","device":"/dev/ttyAMA0","status":2,"mode":3,"time":"2026-10-01T20:25:39.000Z",` +
		`"lat":37.959515,"lon":-122.558473,"altHAE":-1.39,"altMSL":28.234,"alt":28.234}`
	sky = `{"class":"SKY","device":"/dev/ttyAMA0","hdop":0.62,"pdop":1.23,"vdop":1.07,"nSat":42,"uSat":21}`
)

func TestGpsdApply(t *testing.T) {
	g := newGpsdClient("")
	g.apply([]byte(`{"class":"VERSION","release":"3.25"}`))
	g.apply([]byte(tpv))
	g.apply([]byte(sky))
	g.apply([]byte(`{"class":"SKY","satellites":[]}`)) // satellites-only report keeps the DOPs
	fx, _, ok := g.current()
	if !ok {
		t.Fatal("no current fix")
	}
	want := time.Date(2026, 10, 1, 20, 25, 39, 0, time.UTC)
	if !fx.Time.Equal(want) || fx.Lat != 37.959515 || fx.Lon != -122.558473 || fx.AltM != 28.234 {
		t.Fatalf("position = %+v", fx)
	}
	if fx.Quality != 2 || fx.FixType != "3" || fx.Satellites != 21 || fx.PDOP != 1.23 || fx.HDOP != 0.62 || fx.VDOP != 1.07 {
		t.Fatalf("quality = %+v", fx)
	}
}

func TestGpsdNoFixOrStale(t *testing.T) {
	g := newGpsdClient("")
	clock := testNow
	g.now = func() time.Time { return clock }
	g.apply([]byte(`{"class":"TPV","mode":1}`))
	if _, _, ok := g.current(); ok {
		t.Fatal("mode 1 counted as a fix")
	}
	g.apply([]byte(tpv))
	if _, _, ok := g.current(); !ok {
		t.Fatal("fresh 3D fix rejected")
	}
	clock = clock.Add(gpsdFresh + time.Second)
	if _, _, ok := g.current(); ok {
		t.Fatal("stale gpsd fix used")
	}
}

func TestGpsdQuality(t *testing.T) {
	for _, c := range []struct{ mode, status, want int }{
		{1, 0, 0}, {3, 0, 1}, {3, 1, 1}, {3, 2, 2}, {3, 3, 4}, {3, 4, 5}, {2, 5, 6},
	} {
		if got := gpsdQuality(c.mode, c.status); got != c.want {
			t.Errorf("gpsdQuality(%d,%d) = %d, want %d", c.mode, c.status, got, c.want)
		}
	}
}

func TestCurrentFixPrefersGpsd(t *testing.T) {
	b := fixedBoard()
	w := &Driver{gpsd: newGpsdClient(""), gpsBoard: b}
	if g := w.currentFix(); g.src != gpsSrcI2C || g.fix.Lat != 37.95 {
		t.Fatalf("without gpsd fix: %+v", g)
	}
	w.gpsd.apply([]byte(tpv))
	if g := w.currentFix(); g.src != gpsSrcGpsd || !g.valid || g.fix.Lat != 37.959515 {
		t.Fatalf("with gpsd fix: %+v", g)
	}
	b.fixStale = true
	w.gpsd = nil
	if g := w.currentFix(); g.src != gpsSrcNone || g.valid || g.fix.Lat != 37.95 {
		t.Fatalf("neither current: %+v", g)
	}
}

func TestBuildEnv2DOmitsElevation(t *testing.T) {
	g := newGpsdClient("")
	g.apply([]byte(strings.Replace(tpv, `"mode":3`, `"mode":2`, 1)))
	fx, age, ok := g.current()
	p, _ := buildEnv(fixedBoard(), gpsFix{fix: fx, age: age, valid: ok, src: gpsSrcGpsd}, testNow, false)
	if p.Latitude == nil || p.ElevationM != nil {
		t.Fatalf("2D fix: %+v", p)
	}
}

// TestGpsdSession runs the client against a fake gpsd.
func TestGpsdSession(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	watched := make(chan string, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		c.Write([]byte(`{"class":"VERSION","release":"3.25"}` + "\n"))
		line, _ := bufio.NewReader(c).ReadString('\n')
		watched <- line
		c.Write([]byte(tpv + "\n" + sky + "\n"))
		time.Sleep(time.Second)
	}()

	g := newGpsdClient(ln.Addr().String())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { g.run(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	if w := <-watched; !strings.HasPrefix(w, `?WATCH={"enable":true,"json":true}`) {
		t.Fatalf("watch = %q", w)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		if fx, _, ok := g.current(); ok && fx.Satellites == 21 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no fix from fake gpsd")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
