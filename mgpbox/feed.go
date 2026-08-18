package driver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mikefsq/astromi.ch/mgpbox"
)

// feedInterval is how often the environment snapshot is pushed to each target. Every
// consumer diffs the payload, so pushing the full snapshot each cycle is cheap on the
// wire and only real changes reach the device.
const feedInterval = 30 * time.Second

// A target that fails is retried on an exponential backoff rather than on every cycle:
// a mount that is switched off would otherwise cost a 10-second timeout every 30 seconds
// all night, and fill the log with the same error 120 times an hour. The delay doubles
// per consecutive failure from one cycle up to feedBackoffMax, and resets the moment a
// push succeeds.
const (
	feedBackoffBase = feedInterval
	feedBackoffMax  = 15 * time.Minute
)

// feedClient is the shared HTTP client for pushes; a short timeout so one slow or
// unreachable target can't stall the feed loop for the others.
var feedClient = &http.Client{Timeout: 10 * time.Second}

// feedState is a target's health record: its retry bookkeeping (how many consecutive
// pushes have failed, when it may next be tried, what went wrong last — so the log
// reports a failure and a recovery, not the same error every cycle) plus running totals
// an operator can read back through the FeedStatus Action.
//
// It survives a success — only `failures` resets — because "this target has been fine
// for an hour but dropped 3 pushes overnight" is exactly what you want to know, and a
// record deleted on recovery cannot tell you that.
type feedState struct {
	failures    int // consecutive; 0 = healthy
	nextAttempt time.Time
	lastErr     string
	totalFails  int
	totalOK     int
	lastOK      time.Time
	lastTry     time.Time
}

// backoffFor returns the delay after n consecutive failures: one feed cycle, doubling,
// capped at feedBackoffMax.
func backoffFor(n int) time.Duration {
	if n < 1 {
		n = 1
	}
	d := feedBackoffBase
	for i := 1; i < n && d < feedBackoffMax; i++ {
		d *= 2
	}
	if d > feedBackoffMax {
		d = feedBackoffMax
	}
	return d
}

// FeedTarget is one Alpaca device the environment snapshot is pushed to, via its
// `setenvironment` Action. Each consumer takes what it understands and ignores the
// rest — a tenmicron telescope applies pressure and temperature as refraction datums
// (plus the site and time), while a StellarMate SM Pro switch applies temperature,
// humidity and dew point to its dew heaters. One snapshot, many consumers.
type FeedTarget struct {
	Addr   string `json:"addr"`             // host:port of the Alpaca server
	Type   string `json:"type,omitempty"`   // device type: "telescope" (default) or "switch"
	Device int    `json:"device,omitempty"` // that server's device number (usually 0)
}

// feedTargetTypes are the device types known to accept a setenvironment Action.
// Restricting the set keeps a typo from silently POSTing into the void.
var feedTargetTypes = map[string]bool{"telescope": true, "switch": true}

// normalize fills the default type and validates the target.
func (t FeedTarget) normalize() (FeedTarget, error) {
	t.Addr = strings.TrimSpace(t.Addr)
	t.Type = strings.ToLower(strings.TrimSpace(t.Type))
	if t.Type == "" {
		t.Type = "telescope"
	}
	if t.Addr == "" {
		return t, fmt.Errorf("feed target: empty address")
	}
	if !feedTargetTypes[t.Type] {
		return t, fmt.Errorf("feed target %s: unknown device type %q (want telescope or switch)", t.Addr, t.Type)
	}
	if t.Device < 0 {
		return t, fmt.Errorf("feed target %s: negative device number %d", t.Addr, t.Device)
	}
	return t, nil
}

// String renders a target the way ParseFeedTarget accepts it.
func (t FeedTarget) String() string { return fmt.Sprintf("%s/%s/%d", t.Addr, t.Type, t.Device) }

// ParseFeedTarget parses "host:port[/type[/device]]" — e.g. "10.0.1.5:11111",
// "10.0.1.5:11111/telescope/0", "localhost:11130/switch/0". The type defaults to
// telescope and the device to 0, so the historical "host:port" and "host:port/N"
// spellings still mean what they always did.
func ParseFeedTarget(s string) (FeedTarget, error) {
	parts := strings.Split(strings.TrimSpace(s), "/")
	t := FeedTarget{Addr: parts[0]}
	switch len(parts) {
	case 1:
	case 2:
		// "host:port/N" is the historical telescope-device spelling; "host:port/switch"
		// is a type with a default device number.
		if n, err := strconv.Atoi(parts[1]); err == nil {
			t.Device = n
		} else {
			t.Type = parts[1]
		}
	case 3:
		t.Type = parts[1]
		n, err := strconv.Atoi(parts[2])
		if err != nil {
			return t, fmt.Errorf("feed target %q: device number %q is not a number", s, parts[2])
		}
		t.Device = n
	default:
		return t, fmt.Errorf("feed target %q: want host:port[/type[/device]]", s)
	}
	return t.normalize()
}

