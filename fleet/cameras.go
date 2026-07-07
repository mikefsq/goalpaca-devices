package main

import (
	asicamdrv "github.com/mikefsq/goalpaca-devices/astrocam"
	alpacadev "github.com/mikefsq/goalpaca/server"
	"github.com/mikefsq/goindi/ccd"
)

// alpacaCamera is the subset of the Alpaca camera interface the INDI CCD adapter
// needs. Both goalpaca/sim.Camera and astrocam's PureASICamera satisfy it.
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

// ccdSource adapts an Alpaca camera to ccd.Camera (the INDI CCD frame source). RAW
// frames pass through untouched.
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

// asiCCDSource is a ccdSource that also exposes the ASI camera's gain, offset, and
// subframe ROI, so the INDI CCD device advertises CCD_CONTROLS and CCD_FRAME. It
// drives the astrocam camera object's Go methods directly.
type asiCCDSource struct {
	ccdSource
	cam *asicamdrv.PureASICamera
}

func (s *asiCCDSource) Gain() (int, int, int) { return s.cam.Gain(), s.cam.GainMin(), s.cam.GainMax() }
func (s *asiCCDSource) SetGain(n int) error   { return s.cam.SetGain(n) }
func (s *asiCCDSource) Offset() (int, int, int) {
	return s.cam.Offset(), s.cam.OffsetMin(), s.cam.OffsetMax()
}
func (s *asiCCDSource) SetOffset(n int) error { return s.cam.SetOffset(n) }
func (s *asiCCDSource) Subframe() (int, int, int, int) {
	return s.cam.StartX(), s.cam.StartY(), s.cam.NumX(), s.cam.NumY()
}
func (s *asiCCDSource) SetSubframe(x, y, w, h int) error {
	if err := s.cam.SetStartX(x); err != nil {
		return err
	}
	if err := s.cam.SetStartY(y); err != nil {
		return err
	}
	if err := s.cam.SetNumX(w); err != nil {
		return err
	}
	return s.cam.SetNumY(h)
}

// astrocamINDI adds the LiveCamera seam to the astrocam Alpaca driver, so it appears
// over INDI as a guide camera. The driver is embedded (Alpaca behaviour unchanged);
// LiveCamera gates on a live hardware connection so the INDI CCD device drives the
// camera only once it is acquired.
type astrocamINDI struct {
	*asicamdrv.PureASICamera
	src ccd.Camera
}

func newAstrocamINDI(c *asicamdrv.PureASICamera) *astrocamINDI {
	return &astrocamINDI{PureASICamera: c, src: &asiCCDSource{ccdSource: ccdSource{c: c}, cam: c}}
}

func (a *astrocamINDI) LiveCamera() (ccd.Camera, error) {
	if !a.Connected() {
		return nil, alpacadev.ErrNotConnected
	}
	return a.src, nil
}

var (
	_ ccd.Camera           = (*ccdSource)(nil)
	_ ccd.Camera           = (*asiCCDSource)(nil)
	_ ccd.GainController   = (*asiCCDSource)(nil)
	_ ccd.OffsetController = (*asiCCDSource)(nil)
	_ ccd.Subframer        = (*asiCCDSource)(nil)
	_ liveCamera           = (*astrocamINDI)(nil)
)
