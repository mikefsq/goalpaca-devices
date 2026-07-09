package driver

import (
	_ "github.com/mikefsq/astrocam/sensors" // registers the PID -> sensor profile table

	"github.com/mikefsq/goalpaca/registry"
	alpacadev "github.com/mikefsq/goalpaca/server"
	"github.com/mikefsq/goindi/ccd"
)

// init registers this driver in the goalpaca driver registry, so a composed
// host (alpacahurd) can construct it from a config entry by importing this
// package. The blank sensors import above makes every decoded sensor profile
// available to any host that compiles this driver in.
func init() {
	registry.Register(registry.Driver{
		Name:          "asicam",
		Type:          alpacadev.CameraType,
		Description:   "ZWO ASI camera (pure-Go USB driver)",
		ConfigExample: `{ "driver": "asicam", "serial": "1a2b3c4d", "name": "Main camera" }`,
		New: func(spec registry.Spec) (alpacadev.Device, error) {
			var cfg struct {
				Index      int    `json:"index,omitempty"`
				Serial     string `json:"serial,omitempty"`
				FixDefects bool   `json:"fixdefects,omitempty"`
			}
			if err := spec.Decode(&cfg); err != nil {
				return nil, err
			}
			d := NewPureASICamera(cfg.Index, cfg.Serial)
			d.SetFixDefects(cfg.FixDefects) // "fixdefects": true → factory hot-pixel correction
			if spec.Name != "" {
				d.DevName = spec.Name
			}
			// Wrap so the camera can also serve the INDI CCD front-end (guide camera)
			// when the host enables it ("indi": true).
			return newINDICamera(d), nil
		},
	})
}

// ccdSource adapts the camera to ccd.Camera (the INDI CCD frame source). RAW
// frames pass through untouched.
type ccdSource struct{ c *PureASICamera }

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

// The camera's gain, offset, and subframe ROI, exposed so the INDI CCD device
// advertises CCD_CONTROLS and CCD_FRAME.
func (s *ccdSource) Gain() (int, int, int) { return s.c.Gain(), s.c.GainMin(), s.c.GainMax() }
func (s *ccdSource) SetGain(n int) error   { return s.c.SetGain(n) }
func (s *ccdSource) Offset() (int, int, int) {
	return s.c.Offset(), s.c.OffsetMin(), s.c.OffsetMax()
}
func (s *ccdSource) SetOffset(n int) error { return s.c.SetOffset(n) }
func (s *ccdSource) Subframe() (int, int, int, int) {
	return s.c.StartX(), s.c.StartY(), s.c.NumX(), s.c.NumY()
}
func (s *ccdSource) SetSubframe(x, y, w, h int) error {
	if err := s.c.SetStartX(x); err != nil {
		return err
	}
	if err := s.c.SetStartY(y); err != nil {
		return err
	}
	if err := s.c.SetNumX(w); err != nil {
		return err
	}
	return s.c.SetNumY(h)
}

// indiCamera adds the LiveCamera seam to the Alpaca camera, so a host can serve
// it over INDI as a guide camera. The driver is embedded (Alpaca behaviour
// unchanged); LiveCamera gates on a live hardware connection so the INDI CCD
// device drives the camera only once it is acquired.
type indiCamera struct {
	*PureASICamera
	src ccd.Camera
}

func newINDICamera(c *PureASICamera) *indiCamera {
	return &indiCamera{PureASICamera: c, src: &ccdSource{c: c}}
}

func (a *indiCamera) LiveCamera() (ccd.Camera, error) {
	if !a.Connected() {
		return nil, alpacadev.ErrNotConnected
	}
	return a.src, nil
}

var (
	_ ccd.Camera           = (*ccdSource)(nil)
	_ ccd.GainController   = (*ccdSource)(nil)
	_ ccd.OffsetController = (*ccdSource)(nil)
	_ ccd.Subframer        = (*ccdSource)(nil)
)