// ParseFeedTargets parses a comma-separated target list.
func ParseFeedTargets(s string) ([]FeedTarget, error) {
	var out []FeedTarget
	for _, f := range strings.Split(s, ",") {
		if strings.TrimSpace(f) == "" {
			continue
		}
		t, err := ParseFeedTarget(f)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

// envPayload is the JSON pushed to each target's setenvironment Action. Every field is
// a pointer so only the ones the box actually has are sent, and each consumer applies
// only the fields it understands.
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

// feedLoop periodically pushes the environment snapshot to every configured target. It
// no-ops while no target is configured or no box is attached, and exits on ctx cancel.
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

// feedResult is one target's outcome in a push, as reported back to the caller.
type feedResult struct {
	Target  string `json:"target"`
	Reply   string `json:"reply,omitempty"`
	Error   string `json:"error,omitempty"`
	Skipped string `json:"skipped,omitempty"` // "backing off, retry in 4m0s"
}

// pushEnvironment is the scheduled push: it skips targets that are in backoff after a
// recent failure. See pushEnv.
func (m *MGPBox) pushEnvironment(ctx context.Context) (string, error) {
	return m.pushEnv(ctx, false)
}

// pushEnvironmentNow is the operator-triggered push (the PushMount Action): it ignores
// backoff and tries every target immediately, so someone who has just fixed a mount does
// not have to wait out its retry delay.
func (m *MGPBox) pushEnvironmentNow(ctx context.Context) (string, error) {
	return m.pushEnv(ctx, true)
}

// pushEnv builds the current snapshot and posts it to every configured target that is
// not backing off, returning a JSON summary of what each replied, skipped, or failed
// with. Targets are pushed concurrently and independently: an unreachable mount must
// neither cost the dew heaters their weather nor stall the cycle behind its timeout. It
// returns ("", nil) when the feed is off, no box is attached, or there is nothing to
// send yet.
func (m *MGPBox) pushEnv(ctx context.Context, force bool) (string, error) {
	m.mu.Lock()
	dev := m.dev
	targets := append([]FeedTarget(nil), m.feedTargets...)
	now := m.now()
	// Decide up front which targets are still serving out a backoff.
	skipped := make(map[string]time.Duration, len(targets))
	if !force {
		for _, t := range targets {
			if st := m.feedState[t.String()]; st != nil && st.failures > 0 && now.Before(st.nextAttempt) {
				skipped[t.String()] = st.nextAttempt.Sub(now)
			}
		}
	}
	m.mu.Unlock()

	if len(targets) == 0 || dev == nil {
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

	// Push the live targets concurrently: serially, three dead targets would take three
	// HTTP timeouts (30 s) and overrun the 30 s cycle.
	results := make([]feedResult, len(targets))
	var wg sync.WaitGroup
	for i, t := range targets {
		results[i] = feedResult{Target: t.String()}
		if d, ok := skipped[t.String()]; ok {
			results[i].Skipped = fmt.Sprintf("backing off, retry in %s", d.Round(time.Second))
			continue
		}
		wg.Add(1)
		go func(i int, t FeedTarget) {
			defer wg.Done()
			reply, err := m.postAction(ctx, t, "setenvironment", string(body))
			if err != nil {
				results[i].Error = err.Error()
				return
			}
			results[i].Reply = reply
		}(i, t)
	}
	wg.Wait()

	// Fold the outcomes into each target's retry state, and log only the transitions —
	// the first failure and the recovery — rather than the same error every cycle.
	var firstErr error
	m.mu.Lock()
	after := m.now()
	for i, t := range targets {
		key := t.String()
		if results[i].Skipped != "" {
			continue
		}
		st := m.feedState[key]
		if st == nil {
			st = &feedState{}
			m.feedState[key] = st
		}
		st.lastTry = after

		if e := results[i].Error; e != "" {
			st.failures++
			st.totalFails++
			d := backoffFor(st.failures)
			st.nextAttempt = after.Add(d)
			if st.lastErr != e {
				log.Printf("mgpbox: feed to %s failed (%s); retrying in %s", t, e, d)
			}
			st.lastErr = e
			if firstErr == nil {
				firstErr = errors.New(e)
			}
			continue
		}
		if st.failures > 0 {
			log.Printf("mgpbox: feed to %s recovered after %d failed attempt(s)", t, st.failures)
		}
		st.failures = 0
		st.nextAttempt = time.Time{}
		st.lastErr = ""
		st.totalOK++
		st.lastOK = after
	}
	m.mu.Unlock()

	out, _ := json.Marshal(results)
	return string(out), firstErr
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
		// Send the dew point resolveDewpoint gives us — the box's own transducer
		// reading, or one derived from the temperature and humidity it does report
		// — so a consumer gets a usable value whether or not this unit carries the
		// transducer, and gets the same value the ObservingConditions property
		// reports for the same sample.
		//
		// When there is no dew point to be had, the field is omitted rather than
		// sent as 0. `omitempty` cannot do that for us: it omits a nil pointer, not
		// a non-nil pointer to zero. A consumer taking a 0 °C dew point at face
		// value sees a margin of the whole air temperature ("bone dry") and
		// switches its dew heaters off on exactly the night they are needed.
		if dp, ok := resolveDewpoint(me); ok {
			p.DewpointC = ptr(dp)
		}
		any = true
	}
	// Site + time only from a real position fix, so an unlocked receiver never pushes a
	// 0,0 position or an unsynced clock to a consumer.
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

// postAction issues an Alpaca PUT .../{type}/{device}/action against a target server and
// returns the reply's Value, or an error carrying the Alpaca ErrorMessage.
func (m *MGPBox) postAction(ctx context.Context, t FeedTarget, action, params string) (string, error) {
	endpoint := fmt.Sprintf("http://%s/api/v1/%s/%d/action", t.Addr, t.Type, t.Device)
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
		return "", fmt.Errorf("%s returned HTTP %d", t, resp.StatusCode)
	}
	var r struct {
		Value        string `json:"Value"`
		ErrorNumber  int    `json:"ErrorNumber"`
		ErrorMessage string `json:"ErrorMessage"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return "", fmt.Errorf("%s: bad reply: %w", t, err)
	}
	if r.ErrorNumber != 0 {
		return "", fmt.Errorf("%s setenvironment: %s (0x%X)", t, r.ErrorMessage, r.ErrorNumber)
	}
	return r.Value, nil
}

// FeedHealth is one target's feed health, as reported by the FeedStatus Action. It is
// how an operator finds out that the dew controller quietly stopped being fed three
// hours ago — the feed runs in the background, so without this its failures are visible
// only in the log.
type FeedHealth struct {
	Target string `json:"target"`
	// Healthy is false once a push has failed and the target has not yet recovered.
	Healthy bool `json:"healthy"`
	// ConsecutiveFailures is the current failure streak (0 when healthy); it drives the
	// retry backoff. TotalFailures/TotalPushes are running counts since the feed was
	// configured, so a target that is up now but flaky overnight still shows it.
	ConsecutiveFailures int `json:"consecutiveFailures"`
	TotalFailures       int `json:"totalFailures"`
	TotalPushes         int `json:"totalPushes"`

	LastError string `json:"lastError,omitempty"`
	// RetryInSeconds is how long until the next attempt while backing off.
	RetryInSeconds float64 `json:"retryInSeconds,omitempty"`
	// LastSuccessSeconds is how long ago the last successful push was; nil if a push to
	// this target has never succeeded.
	LastSuccessSeconds *float64 `json:"lastSuccessSeconds,omitempty"`
}

// FeedHealth returns the health of every configured feed target. A target that has never
// been pushed to yet reports Healthy with zero counts.
func (m *MGPBox) FeedHealth() []FeedHealth {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	out := make([]FeedHealth, 0, len(m.feedTargets))
	for _, t := range m.feedTargets {
		h := FeedHealth{Target: t.String(), Healthy: true}
		if st := m.feedState[t.String()]; st != nil {
			h.Healthy = st.failures == 0
			h.ConsecutiveFailures = st.failures
			h.TotalFailures = st.totalFails
			h.TotalPushes = st.totalOK + st.totalFails
			h.LastError = st.lastErr
			if st.failures > 0 && now.Before(st.nextAttempt) {
				h.RetryInSeconds = st.nextAttempt.Sub(now).Seconds()
			}
			if !st.lastOK.IsZero() {
				age := now.Sub(st.lastOK).Seconds()
				h.LastSuccessSeconds = &age
			}
		}
		out = append(out, h)
	}
	return out
}

// feedFailures is the total consecutive-failure count across all targets — the single
// number worth putting in DeviceState: zero means every consumer is being fed.
func (m *MGPBox) feedFailures() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, st := range m.feedState {
		n += st.failures
	}
	return n
}

// ptr returns a pointer to v, for building the optional-field envPayload.
func ptr(v float64) *float64 { return &v }
