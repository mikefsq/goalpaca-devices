package ptpcam

import (
	"context"
	"errors"
	"log"
	"time"

	alpacadev "github.com/mikefsq/goalpaca/server"
	"github.com/mikefsq/ptp"
	"github.com/mikefsq/ptp/usb"
)

// Hardware lifecycle, following the pattern astrocam established.
//
// The Alpaca endpoint must come up with or WITHOUT a camera attached: on a
// fleet box the service starts at boot, and the body is very likely powered off
// at that moment, or gets power-cycled later. So Open never fails for absence —
// it starts a supervisor that acquires the camera whenever one appears and
// re-acquires it after it goes away.
//
// Two different failures have to be told apart, because they are detected
// differently:
//
//   - PHYSICAL ABSENCE. The body was unplugged or powered off. Caught by
//     AliveFn, which reads the OS USB registry and never touches the open
//     camera — a probe that sent PTP traffic would compete with an exposure in
//     flight, and on a body mid-capture that is exactly the wrong moment.
//   - A WEDGED SESSION. The camera is still on the bus but has stopped
//     answering: ptp.ErrNotResponding. AliveFn cannot see this, because the
//     device is still enumerated. Operations that hit it set needsReconnect.

// acquireInterval is how often to look for a camera that is not there yet, and
// maxMisses how many consecutive absent probes before tearing down. The
// debounce matters: USB enumeration can blink during a bus reset, and a single
// missed probe is not a disconnection.
const (
	acquireInterval = 3 * time.Second
	probeInterval   = 2 * time.Second
	maxMisses       = 3
)

// Open starts the hardware-management goroutine and returns immediately, so the
// Alpaca endpoint comes up whether or not a camera is attached.
func (c *Camera) Open(ctx context.Context) error {
	c.stopLoop = alpacadev.RunLoop(ctx, c.ID, c.manageHardware)
	return nil
}

// Close releases the camera on graceful shutdown only.
//
// The handback is not politeness: a Fujifilm body left in PC Priority has its
// dials, buttons and shutter dead in its owner's hands, and a Sony can be left
// with its shutter held down.
func (c *Camera) Close(ctx context.Context) error {
	if c.stopLoop != nil {
		c.stopLoop(10 * time.Second) // end the loop Open started before the handle goes
	}
	c.teardown()
	return nil
}

// manageHardware acquires, monitors and re-acquires the camera for the process
// lifetime.
//
// The OS's hotplug notification (HotplugFn, usb.Hotplug) is the interrupt: an
// attach wakes the acquire attempt at once, a detach of the very attachment
// the session holds tears down at once, and any other event brings the
// presence probe forward. The polls stay as the fallback, slower when the
// notifications are there.
func (c *Camera) manageHardware(ctx context.Context) {
	misses := 0
	events := c.hotplug(ctx)
	acquirePoll, probePoll := acquireInterval, probeInterval
	if events != nil {
		acquirePoll, probePoll = 10*time.Second, 10*time.Second
	}
	// eager counts quick retries left after an attach event or a teardown: the
	// first-match notification lands while the OS is still configuring the
	// body, so the first open can fail (and on macOS ptpcamerad may hold it
	// for a moment), and a second later it succeeds.
	eager := 0
	for ctx.Err() == nil {
		if !c.hwPresent.Load() {
			if c.tryAcquire() {
				misses = 0
				log.Printf("ptpcam: camera %s acquired (%s)", c.ID, c.modelName())
				continue
			}
			wait := acquirePoll
			if eager > 0 {
				eager--
				wait = time.Second
			}
			if ev := c.pause(ctx, events, wait); ev != nil && ev.Attached {
				eager = 5
			}
			continue
		}

		// A wedged session: the body is still enumerated but has stopped
		// answering, so the presence probe below cannot see it. Drop and
		// re-acquire — and on macOS the reopen also has to win the race with
		// ptpcamerad, which re-claims a still-image device on enumeration.
		if c.needsReconnect.CompareAndSwap(true, false) {
			log.Printf("ptpcam: camera %s stopped responding — disconnect + re-acquire", c.ID)
			c.teardown()
			misses = 0
			continue
		}

		if c.alive() {
			misses = 0
			if ev := c.pause(ctx, events, probePoll); ev != nil && c.detachedUs(ev) {
				log.Printf("ptpcam: camera %s unplugged (detach notification); re-acquiring", c.ID)
				c.teardown()
				eager = 5
			}
			continue
		}
		if c.replaced.CompareAndSwap(true, false) {
			log.Printf("ptpcam: camera %s replugged (new attachment); re-acquiring", c.ID)
			c.teardown()
			misses = 0
			eager = 5
			continue
		}
		misses++
		if misses < maxMisses {
			c.pause(ctx, events, probeInterval)
			continue
		}
		log.Printf("ptpcam: camera %s is gone (x%d); re-acquiring", c.ID, misses)
		c.teardown()
		misses = 0
	}
}

