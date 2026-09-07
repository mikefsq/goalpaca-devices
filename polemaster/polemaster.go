// Package driver exposes the QHY PoleMaster as an ASCOM Alpaca camera.
// The polemaster library supplies USB transport and firmware without the QHY SDK.
// Run it through cmd/polemaster or the alpacahurd aggregator.
package driver

import (
	"context"
	"encoding/binary"
	"fmt"
	"log"
	"os"
	"sync"
	"sync/atomic"
	"time"

	alpacadev "github.com/mikefsq/goalpaca/server"
	"github.com/mikefsq/polemaster"
)

var _ alpacadev.Camera = (*PoleMaster)(nil)

// MT9M034 sensor geometry; cooling, shutter, and guiding are not available.
const (
	pixelSizeUm = 3.75
	sensorName  = "MT9M034"
)

// The USB library requires a pixel count divisible by 512 at either depth.
// The firmware overwrites partial pixel packets when it appends the frame marker.
const packetPixels = 512

// The added-delay field is a 24-bit millisecond count. This bounds the request,
// not the accuracy of long exposures: the supplied firmware gates pixel transfer.
const maxAddedMs = 0xffffff

// Retry absent hardware and probe idle hardware at separate intervals.
const (
	acquirePoll = 3 * time.Second
	alivePoll   = 5 * time.Second
)

// PoleMaster adapts the USB camera to the Alpaca server.
//
// The USB camera is not concurrency-safe. runExposure calls Snap without mu;
// callers must respect Busy and avoid concurrent camera mutations. The management
// loop skips probes while busy, and teardown waits for the capture to return.
type PoleMaster struct {
	// Stop management before closing the USB handle.
	stopLoop func(time.Duration)
	alpacadev.BaseCamera

	// Empty uses embedded firmware; otherwise reload this HEX file on acquisition.
	firmware string

	// Tests inject a simulated camera here.
	openDev func() (*polemaster.Camera, error)

	hwPresent atomic.Bool

	mu  sync.Mutex
	cam *polemaster.Camera

	// Requested sensor coordinates; capture reads a legal enclosing window
	// and crops back to this subframe.
	startX, startY, numX, numY int

	depth int // polemaster.Depth8 or polemaster.Depth12

	exposeOp alpacadev.Op
	exposeWG sync.WaitGroup
	aborted  atomic.Bool

	// Completed, cropped pixels in the server's row-major byte format.
	frame          []byte
	frameW, frameH int
	frameBpp       int

	lastDuration float64
	lastStart    time.Time
	haveLast     bool

	// Progress is estimated from elapsed time. expTotal uses three readout
	// periods and does not account for longer integrations or USB delays.
	expStart time.Time
	expTotal time.Duration
	expIntg  time.Duration
}

// NewPoleMaster creates a driver; Open starts hardware acquisition.
// A non-empty firmware path replaces the embedded image on each acquisition.
func NewPoleMaster(firmware string) *PoleMaster {
	p := &PoleMaster{
		firmware: firmware,
		numX:     polemaster.Width,
		numY:     polemaster.Height,
		depth:    polemaster.Depth8,
	}
	p.ID = "QHY-PoleMaster-cam0"
	p.DevName = "QHY PoleMaster"
	p.Desc = "QHY PoleMaster polar-alignment camera"
	p.Info = "polemaster — QHY PoleMaster Alpaca Camera driver over Go mikefsq/polemaster"
	p.Version = "0.1.0"
	p.IfaceVer = alpacadev.InterfaceVersionCamera
	p.openDev = p.openReal
	return p
}

// Open starts acquisition asynchronously, so the server can run without a camera.
func (p *PoleMaster) Open(ctx context.Context) error {
	if p.openDev == nil {
		p.openDev = p.openReal
	}
	p.stopLoop = alpacadev.RunLoop(ctx, p.ID, p.manageHardware)
	return nil
}

// Close stops management and waits for any active capture before closing USB.
func (p *PoleMaster) Close(ctx context.Context) error {
	if p.stopLoop != nil {
		p.stopLoop(10 * time.Second)
	}
	p.teardown()
	return nil
}

// Wait for capture before closing USB. Do not hold mu here: runExposure needs
// it to finish. Marking the capture aborted does not cancel its USB transfer.
func (p *PoleMaster) teardown() {
	p.aborted.Store(true)
	p.exposeWG.Wait()
	p.mu.Lock()
	if p.cam != nil {
		_ = p.cam.Close()
		p.cam = nil
	}
	p.hwPresent.Store(false)
	p.mu.Unlock()
}

