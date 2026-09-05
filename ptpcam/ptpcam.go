// Package ptpcam exposes Fujifilm and Sony PTP cameras through ASCOM Alpaca.
package ptpcam

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"image/jpeg"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/mikefsq/goalpaca/alpaca"
	alpacadev "github.com/mikefsq/goalpaca/server"
	"github.com/mikefsq/ptp"
	"github.com/mikefsq/ptp/usb"
)

// Camera is a PTP stills body presented as an Alpaca camera.
//
// It is VENDOR-NEUTRAL: it holds the parent package's capability interfaces
// rather than a Fujifilm or Sony type, so the same driver drives either. Those
// interfaces exist precisely for this, and this is their first real consumer —
// which is why the vendor packages carry compile-time assertions against them.
type Camera struct {
	// stopLoop cancels acquisition and waits before releasing the handle.
	stopLoop func(time.Duration)
	alpacadev.BaseCamera

	// Body is the open camera. The capability fields below are the same object
	// narrowed by type assertion; any may be nil if the body cannot do it.
	Body     ptp.Camera
	capturer ptp.Capturer
	exposure ptp.ExposureControl
	download ptp.Downloader
	focus    ptp.FocusControl
	rawdec   ptp.RawDecoder

	// sensor describes the last decoded readout, so SensorType, MaxADU and the
	// Bayer offsets answer from the frame actually delivered rather than from a
	// guess made before anything was shot.
	sensor *ptp.CFA

	// Opener creates the body. Injected so tests run without hardware and so
	// the vendor choice stays out of this file. It is called repeatedly — once
	// per acquisition attempt — not just at startup.
	Opener func() (ptp.Camera, error)

	// AliveFn reports whether the camera is still on the bus, WITHOUT touching
	// it. cmd/ptpcam supplies a probe over the OS USB registry; tests override
	// it. See Camera.alive.
	AliveFn func() bool

	// HotplugFn subscribes to the OS's USB attach and detach notifications,
	// the interrupt the supervisor selects on beside its poll; usb.Hotplug in
	// the registered driver, nil in tests (polling path). See manageHardware.
	HotplugFn func(context.Context) (<-chan usb.HotplugEvent, error)

	// CardOnly skips transfer and deletes the pending capture.
	// The camera must save to its card; ImageReady remains false.
	CardOnly bool

	// hwPresent is the single source of truth for "is a camera attached", and
	// is what Connected reports. needsReconnect is set by an operation that
	// found the session dead, which the presence probe cannot see.
	hwPresent      atomic.Bool
	needsReconnect atomic.Bool
	// replaced is set by the alive probe when the body at the bus is a new
	// attachment (unplugged and replugged between two probes): the open
	// session is dead for certain, so the supervisor re-acquires at once rather
	// than after the miss count that guards against an enumeration blink.
	replaced atomic.Bool

	// exposeWG joins an in-flight exposure before the handle is freed.
	exposeWG sync.WaitGroup

	mu sync.Mutex

	// Sensor geometry. Learned rather than assumed — see resolveGeometry.
	width, height int

	// pixelSize is the photosite pitch in microns. PTP does not expose it, so
	// it is configured or it stays zero.
	pixelSize float64

	state    alpacadev.CameraState
	exposing bool

	// lastFile is the frame exactly as the camera produced it. lastFrame is the
	// decoded form. Both are the SAME capture; they are kept apart because one
	// is for ASCOM and one is for anything that wants the real file.
	lastFile  []byte
	lastName  string
	lastFrame *alpaca.ImageFrame

	lastDuration float64
	lastStart    time.Time
	haveExposure bool

	// requested is what the client asked for; actual is what the camera's
	// ladder could do. They differ, and the difference is reported.
	requested, actual float64
}

// timingDebug logs per-phase capture timing. Off by default; set
// PTPCAM_TIMING=1. The phases are what a slow capture has to be diagnosed
// against — the camera's own latency is usually the largest of them, and
// telling that apart from the driver's overhead needs measurement.
var timingDebug = func() bool {
	switch strings.ToLower(os.Getenv("PTPCAM_TIMING")) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}()

