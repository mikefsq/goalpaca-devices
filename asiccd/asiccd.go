package driver

import (
	"context"
	"encoding/hex"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	alpacadev "github.com/mikefsq/goalpaca/server"
	goasi "github.com/mikefsq/goasi/ccd"
)

// ASICamera adapts a goasi camera to the alpacadev.Camera + Hardware interfaces.
//
// All goasi (ASI SDK) access is serialized by mu — the SDK is not safe for concurrent per-camera
// calls, and Alpaca HTTP handlers run concurrently. mu is never held across a sleep (the exposure
// poll locks per-call only).
type ASICamera struct {
	alpacadev.BaseCamera

	index      int
	wantSerial string // if set, bind only the camera with this serial (hex)
	cam        *goasi.GoAsiCamera

	mu        sync.Mutex
	hwPresent atomic.Bool // camera attached and SDK handle open; read lock-free by Connected()
	exposeOp  alpacadev.Op
	frame     *goasi.ExposureFrame
	aborted   bool

	lastDuration float64
	lastStart    time.Time
	haveLast     bool

	pulsing bool

	// Desired capture geometry (applied to the SDK at StartExposure).
	binning        int
	startX, startY int
	numX, numY     int

	// Cached control ranges (filled at Open).
	gainMin, gainMax     int
	offsetMin, offsetMax int
	expMinSec, expMaxSec float64

	coolerOn bool
}

// NewASICamera creates the driver for a camera selected by serial (preferred, stable) or, if
// serial is "", by enumeration index. The UniqueID is known up front from the serial, so the
// device registers with a stable identity before the camera is plugged in.
func NewASICamera(index int, serial string) *ASICamera {
	c := &ASICamera{index: index, wantSerial: strings.ToLower(serial)}
	c.Version = "0.1.0"
	c.Info = "asialpaca — ZWO ASI Alpaca driver over goasi"
	c.IfaceVer = alpacadev.InterfaceVersionCamera // ICameraV4 (Platform 7)
	if c.wantSerial != "" {
		c.ID = "ASI-" + c.wantSerial
		c.DevName = "ASI Camera " + c.wantSerial
	} else {
		c.ID = fmt.Sprintf("ASI-cam%d", index) // provisional; adopts serial on first open
		c.DevName = fmt.Sprintf("ASI Camera %d", index)
	}
	return c
}

// --- Hardware lifecycle (persistent owner) ---

// Open starts the hardware-management goroutine and returns immediately, so the Alpaca server
// comes up with or without a camera attached. The goroutine acquires the target camera when it
// appears (matching the serial), monitors it, and re-acquires after an unplug, never exiting the
// process.
func (c *ASICamera) Open(ctx context.Context) error {
	go alpacadev.Supervise(ctx, c.ID, func() { c.manageHardware(ctx) })
	return nil
}

// Close releases the SDK on graceful shutdown only.
func (c *ASICamera) Close(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cam != nil && c.hwPresent.Load() {
		c.cam.ASIStopExposure()
		c.cam.ASICloseCamera()
		c.hwPresent.Store(false)
	}
	return nil
}

// Connected reports hardware presence: the Alpaca logical connection IS the hardware state, true
// exactly when the camera is attached and its SDK handle is open. Read lock-free so it never
// blocks behind a long readout holding mu.
func (c *ASICamera) Connected() bool { return c.hwPresent.Load() }

// Disconnect is a logical no-op: the driver owns the hardware for the life of the process (a
// client disconnect must not reset the TEC cooler).
func (c *ASICamera) Disconnect(ctx context.Context) error { return nil }

// Busy reports a transitory state in which mutating writes must be rejected. Reads the driver-side
// exposure Op (no SDK/USB call), so it never contends with mu.
func (c *ASICamera) Busy() bool { return c.exposeOp.State() == alpacadev.OpBusy }

