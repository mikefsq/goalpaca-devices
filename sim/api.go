package driver

import alpacadev "github.com/mikefsq/goalpaca/server"

// NewMount constructs the simulated mount: an Alpaca telescope coupled to the shared
// guide sky, with LiveMount for the INDI/LX200 front-ends. For hosts that embed
// drivers by direct constructor call rather than through the registry.
func NewMount(name string) alpacadev.Device { return newSimMount(name) }

// NewCamera constructs the simulated guide camera coupled to the same sky, with
// LiveCamera for the INDI CCD front-end. focalLenMM and pixelSizeX/Y (µm) set the
// arcsec→pixel scale of the guide field; sizeX/sizeY set the sensor pixel count. Any
// value <= 0 uses a default (short guide-scope focal length; the sim sensor's pixel
// size, square; the sim sensor's 1936×1096 dimensions).
func NewCamera(name string, focalLenMM, pixelSizeX, pixelSizeY float64, sizeX, sizeY int) alpacadev.Device {
	return newSimCamera(name, focalLenMM, pixelSizeX, pixelSizeY, sizeX, sizeY)
}