// New builds a driver around an opener. Nothing touches hardware until Open.
func New(id, name string, opener func() (ptp.Camera, error)) *Camera {
	c := &Camera{Opener: opener}
	c.ID = id
	c.DevName = name
	c.Desc = "PTP stills camera (Fujifilm/Sony) over Alpaca, no vendor SDK"
	c.Info = "ptpcam — github.com/mikefsq/goalpaca-devices/ptpcam"
	c.Version = "0.1.0"
	c.IfaceVer = 3
	return c
}

// GeometrySource is where CameraXSize/CameraYSize came from, for the operator's
// benefit — a driver reporting a guessed sensor size should say so.
type GeometrySource string

const (
	GeometryConfigured GeometrySource = "configured"
	GeometryReported   GeometrySource = "reported by the camera"
	GeometryLearned    GeometrySource = "learned from the first frame"
	GeometryUnknown    GeometrySource = "unknown"
)

// SetPixelSize records the photosite pitch in microns.
//
// PTP has no property for it — a body reports its image size but never its
// sensor's physical dimensions — so this cannot be discovered and must be told.
// An X-T5 is 3.04, a full-frame A7R V 3.76.
func (c *Camera) SetPixelSize(microns float64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pixelSize = microns
}

// PixelSizeX returns the configured photosite pitch in micrometres, or zero if unset.
func (c *Camera) PixelSizeX() float64 { return c.pixelSizeLocked() }

// PixelSizeY is the same: these sensors have square photosites.
func (c *Camera) PixelSizeY() float64 { return c.pixelSizeLocked() }

func (c *Camera) pixelSizeLocked() float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pixelSize
}

// SetGeometry pins the sensor size explicitly, overriding what is discovered.
func (c *Camera) SetGeometry(w, h int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.width, c.height = w, h
}

// resolveGeometryLocked asks the camera for its image size.
//
// Fujifilm reports ImageSize (0x5003) as a STRING like "7728x5152", which is
// authoritative and available immediately. Sony reports an L/M/S enum instead,
// which says nothing about pixels — so on that body the size stays zero until
// the first frame is decoded, and CameraXSize reports zero rather than a guess.
func (c *Camera) resolveGeometryLocked() {
	if c.width != 0 && c.height != 0 {
		return
	}
	type propStringer interface {
		GetPropString(ptp.Prop) (string, error)
	}
	ps, ok := c.Body.(propStringer)
	if !ok {
		return
	}
	s, err := ps.GetPropString(ptp.PropImageSize)
	if err != nil {
		return
	}
	if w, h, ok := parseSize(s); ok {
		c.width, c.height = w, h
	}
}

// parseSize reads a "WxH" image size. Anything else — an enum name, a mode
// label — is rejected rather than coerced.
func parseSize(s string) (w, h int, ok bool) {
	s = strings.TrimSpace(s)
	for _, sep := range []string{"x", "X", "*"} {
		a, b, found := strings.Cut(s, sep)
		if !found {
			continue
		}
		w, err1 := strconv.Atoi(strings.TrimSpace(a))
		h, err2 := strconv.Atoi(strings.TrimSpace(b))
		if err1 == nil && err2 == nil && w > 0 && h > 0 {
			return w, h, true
		}
	}
	return 0, 0, false
}

func (c *Camera) CameraXSize() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.width
}

func (c *Camera) CameraYSize() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.height
}

// NumX and NumY must agree with the frame actually delivered, or a client
// renders a sheared image. There is no subframing here, so they track the
// sensor.
func (c *Camera) NumX() int { return c.CameraXSize() }
func (c *Camera) NumY() int { return c.CameraYSize() }

func (c *Camera) SensorName() string {
	if c.Body == nil {
		return ""
	}
	return c.Body.Model()
}

// SensorType describes the delivered frame, or the sensor before capture.
// X-Trans is reported as monochrome because ASCOM cannot describe its 6×6 CFA.
// Without sensor information, the fallback is RGB for JPEG captures.
func (c *Camera) SensorType() alpacadev.SensorType {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sensor == nil {
		return alpacadev.SensorColor
	}
	if c.sensor.IsBayer() {
		return alpacadev.SensorRGGB
	}
	return alpacadev.SensorMonochrome
}

