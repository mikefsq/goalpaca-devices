package driver

import (
	alpacadev "github.com/mikefsq/goalpaca/server"
	"github.com/mikefsq/goalpaca/sim"
	"github.com/mikefsq/goindi/ccd"
)

// defaultGuideFocalLenMM is the guide scope focal length used to convert the
// pointing error and seeing (arc-seconds) to pixels when a sim-camera entry does not
// set one — a typical short guide scope. With the 5.86 µm sim sensor this is ~6"/px.
const defaultGuideFocalLenMM = 200.0

// simCamera is the Alpaca camera simulator (goalpaca/sim) rendering the shared guide
// sky in place of the sim's gradient, with a LiveCamera seam so it also drives the
// INDI CCD device (a no-hardware guide camera for PHD2 over Alpaca or INDI).
type simCamera struct {
	*sim.Camera
	src                    ccd.Camera
	see                    *seeing
	focalLenMM             float64
	pixelSizeX, pixelSizeY float64 // µm — set the arcsec→px scale (and reported over Alpaca/INDI)
	sizeX, sizeY           int     // sensor pixel count (CameraXSize/CameraYSize)
}

func newSimCamera(name string, focalLenMM, pixelSizeX, pixelSizeY float64, sizeX, sizeY int) *simCamera {
	c := sim.NewCamera()
	if name != "" {
		c.DevName = name
	}
	if focalLenMM <= 0 {
		focalLenMM = defaultGuideFocalLenMM
	}
	if pixelSizeX <= 0 {
		pixelSizeX = c.PixelSizeX() // sim default (5.86 µm)
	}
	if pixelSizeY <= 0 {
		pixelSizeY = pixelSizeX // assume square pixels
	}
	if sizeX <= 0 {
		sizeX = c.CameraXSize() // sim default (1936)
	}
	if sizeY <= 0 {
		sizeY = c.CameraYSize() // sim default (1096)
	}
	// Reset the default ROI to the configured full frame (NewCamera seeded numX/numY
	// from the sim's fixed size before our override takes effect).
	_ = c.SetNumX(sizeX)
	_ = c.SetNumY(sizeY)
	sc := &simCamera{
		Camera: c, see: newSeeing(len(tileStars)),
		focalLenMM: focalLenMM, pixelSizeX: pixelSizeX, pixelSizeY: pixelSizeY,
		sizeX: sizeX, sizeY: sizeY,
	}
	sc.src = &starCamSource{cam: sc}
	return sc
}

// PixelSizeX/PixelSizeY report the configured pixel size, and CameraXSize/CameraYSize
// the configured sensor dimensions (both shadow sim.Camera's fixed values), so
// Alpaca/INDI clients, the ROI defaults, and the pixel-scale math all agree.
func (s *simCamera) PixelSizeX() float64 { return s.pixelSizeX }
func (s *simCamera) PixelSizeY() float64 { return s.pixelSizeY }
func (s *simCamera) CameraXSize() int    { return s.sizeX }
func (s *simCamera) CameraYSize() int    { return s.sizeY }

// pxPerArcsec converts sky angle to sensor pixels per axis: FL(mm) / (206.265 × pixel
// µm). X and Y differ for non-square pixels.
func (s *simCamera) pxPerArcsec() (x, y float64) {
	return s.focalLenMM / (206.265 * s.pixelSizeX), s.focalLenMM / (206.265 * s.pixelSizeY)
}

// renderFrame paints the ROI (startX,startY,w,h) of the sensor: it projects the
// mount's pointing error and the exposure-averaged seeing (both arc-seconds) to
// pixels and renders the wrapping star tile there. One call per exposure advances
// both the pointing model and the seeing.
func (s *simCamera) renderFrame(startX, startY, w, h int) []byte {
	errRA, errDec := sky.offset()
	exp, _ := s.Camera.LastExposureDuration()
	cmX, cmY, pert := s.see.integrate(exp)
	ppsX, ppsY := s.pxPerArcsec()
	for i := range pert {
		pert[i].dx *= ppsX
		pert[i].dy *= ppsY
	}
	return renderTile(s.CameraXSize(), s.CameraYSize(), startX, startY, w, h,
		errRA*ppsX, errDec*ppsY, cmX*ppsX, cmY*ppsY, pert)
}

// ImageFrame overrides the embedded sim camera's gradient on the Alpaca path with
// the guide sky, honouring the current ROI.
func (s *simCamera) ImageFrame() (alpacadev.ImageFrame, error) {
	f, err := s.Camera.ImageFrame() // ROI geometry, element types, readiness
	if err != nil {
		return f, err
	}
	f.Pixels = s.renderFrame(s.Camera.StartX(), s.Camera.StartY(), f.Width, f.Height)
	return f, nil
}

// LiveCamera exposes the simulator as a ccd.Camera for the INDI CCD device.
func (s *simCamera) LiveCamera() (ccd.Camera, error) { return s.src, nil }

// starCamSource is the ccd.Camera (INDI) frame source: it renders the full sensor
// through the same guide sky as the Alpaca path.
type starCamSource struct {
	cam *simCamera
}

func (s *starCamSource) PixelSizeUm() (float64, float64) {
	return s.cam.pixelSizeX, s.cam.pixelSizeY
}
func (s *starCamSource) Size() (int, int) {
	return s.cam.CameraXSize(), s.cam.CameraYSize()
}
func (s *starCamSource) BitsPerPixel() int { return 16 }
func (s *starCamSource) StartExposure(secs float64) error {
	return s.cam.Camera.StartExposure(secs, true)
}
func (s *starCamSource) ImageReady() bool     { return s.cam.Camera.ImageReady() }
func (s *starCamSource) AbortExposure() error { return s.cam.Camera.AbortExposure() }

func (s *starCamSource) Frame() (int, int, []byte, error) {
	w, h := s.cam.CameraXSize(), s.cam.CameraYSize()
	return w, h, s.cam.renderFrame(0, 0, w, h), nil
}

var _ ccd.Camera = (*starCamSource)(nil)