// Connected reports hardware presence, shared by all clients.
func (p *PoleMaster) Connected() bool { return p.hwPresent.Load() }

// Connect checks presence; hardware acquisition runs independently of clients.
func (p *PoleMaster) Connect(ctx context.Context) error {
	if !p.hwPresent.Load() {
		return alpacadev.ErrNotConnected
	}
	return nil
}

// Disconnect leaves the shared camera open; it does not change hardware presence.
func (p *PoleMaster) Disconnect(ctx context.Context) error { return nil }

// Busy lets the server reject setting changes during capture.
func (p *PoleMaster) Busy() bool { return p.exposeOp.State() == alpacadev.OpBusy }

// Retry acquisition after disconnects. A physical replug clears RAM firmware,
// so the USB library loads it again when the camera returns in its boot state.
func (p *PoleMaster) manageHardware(ctx context.Context) {
	for {
		if !p.hwPresent.Load() {
			p.tryAcquire()
			if sleepCtx(ctx, acquirePoll) {
				return
			}
			continue
		}
		if sleepCtx(ctx, alivePoll) {
			return
		}
		if !p.stillPresent() {
			log.Printf("polemaster: %s stopped answering; releasing it", p.Label())
			p.teardown()
		}
	}
}

// sleepCtx waits for d, reporting whether the context ended first.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return true
	case <-t.C:
		return false
	}
}

// Open a camera and restore the selected readout depth before reporting presence.
func (p *PoleMaster) tryAcquire() {
	cam, err := p.openDev()
	if err != nil {
		return
	}
	p.mu.Lock()
	p.cam = cam
	if err := p.applyLocked(); err != nil {
		p.cam = nil
		p.mu.Unlock()
		_ = cam.Close()
		log.Printf("polemaster: %s opened but would not take its settings: %v", p.Label(), err)
		return
	}
	p.mu.Unlock()
	p.hwPresent.Store(true)
	log.Printf("polemaster: %s acquired", p.Label())
}

// Use the library's automatic boot download unless a custom image is configured.
// A custom image is loaded on every acquisition, including into a running camera.
func (p *PoleMaster) openReal() (*polemaster.Camera, error) {
	if p.firmware == "" {
		return polemaster.OpenCamera()
	}
	image, err := os.ReadFile(p.firmware)
	if err != nil {
		return nil, err
	}
	pids, err := polemaster.Attached()
	if err != nil {
		return nil, err
	}
	if len(pids) == 0 {
		return nil, polemaster.ErrNotFound
	}
	d, err := polemaster.Open(pids[0])
	if err != nil {
		return nil, err
	}
	err = polemaster.LoadImage(d, string(image))
	d.Close()
	if err != nil {
		return nil, err
	}
	dev, err := polemaster.WaitForCamera(15 * time.Second)
	if err != nil {
		return nil, err
	}
	cam := polemaster.Attach(dev)
	if err := cam.Init(); err != nil {
		dev.Close()
		return nil, err
	}
	return cam, nil
}

// Restore readout depth on acquisition; gain and offset keep the library's
// initial defaults. Exposure and geometry are programmed by StartExposure. Requires mu.
func (p *PoleMaster) applyLocked() error {
	return p.cam.SetDepth(p.depth)
}

// Skip idle probes during capture because Snap uses the camera without mu.
func (p *PoleMaster) stillPresent() bool {
	if p.exposeOp.State() == alpacadev.OpBusy {
		return true
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cam == nil {
		return false
	}
	_, err := p.cam.ReadReg(0x3000) // chip version
	return err == nil
}

// Sensor geometry and description.

func (p *PoleMaster) CameraXSize() int    { return polemaster.Width }
func (p *PoleMaster) CameraYSize() int    { return polemaster.Height }
func (p *PoleMaster) PixelSizeX() float64 { return pixelSizeUm }
func (p *PoleMaster) PixelSizeY() float64 { return pixelSizeUm }
func (p *PoleMaster) SensorName() string  { return sensorName }

func (p *PoleMaster) SensorType() alpacadev.SensorType { return alpacadev.SensorMonochrome }

// MaxADU reflects the selected pixel depth.
func (p *PoleMaster) MaxADU() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.depth == polemaster.Depth12 {
		return 4095
	}
	return 255
}

// Record client geometry separately from the hardware window. StartExposure
// selects an enclosing window; runExposure crops it to the requested coordinates.

