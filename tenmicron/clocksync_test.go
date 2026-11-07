package driver

import (
	"context"
	"errors"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/mikefsq/goalpaca-devices/internal/hostclock"
	"github.com/mikefsq/lx200/tenmicron"
)

type syncMountFake struct {
	delays         []time.Duration
	jitter         []time.Duration
	offset         time.Duration
	rtt            time.Duration
	gps            tenmicron.GPSSync
	moving         bool
	nudges         []int
	reads          int
	malformed      bool
	ignore         bool
	failAfterNudge bool
	step           bool
}

func (m *syncMountFake) TimedGet(cmd string) (string, time.Time, time.Time, error) {
	if cmd != ":GJD2#" {
		panic(cmd)
	}
	m.reads++
	if len(m.delays) > 0 {
		m.rtt = m.delays[(m.reads-1)%len(m.delays)]
	}
	t0 := time.Now()
	t1 := t0.Add(m.rtt)
	if m.step {
		t1 = t1.Round(0).Add(time.Second)
	}
	if m.failAfterNudge && len(m.nudges) > 0 {
		return "", t0, t1, errors.New("disconnected")
	}
	if m.malformed {
		return "2460000.12345678L", t0, t1, nil
	}
	stamp := t1.Add(m.offset)
	if len(m.jitter) > 0 {
		stamp = stamp.Add(m.jitter[(m.reads-1)%len(m.jitter)])
	}
	// Fake replies use exact integer arithmetic, like the production parser.
	seconds := stamp.Unix() + 43200
	days := seconds/86400 + 2440587
	fraction := (seconds%86400*1e9 + int64(stamp.Nanosecond())) / int64(julianQuantum)
	return fmt.Sprintf("%07d.%08d", days, fraction), t0, t1, nil
}
func (m *syncMountFake) NudgeTime(ms int) error {
	m.nudges = append(m.nudges, ms)
	if !m.ignore {
		m.offset += time.Duration(ms) * time.Millisecond
	}
	return nil
}
func (m *syncMountFake) GPSSyncState() (tenmicron.GPSSync, error) { return m.gps, nil }
func (m *syncMountFake) Slewing() (bool, error)                   { return m.moving, nil }
func goodHost(context.Context) (hostclock.Clock, error) {
	return hostclock.Clock{Client: "Chrony", Reference: "PPS", Stratum: 1, ErrorMs: 0.025, Synced: true}, nil
}

