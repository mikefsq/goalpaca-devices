// Command asieaf serves a ZWO EAF focuser as a standalone ASCOM Alpaca Focuser,
// over the pure-Go goasi/eaf driver.
//
// The binary is devicemain.Run over the registered driver: every flag
// (-config, -port, one per config key, -discovery, -check, -schema), the
// setup form, persistence, and discovery come from the library. The driver
// package supplies the hardware knowledge and registers itself on import.
package main

import (
	_ "github.com/mikefsq/goalpaca-devices/asieaf" // registers the driver
	"github.com/mikefsq/goalpaca/devicemain"
)

func main() { devicemain.Run("asieaf") }
