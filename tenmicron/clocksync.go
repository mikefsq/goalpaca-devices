package driver

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/mikefsq/goalpaca-devices/internal/hostclock"
	"github.com/mikefsq/lx200/tenmicron"
)

const hostClockLimitMs = 0.1 // 100 microseconds; accuracy, not stratum, is the gate
const julianQuantum = 864 * time.Microsecond

type clockMount interface {
	TimedGet(string) (string, time.Time, time.Time, error)
	NudgeTime(int) error
	GPSSyncState() (tenmicron.GPSSync, error)
	Slewing() (bool, error)
}

type clockSyncResult struct {
	Method                 string          `json:"method"`
	ReturnDelayAllowanceMs float64         `json:"return_delay_allowance_ms"`
	Status                 string          `json:"status"`
	Host                   hostclock.Clock `json:"host"`
	OffsetMs               float64         `json:"offset_ms"` // mount minus host
	UncertaintyMs          float64         `json:"uncertainty_ms"`
	RoundTripMs            float64         `json:"round_trip_ms"`
	Corrections            []int           `json:"corrections_ms"`
}

type clockSample struct{ offset, uncertainty, rtt time.Duration }

// Parse decimal Julian days exactly: floating-point subtraction of a ~2.4M-day
// epoch unnecessarily loses precision. Leap-second samples are not comparable
// with Go's POSIX system clock and are deliberately rejected.
func preciseJulianUTC(reply string) (time.Time, error) {
	parts := strings.Split(reply, ".")
	if len(parts) != 2 || len(parts[0]) != 7 || len(parts[1]) != 8 {
		return time.Time{}, fmt.Errorf("invalid or leap-second GJD2 reply %q", reply)
	}
	for _, c := range parts[0] + parts[1] {
		if c < '0' || c > '9' {
			return time.Time{}, fmt.Errorf("invalid GJD2 reply")
		}
	}
	days, _ := strconv.ParseInt(parts[0], 10, 64)
	fraction, _ := strconv.ParseInt(parts[1], 10, 64)
	if days < 2400000 || days > 2600000 {
		return time.Time{}, fmt.Errorf("mount Julian date outside supported range")
	}
	return time.Unix((days-2440587)*86400-43200, fraction*int64(julianQuantum)).UTC(), nil
}

// Receipt-based estimation: the observed TenMicron request delay is mostly
// before timestamp capture. Do not subtract half that delay. The allowance below
// is an operational assumption accepted for LAN synchronization, not a measured
// bound on the mount-to-host path; direct mount PPS remains more precise.
const receiptDelayAllowance = 15 * time.Millisecond

