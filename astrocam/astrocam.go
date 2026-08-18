// Package driver is the ASCOM Alpaca Camera device for ZWO ASI and PlayerOne
// cameras, over the Go astrocam library (the USB wire protocol implemented
// directly — no ZWO SDK). It is served standalone by cmd/astrocam and hosted by
// the alpacahurd aggregator.
package driver

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mikefsq/astrocam"
	alpacadev "github.com/mikefsq/goalpaca/server"
)

// camDebug logs per-exposure arm/read/total timing. Off by default; set ASICAM_DEBUG=1
// (or true/yes/on) to enable.
var camDebug = func() bool {
	switch strings.ToLower(os.Getenv("ASICAM_DEBUG")) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}()

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

// PureASICamera adapts the Go astrocam.Camera to the alpacadev.Camera + Hardware
// interfaces (cgo-SDK-free USB control transfers over IOKit/usbfs/WinUSB).
//
// astrocam.Camera is internally concurrency-safe, so several Alpaca HTTP handlers can hit
// it at once. The adapter's own mu guards only adapter-side state (geometry, the exposure
// result, flags); it is never held across a blocking readout or a cooling state change.
type PureASICamera struct {
	// stopLoop ends the loop Open started and waits for it. Close calls it
	// before releasing the handle, so a reload's replacement opens the hardware
	// with no old loop left to re-acquire it (server.RunLoop).
	stopLoop func(time.Duration)
	alpacadev.BaseCamera

	index      int
	wantSerial string // if set, bind only the camera with this serial (hex)

	// Injection seams (default to the real USB paths; stub-transport e2e tests override them):
	// openDev opens — but does not Init — the target camera; aliveFn reports whether it is
	// still present.
	openDev func() (*astrocam.Camera, astrocam.DeviceInfo, error)
	aliveFn func() bool
	// hotplugFn subscribes to the OS's USB attach and detach notifications; astrocam.Hotplug by
	// default, nil in tests, which then exercise the polling path.
	hotplugFn func(context.Context) (<-chan astrocam.HotplugEvent, error)

	cam *astrocam.Camera
	loc uint32 // USB location of the open camera (non-perturbing liveness check via Enumerate)
	// attachment is the plugging-in the open handle belongs to (Camera.Attachment); 0 when the
	// platform offers none. Enumerate reporting a different one at loc means the camera was
	// unplugged and replugged between two polls: the port is occupied and the handle is dead.
	attachment uint64

	mu        sync.Mutex
	hwPresent atomic.Bool // camera open; read lock-free by Connected()
	exposeOp  alpacadev.Op
	exposeWG  sync.WaitGroup // tracks the in-flight runExposure goroutine; teardown and AbortExposure join it
	// exposeMu serializes the StartExposure/AbortExposure lifecycle so AbortExposure's join
	// (exposeWG.Wait) can never race a concurrent StartExposure's exposeWG.Add. Held only across
	// those short sequences, never across a readout (which takes c.mu, not this).
	exposeMu sync.Mutex
	frame    []byte // last readout, raw little-endian
	frameW   int
	frameH   int
	frameBpp int // bytes/pixel of the last readout (2 = RAW16, 1 = RAW8)

	lastDuration float64
	lastStart    time.Time
	haveLast     bool

	// fpsPercent is the FPS-percent / bandwidth-overload throttle (40..100) the readout HMAX/line-
	// time math derates by, as set via the Alpaca Action "fpspercent". 0 means "never set" — the
	// camera keeps its link-dependent default (100 on USB3, 40 on USB2); the query reads the live
	// effective value from the camera, not this field. Re-applied on reconnect when non-zero. Lower
	// values slow the readout (larger HMAX) to fit a constrained USB link.
	fpsPercent int

	pulsing bool

	// Desired capture geometry (applied to asicam at StartExposure).
	startX, startY int
	numX, numY     int

	// Factory hot-pixel correction. Off by default; enabled by the host "fixdefects" spec field
	// via SetFixDefects. The per-unit defect map is read once from SPI flash and applied to
	// full-frame RAW16 frames in runExposure.
	fixDefects bool
	defectMap  *astrocam.DefectMap

	// Cached ranges (filled at acquire).
	gainMin, gainMax     int
	offsetMin, offsetMax int
	offsetOK             bool
	expMinSec, expMaxSec float64

	coolerOn bool
	setpoint float64 // CCD temperature setpoint °C

	// needsReconnect is set when a readout returns ErrDeviceWedged (driver-confirmed dead device);
	// manageHardware then tears down and re-acquires. runExposure can't tear down itself (it runs
	// inside exposeWG, which teardown joins — self-deadlock), so it signals through this flag.
	needsReconnect atomic.Bool
	// replaced is set by stillPresent when the device at our port is a new attachment: the
	// handle is dead for certain, so manageHardware re-acquires at once rather than after the
	// miss count that guards against a transient enumeration gap.
	replaced atomic.Bool

	// Video (free-run) mode — toggled by the Alpaca Action "videomode" (constant-exposure guiding).
	// When on, drainVideo runs the sensor free-run (cam.StartVideo) and continuously reads every
	// frame into vidFrame (the latest), bumping vidSeq; StartExposure then waits for a frame newer
	// than the call instead of arming a single shot (~2× the rate). The continuous drain is
	// mandatory: it keeps the FX3 from backing up (the wedge). All under mu unless noted; the drain
	// goroutine's lifetime is owned by vidCancel/vidWG.
	videoOn            bool
	vidCancel          context.CancelFunc
	vidWG              sync.WaitGroup
	vidFrame           []byte // latest free-run frame (raw little-endian)
	vidW, vidH, vidBpp int
	vidSeq             uint64  // bumped each drained frame; StartExposure waits for seq > its snapshot
	vidExp             float64 // exposure (s) the stream runs at; a change restarts it
	// Geometry the stream was armed at — a change in exposure, ROI, or binning restarts it so the
	// free-run stream always reflects the client's current settings (gain is live, no restart).
	vidStartX, vidStartY, vidNumX, vidNumY, vidBin int
}