// BayerOffsetX is the mosaic's phase, and exists only for a 2x2 CFA. ASCOM
// requires it to be unimplemented for a monochrome sensor, which is how an
// X-Trans frame is reported.
func (c *Camera) BayerOffsetX() (int, error) { return c.bayerOffset(true) }

// BayerOffsetY is the mosaic's phase in the other axis.
func (c *Camera) BayerOffsetY() (int, error) { return c.bayerOffset(false) }

func (c *Camera) bayerOffset(x bool) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sensor == nil || !c.sensor.IsBayer() {
		return 0, alpacadev.ErrNotImplemented
	}
	// Offsets locate the first RED photosite within the 2x2 cell, which is what
	// ASCOM's convention means by phase.
	for y := 0; y < 2; y++ {
		for xx := 0; xx < 2; xx++ {
			if c.sensor.ColorAt(xx, y) == ptp.CFARed {
				if x {
					return xx, nil
				}
				return y, nil
			}
		}
	}
	return 0, alpacadev.ErrNotImplemented
}

// MaxADU is the saturation point of the frame actually delivered: the sensor's
// white level for a RAW readout, 255 for the 8-bit JPEG path. Claiming 65535
// for a JPEG would advertise headroom the transport threw away, and claiming
// 255 for a 14-bit readout would understate it by six stops.
func (c *Camera) MaxADU() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sensor != nil && c.sensor.WhiteLevel > 0 {
		return int(c.sensor.WhiteLevel)
	}
	return 255
}

func (c *Camera) HasShutter() bool { return true }

func (c *Camera) CanAbortExposure() bool { return false }
func (c *Camera) CanStopExposure() bool  { return false }

// ExposureMin/Max are the ladder's ends, not a continuous range. A request
// between two rungs is snapped, not rejected.
func (c *Camera) ExposureMin() float64 { return 1.0 / 32000 }
func (c *Camera) ExposureMax() float64 { return 64 }

// ExposureResolution is zero because the camera's speeds are discrete and
// unevenly spaced — roughly a third of a stop apart, so no single step
// describes them. Reporting a fake resolution would imply a precision the body
// does not have.
func (c *Camera) ExposureResolution() float64 { return 0 }

func (c *Camera) CameraState() alpacadev.CameraState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

func (c *Camera) ImageReady() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastFrame != nil && !c.exposing
}

func (c *Camera) PercentCompleted() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.exposing {
		return 0
	}
	if c.lastFrame != nil {
		return 100
	}
	return 0
}

func (c *Camera) LastExposureDuration() (float64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.haveExposure {
		return 0, alpacadev.ErrValueNotSet
	}
	// The ACTUAL duration, which is a rung on the camera's ladder and may not
	// be what the client asked for.
	return c.lastDuration, nil
}

func (c *Camera) LastExposureStartTime() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.haveExposure {
		return "", alpacadev.ErrValueNotSet
	}
	return c.lastStart.UTC().Format("2006-01-02T15:04:05"), nil
}

// StartExposure snaps the requested duration to the nearest speed the camera
// offers, fires the shutter, then downloads and decodes the frame.
//
// It runs the capture on a goroutine so the Alpaca call returns immediately, as
// the spec requires of an initiator; clients poll ImageReady/CameraState.
func (c *Camera) StartExposure(duration float64, light bool) error {
	if !c.hwPresent.Load() {
		return alpacadev.ErrNotConnected
	}
	if !light {
		// There is no dark frame on a body with no shutter-closed mode worth
		// the name. Saying so is better than silently taking a light frame.
		return fmt.Errorf("ptpcam: this camera cannot take a dark frame: %w",
			alpacadev.ErrInvalidValue)
	}
	c.mu.Lock()
	if c.exposing {
		c.mu.Unlock()
		return fmt.Errorf("ptpcam: an exposure is already running: %w", alpacadev.ErrInvalidOperation)
	}
	if c.capturer == nil {
		c.mu.Unlock()
		return fmt.Errorf("ptpcam: this body cannot capture: %w", alpacadev.ErrNotImplemented)
	}
	c.exposing = true
	c.state = alpacadev.CameraExposing
	c.lastFrame, c.lastFile = nil, nil
	c.requested = duration
	c.mu.Unlock()

	// Joined by teardown, so no PTP transaction outlives the handle it runs on.
	c.exposeWG.Add(1)
	go func() {
		defer c.exposeWG.Done()
		c.runExposure(duration)
	}()
	return nil
}