func measureClock(ctx context.Context, m clockMount, hostErrorMs float64) (clockSample, error) {
	anchor := time.Now()
	samples := make([]clockSample, 0, 7)
	var minRTT, maxRTT time.Duration
	for i := 0; i < 7; i++ {
		if err := ctx.Err(); err != nil {
			return clockSample{}, err
		}
		reply, t0, t1, err := m.TimedGet(":GJD2#")
		if err != nil {
			return clockSample{}, err
		}
		stamp, err := preciseJulianUTC(reply)
		if err != nil {
			return clockSample{}, err
		}
		rtt := t1.Sub(t0)
		if i == 0 || rtt < minRTT {
			minRTT = rtt
		}
		maxRTT = max(maxRTT, rtt)
		wall := t1.Round(0).Sub(t0.Round(0))
		if rtt < 0 || absDuration(wall-rtt) > 100*time.Microsecond || absDuration(t0.Round(0).Sub(anchor.Round(0))-t0.Sub(anchor)) > 100*time.Microsecond {
			return clockSample{}, fmt.Errorf("host clock stepped during measurement")
		}
		// Reject stalled exchanges, rather than treating normal ~110 ms command
		// processing as transport error. RTT is diagnostic, not divided into the offset.
		if rtt > 500*time.Millisecond {
			continue
		}
		samples = append(samples, clockSample{offset: stamp.Sub(t1), rtt: rtt})
	}
	if len(samples) < 3 {
		return clockSample{}, fmt.Errorf("need at least three mount-clock samples with round trip at most 500 ms; accepted %d/7, measured %.3f–%.3f ms", len(samples), float64(minRTT)/float64(time.Millisecond), float64(maxRTT)/float64(time.Millisecond))
	}
	slices.SortFunc(samples, func(a, b clockSample) int {
		if a.offset < b.offset {
			return -1
		}
		if a.offset > b.offset {
			return 1
		}
		return 0
	})
	sample := samples[len(samples)/2]
	spread := samples[len(samples)-1].offset - samples[0].offset
	if spread > 30*time.Millisecond {
		return clockSample{}, fmt.Errorf("receipt-based clock samples unstable: spread %.3f ms exceeds 30 ms", float64(spread)/float64(time.Millisecond))
	}
	deviation := max(sample.offset-samples[0].offset, samples[len(samples)-1].offset-sample.offset)
	sample.uncertainty = receiptDelayAllowance + deviation + julianQuantum + time.Duration(hostErrorMs*float64(time.Millisecond))
	return sample, nil
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

func usableHostClock(ctx context.Context, read func(context.Context) (hostclock.Clock, error)) (hostclock.Clock, error) {
	q, err := read(ctx)
	if err != nil {
		return q, fmt.Errorf("host clock accuracy unavailable: %w", err)
	}
	if !q.Synced || math.IsNaN(q.ErrorMs) || math.IsInf(q.ErrorMs, 0) || q.ErrorMs < 0 || q.ErrorMs > hostClockLimitMs {
		return q, fmt.Errorf("host clock must be synchronized with reported error <= 100 us (got %.6g ms)", q.ErrorMs)
	}
	return q, nil
}

func checkClockMount(m clockMount) error {
	gps, err := m.GPSSyncState()
	if err != nil {
		return err
	}
	if gps != tenmicron.GPSSyncOff {
		return fmt.Errorf("mount GPS synchronization is active; no correction applied")
	}
	moving, err := m.Slewing()
	if err != nil {
		return err
	}
	if moving {
		return fmt.Errorf("mount is slewing; no clock correction applied")
	}
	return nil
}

// runClockSync is bounded and never substitutes a stale client timestamp or a
// coarse absolute set. Relative nudges do not age while travelling to the mount.
func runClockSync(ctx context.Context, m clockMount, read func(context.Context) (hostclock.Clock, error)) (out clockSyncResult, err error) {
	out.Corrections = []int{}
	out.Method = "receipt_estimate"
	out.ReturnDelayAllowanceMs = float64(receiptDelayAllowance) / float64(time.Millisecond)
	anchor := time.Now()
	for round := 0; round < 4; round++ {
		q, e := usableHostClock(ctx, read)
		out.Host = q
		if e != nil {
			return out, e
		}
		if e = checkClockMount(m); e != nil {
			return out, e
		}
		sample, e := measureClock(ctx, m, hostClockLimitMs)
		if e != nil {
			return out, e
		}
		out.OffsetMs = float64(sample.offset) / float64(time.Millisecond)
		out.UncertaintyMs = float64(sample.uncertainty) / float64(time.Millisecond)
		out.RoundTripMs = float64(sample.rtt) / float64(time.Millisecond)
		if absDuration(sample.offset) <= sample.uncertainty+time.Millisecond/2 {
			out.Status = "within_measurement_uncertainty"
			return out, nil
		}
		if round == 3 {
			out.Status = "not_converged"
			return out, nil
		}
		correction := int(math.Round(-out.OffsetMs))
		if correction < -999 || correction > 999 {
			return out, fmt.Errorf("mount offset exceeds fine-sync range; set approximate UTC first")
		}
		// Recheck both authorities immediately before changing the clock.
		if _, e = usableHostClock(ctx, read); e != nil {
			return out, e
		}
		if e = checkClockMount(m); e != nil {
			return out, e
		}
		now := time.Now()
		if absDuration(now.Round(0).Sub(anchor.Round(0))-now.Sub(anchor)) > 100*time.Microsecond {
			return out, fmt.Errorf("host clock changed during synchronization")
		}
		if e = ctx.Err(); e != nil {
			return out, e
		}
		if e = m.NudgeTime(correction); e != nil {
			return out, e
		}
		out.Corrections = append(out.Corrections, correction)
	}
	return out, nil
}

func (t *Telescope) actionSyncClock(params string) (string, error) {
	if params != "" {
		return "", badValue("SyncClock takes no parameters")
	}
	result, err := t.syncHostClock()
	if err != nil {
		return "", err
	}
	data, _ := json.Marshal(result)
	return string(data), nil
}

func (t *Telescope) syncHostClock() (clockSyncResult, error) {
	return t.syncHostClockContext(context.Background())
}

func (t *Telescope) syncHostClockContext(parent context.Context) (clockSyncResult, error) {
	t.clockMu.Lock()
	defer t.clockMu.Unlock()
	m, err := t.live()
	if err != nil {
		return clockSyncResult{}, err
	}
	defer m.OpLock()()
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	out, err := runClockSync(ctx, liveClockMount{m}, hostclock.ReadClock)
	if err != nil {
		return out, fmt.Errorf("clock synchronization failed (applied nudges %v ms): %w", out.Corrections, err)
	}
	t.mu.Lock()
	t.snap.utcSkew = time.Duration(out.OffsetMs * float64(time.Millisecond))
	t.mu.Unlock()
	return out, nil
}

// Force a fresh motion status before each correction instead of trusting the poll cache.
type liveClockMount struct{ *tenmicron.Mount }

func (m liveClockMount) Slewing() (bool, error) {
	if _, err := m.Refresh(); err != nil {
		return false, err
	}
	tracking, err := m.Mount.Tracking()
	if err != nil {
		return false, err
	}
	if tracking {
		return false, fmt.Errorf("mount is tracking; no clock correction applied")
	}
	return m.Mount.Slewing()
}

// actionSetClock sets the mount clock to the host's UTC and then nudges it to match within the
// measurement's uncertainty: the coarse :SUDT set (whole seconds) followed by the same fine sync
// as syncclock (±999 ms nudges). The coarse set moves pointing by up to a second of sky, so it is
// refused while the mount tracks or slews, as on connect.
func (t *Telescope) actionSetClock(params string) (string, error) {
	if params != "" {
		return "", badValue("SetClock takes no parameters")
	}
	if err := t.withIdleMount(true, func(m *tenmicron.Mount) error {
		return m.SetUTC(time.Now().UTC().Round(time.Second))
	}); err != nil {
		return "", err
	}
	return t.actionSyncClock("")
}

// clockOffset is a measurement of the mount clock against the host's, changing nothing.
type clockOffset struct {
	OffsetMs      float64 `json:"offset_ms"` // mount minus host
	UncertaintyMs float64 `json:"uncertainty_ms"`
	RoundTripMs   float64 `json:"round_trip_ms"`
}

// actionClockOffset measures the mount clock minus the host clock without correcting it, so a
// client can show the difference before deciding to sync.
func (t *Telescope) actionClockOffset(params string) (string, error) {
	if params != "" {
		return "", badValue("ClockOffset takes no parameters")
	}
	t.clockMu.Lock()
	defer t.clockMu.Unlock()
	m, err := t.live()
	if err != nil {
		return "", err
	}
	defer m.OpLock()()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sample, err := measureClock(ctx, liveClockMount{m}, hostClockLimitMs)
	if err != nil {
		return "", err
	}
	data, _ := json.Marshal(clockOffset{
		OffsetMs:      float64(sample.offset) / float64(time.Millisecond),
		UncertaintyMs: float64(sample.uncertainty) / float64(time.Millisecond),
		RoundTripMs:   float64(sample.rtt) / float64(time.Millisecond),
	})
	return string(data), nil
}
