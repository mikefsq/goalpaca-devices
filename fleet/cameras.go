package main

import (
	asicamdrv "github.com/mikefsq/asicam-alpaca"
	alpacadev "github.com/mikefsq/goalpaca/server"
	"github.com/mikefsq/goindi/ccd"
)

// alpacaCamera is the subset of the Alpaca camera interface the INDI CCD adapter
// needs. Both goalpaca/sim.Camera and astrocam's PureASICamera satisfy it, so one
// adapter serves the simulator and the real ZWO camera with no driver changes.
type alpacaCamera interface {
	PixelSizeX() float64
	PixelSizeY() float64
	CameraXSize() int
	CameraYSize() int
	MaxADU() int
	StartExposure(duration float64, light bool) error
	ImageReady() bool
	ImageFrame() (alpacadev.ImageFrame, error)
	AbortExposure() error
}

// ccdSource adapts an Alpaca camera to ccd.Camera (the frame source the INDI CCD
// device drives). RAW frames pass through untouched.
type ccdSource struct{ c alpacaCamera }

func (s *ccdSource) PixelSizeUm() (float64, float64)  { return s.c.PixelSizeX(), s.c.PixelSizeY() }
func (s *ccdSource) Size() (int, int)                 { return s.c.CameraXSize(), s.c.CameraYSize() }
func (s *ccdSource) StartExposure(secs float64) error { return s.c.StartExposure(secs, true) }
func (s *ccdSource) ImageReady() bool                 { return s.c.ImageReady() }
func (s *ccdSource) AbortExposure() error             { return s.c.AbortExposure() }

// BitsPerPixel derives the depth from MaxADU (RAW16 → 65535, RAW8 → 255).
func (s *ccdSource) BitsPerPixel() int {
	if s.c.MaxADU() > 255 {
		return 16
	}
	return 8
}

func (s *ccdSource) Frame() (int, int, []byte, error) {
	f, err := s.c.ImageFrame()
	if err != nil {
		return 0, 0, nil, err
	}
	return f.Width, f.Height, f.Pixels, nil
}

// astrocamINDI adds the LiveCamera seam to the astrocam Alpaca driver, so it appears
// over INDI as a guide camera. astrocam itself needs no changes — this fleet-side
// wrapper adapts its existing Alpaca camera surface. The Alpaca behaviour is
// unchanged (the driver is embedded), and LiveCamera gates on a live hardware
// connection so the INDI CCD device only drives the camera once it is acquired.
type astrocamINDI struct {
	*asicamdrv.PureASICamera
	src *ccdSource
}

func newAstrocamINDI(c *asicamdrv.PureASICamera) *astrocamINDI {
	return &astrocamINDI{PureASICamera: c, src: &ccdSource{c: c}}
}

func (a *astrocamINDI) LiveCamera() (ccd.Camera, error) {
	if !a.Connected() {
		return nil, alpacadev.ErrNotConnected
	}
	return a.src, nil
}

var (
	_ ccd.Camera = (*ccdSource)(nil)
	_ liveCamera = (*astrocamINDI)(nil)
)