// tryAcquire opens the body and readies it. It reports whether a camera is now
// attached; a failure here is the normal case when none is plugged in, so it is
// deliberately quiet.
func (c *Camera) tryAcquire() bool {
	body, err := c.Opener()
	if err != nil || body == nil {
		return false
	}

	c.mu.Lock()
	c.Body = body
	c.capturer, _ = body.(ptp.Capturer)
	c.exposure, _ = body.(ptp.ExposureControl)
	c.download, _ = body.(ptp.Downloader)
	c.rawdec, _ = body.(ptp.RawDecoder)
	// Ask what a capture WILL produce, so SensorType and MaxADU are right from
	// connect rather than only after the first frame. A client reads them while
	// configuring itself, which is before the shutter has ever fired.
	if c.rawdec != nil {
		if info, err := c.rawdec.SensorInfo(); err == nil {
			c.sensor = info
		}
	}
	c.focus, _ = body.(ptp.FocusControl)
	focus := c.focus
	c.mu.Unlock()

	// Manual focus before anything else. On a dark or filtered subject an AF
	// hunt never resolves, and a half press that never completes is how a
	// tethered body stops answering at all.
	if focus != nil {
		focus.SetManualFocus()
	}

	c.mu.Lock()
	c.resolveGeometryLocked()
	c.mu.Unlock()

	c.hwPresent.Store(true)
	return true
}

// teardown closes the camera and marks it absent.
//
// It joins any in-flight exposure first, so no PTP transaction outlives the
// handle it is running on. Must NOT be called holding c.mu: the exposure
// goroutine takes that lock as it finishes.
func (c *Camera) teardown() {
	c.exposeWG.Wait()

	c.mu.Lock()
	body := c.Body
	c.Body = nil
	c.capturer, c.exposure, c.download, c.focus, c.rawdec = nil, nil, nil, nil, nil
	c.exposing = false
	c.state = alpacadev.CameraIdle
	c.mu.Unlock()

	if body != nil {
		body.Close()
	}
	c.hwPresent.Store(false)
}

// alive reports whether the camera is still on the bus.
//
// AliveFn is a seam: the core driver has no USB knowledge, so a probe over the
// OS registry is supplied from outside (NewAliveProbe, used by cmd/ptpcam and by
// the registry entry in hurd.go) and tests override it. With no probe there is
// nothing non-perturbing to ask, so presence is assumed and a loss is caught by
// needsReconnect instead — later, but without inventing traffic.
func (c *Camera) alive() bool {
	if c.AliveFn == nil {
		return true
	}
	return c.AliveFn()
}

// Attachment is the plugging-in the open body's transport was opened on
// (usb.DeviceInfo.Attachment), or 0 with no body or a transport that offers
// none. The alive probe compares it against the current enumeration.
func (c *Camera) Attachment() uint64 {
	c.mu.Lock()
	body := c.Body
	c.mu.Unlock()
	if body == nil {
		return 0
	}
	if i, ok := body.(interface{ Info() (usb.DeviceInfo, bool) }); ok {
		if d, ok := i.Info(); ok {
			return d.Attachment
		}
	}
	return 0
}

// MarkReplaced tells the supervisor the body on the bus is a new attachment
// and the open session is dead; the next probe pass re-acquires at once. The
// alive probe calls it.
func (c *Camera) MarkReplaced() { c.replaced.Store(true) }

// noteTransportError records a failure that means the session is dead, so the
// supervisor re-acquires.
//
// Only ErrNotResponding counts. A camera refusing an operation — because a dial
// owns the setting, because it is still writing to card — is ALIVE and answering,
// and tearing the session down for that would turn a recoverable refusal into a
// reconnect storm.
func (c *Camera) noteTransportError(err error) {
	if errors.Is(err, ptp.ErrNotResponding) {
		c.needsReconnect.Store(true)
	}
}

func (c *Camera) modelName() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.Body == nil {
		return "unknown"
	}
	return c.Body.Model()
}

// Connected reports hardware presence: the Alpaca logical connection IS the
// hardware state, so a client polling Connected sees the camera come and go.
func (c *Camera) Connected() bool { return c.hwPresent.Load() }

// Connect is the client's presence handshake: it succeeds only if a camera is
// actually attached.
func (c *Camera) Connect(ctx context.Context) error {
	if !c.hwPresent.Load() {
		return alpacadev.ErrNotConnected
	}
	return nil
}

// Disconnect is a logical no-op. The driver owns the hardware for the process
// lifetime, and one client going away must not hand the camera back while
// another is still shooting.
func (c *Camera) Disconnect(ctx context.Context) error { return nil }

// hotplug subscribes through HotplugFn, returning nil where there is none or
// it fails (the loop then polls at the faster cadence).
func (c *Camera) hotplug(ctx context.Context) <-chan usb.HotplugEvent {
	if c.HotplugFn == nil {
		return nil
	}
	ch, err := c.HotplugFn(ctx)
	if err != nil {
		log.Printf("ptpcam: camera %s: no hotplug notifications (%v); polling", c.ID, err)
		return nil
	}
	return ch
}

// pause waits up to d, or until a hotplug event arrives (returned) or ctx
// ends. A nil events channel is a plain timed wait.
func (c *Camera) pause(ctx context.Context, events <-chan usb.HotplugEvent, d time.Duration) *usb.HotplugEvent {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return nil
	case <-t.C:
		return nil
	case ev, ok := <-events:
		if !ok {
			return nil
		}
		return &ev
	}
}

// detachedUs reports whether ev is the detach of the attachment the open
// session was opened on: certain knowledge that the session is dead, so no
// debounce applies. Without an attachment id on either side the answer is no
// and the presence probe decides.
func (c *Camera) detachedUs(ev *usb.HotplugEvent) bool {
	if ev.Attached || ev.Attachment == 0 {
		return false
	}
	att := c.Attachment()
	return att != 0 && ev.Attachment == att
}

func sleepCtx(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
