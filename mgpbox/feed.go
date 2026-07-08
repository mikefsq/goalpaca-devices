package driver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/mikefsq/astromi.ch/mgpbox"
)

// feedInterval is how often the environment snapshot is pushed to the mount. The mount
// driver diffs the payload, so pushing the full snapshot each cycle is cheap on the wire
// and only real changes reach the mount.
const feedInterval = 30 * time.Second

// feedClient is the shared HTTP client for pushes; a short timeout so a slow/unreachable
// mount can't stall the feed loop.
var feedClient = &http.Client{Timeout: 10 * time.Second}

// envPayload is the JSON pushed to the tenmicron setenvironment Action. Field names match
// that action's schema; humidity/dewpoint are extra fields it currently ignores but are
// included so the box's full weather set is available to it. Every field is a pointer so
// only the ones the box actually has are sent.
type envPayload struct {
	PressureHPa  *float64 `json:"pressure_hpa,omitempty"`
	TemperatureC *float64 `json:"temperature_c,omitempty"`
	HumidityPct  *float64 `json:"humidity_pct,omitempty"`
	DewpointC    *float64 `json:"dewpoint_c,omitempty"`
	Latitude     *float64 `json:"latitude,omitempty"`
	Longitude    *float64 `json:"longitude,omitempty"`
	ElevationM   *float64 `json:"elevation_m,omitempty"`
	Time         *string  `json:"time,omitempty"`
}

// feedLoop periodically pushes the environment snapshot to the configured mount. It
// no-ops while no mount is configured or no box is attached, and exits on ctx cancel.
func (m *MGPBox) feedLoop(ctx context.Context) {
	t := time.NewTicker(feedInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := m.pushEnvironment(ctx); err != nil {
				log.Printf("mgpbox: environment feed: %v", err)
			}
		}
	}
}

// pushEnvironment builds the current snapshot and posts it to the mount's setenvironment
// Action, returning the mount's JSON reply. It returns ("", nil) when the feed is off,
// no box is attached, or there is nothing to send yet.
func (m *MGPBox) pushEnvironment(ctx context.Context) (string, error) {
	m.mu.Lock()
	dev, addr, device := m.dev, m.mountAddr, m.mountDevice
	m.mu.Unlock()
	if addr == "" || dev == nil {
		return "", nil
	}
	payload := buildEnv(dev)
	if payload == nil {
		return "", nil // no valid data yet
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return m.postAction(ctx, addr, device, "setenvironment", string(body))
}

// buildEnv assembles the environment payload from the box's latest snapshot: weather when
// a meteo sample exists, and site/time only when the GPS has a real fix (so an unlocked
// receiver never pushes a 0,0 position or a bogus clock).
func buildEnv(dev *mgpbox.MGPBox) *envPayload {
	var p envPayload
	any := false
	if me, ok := dev.Meteo(); ok {
		p.PressureHPa = ptr(me.Pressure)
		p.TemperatureC = ptr(me.Temperature)
		p.HumidityPct = ptr(me.Humidity)
		p.DewpointC = ptr(me.Dewpoint)
		any = true
	}
	// Site + time only from a real position fix, so an unlocked receiver never pushes a
	// 0,0 position or an unsynced clock to the mount.
	if fx, ok := dev.Fix(); ok && fx.HasFix {
		p.Latitude = ptr(fx.Latitude)
		p.Longitude = ptr(fx.Longitude)
		p.ElevationM = ptr(fx.Altitude)
		if !fx.Time.IsZero() {
			ts := fx.Time.UTC().Format(time.RFC3339)
			p.Time = &ts
		}
		any = true
	}
	if !any {
		return nil
	}
	return &p
}

// postAction issues an Alpaca PUT .../telescope/<device>/action against the mount server
// and returns the reply's Value, or an error carrying the Alpaca ErrorMessage.
func (m *MGPBox) postAction(ctx context.Context, addr string, device int, action, params string) (string, error) {
	endpoint := fmt.Sprintf("http://%s/api/v1/telescope/%d/action", addr, device)
	form := url.Values{}
	form.Set("Action", action)
	form.Set("Parameters", params)
	form.Set("ClientID", "1")
	form.Set("ClientTransactionID", fmt.Sprint(atomic.AddUint32(&m.feedTxn, 1)))

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := feedClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("mount %s returned HTTP %d", addr, resp.StatusCode)
	}
	var r struct {
		Value        string `json:"Value"`
		ErrorNumber  int    `json:"ErrorNumber"`
		ErrorMessage string `json:"ErrorMessage"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return "", fmt.Errorf("mount %s: bad reply: %w", addr, err)
	}
	if r.ErrorNumber != 0 {
		return "", fmt.Errorf("mount %s setenvironment: %s (0x%X)", addr, r.ErrorMessage, r.ErrorNumber)
	}
	return r.Value, nil
}

// ptr returns a pointer to v, for building the optional-field envPayload.
func ptr(v float64) *float64 { return &v }
