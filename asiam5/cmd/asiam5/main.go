// Command asiam5 serves a ZWO AM-series harmonic mount (AM3/AM5/AM5N/AM7) as a
// standalone ASCOM Alpaca Telescope, over the lx200/am5 library.
//
// The binary is devicemain.Run over the registered driver: every flag
// (-config, -port, one per config key, -discovery, -check, -schema), the
// setup form, persistence, and discovery come from the library. The driver
// package supplies the hardware knowledge and registers itself on import.
package main

import (
	_ "github.com/mikefsq/goalpaca-devices/asiam5" // registers the driver
	"github.com/mikefsq/goalpaca/devicemain"
)

func main() { devicemain.Run("asiam5") }