// manageHardware acquires, monitors, and re-acquires the camera for the life of the process. When
// no camera is present it polls for one; when present it pings the SDK and, on removal, closes the
// handle (so the library's gating returns NotConnected) then resumes acquiring.
func (c *ASICamera) manageHardware(ctx context.Context) {
	// A real unplug must be seen several times in a row before we tear down: a spurious teardown
	// re-opens the camera, which resets the TEC cooler power to 0 (the SDK then ramps it back over
	// ~10 min), so a transient USB hiccup must not trigger one. ~3 misses ≈ 6 s.
	const maxMisses = 3
	misses := 0

	for ctx.Err() == nil {
		present := c.hwPresent.Load()

		if !present {
			if c.tryAcquire() {
				misses = 0
				log.Printf("asialpaca: camera %s acquired", c.ID)
			} else {
				sleepCtx(ctx, 3*time.Second)
			}
			continue
		}

		// Liveness probe. ASIGetControlValue(ASI_TEMPERATURE) is unusable here: the SDK caches
		// temperature on a background thread, so it keeps returning success after the camera is
		// gone. ASIGetExpStatus does a live USB read (the exposure-poll call), so its error code
		// reports ASI_ERROR_CAMERA_REMOVED. Do not re-enumerate the bus here — that could perturb
		// the open, cooling camera. During an exposure this returns ASI_EXP_WORKING with rc==0, so
		// it is not mistaken for a miss.
		c.mu.Lock()
		_, rc := c.cam.ASIGetExpStatusRC()
		c.mu.Unlock()
		if rc != 0 {
			misses++
			if misses < maxMisses {
				sleepCtx(ctx, 2*time.Second)
				continue
			}
			log.Printf("asialpaca: camera %s unplugged (rc=%d x%d); re-acquiring", c.ID, rc, misses)
			c.mu.Lock()
			c.cam.ASICloseCamera()
			c.hwPresent.Store(false) // Connected() follows this; gate returns NotConnected
			c.mu.Unlock()
			misses = 0
			continue
		}
		misses = 0
		sleepCtx(ctx, 2*time.Second)
	}
}

// tryAcquire scans connected cameras for the target (by serial, else the configured index),
// opens+initializes it, and configures defaults. Returns true once the camera is open and ready.
func (c *ASICamera) tryAcquire() bool {
	n := goasi.ASIGetNumOfConnectedCameras()
	for i := 0; i < n; i++ {
		if c.wantSerial == "" && i != c.index {
			continue
		}
		cam := &goasi.GoAsiCamera{CameraID: i}
		if cam.ASIOpenCamera() != 0 {
			continue
		}
		if cam.ASIInitCamera() != 0 {
			cam.ASICloseCamera()
			continue
		}
		sn := hex.EncodeToString([]byte(cam.ASIGetSerialNumber()))
		if c.wantSerial != "" && !strings.EqualFold(sn, c.wantSerial) {
			cam.ASICloseCamera()
			continue
		}
		c.configureOpened(cam, sn)
		return true
	}
	return false
}

// configureOpened applies capture defaults to a freshly opened camera and
// publishes it as the live handle.
func (c *ASICamera) configureOpened(cam *goasi.GoAsiCamera, serialHex string) {
	cam.ASIGetCameraProperty() // fills CameraInfo
	cam.SensibleDefaults()     // full-frame RAW16, exposure/gain/bandwidth defaults
	cam.SetTECState(0)         // cooler off; client owns the setpoint
	info := cam.CameraInfo

	c.mu.Lock()
	defer c.mu.Unlock()
	c.cam = cam
	c.coolerOn = false
	c.binning = 1
	c.startX, c.startY = 0, 0
	c.numX, c.numY = info.MaxWidth, info.MaxHeight
	c.loadControlCaps()
	c.DevName = info.Name
	c.Desc = fmt.Sprintf("ZWO %s (%dx%d, %.2fum)", info.Name, info.MaxWidth, info.MaxHeight, info.PixelSize)
	if c.wantSerial == "" && strings.Trim(serialHex, "0") != "" {
		c.ID = "ASI-" + serialHex // adopt the real serial when not pinned by flag
	}
	c.hwPresent.Store(true)
}