// NewPureASICamera creates the driver for a camera selected by serial (preferred, stable) or,
// if serial is "", by enumeration index. The UniqueID is known up front from the serial, so
// the device registers with a stable identity before the camera is plugged in.
func NewPureASICamera(index int, serial string) *PureASICamera {
	c := &PureASICamera{index: index, wantSerial: strings.ToLower(serial)}
	c.openDev = c.openReal
	c.aliveFn = c.stillPresent
	c.hotplugFn = astrocam.Hotplug
	c.Version = "0.1.0"
	c.Info = "asicam-alpaca — ZWO ASI Alpaca driver over the Go asicam (no ZWO SDK)"
	c.IfaceVer = alpacadev.InterfaceVersionCamera
	c.setpoint = 0
	if c.wantSerial != "" {
		c.ID = "ASI-" + c.wantSerial
		c.DevName = "ASI Camera " + c.wantSerial
	} else {
		c.ID = fmt.Sprintf("ASI-cam%d", index)
		c.DevName = fmt.Sprintf("ASI Camera %d", index)
	}
	return c
}

// Serial returns the factory serial this camera was told to bind, or "" when it
// binds by enumeration index.
func (c *PureASICamera) Serial() string { return c.wantSerial }

// SetFixDefects enables factory hot-pixel correction: the per-unit defect map read once from
// SPI flash, neighbour-averaged into full-frame RAW16 frames. Off by default; set by the host
// from the "fixdefects" device-spec field.
func (c *PureASICamera) SetFixDefects(on bool) {
	c.mu.Lock()
	c.fixDefects = on
	c.mu.Unlock()
}

// SetFPSPercent stores the readout throttle (40..100) to apply when the camera is
// acquired, and applies it at once when the camera is already open. 0 clears the
// stored value so the camera keeps its link-dependent default. It is the config-file
// counterpart of the "fpspercent" Action.
func (c *PureASICamera) SetFPSPercent(pct int) error {
	if pct != 0 && (pct < 40 || pct > 100) {
		return fmt.Errorf("%w: fpsPercent %d out of range 40..100", alpacadev.ErrInvalidValue, pct)
	}
	if !c.hwPresent.Load() {
		c.mu.Lock()
		c.fpsPercent = pct
		c.mu.Unlock()
		return nil
	}
	if pct == 0 {
		c.mu.Lock()
		c.fpsPercent = 0
		c.mu.Unlock()
		return nil
	}
	_, err := c.actionFPSPercent(strconv.Itoa(pct))
	return err
}

// Reconfigure applies the live keys of a Config from the setup page: FixDefects
// and FpsPercent. Index and Serial select the hardware and are start-time, so
// the setup page never sends a change to them; they are read here only to keep
// the signature honest about what arrives.
func (c *PureASICamera) Reconfigure(v any) error {
	cfg, ok := v.(*Config)
	if !ok {
		return fmt.Errorf("astrocam: Reconfigure wants *Config, got %T", v)
	}
	c.SetFixDefects(cfg.FixDefects)
	return c.SetFPSPercent(cfg.FpsPercent)
}

// --- Hardware lifecycle ---

// Open starts the hardware-management goroutine and returns immediately, so the Alpaca
// endpoint comes up with or without a camera attached.
func (c *PureASICamera) Open(ctx context.Context) error {
	c.stopLoop = alpacadev.RunLoop(ctx, c.ID, c.manageHardware)
	return nil
}

// Close releases the camera on graceful shutdown only (cam.Close stops cooling + USB).
func (c *PureASICamera) Close(ctx context.Context) error {
	if c.stopLoop != nil {
		c.stopLoop(10 * time.Second) // end the loop Open started before the handle goes
	}
	c.teardown()
	return nil
}

// teardown aborts any in-flight exposure and waits for the readout goroutine to exit before
// closing the camera, so no control transfer outlives the freed USB handle. Must be called
// without holding c.mu: runExposure takes c.mu as it finishes, so joining it under the lock
// would deadlock. cam.Close is idempotent, so this is safe to call more than once.
func (c *PureASICamera) teardown() {
	c.stopVideo() // stop the free-run drain + stream first, so no ReadFrame outlives the handle
	if c.hwPresent.Load() {
		_ = c.AbortExposure() // signal the in-flight readout to unwind (StopExposure)
	}
	c.exposeWG.Wait() // join it before freeing the handle
	c.mu.Lock()
	if c.cam != nil {
		_ = c.cam.Close()
	}
	c.hwPresent.Store(false)
	c.mu.Unlock()
}

// Connected reports hardware presence; the Alpaca logical connection IS the hardware state.
func (c *PureASICamera) Connected() bool { return c.hwPresent.Load() }

// Disconnect is a logical no-op: the driver owns the hardware for the process lifetime (a
// client disconnect must not reset the TEC).
func (c *PureASICamera) Disconnect(ctx context.Context) error { return nil }

// Busy rejects mutating writes during an exposure. Driver-side state only, no USB.
func (c *PureASICamera) Busy() bool { return c.exposeOp.State() == alpacadev.OpBusy }

// Connect is the client's presence handshake: succeeds iff the hardware is attached.
func (c *PureASICamera) Connect(ctx context.Context) error {
	if !c.hwPresent.Load() {
		return alpacadev.ErrNotConnected
	}
	return nil
}

