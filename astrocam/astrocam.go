package driver

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mikefsq/astrocam"
	alpacadev "github.com/mikefsq/goalpaca/server"
)

// camDebug logs per-exposure arm/read/total timing to the console. Off by default; set
// ASICAM_DEBUG=1 (or true/yes/on) to diagnose frame-turnaround (e.g. the 174 free-run latency).
var camDebug = func() bool {
	switch strings.ToLower(os.Getenv("ASICAM_DEBUG")) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}()

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

// PureASICamera adapts the pure-Go astrocam.Camera to the alpacadev.Camera + Hardware
// interfaces. It is the cgo-SDK-free USB control transfers over IOKit/usbfs/WinUSB).
//
// astrocam.Camera is internally concurrency-safe (per-transport control-transfer serialization
// + a capture-state mutex + an owned TEC goroutine), so several Alpaca HTTP handlers can hit
// it at once. The adapter's own mu guards only adapter-side state (geometry, the exposure
// result, flags); it is never held across a blocking readout or a cooling state change.
type PureASICamera struct {
	alpacadev.BaseCamera

	index      int
	wantSerial string // if set, bind only the camera with this serial (hex)

	// Injection seams (default to the real USB paths; the stub-transport e2e tests
	// override them): openDev opens — but does not Init — the target camera; aliveFn
	// reports whether it is still present.
	openDev func() (*astrocam.Camera, astrocam.DeviceInfo, error)
	aliveFn func() bool

	cam *astrocam.Camera
	loc uint32 // USB location of the open camera (non-perturbing liveness check via Enumerate)

	mu        sync.Mutex
	hwPresent atomic.Bool // camera open; read lock-free by Connected()
	exposeOp  alpacadev.Op
	exposeWG  sync.WaitGroup // tracks the in-flight runExposure goroutine, so teardown joins it before Close frees the handle
	frame     []byte         // last readout, raw little-endian
	frameW    int
	frameH    int
	frameBpp  int // bytes/pixel of the last readout (2 = RAW16, 1 = RAW8)
	aborted   bool

	lastDuration float64
	lastStart    time.Time
	haveLast     bool

	pulsing bool

	// Desired capture geometry (applied to asicam at StartExposure).
	startX, startY int
	numX, numY     int

	// Cached ranges (filled at acquire).
	gainMin, gainMax     int
	offsetMin, offsetMax int
	offsetOK             bool
	expMinSec, expMaxSec float64

	coolerOn bool
	setpoint float64 // CCD temperature setpoint °C
}