func (p *PoleMaster) StartX() int { p.mu.Lock(); defer p.mu.Unlock(); return p.startX }
func (p *PoleMaster) StartY() int { p.mu.Lock(); defer p.mu.Unlock(); return p.startY }
func (p *PoleMaster) NumX() int   { p.mu.Lock(); defer p.mu.Unlock(); return p.numX }
func (p *PoleMaster) NumY() int   { p.mu.Lock(); defer p.mu.Unlock(); return p.numY }

func (p *PoleMaster) SetStartX(n int) error {
	if n < 0 || n >= polemaster.Width {
		return alpacadev.ErrInvalidValue
	}
	p.mu.Lock()
	p.startX = n
	p.mu.Unlock()
	return nil
}

func (p *PoleMaster) SetStartY(n int) error {
	if n < 0 || n >= polemaster.Height {
		return alpacadev.ErrInvalidValue
	}
	p.mu.Lock()
	p.startY = n
	p.mu.Unlock()
	return nil
}

func (p *PoleMaster) SetNumX(n int) error {
	if n < 1 || n > polemaster.Width {
		return alpacadev.ErrInvalidValue
	}
	p.mu.Lock()
	p.numX = n
	p.mu.Unlock()
	return nil
}

func (p *PoleMaster) SetNumY(n int) error {
	if n < 1 || n > polemaster.Height {
		return alpacadev.ErrInvalidValue
	}
	p.mu.Lock()
	p.numY = n
	p.mu.Unlock()
	return nil
}

// Gain and offset, both in ASCOM's value mode.

func (p *PoleMaster) GainMin() int { return polemaster.GainMin }
func (p *PoleMaster) GainMax() int { return polemaster.GainMax }

func (p *PoleMaster) Gain() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cam == nil {
		return polemaster.GainMin
	}
	return p.cam.Gain()
}

func (p *PoleMaster) SetGain(n int) error {
	if n < polemaster.GainMin || n > polemaster.GainMax {
		return alpacadev.ErrInvalidValue
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cam == nil {
		return alpacadev.ErrNotConnected
	}
	if err := p.cam.SetGain(n); err != nil {
		return fmt.Errorf("%w: %v", alpacadev.ErrInvalidValue, err)
	}
	return nil
}

func (p *PoleMaster) OffsetMin() int { return polemaster.OffsetMin }
func (p *PoleMaster) OffsetMax() int { return polemaster.OffsetMax }

func (p *PoleMaster) Offset() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cam == nil {
		return polemaster.OffsetDef
	}
	return p.cam.Offset()
}

func (p *PoleMaster) SetOffset(n int) error {
	if n < polemaster.OffsetMin || n > polemaster.OffsetMax {
		return alpacadev.ErrInvalidValue
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cam == nil {
		return alpacadev.ErrNotConnected
	}
	if err := p.cam.SetOffset(n); err != nil {
		return fmt.Errorf("%w: %v", alpacadev.ErrInvalidValue, err)
	}
	return nil
}

// Indices are client-visible: keep 8-bit as mode 0 and 12-bit as mode 1.

var readoutModes = []string{"8-bit", "12-bit"}

func (p *PoleMaster) ReadoutModes() []string { return readoutModes }

func (p *PoleMaster) ReadoutMode() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.depth == polemaster.Depth12 {
		return 1
	}
	return 0
}