func (c *Camera) runExposure(duration float64) {
	err := c.expose(duration)
	// A dead session is the supervisor's problem, not this goroutine's: it
	// cannot tear itself down, because teardown joins it.
	c.noteTransportError(err)

	c.mu.Lock()
	defer c.mu.Unlock()
	c.exposing = false
	if err != nil {
		c.state = alpacadev.CameraError
		return
	}
	c.state = alpacadev.CameraIdle
}

func (c *Camera) expose(duration float64) error {
	// Set the shutter first. The vendor package snaps to the nearest rung the
	// camera advertises, which is why the request is not validated here.
	if c.exposure != nil {
		if err := c.exposure.SetShutter(time.Duration(duration * float64(time.Second))); err != nil {
			return fmt.Errorf("setting the shutter to %gs: %w", duration, err)
		}
	}

	start := time.Now()
	// The timeout must exceed the exposure itself, with room for the camera to
	// write the frame.
	var tShutter, tFile, tDecode time.Time
	timeout := time.Duration(duration*float64(time.Second)) + 30*time.Second
	if err := c.capturer.Capture(timeout); err != nil {
		return fmt.Errorf("capture: %w", err)
	}
	tShutter = time.Now()

	// Read back what the camera actually used, rather than assuming the request
	// landed on a rung.
	actual := duration
	if c.exposure != nil {
		if e, err := c.exposure.Exposure(); err == nil && e.Shutter > 0 {
			actual = e.Shutter.Seconds()
		}
	}

	file, name, err := c.collectFrame(c.CardOnly)
	if err != nil {
		return err
	}
	tFile = time.Now()
	if c.CardOnly {
		// The photograph is on the card and nothing crossed USB, so there is no
		// image to serve. The exposure still happened, and its timing is still
		// worth reporting.
		c.mu.Lock()
		defer c.mu.Unlock()
		c.lastDuration, c.lastStart, c.haveExposure = actual, start, true
		c.actual = actual
		return nil
	}
	// A RAW container decodes to an undemosaiced readout, which is what
	// calibration needs; a JPEG-only body still goes through the JPEG path.
	// RAW is tried first because a RAF also CONTAINS a JPEG preview, and
	// delivering that instead would silently hand over a 1920x1280 thumbnail
	// where the client asked for the sensor.
	if frame, cfa, err := c.decodeRaw(file); err == nil {
		tDecode = time.Now()
		if timingDebug {
			log.Printf("ptpcam: shutter+settle %v, frame appeared and downloaded %v, decode %v, total %v",
				tShutter.Sub(start).Round(time.Millisecond),
				tFile.Sub(tShutter).Round(time.Millisecond),
				tDecode.Sub(tFile).Round(time.Millisecond),
				tDecode.Sub(start).Round(time.Millisecond))
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		c.lastFile, c.lastName, c.lastFrame = file, name, frame
		c.sensor = cfa
		c.lastDuration, c.lastStart, c.haveExposure = actual, start, true
		c.actual = actual
		c.width, c.height = frame.Width, frame.Height
		return nil
	}

	frame, err := decodeJPEG(file)
	if err != nil {
		// Keep the file even when it will not decode: a RAW-only capture is a
		// perfectly good frame that simply cannot go through ImageFrame, and
		// the caller can still fetch it byte-for-byte.
		c.mu.Lock()
		c.lastFile, c.lastName = file, name
		c.lastDuration, c.lastStart, c.haveExposure = actual, start, true
		c.mu.Unlock()
		return fmt.Errorf("the frame is not a JPEG, so ImageFrame is unavailable "+
			"(the file itself is intact and can be fetched): %w", err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastFile, c.lastName, c.lastFrame = file, name, frame
	c.lastDuration, c.lastStart, c.haveExposure = actual, start, true
	c.actual = actual
	// The delivered frame is authoritative about geometry, whatever the camera
	// claimed earlier.
	c.width, c.height = frame.Width, frame.Height
	return nil
}

// collectFrame transfers and releases a capture, or only releases it in CardOnly mode.
// Fujifilm blocks property writes until the pending capture is deleted.
// CardOnly requires card storage; otherwise deletion loses the only copy.
func (c *Camera) collectFrame(skip bool) ([]byte, string, error) {
	if c.download == nil {
		return nil, "", fmt.Errorf("this body cannot download frames")
	}
	deadline := time.Now().Add(30 * time.Second)
	waitStart := time.Now()
	for {
		handles, err := c.download.NewFrames()
		if err != nil {
			return nil, "", fmt.Errorf("listing new frames: %w", err)
		}
		if len(handles) > 0 {
			appeared := time.Now()
			data, name, err := c.takeOne(handles, skip)
			if timingDebug {
				log.Printf("ptpcam:   frame appeared %v after the shutter, downloaded in %v",
					appeared.Sub(waitStart).Round(time.Millisecond),
					time.Since(appeared).Round(time.Millisecond))
			}
			return data, name, err
		}
		if time.Now().After(deadline) {
			return nil, "", fmt.Errorf("the camera announced no frame within 30s")
		}
		// Poll tightly. The frame takes most of a second to appear and the
		// wait is pure latency on every capture, so a coarse tick would add up
		// to its whole interval for nothing. NewFrames is one cheap PTP call.
		time.Sleep(15 * time.Millisecond)
	}
}

// takeOne downloads one capture and deletes all returned handles.
// Unexpected extra handles are reported and released to avoid blocking the camera.
func (c *Camera) takeOne(handles []uint32, skip bool) ([]byte, string, error) {
	if len(handles) > 1 {
		log.Printf("ptpcam: camera %s produced %d frames for one exposure (%#x); "+
			"keeping the newest and deleting the rest", c.ID, len(handles), handles)
	}
	h := handles[len(handles)-1]

	var data []byte
	var name string
	if !skip {
		var err error
		data, name, err = c.download.Download(h)
		if err != nil {
			// Leave it on the camera. A failed transfer is not a reason to
			// destroy the frame, even though the camera stays stuck — that is
			// recoverable, and the photograph is not.
			return nil, "", fmt.Errorf("downloading frame %#x: %w", h, err)
		}
	}

	for _, x := range handles {
		if err := c.download.Delete(x); err != nil {
			// The store did not empty, so the next exposure will meet a stuck
			// camera. Say so now rather than let it surface as a mysterious
			// RefusedRightNow later.
			log.Printf("ptpcam: camera %s would not delete frame %#x: %v; "+
				"the body is left holding it", c.ID, x, err)
		}
	}
	return data, name, nil
}

// decodeRaw returns the full, undemosaiced RAW readout as a rank-2, 16-bit ImageFrame.
func (c *Camera) decodeRaw(file []byte) (*alpaca.ImageFrame, *ptp.CFA, error) {
	if c.rawdec == nil {
		return nil, nil, fmt.Errorf("this body cannot decode its own RAW")
	}
	cfa, err := c.rawdec.DecodeCFA(file)
	if err != nil {
		return nil, nil, err
	}
	// Alias samples on little-endian hosts to avoid a full-frame allocation.
	// Big-endian hosts copy and byte-swap to little-endian order.
	px := samplesAsBytes(cfa.Pixels)

	return &alpaca.ImageFrame{
		Rank:        2,
		Width:       cfa.Width,
		Height:      cfa.Height,
		ElementType: alpaca.ImgUInt16,
		Pixels:      px,
	}, cfa, nil
}

// ImageFrame returns the decoded frame. It is the ASCOM path, and the only one
// a standard client sees.
func (c *Camera) ImageFrame() (alpaca.ImageFrame, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.exposing {
		return alpaca.ImageFrame{}, alpacadev.ErrInvalidOperation
	}
	if c.lastFrame == nil {
		return alpaca.ImageFrame{}, alpacadev.ErrValueNotSet
	}
	return *c.lastFrame, nil
}

// LastFile returns the newest frame EXACTLY as the camera produced it, and its
// filename. Nothing is transcoded and nothing was written to disk.
func (c *Camera) LastFile() ([]byte, string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.lastFile) == 0 {
		return nil, "", false
	}
	return c.lastFile, c.lastName, true
}

// decodeJPEG turns a camera JPEG into an ImageFrame of RGB planes.
//
// Rank 3 with 3 planes: the pixels are already demosaiced, so presenting them
// as a Bayer frame would be a lie a client would try to debayer again.
func decodeJPEG(data []byte) (*alpaca.ImageFrame, error) {
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()

	// Plane-major: all of R, then all of G, then all of B, each row-major. The
	// encoder copies Rank-3 frames untouched, so this order is what reaches
	// the client.
	pix := make([]byte, w*h*3)
	plane := w * h
	i := 0
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, _ := img.At(x, y).RGBA()
			pix[i] = byte(r >> 8)
			pix[plane+i] = byte(g >> 8)
			pix[2*plane+i] = byte(bl >> 8)
			i++
		}
	}
	return &alpaca.ImageFrame{
		Rank:        3,
		Width:       w,
		Height:      h,
		Planes:      3,
		ElementType: alpaca.ImgByte,
		Pixels:      pix,
	}, nil
}