func TestClockSyncCorrectsAndVerifies(t *testing.T) {
	for _, offset := range []time.Duration{52 * time.Millisecond, -52 * time.Millisecond} {
		m := &syncMountFake{offset: offset, rtt: 200 * time.Microsecond}
		result, err := runClockSync(context.Background(), m, goodHost)
		if err != nil || result.Status != "within_measurement_uncertainty" || len(m.nudges) == 0 || m.reads < 14 {
			t.Fatalf("%+v %+v %v", m, result, err)
		}
		if math.Abs(result.OffsetMs) > result.UncertaintyMs+0.5 {
			t.Fatal(result)
		}
		if absDuration(m.offset) > 2*time.Millisecond {
			t.Fatalf("residual %v", m.offset)
		}
	}
}
func TestClockSyncRefusesUnsafeReferences(t *testing.T) {
	for _, q := range []hostclock.Clock{{}, {Synced: true, ErrorMs: 0.101}, {Synced: true, ErrorMs: math.NaN()}, {Synced: true, ErrorMs: -1}, {Synced: true, ErrorMs: math.Inf(1)}} {
		m := &syncMountFake{offset: 52 * time.Millisecond}
		_, err := runClockSync(context.Background(), m, func(context.Context) (hostclock.Clock, error) { return q, nil })
		if err == nil || len(m.nudges) > 0 || m.reads > 0 {
			t.Fatalf("%+v %v", m, err)
		}
	}
	for _, m := range []*syncMountFake{{gps: tenmicron.GPSSyncPPS}, {moving: true}, {malformed: true}, {step: true}, {rtt: time.Second}, {offset: 2 * time.Second}} {
		_, err := runClockSync(context.Background(), m, goodHost)
		if err == nil || len(m.nudges) > 0 {
			t.Fatalf("%+v %v", m, err)
		}
	}
}
func TestClockSyncRechecksHostBeforeWrite(t *testing.T) {
	m := &syncMountFake{offset: 52 * time.Millisecond}
	calls := 0
	_, err := runClockSync(context.Background(), m, func(ctx context.Context) (hostclock.Clock, error) {
		calls++
		if calls == 2 {
			return hostclock.Clock{}, errors.New("lost PPS")
		}
		return goodHost(ctx)
	})
	if err == nil || len(m.nudges) > 0 {
		t.Fatalf("%+v %v", m, err)
	}
}
func TestClockSyncBoundedAndVerificationFailure(t *testing.T) {
	m := &syncMountFake{offset: 52 * time.Millisecond, ignore: true}
	r, err := runClockSync(context.Background(), m, goodHost)
	if err != nil || r.Status != "not_converged" || len(m.nudges) != 3 {
		t.Fatalf("%+v %v", r, err)
	}
	m = &syncMountFake{offset: 52 * time.Millisecond, failAfterNudge: true}
	r, err = runClockSync(context.Background(), m, goodHost)
	if err == nil || len(r.Corrections) != 1 {
		t.Fatalf("%+v %v", r, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m = &syncMountFake{offset: 52 * time.Millisecond}
	if _, err = runClockSync(ctx, m, goodHost); err == nil || m.reads > 0 {
		t.Fatal(err)
	}
}
func TestPreciseJulianUTC(t *testing.T) {
	for _, tc := range []struct {
		s    string
		want time.Time
	}{
		{"2440587.50000000", time.Unix(0, 0)},
		{"2440587.50000001", time.Unix(0, 864000)},
		{"2451545.00000000", time.Date(2000, 1, 1, 12, 0, 0, 0, time.UTC)},
	} {
		got, err := preciseJulianUTC(tc.s)
		if err != nil || !got.Equal(tc.want) {
			t.Fatalf("%s: %v %v", tc.s, got, err)
		}
	}
	for _, s := range []string{"", "2440587.50000", "2440587.50000000L", "2440587.NaN00000", "9999999.00000000"} {
		if _, err := preciseJulianUTC(s); err == nil {
			t.Fatal(s)
		}
	}
}
func TestSyncClockRegisteredAndEnvironmentConflict(t *testing.T) {
	tel := &Telescope{}
	if _, ok := tel.actions()["syncclock"]; !ok {
		t.Fatal("missing SyncClock")
	}
	if _, err := tel.actionSetEnvironment(`{"time":"2026-01-01T00:00:00Z","sync_clock":true}`); err == nil {
		t.Fatal("accepted conflicting clocks")
	}
}

func TestReceiptClockSyncIgnoresPreCaptureDelay(t *testing.T) {
	m := &syncMountFake{offset: 52 * time.Millisecond, delays: []time.Duration{96 * time.Millisecond, 125 * time.Millisecond, 111 * time.Millisecond}, jitter: []time.Duration{0, 500 * time.Microsecond, -500 * time.Microsecond}}
	r, err := runClockSync(context.Background(), m, goodHost)
	if err != nil || r.Status != "within_measurement_uncertainty" || len(m.nudges) != 1 {
		t.Fatalf("%+v %v", r, err)
	}
	if m.nudges[0] < -53 || m.nudges[0] > -51 {
		t.Fatalf("wrong correction: %+v", r)
	}
	if r.Method != "receipt_estimate" || r.UncertaintyMs < 15 || r.UncertaintyMs > 18 {
		t.Fatal(r)
	}
}
func TestReceiptClockRejectsVariableReturnDelay(t *testing.T) {
	m := &syncMountFake{offset: 52 * time.Millisecond, rtt: 110 * time.Millisecond, jitter: []time.Duration{0, -40 * time.Millisecond}}
	if _, err := runClockSync(context.Background(), m, goodHost); err == nil || len(m.nudges) != 0 {
		t.Fatalf("%+v %v", m, err)
	}
}

// TestClockActionsRegistered: setclock and clockoffset exist and take no parameters.
func TestClockActionsRegistered(t *testing.T) {
	tel := NewTelescope("test")
	for _, name := range []string{"setclock", "clockoffset"} {
		fn, ok := tel.actions()[name]
		if !ok {
			t.Fatalf("%s not registered", name)
		}
		if _, err := fn("x"); err == nil {
			t.Errorf("%s accepted a parameter", name)
		}
		if _, err := fn(""); err == nil {
			t.Errorf("%s ran with no mount connected", name)
		}
	}
}