// Connect is the client's presence handshake: it succeeds iff the hardware is attached
// (Connected ≡ hwPresent). It does not open hardware — the driver already owns it — so it is a
// check, not a state change.
func (c *ASICamera) Connect(ctx context.Context) error {
	if !c.hwPresent.Load() {
		return alpacadev.ErrNotConnected
	}
	return nil
}

func sleepCtx(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// loadControlCaps caches gain/offset/exposure ranges from the SDK control caps.
func (c *ASICamera) loadControlCaps() {
	c.cam.ASIGetNumOfControls()
	for i := 0; i < c.cam.NControls; i++ {
		rc, caps := c.cam.ASIGetControlCaps(i)
		if rc != 0 {
			continue
		}
		switch caps.ASI_Control_Type {
		case goasi.ASI_GAIN:
			c.gainMin, c.gainMax = caps.MinValue, caps.MaxValue
		case goasi.ASI_OFFSET:
			c.offsetMin, c.offsetMax = caps.MinValue, caps.MaxValue
		case goasi.ASI_EXPOSURE:
			c.expMinSec = float64(caps.MinValue) / 1e6 // SDK exposure is microseconds
			c.expMaxSec = float64(caps.MaxValue) / 1e6
		}
	}
}

// --- Geometry / description ---

func (c *ASICamera) CameraXSize() int         { return c.cam.CameraInfo.MaxWidth }
func (c *ASICamera) CameraYSize() int         { return c.cam.CameraInfo.MaxHeight }
func (c *ASICamera) PixelSizeX() float64      { return c.cam.CameraInfo.PixelSize }
func (c *ASICamera) PixelSizeY() float64      { return c.cam.CameraInfo.PixelSize }
func (c *ASICamera) ElectronsPerADU() float64 { return c.cam.CameraInfo.ElecPerADU }
func (c *ASICamera) SensorName() string       { return c.cam.CameraInfo.Name }
func (c *ASICamera) HasShutter() bool         { return c.cam.CameraInfo.MechanicalShutter }

// MaxADU: RAW16 data is unsigned 16-bit.
func (c *ASICamera) MaxADU() int { return 65535 }

func (c *ASICamera) SensorType() alpacadev.SensorType {
	if c.cam.CameraInfo.IsColorCam {
		return alpacadev.SensorRGGB // RAW16 Bayer; client debayers
	}
	return alpacadev.SensorMonochrome
}

// --- Binning (symmetric only) ---

func (c *ASICamera) BinX() int { return c.binning }
func (c *ASICamera) BinY() int { return c.binning }

func (c *ASICamera) MaxBinX() int { return c.maxBin() }
func (c *ASICamera) MaxBinY() int { return c.maxBin() }

func (c *ASICamera) maxBin() int {
	max := 1
	for _, b := range c.cam.CameraInfo.SupportedBins {
		if b > max {
			max = b
		}
	}
	return max
}

func (c *ASICamera) SetBinX(n int) error {
	if n < 1 || n > c.maxBin() {
		return alpacadev.ErrInvalidValue
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.binning = n
	// Reset to full frame at the new binning (ASCOM clients then set ROI).
	c.startX, c.startY = 0, 0
	c.numX = c.cam.CameraInfo.MaxWidth / n
	c.numY = c.cam.CameraInfo.MaxHeight / n
	return nil
}

// SetBinY: asymmetric binning is unsupported, so Y must equal X.
func (c *ASICamera) SetBinY(n int) error {
	if n != c.binning {
		return alpacadev.ErrInvalidValue
	}
	return nil
}

// --- Subframe (stored; applied at StartExposure) ---

func (c *ASICamera) StartX() int { return c.startX }
func (c *ASICamera) StartY() int { return c.startY }
func (c *ASICamera) NumX() int   { return c.numX }
func (c *ASICamera) NumY() int   { return c.numY }

func (c *ASICamera) SetStartX(n int) error { c.mu.Lock(); c.startX = n; c.mu.Unlock(); return nil }
func (c *ASICamera) SetStartY(n int) error { c.mu.Lock(); c.startY = n; c.mu.Unlock(); return nil }
func (c *ASICamera) SetNumX(n int) error   { c.mu.Lock(); c.numX = n; c.mu.Unlock(); return nil }
func (c *ASICamera) SetNumY(n int) error   { c.mu.Lock(); c.numY = n; c.mu.Unlock(); return nil }

// --- Gain / Offset ---

func (c *ASICamera) Gain() int      { return c.cam.Gain }
func (c *ASICamera) GainMin() int   { return c.gainMin }
func (c *ASICamera) GainMax() int   { return c.gainMax }
func (c *ASICamera) Offset() int    { return c.cam.Offset }
func (c *ASICamera) OffsetMin() int { return c.offsetMin }
func (c *ASICamera) OffsetMax() int { return c.offsetMax }

func (c *ASICamera) SetGain(n int) error {
	if n < c.gainMin || n > c.gainMax {
		return alpacadev.ErrInvalidValue
	}
	c.mu.Lock()
	c.cam.SetGain(n)
	c.mu.Unlock()
	return nil
}

func (c *ASICamera) SetOffset(n int) error {
	if n < c.offsetMin || n > c.offsetMax {
		return alpacadev.ErrInvalidValue
	}
	c.mu.Lock()
	c.cam.SetOffset(n)
	c.mu.Unlock()
	return nil
}

// --- Exposure (async) ---

func (c *ASICamera) ExposureMin() float64        { return c.expMinSec }
func (c *ASICamera) ExposureMax() float64        { return c.expMaxSec }
func (c *ASICamera) ExposureResolution() float64 { return 1e-6 } // 1 microsecond
func (c *ASICamera) CanAbortExposure() bool      { return true }
func (c *ASICamera) CanStopExposure() bool       { return true }

func (c *ASICamera) StartExposure(duration float64, light bool) error {
	if duration < 0 {
		return alpacadev.ErrInvalidValue
	}
	c.mu.Lock()
	// Apply geometry, then exposure.
	c.cam.ASISetROIFormat(c.numX, c.numY, c.binning, c.cam.ImgFormat)
	c.cam.ASISetStartPos(c.startX, c.startY)
	c.cam.SetExposure(time.Duration(duration * float64(time.Second)))
	c.lastDuration = duration
	c.lastStart = time.Now().UTC()
	c.haveLast = true
	c.frame = nil
	c.aborted = false
	c.mu.Unlock()

	c.exposeOp.Begin()
	go c.runExposure(light)
	return nil
}

func (c *ASICamera) runExposure(light bool) {
	isDark := 0
	if !light {
		isDark = 1
	}
	c.mu.Lock()
	c.cam.ASIStartExposure(isDark)
	maxWait := time.Duration(c.lastDuration*float64(time.Second)) + 30*time.Second
	c.mu.Unlock()
	deadline := time.Now().Add(maxWait)

	for {
		c.mu.Lock()
		if c.aborted {
			c.mu.Unlock()
			c.exposeOp.Reset()
			return
		}
		status := c.cam.ASIGetExpStatus()
		c.mu.Unlock()

		switch status {
		case goasi.ASI_EXP_SUCCESS:
			c.mu.Lock()
			rc, f := c.cam.GetExposureBytes()
			c.mu.Unlock()
			if rc != 0 {
				c.exposeOp.Fail(fmt.Errorf("readout failed: rc=%d", rc))
				return
			}
			c.mu.Lock()
			c.frame = &f
			c.mu.Unlock()
			c.exposeOp.Complete()
			return
		case goasi.ASI_EXP_FAILED:
			c.exposeOp.Fail(fmt.Errorf("exposure failed"))
			return
		}
		if time.Now().After(deadline) {
			c.exposeOp.Fail(fmt.Errorf("exposure timed out after %s (camera lost?)", maxWait))
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (c *ASICamera) AbortExposure() error {
	c.mu.Lock()
	c.aborted = true
	c.cam.ASIStopExposure()
	c.mu.Unlock()
	c.exposeOp.Reset()
	return nil
}

func (c *ASICamera) StopExposure() error { return c.AbortExposure() }

func (c *ASICamera) ImageReady() bool { return c.exposeOp.State() == alpacadev.OpDone }

func (c *ASICamera) CameraState() alpacadev.CameraState {
	switch c.exposeOp.State() {
	case alpacadev.OpBusy:
		return alpacadev.CameraExposing
	case alpacadev.OpFailed:
		return alpacadev.CameraError
	default:
		return alpacadev.CameraIdle
	}
}

func (c *ASICamera) PercentCompleted() int {
	if c.exposeOp.State() == alpacadev.OpDone {
		return 100
	}
	if c.exposeOp.State() != alpacadev.OpBusy {
		return 0
	}
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
}

func (c *ASICamera) LastExposureDuration() (float64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.haveLast {
		return 0, alpacadev.ErrValueNotSet
	}
	return c.lastDuration, nil
}

func (c *ASICamera) LastExposureStartTime() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.haveLast {
		return "", alpacadev.ErrValueNotSet
	}
	return c.lastStart.Format("2006-01-02T15:04:05"), nil
}

// ImageFrame returns the last readout as ImageBytes-ready data. RAW16 is transmitted as unsigned
// 16-bit, presented to clients as Int32. Orientation: the SDK buffer is row-major; [x,y] ordering
// unverified against a client.
func (c *ASICamera) ImageFrame() (alpacadev.ImageFrame, error) {
	if c.exposeOp.State() != alpacadev.OpDone {
		return alpacadev.ImageFrame{}, alpacadev.ErrValueNotSet
	}
	c.mu.Lock()
	f := c.frame
	c.mu.Unlock()
	if f == nil {
		return alpacadev.ImageFrame{}, alpacadev.ErrValueNotSet
	}
	if f.ImgFormat != goasi.ASI_IMG_RAW16 {
		return alpacadev.ImageFrame{}, alpacadev.NewError(alpacadev.ErrNumNotImplemented,
			"only RAW16 transport implemented")
	}
	return alpacadev.ImageFrame{
		Rank:                    2,
		Width:                   f.Width,
		Height:                  f.Height,
		ElementType:             alpacadev.ImgInt32,
		TransmissionElementType: alpacadev.ImgUInt16,
		Pixels:                  f.Pixels,
	}, nil
}

// --- Cooling ---

func (c *ASICamera) CanGetCoolerPower() bool    { return c.cam.CameraInfo.IsCoolerCam }
func (c *ASICamera) CanSetCCDTemperature() bool { return c.cam.CameraInfo.IsCoolerCam }

func (c *ASICamera) CCDTemperature() (float64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cam.GetTemp(), nil
}

func (c *ASICamera) CoolerOn() bool { return c.coolerOn }

func (c *ASICamera) SetCoolerOn(on bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if on {
		c.cam.SetTECState(1)
	} else {
		c.cam.SetTECState(0)
	}
	c.coolerOn = on
	return nil
}

func (c *ASICamera) CoolerPower() (float64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return float64(c.cam.GetTECPower()), nil
}

func (c *ASICamera) SetCCDTemperature() (float64, error) {
	return float64(c.cam.TempSetp), nil
}

func (c *ASICamera) SetSetCCDTemperature(t float64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cam.SetTemp(int(t))
	return nil
}

// --- Guiding ---

func (c *ASICamera) CanPulseGuide() bool { return c.cam.CameraInfo.ST4Port }
func (c *ASICamera) IsPulseGuiding() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pulsing
}

func (c *ASICamera) PulseGuide(dir alpacadev.GuideDirection, durationMs int) error {
	if !c.cam.CameraInfo.ST4Port {
		return alpacadev.ErrNotImplemented
	}
	c.mu.Lock()
	c.pulsing = true
	c.cam.ASIPulseGuideOn(int(dir)) // alpacadev and goasi guide enums share order
	c.mu.Unlock()

	go func() {
		time.Sleep(time.Duration(durationMs) * time.Millisecond)
		c.mu.Lock()
		c.cam.ASIPulseGuideOff(int(dir))
		c.pulsing = false
		c.mu.Unlock()
	}()
	return nil
}