// Gain maps to ISO. Read it back after setting to obtain the camera’s
// selected value from its supported exposure ladder.
func (c *Camera) Gain() int {
	if !c.hwPresent.Load() {
		return 0
	}
	c.mu.Lock()
	exp := c.exposure
	c.mu.Unlock()
	if exp == nil {
		return 0
	}
	e, err := exp.Exposure()
	if err != nil || e == nil {
		return 0
	}
	return int(e.ISO)
}

func (c *Camera) SetGain(v int) error {
	if !c.hwPresent.Load() {
		return alpacadev.ErrNotConnected
	}
	c.mu.Lock()
	exp := c.exposure
	c.mu.Unlock()
	if exp == nil {
		return alpacadev.ErrNotImplemented
	}
	if v < 0 {
		return fmt.Errorf("ptpcam: ISO %d is negative: %w", v, alpacadev.ErrInvalidValue)
	}
	if err := exp.SetISO(uint32(v)); err != nil {
		return fmt.Errorf("ptpcam: setting ISO %d: %w", v, err)
	}
	return nil
}

// GainMin and GainMax bound the ISO range broadly rather than precisely: the
// real set is a discrete ladder that moves with the body and its drive mode,
// and the camera snaps to it. These exist because ASCOM requires them.
func (c *Camera) GainMin() int { return 64 }
func (c *Camera) GainMax() int { return 102400 }

// Gains reports NotImplemented: this driver is in value mode, and ASCOM forbids
// offering both.
func (c *Camera) Gains() ([]string, error) { return nil, alpacadev.ErrNotImplemented }

// nativeLittleEndian reports whether uint16 in memory is already the
// little-endian byte order ImageBytes specifies.
var nativeLittleEndian = func() bool {
	v := uint16(1)
	return (*[2]byte)(unsafe.Pointer(&v))[0] == 1
}()

// samplesAsBytes views 16-bit samples as the bytes ImageBytes carries.
//
// On a little-endian host this ALIASES the sample slice — no allocation, no
// copy — so the CFA and the ImageFrame share one buffer. The CFA is not used
// again after the frame is built, so the aliasing is not observable; if that
// ever changes, this becomes a copy again.
func samplesAsBytes(px []uint16) []byte {
	if len(px) == 0 {
		return nil
	}
	if nativeLittleEndian {
		return unsafe.Slice((*byte)(unsafe.Pointer(&px[0])), len(px)*2)
	}
	out := make([]byte, len(px)*2)
	for i, v := range px {
		binary.LittleEndian.PutUint16(out[i*2:], v)
	}
	return out
}
