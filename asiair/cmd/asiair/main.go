// Command asiair serves the ZWO ASIAIR power board as a standalone ASCOM Alpaca
// Switch: four ports (two dimmable), DSLR shutter, per-port telemetry.
//
// The binary is devicemain.Run over the registered driver: every flag
// (-config, -port, one per config key, -discovery, -check, -schema), the
// setup form, persistence, and discovery come from the library. The driver
// package supplies the hardware knowledge and registers itself on import.
package main

import (
	_ "github.com/mikefsq/goalpaca-devices/asiair" // registers the driver
	"github.com/mikefsq/goalpaca/devicemain"
)

func main() { devicemain.Run("asiair-switch") }
