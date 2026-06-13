package main

import (
	alpacadev "github.com/mikefsq/goalpaca/server"
	"github.com/mikefsq/goalpaca/sim"
	"github.com/mikefsq/goindi/ccd"
)

// simCamera is the Alpaca camera simulator (goalpaca/sim) with a LiveCamera seam, so
// the SAME simulated camera is exposed over Alpaca AND drives the fleet's INDI CCD
// device — letting PHD2 connect a guide camera over INDI with no hardware. It reuses
// the generic ccdSource adapter (the sim camera satisfies alpacaCamera).
type simCamera struct {
	*sim.Camera
	src ccd.Camera
}

func newSimCamera(name string) *simCamera {
	c := sim.NewCamera()
	if name != "" {
		c.DevName = name
	}
	// Render a drifting star coupled to the sim mount (closed guide loop), not the
	// sim's gradient — so PHD2 can actually calibrate and guide against the sim.
	return &simCamera{Camera: c, src: &starCamSource{c: c}}
}

// ImageFrame overrides the embedded sim camera's gradient on the ALPACA path with the
// same drifting star the INDI front-end shows (coupled to simSky). The star is placed
// at its full-frame position and cropped to the current ROI, so PHD2 can find,
// calibrate, and guide on it whether it reads full frames or subframes over Alpaca.
// Without this override the Alpaca camera serves sim.Camera's plain gradient.
func (s *simCamera) ImageFrame() (alpacadev.ImageFrame, error) {
	f, err := s.Camera.ImageFrame() // honors ROI; gives geometry, element types, readiness
	if err != nil {
		return f, err
	}
	ox, oy := simSky.position()
	cx := float64(s.Camera.CameraXSize())/2 + ox - float64(s.Camera.StartX())
	cy := float64(s.Camera.CameraYSize())/2 + oy - float64(s.Camera.StartY())
	f.Pixels = renderStar(f.Width, f.Height, cx, cy)
	return f, nil
}

// LiveCamera exposes the simulator as a ccd.Camera for the INDI CCD device.
func (s *simCamera) LiveCamera() (ccd.Camera, error) { return s.src, nil }

var _ liveCamera = (*simCamera)(nil)
