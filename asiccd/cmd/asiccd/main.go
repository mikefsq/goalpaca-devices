// Command asiccd serves a ZWO ASI camera as a standalone ASCOM Alpaca Camera,
// over the goasi/ccd SDK wrapper (cgo; needs the ZWO SDK).
//
// The binary is devicemain.Run over the registered driver: every flag
// (-config, -port, one per config key, -discovery, -check, -schema), the
// setup form, persistence, and discovery come from the library. The driver
// package supplies the hardware knowledge and registers itself on import.
package main

import (
	_ "github.com/mikefsq/goalpaca-devices/asiccd" // registers the driver
	"github.com/mikefsq/goalpaca/devicemain"
)

func main() { devicemain.Run("asiccd") }
