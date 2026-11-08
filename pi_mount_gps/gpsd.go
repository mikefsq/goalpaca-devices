package driver

import (
	"bufio"
	"context"
	"encoding/json"
	"log"
	"net"
	"strconv"
	"sync"
	"time"

	pimountgps "github.com/mikefsq/x1500_gps_12v/driver"
)

// defaultGpsdAddr is where gpsd listens on a standard install.
const defaultGpsdAddr = "localhost:2947"

// gpsdFresh is how recent a gpsd report must be to be used; gpsd reports once
// per receiver epoch, so a few missed seconds means it has lost the receiver.
const gpsdFresh = 5 * time.Second

// gpsdClient follows gpsd's JSON stream and keeps the latest fix. gpsd is the
// system's standard GPS source: when it has a fix the driver uses it, falling
// back to the board's own I2C receiver otherwise.
type gpsdClient struct {
	addr string
	now  func() time.Time

	mu    sync.Mutex
	fix   pimountgps.Fix
	mode  int       // TPV mode: 0/1 no fix, 2 2D, 3 3D
	tpvAt time.Time // when the last TPV arrived; zero = never
	up    bool      // connected, for logging transitions
}

func newGpsdClient(addr string) *gpsdClient {
	return &gpsdClient{addr: addr, now: time.Now}
}

// run keeps a gpsd connection open until ctx ends, reconnecting after errors.
func (g *gpsdClient) run(ctx context.Context) {
	for ctx.Err() == nil {
		err := g.session(ctx)
		g.mu.Lock()
		wasUp := g.up
		g.up = false
		g.mu.Unlock()
		if wasUp && ctx.Err() == nil {
			log.Printf("pi_mount_gps: gpsd %s lost (%v); GPS fallback will be used if no fresh fix remains", g.addr, err)
		}
		sleepCtx(ctx, 10*time.Second)
	}
}

// session runs one connection: subscribe, then apply reports until an error.
func (g *gpsdClient) session(ctx context.Context) error {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", g.addr)
	if err != nil {
		return err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()

	if _, err := conn.Write([]byte(`?WATCH={"enable":true,"json":true};` + "\n")); err != nil {
		return err
	}
	g.mu.Lock()
	g.up = true
	g.mu.Unlock()
	log.Printf("pi_mount_gps: gpsd %s connected", g.addr)

	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 64*1024), 1024*1024) // SKY reports list every satellite
	for {
		// gpsd reports every second while it has a receiver; silence means trouble.
		conn.SetReadDeadline(time.Now().Add(30 * time.Second))
		if !sc.Scan() {
			if err := sc.Err(); err != nil {
				return err
			}
			return net.ErrClosed
		}
		g.apply(sc.Bytes())
	}
}

// gpsdReport holds the fields of TPV and SKY reports the driver uses.
type gpsdReport struct {
	Class  string   `json:"class"`
	Mode   int      `json:"mode"`
	Status int      `json:"status"`
	Time   string   `json:"time"`
	Lat    *float64 `json:"lat"`
	Lon    *float64 `json:"lon"`
	AltMSL *float64 `json:"altMSL"`
	Alt    *float64 `json:"alt"` // pre-3.20 gpsd's name for altMSL
	PDOP   *float64 `json:"pdop"`
	HDOP   *float64 `json:"hdop"`
	VDOP   *float64 `json:"vdop"`
	USat   *int     `json:"uSat"`
}

// apply folds one gpsd JSON line into the fix. Other classes are ignored.
func (g *gpsdClient) apply(line []byte) {
	var r gpsdReport
	if json.Unmarshal(line, &r) != nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	f := &g.fix
	switch r.Class {
	case "TPV":
		g.mode = r.Mode
		g.tpvAt = g.now()
		f.Valid = r.Mode >= 2
		f.FixType = strconv.Itoa(max(r.Mode, 1)) // GSA digits: 1 none, 2 2D, 3 3D
		f.Quality = gpsdQuality(r.Mode, r.Status)
		if t, err := time.Parse(time.RFC3339Nano, r.Time); err == nil {
			f.Time = t
		}
		if r.Lat != nil && r.Lon != nil {
			f.Lat, f.Lon = *r.Lat, *r.Lon
		}
		if r.AltMSL != nil {
			f.AltM = *r.AltMSL
		} else if r.Alt != nil {
			f.AltM = *r.Alt
		}
	case "SKY":
		// Some SKY reports carry only the dilutions, others only satellites.
		if r.PDOP != nil {
			f.PDOP = *r.PDOP
		}
		if r.HDOP != nil {
			f.HDOP = *r.HDOP
		}
		if r.VDOP != nil {
			f.VDOP = *r.VDOP
		}
		if r.USat != nil {
			f.Satellites = *r.USat
		}
	}
}

// gpsdQuality maps a TPV mode and status to the GGA fix-quality digit the
// I2C receiver reports, so both sources read alike.
func gpsdQuality(mode, status int) int {
	if mode < 2 {
		return 0
	}
	switch status {
	case 2:
		return 2 // DGPS
	case 3:
		return 4 // RTK fixed
	case 4:
		return 5 // RTK float
	case 5, 6:
		return 6 // dead reckoning
	}
	return 1
}

// current returns gpsd's fix when it is recent and has a position.
func (g *gpsdClient) current() (pimountgps.Fix, time.Duration, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.tpvAt.IsZero() || g.mode < 2 {
		return pimountgps.Fix{}, 0, false
	}
	age := g.now().Sub(g.tpvAt)
	if age > gpsdFresh {
		return pimountgps.Fix{}, 0, false
	}
	return g.fix, age, true
}