// manageHardware acquires, monitors, and re-acquires the camera for the process lifetime.
//
// The OS's hotplug notification (astrocam.Hotplug) is the interrupt: an attach wakes the
// acquire attempt at once, a detach of the very attachment we hold tears down at once, and any
// other event brings the presence check forward. The polls stay as the fallback for a platform
// without notifications or a missed one, at a slower cadence when the notifications are there.
func (c *PureASICamera) manageHardware(ctx context.Context) {
	const maxMisses = 3
	misses := 0
	events := c.hotplug(ctx)
	acquirePoll, alivePoll := 3*time.Second, 2*time.Second
	if events != nil {
		acquirePoll, alivePoll = 10*time.Second, 10*time.Second
	}
	// eager counts quick retries left after an attach event or a teardown: the
	// first-match notification lands while the OS is still configuring the
	// device, so the first open can fail, and a second later it succeeds. Only
	// after those does the acquire fall back to the slow poll.
	eager := 0
	for ctx.Err() == nil {
		if !c.hwPresent.Load() {
			if c.tryAcquire() {
				misses = 0
				log.Printf("asicam-alpaca: %s acquired", c.Label())
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
		// ErrDeviceWedged from a readout (driver-confirmed dead device): drop and re-acquire. A wedge
		// isn't a physical absence, so the aliveFn probe below won't catch it; teardown joins the
		// in-flight readout, then the loop re-opens by serial and re-Inits.
		if c.needsReconnect.CompareAndSwap(true, false) {
			log.Printf("asicam-alpaca: %s readout wedged — disconnect + re-acquire", c.Label())
			c.teardown()
			misses = 0
			continue
		}
		// Liveness via the aliveFn seam — by default Enumerate (reads the OS USB registry, never
		// touches the open camera; non-perturbing). Tests override it.
		if c.aliveFn() {
			misses = 0
			if ev := c.pause(ctx, events, alivePoll); ev != nil && c.detachedUs(ev) {
				log.Printf("asicam-alpaca: %s unplugged (detach notification); re-acquiring", c.Label())
				c.teardown()
				eager = 5
			}
			continue
		}
		if c.replaced.CompareAndSwap(true, false) {
			log.Printf("asicam-alpaca: %s replugged (new attachment at the same port); re-acquiring", c.Label())
			c.teardown()
			misses = 0
			eager = 5
			continue
		}
		misses++
		if misses < maxMisses {
			c.pause(ctx, events, 2*time.Second)
			continue
		}
		log.Printf("asicam-alpaca: %s unplugged (x%d); re-acquiring", c.Label(), misses)
		c.teardown()
		misses = 0
	}
}

// stillPresent reports whether the camera the open handle belongs to is still on the bus:
// present at our USB location and, where the platform tells attachments apart, the same
// attachment. A different attachment at the same location is a replug (macOS keeps the
// locationID per port; the old IOUSBHost handle is dead), reported through c.replaced so
// manageHardware re-acquires without waiting out the miss count.
func (c *PureASICamera) stillPresent() bool {
	devs, err := astrocam.Enumerate()
	if err != nil {
		return true // can't tell — don't tear down on a query error
	}
	c.mu.Lock()
	loc, att := c.loc, c.attachment
	c.mu.Unlock()
	present, replaced := attachmentPresent(devs, loc, att)
	if replaced {
		c.replaced.Store(true)
	}
	return present
}

// attachmentPresent is stillPresent's judgement over an enumeration: present when a device
// sits at loc with the same attachment (or an attachment neither side can name); replaced
// when a device sits at loc under a different attachment.
func attachmentPresent(devs []astrocam.DeviceInfo, loc uint32, attachment uint64) (present, replaced bool) {
	for _, d := range devs {
		if d.Location != loc {
			continue
		}
		if attachment != 0 && d.Attachment != 0 && d.Attachment != attachment {
			return false, true
		}
		return true, false
	}
	return false, false
}

// tryAcquire opens the target camera (via the openDev seam), initializes it, and publishes
// it as the live handle. Returns true on success.
func (c *PureASICamera) tryAcquire() bool {
	cam, d, err := c.openDev()
	if err != nil || cam == nil {
		return false
	}
	if err := cam.Init(); err != nil {
		_ = cam.Close()
		return false
	}
	sn, _ := cam.SerialNumber()
	c.configureOpened(cam, d, sn.String())
	return true
}

// openReal is the default openDev: it opens (but does not Init) the target camera by serial
// (OpenSerial) or by enumeration index (Enumerate + OpenLocation).
func (c *PureASICamera) openReal() (*astrocam.Camera, astrocam.DeviceInfo, error) {
	var (
		t   astrocam.Transport
		d   astrocam.DeviceInfo
		err error
	)
	if c.wantSerial != "" {
		t, d, err = astrocam.OpenSerial(c.wantSerial)
	} else {
		devs, e := astrocam.Enumerate()
		if e != nil || c.index >= len(devs) {
			return nil, d, fmt.Errorf("no camera at index %d", c.index)
		}
		d = devs[c.index]
		t, err = astrocam.OpenLocation(d.VID, d.Location)
	}
	if err != nil {
		return nil, d, err
	}
	cam, err := astrocam.Open(t, d.VID, d.PID)
	if err != nil {
		_ = t.Close()
		return nil, d, err
	}
	return cam, d, nil
}

// configureOpened applies capture defaults and publishes the handle.
func (c *PureASICamera) configureOpened(cam *astrocam.Camera, d astrocam.DeviceInfo, serialHex string) {
	info := cam.Info()
	c.mu.Lock()
	fpsPct := c.fpsPercent
	c.mu.Unlock()
	if fpsPct != 0 {
		cam.SetFPSPercent(fpsPct) // re-apply the throttle set before this (re)connect
	}
	_ = cam.SetROI(0, 0, info.MaxWidth, info.MaxHeight) // full frame
	_ = cam.SetGain(0)
	_ = cam.SetExposure(time.Second)
	// The offset is set explicitly to the range default and then read back from the sensor
	// (Offset() reads the registers), so the value the client sees is what the camera holds.
	omin, omax, odef, ook := cam.OffsetRange()
	if ook {
		if err := cam.SetOffset(odef); err != nil {
			log.Printf("asicam-alpaca: %s: set default offset %d: %v", c.Label(), odef, err)
		}
		if got := cam.Offset(); got != odef {
			log.Printf("asicam-alpaca: %s: offset read back %d after setting %d", c.Label(), got, odef)
		}
	}

	gmin, gmax := cam.GainRange()
	emin, emax := cam.ExposureRange()

	c.mu.Lock()
	defer c.mu.Unlock()
	c.cam = cam
	c.loc = d.Location
	c.attachment = cam.Attachment()
	c.coolerOn = false
	c.startX, c.startY = 0, 0
	c.numX, c.numY = info.MaxWidth, info.MaxHeight
	c.gainMin, c.gainMax = gmin, gmax
	c.offsetMin, c.offsetMax, c.offsetOK = omin, omax, ook
	c.expMinSec, c.expMaxSec = emin.Seconds(), emax.Seconds()
	c.DevName = cam.Name()
	c.Desc = fmt.Sprintf("ZWO %s (%dx%d, %.2fµm) [Go asicam]", cam.Name(), info.MaxWidth, info.MaxHeight, info.PixelUm)
	if c.wantSerial == "" && strings.Trim(serialHex, "0") != "" {
		c.ID = "ASI-" + serialHex
	}
	c.hwPresent.Store(true)
}

// hotplug subscribes to the OS's attach and detach notifications through the hotplugFn seam,
// returning nil where none is available (the loop then polls at the faster cadence).
func (c *PureASICamera) hotplug(ctx context.Context) <-chan astrocam.HotplugEvent {
	if c.hotplugFn == nil {
		return nil
	}
	ch, err := c.hotplugFn(ctx)
	if err != nil {
		log.Printf("asicam-alpaca: %s: no hotplug notifications (%v); polling", c.Label(), err)
		return nil
	}
	return ch
}

// pause waits up to d, or until a hotplug event arrives (returned) or ctx ends. A nil events
// channel is a plain timed wait. Events that queued up behind the first are left for the next
// pause; each pass of the loop re-checks presence anyway.
func (c *PureASICamera) pause(ctx context.Context, events <-chan astrocam.HotplugEvent, d time.Duration) *astrocam.HotplugEvent {
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

// detachedUs reports whether ev is the detach of the attachment the open handle belongs to,
// which is certain knowledge that the handle is dead: no debounce applies. Without an
// attachment id on either side the answer is no, and the presence poll decides.
func (c *PureASICamera) detachedUs(ev *astrocam.HotplugEvent) bool {
	if ev.Attached || ev.Attachment == 0 {
		return false
	}
	c.mu.Lock()
	att := c.attachment
	c.mu.Unlock()
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

// --- Geometry / description ---

func (c *PureASICamera) CameraXSize() int    { return c.cam.Info().MaxWidth }
func (c *PureASICamera) CameraYSize() int    { return c.cam.Info().MaxHeight }
func (c *PureASICamera) PixelSizeX() float64 { return c.cam.Info().PixelUm }
func (c *PureASICamera) PixelSizeY() float64 { return c.cam.Info().PixelUm }
func (c *PureASICamera) SensorName() string  { return c.cam.Name() }
func (c *PureASICamera) MaxADU() int {
	if c.cam.OutputDepth() == 1 {
		return 255 // RAW8
	}
	return 65535 // RAW16
}

func (c *PureASICamera) SensorType() alpacadev.SensorType {
	if c.cam.Color() {
		return alpacadev.SensorRGGB // RAW16 Bayer; client debayers
	}
	return alpacadev.SensorMonochrome
}

// --- Binning: symmetric, factors advertised from the sensor's Bins. SetBinX drives
// astrocam.SetBinning; a listed factor whose readout geometry isn't decoded yet fails later at
// SetROI (StartExposure) with InvalidValue, not here. ---

func (c *PureASICamera) BinX() int { return c.cam.Binning() }
func (c *PureASICamera) BinY() int { return c.cam.Binning() }

// maxBin is the largest symmetric factor the sensor advertises (Alpaca MaxBinX/Y).
func (c *PureASICamera) maxBin() int {
	m := 1
	for _, b := range c.cam.Bins() {
		if b > m {
			m = b
		}
	}
	return m
}
func (c *PureASICamera) MaxBinX() int { return c.maxBin() }
func (c *PureASICamera) MaxBinY() int { return c.maxBin() }

// SetBinX selects the symmetric binning factor and resets the subframe to the full binned
// frame (ASCOM convention; NumX/NumY are in binned pixels). astrocam.SetBinning validates the
// factor against the sensor's Bins.
func (c *PureASICamera) SetBinX(n int) error {
	if err := c.cam.SetBinning(n); err != nil {
		return fmt.Errorf("%w: %v", alpacadev.ErrInvalidValue, err)
	}
	c.mu.Lock()
	c.startX, c.startY = 0, 0
	c.numX, c.numY = c.cam.Info().MaxWidth/n, c.cam.Info().MaxHeight/n
	c.mu.Unlock()
	return nil
}
func (c *PureASICamera) SetBinY(n int) error { return c.SetBinX(n) }

// --- Subframe (stored; applied at StartExposure) ---

func (c *PureASICamera) StartX() int { c.mu.Lock(); defer c.mu.Unlock(); return c.startX }
func (c *PureASICamera) StartY() int { c.mu.Lock(); defer c.mu.Unlock(); return c.startY }
func (c *PureASICamera) NumX() int   { c.mu.Lock(); defer c.mu.Unlock(); return c.numX }
func (c *PureASICamera) NumY() int   { c.mu.Lock(); defer c.mu.Unlock(); return c.numY }

func (c *PureASICamera) SetStartX(n int) error { c.mu.Lock(); c.startX = n; c.mu.Unlock(); return nil }
func (c *PureASICamera) SetStartY(n int) error { c.mu.Lock(); c.startY = n; c.mu.Unlock(); return nil }
func (c *PureASICamera) SetNumX(n int) error   { c.mu.Lock(); c.numX = n; c.mu.Unlock(); return nil }
func (c *PureASICamera) SetNumY(n int) error   { c.mu.Lock(); c.numY = n; c.mu.Unlock(); return nil }

// --- Gain + Offset (offset = ASI Brightness / black level) ---

func (c *PureASICamera) Gain() int    { return c.cam.Gain() }
func (c *PureASICamera) GainMin() int { return c.gainMin }
func (c *PureASICamera) GainMax() int { return c.gainMax }

func (c *PureASICamera) SetGain(n int) error {
	if n < c.gainMin || n > c.gainMax {
		return alpacadev.ErrInvalidValue
	}
	return c.cam.SetGain(n)
}

func (c *PureASICamera) Offset() int    { return c.cam.Offset() }
func (c *PureASICamera) OffsetMin() int { return c.offsetMin }
func (c *PureASICamera) OffsetMax() int { return c.offsetMax }

func (c *PureASICamera) SetOffset(n int) error {
	if !c.offsetOK || n < c.offsetMin || n > c.offsetMax {
		return alpacadev.ErrInvalidValue
	}
	return c.cam.SetOffset(n)
}

// --- Exposure (async) ---

func (c *PureASICamera) ExposureMin() float64        { return c.expMinSec }
func (c *PureASICamera) ExposureMax() float64        { return c.expMaxSec }
func (c *PureASICamera) ExposureResolution() float64 { return 1e-6 } // 1 µs
func (c *PureASICamera) CanAbortExposure() bool      { return true }
func (c *PureASICamera) CanStopExposure() bool       { return true }

// --- Video (free-run) mode: the Alpaca Action "videomode" toggles it ---

// SupportedActions advertises the device-specific Actions (CamelCase; matched
// case-insensitively). "VideoMode" switches the camera between single-shot and continuous
// free-run streaming — params on|off writes, empty reads the current state (put/empty=read);
// "FpsPercent" (params 40..100, or empty to query) sets the readout bandwidth-overload
// throttle; "CoolerFault" (read-only) returns the error the cooling loop retired with, or "".
func (c *PureASICamera) SupportedActions() []string {
	return []string{"VideoMode", "FpsPercent", "CoolerFault"}
}

// Action handles the device-specific commands. videomode=on starts the internal free-run stream
// (StartExposure then serves the latest streamed frame, ~2× the rate); videomode=off returns to
// single-shot. Frames still flow over the standard ImageArray path; only the acquisition engine
// changes. fpspercent sets the FPS-percent / bandwidth-overload throttle (see actionFPSPercent).
func (c *PureASICamera) Action(name, params string) (string, error) {
	switch {
	case strings.EqualFold(name, "videomode"):
		return c.actionVideoMode(params)
	case strings.EqualFold(name, "fpspercent"):
		return c.actionFPSPercent(params)
	case strings.EqualFold(name, "coolerfault"):
		// Read-only: the error the cooling loop retired with (empty while it runs or was
		// switched off by the client); CoolerOn already reflects the retire.
		if err := c.cam.CoolerFault(); err != nil {
			return err.Error(), nil
		}
		return "", nil
	default:
		return c.BaseCamera.Action(name, params)
	}
}

func (c *PureASICamera) actionVideoMode(params string) (string, error) {
	if !c.hwPresent.Load() {
		return "", alpacadev.ErrNotConnected
	}
	switch strings.ToLower(strings.TrimSpace(params)) {
	case "": // empty params reads the current state (put/empty = read)
		c.mu.Lock()
		on := c.videoOn
		c.mu.Unlock()
		return strconv.FormatBool(on), nil
	case "on", "true", "1", "start":
		c.mu.Lock()
		dur := c.lastDuration
		c.mu.Unlock()
		if dur <= 0 {
			dur = 0.1 // default exposure if the client never set one
		}
		if err := c.startVideo(dur); err != nil {
			return "", err
		}
		return "ok", nil
	case "off", "false", "0", "stop":
		c.stopVideo()
		return "ok", nil
	default:
		return "", fmt.Errorf("%w: videomode wants on|off (or empty to read)", alpacadev.ErrInvalidValue)
	}
}

// actionFPSPercent gets/sets the FPS-percent (bandwidth-overload) throttle the readout HMAX/
// line-time math derates by. Empty params returns the live effective value (the link-dependent
// default — 100 on USB3, 40 on USB2 — until the client sets one); an integer 40..100 sets it.
// The throttle takes effect on the next SetROI/SetExposure, so a running video stream is re-armed
// to apply it immediately; single-shot picks it up on the next StartExposure. The value is stored
// and re-applied after a reconnect.
func (c *PureASICamera) actionFPSPercent(params string) (string, error) {
	if !c.hwPresent.Load() {
		return "", alpacadev.ErrNotConnected
	}
	params = strings.TrimSpace(params)
	if params == "" { // query the live effective throttle (link-dependent default when never set)
		c.mu.Lock()
		pct := c.cam.FPSPercent()
		c.mu.Unlock()
		return strconv.Itoa(pct), nil
	}
	pct, err := strconv.Atoi(params)
	if err != nil {
		return "", fmt.Errorf("%w: fpspercent wants an integer 40..100", alpacadev.ErrInvalidValue)
	}
	if pct < 40 || pct > 100 {
		return "", fmt.Errorf("%w: fpspercent %d out of range 40..100", alpacadev.ErrInvalidValue, pct)
	}
	// Serialize the whole set against the video lifecycle so the throttle change and any re-arm are
	// one atomic step — a concurrent videomode toggle can't interleave between the videoOn snapshot
	// and the re-arm (the *Locked helpers run the stop/start bodies under this already-held exposeMu).
	c.exposeMu.Lock()
	defer c.exposeMu.Unlock()
	c.mu.Lock()
	c.fpsPercent = pct
	c.cam.SetFPSPercent(pct)
	videoOn, vidExp := c.videoOn, c.vidExp
	c.mu.Unlock()
	if videoOn { // re-arm so the running stream re-runs SetROI/SetExposure with the new throttle
		c.stopVideoLocked()
		if err := c.startVideoLocked(vidExp); err != nil {
			// The device errored while re-arming (so the stream can't run regardless); the throttle
			// is applied and persisted and will take on the next arm/reconnect. Surface the failure.
			return "", err
		}
	}
	return strconv.Itoa(pct), nil
}

// startVideo arms the sensor for free-run at the given exposure and launches the drain goroutine.
// Idempotent if already running at the same exposure. Holds exposeMu so it can't race a
// StartExposure/AbortExposure lifecycle, and quiesces any in-flight single-shot first.
func (c *PureASICamera) startVideo(dur float64) error {
	c.exposeMu.Lock()
	defer c.exposeMu.Unlock()
	return c.startVideoLocked(dur)
}

// startVideoLocked is the body of startVideo; the caller must hold exposeMu (e.g. actionFPSPercent,
// which re-arms the stream under exposeMu so the throttle change and the re-arm are one atomic step).
func (c *PureASICamera) startVideoLocked(dur float64) error {
	c.mu.Lock()
	already := c.videoOn && c.vidExp == dur
	c.mu.Unlock()
	if already {
		return nil
	}
	c.stopVideoLocked() // stop a prior stream (e.g. exposure change) before re-arming
	c.mu.Lock()
	if err := c.cam.SetROI(c.startX, c.startY, c.numX, c.numY); err != nil {
		c.mu.Unlock()
		return fmt.Errorf("%w: %v", alpacadev.ErrInvalidValue, err)
	}
	c.startX, c.startY, _, _ = c.cam.ROI() // the start the sensor aligned to (StartX/StartY report it)
	if err := c.cam.SetExposure(time.Duration(dur * float64(time.Second))); err != nil {
		c.mu.Unlock()
		return err
	}
	if err := c.cam.StartVideo(true); err != nil {
		c.mu.Unlock()
		return fmt.Errorf("start video: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.vidCancel = cancel
	c.videoOn = true
	c.vidExp = dur
	c.vidSeq = 0
	c.vidStartX, c.vidStartY, c.vidNumX, c.vidNumY = c.startX, c.startY, c.numX, c.numY
	c.vidBin = c.cam.Binning() // snapshot the armed geometry for change detection
	c.mu.Unlock()
	c.vidWG.Add(1)
	go c.drainVideo(ctx, dur)
	log.Printf("asicam-alpaca: %s video mode ON (exp %.3fs)", c.Label(), dur)
	return nil
}

// stopVideo stops the free-run drain and halts the stream. Safe when not running.
func (c *PureASICamera) stopVideo() {
	c.exposeMu.Lock()
	defer c.exposeMu.Unlock()
	c.stopVideoLocked()
}

// stopVideoLocked is the body of stopVideo; the caller must hold exposeMu.
func (c *PureASICamera) stopVideoLocked() {
	c.mu.Lock()
	if !c.videoOn {
		c.mu.Unlock()
		return
	}
	cancel := c.vidCancel
	c.videoOn = false
	c.vidCancel = nil
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	c.vidWG.Wait()           // join the drain before touching the device again
	_ = c.cam.StopExposure() // halt the sensor stream
	log.Printf("asicam-alpaca: %s video mode OFF", c.Label())
}

// drainVideo runs for the lifetime of video mode: it reads every free-run frame back-to-back (no
// re-arm) into vidFrame, bumping vidSeq, so the sensor never backs up the FX3 and StartExposure
// always has a fresh frame to hand out. A device-wedge ends the drain and signals a reset.
func (c *PureASICamera) drainVideo(ctx context.Context, dur float64) {
	defer c.vidWG.Done()
	w, h := c.numX, c.numY
	bpp := c.cam.OutputDepth()
	buf := make([]byte, c.cam.FrameBytes())
	for ctx.Err() == nil {
		n, err := c.cam.ReadFrame(buf, false)
		if err != nil {
			if errors.Is(err, astrocam.ErrDeviceWedged) {
				c.needsReconnect.Store(true) // manageHardware re-acquires
				return
			}
			continue // transient short/stall — the next read recovers
		}
		if n < len(buf) {
			continue
		}
		c.mu.Lock()
		if cap(c.vidFrame) < n {
			c.vidFrame = make([]byte, n)
		}
		c.vidFrame = c.vidFrame[:n]
		copy(c.vidFrame, buf[:n])
		c.vidW, c.vidH, c.vidBpp = w, h, bpp
		c.vidSeq++
		c.mu.Unlock()
	}
}

// waitVideoFrame is the video-mode replacement for runExposure: it waits for the drain to deliver
// a frame newer than `want` (one captured after its StartExposure), publishes it, and completes
// the op. Bounded so a stalled stream fails the op instead of hanging forever.
func (c *PureASICamera) waitVideoFrame(want uint64, dur float64) {
	defer c.exposeWG.Done()
	deadline := time.Now().Add(2*time.Duration(dur*float64(time.Second)) + readoutGrace)
	for {
		c.mu.Lock()
		seq, on := c.vidSeq, c.videoOn
		if on && seq >= want {
			n := len(c.vidFrame)
			if cap(c.frame) < n {
				c.frame = make([]byte, n)
			}
			c.frame = c.frame[:n]
			copy(c.frame, c.vidFrame)
			c.frameW, c.frameH, c.frameBpp = c.vidW, c.vidH, c.vidBpp
			c.mu.Unlock()
			c.exposeOp.Complete()
			return
		}
		c.mu.Unlock()
		if !on {
			c.exposeOp.Fail(fmt.Errorf("video mode stopped before a frame arrived"))
			return
		}
		if time.Now().After(deadline) {
			c.exposeOp.Fail(fmt.Errorf("video stream delivered no frame within %s", time.Since(deadline)))
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func (c *PureASICamera) StartExposure(duration float64, light bool) error {
	if duration < 0 {
		return alpacadev.ErrInvalidValue
	}
	// Video mode: wait for the next free-running frame (one captured after this call) instead of
	// arming a single shot. A changed exposure restarts the stream at the new rate.
	curBin := c.cam.Binning()
	c.mu.Lock()
	vid := c.videoOn
	// Restart the stream if exposure, ROI, or binning changed since it was armed (gain stays live).
	changed := duration != c.vidExp ||
		c.startX != c.vidStartX || c.startY != c.vidStartY ||
		c.numX != c.vidNumX || c.numY != c.vidNumY || curBin != c.vidBin
	c.mu.Unlock()
	if vid {
		if changed {
			if err := c.startVideo(duration); err != nil {
				return err
			}
		}
		c.mu.Lock()
		want := c.vidSeq + 1
		c.lastDuration = duration
		c.lastStart = time.Now().UTC()
		c.haveLast = true
		c.frame = nil
		c.mu.Unlock()
		c.exposeMu.Lock()
		c.exposeOp.Begin()
		c.exposeWG.Add(1)
		go c.waitVideoFrame(want, duration)
		c.exposeMu.Unlock()
		return nil
	}
	c.mu.Lock()
	// Apply the ROI window now that all four of StartX/StartY/NumX/NumY are known. asicam
	// validates the composite window against the sensor bounds; an out-of-range window is a
	// client value error (ASCOM InvalidValue), not a driver fault.
	if err := c.cam.SetROI(c.startX, c.startY, c.numX, c.numY); err != nil {
		c.mu.Unlock()
		return fmt.Errorf("%w: %v", alpacadev.ErrInvalidValue, err)
	}
	c.startX, c.startY, _, _ = c.cam.ROI() // the start the sensor aligned to (StartX/StartY report it)
	if err := c.cam.SetExposure(time.Duration(duration * float64(time.Second))); err != nil {
		c.mu.Unlock()
		return err
	}
	c.lastDuration = duration
	c.lastStart = time.Now().UTC()
	c.haveLast = true
	c.frame = nil
	c.mu.Unlock()

	c.exposeMu.Lock()
	c.exposeOp.Begin()
	c.exposeWG.Add(1)
	go c.runExposure(light)
	c.exposeMu.Unlock()
	return nil
}

// readoutGrace bounds how long past the (host-timed) integration the frame readout may run before
// the watchdog treats it as hung. A real USB readout of a full frame is sub-second, so this only
// trips on a genuine stall (notably the USB2 174's worker, which can block forever on a stalled
// bulk read). The deadline is 2×exposure + this.
const readoutGrace = 20 * time.Second

// runExposure arms then reads one frame. astrocam.GetDataAfterExp blocks for the whole host-timed
// integration + readout, so this whole call lives in its own goroutine and never holds the adapter
// mu across it (other handlers — temperature, cooler power — keep working).
//
// A watchdog bounds the readout: if it overruns the deadline, StopExposure unblocks the in-flight
// bulk read so GetDataAfterExp returns and the op fails. Without it, a stalled read pins exposeOp
// at OpBusy forever (CameraState=Exposing, Busy()=true), rejecting every later StartExposure with
// InvalidOperation (1035) until the camera is manually aborted.
func (c *PureASICamera) runExposure(light bool) {
	defer c.exposeWG.Done()
	c.mu.Lock()
	w, h := c.numX, c.numY
	sx, sy := c.startX, c.startY
	dur := c.lastDuration
	c.mu.Unlock()

	bpp := c.cam.OutputDepth()
	buf := make([]byte, c.cam.FrameBytes())
	t0 := time.Now()
	if err := c.cam.StartExposure(light); err != nil {
		c.exposeOp.Fail(fmt.Errorf("arm: %w", err))
		return
	}
	tArm := time.Now()

	// Watchdog: preempt a hung readout after a generous deadline so the op can fail and recover.
	deadline := 2*time.Duration(dur*float64(time.Second)) + readoutGrace
	done := make(chan struct{})
	var timedOut atomic.Bool
	go func() {
		select {
		case <-done:
		case <-time.After(deadline):
			timedOut.Store(true)
			_ = c.cam.StopExposure() // unblock the stalled bulk read so GetDataAfterExp returns
		}
	}()

	n, err := c.cam.GetDataAfterExp(buf)
	close(done)
	if camDebug {
		log.Printf("asicam-alpaca: %s exposure arm=%.0fms read=%.0fms total=%.0fms n=%d/%d",
			c.ID, ms(tArm.Sub(t0)), ms(time.Since(tArm)), ms(time.Since(t0)), n, c.cam.FrameBytes())
	}

	if timedOut.Load() {
		c.exposeOp.Fail(fmt.Errorf("readout timed out after %s — camera not delivering frame", deadline))
		return
	}
	if err != nil {
		if errors.Is(err, astrocam.ErrDeviceWedged) {
			c.needsReconnect.Store(true) // driver-confirmed dead — manageHardware re-acquires
		}
		c.exposeOp.Fail(fmt.Errorf("readout: %w", err))
		return
	}
	c.mu.Lock()
	fix := c.fixDefects
	c.mu.Unlock()
	if fix {
		c.applyDefects(buf[:n], w, h, sx, sy, bpp)
	}
	c.mu.Lock()
	c.frame = buf[:n]
	c.frameW, c.frameH = w, h
	c.frameBpp = bpp
	c.mu.Unlock()
	c.exposeOp.Complete()
}

// applyDefects applies the factory hot-pixel map to a full-frame RAW16 frame in place,
// neighbour-averaging each hot/dead pixel. Full-frame RAW16 only (the map is full-sensor), so
// ROI / RAW8 / binned frames are left raw. The map is read from SPI flash once and cached. Gated
// by SetFixDefects.
func (c *PureASICamera) applyDefects(frame []byte, w, h, sx, sy, bpp int) {
	info := c.cam.Info()
	if bpp != 2 || sx != 0 || sy != 0 || w != info.MaxWidth || h != info.MaxHeight {
		return // map is full-sensor RAW16 only
	}
	c.mu.Lock()
	dm := c.defectMap
	c.mu.Unlock()
	if dm == nil {
		loaded, err := c.cam.LoadDefectMap(info.MaxWidth, info.MaxHeight)
		if err != nil {
			if camDebug {
				log.Printf("asicam-alpaca: %s fixdefects: load map: %v (frame left raw)", c.Label(), err)
			}
			return
		}
		c.mu.Lock()
		c.defectMap, dm = loaded, loaded
		c.mu.Unlock()
	}
	dm.ApplyRAW16(frame)
}

// AbortExposure stops the in-flight readout and waits for its goroutine to fully exit before
// returning, so the next StartExposure can never be clobbered by a late readout publishing onto
// its Op. StopExposure unblocks the bulk read (the watchdog is the backstop if it doesn't); the
// joined readout's terminal Complete/Fail is then cleared by Reset. Safe when nothing is exposing
// (exposeWG.Wait returns immediately). Must not be called holding c.mu — runExposure takes c.mu as
// it finishes, so joining it under that lock would deadlock.
func (c *PureASICamera) AbortExposure() error {
	c.exposeMu.Lock()
	defer c.exposeMu.Unlock()
	c.mu.Lock()
	vid := c.videoOn
	c.mu.Unlock()
	if !vid {
		_ = c.cam.StopExposure() // unblock the in-flight bulk read so runExposure returns
	}
	// In video mode the in-flight op is a waitVideoFrame, not a USB read; it completes on the next
	// streamed frame, so the join below returns without halting the stream.
	c.exposeWG.Wait()  // join it: no late readout can publish after this
	c.exposeOp.Reset() // clear the terminal state the joined readout left
	return nil
}

func (c *PureASICamera) StopExposure() error { return c.AbortExposure() }

func (c *PureASICamera) ImageReady() bool { return c.exposeOp.State() == alpacadev.OpDone }

func (c *PureASICamera) CameraState() alpacadev.CameraState {
	switch c.exposeOp.State() {
	case alpacadev.OpBusy:
		return alpacadev.CameraExposing
	case alpacadev.OpFailed:
		return alpacadev.CameraError
	default:
		return alpacadev.CameraIdle
	}
}

func (c *PureASICamera) PercentCompleted() int {
	switch c.exposeOp.State() {
	case alpacadev.OpDone:
		return 100
	case alpacadev.OpBusy:
		c.mu.Lock()
		dur, start := c.lastDuration, c.lastStart
		c.mu.Unlock()
		if dur <= 0 {
			return 0
		}
		pct := int(time.Since(start).Seconds() / dur * 100)
		if pct > 100 {
			pct = 100
		}
		return pct
	default:
		return 0
	}
}

func (c *PureASICamera) LastExposureDuration() (float64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.haveLast {
		return 0, alpacadev.ErrValueNotSet
	}
	return c.lastDuration, nil
}

func (c *PureASICamera) LastExposureStartTime() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.haveLast {
		return "", alpacadev.ErrValueNotSet
	}
	return c.lastStart.Format("2006-01-02T15:04:05"), nil
}

// ImageFrame returns the last readout: raw little-endian pixels, transmitted as UInt16 (RAW16)
// or byte (RAW8), presented to clients as Int32 (ASCOM's convention for unsigned camera data).
//
// Orientation: Pixels is the sensor's raster order (row-major) passed straight through, with
// Width/Height in the metadata — identical to the SDK-based asiccd driver. ASCOM
// ImageArray[NumX][NumY] is column-major, so a raster buffer labelled [Width][Height] is the
// transpose of the strict ASCOM order; whether a transpose is needed for correct client
// orientation is unverified and is a shared convention question for the framework's
// EncodeImageBytes, not an asicam-only divergence. The SDK's Flip control defaults to None
// (max 3 = None/Horiz/Vert/Both), and asicam applies no software flip — matching that default.
func (c *PureASICamera) ImageFrame() (alpacadev.ImageFrame, error) {
	if c.exposeOp.State() != alpacadev.OpDone {
		return alpacadev.ImageFrame{}, alpacadev.ErrValueNotSet
	}
	c.mu.Lock()
	f, w, h, bpp := c.frame, c.frameW, c.frameH, c.frameBpp
	c.mu.Unlock()
	if f == nil {
		return alpacadev.ImageFrame{}, alpacadev.ErrValueNotSet
	}
	// Transmit as the captured depth: RAW16 → UInt16, RAW8 → byte (both presented as Int32).
	tx := alpacadev.ImgUInt16
	if bpp == 1 {
		tx = alpacadev.ImgByte
	}
	return alpacadev.ImageFrame{
		Rank:                    2,
		Width:                   w,
		Height:                  h,
		ElementType:             alpacadev.ImgInt32,
		TransmissionElementType: tx,
		Pixels:                  f,
	}, nil
}

// --- Readout modes: output bit depth (RAW16 / RAW8) ---

func (c *PureASICamera) ReadoutModes() []string { return []string{"RAW16", "RAW8"} }

func (c *PureASICamera) ReadoutMode() int {
	if c.cam.OutputDepth() == 1 {
		return 1 // RAW8
	}
	return 0 // RAW16
}

func (c *PureASICamera) SetReadoutMode(n int) error {
	switch n {
	case 0:
		return c.cam.SetOutputDepth(2) // RAW16
	case 1:
		return c.cam.SetOutputDepth(1) // RAW8
	default:
		return alpacadev.ErrInvalidValue
	}
}

// --- Cooling ---

func (c *PureASICamera) CanGetCoolerPower() bool    { return c.cam.Cooled() }
func (c *PureASICamera) CanSetCCDTemperature() bool { return c.cam.Cooled() }

func (c *PureASICamera) CCDTemperature() (float64, error) {
	// A non-cooled body (e.g. the ASI174MM Mini guide cam) has no temperature sensor and must
	// not touch USB: the 0xB3 control read would, if polled during a frame readout, land mid-bulk
	// and wedge the USB2 path. Gate on the cooler capability (same predicate as
	// CanGetCoolerPower/CanSetCCDTemperature) so an uncooled camera answers NotImplemented with
	// zero wire traffic.
	if !c.cam.Cooled() {
		return 0, alpacadev.ErrNotImplemented
	}
	t, err := c.cam.Temperature()
	if err != nil {
		return 0, alpacadev.ErrNotImplemented // cooled body but the read failed
	}
	return t, nil
}

// CoolerOn reports the regulation state as the camera holds it: the client's request and the
// loop still running. When the loop retires on its own (thermal I/O failing for
// runMaxConsecFails ticks; astrocam zeroes the TEC and drops the loop) the flag follows, the
// fault is logged once, and the "CoolerFault" Action returns it.
func (c *PureASICamera) CoolerOn() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.coolerOn && c.cam != nil && !c.cam.CoolerOn() {
		c.coolerOn = false
		if err := c.cam.CoolerFault(); err != nil {
			log.Printf("asicam-alpaca: %s: cooling loop stopped: %v", c.Label(), err)
		}
	}
	return c.coolerOn
}

func (c *PureASICamera) SetCoolerOn(on bool) error {
	if !c.cam.Cooled() {
		return alpacadev.ErrNotImplemented
	}
	c.mu.Lock()
	setp := c.setpoint
	cam := c.cam
	c.coolerOn = on
	c.mu.Unlock()

	if on {
		cfg := astrocam.DefaultCoolerConfig()
		cfg.RampRate = 6 // controlled cooldown rate (°C/min)
		return cam.EnableCooling(nil, setp, cfg)
	}
	cam.DisableCooling()
	th := cam.HardwareThermal()
	_ = th.SetTECPower(0)
	_ = th.SetFan(false)
	return nil
}

func (c *PureASICamera) CoolerPower() (float64, error) { return c.cam.CoolerPower(), nil }

func (c *PureASICamera) SetCCDTemperature() (float64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.setpoint, nil
}

func (c *PureASICamera) SetSetCCDTemperature(t float64) error {
	c.mu.Lock()
	on := c.coolerOn
	cam := c.cam
	c.setpoint = t
	c.mu.Unlock()
	if on {
		cam.SetTargetTemp(t)
	}
	return nil
}

// --- Guiding (ST4) ---

func (c *PureASICamera) CanPulseGuide() bool { return c.cam.ST4() }

func (c *PureASICamera) IsPulseGuiding() bool { c.mu.Lock(); defer c.mu.Unlock(); return c.pulsing }

func (c *PureASICamera) PulseGuide(dir alpacadev.GuideDirection, durationMs int) error {
	if !c.cam.ST4() {
		return alpacadev.ErrNotImplemented
	}
	c.mu.Lock()
	c.pulsing = true
	c.mu.Unlock()
	if err := c.cam.PulseGuideOn(astrocam.GuideDir(dir)); err != nil { // enums share order with ASCOM
		c.mu.Lock()
		c.pulsing = false
		c.mu.Unlock()
		return err
	}
	go func() {
		time.Sleep(time.Duration(durationMs) * time.Millisecond)
		_ = c.cam.PulseGuideOff(astrocam.GuideDir(dir))
		c.mu.Lock()
		c.pulsing = false
		c.mu.Unlock()
	}()
	return nil
}