func (p *PoleMaster) SetReadoutMode(n int) error {
	if n < 0 || n >= len(readoutModes) {
		return alpacadev.ErrInvalidValue
	}
	depth := polemaster.Depth8
	if n == 1 {
		depth = polemaster.Depth12
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.depth = depth
	if p.cam == nil {
		return nil // replayed by applyLocked when the camera is acquired
	}
	if err := p.cam.SetDepth(depth); err != nil {
		return fmt.Errorf("%w: %v", alpacadev.ErrInvalidValue, err)
	}
	return nil
}

// Exposure.

// ExposureMin is one sensor row, the quantum the integration counter works in.
func (p *PoleMaster) ExposureMin() float64 { return polemaster.RowTime.Seconds() }

// ExposureMax reports the protocol limit, not a verified integration duration.
// The supplied firmware implements the added portion by gating pixel transfer.
func (p *PoleMaster) ExposureMax() float64 {
	return polemaster.MaxSensorExposure.Seconds() + maxAddedMs/1000.0
}

// ExposureResolution reports sensor row time. Beyond the sensor counter's
// range, the added delay uses coarser one-millisecond steps.
func (p *PoleMaster) ExposureResolution() float64 { return polemaster.RowTime.Seconds() }

// HasShutter is false; light is ignored and dark frames require a lens cap.
func (p *PoleMaster) HasShutter() bool { return false }

// CanAbortExposure allows discarding a capture, not interrupting its USB read.
func (p *PoleMaster) CanAbortExposure() bool { return true }

// CanStopExposure is false because early completion with a usable image is unsupported.
func (p *PoleMaster) CanStopExposure() bool { return false }

func (p *PoleMaster) StopExposure() error { return alpacadev.ErrNotImplemented }

// StartExposure programs geometry and duration, then starts asynchronous capture.
// Nonnegative durations below one row are raised to one row; light is ignored.
func (p *PoleMaster) StartExposure(duration float64, light bool) error {
	if duration < 0 || duration > p.ExposureMax() {
		return alpacadev.ErrInvalidValue
	}
	if !p.hwPresent.Load() {
		return alpacadev.ErrNotConnected
	}
	d := time.Duration(duration * float64(time.Second))
	if d < polemaster.RowTime {
		d = polemaster.RowTime // the sensor integrates for whole rows
	}

	p.mu.Lock()
	if p.cam == nil {
		p.mu.Unlock()
		return alpacadev.ErrNotConnected
	}
	hx, hy, hw, hh := hardwareWindow(p.startX, p.startY, p.numX, p.numY)
	if err := p.cam.SetROI(hx, hy, hw, hh); err != nil {
		p.mu.Unlock()
		return fmt.Errorf("%w: %v", alpacadev.ErrInvalidValue, err)
	}
	if err := p.cam.SetExposure(d); err != nil {
		p.mu.Unlock()
		return fmt.Errorf("%w: %v", alpacadev.ErrInvalidValue, err)
	}
	period := p.cam.FramePeriod()
	p.expIntg = p.cam.Exposure()
	// Budget three readout periods for settling and capture. This is a progress
	// estimate; long integrations can exceed it and leave progress at 99%.
	p.expTotal = 3 * period
	p.expStart = time.Now()
	p.lastDuration = p.expIntg.Seconds()
	p.lastStart = time.Now().UTC()
	p.haveLast = true
	p.frame = nil
	p.mu.Unlock()

	if !p.exposeOp.TryBegin() {
		return alpacadev.ErrInvalidOperation
	}
	p.aborted.Store(false)
	p.exposeWG.Add(1)
	go p.runExposure(hx, hy, hw, hh)
	return nil
}

// runExposure takes the frame and crops it to the subframe the client set.
func (p *PoleMaster) runExposure(hx, hy, hw, hh int) {
	defer p.exposeWG.Done()

	p.mu.Lock()
	cam := p.cam
	sx, sy, w, h := p.startX, p.startY, p.numX, p.numY
	depth := p.depth
	p.mu.Unlock()
	if cam == nil {
		p.exposeOp.Fail(alpacadev.ErrNotConnected)
		return
	}

	raw, err := cam.Snap()
	if err != nil {
		log.Printf("polemaster: %s exposure failed: %v", p.Label(), err)
		p.hwPresent.Store(false) // a failed read means the endpoint needs re-acquiring
		p.exposeOp.Fail(err)
		return
	}
	if p.aborted.Load() {
		p.exposeOp.Reset()
		return
	}

	bpp := 1
	if depth == polemaster.Depth12 {
		bpp = 2
	}
	pixels := crop(raw, hw, hh, bpp, sx-hx, sy-hy, w, h)
	if depth == polemaster.Depth12 {
		pixels = decode12(pixels)
	}

	p.mu.Lock()
	p.frame, p.frameW, p.frameH, p.frameBpp = pixels, w, h, bpp
	p.mu.Unlock()
	p.exposeOp.Complete()
}

// AbortExposure marks the result for discard. Busy remains true until Snap
// returns; neither the sensor nor the blocking USB read is interrupted.
func (p *PoleMaster) AbortExposure() error {
	if p.exposeOp.State() != alpacadev.OpBusy {
		return nil
	}
	p.aborted.Store(true)
	return nil
}

func (p *PoleMaster) ImageReady() bool { return p.exposeOp.State() == alpacadev.OpDone }

func (p *PoleMaster) CameraState() alpacadev.CameraState {
	switch p.exposeOp.State() {
	case alpacadev.OpBusy:
		p.mu.Lock()
		elapsed, intg := time.Since(p.expStart), p.expIntg
		p.mu.Unlock()
		if elapsed < intg {
			return alpacadev.CameraExposing
		}
		return alpacadev.CameraReading
	case alpacadev.OpFailed:
		return alpacadev.CameraError
	default:
		return alpacadev.CameraIdle
	}
}

func (p *PoleMaster) PercentCompleted() int {
	switch p.exposeOp.State() {
	case alpacadev.OpDone:
		return 100
	case alpacadev.OpBusy:
		p.mu.Lock()
		elapsed, total := time.Since(p.expStart), p.expTotal
		p.mu.Unlock()
		if total <= 0 {
			return 0
		}
		pct := int(100 * elapsed / total)
		if pct > 99 {
			pct = 99 // 100 belongs to the completed frame
		}
		return pct
	default:
		return 0
	}
}

func (p *PoleMaster) LastExposureDuration() (float64, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.haveLast {
		return 0, alpacadev.ErrValueNotSet
	}
	return p.lastDuration, nil
}

func (p *PoleMaster) LastExposureStartTime() (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.haveLast {
		return "", alpacadev.ErrValueNotSet
	}
	return p.lastStart.Format("2006-01-02T15:04:05"), nil
}

// ImageFrame supplies row-major bytes to the server: one byte at depth 8,
// or little-endian uint16 at depth 12. The server handles Alpaca image serialization.
func (p *PoleMaster) ImageFrame() (alpacadev.ImageFrame, error) {
	if p.exposeOp.State() != alpacadev.OpDone {
		return alpacadev.ImageFrame{}, alpacadev.ErrValueNotSet
	}
	p.mu.Lock()
	f, w, h, bpp := p.frame, p.frameW, p.frameH, p.frameBpp
	p.mu.Unlock()
	if f == nil {
		return alpacadev.ImageFrame{}, alpacadev.ErrValueNotSet
	}
	tx := alpacadev.ImgByte
	if bpp == 2 {
		tx = alpacadev.ImgUInt16
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

// Choose an enclosing window with even coordinates and dimensions and a pixel
// count divisible by 512. Minimize height first to reduce readout time, then width;
// this is not necessarily the smallest area. Fall back to full frame if no fit exists.
func hardwareWindow(x, y, w, h int) (int, int, int, int) {
	if w <= 0 || h <= 0 || x < 0 || y < 0 ||
		x+w > polemaster.Width || y+h > polemaster.Height {
		return 0, 0, polemaster.Width, polemaster.Height
	}
	ox, oy := x&^1, y&^1
	needW, needH := even(x+w-ox), even(y+h-oy)

	for hh := needH; hh <= polemaster.Height; hh += 2 {
		// hw*hh must be divisible by 512, so hw must supply the factors
		// missing from hh. The first fitting height minimizes row readout time.
		step := packetPixels / gcd(packetPixels, hh)
		hw := ((needW + step - 1) / step) * step
		if hw > polemaster.Width || hh > polemaster.Height {
			continue
		}
		wx, wy := ox, oy
		if wx+hw > polemaster.Width {
			wx = (polemaster.Width - hw) &^ 1
		}
		if wy+hh > polemaster.Height {
			wy = (polemaster.Height - hh) &^ 1
		}
		if wx > x || wx+hw < x+w || wy > y || wy+hh < y+h {
			continue
		}
		return wx, wy, hw, hh
	}
	return 0, 0, polemaster.Width, polemaster.Height
}

// even rounds up to the next even number.
func even(n int) int {
	if n%2 != 0 {
		return n + 1
	}
	return n
}

func gcd(a, b int) int {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

// Crop row-major pixels using coordinates relative to the hardware window.
// A full-window crop aliases src; other crops allocate. The caller must supply
// a contained subframe and a complete source buffer.
func crop(src []byte, hw, hh, bpp, x, y, w, h int) []byte {
	if x == 0 && y == 0 && w == hw && h == hh {
		return src
	}
	out := make([]byte, w*h*bpp)
	stride := hw * bpp
	for row := 0; row < h; row++ {
		from := (y+row)*stride + x*bpp
		copy(out[row*w*bpp:(row+1)*w*bpp], src[from:from+w*bpp])
	}
	return out
}

// Convert packed camera pixels to the server's little-endian uint16 format.
// The wire bytes contain bits 11:4, then bits 3:0 below an unused upper nibble.
func decode12(wire []byte) []byte {
	px := make([]uint16, len(wire)/2)
	polemaster.Pixels12(wire, px)
	out := make([]byte, len(px)*2)
	for i, v := range px {
		binary.LittleEndian.PutUint16(out[2*i:], v)
	}
	return out
}
