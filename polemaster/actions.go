package driver

import (
	"strconv"
	"strings"

	alpacadev "github.com/mikefsq/goalpaca/server"
)

// Measured with the stock fixed optics by plate solving a PoleMaster frame.
// This calibration is a scale hint, not a field distortion model.
const pixelScaleArcsec = 31.0276

// SupportedActions advertises angular scale separately from PixelSizeX/Y (microns).
func (p *PoleMaster) SupportedActions() []string { return []string{"PixelScale"} }

// Action returns the stock optical scale in arcseconds per pixel on either axis.
// Readout depth and subframe cropping do not change it; binning is fixed at 1x1.
func (p *PoleMaster) Action(name, params string) (string, error) {
	if !strings.EqualFold(name, "PixelScale") {
		return p.BaseCamera.Action(name, params)
	}
	if !p.hwPresent.Load() {
		return "", alpacadev.ErrNotConnected
	}
	if strings.TrimSpace(params) != "" {
		return "", alpacadev.ErrInvalidValue
	}
	return strconv.FormatFloat(pixelScaleArcsec, 'f', -1, 64), nil
}