// NewPureASICamera creates the driver for a camera selected by serial (preferred, stable) or,
// if serial is "", by enumeration index. The UniqueID is known up front from the serial, so
// the device registers with a stable identity even before the camera is plugged in.
func NewPureASICamera(index int, serial string) *PureASICamera {
	c := &PureASICamera{index: index, wantSerial: strings.ToLower(serial)}
	c.openDev = c.openReal
	c.aliveFn = c.stillPresent
	c.Version = "0.1.0"
	c.Info = "asicam-alpaca — ZWO ASI Alpaca driver over the pure-Go asicam (no ZWO SDK)"
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

// --- Hardware lifecycle ---

// Open starts the hardware-management goroutine and returns immediately, so the Alpaca
// endpoint comes up with or without a camera attached.
func (c *PureASICamera) Open(ctx context.Context) error {
	go alpacadev.Supervise(ctx, c.ID, func() { c.manageHardware(ctx) })
	return nil
}

// Close releases the camera on graceful shutdown only (cam.Close stops cooling + USB).
func (c *PureASICamera) Close(ctx context.Context) error {
	c.teardown()
	return nil
}

// teardown aborts any in-flight exposure and waits for the readout goroutine to exit
// before closing the camera, so no control transfer outlives the freed USB handle (the
// use-after-free that crashed mid-readout on an unplug/reconnect). It must be called
// WITHOUT holding c.mu: runExposure takes c.mu as it finishes, so joining it under the
// lock would deadlock. cam.Close is idempotent, so this is safe to call more than once.
func (c *PureASICamera) teardown() {
	if c.hwPresent.Load() {
		_ = c.AbortExposure() // signal the in-flight readout to unwind (sets aborted + StopExposure)
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
func (c *PureASICamera) manageHardware(ctx context.Context) {
	const maxMisses = 3
	misses := 0
	for ctx.Err() == nil {
		if !c.hwPresent.Load() {
			if c.tryAcquire() {
				misses = 0
				log.Printf("asicam-alpaca: camera %s acquired", c.ID)
			} else {
				sleepCtx(ctx, 3*time.Second)
			}
			continue
		}
		// Liveness via the aliveFn seam — by default Enumerate (reads the OS USB registry,
		// never touches the open camera; non-perturbing). Tests override it.
		if c.aliveFn() {
			misses = 0
			sleepCtx(ctx, 2*time.Second)
			continue
		}
		misses++
		if misses < maxMisses {
			sleepCtx(ctx, 2*time.Second)
			continue
		}
		log.Printf("asicam-alpaca: camera %s unplugged (x%d); re-acquiring", c.ID, misses)
		c.teardown()
		misses = 0
	}
}

// stillPresent reports whether our camera's USB location is still on the bus.
func (c *PureASICamera) stillPresent() bool {
	devs, err := astrocam.Enumerate()
	if err != nil {
		return true // can't tell — don't tear down on a query error
	}
	c.mu.Lock()
	loc := c.loc
	c.mu.Unlock()
	for _, d := range devs {
		if d.Location == loc {
			return true
		}
	}
	return false
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
	_ = cam.SetROI(0, 0, info.MaxWidth, info.MaxHeight) // full frame
	_ = cam.SetGain(0)
	_ = cam.SetExposure(time.Second)

	gmin, gmax := cam.GainRange()
	emin, emax := cam.ExposureRange()

	c.mu.Lock()
	defer c.mu.Unlock()
	c.cam = cam
	c.loc = d.Location
	c.coolerOn = false
	c.startX, c.startY = 0, 0
	c.numX, c.numY = info.MaxWidth, info.MaxHeight
	c.gainMin, c.gainMax = gmin, gmax
	c.offsetMin, c.offsetMax, _, c.offsetOK = cam.OffsetRange()
	c.expMinSec, c.expMaxSec = emin.Seconds(), emax.Seconds()
	c.DevName = cam.Name()
	c.Desc = fmt.Sprintf("ZWO %s (%dx%d, %.2fµm) [pure-Go asicam]", cam.Name(), info.MaxWidth, info.MaxHeight, info.PixelUm)
	if c.wantSerial == "" && strings.Trim(serialHex, "0") != "" {
		c.ID = "ASI-" + serialHex
	}
	c.hwPresent.Store(true)
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

// --- Binning: symmetric, factors advertised from the sensor's Bins (hardware capability).
// SetBinX drives astrocam.SetBinning; a factor the sensor lists but whose readout geometry
// isn't decoded yet fails later at SetROI (StartExposure) with InvalidValue, not here. ---

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

func (c *PureASICamera) StartExposure(duration float64, light bool) error {
	if duration < 0 {
		return alpacadev.ErrInvalidValue
	}
	c.mu.Lock()
	// Apply the ROI window now that all four of StartX/StartY/NumX/NumY are known. asicam
	// validates the composite window against the sensor bounds; an out-of-range window is a
	// client value error (ASCOM InvalidValue), not a driver fault. Surface it instead of
	// silently exposing the previous geometry — otherwise FrameBytes would disagree with
	// what the client thinks it requested.
	if err := c.cam.SetROI(c.startX, c.startY, c.numX, c.numY); err != nil {
		c.mu.Unlock()
		return fmt.Errorf("%w: %v", alpacadev.ErrInvalidValue, err)
	}
	if err := c.cam.SetExposure(time.Duration(duration * float64(time.Second))); err != nil {
		c.mu.Unlock()
		return err
	}
	c.lastDuration = duration
	c.lastStart = time.Now().UTC()
	c.haveLast = true
	c.frame = nil
	c.aborted = false
	c.mu.Unlock()

	c.exposeOp.Begin()
	c.exposeWG.Add(1)
	go c.runExposure(light)
	return nil
}

// runExposure arms then reads one frame. astrocam.GetDataAfterExp blocks for the whole
// host-timed integration + readout, so this whole call lives in its own goroutine and never
// holds the adapter mu across it (other handlers — temperature, cooler power — keep working).
func (c *PureASICamera) runExposure(light bool) {
	defer c.exposeWG.Done()
	c.mu.Lock()
	w, h := c.numX, c.numY
	c.mu.Unlock()

	bpp := c.cam.OutputDepth()
	buf := make([]byte, c.cam.FrameBytes())
	t0 := time.Now()
	if err := c.cam.StartExposure(light); err != nil {
		c.exposeOp.Fail(fmt.Errorf("arm: %w", err))
		return
	}
	tArm := time.Now()
	n, err := c.cam.GetDataAfterExp(buf)
	if camDebug {
		log.Printf("asicam-alpaca: %s exposure arm=%.0fms read=%.0fms total=%.0fms n=%d/%d",
			c.ID, ms(tArm.Sub(t0)), ms(time.Since(tArm)), ms(time.Since(t0)), n, c.cam.FrameBytes())
	}

	c.mu.Lock()
	aborted := c.aborted
	c.mu.Unlock()
	if aborted {
		c.exposeOp.Reset()
		return
	}
	if err != nil {
		c.exposeOp.Fail(fmt.Errorf("readout: %w", err))
		return
	}
	c.mu.Lock()
	c.frame = buf[:n]
	c.frameW, c.frameH = w, h
	c.frameBpp = bpp
	c.mu.Unlock()
	c.exposeOp.Complete()
}

func (c *PureASICamera) AbortExposure() error {
	c.mu.Lock()
	c.aborted = true
	c.mu.Unlock()
	_ = c.cam.StopExposure()
	c.exposeOp.Reset()
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
// ORIENTATION: Pixels is the sensor's RASTER order (row-major: for y, for x) passed straight
// through, with Width/Height in the metadata — IDENTICAL to the SDK-based asiccd reference
// driver, so the two behave the same in any client. NOTE: ASCOM ImageArray[NumX][NumY] is
// column-major (x outer), so a raster buffer labelled [Width][Height] is the transpose of the
// strict ASCOM order. Whether a transpose is needed for correct NINA orientation is UNVERIFIED
// (no NINA + camera here) and is a SHARED convention question — if it is, the fix belongs in the
// framework's EncodeImageBytes for every driver, not an asicam-only divergence from asiccd. The
// SDK's Flip control defaults to None (GetCtrllCaps: max 3 = None/Horiz/Vert/Both), and asicam
// applies no software flip — matching that default.
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
	t, err := c.cam.Temperature()
	if err != nil {
		return 0, alpacadev.ErrNotImplemented // non-cooled body has no temperature sensor
	}
	return t, nil
}

func (c *PureASICamera) CoolerOn() bool { c.mu.Lock(); defer c.mu.Unlock(); return c.coolerOn }

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
		cfg.RampRate = 6 // gentle controlled cooldown by default (°C/min)
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
